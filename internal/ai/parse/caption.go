package parse

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// MaxCaptionChars is the upper bound enforced on caption length.
const MaxCaptionChars = 300

type captionEnvelope struct {
	Caption string `json:"caption"`
}

// markdownPatterns are structural matches: any hit means the model
// emitted markdown despite the prompt. Bare apostrophes/hyphens are
// fine because they don't form constructs.
var markdownPatterns = []*regexp.Regexp{
	regexp.MustCompile("`"),                        // any backtick
	regexp.MustCompile(`\*\*[^*]+\*\*`),            // **bold**
	regexp.MustCompile(`\*[^*\s]([^*]*[^*\s])?\*`), // *emphasis*
	regexp.MustCompile(`(?m)^\s*#\s`),              // # heading
	regexp.MustCompile(`\[[^\]]+\]\([^)]+\)`),      // [text](url)
	regexp.MustCompile(`(?m)^\s*>\s`),              // > blockquote
	regexp.MustCompile(`\|[^|\n]+\|`),              // |table|
	regexp.MustCompile(`(?m)^\s*[-*]\s`),           // - or * list item
	regexp.MustCompile(`(?m)^\s*\d+\.\s`),          // 1. ordered list
	regexp.MustCompile(`_[^_\s]([^_]*[^_\s])?_`),   // _emphasis_
}

// Caption extracts the caption text and applies the validation pipeline.
// On any rejection, returns ErrMalformed wrapped with the reason.
func Caption(raw string) (string, error) {
	body := strings.TrimSpace(raw)
	if m := fencedJSON.FindStringSubmatch(body); m != nil {
		body = m[1]
	}
	var env captionEnvelope
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return "", fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	text := strings.TrimSpace(env.Caption)
	if text == "" {
		return "", fmt.Errorf("%w: empty caption", ErrMalformed)
	}
	if len([]rune(text)) > MaxCaptionChars {
		return "", fmt.Errorf("%w: caption exceeds %d chars", ErrMalformed, MaxCaptionChars)
	}
	for _, pat := range markdownPatterns {
		if pat.MatchString(text) {
			return "", fmt.Errorf("%w: caption contains markdown (%s)", ErrMalformed, pat.String())
		}
	}
	return text, nil
}
