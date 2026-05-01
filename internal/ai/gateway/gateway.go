// Package gateway defines fotobank's interface to vision-language
// models. v1 ships a single OpenAI-compatible chat-completions impl;
// the interface is provider-neutral so future native adapters can
// plug in without changing worker code.
package gateway

import (
	"context"
	"errors"
)

// Request is one VLM call: a prompt plus the JPEG bytes to attach as
// the user's image content part.
type Request struct {
	Model     string
	Prompt    string
	JPEG      []byte
	MaxTokens int // 0 = provider default
}

// Response is the raw assistant message the VLM returned. The caller
// (see internal/ai/parse) is responsible for defensive JSON extraction.
type Response struct {
	Text string
}

// VisionGateway is the minimum contract a vision provider must implement.
type VisionGateway interface {
	// Generate calls the model and returns the raw assistant message text.
	// Implementations honor ctx for cancellation.
	Generate(ctx context.Context, req Request) (Response, error)
	// HealthCheck performs a low-cost reachability probe. Should not
	// invoke the model itself.
	HealthCheck(ctx context.Context) error
}

// ErrPermanent4xx wraps non-retryable HTTP 4xx responses (other than
// 429). Callers use errors.Is to detect; the error message still
// carries the status code and a bounded response body.
var ErrPermanent4xx = errors.New("gateway: non-retryable 4xx")

// ErrTransient is a transient failure (5xx, 429-after-retries, network).
// Wraps the underlying cause for diagnostic purposes; classification at
// the worker level uses errors.Is.
var ErrTransient = errors.New("gateway: transient failure")
