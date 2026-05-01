package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// OpenAIConfig configures the OpenAI-compatible chat-completions client.
type OpenAIConfig struct {
	Endpoint    string        // base URL ending in /v1; "/chat/completions" appended
	APIKey      string        // optional bearer token
	Timeout     time.Duration // per-request HTTP timeout (default 30s)
	MaxRetries  int           // total HTTP attempts (default 2)
	BackoffBase time.Duration // exponential base (default 5s)
	BackoffCap  time.Duration // max backoff (default 60s)
}

// OpenAICompatible is the v1 vision gateway implementation.
type OpenAICompatible struct {
	cfg  OpenAIConfig
	http *http.Client
	rng  *rand.Rand
}

// NewOpenAICompatible applies defaults and constructs a client.
func NewOpenAICompatible(cfg OpenAIConfig) *OpenAICompatible {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 2
	}
	if cfg.BackoffBase == 0 {
		cfg.BackoffBase = 5 * time.Second
	}
	if cfg.BackoffCap == 0 {
		cfg.BackoffCap = 60 * time.Second
	}
	return &OpenAICompatible{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.Timeout},
		rng:  rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0xdeadbeef)),
	}
}

type chatRequest struct {
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

type chatMessage struct {
	Role    string        `json:"role"`
	Content []contentPart `json:"content"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Generate sends a single chat-completion request and returns the
// assistant's raw text. Transient errors are retried per cfg.MaxRetries.
func (c *OpenAICompatible) Generate(ctx context.Context, req Request) (Response, error) {
	dataURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(req.JPEG)
	body := chatRequest{
		Model: req.Model,
		Messages: []chatMessage{{
			Role: "user",
			Content: []contentPart{
				{Type: "text", Text: req.Prompt},
				{Type: "image_url", ImageURL: &imageURL{URL: dataURL}},
			},
		}},
		MaxTokens: req.MaxTokens,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Response{}, fmt.Errorf("marshal: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= c.cfg.MaxRetries; attempt++ {
		text, retryAfter, retryAfterSet, err := c.doOnce(ctx, payload)
		if err == nil {
			return Response{Text: text}, nil
		}
		// Permanent: bail.
		if errors.Is(err, ErrPermanent4xx) {
			return Response{}, err
		}
		lastErr = err
		if attempt == c.cfg.MaxRetries {
			break
		}
		// Transient: backoff and retry.
		backoff := c.computeBackoff(attempt, retryAfter, retryAfterSet)
		if backoff > 0 {
			select {
			case <-ctx.Done():
				return Response{}, fmt.Errorf("gateway: ctx canceled during backoff: %w", ctx.Err())
			case <-time.After(backoff):
			}
		}
	}
	return Response{}, fmt.Errorf("gateway: giving up after %d attempts: %w", c.cfg.MaxRetries, lastErr)
}

func (c *OpenAICompatible) doOnce(ctx context.Context, payload []byte) (string, time.Duration, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoint+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", 0, false, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", 0, false, fmt.Errorf("%w: http do: %v", ErrTransient, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusTooManyRequests {
		ra, ok := parseRetryAfter(resp.Header.Get("Retry-After"))
		return "", ra, ok, fmt.Errorf("%w: HTTP 429", ErrTransient)
	}
	if resp.StatusCode >= 500 {
		return "", 0, false, fmt.Errorf("%w: HTTP %d", ErrTransient, resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			return "", 0, false, fmt.Errorf("HTTP %d: %w", resp.StatusCode, ErrPermanent4xx)
		}
		return "", 0, false, fmt.Errorf("HTTP %d: %s: %w", resp.StatusCode, msg, ErrPermanent4xx)
	}

	var r chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", 0, false, fmt.Errorf("%w: decode: %v", ErrTransient, err)
	}
	if len(r.Choices) == 0 {
		return "", 0, false, fmt.Errorf("%w: no choices in response", ErrTransient)
	}
	return r.Choices[0].Message.Content, 0, false, nil
}

func (c *OpenAICompatible) computeBackoff(attempt int, retryAfter time.Duration, retryAfterSet bool) time.Duration {
	if retryAfterSet {
		return retryAfter
	}
	// Exponential with jitter.
	shift := min(attempt-1, 8)
	exp := min(c.cfg.BackoffBase<<uint(shift), c.cfg.BackoffCap)
	if exp <= 0 {
		return 0
	}
	jitterCap := int64(exp / 4)
	if jitterCap <= 0 {
		return exp
	}
	return exp + time.Duration(c.rng.Int64N(jitterCap))
}

// HealthCheck pings /v1/models. Provider-agnostic: every OpenAI-compatible
// server (Ollama, vLLM, llama.cpp, OpenAI itself) implements GET /v1/models.
func (c *OpenAICompatible) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.Endpoint+"/models", nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("health: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("health: HTTP %d", resp.StatusCode)
	}
	return nil
}

func parseRetryAfter(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	const maxWait = time.Hour
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		d := time.Duration(secs) * time.Second
		if d > maxWait {
			return maxWait, true
		}
		return d, true
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d <= 0 {
			return 0, true
		}
		if d > maxWait {
			return maxWait, true
		}
		return d, true
	}
	return 0, false
}
