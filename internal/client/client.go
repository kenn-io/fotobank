// Package client is Fotobank's typed daemon HTTP client. Wire types belong to
// httpapi; discovery, endpoints and proof use Kit's daemon package.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/kit/daemon"
)

// Commit discovers a proven local server without opening SQLite or Docbank.
// The caller ensures the daemon first. Requests never fall back to local writes.
func Commit(ctx context.Context, configPath, version, checkoutID string, owner owners.Principal) (httpapi.CheckoutCommitResult, error) {
	out := httpapi.CheckoutCommitResult{CheckoutID: checkoutID}
	err := call(ctx, configPath, version, http.MethodPost, "/api/v1/operator/checkouts/"+url.PathEscape(checkoutID)+"/commit",
		httpapi.CheckoutCommitRequest{Hub: owner.Hub, UserID: owner.UserID}, &out, "inspect checkout list/status before retrying")
	if err == nil && out.Error != "" {
		err = errors.New(out.Error)
	}
	return out, err
}

// call proves the peer before sending a command. Requests are never retried:
// a lost response may follow a successful mutation.
func call(ctx context.Context, configPath, version, method, path string, input, output any, recoveryHint string) error {
	rec, _, found, err := findDaemon(ctx, configPath)
	if err != nil {
		return err
	}
	if !found || rec.Version != version {
		return fmt.Errorf("no matching Fotobank server; run fotobank daemon start")
	}
	return callRecord(ctx, rec, method, path, input, output, recoveryHint)
}

func callRecord(ctx context.Context, rec daemon.RuntimeRecord, method, path string, input, output any, recoveryHint string) error {
	response, err := requestRecord(ctx, rec, method, path, input, recoveryHint)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if output == nil {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		return fmt.Errorf("read operator result; %s: %w", recoveryHint, err)
	}
	return nil
}

// requestRecord returns an authenticated response whose body the caller owns.
// Both JSON results and progress streams share this proof and no-retry policy.
func requestRecord(ctx context.Context, rec daemon.RuntimeRecord, method, path string, input any, recoveryHint string) (*http.Response, error) {
	proof, err := daemon.NewProof([]byte(rec.Metadata["token"]))
	if err != nil {
		return nil, err
	}
	if _, err := proof.Probe(ctx, rec, daemon.ProbeOptions{ExpectedService: "fotobank-operator"}); err != nil {
		return nil, err
	}
	credential := rec.Metadata["token"]
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	ep := rec.Endpoint()
	req, err := http.NewRequestWithContext(ctx, method,
		ep.BaseURL()+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+credential)
	req.Header.Set("Content-Type", "application/json")
	client := ep.HTTPClient(daemon.HTTPClientOptions{DisableKeepAlives: true})
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("operator response unavailable; %s: %w", recoveryHint, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("operator command returned %s: %s", response.Status, body)
	}
	return response, nil
}
