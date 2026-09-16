package client

import (
	"context"

	"go.kenn.io/fotobank/internal/client/generated"
	"go.kenn.io/fotobank/internal/httpapi"
	aiservice "go.kenn.io/fotobank/internal/service/ai"
)

func AIHealth(ctx context.Context, configPath, version string) (aiservice.Health, error) {
	var out aiservice.Health
	err := call(ctx, configPath, version, &out, "retry AI status", func(c *generated.Client) (*generated.AiHealthResponse, error) {
		return c.AiHealth(ctx)
	})
	return out, err
}

func AcknowledgeAI(ctx context.Context, configPath, version string, input httpapi.AIAcknowledgeRequest) error {
	return call(ctx, configPath, version, nil, "inspect AI status before retrying acknowledgment", func(c *generated.Client) (*generated.AiAcknowledgeResponse, error) {
		return c.AiAcknowledge(ctx, &generated.AiAcknowledgeRequestOptions{Body: &input})
	})
}

func BackfillAI(ctx context.Context, configPath, version string, input httpapi.AIBackfillRequest) (httpapi.AIEnqueuedResult, error) {
	var out httpapi.AIEnqueuedResult
	err := call(ctx, configPath, version, &out, "inspect AI status before retrying backfill", func(c *generated.Client) (*generated.AiBackfillResponse, error) {
		return c.AiBackfill(ctx, &generated.AiBackfillRequestOptions{Body: &input})
	})
	return out, err
}

func RetryFailedAI(ctx context.Context, configPath, version string, input httpapi.AIRetryFailedRequest) (httpapi.AIEnqueuedResult, error) {
	var out httpapi.AIEnqueuedResult
	err := call(ctx, configPath, version, &out, "inspect AI status before retrying failed jobs", func(c *generated.Client) (*generated.AiRetryFailedResponse, error) {
		return c.AiRetryFailed(ctx, &generated.AiRetryFailedRequestOptions{Body: &input})
	})
	return out, err
}
