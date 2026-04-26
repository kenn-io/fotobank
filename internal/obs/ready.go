package obs

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Ready is the boolean readiness gate flipped by graceful shutdown.
// Once false, every /readyz probe returns 503 immediately, bypassing
// the deep-check cache so scrapers see the transition before the main
// listener stops accepting requests.
type Ready struct {
	v atomic.Bool
}

// NewReady returns a Ready gate initialized to true. Production code
// flips it to false on graceful shutdown so /readyz starts failing
// before the main listener stops accepting requests.
func NewReady() *Ready {
	r := &Ready{}
	r.v.Store(true)
	return r
}

// Store updates the readiness state.
func (r *Ready) Store(ok bool) { r.v.Store(ok) }

// Load reports the current readiness state.
func (r *Ready) Load() bool { return r.v.Load() }

// ReadyCheck is a named probe. Fn must return an error to signal
// failure and respect ctx for cancellation.
type ReadyCheck struct {
	Name string
	Fn   func(ctx context.Context) error
}

// ReadyzConfig tunes the deadline and cache TTL. Production uses 2s/5s;
// unit tests use 20-50ms / 0.
type ReadyzConfig struct {
	DeadlineTotal time.Duration
	CacheTTL      time.Duration
}

type readyzResult struct {
	StatusOK bool
	At       time.Time
	Body     []byte
}

type readyzHandler struct {
	ready  *Ready
	checks []ReadyCheck
	cfg    ReadyzConfig

	mu       sync.Mutex
	cache    *readyzResult
	inflight chan struct{} // nil except while a single check pass is running
	pending  []chan readyzResult
}

// NewReadyzHandler builds a /readyz HTTP handler. The cfg deadline
// caps the total time the check pass may take; the cache TTL deduplicates
// successive probes. Concurrent cache-miss probes share a single check
// pass via singleflight.
func NewReadyzHandler(ready *Ready, checks []ReadyCheck, cfg ReadyzConfig) http.Handler {
	return &readyzHandler{ready: ready, checks: checks, cfg: cfg}
}

func (h *readyzHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.ready.Load() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"shutting_down","checks":[]}`))
		return
	}
	res := h.runOrCache(r.Context())
	w.Header().Set("Content-Type", "application/json")
	if res.StatusOK {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_, _ = w.Write(res.Body)
}

// runOrCache returns a fresh result, either from cache or by running
// the check pass once across concurrent probes.
func (h *readyzHandler) runOrCache(ctx context.Context) readyzResult {
	h.mu.Lock()
	if h.cache != nil && time.Since(h.cache.At) < h.cfg.CacheTTL {
		c := *h.cache
		h.mu.Unlock()
		return c
	}
	if h.inflight != nil {
		// Another goroutine is running the check pass; park on a fresh
		// channel until the leader broadcasts the result.
		ch := make(chan readyzResult, 1)
		h.pending = append(h.pending, ch)
		h.mu.Unlock()
		return <-ch
	}
	// We're the leader for this pass.
	h.inflight = make(chan struct{})
	leader := h.inflight
	h.mu.Unlock()

	res := h.runChecks(ctx)

	h.mu.Lock()
	h.cache = &res
	pending := h.pending
	h.pending = nil
	h.inflight = nil
	close(leader)
	h.mu.Unlock()

	for _, ch := range pending {
		ch <- res
	}
	return res
}

func (h *readyzHandler) runChecks(parent context.Context) readyzResult {
	ctx, cancel := context.WithTimeout(parent, h.cfg.DeadlineTotal)
	defer cancel()

	type checkOut struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Err    string `json:"err,omitempty"`
	}
	out := make([]checkOut, 0, len(h.checks))
	allOK := true
	for _, c := range h.checks {
		err := c.Fn(ctx)
		entry := checkOut{Name: c.Name, Status: "ok"}
		if err != nil {
			allOK = false
			entry.Status = "fail"
			entry.Err = err.Error()
		}
		out = append(out, entry)
	}
	body := struct {
		Status string     `json:"status"`
		Checks []checkOut `json:"checks"`
	}{
		Status: "ok",
		Checks: out,
	}
	if !allOK {
		body.Status = "fail"
	}
	bodyBytes, _ := json.Marshal(body)
	return readyzResult{StatusOK: allOK, At: time.Now(), Body: bodyBytes}
}
