package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

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
//
// Event IDs are assigned per-principal under bus.mu so a subscriber
// observes a strictly contiguous monotonic stream for its principal.
// This keeps the Last-Event-ID gap check (lastID < oldestID-1) sound
// when many principals are emitting concurrently.
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
// nextID is the next ID to hand out for this principal — kept here
// (rather than as a global atomic) so subscribers see strictly
// contiguous IDs even when other principals emit concurrently.
type ring struct {
	events []Event
	next   int
	subs   []chan Event
	nextID int64
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
//
// Tests are the primary callers of this form (they pre-seed events
// with explicit IDs); production emit helpers use PublishAutoID.
func (b *EventBus) Publish(p owners.Principal, ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.publishLocked(p, ev)
	if ev.ID > b.bufs[p].nextID {
		b.bufs[p].nextID = ev.ID
	}
}

// PublishAutoID assigns a per-principal monotonic ID to a new event
// inside the bus lock and publishes it. Returns the assigned ID. This
// is the only emission path that production helpers should use — it
// guarantees that subscribers observe events in the same order their
// IDs were allocated, which is what the Last-Event-ID resume contract
// depends on.
func (b *EventBus) PublishAutoID(p owners.Principal, evType string, data json.RawMessage) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.bufs[p]
	if r == nil {
		r = &ring{events: make([]Event, 0, b.bufLen)}
		b.bufs[p] = r
	}
	r.nextID++
	ev := Event{ID: r.nextID, Type: evType, Data: data}
	b.publishLocked(p, ev)
	return ev.ID
}

// publishLocked is the shared body used by Publish and PublishAutoID.
// The caller must hold b.mu and must have already initialised the
// principal's ring (PublishAutoID does this; Publish lets it happen
// via the nil-check below for backward compatibility with tests).
func (b *EventBus) publishLocked(p owners.Principal, ev Event) {
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
//
// subscribe is retained for tests that want to exercise the registration
// path without snapshotting the ring; the production handler uses
// subscribeFrom so the snapshot and registration happen atomically.
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
	return ch, b.makeUnsub(p, ch)
}

// makeUnsub returns the cancellation closure used by subscribe and
// subscribeFrom. Both code paths must remove ch from the ring's subs
// list under b.mu and close the channel exactly once; sharing the
// closure keeps that invariant in one place.
func (b *EventBus) makeUnsub(p owners.Principal, ch chan Event) func() {
	return func() {
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
// replayAfter is retained for tests that want to inspect the ring
// independent of subscription; the production handler uses
// subscribeFrom because a separate replay+subscribe pair leaves a
// window where Publish'd events are lost (neither replayed nor
// delivered live).
func (b *EventBus) replayAfter(p owners.Principal, lastID int64) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	events, _ := snapshotLocked(b.bufs[p], b.bufLen, lastID)
	return events
}

// snapshotLocked returns the events in r whose ID is greater than
// lastID (in monotonic order) along with the oldest retained ID
// currently in the ring. The caller must hold b.mu.
//
// Once the ring has wrapped, the underlying slice is logically rotated
// — the oldest entry sits at index r.next, not index 0 — so iteration
// must start there to preserve chronological order. Pre-wraparound,
// r.next is still 0 and the loop walks the slice in the natural
// append order.
//
// When the ring is empty (or r is nil), oldestID is 0 so callers can
// distinguish "no events ever" from "events exist".
func snapshotLocked(r *ring, bufLen int, lastID int64) (events []Event, oldestID int64) {
	if r == nil {
		return nil, 0
	}
	n := len(r.events)
	if n == 0 {
		return nil, 0
	}
	start := 0
	if n == bufLen {
		start = r.next
	}
	oldestID = r.events[start].ID
	out := make([]Event, 0, n)
	for i := range n {
		ev := r.events[(start+i)%n]
		if ev.ID > lastID {
			out = append(out, ev)
		}
	}
	return out, oldestID
}

// subscribeFrom atomically captures the events with id > lastID and
// registers a live subscription so no events are lost in the seam
// between bootstrap and live delivery. The returned oldestID is the
// smallest event ID currently retained in the ring (0 when the ring
// is empty), which the handler uses to decide whether the client's
// Last-Event-ID predates the retained window.
func (b *EventBus) subscribeFrom(p owners.Principal, lastID int64) (
	replay []Event, oldestID int64, ch chan Event, unsub func(),
) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.bufs[p]
	if r == nil {
		r = &ring{events: make([]Event, 0, b.bufLen)}
		b.bufs[p] = r
	}
	replay, oldestID = snapshotLocked(r, b.bufLen, lastID)
	ch = make(chan Event, 16)
	r.subs = append(r.subs, ch)
	unsub = b.makeUnsub(p, ch)
	return replay, oldestID, ch, unsub
}

// eventsHandler returns the raw HTTP handler that serves
// GET /api/v1/events as a text/event-stream connection. The caller's
// Identity is read from the request context (placed there by
// WithMiddleware), so the handler must be registered behind the same
// middleware chain that powers the huma routes.
//
// On connect the handler emits a synthetic "hello" control event
// whose JSON data carries {"principal": "<userID>"}. If the client
// supplied a Last-Event-ID header, the handler either replays the
// buffered events newer than that ID or, when the client's ID predates
// the retained window (or the ring is empty), emits a "catchup-required"
// control event so the client knows to refetch state via REST.
//
// Control events are written without an SSE id field so EventSource
// does not advance the client's lastEventId past them. Clients track
// lastEventId only via writeSSE-emitted (domain) frames. After the
// bootstrap phase the handler subscribes to the per-principal channel
// and forwards each Event as a standard SSE frame until the request
// context is cancelled.
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

		// SSE connections sit idle between events for arbitrarily long
		// periods. Disable the per-connection write deadline so the
		// server's WriteTimeout (60s by default) doesn't silently kill
		// long-poll subscribers. NewResponseController returns a
		// non-nil controller for any ResponseWriter; SetWriteDeadline
		// returning ErrNotSupported is harmless on test transports.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

		hello, err := json.Marshal(map[string]any{"principal": caller.UserID})
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		writeControlSSE(w, "hello", hello)
		flusher.Flush()

		var lastID int64
		if v := r.Header.Get("Last-Event-ID"); v != "" {
			if n, perr := strconv.ParseInt(v, 10, 64); perr == nil && n > 0 {
				lastID = n
			}
		}

		// Atomic snapshot + subscribe under bus.mu so events Publish'd
		// between the snapshot and the subscription are never lost.
		replay, oldestID, ch, unsub := bus.subscribeFrom(caller, lastID)
		defer unsub()

		if lastID > 0 {
			// gap: the client's last-seen ID predates the retained
			// window (or the ring is empty), so events between lastID
			// and oldestID may have been evicted. Tell the client to
			// refetch state via REST instead of silently skipping
			// the gap.
			gap := oldestID == 0 || lastID < oldestID-1
			if gap {
				writeControlSSE(w, "catchup-required", json.RawMessage(`{}`))
			} else {
				for _, ev := range replay {
					writeSSE(w, ev)
				}
			}
			flusher.Flush()
		}

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

// writeSSE serialises a domain event with its monotonic id so the
// client's EventSource records it as the new Last-Event-ID. Network
// errors here are unrecoverable from the handler's vantage (the client
// has disconnected), so they are intentionally not propagated; the
// next flusher.Flush call or the next select on ctx.Done will pick
// the failure up and end the loop.
func writeSSE(w http.ResponseWriter, ev Event) {
	fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Type, string(ev.Data))
}

// writeControlSSE serialises a control event (hello, catchup-required)
// without an id field so EventSource does not advance the client's
// lastEventId past sentinel frames. Per the SSE spec, omitting the
// id field leaves lastEventId unchanged for the dispatched message.
func writeControlSSE(w http.ResponseWriter, eventType string, data json.RawMessage) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, string(data))
}

// AI event names emitted on the SSE wire. The worker fires
// EventNameAITagCompleted / EventNameAICaptionCompleted on terminal
// job state and EventNameAIHealthChanged when reachability or queue
// depth crosses a threshold (Q1 throttles the latter on the frontend).
const (
	EventNameAITagCompleted     = "ai.tag.completed"
	EventNameAICaptionCompleted = "ai.caption.completed"
	EventNameAIHealthChanged    = "ai.health.changed"
)

// AICompletedEvent is the payload for ai.tag.completed and
// ai.caption.completed. Status is "done" or "failed"; ResultID is
// populated only on success (the SPA uses it to fetch the new tags or
// caption row via REST).
type AICompletedEvent struct {
	MediaID       string `json:"media_id"`
	Task          string `json:"task"`
	Status        string `json:"status"`
	ModelID       string `json:"model_id"`
	PromptVersion string `json:"prompt_version"`
	InputProfile  string `json:"input_profile"`
	ResultID      string `json:"result_id,omitempty"`
}

// AIHealthChangedEvent is the payload for ai.health.changed. Vision
// reports gateway reachability; Tag and Caption carry per-task queue
// counters so the SPA can refresh the status dot without polling.
type AIHealthChangedEvent struct {
	Vision  AIHealthVisionDelta `json:"vision"`
	Tag     AIHealthTaskDelta   `json:"tag"`
	Caption AIHealthTaskDelta   `json:"caption"`
}

// AIHealthVisionDelta is the vision-gateway slice of an
// ai.health.changed payload.
type AIHealthVisionDelta struct {
	Reachable bool   `json:"reachable"`
	LastError string `json:"last_error,omitempty"`
}

// AIHealthTaskDelta is the per-task slice of an ai.health.changed
// payload (Pending = queued+running; FailedActive = failures inside
// the active retry window).
type AIHealthTaskDelta struct {
	Pending      int `json:"pending"`
	FailedActive int `json:"failed_active"`
}
