package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/owners"
)

// TestEmitAICompletedRoutesByTask confirms the helper picks the right
// event name based on ev.Task. Tag and caption are the only tasks the
// worker emits today; an unknown task falls through to the tag name
// rather than producing an unmapped event the SPA cannot render.
func TestEmitAICompletedRoutesByTask(t *testing.T) {
	r := require.New(t)
	bus := NewEventBus()
	p := owners.Principal{Hub: "local", UserID: "alice"}

	bus.EmitAICompleted(p, AICompletedEvent{
		MediaID: "m1", Task: "tag", Status: "done",
		ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip",
	})
	bus.EmitAICompleted(p, AICompletedEvent{
		MediaID: "m1", Task: "caption", Status: "done",
		ModelID: "m", PromptVersion: "cap-v1", InputProfile: "ip",
	})

	got := bus.replayAfter(p, 0)
	r.Len(got, 2)
	r.Equal(EventNameAITagCompleted, got[0].Type)
	r.Equal(EventNameAICaptionCompleted, got[1].Type)
	r.Greater(got[1].ID, got[0].ID, "IDs must be monotonic")

	// The wire payload must round-trip back into AICompletedEvent so
	// downstream consumers (CLI, SPA) can decode the same struct the
	// helper marshalled.
	var decoded AICompletedEvent
	r.NoError(json.Unmarshal(got[0].Data, &decoded))
	r.Equal("tag", decoded.Task)
	r.Equal("m1", decoded.MediaID)
}

// TestEmitAIHealthChangedFlowsThroughBus exercises the second emit
// helper end-to-end: the bus must record the canonical event name and
// the payload must decode back into AIHealthChangedEvent.
func TestEmitAIHealthChangedFlowsThroughBus(t *testing.T) {
	r := require.New(t)
	bus := NewEventBus()
	p := owners.Principal{Hub: "local", UserID: "alice"}

	bus.EmitAIHealthChanged(p, AIHealthChangedEvent{
		Vision: AIHealthVisionDelta{Reachable: false, LastError: "i/o timeout"},
		Tag:    AIHealthTaskDelta{Pending: 3, FailedActive: 2},
	})

	got := bus.replayAfter(p, 0)
	r.Len(got, 1)
	r.Equal(EventNameAIHealthChanged, got[0].Type)

	var decoded AIHealthChangedEvent
	r.NoError(json.Unmarshal(got[0].Data, &decoded))
	r.False(decoded.Vision.Reachable)
	r.Equal("i/o timeout", decoded.Vision.LastError)
	r.Equal(3, decoded.Tag.Pending)
	r.Equal(2, decoded.Tag.FailedActive)
}
