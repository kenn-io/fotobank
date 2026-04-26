// Package config loads fotobank's TOML configuration file, applies
// defaults, and layers a narrow set of environment-variable overrides
// (see applyEnvOverrides).
package config

import (
	_ "embed"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/wesm/fotobank/internal/errs"
)

//go:embed config.example.toml
var exampleTOML []byte

// DefaultConfigPath resolves the config file path with this precedence:
// $FOTOBANK_CONFIG → $XDG_CONFIG_HOME/fotobank/config.toml →
// $HOME/.config/fotobank/config.toml → "./config.toml".
func DefaultConfigPath() string {
	if v := os.Getenv("FOTOBANK_CONFIG"); v != "" {
		return v
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "fotobank", "config.toml")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "fotobank", "config.toml")
	}
	return "./config.toml"
}

// EnsureDefault writes the embedded example config if path does not exist.
// Returns scaffolded=true when a new file was written, false if the file
// was already present.
func EnsureDefault(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("stat %q: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("mkdir %q: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, exampleTOML, 0o600); err != nil {
		return false, fmt.Errorf("write %q: %w", path, err)
	}
	return true, nil
}

type Config struct {
	Flash         Flash         `toml:"flash"`
	NAS           NAS           `toml:"nas"`
	Storage       Storage       `toml:"storage"`
	Identity      Identity      `toml:"identity"`
	HTTP          HTTP          `toml:"http"`
	Imports       Imports       `toml:"imports"`
	Thumbs        Thumbs        `toml:"thumbs"`
	Broker        Broker        `toml:"broker"`
	Backup        Backup        `toml:"backup"`
	Observability Observability `toml:"observability"`
}

type Flash struct {
	Root string `toml:"root"`
}

type NAS struct {
	Root string `toml:"root"`
}

type Storage struct {
	Mode                   string `toml:"mode"`
	OriginalsCacheDays     int    `toml:"originals_cache_days"`
	OriginalsCacheMaxMedia int    `toml:"originals_cache_max_media"`
	ThumbsCacheEnabled     bool   `toml:"thumbs_cache_enabled"`
}

type Identity struct {
	Mode   string         `toml:"mode"`
	Stub   IdentityStub   `toml:"stub"`
	Header IdentityHeader `toml:"header"`
}

type IdentityStub struct {
	Hub        string `toml:"hub"`
	UserID     string `toml:"user_id"`
	Handle     string `toml:"handle"`
	StorageKey string `toml:"storage_key"`
}

type IdentityHeader struct {
	UserIDHeader      string   `toml:"user_id_header"`
	HubHeader         string   `toml:"hub_header"`
	HandleHeader      string   `toml:"handle_header"`
	ScopesHeader      string   `toml:"scopes_header"`
	RequestIDHeader   string   `toml:"request_id_header"`
	TrustedProxyCIDRs []string `toml:"trusted_proxy_cidrs"`
	ProxySecretHeader string   `toml:"proxy_secret_header"`
	ProxySecret       string   `toml:"proxy_secret"`
	ProxyMTLSCAFile   string   `toml:"proxy_mtls_ca_file"`
}

type HTTP struct {
	ListenAddress  string        `toml:"listen_address"`
	BaseURL        string        `toml:"base_url"`
	RequestTimeout time.Duration `toml:"request_timeout"`
	WriteTimeout   time.Duration `toml:"write_timeout"`
	CORSOrigins    []string      `toml:"cors_origins"`
}

type Imports struct {
	ConcurrentWorkers int    `toml:"concurrent_workers"`
	FileLockPath      string `toml:"file_lock_path"`
}

type Thumbs struct {
	WorkerConcurrency int           `toml:"worker_concurrency"`
	PollInterval      time.Duration `toml:"poll_interval"`
	LeaseTimeout      time.Duration `toml:"lease_timeout"`
}

type Broker struct {
	Mode string     `toml:"mode"`
	Exec BrokerExec `toml:"exec"`
}

type BrokerExec struct {
	Command          string        `toml:"command"`
	PublishScopeArgs []string      `toml:"publish_scope_args"`
	RevokeScopeArgs  []string      `toml:"revoke_scope_args"`
	CallTimeout      time.Duration `toml:"call_timeout"`
	Env              []string      `toml:"env"`
}

type Backup struct {
	Enabled    bool   `toml:"enabled"`
	Dir        string `toml:"dir"`
	Keep15Min  int    `toml:"keep_15min"`
	KeepHourly int    `toml:"keep_hourly"`
	KeepDaily  int    `toml:"keep_daily"`
}

type Observability struct {
	AdminEnabled bool                 `toml:"admin_enabled"`
	AdminListen  string               `toml:"admin_listen"`
	PprofEnabled bool                 `toml:"pprof_enabled"`
	Logging      ObservabilityLogging `toml:"logging"`
}

type ObservabilityLogging struct {
	Format    string `toml:"format"`
	Level     string `toml:"level"`
	AddSource bool   `toml:"add_source"`
}

// Load reads the file at path, parses it as TOML, applies defaults,
// and returns the config. Returns an error if the file is missing or
// malformed; callers decide whether to exit.
func Load(path string) (*Config, error) {
	var cfg Config
	meta, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return nil, fmt.Errorf("load config %q: %w", path, err)
	}
	applyDefaults(&cfg, meta)
	applyEnvOverrides(&cfg)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// applyEnvOverrides layers a narrow set of environment-variable overrides
// onto the config. Semantics by field:
//   - FOTOBANK_PROXY_SECRET is a FALLBACK: TOML wins if non-empty, env
//     fills in otherwise (keeps secrets out of committed configs).
//   - FOTOBANK_DEV_HUB/USER_ID/HANDLE are OVERRIDES: env wins over TOML
//     (operator convenience for CI and testing).
func applyEnvOverrides(c *Config) {
	if c.Identity.Header.ProxySecret == "" {
		c.Identity.Header.ProxySecret = os.Getenv("FOTOBANK_PROXY_SECRET")
	}
	if v := os.Getenv("FOTOBANK_DEV_HUB"); v != "" {
		c.Identity.Stub.Hub = v
	}
	if v := os.Getenv("FOTOBANK_DEV_USER_ID"); v != "" {
		c.Identity.Stub.UserID = v
	}
	if v := os.Getenv("FOTOBANK_DEV_HANDLE"); v != "" {
		c.Identity.Stub.Handle = v
	}
}

// Validate returns ErrBadConfiguration (wrapped with detail) if any
// required setting is missing or any enum field holds an unknown value.
func (c *Config) Validate() error {
	if c.NAS.Root == "" {
		return fmt.Errorf("%w: [nas].root is required", errs.ErrBadConfiguration)
	}
	switch c.Identity.Mode {
	case "stub":
		// nothing extra
	case "header":
		if err := c.validateHeaderGuard(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: [identity].mode=%q (must be stub|header)",
			errs.ErrBadConfiguration, c.Identity.Mode)
	}
	switch c.Storage.Mode {
	case "nas_only", "flash_cache":
	default:
		return fmt.Errorf("%w: [storage].mode=%q (must be nas_only|flash_cache)",
			errs.ErrBadConfiguration, c.Storage.Mode)
	}
	switch c.Broker.Mode {
	case "stub":
		// nothing extra
	case "exec":
		if strings.TrimSpace(c.Broker.Exec.Command) == "" {
			return fmt.Errorf("%w: [broker.exec].command is required when mode=exec",
				errs.ErrBadConfiguration)
		}
		// call_timeout: 0 is the documented "use the default" sentinel
		// and follows fotobank's convention for zero-valued numeric
		// config (see originals_cache_days, worker_concurrency, etc.).
		// applyDefaults rewrites 0 to 30s before this arm runs, so a 0
		// the user typed and a missing key are indistinguishable here
		// and both produce a 30s effective timeout — there is no way
		// to express "no per-call timeout". Negative values are
		// rejected to defend against a future refactor that reorders
		// applyDefaults after Validate.
		if c.Broker.Exec.CallTimeout < 0 {
			return fmt.Errorf("%w: [broker.exec].call_timeout must be >= 0",
				errs.ErrBadConfiguration)
		}
	default:
		return fmt.Errorf("%w: [broker].mode=%q (must be stub|exec)",
			errs.ErrBadConfiguration, c.Broker.Mode)
	}
	// Retention validation only applies when the worker will actually run.
	// An operator who set enabled=false should be free to leave keep_*
	// at zero or unset; the values would never be consulted.
	if c.Backup.Enabled {
		if c.Backup.Keep15Min < 1 {
			return fmt.Errorf("%w: backup.keep_15min must be >= 1", errs.ErrBadConfiguration)
		}
		if c.Backup.KeepHourly < 1 {
			return fmt.Errorf("%w: backup.keep_hourly must be >= 1", errs.ErrBadConfiguration)
		}
		if c.Backup.KeepDaily < 1 {
			return fmt.Errorf("%w: backup.keep_daily must be >= 1", errs.ErrBadConfiguration)
		}
	}
	if c.Backup.Dir != "" && !filepath.IsAbs(c.Backup.Dir) {
		return fmt.Errorf("%w: backup.dir must be absolute when set", errs.ErrBadConfiguration)
	}
	if c.Observability.AdminEnabled {
		if !isLoopbackOrUnixListen(c.Observability.AdminListen) {
			return fmt.Errorf("%w: observability.admin_listen must be loopback (127.0.0.1, ::1) or unix:; got %q",
				errs.ErrBadConfiguration, c.Observability.AdminListen)
		}
	}
	switch c.Observability.Logging.Format {
	case "auto", "json", "text":
	default:
		return fmt.Errorf("%w: observability.logging.format=%q (must be auto|json|text)",
			errs.ErrBadConfiguration, c.Observability.Logging.Format)
	}
	switch c.Observability.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("%w: observability.logging.level=%q (must be debug|info|warn|error)",
			errs.ErrBadConfiguration, c.Observability.Logging.Level)
	}
	return nil
}

func (c *Config) validateHeaderGuard() error {
	h := c.Identity.Header
	if isLoopbackBind(c.HTTP.ListenAddress) ||
		len(h.TrustedProxyCIDRs) > 0 ||
		(h.ProxySecretHeader != "" && h.ProxySecret != "") ||
		h.ProxyMTLSCAFile != "" {
		return nil
	}
	return fmt.Errorf("%w: [identity].mode=header requires loopback bind, trusted_proxy_cidrs, proxy_secret (header + value), or mtls",
		errs.ErrBadConfiguration)
}

// isLoopbackBind reports whether addr is a loopback or UDS bind. It
// accepts numeric addresses only — names like "localhost" are rejected
// (no DNS at config-validate time). Callers who want "localhost" must
// use "127.0.0.1" or "[::1]" instead.
func isLoopbackBind(addr string) bool {
	if strings.HasPrefix(addr, "unix:") {
		return true
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isLoopbackOrUnixListen reports whether addr is a loopback TCP bind or
// a unix-socket path. The admin listener carries unauthenticated
// /metrics and optionally pprof, so non-loopback binds are rejected at
// validation time as defense in depth.
func isLoopbackOrUnixListen(addr string) bool {
	if strings.HasPrefix(addr, "unix:") {
		return true
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	switch host {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return false
}

func applyDefaults(c *Config, meta toml.MetaData) {
	if c.Flash.Root == "" {
		c.Flash.Root = defaultFlashRoot()
	}
	if c.Storage.Mode == "" {
		c.Storage.Mode = "flash_cache"
	}
	if c.Storage.OriginalsCacheDays == 0 {
		c.Storage.OriginalsCacheDays = 30
	}
	if c.Storage.OriginalsCacheMaxMedia == 0 {
		c.Storage.OriginalsCacheMaxMedia = 100_000
	}
	// Only default ThumbsCacheEnabled to true when it was not explicitly
	// set in the TOML; otherwise we would silently override a user's
	// explicit `thumbs_cache_enabled = false`.
	if !meta.IsDefined("storage", "thumbs_cache_enabled") {
		c.Storage.ThumbsCacheEnabled = true
	}
	if c.Identity.Mode == "" {
		c.Identity.Mode = "stub"
	}
	if c.Identity.Stub.Hub == "" {
		c.Identity.Stub.Hub = "dev-local"
	}
	if c.Identity.Stub.UserID == "" {
		c.Identity.Stub.UserID = "owner"
	}
	if c.Identity.Stub.Handle == "" {
		c.Identity.Stub.Handle = "owner"
	}
	if c.Identity.Header.UserIDHeader == "" {
		c.Identity.Header.UserIDHeader = "X-Auth-User-Id"
	}
	if c.Identity.Header.HubHeader == "" {
		c.Identity.Header.HubHeader = "X-Auth-Hub"
	}
	if c.Identity.Header.HandleHeader == "" {
		c.Identity.Header.HandleHeader = "X-Auth-Handle"
	}
	if c.Identity.Header.ScopesHeader == "" {
		c.Identity.Header.ScopesHeader = "X-Auth-Scopes"
	}
	if c.Identity.Header.RequestIDHeader == "" {
		c.Identity.Header.RequestIDHeader = "X-Auth-Request-Id"
	}
	if c.Identity.Header.ProxySecretHeader == "" {
		c.Identity.Header.ProxySecretHeader = "X-Auth-Proxy-Secret"
	}
	if c.HTTP.ListenAddress == "" {
		c.HTTP.ListenAddress = "127.0.0.1:8090"
	}
	if c.HTTP.RequestTimeout == 0 {
		c.HTTP.RequestTimeout = 30 * time.Second
	}
	if c.HTTP.WriteTimeout == 0 {
		c.HTTP.WriteTimeout = 60 * time.Second
	}
	if c.Imports.ConcurrentWorkers == 0 {
		c.Imports.ConcurrentWorkers = 2
	}
	if c.Thumbs.WorkerConcurrency == 0 {
		c.Thumbs.WorkerConcurrency = 4
	}
	if c.Thumbs.PollInterval == 0 {
		c.Thumbs.PollInterval = 5 * time.Second
	}
	if c.Thumbs.LeaseTimeout == 0 {
		c.Thumbs.LeaseTimeout = 10 * time.Minute
	}
	if c.Broker.Mode == "" {
		c.Broker.Mode = "stub"
	}
	if c.Broker.Exec.CallTimeout == 0 {
		c.Broker.Exec.CallTimeout = 30 * time.Second
	}
	// Default Enabled to true unless the operator explicitly set it.
	if !meta.IsDefined("backup", "enabled") {
		c.Backup.Enabled = true
	}
	// Default keep counts only when the operator did not set them; an
	// explicit 0 is preserved so Validate rejects it as out of range.
	if !meta.IsDefined("backup", "keep_15min") {
		c.Backup.Keep15Min = 4
	}
	if !meta.IsDefined("backup", "keep_hourly") {
		c.Backup.KeepHourly = 24
	}
	if !meta.IsDefined("backup", "keep_daily") {
		c.Backup.KeepDaily = 7
	}
	// Observability defaults: admin listener on by default at loopback
	// 9090; auto-format logging at info; pprof off; add_source off.
	// Treat the zero value of the parsed Observability table as "config
	// did not provide [observability]" and apply defaults wholesale.
	if c.Observability == (Observability{}) {
		c.Observability = Observability{
			AdminEnabled: true,
			AdminListen:  "127.0.0.1:9090",
			Logging: ObservabilityLogging{
				Format: "auto",
				Level:  "info",
			},
		}
	} else {
		if c.Observability.AdminListen == "" {
			c.Observability.AdminListen = "127.0.0.1:9090"
		}
		if c.Observability.Logging.Format == "" {
			c.Observability.Logging.Format = "auto"
		}
		if c.Observability.Logging.Level == "" {
			c.Observability.Logging.Level = "info"
		}
	}
}

func defaultFlashRoot() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "fotobank")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "fotobank")
	}
	return "./.fotobank-state"
}
