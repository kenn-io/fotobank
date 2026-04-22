// Package config loads fotobank's TOML configuration file and applies
// defaults. Environment-variable overrides will be layered in a later task.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/wesm/fotobank/internal/errs"
)

type Config struct {
	Flash    Flash    `toml:"flash"`
	NAS      NAS      `toml:"nas"`
	Storage  Storage  `toml:"storage"`
	Identity Identity `toml:"identity"`
	HTTP     HTTP     `toml:"http"`
	Imports  Imports  `toml:"imports"`
	Thumbs   Thumbs   `toml:"thumbs"`
	Broker   Broker   `toml:"broker"`
	Backup   Backup   `toml:"backup"`
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
	Command           string   `toml:"command"`
	RegisterScopeArgs []string `toml:"register_scope_args"`
}

type Backup struct {
	SnapshotInterval  time.Duration `toml:"snapshot_interval"`
	SnapshotRetention int           `toml:"snapshot_retention"`
	WALShipping       bool          `toml:"wal_shipping"`
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
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
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
	case "stub", "exec":
	default:
		return fmt.Errorf("%w: [broker].mode=%q (must be stub|exec)",
			errs.ErrBadConfiguration, c.Broker.Mode)
	}
	return nil
}

func (c *Config) validateHeaderGuard() error {
	h := c.Identity.Header
	// Reading FOTOBANK_PROXY_SECRET here is provisional: Task 13 will
	// layer env overrides directly into ProxySecret, at which point this
	// os.Getenv call can be removed. Until then, accepting env at this
	// point keeps secrets out of committed TOML files.
	if isLoopbackBind(c.HTTP.ListenAddress) ||
		len(h.TrustedProxyCIDRs) > 0 ||
		(h.ProxySecretHeader != "" && (h.ProxySecret != "" || os.Getenv("FOTOBANK_PROXY_SECRET") != "")) ||
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
	if c.Backup.SnapshotInterval == 0 {
		c.Backup.SnapshotInterval = 15 * time.Minute
	}
	if c.Backup.SnapshotRetention == 0 {
		c.Backup.SnapshotRetention = 96
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
