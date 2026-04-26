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
	"strconv"
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
[thumbs]
poll_interval = "100ms"
[observability]
admin_listen = "127.0.0.1:0"
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
		ID           string `json:"id"`
		Type         string `json:"type"`
		MimeType     string `json:"mime_type"`
		Path         string `json:"path"`
		Size         int64  `json:"size"`
		Checksum     string `json:"checksum"`
		ThumbStatus  string `json:"thumb_status"`
		ThumbVersion int    `json:"thumb_version"`
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

	// 7. Wait for the thumbnail worker to drain each imported row to a
	// terminal state. Photos should become "ready"; the video should
	// settle on "no_preview" (Plan C defers video posters to Plan E).
	waitAllTerminal := func() []mediaItem {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			resp, err := client.Get(base + "/api/v1/media")
			r.NoError(err)
			var body listBody
			decErr := json.NewDecoder(resp.Body).Decode(&body)
			_ = resp.Body.Close()
			r.NoError(decErr)
			allTerminal := len(body.Items) == 3
			for _, it := range body.Items {
				switch it.ThumbStatus {
				case "ready", "no_preview", "failed":
				default:
					allTerminal = false
				}
			}
			if allTerminal {
				return body.Items
			}
			time.Sleep(100 * time.Millisecond)
		}
		r.Fail("thumb worker did not drain all rows within 15s")
		return nil
	}
	items := waitAllTerminal()

	// Separate photos from video; assert per-type terminal status.
	type photoRow struct {
		ID      string
		Version int
	}
	var photos []photoRow
	for _, it := range items {
		if it.Type == "photo" {
			r.Equalf("ready", it.ThumbStatus, "photo %s should be ready", it.ID)
			photos = append(photos, photoRow{ID: it.ID, Version: it.ThumbVersion})
			continue
		}
		r.Equalf("no_preview", it.ThumbStatus, "video %s should be no_preview", it.ID)
	}
	r.Len(photos, 2, "expected two photo rows")

	// 8. Fetch grid thumb for each photo; assert 200, ETag, immutable cache.
	for _, p := range photos {
		url := base + "/api/v1/media/" + p.ID + "/thumb?size=grid&v=" + strconv.Itoa(p.Version)
		resp, err := client.Get(url)
		r.NoError(err)
		r.Equalf(http.StatusOK, resp.StatusCode, "url=%s", url)
		r.Equal(`"`+p.ID+`-grid-v`+strconv.Itoa(p.Version)+`"`, resp.Header.Get("ETag"))
		r.Contains(resp.Header.Get("Cache-Control"), "immutable")
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		r.NoError(err)
		r.Greater(len(body), 100, "thumb body suspiciously small")
	}

	// Record each row's version before regenerate so we can detect the bump.
	oldVersions := map[string]int{}
	for _, it := range items {
		oldVersions[it.ID] = it.ThumbVersion
	}

	// 9. Regenerate everything. All 3 rows get bumped (the video re-settles
	// to no_preview at the new version, but still counts as enqueued).
	{
		var out, eout bytes.Buffer
		code := cli.RunContext(context.Background(),
			[]string{"thumbs", "regenerate", "--all", "--config", cfg}, &out, &eout)
		r.Equalf(0, code, "regenerate stderr=%s", eout.String())
		r.Contains(out.String(), "3 rows enqueued")
	}

	// 10. Old URL: 404 immediately with no-store cache directive so clients
	// don't cache a stale-version miss past the worker's reprocess window.
	oldURL := base + "/api/v1/media/" + photos[0].ID +
		"/thumb?size=grid&v=" + strconv.Itoa(photos[0].Version)
	staleResp, err := client.Get(oldURL)
	r.NoError(err)
	_ = staleResp.Body.Close()
	r.Equal(http.StatusNotFound, staleResp.StatusCode)
	r.Equal("no-store", staleResp.Header.Get("Cache-Control"),
		"404 must be no-store to avoid stale caching")

	// 11. All rows: eventually each lands at v+1 with the expected terminal
	// status. This catches photos[1] and the video not re-draining.
	waitAllRebumped := func() []mediaItem {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			resp, err := client.Get(base + "/api/v1/media")
			r.NoError(err)
			var body listBody
			decErr := json.NewDecoder(resp.Body).Decode(&body)
			_ = resp.Body.Close()
			r.NoError(decErr)
			allRebumped := len(body.Items) == 3
			for _, it := range body.Items {
				wantTerminal := "ready"
				if it.Type == "video" {
					wantTerminal = "no_preview"
				}
				if it.ThumbStatus != wantTerminal || it.ThumbVersion != oldVersions[it.ID]+1 {
					allRebumped = false
					break
				}
			}
			if allRebumped {
				return body.Items
			}
			time.Sleep(100 * time.Millisecond)
		}
		r.Fail("not all rows re-drained at v+1 within 15s")
		return nil
	}
	rebumped := waitAllRebumped()

	// 12. Fetch new thumb URL for each photo; assert 200.
	for _, it := range rebumped {
		if it.Type != "photo" {
			continue
		}
		url := base + "/api/v1/media/" + it.ID + "/thumb?size=grid&v=" + strconv.Itoa(it.ThumbVersion)
		resp, err := client.Get(url)
		r.NoError(err)
		status := resp.StatusCode
		_ = resp.Body.Close()
		r.Equalf(http.StatusOK, status, "url=%s", url)
	}

	// --- Albums round-trip ---------------------------------------------------
	// Create an album, add both photo rows, list albums and assert cover
	// is populated, then delete the album and list again.
	cBody, err := json.Marshal(map[string]string{"name": "E2E Trip"})
	r.NoError(err)
	cReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/api/v1/albums", bytes.NewReader(cBody))
	r.NoError(err)
	cReq.Header.Set("Content-Type", "application/json")
	cResp, err := client.Do(cReq)
	r.NoError(err)
	var created struct {
		ID string `json:"id"`
	}
	r.NoError(json.NewDecoder(cResp.Body).Decode(&created))
	r.NoError(cResp.Body.Close())
	r.Equal(http.StatusCreated, cResp.StatusCode)
	r.NotEmpty(created.ID)

	// photos was populated earlier when the two imported photo rows were
	// drained to thumb_status='ready'. Build the add-media request from it.
	photoIDs := make([]string, 0, len(photos))
	for _, p := range photos {
		photoIDs = append(photoIDs, p.ID)
	}
	addBody, err := json.Marshal(map[string]any{"media_ids": photoIDs})
	r.NoError(err)
	addReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/api/v1/albums/"+created.ID+"/media", bytes.NewReader(addBody))
	r.NoError(err)
	addReq.Header.Set("Content-Type", "application/json")
	addResp, err := client.Do(addReq)
	r.NoError(err)
	var addOut struct {
		Added          int `json:"added"`
		AlreadyPresent int `json:"already_present"`
	}
	r.NoError(json.NewDecoder(addResp.Body).Decode(&addOut))
	r.NoError(addResp.Body.Close())
	r.Equal(http.StatusOK, addResp.StatusCode)
	r.Equal(len(photoIDs), addOut.Added)

	// By the time this code runs, earlier sections of the test have already
	// waited for thumbs to reach 'ready' for both photos, so /api/v1/albums
	// should return a non-nil cover on the first call. Keep a short bounded
	// poll to tolerate one extra scheduling hop.
	var sawCover bool
	for range 20 {
		listReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
			base+"/api/v1/albums", nil)
		r.NoError(err)
		listResp, err := client.Do(listReq)
		r.NoError(err)
		var listOut struct {
			Items []struct {
				ID    string `json:"id"`
				Cover *struct {
					MediaID      string `json:"media_id"`
					ThumbVersion int    `json:"thumb_version"`
				} `json:"cover"`
			} `json:"items"`
		}
		r.NoError(json.NewDecoder(listResp.Body).Decode(&listOut))
		r.NoError(listResp.Body.Close())
		for _, it := range listOut.Items {
			if it.ID == created.ID && it.Cover != nil {
				sawCover = true
				break
			}
		}
		if sawCover {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.True(sawCover, "cover should appear after thumbs reach 'ready'")

	// Delete the album and confirm it leaves the list.
	delReq, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		base+"/api/v1/albums/"+created.ID, nil)
	r.NoError(err)
	delResp, err := client.Do(delReq)
	r.NoError(err)
	r.NoError(delResp.Body.Close())
	r.Equal(http.StatusNoContent, delResp.StatusCode)

	listReq2, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/api/v1/albums", nil)
	r.NoError(err)
	listResp2, err := client.Do(listReq2)
	r.NoError(err)
	var listOut2 struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	r.NoError(json.NewDecoder(listResp2.Body).Decode(&listOut2))
	r.NoError(listResp2.Body.Close())
	for _, it := range listOut2.Items {
		r.NotEqual(created.ID, it.ID, "album should be absent after delete")
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
