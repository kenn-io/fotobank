package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/config"
	"github.com/wesm/fotobank/internal/errs"
)

func TestLoadAppliesDefaults(t *testing.T) {
	r := require.New(t)
	cfg, err := config.Load(filepath.Join("..", "..", "testdata", "config", "minimal.toml"))
	r.NoError(err)

	r.Equal("/tmp/test-nas", cfg.NAS.Root)
	r.NotEmpty(cfg.Flash.Root)               // defaulted
	r.Equal("flash_cache", cfg.Storage.Mode) // defaulted
	r.True(cfg.Storage.ThumbsCacheEnabled)   // defaulted true when unset
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

func TestExplicitThumbsCacheFalseIsHonored(t *testing.T) {
	// Regression test: default should not overwrite an explicit false.
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[storage]
thumbs_cache_enabled = false
`), 0o600))
	cfg, err := config.Load(p)
	require.NoError(t, err)
	require.False(t, cfg.Storage.ThumbsCacheEnabled)
}

func TestExplicitTOMLValuesWinOverDefaults(t *testing.T) {
	// Regression test: TOML values override defaults for every section.
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/custom/nas"
[flash]
root = "/custom/flash"
[storage]
mode = "nas_only"
originals_cache_days = 7
originals_cache_max_media = 10
[identity]
mode = "header"
[http]
listen_address = "127.0.0.1:9999"
request_timeout = "15s"
write_timeout = "45s"
[imports]
concurrent_workers = 8
[thumbs]
worker_concurrency = 1
poll_interval = "1s"
lease_timeout = "2m"
[broker]
mode = "exec"
[backup]
snapshot_interval = "1h"
snapshot_retention = 48
`), 0o600))
	cfg, err := config.Load(p)
	require.NoError(t, err)
	r := require.New(t)
	r.Equal("/custom/nas", cfg.NAS.Root)
	r.Equal("/custom/flash", cfg.Flash.Root)
	r.Equal("nas_only", cfg.Storage.Mode)
	r.Equal(7, cfg.Storage.OriginalsCacheDays)
	r.Equal(10, cfg.Storage.OriginalsCacheMaxMedia)
	r.Equal("header", cfg.Identity.Mode)
	r.Equal("127.0.0.1:9999", cfg.HTTP.ListenAddress)
	r.Equal(15*time.Second, cfg.HTTP.RequestTimeout)
	r.Equal(45*time.Second, cfg.HTTP.WriteTimeout)
	r.Equal(8, cfg.Imports.ConcurrentWorkers)
	r.Equal(1, cfg.Thumbs.WorkerConcurrency)
	r.Equal(time.Second, cfg.Thumbs.PollInterval)
	r.Equal(2*time.Minute, cfg.Thumbs.LeaseTimeout)
	r.Equal("exec", cfg.Broker.Mode)
	r.Equal(time.Hour, cfg.Backup.SnapshotInterval)
	r.Equal(48, cfg.Backup.SnapshotRetention)
}

func TestValidateRequiresNASRoot(t *testing.T) {
	_, err := config.Load(filepath.Join("..", "..", "testdata", "config", "missing-nas.toml"))
	require.Error(t, err)
	require.ErrorIs(t, err, errs.ErrBadConfiguration)
}

func TestValidateHeaderModeRequiresGuard(t *testing.T) {
	// Neutralise an inherited env secret so the test is deterministic
	// regardless of the developer's shell.
	t.Setenv("FOTOBANK_PROXY_SECRET", "")
	_, err := config.Load(filepath.Join("..", "..", "testdata", "config", "header-no-guard.toml"))
	require.Error(t, err)
	require.ErrorIs(t, err, errs.ErrBadConfiguration)
}

func TestValidateAcceptsLoopbackInHeaderMode(t *testing.T) {
	// Loopback bind satisfies the guard on its own.
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	err := os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[identity]
mode = "header"
[http]
listen_address = "127.0.0.1:8090"
`), 0o600)
	require.NoError(t, err)
	_, err = config.Load(p)
	require.NoError(t, err)
}

func TestValidateRejectsUnknownIdentityMode(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[identity]
mode = "bogus"
`), 0o600))
	_, err := config.Load(p)
	require.ErrorIs(t, err, errs.ErrBadConfiguration)
}

func TestValidateRejectsUnknownStorageMode(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[storage]
mode = "weird"
`), 0o600))
	_, err := config.Load(p)
	require.ErrorIs(t, err, errs.ErrBadConfiguration)
}

func TestValidateRejectsUnknownBrokerMode(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[broker]
mode = "unknown"
`), 0o600))
	_, err := config.Load(p)
	require.ErrorIs(t, err, errs.ErrBadConfiguration)
}

func TestValidateHeaderModeGuardSatisfiers(t *testing.T) {
	// Exercise each of the three non-loopback guard satisfiers with a
	// public bind address so only the guard can accept the config.
	t.Setenv("FOTOBANK_PROXY_SECRET", "")

	header := func(extra string) string {
		return `
[nas]
root = "/tmp/nas"
[identity]
mode = "header"
[http]
listen_address = "0.0.0.0:8090"
` + extra
	}

	cases := []struct {
		name  string
		extra string
	}{
		{
			name: "cidr",
			extra: `[identity.header]
trusted_proxy_cidrs = ["10.0.0.0/8"]
`,
		},
		{
			name: "proxy_secret",
			extra: `[identity.header]
proxy_secret = "s3cret"
`,
		},
		{
			name: "mtls",
			extra: `[identity.header]
proxy_mtls_ca_file = "/etc/ca.pem"
`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tmp := t.TempDir()
			p := filepath.Join(tmp, "c.toml")
			require.NoError(t, os.WriteFile(p, []byte(header(c.extra)), 0o600))
			_, err := config.Load(p)
			require.NoError(t, err)
		})
	}
}
