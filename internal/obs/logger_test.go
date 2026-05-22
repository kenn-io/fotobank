package obs

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewLogger_BufferAutoIsJSON(t *testing.T) {
	hermeticLoggerEnv(t)
	var buf bytes.Buffer
	lg := NewLogger(LoggerConfig{Format: "auto", Level: "info"}, &buf)
	lg.Info("hello", "k", "v")
	out := buf.String()
	require.True(t, strings.HasPrefix(strings.TrimSpace(out), "{"),
		"auto on a non-TTY writer must produce JSON; got %q", out)
}

func TestNewLogger_TerminalAutoIsText(t *testing.T) {
	hermeticLoggerEnv(t)
	prev := isTerminal
	isTerminal = func(uintptr) bool { return true }
	t.Cleanup(func() { isTerminal = prev })

	var ttyish ttyWriter
	lg := NewLogger(LoggerConfig{Format: "auto", Level: "info"}, &ttyish)
	lg.Info("hello", "k", "v")
	out := ttyish.buf.String()
	require.False(t, strings.HasPrefix(strings.TrimSpace(out), "{"),
		"auto on a TTY-shaped writer must produce text; got %q", out)
	require.Contains(t, out, "hello")
	require.Contains(t, out, "k=v")
}

func TestNewLogger_ForcedJSON(t *testing.T) {
	hermeticLoggerEnv(t)
	var buf bytes.Buffer
	lg := NewLogger(LoggerConfig{Format: "json", Level: "info"}, &buf)
	lg.Info("x")
	require.True(t, strings.HasPrefix(strings.TrimSpace(buf.String()), "{"))
}

func TestNewLogger_ForcedText(t *testing.T) {
	hermeticLoggerEnv(t)
	var buf bytes.Buffer
	lg := NewLogger(LoggerConfig{Format: "text", Level: "info"}, &buf)
	lg.Info("x")
	require.False(t, strings.HasPrefix(strings.TrimSpace(buf.String()), "{"))
}

func TestNewLogger_LevelDebugVisible(t *testing.T) {
	hermeticLoggerEnv(t)
	var buf bytes.Buffer
	lg := NewLogger(LoggerConfig{Format: "json", Level: "debug"}, &buf)
	lg.Debug("dbg")
	require.Contains(t, buf.String(), `"msg":"dbg"`)
}

func TestNewLogger_LevelInfoSuppressesDebug(t *testing.T) {
	hermeticLoggerEnv(t)
	var buf bytes.Buffer
	lg := NewLogger(LoggerConfig{Format: "json", Level: "info"}, &buf)
	lg.Debug("dbg")
	require.Empty(t, buf.String())
}

func TestNewLogger_EnvOverridesLevel(t *testing.T) {
	t.Setenv("FOTOBANK_LOG_LEVEL", "debug")
	var buf bytes.Buffer
	lg := NewLogger(LoggerConfig{Format: "json", Level: "info"}, &buf)
	lg.Debug("dbg")
	require.Contains(t, buf.String(), `"msg":"dbg"`)
}

func TestNewLogger_BogusEnvLevelFallsBackSilently(t *testing.T) {
	t.Setenv("FOTOBANK_LOG_LEVEL", "verbose")
	var buf bytes.Buffer
	lg := NewLogger(LoggerConfig{Format: "json", Level: "warn"}, &buf)
	// Bogus env keeps the configured warn level: info should be silent.
	lg.Info("info")
	require.Empty(t, buf.String())
	lg.Warn("warn")
	require.Contains(t, buf.String(), `"msg":"warn"`)
}

func TestNewLogger_AddSource(t *testing.T) {
	hermeticLoggerEnv(t)
	var buf bytes.Buffer
	lg := NewLogger(LoggerConfig{Format: "json", Level: "info", AddSource: true}, &buf)
	lg.Info("here")
	require.Contains(t, buf.String(), `"source"`)
}

// ttyWriter is an io.Writer that exposes Fd() so isTTYWriter type-
// asserts successfully. The stub `isTerminal` controls the answer.
type ttyWriter struct {
	buf bytes.Buffer
}

func (t *ttyWriter) Write(p []byte) (int, error) { return t.buf.Write(p) }
func (t *ttyWriter) Fd() uintptr                 { return 1 }

// Ensure slog uses our handler, not the default.
var _ slog.Handler = (*slog.JSONHandler)(nil)

// hermeticLoggerEnv clears FOTOBANK_LOG_LEVEL for the duration of t so
// a developer running the suite with the env set cannot perturb level
// expectations in tests that don't specifically exercise the override.
// Tests that intentionally drive the env path call t.Setenv themselves
// after this helper.
func hermeticLoggerEnv(t *testing.T) {
	t.Helper()
	t.Setenv("FOTOBANK_LOG_LEVEL", "")
}
