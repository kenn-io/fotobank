package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"

	"github.com/wesm/fotobank/internal/owners"
)

// EventBus fans server-sent events out to per-principal subscribers and
// keeps a small ring buffer per principal so reconnecting clients can
// resume via Last-Event-ID. F1 emits only the synthetic "hello" event
// on connect; later sub-plans (F3 in particular) publish domain events
// such as ai.tag.completed and import.progress through Publish.
//
// The buffer is in-memory only; a process restart loses every queued
// event. Clients that reconnect with a Last-Event-ID newer than the
// buffer's oldest entry get the missed events; clients whose
// Last-Event-ID predates the buffer receive a synthetic
// "catchup-required" event so they know to refetch state via REST.
type EventBus struct {
	mu     sync.Mutex
	bufs   map[owners.Principal]*ring
	bufLen int
}

// Event is a single SSE payload. ID is monotonic per principal (the
// caller is responsible for assigning IDs); Type is the event name
// emitted on the wire; Data is the JSON payload sent verbatim.
type Event struct {
	ID   int64           `json:"id"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// ring is a per-principal slot: the buffered events plus the active
// subscriber channels. When events fills to bufLen, next is the
// circular write index. subs is appended/sliced under EventBus.mu.
type ring struct {
	events []Event
	next   int
	subs   []chan Event
}

const eventBusBufLen = 256

// NewEventBus constructs an empty EventBus with the default 256-event
// per-principal ring buffer.
func NewEventBus() *EventBus {
	return NewEventBusWithSize(eventBusBufLen)
}

// NewEventBusWithSize constructs an empty EventBus with a per-principal
// ring buffer of the given length. Production callers should use
// NewEventBus; this constructor is exported so tests can shrink the ring
// to exercise wraparound cheaply, and so a future caller can tune the
// buffer size without forking the constructor.
func NewEventBusWithSize(bufLen int) *EventBus {
	return &EventBus{bufs: map[owners.Principal]*ring{}, bufLen: bufLen}
}

// Publish appends ev to the per-principal ring buffer and pushes it to
// every active subscriber channel for that principal. Sends to a full
// subscriber channel are dropped silently — the slow subscriber will
// see "catchup-required" the next time it reconnects with a stale
// Last-Event-ID, so the missed event is recoverable via REST.
func (b *EventBus) Publish(p owners.Principal, ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.bufs[p]
	if r == nil {
		r = &ring{events: make([]Event, 0, b.bufLen)}
		b.bufs[p] = r
	}
	if len(r.events) < b.bufLen {
		r.events = append(r.events, ev)
	} else {
		r.events[r.next] = ev
		r.next = (r.next + 1) % b.bufLen
	}
	for _, ch := range r.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// subscribe registers a new subscriber channel for principal p and
// returns the channel plus a cancellation function that removes the
// subscriber and closes the channel. The channel is buffered so a
// brief stall in the consumer does not block the publisher.
func (b *EventBus) subscribe(p owners.Principal) (chan Event, func()) {
	ch := make(chan Event, 16)
	b.mu.Lock()
	r := b.bufs[p]
	if r == nil {
		r = &ring{events: make([]Event, 0, b.bufLen)}
		b.bufs[p] = r
	}
	r.subs = append(r.subs, ch)
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		r := b.bufs[p]
		if r == nil {
			return
		}
		for i, c := range r.subs {
			if c == ch {
				r.subs = append(r.subs[:i], r.subs[i+1:]...)
				close(ch)
				return
			}
		}
	}
}

// replayAfter returns every buffered event for p whose ID is strictly
// greater than lastID, in monotonic ID order. The returned slice is a
// fresh copy so callers may iterate without holding b.mu.
//
// Once the ring has wrapped, the underlying slice is logically rotated
// — the oldest entry sits at index r.next, not index 0 — so iteration
// must start there to preserve chronological order. Pre-wraparound,
// r.next is still 0 and the loop walks the slice in the natural
// append order.
func (b *EventBus) replayAfter(p owners.Principal, lastID int64) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.bufs[p]
	if r == nil {
		return nil
	}
	n := len(r.events)
	out := make([]Event, 0, n)
	start := 0
	if n == b.bufLen {
		start = r.next
	}
	for i := range n {
		ev := r.events[(start+i)%n]
		if ev.ID > lastID {
			out = append(out, ev)
		}
	}
	return out
}

// eventsHandler returns the raw HTTP handler that serves
// GET /api/v1/events as a text/event-stream connection. The caller's
// Identity is read from the request context (placed there by
// WithMiddleware), so the handler must be registered behind the same
// middleware chain that powers the huma routes.
//
// On connect the handler emits a synthetic "hello" event (id=0) whose
// JSON data carries {"principal": "<userID>"}. If the client supplied
// a Last-Event-ID header, the handler either replays the buffered
// events newer than that ID or, if none are available, emits a
// "catchup-required" event (also id=0) so the client knows to refetch
// state via REST. After the bootstrap phase the handler subscribes to
// the per-principal channel and forwards each Event as a standard SSE
// frame until the request context is cancelled.
//
// id=0 is reserved for control events (hello, catchup-required).
// Clients MUST NOT record control event IDs as their Last-Event-ID
// high-water mark; doing so would cause the server to either re-replay
// the same events or, in the catchup-required case, loop forever
// reissuing the control event on every reconnect.
func eventsHandler(bus *EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := IdentityFromContext(r.Context())
		if !ok {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		caller := id.Principal.OwnersPrincipal()
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		hello, err := json.Marshal(map[string]any{"principal": caller.UserID})
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		writeSSE(w, Event{ID: 0, Type: "hello", Data: hello})
		flusher.Flush()

		var lastID int64
		if v := r.Header.Get("Last-Event-ID"); v != "" {
			if n, perr := strconv.ParseInt(v, 10, 64); perr == nil && n > 0 {
				lastID = n
			}
		}
		if lastID > 0 {
			missed := bus.replayAfter(caller, lastID)
			if len(missed) == 0 {
				// id=0 marks this as a control event; see eventsHandler doc.
				writeSSE(w, Event{ID: 0, Type: "catchup-required", Data: json.RawMessage(`{}`)})
			} else {
				for _, ev := range missed {
					writeSSE(w, ev)
				}
			}
			flusher.Flush()
		}

		ch, unsub := bus.subscribe(caller)
		defer unsub()
		for {
			select {
			case <-r.Context().Done():
				return
			case ev, open := <-ch:
				if !open {
					return
				}
				writeSSE(w, ev)
				flusher.Flush()
			}
		}
	}
}

// writeSSE serialises ev as a single SSE frame on w. Network errors
// here are unrecoverable from the handler's vantage (the client has
// disconnected), so they are intentionally not propagated; the next
// flusher.Flush call or the next select on ctx.Done will pick the
// failure up and end the loop.
func writeSSE(w http.ResponseWriter, ev Event) {
	fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Type, string(ev.Data))
}
