package client

import (
	"context"
	"net/http"

	"go.kenn.io/fotobank/internal/httpapi"
	aiservice "go.kenn.io/fotobank/internal/service/ai"
)

func AIHealth(ctx context.Context, dbPath, version string) (aiservice.Health, error) {
	var out aiservice.Health
	err := call(ctx, dbPath, version, http.MethodGet, "/api/v1/ai/health", nil, &out, "retry AI status")
	return out, err
}

func AcknowledgeAI(ctx context.Context, dbPath, version string, input httpapi.AIAcknowledgeRequest) error {
	return call(ctx, dbPath, version, http.MethodPost, "/api/v1/ai/acknowledge", input, nil, "inspect AI status before retrying acknowledgment")
}
