package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/kit/daemon"
)

// Commit discovers a proven local server without opening SQLite or Docbank.
// It never starts a server, falls back to offline writes, or retries a request.
func Commit(ctx context.Context, dbPath, version, checkoutID string, owner owners.Principal) (CommitResult, error) {
	out := CommitResult{CheckoutID: checkoutID}
	err := call(ctx, dbPath, version, "/checkouts/"+url.PathEscape(checkoutID)+"/commit",
		map[string]string{"hub": owner.Hub, "user_id": owner.UserID}, &out, "inspect checkout list/status before retrying")
	if err == nil && out.Error != "" {
		err = errors.New(out.Error)
	}
	return out, err
}

// call proves the peer before sending a command. Requests are never retried:
// a lost response may follow a successful mutation.
func call(ctx context.Context, dbPath, version, path string, input, output any, recoveryHint string) error {
	store := daemon.RuntimeStore{Dir: dbPath + ".operator"}
	if _, err := os.Stat(store.Dir); err != nil {
		return fmt.Errorf("start fotobank serve with the same configuration before running this command: %w", err)
	}
	records, err := store.List()
	if err != nil {
		return fmt.Errorf("read operator discovery: %w", err)
	}
	for _, rec := range records {
		if rec.Service != serviceName || rec.Version != version || rec.Network != daemon.NetworkTCP || daemon.RequireLoopback(rec.Address) != nil {
			continue
		}
		credential := rec.Metadata["token"]
		proof, err := daemon.NewProof([]byte(credential))
		if err != nil {
			continue
		}
		if _, err := proof.Probe(ctx, rec, daemon.ProbeOptions{ExpectedService: serviceName}); err != nil {
			continue
		}
		body, err := json.Marshal(input)
		if err != nil {
			return err
		}
		ep := rec.Endpoint()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			ep.BaseURL()+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+credential)
		req.Header.Set("Content-Type", "application/json")
		client := ep.HTTPClient(daemon.HTTPClientOptions{DisableKeepAlives: true})
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		response, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("operator response unavailable; %s: %w", recoveryHint, err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
			return fmt.Errorf("operator command returned %s: %s", response.Status, body)
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output); err != nil {
			return fmt.Errorf("read operator result; %s: %w", recoveryHint, err)
		}
		return nil
	}
	return fmt.Errorf("no matching Fotobank server; start fotobank serve with the same configuration and binary before running this command")
}
