package embedding_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/ai/embedding"
)

// vec returns a JSON array literal of length n with every entry set to v.
// Used by tests that need a server reply with a known dimension.
func vec(n int, v float32) string {
	s := strconv.FormatFloat(float64(v), 'g', -1, 32)
	parts := make([]string, n)
	for i := range parts {
		parts[i] = s
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func TestClient_BatchImagesReturnsVectorsByIndex(t *testing.T) {
	r := require.New(t)
	type observed struct {
		path  string
		input []string
		model string
		err   error
	}
	var seen observed
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seen.path = req.URL.Path
		var body struct {
			Input []string `json:"input"`
			Model string   `json:"model"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			seen.err = err
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		seen.input = body.Input
		seen.model = body.Model
		// Reply with two distinct 768-dim vectors keyed by index.
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w,
			`{"data":[{"embedding":`+vec(768, 0.1)+`,"index":0},`+
				`{"embedding":`+vec(768, 0.2)+`,"index":1}],"model":"siglip2"}`)
	}))
	defer srv.Close()

	c := embedding.NewClient(embedding.Config{
		Parts: (ai.EmbedConfig{
			Endpoint:  srv.URL + "/v1",
			Model:     "siglip2",
			Dimension: 768,
			Timeout:   5 * time.Second,
		}).EmbeddingParts(),
	})

	out, err := c.EmbedImages(context.Background(), "", 0, [][]byte{[]byte("a-bytes"), []byte("b-bytes")})
	r.NoError(err)
	r.NoError(seen.err)
	r.Equal("/v1/embeddings", seen.path)
	r.Len(seen.input, 2)
	r.Equal("siglip2", seen.model)
	r.Len(out, 2)
	r.Len(out[0], 768)
	r.Len(out[1], 768)
	r.NotEqual(out[0], out[1])
}

// TestClient_ReordersOutOfOrderResponse verifies that the client
// reorders the returned vectors to match the input order regardless of
// how the server emitted them. The mock server replies with index 2,
// then 0, then 1 — if the client trusted server order without sorting
// by `index`, the asserted positional alignment below would fail.
func TestClient_ReordersOutOfOrderResponse(t *testing.T) {
	r := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Three inputs were sent (indexes 0..2). Reply with them in
		// reverse-ish order so trusting the response order would put
		// the wrong vector at out[0].
		_, _ = io.WriteString(w,
			`{"data":[`+
				`{"embedding":`+vec(768, 0.3)+`,"index":2},`+
				`{"embedding":`+vec(768, 0.1)+`,"index":0},`+
				`{"embedding":`+vec(768, 0.2)+`,"index":1}`+
				`],"model":"siglip2"}`)
	}))
	defer srv.Close()

	c := embedding.NewClient(embedding.Config{
		Parts: (ai.EmbedConfig{
			Endpoint:  srv.URL + "/v1",
			Model:     "siglip2",
			Dimension: 768,
			Timeout:   5 * time.Second,
		}).EmbeddingParts(),
	})
	out, err := c.EmbedImages(context.Background(), "", 0,
		[][]byte{[]byte("a"), []byte("b"), []byte("c")})
	r.NoError(err)
	r.Len(out, 3)
	// out[i] must be the vector originally tagged index=i. The
	// deterministic per-position float values make a swap detectable.
	r.InDelta(0.1, float64(out[0][0]), 1e-6, "out[0] must be the index=0 vector")
	r.InDelta(0.2, float64(out[1][0]), 1e-6, "out[1] must be the index=1 vector")
	r.InDelta(0.3, float64(out[2][0]), 1e-6, "out[2] must be the index=2 vector")
}

func TestClient_RejectsDimensionMismatchAsMalformed(t *testing.T) {
	r := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"embedding":[0.1,0.2,0.3],"index":0}],"model":"siglip2"}`)
	}))
	defer srv.Close()

	c := embedding.NewClient(embedding.Config{
		Parts: (ai.EmbedConfig{
			Endpoint:  srv.URL + "/v1",
			Model:     "siglip2",
			Dimension: 768,
			Timeout:   5 * time.Second,
		}).EmbeddingParts(),
	})
	_, err := c.EmbedImages(context.Background(), "", 0, [][]byte{[]byte("x")})
	r.ErrorIs(err, embedding.ErrMalformed)
}

func TestClient_4xxIsPermanent(t *testing.T) {
	r := require.New(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, `{"error":"bad model"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	c := embedding.NewClient(embedding.Config{
		Parts: (ai.EmbedConfig{
			Endpoint:  srv.URL + "/v1",
			Model:     "x",
			Dimension: 768,
			Timeout:   5 * time.Second,
		}).EmbeddingParts(),
		MaxRetries: 3,
	})
	_, err := c.EmbedImages(context.Background(), "", 0, [][]byte{[]byte("x")})
	r.ErrorIs(err, embedding.ErrProvider4xx)
	r.EqualValues(1, hits.Load(), "no retries on 4xx")
}

func TestClient_5xxRetriedAndEventuallyTransient(t *testing.T) {
	r := require.New(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := embedding.NewClient(embedding.Config{
		Parts: (ai.EmbedConfig{
			Endpoint:  srv.URL + "/v1",
			Model:     "x",
			Dimension: 768,
			Timeout:   5 * time.Second,
		}).EmbeddingParts(),
		MaxRetries: 1,
	})
	_, err := c.EmbedImages(context.Background(), "", 0, [][]byte{[]byte("x")})
	r.ErrorIs(err, embedding.ErrTransient)
	r.GreaterOrEqual(hits.Load(), int32(2), "must retry once on 5xx")
}

// TestClient_ContextCancelDoesNotWrapAsTransient verifies that a
// pre-cancelled context surfaces context.Canceled, not ErrTransient.
// Wrapping the cancel as transient would make a deliberate shutdown
// indistinguishable from a 5xx and cause callers to retry instead of
// quietly stopping.
func TestClient_ContextCancelDoesNotWrapAsTransient(t *testing.T) {
	r := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Slow handler — should not be reached if cancellation works.
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(w, `{"data":[{"embedding":`+vec(768, 0.1)+`,"index":0}],"model":"m"}`)
	}))
	defer srv.Close()

	c := embedding.NewClient(embedding.Config{
		Parts: (ai.EmbedConfig{
			Endpoint:  srv.URL + "/v1",
			Model:     "m",
			Dimension: 768,
			Timeout:   5 * time.Second,
		}).EmbeddingParts(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.EmbedImages(ctx, "", 0, [][]byte{[]byte("x")})
	r.Error(err)
	r.ErrorIs(err, context.Canceled, "got %v", err)
	r.NotErrorIs(err, embedding.ErrTransient, "must not be wrapped as transient")
}

// TestClient_ContextCanceledDuringBodyReadDoesNotWrapAsTransient
// covers the body-read window: the server flushes a status header
// (so http.Do returns success) then stalls during body emission.
// The client's per-call context expires inside io.ReadAll, parse
// classifies the truncated read as transient, but the post-parse
// ctx.Err() check must convert that into a raw context error so
// callers can detect the deadline cleanly.
func TestClient_ContextCanceledDuringBodyReadDoesNotWrapAsTransient(t *testing.T) {
	r := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Flush headers + a partial body, then stall long enough that
		// the per-call deadline fires before the body finishes. The
		// flush ensures http.Do returns success and parse is reached.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			_, _ = io.WriteString(w, `{"data":[`)
			f.Flush()
		}
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	c := embedding.NewClient(embedding.Config{
		Parts: (ai.EmbedConfig{
			Endpoint:  srv.URL + "/v1",
			Model:     "m",
			Dimension: 768,
			Timeout:   5 * time.Second,
		}).EmbeddingParts(),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.EmbedImages(ctx, "", 0, [][]byte{[]byte("x")})
	r.Error(err)
	r.ErrorIs(err, context.DeadlineExceeded, "got %v", err)
	r.NotErrorIs(err, embedding.ErrTransient, "must not be wrapped as transient")
}

// TestClient_NonZeroDimensionOverridesConfig pins the per-call
// dimension routing contract. The client validates response vector
// length against the supplied dimension argument when it is > 0,
// falling back to cfg.Dimension only when the caller passed 0.
//
// The worker's per-claim dispatch passes gen.Dimension on every
// EmbedImages call so a stale-fp claim under a generation built at
// dim=512 still validates against 512 regardless of the worker's
// currently-configured dim. This client-level test pins the
// validation contract that makes that work: a 512-arg call with a
// 512-vec response succeeds; a 768-arg (cfg-fallback) call with a
// 512-vec response is rejected as malformed.
func TestClient_NonZeroDimensionOverridesConfig(t *testing.T) {
	r := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w,
			`{"data":[{"embedding":`+vec(512, 0.1)+`,"index":0}],"model":"siglip2"}`)
	}))
	defer srv.Close()

	c := embedding.NewClient(embedding.Config{
		Parts: (ai.EmbedConfig{
			Endpoint:  srv.URL + "/v1",
			Model:     "siglip2",
			Dimension: 768,
			Timeout:   5 * time.Second,
		}).EmbeddingParts(),
	})

	// Per-call dim=512 with a 512-vec response: passes validation.
	out, err := c.EmbedImages(context.Background(), "siglip2", 512, [][]byte{[]byte("a")})
	r.NoError(err, "explicit per-call dim=512 must validate against 512-vec response")
	r.Len(out, 1)
	r.Len(out[0], 512)

	// Per-call dim=0 falls back to cfg.Dimension=768. The 512-vec
	// response now mismatches the expected 768 — surfaces as
	// ErrMalformed.
	_, err = c.EmbedImages(context.Background(), "siglip2", 0, [][]byte{[]byte("a")})
	r.ErrorIs(err, embedding.ErrMalformed,
		"dim=0 falls back to cfg.Dimension=768; 512-vec response must be malformed")
}

// TestClient_DeadlineExceededDoesNotWrapAsTransient is the deadline twin
// of the cancel test: a tiny per-call deadline that fires mid-flight
// returns context.DeadlineExceeded, not ErrTransient.
func TestClient_DeadlineExceededDoesNotWrapAsTransient(t *testing.T) {
	r := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = io.WriteString(w, `{"data":[{"embedding":`+vec(768, 0.1)+`,"index":0}],"model":"m"}`)
	}))
	defer srv.Close()

	c := embedding.NewClient(embedding.Config{
		Parts: (ai.EmbedConfig{
			Endpoint:  srv.URL + "/v1",
			Model:     "m",
			Dimension: 768,
			Timeout:   5 * time.Second,
		}).EmbeddingParts(),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	_, err := c.EmbedImages(ctx, "", 0, [][]byte{[]byte("x")})
	r.Error(err)
	r.ErrorIs(err, context.DeadlineExceeded, "got %v", err)
	r.NotErrorIs(err, embedding.ErrTransient, "must not be wrapped as transient")
}

func TestClient_TextAndImageUseSameSpace(t *testing.T) {
	r := require.New(t)
	requests := make(chan map[string]json.RawMessage, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(req.Body).Decode(&body); !assert.NoError(t, err) {
			w.WriteHeader(400)
			return
		}
		requests <- body
		assert.Equal(t, "Bearer synthetic-key", req.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[3,4]}]}`)
	}))
	t.Cleanup(srv.Close)
	c := embedding.NewClient(embedding.Config{Parts: (ai.EmbedConfig{
		Endpoint: srv.URL, Model: "next-model", Dimension: 3,
	}).EmbeddingParts(), APIKey: "synthetic-key"})
	images, err := c.EmbedImages(t.Context(), "active-model", 2, [][]byte{{1, 2, 3}})
	r.NoError(err)
	text, err := c.EmbedTexts(t.Context(), "active-model", 2, []string{"a dog on a beach"})
	r.NoError(err)
	r.Equal([][]float32{{3, 4}}, text, "must not normalize query vectors against existing image vectors")
	r.Equal(images, text)
	r.Len(requests, 2)
	received := []map[string]json.RawMessage{<-requests, <-requests}
	for _, request := range received {
		r.JSONEq(`"active-model"`, string(request["model"]))
		r.NotContains(request, "input_type")
		r.NotContains(request, "dimensions")
	}
	r.JSONEq(`["a dog on a beach"]`, string(received[1]["input"]))
}

func TestClient_TextRetryPolicy(t *testing.T) {
	for _, tc := range []struct {
		name             string
		status, attempts int
		want             error
	}{
		{"rate limited", 429, 3, embedding.ErrTransient},
		{"server failure", 503, 3, embedding.ErrTransient},
		{"request timeout stays permanent", 408, 1, embedding.ErrProvider4xx},
		{"credentials", 401, 1, embedding.ErrProvider4xx},
		{"malformed", 200, 1, embedding.ErrMalformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				// The old policy retries immediately, even when a provider sends this.
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{}`)
			}))
			t.Cleanup(srv.Close)
			c := embedding.NewClient(embedding.Config{Parts: (ai.EmbedConfig{Endpoint: srv.URL, Model: "m", Dimension: 2}).EmbeddingParts(), MaxRetries: 2})
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			_, err := c.EmbedTexts(ctx, "", 0, []string{"dog"})
			r.ErrorIs(err, tc.want)
			r.EqualValues(tc.attempts, calls.Load())
		})
	}
}

func TestClient_TextCancellationDuringBodyRead(t *testing.T) {
	r := require.New(t)
	started := make(chan struct{})
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-req.Context().Done()
	}))
	t.Cleanup(srv.Close)
	c := embedding.NewClient(embedding.Config{Parts: (ai.EmbedConfig{Endpoint: srv.URL, Model: "m", Dimension: 2}).EmbeddingParts(), MaxRetries: 2})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.EmbedTexts(ctx, "", 0, []string{"dog"}); done <- err }()
	<-started
	cancel()
	err := <-done
	r.ErrorIs(err, context.Canceled)
	r.NotErrorIs(err, embedding.ErrTransient)
	r.EqualValues(1, calls.Load())
}
