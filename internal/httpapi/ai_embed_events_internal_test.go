package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai/embedding"
	"go.kenn.io/fotobank/internal/owners"
)

// Compile-time check: AIEmbedEvents satisfies embedding.EventEmitter.
// If the embedding-side interface or the adapter method set drifts,
// this assertion catches it before the test body runs.
var _ embedding.EventEmitter = (*AIEmbedEvents)(nil)

// TestAIEmbedEvents_RoutesAllFiveLifecycleEvents exercises every
// adapter method against a single bound principal and asserts that
// each fires the matching SSE event with the canonical payload shape.
// The adapter is the bridge between the embedding package's principal-
// free EventEmitter contract and the per-principal EventBus, so this
// test is the primary regression guard against either side drifting.
//
// Lives in the internal test file so the assertions can use
// replayAfter (which is package-private — every existing emit-helper
// test does the same thing).
func TestAIEmbedEvents_RoutesAllFiveLifecycleEvents(t *testing.T) {
	r := require.New(t)
	bus := NewEventBus()
	p := owners.Principal{Hub: "local", UserID: "alice"}
	ev := NewAIEmbedEvents(bus, p)

	const fp = "siglip2||jpeg-384-q85-metadata-stripped-embed-v1"
	ev.EmitAIEmbedCompleted("m-completed", fp)
	ev.EmitAIEmbedFailed("m-failed", fp, "transient")
	ev.EmitAIEmbedGenerationCreated(11, fp)
	ev.EmitAIEmbedGenerationActivated(12, fp)
	ev.EmitAIEmbedGenerationRetired(13, fp)

	got := bus.replayAfter(p, 0)
	r.Len(got, 5, "five emit calls must produce five events on the principal's channel")

	// Pin event name + payload shape for each. ID strict-monotonicity
	// over the principal's channel is already exercised by
	// TestPublishAutoIDIsMonotonicPerPrincipal — here we only need to
	// confirm the adapter delivered the right name + payload pairing.
	r.Equal(EventNameAIEmbedCompleted, got[0].Type)
	var completed AIEmbedCompletedEvent
	r.NoError(json.Unmarshal(got[0].Data, &completed))
	r.Equal("m-completed", completed.MediaID)
	r.Equal(fp, completed.Fingerprint)

	r.Equal(EventNameAIEmbedFailed, got[1].Type)
	var failed AIEmbedFailedEvent
	r.NoError(json.Unmarshal(got[1].Data, &failed))
	r.Equal("m-failed", failed.MediaID)
	r.Equal(fp, failed.Fingerprint)
	r.Equal("transient", failed.ErrorKind)

	r.Equal(EventNameAIEmbedGenerationCreated, got[2].Type)
	var created AIEmbedGenerationEvent
	r.NoError(json.Unmarshal(got[2].Data, &created))
	r.EqualValues(11, created.ID)

	r.Equal(EventNameAIEmbedGenerationActivated, got[3].Type)
	var activated AIEmbedGenerationEvent
	r.NoError(json.Unmarshal(got[3].Data, &activated))
	r.EqualValues(12, activated.ID)

	r.Equal(EventNameAIEmbedGenerationRetired, got[4].Type)
	var retired AIEmbedGenerationEvent
	r.NoError(json.Unmarshal(got[4].Data, &retired))
	r.EqualValues(13, retired.ID)
}
