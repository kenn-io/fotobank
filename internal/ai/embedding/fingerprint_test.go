package embedding_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/ai/embedding"
)

// TestFingerprint_FromConfig pins the canonical mapping from
// ai.EmbedConfig to ai.Fingerprint that the importer enqueue, the
// worker, and the gap scanner all rely on. Critically:
//
//   - PromptVersion is empty (the embed task has no prompt).
//   - InputProfile encodes the configured input edge so a config swap
//     produces a distinct fingerprint and a clean re-embed pass.
func TestFingerprint_FromConfig(t *testing.T) {
	r := require.New(t)
	fp := embedding.Fingerprint(ai.EmbedConfig{Model: "siglip2", InputEdge: 384})
	r.Equal(ai.Fingerprint{
		ModelID:       "siglip2",
		PromptVersion: "",
		InputProfile:  "jpeg-384-q85-metadata-stripped-embed-v1",
	}, fp)
	// Round-tripping through Fingerprint.String must produce the
	// canonical form the worker's parseFingerprint reverses, with the
	// blank middle segment preserved.
	r.Equal("siglip2||jpeg-384-q85-metadata-stripped-embed-v1", fp.String())
}

// TestFingerprint_DifferentEdgeProducesDifferentProfile pins the
// invariant that a model-config edge change yields a fresh fingerprint.
// Without this, an operator who swaps cfg.AI.Embed.InputEdge would keep
// hitting the same generation row and the panel would never reveal the
// drift.
func TestFingerprint_DifferentEdgeProducesDifferentProfile(t *testing.T) {
	r := require.New(t)
	a := embedding.Fingerprint(ai.EmbedConfig{Model: "m", InputEdge: 256})
	b := embedding.Fingerprint(ai.EmbedConfig{Model: "m", InputEdge: 384})
	r.NotEqual(a.InputProfile, b.InputProfile)
}

// TestEdgeFromInputProfile_AcceptsCanonical pins that a well-formed
// profile string round-trips through the parser unchanged.
func TestEdgeFromInputProfile_AcceptsCanonical(t *testing.T) {
	r := require.New(t)
	edge, err := embedding.EdgeFromInputProfile("jpeg-384-q85-metadata-stripped-embed-v1")
	r.NoError(err)
	r.Equal(384, edge)

	edge, err = embedding.EdgeFromInputProfile("jpeg-256-q85-metadata-stripped-embed-v1")
	r.NoError(err)
	r.Equal(256, edge)
}

// TestEdgeFromInputProfile_RejectsAnchorViolations is the regression
// test for the anchor fix: prior to the regexp parse, fmt.Sscanf
// silently accepted "v10" (and any other trailing garbage) as if it
// were v1, which would route a future profile bump through the
// current pipeline. The new parser must reject these strictly.
func TestEdgeFromInputProfile_RejectsAnchorViolations(t *testing.T) {
	r := require.New(t)
	bad := []string{
		// Trailing garbage — the legacy Sscanf parse accepted these.
		"jpeg-384-q85-metadata-stripped-embed-v10",
		"jpeg-384-q85-metadata-stripped-embed-v1-extra",
		"jpeg-384-q85-metadata-stripped-embed-v1\n",
		// Leading garbage — must match from the start.
		" jpeg-384-q85-metadata-stripped-embed-v1",
		"prefix-jpeg-384-q85-metadata-stripped-embed-v1",
		// Wrong shape entirely.
		"jpeg-384-embed",
		"",
		"random",
		// Edge digit missing or zero.
		"jpeg--q85-metadata-stripped-embed-v1",
		"jpeg-0-q85-metadata-stripped-embed-v1",
	}
	for _, s := range bad {
		_, err := embedding.EdgeFromInputProfile(s)
		r.Errorf(err, "input %q must be rejected", s)
	}
}
