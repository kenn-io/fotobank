// Package client is Fotobank's typed daemon HTTP client. Wire types belong to
// httpapi; discovery, endpoints and proof use Kit's daemon package.
package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/doordash-oss/oapi-codegen-dd/v3/pkg/runtime"
	"go.kenn.io/fotobank/internal/client/generated"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/kit/daemon"
)

// Commit discovers a proven local server without opening SQLite or Docbank.
// The caller ensures the daemon first. Requests never fall back to local writes.
func Commit(ctx context.Context, configPath, version, checkoutID string, owner owners.Principal) (httpapi.CheckoutCommitResult, error) {
	out := httpapi.CheckoutCommitResult{CheckoutID: checkoutID}
	err := call(ctx, configPath, version, &out, "inspect checkout list/status before retrying", func(c *generated.Client) (*generated.CommitCheckoutResponse, error) {
		return c.CommitCheckout(ctx, &generated.CommitCheckoutRequestOptions{Body: &httpapi.CheckoutCommitRequest{Hub: owner.Hub, UserID: owner.UserID}, PathParams: &generated.CommitCheckoutPath{ID: checkoutID}})
	})
	if err == nil && out.Error != "" {
		err = errors.New(out.Error)
	}
	return out, err
}

// call discovers a proven peer and invokes a generated operation exactly once.
func call[T any](ctx context.Context, configPath, version string, output *T, recoveryHint string, operation func(*generated.Client) (*T, error)) error {
	rec, _, found, err := findDaemon(ctx, configPath)
	if err != nil {
		return err
	}
	if !found || rec.Version != version {
		return fmt.Errorf("no matching Fotobank server; run fotobank daemon start")
	}
	return callRecord(ctx, rec, output, recoveryHint, operation)
}

func callRecord[T any](ctx context.Context, rec daemon.RuntimeRecord, output *T, recoveryHint string, operation func(*generated.Client) (*T, error)) error {
	c, err := recordClient(ctx, rec)
	if err != nil {
		return err
	}
	result, err := operation(c)
	if err != nil {
		return fmt.Errorf("operator response unavailable; %s: %w", recoveryHint, err)
	}
	if output != nil && result != nil {
		*output = *result
	}
	return nil
}

func recordClient(ctx context.Context, rec daemon.RuntimeRecord) (*generated.Client, error) {
	c, _, err := recordAPI(ctx, rec)
	if err != nil {
		return nil, err
	}
	return generated.NewClient(c), nil
}

func recordAPI(ctx context.Context, rec daemon.RuntimeRecord) (*runtime.Client, operatorTransport, error) {
	proof, err := daemon.NewProof([]byte(rec.Metadata["token"]))
	if err != nil {
		return nil, operatorTransport{}, err
	}
	if _, err := proof.Probe(ctx, rec, daemon.ProbeOptions{ExpectedService: "fotobank-operator"}); err != nil {
		return nil, operatorTransport{}, err
	}
	ep := rec.Endpoint()
	client := ep.HTTPClient(daemon.HTTPClientOptions{DisableKeepAlives: true})
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	api, err := runtime.NewAPIClient(ep.BaseURL(), runtime.WithHTTPClient(operatorTransport{client}), runtime.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+rec.Metadata["token"])
		return nil
	}))
	return api, operatorTransport{client}, err
}

type operatorTransport struct{ client *http.Client }

func (t operatorTransport) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	response, err := t.client.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("operator command returned %s: %s", response.Status, body)
	}
	return response, nil
}

// Downloads retain the response body so large originals never buffer in memory.
// Request paths and parameters still come from the generated client.
type downloadAPI struct {
	*runtime.Client
	transport operatorTransport
}

func (c downloadAPI) ExecuteRequest(ctx context.Context, req *http.Request, _ string) (*runtime.Response, error) {
	response, err := c.transport.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	return &runtime.Response{Raw: response, StatusCode: response.StatusCode, Headers: response.Header}, nil
}
