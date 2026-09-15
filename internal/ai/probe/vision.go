package probe

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"fmt"
)

type visionRequest struct {
	Model       string          `json:"model"`
	Messages    []visionMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens"`
	Temperature int             `json:"temperature"`
}

type visionMessage struct {
	Role    string              `json:"role"`
	Content []visionContentPart `json:"content"`
}

type visionContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *visionImageURL `json:"image_url,omitempty"`
}

type visionImageURL struct {
	URL string `json:"url"`
}

type visionResponse struct {
	Model   *string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Vision posts a multimodal chat-completions request using pending
// form values and classifies the response.
func Vision(ctx context.Context, cfg VisionConfig) Result {
	apiKey, authErr := resolveAPIKey(cfg.APIKeyEnv)
	if authErr != nil {
		return *authErr
	}
	dataURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(tinyJPEG)
	body := visionRequest{
		Model: cfg.Model,
		Messages: []visionMessage{{
			Role: "user",
			Content: []visionContentPart{
				{Type: "text", Text: "Reply with the single word 'ok'."},
				{Type: "image_url", ImageURL: &visionImageURL{URL: dataURL}},
			},
		}},
		MaxTokens:   8,
		Temperature: 0,
	}
	resp, latency, err := postJSON(ctx, cfg.Endpoint, "/chat/completions", apiKey, body)
	if err != nil {
		return classifyTransportError(err, latency)
	}
	defer func() { _ = resp.Body.Close() }()
	if status, ok := classifyStatus(resp, latency); ok {
		return status
	}

	var decoded visionResponse
	if err := json.UnmarshalRead(resp.Body, &decoded); err != nil {
		return malformed(latency, "decode response: "+err.Error())
	}
	if len(decoded.Choices) == 0 {
		return malformed(latency, "response missing choices[0].message")
	}
	result := okResult(latency, "vision endpoint executed test request")
	result.ModelEchoed = decoded.Model
	if decoded.Model == nil {
		result.Warnings = append(result.Warnings, "vision endpoint did not echo the configured model name")
	} else if *decoded.Model != cfg.Model {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("vision endpoint echoed model %q instead of configured model %q", *decoded.Model, cfg.Model))
	}
	return result
}
