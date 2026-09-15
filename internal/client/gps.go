package client

import (
	"context"
	"errors"

	"go.kenn.io/fotobank/internal/client/generated"
	"go.kenn.io/fotobank/internal/httpapi"
)

func BackfillGPS(ctx context.Context, configPath, version string, request httpapi.GPSBackfillRequest) (httpapi.GPSBackfillResult, error) {
	var result httpapi.GPSBackfillResult
	err := call(ctx, configPath, version, &result, "inspect GPS results before rerunning backfill", func(c *generated.Client) (*generated.BackfillGpsResponse, error) {
		return c.BackfillGps(ctx, &generated.BackfillGpsRequestOptions{Body: &request})
	})
	if err == nil && result.Error != "" {
		err = errors.New(result.Error)
	}
	return result, err
}
