package client

import (
	"context"

	"go.kenn.io/fotobank/internal/client/generated"
	"go.kenn.io/fotobank/internal/httpapi"
)

func SetupHidden(ctx context.Context, configPath, version string, input httpapi.HiddenPasscodeRequest) error {
	return call(ctx, configPath, version, nil, "inspect hidden privacy state before retrying", func(c *generated.Client) (*struct{}, error) {
		return c.HiddenSetup(ctx, &generated.HiddenSetupRequestOptions{Body: &input})
	})
}

func ChangeHidden(ctx context.Context, configPath, version string, input httpapi.HiddenChangeRequest) error {
	return call(ctx, configPath, version, nil, "check which passcode works before retrying", func(c *generated.Client) (*struct{}, error) {
		return c.HiddenChange(ctx, &generated.HiddenChangeRequestOptions{Body: &input})
	})
}

func DisableHidden(ctx context.Context, configPath, version string, input httpapi.HiddenPasscodeRequest) error {
	return call(ctx, configPath, version, nil, "inspect hidden privacy state before retrying", func(c *generated.Client) (*struct{}, error) {
		return c.HiddenDisable(ctx, &generated.HiddenDisableRequestOptions{Body: &input})
	})
}

func ResetHidden(ctx context.Context, configPath, version string, input httpapi.ResetHiddenRequest) (httpapi.ResetHiddenResult, error) {
	var out httpapi.ResetHiddenResult
	err := call(ctx, configPath, version, &out, "inspect hidden privacy state before retrying", func(c *generated.Client) (*generated.ResetHiddenPasscodeResponse, error) {
		return c.ResetHiddenPasscode(ctx, &generated.ResetHiddenPasscodeRequestOptions{Body: &input})
	})
	return out, err
}
