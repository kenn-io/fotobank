package config_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/config"
)

func TestLoadAppliesDefaults(t *testing.T) {
	r := require.New(t)
	cfg, err := config.Load(filepath.Join("..", "..", "testdata", "config", "minimal.toml"))
	r.NoError(err)

	r.Equal("/tmp/test-nas", cfg.NAS.Root)
	r.NotEmpty(cfg.Flash.Root)               // defaulted
	r.Equal("flash_cache", cfg.Storage.Mode) // defaulted
	r.Equal("stub", cfg.Identity.Mode)       // defaulted
	r.Equal("127.0.0.1:8090", cfg.HTTP.ListenAddress)
	r.Equal(30*time.Second, cfg.HTTP.RequestTimeout)
	r.Equal(60*time.Second, cfg.HTTP.WriteTimeout)
	r.Equal(2, cfg.Imports.ConcurrentWorkers)
	r.Equal(4, cfg.Thumbs.WorkerConcurrency)
	r.Equal("stub", cfg.Broker.Mode)
}

func TestLoadMissingFileIsError(t *testing.T) {
	_, err := config.Load("/no/such/path.toml")
	require.Error(t, err)
}
