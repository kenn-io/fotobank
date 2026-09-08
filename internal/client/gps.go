package client

import (
	"context"
	"errors"
	"net/http"

	"go.kenn.io/fotobank/internal/httpapi"
)

func BackfillGPS(ctx context.Context, dbPath, version string, request httpapi.GPSBackfillRequest) (httpapi.GPSBackfillResult, error) {
	var result httpapi.GPSBackfillResult
	err := call(ctx, dbPath, version, http.MethodPost, "/api/v1/operator/gps/backfill", request, &result, "inspect GPS results before rerunning backfill")
	if err == nil && result.Error != "" {
		err = errors.New(result.Error)
	}
	return result, err
}
