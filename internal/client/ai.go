package client

import (
	"context"
	"net/http"

	"go.kenn.io/fotobank/internal/httpapi"
	aiservice "go.kenn.io/fotobank/internal/service/ai"
)

func AIHealth(ctx context.Context, configPath, version string) (aiservice.Health, error) {
	var out aiservice.Health
	err := call(ctx, configPath, version, http.MethodGet, "/api/v1/ai/health", nil, &out, "retry AI status")
	return out, err
}

func AcknowledgeAI(ctx context.Context, configPath, version string, input httpapi.AIAcknowledgeRequest) error {
	return call(ctx, configPath, version, http.MethodPost, "/api/v1/ai/acknowledge", input, nil, "inspect AI status before retrying acknowledgment")
}

func BackfillAI(ctx context.Context, configPath, version string, input httpapi.AIBackfillRequest) (httpapi.AIEnqueuedResult, error) {
	var out httpapi.AIEnqueuedResult
	err := call(ctx, configPath, version, http.MethodPost, "/api/v1/ai/backfill", input, &out, "inspect AI status before retrying backfill")
	return out, err
}

func RetryFailedAI(ctx context.Context, configPath, version string, input httpapi.AIRetryFailedRequest) (httpapi.AIEnqueuedResult, error) {
	var out httpapi.AIEnqueuedResult
	err := call(ctx, configPath, version, http.MethodPost, "/api/v1/ai/retry-failed", input, &out, "inspect AI status before retrying failed jobs")
	return out, err
}
