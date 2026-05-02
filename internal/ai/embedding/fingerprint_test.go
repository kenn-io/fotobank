package embedding_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/embedding"
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
