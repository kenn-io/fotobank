package client

import (
	"context"
	"net/http"

	"go.kenn.io/fotobank/internal/httpapi"
)

func SetupHidden(ctx context.Context, configPath, version string, input httpapi.HiddenPasscodeRequest) error {
	return call(ctx, configPath, version, http.MethodPost, "/api/v1/auth/hidden/setup", input, nil, "inspect hidden privacy state before retrying")
}

func ChangeHidden(ctx context.Context, configPath, version string, input httpapi.HiddenChangeRequest) error {
	return call(ctx, configPath, version, http.MethodPost, "/api/v1/auth/hidden/change", input, nil, "check which passcode works before retrying")
}

func DisableHidden(ctx context.Context, configPath, version string, input httpapi.HiddenPasscodeRequest) error {
	return call(ctx, configPath, version, http.MethodPost, "/api/v1/auth/hidden/disable", input, nil, "inspect hidden privacy state before retrying")
}

func ResetHidden(ctx context.Context, configPath, version string, input httpapi.ResetHiddenRequest) (httpapi.ResetHiddenResult, error) {
	var out httpapi.ResetHiddenResult
	err := call(ctx, configPath, version, http.MethodPost, "/api/v1/operator/hidden/reset", input, &out, "inspect hidden privacy state before retrying")
	return out, err
}
