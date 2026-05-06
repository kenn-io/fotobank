package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/embedding"
	"github.com/wesm/fotobank/internal/ai/imginput"
	aiprompts "github.com/wesm/fotobank/internal/ai/prompts"
)

// ClaimFingerprints are queue-safety identities stored in
// ai_jobs.fingerprint.
type ClaimFingerprints struct {
	Tag     string
	Caption string
	Embed   string
}

// ResultFingerprints are provenance identities stored on result and
// generation tables.
type ResultFingerprints struct {
	Tag     ai.Fingerprint
	Caption ai.Fingerprint
	Embed   ai.Fingerprint
}

func deriveFingerprints(cfg ai.Config) (ClaimFingerprints, ResultFingerprints) {
	tagPrompt := aiprompts.Tag()
	captionPrompt := aiprompts.Caption()
	result := ResultFingerprints{
		Tag: ai.Fingerprint{
			ModelID:       cfg.Tag.Model,
			PromptVersion: tagPrompt.Version,
			InputProfile:  imginput.ProfileV1,
		},
		Caption: ai.Fingerprint{
			ModelID:       cfg.Caption.Model,
			PromptVersion: captionPrompt.Version,
			InputProfile:  imginput.ProfileV1,
		},
		Embed: embedding.Fingerprint(cfg.Embed),
	}
	claim := ClaimFingerprints{
		Tag: claimFingerprint(map[string]any{
			"task":            string(ai.TaskTag),
			"ai_enabled":      cfg.Enabled,
			"endpoint":        cfg.Vision.Endpoint,
			"api_key_env":     cfg.Vision.APIKeyEnv,
			"task_enabled":    cfg.Tag.Enabled,
			"model":           cfg.Tag.Model,
			"prompt_version":  tagPrompt.Version,
			"prompt_hash":     tagPrompt.Hash,
			"input_profile":   imginput.ProfileV1,
			"fingerprint_rev": 1,
		}),
		Caption: claimFingerprint(map[string]any{
			"task":            string(ai.TaskCaption),
			"ai_enabled":      cfg.Enabled,
			"endpoint":        cfg.Vision.Endpoint,
			"api_key_env":     cfg.Vision.APIKeyEnv,
			"task_enabled":    cfg.Caption.Enabled,
			"model":           cfg.Caption.Model,
			"prompt_version":  captionPrompt.Version,
			"prompt_hash":     captionPrompt.Hash,
			"input_profile":   imginput.ProfileV1,
			"fingerprint_rev": 1,
		}),
		Embed: claimFingerprint(map[string]any{
			"task":            string(ai.TaskEmbed),
			"task_enabled":    cfg.Embed.Enabled,
			"endpoint":        cfg.Embed.Endpoint,
			"api_key_env":     cfg.Embed.APIKeyEnv,
			"model":           cfg.Embed.Model,
			"dimension":       cfg.Embed.Dimension,
			"input_edge":      cfg.Embed.InputEdge,
			"fingerprint_rev": 1,
		}),
	}
	return claim, result
}

func claimFingerprint(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
