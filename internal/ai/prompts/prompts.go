// Package prompts owns the LLM prompts used by fotobank's AI workers.
// Each prompt is a Go const; bumping a prompt requires bumping the
// version string. The CI test pins the SHA-256 of every const so an
// edit without a version bump fails the build.
package prompts

import (
	"crypto/sha256"
	"encoding/hex"
)

// Prompt bundles a prompt string with its stable version identifier and
// content hash. Workers persist Version on every ai_results row;
// ai_results.prompt_hash is filled from Hash so accidental drift is
// detectable post-hoc as well as in CI.
type Prompt struct {
	Version string
	Text    string
	Hash    string
}

// tagV1 asks the VLM for a JSON array of free-text tags.
const tagV1 = `Look at this photograph and emit a JSON object describing what is visible.

Output strictly valid JSON in this shape (no markdown, no commentary):
{"tags": ["...", "..."]}

Rules:
- Up to 10 tags, ordered most salient first.
- Free-text English; no scores; no sentences.
- Describe subjects, scenes, settings, mood, weather, time of day.
- Do NOT name specific people or invent place names.
- Do NOT include camera/lens metadata.
- If the photo is empty or you cannot describe it, return {"tags": []}.`

// captionV1 asks for a 1–3 sentence neutral description.
const captionV1 = `Look at this photograph and emit a JSON object with a brief plain-text description.

Output strictly valid JSON in this shape (no markdown, no commentary):
{"caption": "..."}

Rules:
- 1 to 3 sentences, at most 300 characters.
- English; descriptive third-person ("A small dog stands on a beach…").
- No first-person ("I see").
- No markdown, no headings, no bullets, no links, no code.
- Do NOT name specific people or invent place names.
- If the photo is empty or you cannot describe it, return {"caption": ""}.`

// Tag returns the active tag prompt.
func Tag() Prompt { return mk("tags-v1", tagV1) }

// Caption returns the active caption prompt.
func Caption() Prompt { return mk("caption-v1", captionV1) }

func mk(version, text string) Prompt {
	sum := sha256.Sum256([]byte(text))
	return Prompt{Version: version, Text: text, Hash: hex.EncodeToString(sum[:])}
}
