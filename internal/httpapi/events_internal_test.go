package httpapi

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/owners"
)

// TestEventBusReplayAfterWraparound exercises the bug fixed alongside
// these tests: once the ring has wrapped, the oldest entry sits at
// r.next rather than index 0, so a naive walk over r.events returns
// events out of monotonic ID order. The test publishes more events
// than fit in the ring and asserts the replay is monotonic, the two
// oldest entries have been evicted, and the most recent entry is last.
func TestEventBusReplayAfterWraparound(t *testing.T) {
	r := require.New(t)
	bus := NewEventBusWithSize(4)
	p := owners.Principal{Hub: "local", UserID: "alice"}

	for i := int64(1); i <= 6; i++ {
		bus.Publish(p, Event{ID: i, Type: "test", Data: json.RawMessage(`{}`)})
	}

	got := bus.replayAfter(p, 0)
	r.Len(got, 4)
	for i := 1; i < len(got); i++ {
		r.Greater(got[i].ID, got[i-1].ID, "events should be in monotonic ID order")
	}
	r.Equal(int64(3), got[0].ID, "oldest surviving event after eviction should be ID 3")
	r.Equal(int64(6), got[3].ID, "newest event should be last")
}

// TestEventBusReplayAfterFiltersByID confirms replayAfter only returns
// events whose ID is strictly greater than the supplied watermark.
func TestEventBusReplayAfterFiltersByID(t *testing.T) {
	r := require.New(t)
	bus := NewEventBusWithSize(4)
	p := owners.Principal{Hub: "local", UserID: "alice"}

	for i := int64(1); i <= 4; i++ {
		bus.Publish(p, Event{ID: i, Type: "test", Data: json.RawMessage(`{}`)})
	}
	got := bus.replayAfter(p, 2)
	r.Len(got, 2)
	r.Equal(int64(3), got[0].ID)
	r.Equal(int64(4), got[1].ID)
}

// TestEventBusSlowSubscriberDoesNotBlockPublisher confirms that a
// subscriber that never drains its channel cannot stall Publish. The
// subscriber channel has capacity 16; we publish 100 events and require
// the publisher loop to complete promptly because select-default in
// Publish drops sends to a full channel.
func TestEventBusSlowSubscriberDoesNotBlockPublisher(t *testing.T) {
	r := require.New(t)
	bus := NewEventBusWithSize(256)
	p := owners.Principal{Hub: "local", UserID: "alice"}

	_, unsub := bus.subscribe(p)
	defer unsub()

	done := make(chan struct{})
	go func() {
		for i := int64(1); i <= 100; i++ {
			bus.Publish(p, Event{ID: i, Type: "test", Data: json.RawMessage(`{}`)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		r.FailNow("Publish blocked on slow subscriber")
	}
}

// TestEventBusSubscribeFromReportsOldestID exercises the gap-detection
// path: after the ring has wrapped, a client whose Last-Event-ID
// predates the retained window must observe the oldestID returned by
// subscribeFrom so the handler can emit catchup-required instead of
// silently skipping the evicted range.
func TestEventBusSubscribeFromReportsOldestID(t *testing.T) {
	r := require.New(t)
	bus := NewEventBusWithSize(4)
	p := owners.Principal{Hub: "local", UserID: "alice"}

	// Publish 6 events into a 4-slot ring; IDs 1 and 2 are evicted,
	// IDs 3..6 survive. The oldest retained ID is therefore 3.
	for i := int64(1); i <= 6; i++ {
		bus.Publish(p, Event{ID: i, Type: "test", Data: json.RawMessage(`{}`)})
	}

	// Client claims it last saw ID 1 (evicted). subscribeFrom should
	// report oldestID=3 so the handler detects lastID < oldestID-1
	// and emits catchup-required.
	replay, oldestID, ch, unsub := bus.subscribeFrom(p, 1)
	defer unsub()
	r.NotNil(ch)
	r.Equal(int64(3), oldestID, "oldest retained ID after wraparound should be 3")
	r.Len(replay, 4, "replay covers 4 surviving events even though gap is detected")

	// Verify the handler-side gap check would fire: lastID=1 < oldestID-1=2.
	r.Less(int64(1), oldestID-1, "lastID=1 must be strictly less than oldestID-1 to trigger catchup-required")
}

// TestEventBusSubscribeFromEmptyRing covers the cold-start case: a
// client reconnects with Last-Event-ID set, but the server has nothing
// buffered. The handler relies on oldestID==0 to emit catchup-required
// rather than treating an empty replay as "fully caught up".
func TestEventBusSubscribeFromEmptyRing(t *testing.T) {
	r := require.New(t)
	bus := NewEventBusWithSize(4)
	p := owners.Principal{Hub: "local", UserID: "alice"}

	replay, oldestID, ch, unsub := bus.subscribeFrom(p, 5)
	defer unsub()
	r.NotNil(ch)
	r.Empty(replay, "no events to replay from an empty ring")
	r.Equal(int64(0), oldestID, "empty ring reports oldestID=0")
}

// TestEventBusPublishAutoIDOrderUnderConcurrency confirms PublishAutoID
// assigns IDs and appends to the ring under the same lock. Before the
// fix, NextID was called outside Publish's lock, so concurrent emitters
// could allocate IDs in one order and publish them in another — leaving
// subscribers with a non-monotonic stream that broke Last-Event-ID
// resume. The test fires N concurrent emits and asserts the buffered
// stream is strictly increasing and contiguous from 1..N.
func TestEventBusPublishAutoIDOrderUnderConcurrency(t *testing.T) {
	r := require.New(t)
	bus := NewEventBusWithSize(256)
	p := owners.Principal{Hub: "local", UserID: "alice"}

	const n = 200
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			bus.PublishAutoID(p, "test", json.RawMessage(`{}`))
		}()
	}
	wg.Wait()

	got := bus.replayAfter(p, 0)
	r.Len(got, n)
	for i := 1; i < len(got); i++ {
		r.Greaterf(got[i].ID, got[i-1].ID,
			"events must be in strictly increasing ID order; saw %d then %d",
			got[i-1].ID, got[i].ID)
	}
	for i, ev := range got {
		r.EqualValuesf(i+1, ev.ID, "event[%d] id mismatch", i)
	}
}

// TestEventBusPublishAutoIDPerPrincipalGapDetection confirms that
// per-principal contiguous IDs make the Last-Event-ID gap check
// (lastID < oldestID-1) sound when many principals emit concurrently.
// Before the fix, a global counter meant another principal's emits
// could create numeric gaps in Alice's stream that fired
// catchup-required even when nothing was actually evicted.
func TestEventBusPublishAutoIDPerPrincipalGapDetection(t *testing.T) {
	r := require.New(t)
	bus := NewEventBusWithSize(4)
	alice := owners.Principal{Hub: "local", UserID: "alice"}
	bob := owners.Principal{Hub: "local", UserID: "bob"}

	// Interleave Alice and Bob; Alice's IDs must remain 1..4 contiguous.
	bus.PublishAutoID(alice, "t", json.RawMessage(`{}`))
	bus.PublishAutoID(bob, "t", json.RawMessage(`{}`))
	bus.PublishAutoID(alice, "t", json.RawMessage(`{}`))
	bus.PublishAutoID(bob, "t", json.RawMessage(`{}`))
	bus.PublishAutoID(alice, "t", json.RawMessage(`{}`))
	bus.PublishAutoID(alice, "t", json.RawMessage(`{}`))

	// Alice's last seen ID is 3; nothing has been evicted from her ring
	// (only 4 events, ring size 4). Gap check should NOT fire.
	replay, oldestID, _, unsub := bus.subscribeFrom(alice, 3)
	defer unsub()
	r.Equal(int64(1), oldestID, "Alice's oldest retained ID is 1")
	r.Len(replay, 1, "only event 4 is newer than lastID=3")
	r.Equal(int64(4), replay[0].ID)
	// Per the handler logic: lastID < oldestID-1 → 3 < 0 → false → no catchup.
	r.GreaterOrEqual(int64(3), oldestID-1, "no catchup-required when nothing evicted")
}

// TestEventBusSubscribeFromAtomic confirms the snapshot-then-subscribe
// seam is closed: an event published concurrently with subscribeFrom
// must surface either via the replay slice or via the live channel,
// never disappear in the middle. Without the single-mu guarantee the
// previous handler had a window between replayAfter and subscribe
// where Publish'd events were lost. The test races a goroutine that
// fires N events against repeated subscribeFrom calls and asserts
// that for every iteration the union of replay+channel covers every
// published ID.
func TestEventBusSubscribeFromAtomic(t *testing.T) {
	r := require.New(t)
	bus := NewEventBusWithSize(256)
	p := owners.Principal{Hub: "local", UserID: "alice"}

	const iterations = 100
	for iter := range iterations {
		// Each iteration subscribes from the watermark of the
		// previous iteration's tail, then a goroutine publishes a
		// fresh event concurrently. Whether the event lands in
		// replay or arrives via channel depends on scheduling, but
		// it must NEVER be lost.
		baseID := int64(iter * 10)
		bus.Publish(p, Event{ID: baseID + 1, Type: "pre", Data: json.RawMessage(`{}`)})

		var wg sync.WaitGroup
		wg.Add(1)
		raceID := baseID + 2
		go func() {
			defer wg.Done()
			bus.Publish(p, Event{ID: raceID, Type: "race", Data: json.RawMessage(`{}`)})
		}()

		replay, _, ch, unsub := bus.subscribeFrom(p, baseID)

		// Drain channel for a short window; whichever path the race
		// event took, it must be observable.
		seen := map[int64]bool{}
		for _, ev := range replay {
			seen[ev.ID] = true
		}
		// Wait up to 250ms for the live event if it didn't land in
		// replay. The publisher goroutine is racing under bus.mu so
		// it must complete promptly once subscribeFrom releases.
		wg.Wait()
		deadline := time.After(250 * time.Millisecond)
	drain:
		for !seen[raceID] {
			select {
			case ev, open := <-ch:
				if !open {
					break drain
				}
				seen[ev.ID] = true
			case <-deadline:
				break drain
			}
		}
		unsub()

		r.Truef(seen[baseID+1], "iter %d: pre-existing event %d missing", iter, baseID+1)
		r.Truef(seen[raceID], "iter %d: raced event %d lost between snapshot and subscribe",
			iter, raceID)
	}
}
