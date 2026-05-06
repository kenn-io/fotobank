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
[broker.exec]
command = "/bin/true"
[backup]
dir = "/custom/backup"
keep_15min = 8
keep_hourly = 12
keep_daily = 14
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
	r.Equal("/custom/backup", cfg.Backup.Dir)
	r.Equal(8, cfg.Backup.Keep15Min)
	r.Equal(12, cfg.Backup.KeepHourly)
	r.Equal(14, cfg.Backup.KeepDaily)
}

func TestValidateRequiresNASRoot(t *testing.T) {
	_, err := config.Load(filepath.Join("..", "..", "testdata", "config", "missing-nas.toml"))
	require.Error(t, err)
	require.ErrorIs(t, err, errs.ErrBadConfiguration)
}

// TestHTTPDevInsecureCookiesDefaultsFalse verifies the F2.4 cookie-mode
// flag stays false unless the operator opts in. Production deployments
// must never silently emit non-Secure unlock cookies.
func TestHTTPDevInsecureCookiesDefaultsFalse(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "..", "testdata", "config", "minimal.toml"))
	require.NoError(t, err)
	require.False(t, cfg.HTTP.DevInsecureCookies)
}

// TestHTTPDevInsecureCookiesExplicitTrue verifies the operator opt-in is
// honored. Used for HTTP loopback dev/e2e setups.
func TestHTTPDevInsecureCookiesExplicitTrue(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[http]
dev_insecure_cookies = true
`), 0o600))
	cfg, err := config.Load(p)
	require.NoError(t, err)
	require.True(t, cfg.HTTP.DevInsecureCookies)
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

func TestAdminPrincipalsParseFromTOML(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[admin]
principals = [
  { hub = "dev-local", user_id = "owner" },
  { hub = "remote", user_id = "admin" },
]
`), 0o600))

	cfg, err := config.Load(p)
	require.NoError(t, err)
	require.Equal(t, []config.AdminPrincipal{
		{Hub: "dev-local", UserID: "owner"},
		{Hub: "remote", UserID: "admin"},
	}, cfg.Admin.Principals)
}

func TestAdminPrincipalsDefaultToStubPrincipal(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[identity.stub]
hub = "dev-local"
user_id = "owner"
`), 0o600))

	cfg, err := config.Load(p)
	require.NoError(t, err)
	require.Equal(t, []config.AdminPrincipal{
		{Hub: "dev-local", UserID: "owner"},
	}, cfg.Admin.Principals)
}

func TestAdminPrincipalsAbsentInHeaderModeLeavesEmptyAllowlist(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[identity]
mode = "header"
[http]
listen_address = "127.0.0.1:8090"
`), 0o600))

	cfg, err := config.Load(p)
	require.NoError(t, err)
	require.Empty(t, cfg.Admin.Principals)
}

func TestValidateRejectsIncompleteAdminPrincipal(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[admin]
principals = [
  { hub = "dev-local", user_id = "" },
]
`), 0o600))

	_, err := config.Load(p)
	require.ErrorIs(t, err, errs.ErrBadConfiguration)
	require.Contains(t, err.Error(), "admin.principals")
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

func TestDefaultConfigPathHonoursXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	t.Setenv("FOTOBANK_CONFIG", "")
	require.Equal(t, "/tmp/xdg/fotobank/config.toml", config.DefaultConfigPath())
}

func TestDefaultConfigPathHonoursEnvOverride(t *testing.T) {
	t.Setenv("FOTOBANK_CONFIG", "/custom/c.toml")
	require.Equal(t, "/custom/c.toml", config.DefaultConfigPath())
}

func TestEnsureDefaultWritesExampleOnFirstRun(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")

	scaffolded, err := config.EnsureDefault(path)
	r.NoError(err)
	r.True(scaffolded)

	// File now exists.
	_, err = os.Stat(path)
	r.NoError(err)

	// Second run is a no-op.
	scaffolded, err = config.EnsureDefault(path)
	r.NoError(err)
	r.False(scaffolded)
}

func TestEnsureDefaultWrittenFileLoadsCleanly(t *testing.T) {
	// After EnsureDefault writes the example, config.Load should parse it
	// successfully (the embedded example must itself be a valid config).
	r := require.New(t)
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	_, err := config.EnsureDefault(path)
	r.NoError(err)

	// The example file sets nas.root to a placeholder; since it may be
	// "/mnt/nas/fotobank" which is valid syntax, Load should parse it.
	cfg, err := config.Load(path)
	r.NoError(err)
	r.NotNil(cfg)
	r.NotEmpty(cfg.NAS.Root)
}

func TestProxySecretFallsBackToEnvIfTOMLUnset(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[identity]
mode = "header"
[identity.header]
proxy_secret_header = "X-Foo"
[http]
listen_address = "0.0.0.0:9090"
`), 0o600))

	t.Setenv("FOTOBANK_PROXY_SECRET", "s3cret")
	cfg, err := config.Load(p)
	require.NoError(t, err)
	require.Equal(t, "s3cret", cfg.Identity.Header.ProxySecret)
}

func TestProxySecretTOMLWinsOverEnv(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[identity.header]
proxy_secret = "toml-wins"
`), 0o600))

	t.Setenv("FOTOBANK_PROXY_SECRET", "env-loses")
	cfg, err := config.Load(p)
	require.NoError(t, err)
	require.Equal(t, "toml-wins", cfg.Identity.Header.ProxySecret)
}

func TestStubDevEnvOverridesTOML(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[identity.stub]
hub = "toml-hub"
user_id = "toml-user"
handle = "toml-handle"
`), 0o600))

	t.Setenv("FOTOBANK_DEV_HUB", "env-hub")
	t.Setenv("FOTOBANK_DEV_USER_ID", "env-user")
	t.Setenv("FOTOBANK_DEV_HANDLE", "env-handle")

	cfg, err := config.Load(p)
	require.NoError(t, err)
	r := require.New(t)
	r.Equal("env-hub", cfg.Identity.Stub.Hub)
	r.Equal("env-user", cfg.Identity.Stub.UserID)
	r.Equal("env-handle", cfg.Identity.Stub.Handle)
}

func TestBrokerExecRequiresCommand(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[broker]
mode = "exec"
`), 0o600))
	_, err := config.Load(p)
	require.Error(t, err)
	require.ErrorIs(t, err, errs.ErrBadConfiguration)
}

func TestBrokerExecDefaultsCallTimeout(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[broker]
mode = "exec"
[broker.exec]
command = "/usr/local/bin/fb-broker"
`), 0o600))
	cfg, err := config.Load(p)
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, cfg.Broker.Exec.CallTimeout)
}

func TestBrokerExecAcceptsArgsless(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[broker]
mode = "exec"
[broker.exec]
command = "/usr/local/bin/fb-broker"
`), 0o600))
	cfg, err := config.Load(p)
	r.NoError(err)
	r.Empty(cfg.Broker.Exec.PublishScopeArgs)
	r.Empty(cfg.Broker.Exec.RevokeScopeArgs)
}

func TestBrokerExecRejectsNegativeTimeout(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[broker]
mode = "exec"
[broker.exec]
command = "/usr/local/bin/fb-broker"
call_timeout = "-1s"
`), 0o600))
	_, err := config.Load(p)
	require.Error(t, err)
	require.ErrorIs(t, err, errs.ErrBadConfiguration)
}

func TestBackupDefaults(t *testing.T) {
	r := require.New(t)
	cfg, err := config.Load(filepath.Join("..", "..", "testdata", "config", "minimal.toml"))
	r.NoError(err)
	r.True(cfg.Backup.Enabled) // defaulted true when [backup] absent
	r.Empty(cfg.Backup.Dir)    // empty = derive from nas.root at use site
	r.Equal(4, cfg.Backup.Keep15Min)
	r.Equal(24, cfg.Backup.KeepHourly)
	r.Equal(7, cfg.Backup.KeepDaily)
}

func TestBackupExplicitDisabledHonored(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[backup]
enabled = false
`), 0o600))
	cfg, err := config.Load(p)
	require.NoError(t, err)
	require.False(t, cfg.Backup.Enabled)
}

func TestBackupValidationRejectsZeroKeepCount(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[backup]
keep_15min = 0
`), 0o600))
	_, err := config.Load(p)
	require.ErrorIs(t, err, errs.ErrBadConfiguration)
}

func TestBackupValidationRejectsRelativeDir(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[backup]
dir = "relative/path"
`), 0o600))
	_, err := config.Load(p)
	require.ErrorIs(t, err, errs.ErrBadConfiguration)
}

// When backups are disabled, retention counts are never consulted, so
// keep_* validation must not block boot. An absolute dir is still
// validated because the dir field is read by the CLI snapshot/list/
// restore subcommands regardless of the worker being enabled.
func TestBackupDisabledSkipsKeepValidation(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[backup]
enabled = false
keep_15min = 0
keep_hourly = 0
keep_daily = 0
`), 0o600))
	cfg, err := config.Load(p)
	require.NoError(t, err)
	require.False(t, cfg.Backup.Enabled)
}

func TestObservabilityDefaults(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
`), 0o600))
	cfg, err := config.Load(p)
	r.NoError(err)
	r.True(cfg.Observability.AdminEnabled)
	r.Equal("127.0.0.1:9090", cfg.Observability.AdminListen)
	r.False(cfg.Observability.PprofEnabled)
	r.Equal("auto", cfg.Observability.Logging.Format)
	r.Equal("info", cfg.Observability.Logging.Level)
	r.False(cfg.Observability.Logging.AddSource)
}

func TestObservabilityRejectsNonLoopbackAdmin(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[observability]
admin_enabled = true
admin_listen = "0.0.0.0:9090"
`), 0o600))
	_, err := config.Load(p)
	require.ErrorIs(t, err, errs.ErrBadConfiguration)
	require.Contains(t, err.Error(), "loopback")
}

func TestObservabilityAcceptsLoopbackAndUnix(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:9090", "[::1]:0", "unix:/tmp/fb.sock"} {
		tmp := t.TempDir()
		p := filepath.Join(tmp, "c.toml")
		require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[observability]
admin_enabled = true
admin_listen = "`+addr+`"
`), 0o600))
		_, err := config.Load(p)
		require.NoError(t, err, "addr=%s must be accepted", addr)
	}
}

// TestObservabilityRejectsLocalhostHostname pins the security choice
// that `localhost` is NOT an acceptable admin bind, even though it
// commonly resolves to a loopback address. /etc/hosts mappings can
// vary and the unauthenticated admin listener must rely on the kernel
// giving it a literal loopback IP, not on hostname resolution.
func TestObservabilityRejectsLocalhostHostname(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[observability]
admin_enabled = true
admin_listen = "localhost:9090"
`), 0o600))
	_, err := config.Load(p)
	require.ErrorIs(t, err, errs.ErrBadConfiguration)
	require.Contains(t, err.Error(), "loopback")
}

func TestObservabilityDisabledSkipsAdminListenValidation(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[observability]
admin_enabled = false
admin_listen = "192.168.1.5:9090"
`), 0o600))
	cfg, err := config.Load(p)
	require.NoError(t, err)
	require.False(t, cfg.Observability.AdminEnabled)
}

func TestObservabilityPartialBlockKeepsDefaultAdmin(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[observability]
pprof_enabled = true
`), 0o600))
	cfg, err := config.Load(p)
	r.NoError(err)
	r.True(cfg.Observability.AdminEnabled,
		"writing [observability] for an unrelated field must NOT silently disable the admin listener")
	r.True(cfg.Observability.PprofEnabled)
	r.Equal("127.0.0.1:9090", cfg.Observability.AdminListen)
}

func TestValidateRejectsEnabledAIWithoutEndpoint(t *testing.T) {
	// Endpoint is required only when a vision-using task is enabled;
	// turn tag on so the missing endpoint actually fails validation.
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[ai]
enabled = true
[ai.tag]
enabled = true
model = "m"
`), 0o600))
	_, err := config.Load(p)
	require.ErrorIs(t, err, errs.ErrBadConfiguration)
	require.Contains(t, err.Error(), "endpoint")
}

func TestValidateRejectsEnabledAITagWithoutModel(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[ai]
enabled = true
[ai.vision]
endpoint = "http://127.0.0.1:11434/v1"
[ai.tag]
enabled = true
`), 0o600))
	_, err := config.Load(p)
	require.ErrorIs(t, err, errs.ErrBadConfiguration)
	require.Contains(t, err.Error(), "ai.tag.model")
}

func TestConfig_UI_SharingEnabled_DefaultsFalse(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "..", "testdata", "config", "minimal.toml"))
	require.NoError(t, err)
	require.False(t, cfg.UI.SharingEnabled, "sharing_enabled defaults to false")
}

func TestConfig_UI_SharingEnabled_ParsesTrue(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[ui]
sharing_enabled = true
`), 0o600))
	cfg, err := config.Load(p)
	require.NoError(t, err)
	require.True(t, cfg.UI.SharingEnabled)
}

func TestObservabilityRejectsBadFormatAndLevel(t *testing.T) {
	for _, body := range []string{
		`[observability.logging]
format = "xml"`,
		`[observability.logging]
level = "verbose"`,
	} {
		tmp := t.TempDir()
		p := filepath.Join(tmp, "c.toml")
		require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
`+body), 0o600))
		_, err := config.Load(p)
		require.ErrorIs(t, err, errs.ErrBadConfiguration, "body=%q must reject", body)
	}
}

// TestLoadExpandsTildeInPaths covers the regression that wrote a
// fresh-install user's library to a literal "~" subdirectory of the
// process CWD because the documented "~ expanded" comment in the
// example config was lying — Load never actually did the expansion.
// Every filesystem-path config field gets the expansion: nas root,
// flash root, the import lock path, and the optional mTLS CA file.
func TestLoadExpandsTildeInPaths(t *testing.T) {
	r := require.New(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Clear XDG_STATE_HOME so defaultFlashRoot falls back to $HOME.
	t.Setenv("XDG_STATE_HOME", "")

	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[flash]
root = "~/flash-state"
[nas]
root = "~/photos"
[imports]
file_lock_path = "~/locks/import.lock"
[identity]
mode = "stub"
[identity.stub]
hub = "h"
user_id = "u"
[http]
listen_address = "127.0.0.1:0"
`), 0o600))
	cfg, err := config.Load(p)
	r.NoError(err)
	r.Equal(filepath.Join(home, "flash-state"), cfg.Flash.Root)
	r.Equal(filepath.Join(home, "photos"), cfg.NAS.Root)
	r.Equal(filepath.Join(home, "locks", "import.lock"), cfg.Imports.FileLockPath)
}

// TestLoadLeavesAbsoluteAndRelativePathsAlone proves the expander is
// a no-op for paths that don't start with "~". Absolute paths must
// pass through unchanged so deployments writing to /var/lib/fotobank
// don't get rewritten; relative paths likewise.
func TestLoadLeavesAbsoluteAndRelativePathsAlone(t *testing.T) {
	r := require.New(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[flash]
root = "/var/lib/fotobank"
[nas]
root = "./relative-nas"
[identity]
mode = "stub"
[identity.stub]
hub = "h"
user_id = "u"
[http]
listen_address = "127.0.0.1:0"
`), 0o600))
	cfg, err := config.Load(p)
	r.NoError(err)
	r.Equal("/var/lib/fotobank", cfg.Flash.Root)
	r.Equal("./relative-nas", cfg.NAS.Root)
}

// TestLoadExpandsBareTilde covers the edge case where a path is just
// "~" with no trailing slash; the expander must return $HOME, not an
// empty string or "$HOME/".
func TestLoadExpandsBareTilde(t *testing.T) {
	r := require.New(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "~"
[identity]
mode = "stub"
[identity.stub]
hub = "h"
user_id = "u"
[http]
listen_address = "127.0.0.1:0"
`), 0o600))
	cfg, err := config.Load(p)
	r.NoError(err)
	r.Equal(home, cfg.NAS.Root)
}

// TestLoadRejectsTildeUserForm pins the contract that "~alice/photos"
// is rejected with a clear error rather than silently passed through
// as a relative path. Without this guard, a config like
// `root = "~alice/photos"` would create a literal `./~alice/photos`
// directory under CWD on first write — surprising and silent.
func TestLoadRejectsTildeUserForm(t *testing.T) {
	r := require.New(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "~alice/photos"
[identity]
mode = "stub"
[identity.stub]
hub = "h"
user_id = "u"
[http]
listen_address = "127.0.0.1:0"
`), 0o600))
	_, err := config.Load(p)
	r.Error(err)
	r.Contains(err.Error(), "~user form")
}
