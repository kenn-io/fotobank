package embedding

import (
	"fmt"

	"github.com/wesm/fotobank/internal/ai"
)

// Fingerprint returns the canonical (model, prompt, profile) triple for
// the embed task under cfg. PromptVersion is intentionally empty — the
// embed task has no prompt, and the gap scanner's failure-budget query
// hard-codes the empty string against ai_failures.prompt_version (see
// internal/ai/gapscanner/scanner.go::embedCandidates), so any deviation
// here would silently break gap-fill.
//
// The InputProfile string encodes both the JPEG re-encode parameters
// applied by encode.EncodeEmbed (q=85, metadata stripped) and the
// configured input edge length so a model swap that changes edge size
// produces a distinct fingerprint and triggers a clean re-embed pass.
func Fingerprint(cfg ai.EmbedConfig) ai.Fingerprint {
	return ai.Fingerprint{
		ModelID:       cfg.Model,
		PromptVersion: "",
		InputProfile:  fmt.Sprintf("jpeg-%d-q85-metadata-stripped-embed-v1", cfg.InputEdge),
	}
}
