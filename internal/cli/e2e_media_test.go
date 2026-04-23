package cli_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/cli"
)

// TestE2EMediaPipeline exercises the full import -> list -> stream path
// against a real server: the `import` subcommand populates the DB and
// NAS, then the `server` subcommand serves the three rows over the HTTP
// API. The test asserts that /api/v1/media returns all three, the
// detail endpoint returns each row, and /original streams the exact
// fixture bytes (verified by MD5). Finally it cancels ctx and asserts a
// clean zero-code shutdown.
func TestE2EMediaPipeline(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	flashRoot := filepath.Join(tmp, "flash")
	r.NoError(os.MkdirAll(nasRoot, 0o700))
	r.NoError(os.MkdirAll(flashRoot, 0o700))

	cfg := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(cfg, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[identity]
mode = "stub"
[identity.stub]
hub = "local"
user_id = "alice"
handle = "Alice"
storage_key = "alice-sk"
[http]
listen_address = "127.0.0.1:0"
[imports]
file_lock_path = %q
`, nasRoot, flashRoot, filepath.Join(tmp, "import.lock")), 0o600))

	t.Setenv("FOTOBANK_CONFIG", cfg)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))
	addrSink := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", addrSink)

	src := seedImportSource(t,
		"photo-with-timestamp.jpg",
		"photo-no-exif.jpg",
		"video.mp4",
	)

	// 1. Import three fixtures. Run with a fresh context so the import
	// finishes before the server starts observing the test context.
	var impOut, impErr bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"import", "--config", cfg, src},
		&impOut, &impErr)
	r.Equal(0, code, "import failed: stdout=%s stderr=%s", impOut.String(), impErr.String())
	r.Contains(impOut.String(), "imported=3")

	// 2. Boot the server in a goroutine.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan int, 1)
	go func() {
		var so, se bytes.Buffer
		done <- cli.RunContext(ctx, []string{"server"}, &so, &se)
	}()

	var addr string
	for range 100 {
		if b, err := os.ReadFile(addrSink); err == nil && len(b) > 0 {
			addr = strings.TrimSpace(string(b))
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	r.NotEmpty(addr, "server did not publish bind address")

	client := &http.Client{Timeout: 5 * time.Second}
	base := "http://" + addr

	// 3. List media: expect three items.
	type mediaItem struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		MimeType string `json:"mime_type"`
		Path     string `json:"path"`
		Size     int64  `json:"size"`
		Checksum string `json:"checksum"`
	}
	type listBody struct {
		Items      []mediaItem `json:"items"`
		NextOffset *int        `json:"next_offset,omitempty"`
		Total      *int        `json:"total,omitempty"`
	}

	listResp, err := client.Get(base + "/api/v1/media")
	r.NoError(err)
	r.Equal(http.StatusOK, listResp.StatusCode)
	var lb listBody
	r.NoError(json.NewDecoder(listResp.Body).Decode(&lb))
	r.NoError(listResp.Body.Close())
	r.Len(lb.Items, 3, "expected three imported media rows")

	// Build the expected checksum -> fixture filename map from on-disk
	// fixtures so we don't hard-code sums that could drift.
	fixtureSums := map[string]string{}
	fixtureDir := importFixtureDir(t)
	for _, name := range []string{"photo-with-timestamp.jpg", "photo-no-exif.jpg", "video.mp4"} {
		fixtureSums[md5OfFile(t, filepath.Join(fixtureDir, name))] = name
	}

	gotSums := map[string]mediaItem{}
	for _, it := range lb.Items {
		r.NotEmpty(it.ID)
		r.NotEmpty(it.Path)
		r.NotEmpty(it.Checksum)
		gotSums[it.Checksum] = it
	}
	for sum, name := range fixtureSums {
		_, ok := gotSums[sum]
		r.Truef(ok, "fixture %s (md5=%s) missing from /media listing", name, sum)
	}

	// 4. Detail endpoint: fetch each item by ID.
	for _, it := range lb.Items {
		detailResp, err := client.Get(base + "/api/v1/media/" + it.ID)
		r.NoError(err)
		r.Equal(http.StatusOK, detailResp.StatusCode)
		var detail mediaItem
		r.NoError(json.NewDecoder(detailResp.Body).Decode(&detail))
		r.NoError(detailResp.Body.Close())
		r.Equal(it.ID, detail.ID)
		r.Equal(it.Checksum, detail.Checksum)
		r.Equal(it.Path, detail.Path)
		r.Equal(it.Size, detail.Size)
	}

	// 5. Stream the original bytes for each item and verify the MD5
	// matches the on-disk fixture. Also spot-check headers on each
	// response (ETag is the quoted checksum; Content-Length matches
	// the fixture size on-disk).
	for _, it := range lb.Items {
		fixtureName, ok := fixtureSums[it.Checksum]
		r.Truef(ok, "unexpected checksum %q in /media", it.Checksum)
		fixturePath := filepath.Join(fixtureDir, fixtureName)
		fixtureInfo, err := os.Stat(fixturePath)
		r.NoError(err)

		origResp, err := client.Get(base + "/api/v1/media/" + it.ID + "/original")
		r.NoError(err)
		r.Equal(http.StatusOK, origResp.StatusCode)
		r.Equal(`"`+it.Checksum+`"`, origResp.Header.Get("ETag"))
		r.Equal(fmt.Sprintf("%d", fixtureInfo.Size()), origResp.Header.Get("Content-Length"))
		gotBody, err := io.ReadAll(origResp.Body)
		closeErr := origResp.Body.Close()
		r.NoError(err)
		r.NoError(closeErr)
		sum := md5.Sum(gotBody)
		r.Equalf(it.Checksum, hex.EncodeToString(sum[:]),
			"streamed body md5 mismatch for fixture %s", fixtureName)
	}

	// 6. Cancel and assert the server exits cleanly.
	cancel()
	select {
	case code := <-done:
		r.Equal(0, code)
	case <-time.After(5 * time.Second):
		r.Fail("server did not shut down")
	}
}

// md5OfFile returns the hex MD5 of the given file's contents. Used to
// compute expected checksums from on-disk fixtures so the test doesn't
// rely on magic string literals.
func md5OfFile(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	h := md5.New()
	_, err = io.Copy(h, f)
	require.NoError(t, err)
	return hex.EncodeToString(h.Sum(nil))
}
