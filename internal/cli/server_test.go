package cli_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/cli"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
)

func TestServerRespondsToHealthz(t *testing.T) {
	r := require.New(t)

	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	r.NoError(os.MkdirAll(nasRoot, 0o700))

	cfgPath := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[http]
listen_address = "127.0.0.1:0"
`, nasRoot, filepath.Join(tmp, "flash")), 0o600))

	addrFile := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", addrFile)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan int, 1)
	go func() {
		var out, eout bytes.Buffer
		errCh <- cli.RunContext(ctx, []string{"server", "--config", cfgPath}, &out, &eout)
	}()

	var resolved string
	for range 100 {
		if b, err := os.ReadFile(addrFile); err == nil && len(b) > 0 {
			resolved = strings.TrimSpace(string(b))
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.NotEmpty(resolved, "server never published its bind address")

	resp, err := http.Get("http://" + resolved + "/api/v1/healthz")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	cancel()
	select {
	case code := <-errCh:
		r.Equal(0, code)
	case <-time.After(5 * time.Second):
		r.Fail("server did not shut down within 5s")
	}
}

func TestServerDrainsPendingThumbRow(t *testing.T) {
	// Smoke test: seed a ready JPEG row pre-import, boot the server,
	// poll until thumb_status becomes 'ready' (worker has drained it).
	// Uses the existing photo-with-timestamp fixture as the source.
	r := require.New(t)
	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	r.NoError(os.MkdirAll(filepath.Join(nasRoot, "u", "2024"), 0o700))
	// Place a source JPEG where NASOnly expects it.
	fixture, err := os.ReadFile(filepath.Join("..", "..", "testdata", "exif", "photo-with-timestamp.jpg"))
	r.NoError(err)
	srcPath := filepath.Join(nasRoot, "u", "2024", "a.jpg")
	r.NoError(os.WriteFile(srcPath, fixture, 0o600))

	cfgPath := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[identity]
mode = "stub"
[identity.stub]
hub = "h"
user_id = "u"
[http]
listen_address = "127.0.0.1:0"
[thumbs]
poll_interval = "20ms"
worker_concurrency = 1
`, nasRoot, filepath.Join(tmp, "flash")), 0o600))

	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	// Migrate the DB by opening+closing once, then insert a pending row.
	d, err := db.Open(dbPath)
	r.NoError(err)
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err = d.WriteDB().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "u", time.Now().UTC(),
	)
	r.NoError(err)
	mediaID := uuid.NewString()
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: mediaID, Owner: p, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "2024/a.jpg", ImportedAt: time.Now().UTC(),
		Size: int64(len(fixture)), Checksum: mediaID, ThumbStatus: "pending",
	}))
	_ = d.Close()

	addrFile := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", addrFile)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan int, 1)
	go func() {
		var out, eout bytes.Buffer
		errCh <- cli.RunContext(ctx, []string{"server", "--config", cfgPath}, &out, &eout)
	}()

	// Wait for boot.
	for range 100 {
		if b, err := os.ReadFile(addrFile); err == nil && len(b) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Poll DB until the worker drains the row.
	deadline := time.Now().Add(10 * time.Second)
	d2, err := db.Open(dbPath)
	r.NoError(err)
	repo2 := media.NewRepo(d2.WriteDB(), d2.ReadDB())
	for time.Now().Before(deadline) {
		got, err := repo2.GetByID(context.Background(), mediaID)
		r.NoError(err)
		if got.ThumbStatus == "ready" {
			_ = d2.Close()
			cancel()
			<-errCh
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = d2.Close()
	cancel()
	<-errCh
	r.Fail("worker did not drain pending row within 10s")
}

func TestFlashJanitorLeavesSiblingFlashStateAlone(t *testing.T) {
	// Regression: FlashCache.Evict walks its root and prunes by age. The
	// cache root must be a dedicated subdirectory of cfg.Flash.Root so
	// sibling state files (sqlite DB, WAL, shm, etc.) are never swept.
	r := require.New(t)

	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	flashRoot := filepath.Join(tmp, "flash")
	r.NoError(os.MkdirAll(nasRoot, 0o700))
	r.NoError(os.MkdirAll(flashRoot, 0o700))

	// Seed a sentinel file directly under flashRoot with a very old
	// mtime. If Evict ever walks flashRoot itself, this sentinel is the
	// first thing it would unlink.
	sentinel := filepath.Join(flashRoot, "sqlite-sentinel")
	r.NoError(os.WriteFile(sentinel, []byte("db bytes"), 0o600))
	old := time.Now().Add(-365 * 24 * time.Hour)
	r.NoError(os.Chtimes(sentinel, old, old))

	cfgPath := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[storage]
mode = "flash_cache"
originals_cache_days = 1
[http]
listen_address = "127.0.0.1:0"
`, nasRoot, flashRoot), 0o600))

	addrFile := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", addrFile)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	errCh := make(chan int, 1)
	go func() {
		var out, eout bytes.Buffer
		errCh <- cli.RunContext(ctx, []string{"server", "--config", cfgPath}, &out, &eout)
	}()

	// Wait for server boot (the synchronous startup Evict runs before
	// Serve), then confirm the sentinel survived.
	for range 100 {
		if b, err := os.ReadFile(addrFile); err == nil && len(b) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, statErr := os.Stat(sentinel)
	r.NoError(statErr, "flash janitor removed a sibling state file at %s", sentinel)

	cancel()
	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		r.Fail("server did not shut down within 5s")
	}
}

func TestServerListensOnUnixSocket(t *testing.T) {
	// Regression: [http].listen_address may be "unix:/path/to/sock",
	// which Validate accepts. runServer must bind a Unix socket rather
	// than pass the literal string to net.Listen("tcp", ...).
	r := require.New(t)

	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	r.NoError(os.MkdirAll(nasRoot, 0o700))
	sockPath := filepath.Join(tmp, "s.sock") // short enough for macOS's sun_path cap

	cfgPath := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[http]
listen_address = "unix:%s"
`, nasRoot, filepath.Join(tmp, "flash"), sockPath), 0o600))

	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan int, 1)
	go func() {
		var out, eout bytes.Buffer
		errCh <- cli.RunContext(ctx, []string{"server", "--config", cfgPath}, &out, &eout)
	}()

	// Poll until the socket file appears (server has bound).
	var bound bool
	for range 100 {
		if _, err := os.Stat(sockPath); err == nil {
			bound = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.True(bound, "server never bound the Unix socket at %s", sockPath)

	// Issue a request over the socket and confirm /healthz responds.
	tr := &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", sockPath)
		},
	}
	client := &http.Client{Transport: tr}
	resp, err := client.Get("http://fotobank/api/v1/healthz")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	cancel()
	select {
	case code := <-errCh:
		r.Equal(0, code)
	case <-time.After(5 * time.Second):
		r.Fail("server did not shut down within 5s")
	}
}

func TestServerListenOverrideIsRevalidated(t *testing.T) {
	// Regression: --listen used to mutate cfg.HTTP.ListenAddress after
	// Validate ran. In header mode with no guard, the file-only config
	// validates because of the loopback bind; overriding to a public
	// address would start successfully and only fail requests. Ensure
	// the CLI re-validates and exits 1 when the override would break
	// the identity guard.
	r := require.New(t)

	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	r.NoError(os.MkdirAll(nasRoot, 0o700))

	cfgPath := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[identity]
mode = "header"
[http]
listen_address = "127.0.0.1:0"
`, nasRoot, filepath.Join(tmp, "flash")), 0o600))

	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))
	// Neutralise any inherited secret so the guard's re-validation
	// fails deterministically.
	t.Setenv("FOTOBANK_PROXY_SECRET", "")

	var out, eout bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"server", "--config", cfgPath, "--listen", "0.0.0.0:0"},
		&out, &eout)
	r.Equal(1, code)
	r.Contains(eout.String(), "header")
}
