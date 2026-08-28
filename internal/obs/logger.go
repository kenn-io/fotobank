// Package obs is fotobank's observability layer: the canonical slog
// logger, a private metrics registry, and the admin HTTP listener that
// hosts /metrics, /readyz, and (optionally) pprof. See
// docs/architecture/operations.md.
package obs

import (
	"io"
	"log/slog"
	"os"

	"golang.org/x/term"
)

// LoggerConfig is the obs-package mirror of config.ObservabilityLogging.
// Kept independent from internal/config so tests in this package don't
// import the heavier config layer.
type LoggerConfig struct {
	Format    string // "auto" | "json" | "text"
	Level     string // "debug" | "info" | "warn" | "error"
	AddSource bool
}

// NewLogger constructs the canonical *slog.Logger. Format "auto" picks
// JSON when stderr is not a TTY, text otherwise; explicit "json"/"text"
// overrides. FOTOBANK_LOG_LEVEL overrides the configured level when
// set to a recognized value; bogus env values are silently ignored
// (the configured level wins). Tests pass a bytes.Buffer-shaped writer
// to deterministically get JSON.
func NewLogger(cfg LoggerConfig, stderr io.Writer) *slog.Logger {
	level := parseLevel(cfg.Level)
	if env := os.Getenv("FOTOBANK_LOG_LEVEL"); env != "" {
		if l, ok := tryParseLevel(env); ok {
			level = l
		}
	}
	format := cfg.Format
	if format == "auto" {
		if isTTYWriter(stderr) {
			format = "text"
		} else {
			format = "json"
		}
	}
	opts := &slog.HandlerOptions{Level: level, AddSource: cfg.AddSource}
	if format == "json" {
		return slog.New(slog.NewJSONHandler(stderr, opts))
	}
	return slog.New(slog.NewTextHandler(stderr, opts))
}

// parseLevel converts a config-validated level string. Validation
// already ensures the value is one of the four; an unknown value here
// would be a programmer error and we conservatively return Info.
func parseLevel(s string) slog.Level {
	if l, ok := tryParseLevel(s); ok {
		return l
	}
	return slog.LevelInfo
}

func tryParseLevel(s string) (slog.Level, bool) {
	switch s {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	}
	return 0, false
}

// IsValidLogLevel reports whether s is a recognized level string.
// Used by callers (cli/server.go) to warn the operator when an env
// override like FOTOBANK_LOG_LEVEL is bogus and was therefore ignored
// by NewLogger. NewLogger itself swallows the bad value silently
// because at construction time there is no logger yet to emit on.
func IsValidLogLevel(s string) bool {
	_, ok := tryParseLevel(s)
	return ok
}

// isTerminal is the test seam over term.IsTerminal. Tests stub it to
// force terminal/non-terminal decisions independent of the actual
// stdout/stderr the test harness inherits.
var isTerminal = func(fd uintptr) bool { return term.IsTerminal(int(fd)) }

func isTTYWriter(w io.Writer) bool {
	f, ok := w.(interface{ Fd() uintptr })
	return ok && isTerminal(f.Fd())
}
