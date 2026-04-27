package httpapi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/owners"
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
