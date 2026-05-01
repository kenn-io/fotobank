package httpapi_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/httpapi"
	"github.com/wesm/fotobank/internal/owners"
)

// TestAICompletedEventMarshal pins the JSON wire shape the SPA relies
// on (snake_case keys, omitempty on result_id). Renaming a field here
// silently breaks the frontend, so the test asserts on substring
// matches rather than just round-tripping back through Unmarshal.
func TestAICompletedEventMarshal(t *testing.T) {
	r := require.New(t)
	ev := httpapi.AICompletedEvent{
		MediaID:       "m1",
		Task:          "tag",
		Status:        "done",
		ModelID:       "qwen2.5-vl:3b",
		PromptVersion: "tags-v1",
		InputProfile:  "jpeg-1024-q85-metadata-stripped-v1",
		ResultID:      "r1",
	}
	b, err := json.Marshal(ev)
	r.NoError(err)
	s := string(b)
	r.Contains(s, `"media_id":"m1"`)
	r.Contains(s, `"task":"tag"`)
	r.Contains(s, `"status":"done"`)
	r.Contains(s, `"model_id":"qwen2.5-vl:3b"`)
	r.Contains(s, `"prompt_version":"tags-v1"`)
	r.Contains(s, `"input_profile":"jpeg-1024-q85-metadata-stripped-v1"`)
	r.Contains(s, `"result_id":"r1"`)
}

// TestAICompletedEventOmitsResultIDWhenEmpty confirms the omitempty
// tag holds: failure events carry no result row, so the SPA should
// not see "result_id":"" on the wire.
func TestAICompletedEventOmitsResultIDWhenEmpty(t *testing.T) {
	r := require.New(t)
	ev := httpapi.AICompletedEvent{
		MediaID: "m1", Task: "tag", Status: "failed",
		ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip",
	}
	b, err := json.Marshal(ev)
	r.NoError(err)
	r.NotContains(string(b), "result_id")
}

// TestAIHealthChangedEventMarshal pins the health-delta wire shape.
// LastError is omitempty (no key on the happy path); Pending and
// FailedActive are not (the SPA needs to see zero values to clear
// stale counters).
func TestAIHealthChangedEventMarshal(t *testing.T) {
	r := require.New(t)
	ev := httpapi.AIHealthChangedEvent{
		Vision:  httpapi.AIHealthVisionDelta{Reachable: true},
		Tag:     httpapi.AIHealthTaskDelta{Pending: 5, FailedActive: 1},
		Caption: httpapi.AIHealthTaskDelta{Pending: 0, FailedActive: 0},
	}
	b, err := json.Marshal(ev)
	r.NoError(err)
	s := string(b)
	r.Contains(s, `"reachable":true`)
	r.NotContains(s, "last_error")
	r.Contains(s, `"pending":5`)
	r.Contains(s, `"failed_active":1`)
	r.Contains(s, `"pending":0`)
}

// TestPublishAutoIDIsMonotonicPerPrincipal confirms PublishAutoID
// hands out strictly increasing per-principal IDs. The Last-Event-ID
// resume contract depends on this: a subscriber must see a contiguous
// monotonic stream for its own principal even when other principals
// emit concurrently.
func TestPublishAutoIDIsMonotonicPerPrincipal(t *testing.T) {
	r := require.New(t)
	bus := httpapi.NewEventBus()
	alice := owners.Principal{Hub: "local", UserID: "alice"}
	bob := owners.Principal{Hub: "local", UserID: "bob"}

	const n = 100
	var prev int64
	for range n {
		id := bus.PublishAutoID(alice, "test", json.RawMessage(`{}`))
		r.Greater(id, prev, "Alice's IDs must be strictly increasing")
		prev = id
	}
	// Bob's stream is independent — his first ID is 1, not 101.
	bobID := bus.PublishAutoID(bob, "test", json.RawMessage(`{}`))
	r.EqualValues(1, bobID, "Bob's per-principal counter starts fresh")
}
