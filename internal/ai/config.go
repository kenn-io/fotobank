package ai

import (
	"fmt"
	"net/url"
	"os"
	"time"
)

// Config is the [ai] TOML block plus its sub-blocks.
type Config struct {
	Enabled bool         `toml:"enabled"`
	Vision  VisionConfig `toml:"vision"`
	Tag     TaskConfig   `toml:"tag"`
	Caption TaskConfig   `toml:"caption"`
	Embed   EmbedConfig  `toml:"embed"`
}

// VisionConfig is the shared OpenAI-compatible chat-completions endpoint.
type VisionConfig struct {
	Endpoint    string        `toml:"endpoint"`
	APIKeyEnv   string        `toml:"api_key_env"`
	Timeout     time.Duration `toml:"timeout"`
	MaxRetries  int           `toml:"max_retries"`
	MaxInflight int           `toml:"max_inflight"`
}

// APIKey resolves the bearer token from the env var named in APIKeyEnv.
// Returns "" when APIKeyEnv is empty or the variable is unset.
func (v VisionConfig) APIKey() string {
	if v.APIKeyEnv == "" {
		return ""
	}
	return os.Getenv(v.APIKeyEnv)
}

// timeoutOrDefault returns the per-request HTTP timeout, defaulting to 2m.
// Used by the OpenAI-compat client (C2) once it lands.
//
//nolint:unused // consumed by internal/ai/openai client in a follow-up task
func (v VisionConfig) timeoutOrDefault() time.Duration {
	if v.Timeout <= 0 {
		return 2 * time.Minute
	}
	return v.Timeout
}

// TaskConfig configures a single AI task (tag or caption).
type TaskConfig struct {
	Enabled           bool   `toml:"enabled"`
	Model             string `toml:"model"`
	WorkerConcurrency int    `toml:"worker_concurrency"`
}

// EmbedConfig configures the image-embedding pipeline. The endpoint is
// OpenAI-compatible (POST /v1/embeddings); the worker batches resolved
// previews up to BatchSize per request.
type EmbedConfig struct {
	Enabled           bool          `toml:"enabled"`
	Model             string        `toml:"model"`
	Endpoint          string        `toml:"endpoint"`
	APIKeyEnv         string        `toml:"api_key_env"`
	Dimension         int           `toml:"dimension"`
	InputEdge         int           `toml:"input_edge"`
	WorkerConcurrency int           `toml:"worker_concurrency"`
	BatchSize         int           `toml:"batch_size"`
	MaxRetries        int           `toml:"max_retries"`
	Timeout           time.Duration `toml:"timeout"`
}

// APIKey resolves the bearer token from the env var named in APIKeyEnv.
// Returns "" when APIKeyEnv is empty or the variable is unset.
func (e EmbedConfig) APIKey() string {
	if e.APIKeyEnv == "" {
		return ""
	}
	return os.Getenv(e.APIKeyEnv)
}

// ApplyDefaults fills sensible defaults so a minimal [ai] block works.
func (c *Config) ApplyDefaults() {
	if c.Vision.Timeout == 0 {
		c.Vision.Timeout = 2 * time.Minute
	}
	if c.Vision.MaxRetries == 0 {
		c.Vision.MaxRetries = 2
	}
	if c.Vision.MaxInflight < 1 {
		c.Vision.MaxInflight = 1
	}
	if c.Tag.WorkerConcurrency < 1 {
		c.Tag.WorkerConcurrency = 1
	}
	if c.Caption.WorkerConcurrency < 1 {
		c.Caption.WorkerConcurrency = 1
	}
	if c.Embed.InputEdge <= 0 {
		c.Embed.InputEdge = 384
	}
	if c.Embed.BatchSize <= 0 {
		c.Embed.BatchSize = 32
	}
	if c.Embed.WorkerConcurrency < 1 {
		c.Embed.WorkerConcurrency = 1
	}
	if c.Embed.MaxRetries < 1 {
		c.Embed.MaxRetries = 1
	}
	if c.Embed.Timeout <= 0 {
		c.Embed.Timeout = 10 * time.Second
	}
}

// Validate is invoked at boot. Disabled configs are not checked.
func (c *Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Vision.Endpoint == "" {
		return fmt.Errorf("ai.vision.endpoint: required when ai.enabled=true")
	}
	u, err := url.Parse(c.Vision.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("ai.vision.endpoint: must be http(s) URL with host (got %q)", c.Vision.Endpoint)
	}
	if c.Tag.Enabled && c.Tag.Model == "" {
		return fmt.Errorf("ai.tag.model: required when ai.tag.enabled=true")
	}
	if c.Caption.Enabled && c.Caption.Model == "" {
		return fmt.Errorf("ai.caption.model: required when ai.caption.enabled=true")
	}
	if c.Embed.Enabled {
		if c.Embed.Model == "" {
			return fmt.Errorf("ai.embed.model: required when ai.embed.enabled=true")
		}
		if c.Embed.Endpoint == "" {
			return fmt.Errorf("ai.embed.endpoint: required when ai.embed.enabled=true")
		}
		if u, err := url.Parse(c.Embed.Endpoint); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("ai.embed.endpoint: must be http(s) URL with host (got %q)", c.Embed.Endpoint)
		}
		if c.Embed.Dimension <= 0 {
			return fmt.Errorf("ai.embed.dimension: must be > 0 when enabled")
		}
	}
	return nil
}
