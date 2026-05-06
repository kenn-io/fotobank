package probe

import (
	"context"
	"encoding/json"
	"fmt"
)

type embedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embedResponse struct {
	Model *string `json:"model"`
	Data  []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed posts one portable text input to /v1/embeddings and verifies
// the configured vector dimension at data[0].embedding.
func Embed(ctx context.Context, cfg EmbedConfig) Result {
	apiKey, authErr := resolveAPIKey(cfg.APIKeyEnv)
	if authErr != nil {
		return *authErr
	}
	resp, latency, err := postJSON(ctx, cfg.Endpoint, "/embeddings", apiKey, embedRequest{
		Model: cfg.Model,
		Input: "test",
	})
	if err != nil {
		return classifyTransportError(err, latency)
	}
	defer func() { _ = resp.Body.Close() }()
	if status, ok := classifyStatus(resp, latency); ok {
		return status
	}

	var decoded embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return malformed(latency, "decode response: "+err.Error())
	}
	if len(decoded.Data) == 0 {
		return malformed(latency, "response missing data[0].embedding")
	}
	gotDim := len(decoded.Data[0].Embedding)
	if gotDim != cfg.Dimension {
		return Result{
			OK:             false,
			LatencyMS:      latency.Milliseconds(),
			Classification: ClassDimensionMismatch,
			Detail: fmt.Sprintf(
				"model returned %d dimensions at data[0].embedding; configured %d",
				gotDim, cfg.Dimension,
			),
			ModelEchoed: decoded.Model,
		}
	}
	result := okResult(latency, "embedding endpoint returned configured dimension")
	result.ModelEchoed = decoded.Model
	return result
}
