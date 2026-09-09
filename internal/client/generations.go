package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"go.kenn.io/fotobank/internal/httpapi"
)

func ListGenerations(ctx context.Context, dbPath, version, state string) (httpapi.GenerationListResult, error) {
	var result httpapi.GenerationListResult
	err := call(ctx, dbPath, version, http.MethodGet, "/api/v1/operator/ai/generations?"+url.Values{"state": {state}}.Encode(), nil, &result, "retry generation listing")
	return result, err
}

func GetGeneration(ctx context.Context, dbPath, version string, id int64) (httpapi.GenerationDetails, error) {
	var result httpapi.GenerationDetails
	err := call(ctx, dbPath, version, http.MethodGet, fmt.Sprintf("/api/v1/operator/ai/generations/%d", id), nil, &result, "inspect the generation again")
	return result, err
}

func PromoteGeneration(ctx context.Context, dbPath, version string, id int64) (httpapi.PromoteGenerationResult, error) {
	var result httpapi.PromoteGenerationResult
	err := call(ctx, dbPath, version, http.MethodPost, fmt.Sprintf("/api/v1/operator/ai/generations/%d/promote", id), httpapi.PromoteGenerationRequest{Confirm: true}, &result, "list generations before retrying promotion")
	return result, err
}

func CompactGenerations(ctx context.Context, dbPath, version string, dryRun bool) (httpapi.CompactGenerationsResult, error) {
	var result httpapi.CompactGenerationsResult
	err := call(ctx, dbPath, version, http.MethodPost, "/api/v1/operator/ai/generations/compact", httpapi.CompactGenerationsRequest{DryRun: dryRun}, &result, "inspect retired generations before retrying compaction")
	return result, err
}
