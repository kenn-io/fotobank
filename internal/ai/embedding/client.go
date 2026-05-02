// Package embedding implements the OpenAI-compatible embeddings client used
// by fotobank's search v1 indexing and query-time encoding.
//
// The client targets the standard `/v1/embeddings` envelope:
//
//	POST {Endpoint}/embeddings
//	Body: {"input": [...], "model": "..."}
//	Reply: {"data":[{"embedding":[...],"index":N}], "model":"..."}
//
// Inputs are sent in batch and one float32 vector is returned per input,
// in input order. Image inputs are passed as JPEG byte slices and serialized
// as `data:image/jpeg;base64,...` data URLs. Text inputs are passed verbatim.
//
// Errors are classified into three sentinels for the worker layer:
//
//   - ErrProvider4xx    — permanent: don't retry, surface to operator.
//   - ErrTransient      — retry-eligible (5xx, 429, network, decode hiccups).
//   - ErrMalformed      — config/contract drift (dimension mismatch, JSON shape):
//     retrying won't help, but the cause is the response, not our request.
package embedding

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Sentinel errors. Callers should compare with errors.Is.
var (
	// ErrTransient indicates a temporary failure (5xx, 429, network blip,
	// truncated body). Retry is appropriate; the worker layer decides
	// whether to retry now or schedule a follow-up attempt.
	ErrTransient = errors.New("embedding: transient")

	// ErrProvider4xx indicates a non-retriable failure attributable to the
	// request (bad model name, payload too large, auth). Surface to the
	// operator; do not retry without intervention.
	ErrProvider4xx = errors.New("embedding: provider 4xx")

	// ErrMalformed indicates the response contract was violated — a dimension
	// mismatch or undecodable envelope. Retrying won't help: either the
	// configured Dimension is wrong, or the server is misbehaving.
	ErrMalformed = errors.New("embedding: malformed response")
)

// Config configures an embedding Client.
type Config struct {
	// Endpoint is the OpenAI-compatible base URL ending in /v1
	// (e.g. "https://api.openai.com/v1"). The client appends "/embeddings".
	Endpoint string

	// APIKey is an optional bearer token. When empty, no Authorization
	// header is set — convenient for self-hosted servers without auth.
	APIKey string

	// Model is the model name forwarded as the "model" field of the
	// request body.
	Model string

	// Dimension is the expected length of every returned embedding vector.
	// A response with any vector of a different length is rejected with
	// ErrMalformed.
	Dimension int

	// Timeout is the per-request HTTP timeout. Applied to the underlying
	// http.Client; covers connect + headers + body.
	Timeout time.Duration

	// MaxRetries is the number of additional attempts after the first one
	// for transient failures. MaxRetries=0 means a single attempt total;
	// MaxRetries=1 means one initial attempt and one retry. Permanent
	// 4xx and malformed responses are not retried regardless.
	MaxRetries int
}

// Client posts batched embedding requests to an OpenAI-compatible endpoint.
type Client struct {
	cfg  Config
	http *http.Client
}

// NewClient builds a Client using the supplied Config. The http.Client
// timeout is set from cfg.Timeout (zero = no timeout).
func NewClient(cfg Config) *Client {
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.Timeout},
	}
}

// EmbedImages sends a batch of JPEG byte slices as base64 data URLs and
// returns one float32 vector per input, in input order. Each vector is
// validated to match the configured Dimension; any mismatch fails the
// whole batch with ErrMalformed.
//
// The model parameter is forwarded as the request body's "model"
// field. The worker passes the claim's fingerprint.ModelID so a
// mid-rollout batch under the prior model targets that endpoint
// correctly. An empty model falls back to cfg.Model — the boot probe
// uses that path because it tests the configured default.
func (c *Client) EmbedImages(ctx context.Context, model string, jpegs [][]byte) ([][]float32, error) {
	inputs := make([]string, len(jpegs))
	for i, b := range jpegs {
		inputs[i] = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b)
	}
	return c.callOnce(ctx, model, inputs)
}

// EmbedTexts is the query-time counterpart to EmbedImages. The configured
// model must produce a shared image-text embedding space for hybrid
// ranking to remain comparable. See EmbedImages for the model fallback
// semantics.
func (c *Client) EmbedTexts(ctx context.Context, model string, texts []string) ([][]float32, error) {
	// Defensive copy is unnecessary — strings are immutable. Pass through.
	return c.callOnce(ctx, model, texts)
}

// callOnce is the request engine: builds the JSON body once, then loops
// up to MaxRetries+1 attempts. Per-attempt classification routes to the
// appropriate sentinel.
func (c *Client) callOnce(ctx context.Context, model string, input []string) ([][]float32, error) {
	if model == "" {
		model = c.cfg.Model
	}
	body, err := json.Marshal(map[string]any{
		"input": input,
		"model": model,
	})
	if err != nil {
		// Marshal failure on a tiny static-shape map is effectively
		// impossible, but propagate honestly rather than panicking.
		return nil, fmt.Errorf("embedding: marshal request: %w", err)
	}

	endpoint := c.cfg.Endpoint + "/embeddings"

	var lastErr error
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		// Honor cancellation between attempts. Return the raw ctx
		// error (context.Canceled / context.DeadlineExceeded) so
		// callers can errors.Is(err, context.Canceled). Wrapping it
		// behind ErrTransient would make the cancel indistinguishable
		// from a 5xx and trigger needless retries upstream.
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("embedding: build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if c.cfg.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			// If the error is the context's own (cancel or deadline),
			// surface it unwrapped — matches the pre-loop check above
			// and keeps errors.Is(err, context.Canceled) usable. Don't
			// retry: the context isn't going to un-cancel.
			if cerr := ctx.Err(); cerr != nil && errors.Is(err, cerr) {
				return nil, cerr
			}
			// Network errors are transient by policy.
			lastErr = fmt.Errorf("%w: http do: %v", ErrTransient, err)
			continue
		}

		out, class, perr := parse(resp, len(input), c.cfg.Dimension)
		_ = resp.Body.Close()

		switch class {
		case classOK:
			return out, nil
		case class4xx:
			return nil, fmt.Errorf("%w: %v", ErrProvider4xx, perr)
		case classMalformed:
			return nil, fmt.Errorf("%w: %v", ErrMalformed, perr)
		case classTransient:
			// If the context was cancelled or the deadline expired
			// during the body read, parse classifies the truncated
			// read as transient. Surface the raw context error
			// instead so callers can errors.Is(err, context.Canceled
			// / DeadlineExceeded) and don't get a needless retry on
			// a deliberate cancel. Scoped to the transient branch
			// only — a successful, 4xx, or malformed response that
			// happens to land just as ctx is cancelled must still be
			// reported on its own merits.
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
			lastErr = fmt.Errorf("%w: %v", ErrTransient, perr)
			continue
		default:
			// Should be unreachable; treat as malformed to fail loudly.
			return nil, fmt.Errorf("%w: unknown classification", ErrMalformed)
		}
	}

	if lastErr == nil {
		// MaxRetries < 0 or some other oddity that skipped the loop.
		lastErr = fmt.Errorf("%w: exhausted retries", ErrTransient)
	}
	return nil, lastErr
}

// classification names the result of inspecting one HTTP response.
type classification int

const (
	classOK classification = iota + 1
	class4xx
	classMalformed
	classTransient
)

// embedEnvelope mirrors the OpenAI `/v1/embeddings` reply shape, plus an
// `error` field that some servers populate alongside non-2xx statuses.
type embedEnvelope struct {
	Data  []embedItem `json:"data"`
	Model string      `json:"model"`
	Error any         `json:"error,omitempty"`
}

type embedItem struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

// parse classifies a single HTTP response and, on success, returns the
// embedding vectors in input order. The response body is read once.
//
//   - 2xx + valid envelope + matching dim: classOK.
//   - 4xx (excluding 429): class4xx (permanent).
//   - 5xx, 429, network-side decode failures: classTransient.
//   - JSON shape error or dimension mismatch on a 2xx: classMalformed.
func parse(resp *http.Response, wantCount, wantDim int) ([][]float32, classification, error) {
	// Cap body reads to a generous limit. 768-dim float arrays at full text
	// representation comfortably fit in 32 MiB even for batches of dozens.
	const maxBody = 32 << 20 // 32 MiB
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if readErr != nil {
		// Truncated/aborted bodies are treated as transient; a retry may succeed.
		return nil, classTransient, fmt.Errorf("read body (HTTP %d): %v", resp.StatusCode, readErr)
	}

	switch {
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = "(empty body)"
		}
		return nil, classTransient, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(msg, 256))
	case resp.StatusCode >= 400:
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = "(empty body)"
		}
		return nil, class4xx, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(msg, 256))
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		// 1xx/3xx fall here. http.Client already follows 3xx by default, so
		// this should not happen in practice. Treat as transient.
		return nil, classTransient, fmt.Errorf("HTTP %d (unexpected status)", resp.StatusCode)
	}

	var env embedEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, classMalformed, fmt.Errorf("decode envelope: %v", err)
	}
	if len(env.Data) != wantCount {
		return nil, classMalformed, fmt.Errorf("data length: got %d want %d", len(env.Data), wantCount)
	}

	// Defensive copy so the caller can't observe internal aliasing if
	// Index ordering deviates from input order.
	items := make([]embedItem, len(env.Data))
	copy(items, env.Data)

	// Validate index field is sane and unique before sorting; out-of-range
	// or duplicate indexes are malformed by contract.
	seen := make(map[int]struct{}, len(items))
	for _, it := range items {
		if it.Index < 0 || it.Index >= wantCount {
			return nil, classMalformed, fmt.Errorf("index %d out of range [0,%d)", it.Index, wantCount)
		}
		if _, dup := seen[it.Index]; dup {
			return nil, classMalformed, fmt.Errorf("duplicate index %d", it.Index)
		}
		seen[it.Index] = struct{}{}
	}

	// Sort to enforce input order regardless of server-side ordering.
	sort.Slice(items, func(i, j int) bool { return items[i].Index < items[j].Index })

	out := make([][]float32, wantCount)
	for i, it := range items {
		if len(it.Embedding) != wantDim {
			return nil, classMalformed, fmt.Errorf("vector[%d] dim: got %d want %d", i, len(it.Embedding), wantDim)
		}
		// items[i] is positionally aligned with index i after sort.
		out[i] = it.Embedding
	}
	return out, classOK, nil
}

// truncate shortens a string for inclusion in error messages without
// blowing up logs on huge HTML error pages.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
