// Command e2e-server boots a fotobank server against a freshly created
// temp-file SQLite database so Playwright can run smoke tests against
// the embedded SPA. It writes a minimal TOML config under a temp dir,
// then delegates to internal/cli the same way the production fotobank
// binary does.
//
// The server listens on 127.0.0.1:8080 deterministically so the
// Playwright webServer config in the frontend package can target a
// fixed URL.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/wesm/fotobank/internal/cli"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	tmp, err := os.MkdirTemp("", "fotobank-e2e-")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	cfgPath := filepath.Join(tmp, "fotobank.toml")
	nasRoot := filepath.Join(tmp, "nas")
	flashRoot := filepath.Join(tmp, "flash")
	for _, d := range []string{nasRoot, flashRoot} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("creating %s: %w", d, err)
		}
	}
	cfg := fmt.Sprintf(`
[nas]
root = "%s"
[flash]
root = "%s"
[identity]
mode = "stub"
[identity.stub]
hub = "local"
user_id = "alice"
handle = "Alice"
storage_key = "alice-sk"
[http]
listen_address = "127.0.0.1:8080"
[imports]
file_lock_path = "%s"
[backup]
enabled = false
[observability]
admin_listen = "127.0.0.1:0"
`, nasRoot, flashRoot, filepath.Join(tmp, "import.lock"))
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	if code := cli.RunContext(ctx, []string{"server", "--config", cfgPath}, os.Stdout, os.Stderr); code != 0 {
		return fmt.Errorf("server exited with code %d", code)
	}
	return nil
}
