package client

import (
	"context"
	"errors"

	"go.kenn.io/fotobank/internal/client/generated"
	"go.kenn.io/fotobank/internal/httpapi"
)

func RecoverContent(ctx context.Context, configPath, version string, request httpapi.ContentRecoveryRequest) (httpapi.ContentRecoveryResult, error) {
	var out httpapi.ContentRecoveryResult
	err := call(ctx, configPath, version, &out, "rerun content recover to reconcile completed work", func(c *generated.Client) (*generated.RecoverContentResponse, error) {
		return c.RecoverContent(ctx, &generated.RecoverContentRequestOptions{Body: &request})
	})
	if err == nil && out.Error != "" {
		err = errors.New(out.Error)
	}
	return out, err
}
