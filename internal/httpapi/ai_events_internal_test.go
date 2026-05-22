package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/owners"
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

// embedTestPrincipal returns the canonical principal used by the
// ai.embed.* emit-helper tests. Pulled out so the assertions stay
// uniform across the five sibling tests.
func embedTestPrincipal() owners.Principal {
	return owners.Principal{Hub: "local", UserID: "alice"}
}

// TestEmitAIEmbedCompleted exercises the worker's per-claim
// completion emit: the helper must publish under EventNameAIEmbedCompleted
// and the payload must round-trip back into AIEmbedCompletedEvent so
// listeners decode the same shape the worker marshalled.
func TestEmitAIEmbedCompleted(t *testing.T) {
	r := require.New(t)
	bus := NewEventBus()
	p := embedTestPrincipal()

	bus.EmitAIEmbedCompleted(p, AIEmbedCompletedEvent{
		MediaID:     "m1",
		Fingerprint: "siglip2||jpeg-384-q85-metadata-stripped-embed-v1",
	})

	got := bus.replayAfter(p, 0)
	r.Len(got, 1)
	r.Equal(EventNameAIEmbedCompleted, got[0].Type)

	var decoded AIEmbedCompletedEvent
	r.NoError(json.Unmarshal(got[0].Data, &decoded))
	r.Equal("m1", decoded.MediaID)
	r.Equal("siglip2||jpeg-384-q85-metadata-stripped-embed-v1", decoded.Fingerprint)
}

// TestEmitAIEmbedFailed exercises the worker's per-claim failure emit:
// the helper must publish under EventNameAIEmbedFailed and the payload
// must carry the (media, fingerprint, error_kind) triple — the SPA
// uses error_kind to colour-code the failure without re-deriving from
// the message.
func TestEmitAIEmbedFailed(t *testing.T) {
	r := require.New(t)
	bus := NewEventBus()
	p := embedTestPrincipal()

	bus.EmitAIEmbedFailed(p, AIEmbedFailedEvent{
		MediaID:     "m1",
		Fingerprint: "siglip2||jpeg-384-q85-metadata-stripped-embed-v1",
		ErrorKind:   "transient",
	})

	got := bus.replayAfter(p, 0)
	r.Len(got, 1)
	r.Equal(EventNameAIEmbedFailed, got[0].Type)

	var decoded AIEmbedFailedEvent
	r.NoError(json.Unmarshal(got[0].Data, &decoded))
	r.Equal("m1", decoded.MediaID)
	r.Equal("siglip2||jpeg-384-q85-metadata-stripped-embed-v1", decoded.Fingerprint)
	r.Equal("transient", decoded.ErrorKind)
}

// TestEmitAIEmbedGenerationCreated exercises the registry's
// FindOrCreateBuilding insert-path emit. The payload carries (id,
// fingerprint) so listeners can route by either key — the CLI prefers
// the id, the SPA prefers the fingerprint.
func TestEmitAIEmbedGenerationCreated(t *testing.T) {
	r := require.New(t)
	bus := NewEventBus()
	p := embedTestPrincipal()

	bus.EmitAIEmbedGenerationCreated(p, AIEmbedGenerationEvent{
		ID:          7,
		Fingerprint: "siglip2||jpeg-384-q85-metadata-stripped-embed-v1",
	})

	got := bus.replayAfter(p, 0)
	r.Len(got, 1)
	r.Equal(EventNameAIEmbedGenerationCreated, got[0].Type)

	var decoded AIEmbedGenerationEvent
	r.NoError(json.Unmarshal(got[0].Data, &decoded))
	r.EqualValues(7, decoded.ID)
	r.Equal("siglip2||jpeg-384-q85-metadata-stripped-embed-v1", decoded.Fingerprint)
}

// TestEmitAIEmbedGenerationActivated exercises the activator's
// promote-to-active emit. Payload mirrors the created event so the
// SPA can render the lifecycle as a single timeline.
func TestEmitAIEmbedGenerationActivated(t *testing.T) {
	r := require.New(t)
	bus := NewEventBus()
	p := embedTestPrincipal()

	bus.EmitAIEmbedGenerationActivated(p, AIEmbedGenerationEvent{
		ID:          7,
		Fingerprint: "siglip2||jpeg-384-q85-metadata-stripped-embed-v1",
	})

	got := bus.replayAfter(p, 0)
	r.Len(got, 1)
	r.Equal(EventNameAIEmbedGenerationActivated, got[0].Type)

	var decoded AIEmbedGenerationEvent
	r.NoError(json.Unmarshal(got[0].Data, &decoded))
	r.EqualValues(7, decoded.ID)
	r.Equal("siglip2||jpeg-384-q85-metadata-stripped-embed-v1", decoded.Fingerprint)
}

// TestEmitAIEmbedGenerationRetired exercises the registry's
// retire-on-promote emit. Only fires when the retire-prior-active
// UPDATE actually changed a row — the bus-side helper itself doesn't
// gate on that (the gating lives in Generations.Promote /
// PromoteFromBuilding); this test pins the wire shape and routing.
func TestEmitAIEmbedGenerationRetired(t *testing.T) {
	r := require.New(t)
	bus := NewEventBus()
	p := embedTestPrincipal()

	bus.EmitAIEmbedGenerationRetired(p, AIEmbedGenerationEvent{
		ID:          6,
		Fingerprint: "siglip2|prev|jpeg-384-q85-metadata-stripped-embed-v1",
	})

	got := bus.replayAfter(p, 0)
	r.Len(got, 1)
	r.Equal(EventNameAIEmbedGenerationRetired, got[0].Type)

	var decoded AIEmbedGenerationEvent
	r.NoError(json.Unmarshal(got[0].Data, &decoded))
	r.EqualValues(6, decoded.ID)
	r.Equal("siglip2|prev|jpeg-384-q85-metadata-stripped-embed-v1", decoded.Fingerprint)
}
