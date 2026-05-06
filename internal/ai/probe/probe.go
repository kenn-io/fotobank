// Package probe contains one-shot admin diagnostics for AI endpoints.
// It deliberately bypasses production gateways/workers so Test Endpoint
// does not consume worker semaphores, emit job metrics, or publish
// pending form values into the runtime provider.
package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const defaultTimeout = 15 * time.Second

// Classification is the operator-facing diagnostic class.
type Classification string

const (
	ClassOK                Classification = "ok"
	ClassAuthFailed        Classification = "auth_failed"
	ClassUnreachable       Classification = "unreachable"
	ClassModelMismatch     Classification = "model_mismatch"
	ClassDimensionMismatch Classification = "dimension_mismatch"
	ClassMalformedResponse Classification = "malformed_response"
	ClassTimeout           Classification = "timeout"
	ClassProviderError     Classification = "provider_error"
)

// Result is the structured response returned to the admin UI.
type Result struct {
	OK             bool           `json:"ok"`
	LatencyMS      int64          `json:"latency_ms"`
	Classification Classification `json:"classification"`
	Detail         string         `json:"detail"`
	ModelEchoed    *string        `json:"model_echoed,omitempty"`
	Warnings       []string       `json:"warnings"`
}

// VisionConfig is the pending form state needed for a vision probe.
type VisionConfig struct {
	Endpoint  string
	Model     string
	APIKeyEnv string
}

// EmbedConfig is the pending form state needed for an embeddings probe.
type EmbedConfig struct {
	Endpoint  string
	Model     string
	APIKeyEnv string
	Dimension int
}

var tinyJPEG = mustBuildTinyJPEG()

func mustBuildTinyJPEG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, G: 255, B: 255, A: 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		panic(fmt.Sprintf("probe: encode tiny jpeg: %v", err))
	}
	return buf.Bytes()
}

func resolveAPIKey(envName string) (string, *Result) {
	if envName == "" {
		return "", nil
	}
	v := os.Getenv(envName)
	if v == "" {
		return "", &Result{
			OK:             false,
			Classification: ClassAuthFailed,
			Detail:         "env var $" + envName + " is not set in the server process",
		}
	}
	return v, nil
}

func postJSON(ctx context.Context, endpoint, path, apiKey string, body any) (*http.Response, time.Duration, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal request: %w", err)
	}
	url := strings.TrimRight(endpoint, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: defaultTimeout}
	start := time.Now()
	resp, err := client.Do(req)
	return resp, time.Since(start), err
}

func classifyTransportError(err error, latency time.Duration) Result {
	class := ClassUnreachable
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		class = ClassTimeout
	} else {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			class = ClassTimeout
		}
	}
	return Result{
		OK:             false,
		LatencyMS:      latency.Milliseconds(),
		Classification: class,
		Detail:         err.Error(),
	}
}

func classifyStatus(resp *http.Response, latency time.Duration) (Result, bool) {
	if resp.StatusCode < 400 {
		return Result{}, false
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	detail := strings.TrimSpace(string(body))
	if detail == "" {
		detail = http.StatusText(resp.StatusCode)
	}
	class := ClassProviderError
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		class = ClassAuthFailed
	} else if modelNotFound(detail) {
		class = ClassModelMismatch
	}
	return Result{
		OK:             false,
		LatencyMS:      latency.Milliseconds(),
		Classification: class,
		Detail:         detail,
	}, true
}

func modelNotFound(body string) bool {
	lower := strings.ToLower(body)
	return strings.Contains(lower, "model") &&
		(strings.Contains(lower, "not found") || strings.Contains(lower, "unknown"))
}

func okResult(latency time.Duration, detail string) Result {
	return Result{OK: true, LatencyMS: latency.Milliseconds(), Classification: ClassOK, Detail: detail}
}

func malformed(latency time.Duration, detail string) Result {
	return Result{OK: false, LatencyMS: latency.Milliseconds(), Classification: ClassMalformedResponse, Detail: detail}
}
