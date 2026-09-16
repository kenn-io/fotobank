package client

import (
	"context"

	"go.kenn.io/fotobank/internal/client/generated"
	"go.kenn.io/fotobank/internal/httpapi"
)

func ListGenerations(ctx context.Context, configPath, version, state string) (httpapi.GenerationListResult, error) {
	var result httpapi.GenerationListResult
	err := call(ctx, configPath, version, &result, "retry generation listing", func(c *generated.Client) (*generated.ListEmbeddingGenerationsResponse, error) {
		return c.ListEmbeddingGenerations(ctx, &generated.ListEmbeddingGenerationsRequestOptions{Query: &generated.ListEmbeddingGenerationsQuery{State: new(generated.ListEmbeddingGenerationsQueryState(state))}})
	})
	return result, err
}

func GetGeneration(ctx context.Context, configPath, version string, id int64) (httpapi.GenerationDetails, error) {
	var result httpapi.GenerationDetails
	err := call(ctx, configPath, version, &result, "inspect the generation again", func(c *generated.Client) (*generated.GetEmbeddingGenerationResponse, error) {
		return c.GetEmbeddingGeneration(ctx, &generated.GetEmbeddingGenerationRequestOptions{PathParams: &generated.GetEmbeddingGenerationPath{ID: id}})
	})
	return result, err
}

func PromoteGeneration(ctx context.Context, configPath, version string, id int64) (httpapi.PromoteGenerationResult, error) {
	var result httpapi.PromoteGenerationResult
	err := call(ctx, configPath, version, &result, "list generations before retrying promotion", func(c *generated.Client) (*generated.PromoteEmbeddingGenerationResponse, error) {
		return c.PromoteEmbeddingGeneration(ctx, &generated.PromoteEmbeddingGenerationRequestOptions{Body: &httpapi.PromoteGenerationRequest{Confirm: true}, PathParams: &generated.PromoteEmbeddingGenerationPath{ID: id}})
	})
	return result, err
}

func CompactGenerations(ctx context.Context, configPath, version string, dryRun bool) (httpapi.CompactGenerationsResult, error) {
	var result httpapi.CompactGenerationsResult
	err := call(ctx, configPath, version, &result, "inspect retired generations before retrying compaction", func(c *generated.Client) (*generated.CompactEmbeddingGenerationsResponse, error) {
		return c.CompactEmbeddingGenerations(ctx, &generated.CompactEmbeddingGenerationsRequestOptions{Body: &httpapi.CompactGenerationsRequest{DryRun: dryRun}})
	})
	return result, err
}
