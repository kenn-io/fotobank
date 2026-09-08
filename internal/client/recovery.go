package client

import (
	"context"
	"errors"
	"net/http"

	"go.kenn.io/fotobank/internal/httpapi"
)

func RecoverContent(ctx context.Context, dbPath, version string, request httpapi.ContentRecoveryRequest) (httpapi.ContentRecoveryResult, error) {
	var out httpapi.ContentRecoveryResult
	err := call(ctx, dbPath, version, http.MethodPost, "/api/v1/operator/content/recover", request, &out, "rerun content recover to reconcile completed work")
	if err == nil && out.Error != "" {
		err = errors.New(out.Error)
	}
	return out, err
}
