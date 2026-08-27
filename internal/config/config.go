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
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/search"
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
	Docbank       Docbank       `toml:"docbank"`
	NAS           NAS           `toml:"nas"`
	Storage       Storage       `toml:"storage"`
	Identity      Identity      `toml:"identity"`
	HTTP          HTTP          `toml:"http"`
	Imports       Imports       `toml:"imports"`
	Thumbs        Thumbs        `toml:"thumbs"`
	Broker        Broker        `toml:"broker"`
	Backup        Backup        `toml:"backup"`
	Observability Observability `toml:"observability"`
	Admin         Admin         `toml:"admin"`
	AI            ai.Config     `toml:"ai"`
	Search        search.Config `toml:"search"`
	UI            UI            `toml:"ui"`
}

type Admin struct {
	Principals []AdminPrincipal `toml:"principals"`
}

type AdminPrincipal struct {
	Hub    string `toml:"hub"`
	UserID string `toml:"user_id"`
}

type UI struct {
	SharingEnabled bool `toml:"sharing_enabled"`
}

type Flash struct {
	Root string `toml:"root"`
}

type Docbank struct {
	Root string `toml:"root"`
}

const (
	FlashOriginalsCacheDir = "originals"
	FlashThumbsCacheDir    = "thumbs"
)

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
	// DevInsecureCookies enables non-Secure cookie issuance for HTTP
	// loopback dev/e2e. Production deployments leave this false; the
	// server then issues __Host-fotobank-hidden with Secure unconditionally.
	// We do NOT sniff X-Forwarded-Proto — that opens a downgrade path
	// against an untrusted reverse proxy.
	DevInsecureCookies bool `toml:"dev_insecure_cookies"`
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
	cfg, err := LoadUnchecked(path)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// LoadUnchecked reads the config file, applies defaults/env/path
// expansion, and skips final validation. Runtime override providers use
// this to merge DB-backed overrides before validating the effective
// configuration. Normal callers should use Load.
func LoadUnchecked(path string) (*Config, error) {
	var cfg Config
	meta, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return nil, fmt.Errorf("load config %q: %w", path, err)
	}
	applyDefaults(&cfg, meta)
	applyEnvOverrides(&cfg)
	if err := expandHomePaths(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// expandHomePaths rewrites filesystem-path config fields so a leading
// "~" or "~/" expands to $HOME. The example config documents this
// expansion as a feature; without it a fresh-install user typing
// `root = "~/fotobank"` ends up with a literal "~" subdirectory of
// the working directory, with photos and DBs landing in the wrong
// place. URL-shaped fields (base_url, AI endpoints, CORS origins) are
// left alone — they don't carry filesystem paths.
func expandHomePaths(c *Config) error {
	fields := []*string{
		&c.Flash.Root,
		&c.Docbank.Root,
		&c.NAS.Root,
		&c.Imports.FileLockPath,
		&c.Identity.Header.ProxyMTLSCAFile,
	}
	for _, p := range fields {
		expanded, err := expandHome(*p)
		if err != nil {
			return err
		}
		*p = expanded
	}
	return nil
}

// expandHome returns p with a leading "~", "~/", or "~\" replaced by $HOME.
// Empty strings, absolute paths, and relative paths that don't start
// with "~" pass through unchanged. The "~user" form is rejected with
// an explicit error so a config of `root = "~alice/photos"` doesn't
// silently fall through to a literal `~alice/photos` directory under
// CWD — we only support the current user's home, matching shell
// defaults for `~`.
func expandHome(p string) (string, error) {
	if p == "" {
		return p, nil
	}
	if !strings.HasPrefix(p, "~") {
		return p, nil
	}
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return "", fmt.Errorf("path %q: ~user form is not supported, use ~ or ~/<rest>", p)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand %q: %w", p, err)
	}
	if p == "~" {
		return home, nil
	}
	return filepath.Join(home, p[2:]), nil
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
	if c.Docbank.Root == "" {
		return fmt.Errorf("%w: [docbank].root is required", errs.ErrBadConfiguration)
	}
	docbankRoot, err := canonicalConfigPath(c.Docbank.Root)
	if err != nil {
		return fmt.Errorf("%w: canonicalize [docbank].root: %v", errs.ErrBadConfiguration, err)
	}
	nasRoot, err := canonicalConfigPath(c.NAS.Root)
	if err != nil {
		return fmt.Errorf("%w: canonicalize [nas].root: %v", errs.ErrBadConfiguration, err)
	}
	flashRoot, err := canonicalConfigPath(c.Flash.Root)
	if err != nil {
		return fmt.Errorf("%w: canonicalize [flash].root: %v", errs.ErrBadConfiguration, err)
	}
	c.Docbank.Root = docbankRoot
	c.NAS.Root = nasRoot
	c.Flash.Root = flashRoot
	if pathsOverlap(docbankRoot, nasRoot) {
		return fmt.Errorf("%w: [docbank].root and [nas].root must not overlap", errs.ErrBadConfiguration)
	}
	if pathContains(docbankRoot, flashRoot) {
		return fmt.Errorf("%w: [docbank].root must not contain [flash].root", errs.ErrBadConfiguration)
	}
	for _, cacheDir := range []string{FlashOriginalsCacheDir, FlashThumbsCacheDir} {
		cacheRoot, err := canonicalConfigPath(filepath.Join(flashRoot, cacheDir))
		if err != nil {
			return fmt.Errorf("%w: canonicalize [flash].root/%s: %v",
				errs.ErrBadConfiguration, cacheDir, err)
		}
		if pathsOverlap(docbankRoot, cacheRoot) {
			return fmt.Errorf("%w: [docbank].root must not overlap [flash].root/%s",
				errs.ErrBadConfiguration, cacheDir)
		}
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
	for i, p := range c.Admin.Principals {
		if strings.TrimSpace(p.Hub) == "" || strings.TrimSpace(p.UserID) == "" {
			return fmt.Errorf("%w: admin.principals[%d] hub and user_id are required",
				errs.ErrBadConfiguration, i)
		}
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
	if err := c.AI.Validate(); err != nil {
		return fmt.Errorf("%w: %s", errs.ErrBadConfiguration, err)
	}
	if err := c.Search.Validate(); err != nil {
		return fmt.Errorf("%w: %s", errs.ErrBadConfiguration, err)
	}
	return nil
}

func canonicalConfigPath(value string) (string, error) {
	target := value
	if !filepath.IsAbs(target) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		target = cwd + string(os.PathSeparator) + target
	}
	current := strings.TrimRight(target, string(os.PathSeparator))
	if current == "" {
		current = string(os.PathSeparator)
	}
	var missing []string
	for {
		_, lstatErr := os.Lstat(current)
		if lstatErr == nil {
			break
		}
		if !errors.Is(lstatErr, os.ErrNotExist) {
			return "", lstatErr
		}
		parent, component := rawPathParent(current)
		if parent == current {
			return "", lstatErr
		}
		if component == "." || component == ".." {
			return "", fmt.Errorf("path traverses %q after a missing component", component)
		}
		missing = append(missing, component)
		current = parent
	}
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil {
		return "", err
	}
	slices.Reverse(missing)
	return filepath.Join(append([]string{resolved}, missing...)...), nil
}

func rawPathParent(value string) (string, string) {
	volume := filepath.VolumeName(value)
	remainder := value[len(volume):]
	remainder = strings.TrimRight(remainder, string(os.PathSeparator))
	index := strings.LastIndex(remainder, string(os.PathSeparator))
	if index < 0 {
		return value, ""
	}
	component := remainder[index+1:]
	parentRemainder := strings.TrimRight(remainder[:index], string(os.PathSeparator))
	if parentRemainder == "" {
		parentRemainder = string(os.PathSeparator)
	}
	return volume + parentRemainder, component
}

func pathContains(parent, candidate string) bool {
	rel, err := filepath.Rel(parent, candidate)
	if err == nil && (rel == "." ||
		(rel != ".." &&
			!strings.HasPrefix(rel, ".."+string(os.PathSeparator)))) {
		return true
	}
	return pathContainsFold(parent, candidate)
}

// pathContainsFold rejects case-only aliases on case-insensitive filesystems.
// Applying the rule on every platform also keeps a configuration portable
// between a case-sensitive development machine and a case-insensitive NAS.
func pathContainsFold(parent, candidate string) bool {
	parentVolume, parentParts := pathParts(parent)
	candidateVolume, candidateParts := pathParts(candidate)
	if !strings.EqualFold(parentVolume, candidateVolume) || len(parentParts) > len(candidateParts) {
		return false
	}
	for i, part := range parentParts {
		if !strings.EqualFold(part, candidateParts[i]) {
			return false
		}
	}
	return true
}

func pathParts(value string) (string, []string) {
	clean := filepath.Clean(value)
	volume := filepath.VolumeName(clean)
	remainder := strings.TrimPrefix(clean, volume)
	remainder = strings.Trim(remainder, string(os.PathSeparator))
	if remainder == "" {
		return volume, []string{}
	}
	return volume, strings.Split(remainder, string(os.PathSeparator))
}

func pathsOverlap(left, right string) bool {
	return pathContains(left, right) || pathContains(right, left)
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
// validation time as defense in depth. We accept only literal loopback
// IPs (127.0.0.1, ::1, expanded forms) and unix: paths — `localhost` is
// rejected because /etc/hosts mappings can vary and could resolve to a
// non-loopback address in unusual environments.
func isLoopbackOrUnixListen(addr string) bool {
	// `unix:` prefix has no host:port shape; check first so SplitHostPort
	// does not treat the path as a port.
	if strings.HasPrefix(addr, "unix:") {
		return true
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}

func applyDefaults(c *Config, meta toml.MetaData) {
	if c.Flash.Root == "" {
		c.Flash.Root = defaultFlashRoot()
	}
	if c.Docbank.Root == "" {
		c.Docbank.Root = filepath.Join(c.Flash.Root, "docbank")
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
	if c.Identity.Mode == "stub" && !meta.IsDefined("admin", "principals") {
		c.Admin.Principals = []AdminPrincipal{{
			Hub:    c.Identity.Stub.Hub,
			UserID: c.Identity.Stub.UserID,
		}}
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
	// Use meta.IsDefined so an operator who writes [observability] for
	// other fields (e.g. pprof_enabled) still gets AdminEnabled=true
	// unless they explicitly set admin_enabled=false.
	if !meta.IsDefined("observability", "admin_enabled") {
		c.Observability.AdminEnabled = true
	}
	if c.Observability.AdminListen == "" {
		c.Observability.AdminListen = "127.0.0.1:9090"
	}
	if c.Observability.Logging.Format == "" {
		c.Observability.Logging.Format = "auto"
	}
	if c.Observability.Logging.Level == "" {
		c.Observability.Logging.Level = "info"
	}
	c.AI.ApplyDefaults()
	c.Search.ApplyDefaults()
}

// defaultFlashRoot returns the default location for the SQLite DB,
// flash cache, and operational state. The default is "~/.fotobank"
// — a single hidden directory next to the user's photos in
// "~/fotobank". XDG_STATE_HOME, when set, still wins so containerized
// or sandboxed deployments can route state under their normal
// XDG-spec layout, but the bare-shell default is the simpler one.
func defaultFlashRoot() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "fotobank")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".fotobank")
	}
	return "./.fotobank"
}
