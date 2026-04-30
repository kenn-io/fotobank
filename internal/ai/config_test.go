package ai_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
)

func TestConfigDefaults(t *testing.T) {
	c := ai.Config{
		Vision:  ai.VisionConfig{Endpoint: "http://127.0.0.1:11434/v1"},
		Tag:     ai.TaskConfig{Enabled: true, Model: "qwen2.5-vl:3b"},
		Caption: ai.TaskConfig{Enabled: true, Model: "llama3.2-vision:11b"},
	}
	c.ApplyDefaults()
	require.Equal(t, 2*time.Minute, c.Vision.Timeout)
}

func TestConfigValidate_disabledNoEndpoint(t *testing.T) {
	c := ai.Config{Enabled: false}
	c.ApplyDefaults()
	require.NoError(t, c.Validate())
}

func TestConfigValidate_enabledRequiresEndpoint(t *testing.T) {
	c := ai.Config{Enabled: true}
	c.ApplyDefaults()
	err := c.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "endpoint")
}

func TestConfigValidate_maxInflightAtLeastOne(t *testing.T) {
	c := ai.Config{
		Enabled: true,
		Vision:  ai.VisionConfig{Endpoint: "http://x/v1", MaxInflight: 0},
		Tag:     ai.TaskConfig{Enabled: true, Model: "m"},
	}
	c.ApplyDefaults()
	require.GreaterOrEqual(t, c.Vision.MaxInflight, 1)
}

func TestConfigValidate_workerConcurrencyMustBePositive(t *testing.T) {
	c := ai.Config{
		Enabled: true,
		Vision:  ai.VisionConfig{Endpoint: "http://x/v1", MaxInflight: 1},
		Tag:     ai.TaskConfig{Enabled: true, Model: "m", WorkerConcurrency: 0},
	}
	c.ApplyDefaults()
	require.GreaterOrEqual(t, c.Tag.WorkerConcurrency, 1)
}
