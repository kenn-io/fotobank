package embedding

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/wesm/fotobank/internal/ai"
)

// embedProfileRE anchors the canonical embed input profile shape so the
// parse rejects trailing or leading garbage. Without anchors, Sscanf
// would accept "jpeg-384-q85-metadata-stripped-embed-v10" as
// edge=384 because it stops scanning at the first format mismatch —
// silently treating a bumped revision as the original v1 profile.
var embedProfileRE = regexp.MustCompile(`^jpeg-(\d+)-q85-metadata-stripped-embed-v1$`)

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

// EdgeFromInputProfile parses the edge size out of an embed input
// profile string of the form "jpeg-{N}-q85-metadata-stripped-embed-v1"
// (the canonical form Fingerprint emits). Returns an error when the
// profile string doesn't match — the worker uses the error to drive
// the malformed-fp branch, which marks the claim failed without
// writing an ai_failures row keyed on the corrupt triple.
//
// The worker needs this because ai_jobs rows carry their fingerprint
// verbatim. Mid-rollout, a claim under the prior fingerprint must be
// encoded to that fingerprint's edge — not to cfg.InputEdge — or the
// resulting vector lives under the wrong InputProfile and search-time
// queries that share the active fingerprint can't find it.
//
// The regex is anchored at both ends so a future profile bump (e.g.
// "embed-v10") is rejected here rather than silently treated as v1
// with a parsed edge — which would route a v10 vector through the v1
// pipeline and quietly corrupt the index.
func EdgeFromInputProfile(profile string) (int, error) {
	m := embedProfileRE.FindStringSubmatch(profile)
	if m == nil {
		return 0, fmt.Errorf("invalid embed input profile %q", profile)
	}
	edge, err := strconv.Atoi(m[1])
	if err != nil || edge <= 0 {
		return 0, fmt.Errorf("invalid edge in input profile %q", profile)
	}
	return edge, nil
}
