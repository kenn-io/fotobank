package thumb_test

import (
	"bytes"
	"context"
	"database/sql"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/contentresolver"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/obs"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/storage"
	"go.kenn.io/fotobank/internal/testutil"
	"go.kenn.io/fotobank/internal/testutil/assetfixture"
	"go.kenn.io/fotobank/internal/thumb"
)

// workerFixture wires a real SQLite DB, a NAS-backed Store, a Queue, and
// an owner so tests can seed rows + bytes and observe worker transitions.
type workerFixture struct {
	rw      *sql.DB
	repo    *media.Repo
	queue   *thumb.Queue
	store   storage.Store
	content *content.Adapter
	resolve *contentresolver.Resolver
	owner   owners.Principal
}

func newWorkerFixture(t *testing.T) workerFixture {
	t.Helper()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	q := thumb.NewQueue(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC(),
	)
	require.NoError(t, err)
	store := storage.NewNASOnly(t.TempDir(), map[owners.Principal]string{p: "550e8400-e29b-41d4-a716-446655440000"})
	contentStore, err := content.Open(context.Background(), content.Config{Root: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, contentStore.Close()) })
	return workerFixture{
		rw: d.WriteDB(), repo: repo, queue: q, store: store, content: contentStore,
		resolve: contentresolver.New(repo, contentStore), owner: p,
	}
}

// seedPhotoRow inserts a pending photo row and writes real JPEG bytes to
// the store at the row's path. Returns the row ID.
func seedPhotoRow(t *testing.T, fx workerFixture, path string) string {
	t.Helper()
	bs, err := os.ReadFile(filepath.Join("..", "..", "testdata", "exif", "photo-with-timestamp.jpg"))
	require.NoError(t, err)
	id := uuid.NewString()
	bs = append(bs, id...)
	m := media.Media{
		ID:                 id,
		Owner:              fx.owner,
		Type:               media.TypePhoto,
		MimeType:           "image/jpeg",
		DocbankVirtualPath: path,
		OriginalFilename:   "x.jpg",
		ImportedAt:         time.Now().UTC().Truncate(time.Second),
		Size:               int64(len(bs)),
		SHA256:             uuid.NewString(),
		ThumbStatus:        "pending",
	}
	assetfixture.InsertContent(t, fx.repo, fx.content, bs, m)
	return id
}

// seedVideoRow inserts a pending video row without writing bytes — the
// worker must skip the storage read entirely for non-decodable types.
func seedVideoRow(t *testing.T, fx workerFixture) string {
	t.Helper()
	id := uuid.NewString()
	m := media.Media{
		ID:                 id,
		Owner:              fx.owner,
		Type:               media.TypeVideo,
		MimeType:           "video/mp4",
		DocbankVirtualPath: "2024/v-" + id + ".mp4",
		OriginalFilename:   "v.mp4",
		ImportedAt:         time.Now().UTC().Truncate(time.Second),
		Size:               0,
		SHA256:             uuid.NewString(),
		ThumbStatus:        "pending",
	}
	assetfixture.InsertContent(t, fx.repo, fx.content, nil, m)
	return id
}

// seedPNGPhotoRow inserts a pending photo row with mime image/png and
// writes a small synthesized PNG to the store at the row's path.
// Ingest classifies .png as TypePhoto (see discover.go), so the worker
// must decode image/png rows to ready — not MarkFailed.
func seedPNGPhotoRow(t *testing.T, fx workerFixture, path string) string {
	t.Helper()
	src := image.NewRGBA(image.Rect(0, 0, 32, 32))
	src.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, src))
	id := uuid.NewString()
	m := media.Media{
		ID:                 id,
		Owner:              fx.owner,
		Type:               media.TypePhoto,
		MimeType:           "image/png",
		DocbankVirtualPath: path,
		OriginalFilename:   "x.png",
		ImportedAt:         time.Now().UTC().Truncate(time.Second),
		Size:               int64(buf.Len()),
		SHA256:             uuid.NewString(),
		ThumbStatus:        "pending",
	}
	assetfixture.InsertContent(t, fx.repo, fx.content, buf.Bytes(), m)
	return id
}

func readThumbStatusFor(t *testing.T, rw *sql.DB, id string) string {
	t.Helper()
	var status string
	err := rw.QueryRowContext(context.Background(),
		`SELECT thumb_status FROM assets WHERE id = ?`, id).Scan(&status)
	require.NoError(t, err)
	return status
}

func readThumbVersionFor(t *testing.T, rw *sql.DB, id string) int {
	t.Helper()
	var v int
	err := rw.QueryRowContext(context.Background(),
		`SELECT thumb_version FROM assets WHERE id = ?`, id).Scan(&v)
	require.NoError(t, err)
	return v
}

// waitForStatus polls the DB for up to 5s for row id to reach want.
func waitForStatus(t *testing.T, rw *sql.DB, id, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if readThumbStatusFor(t, rw, id) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.Equalf(t, want, readThumbStatusFor(t, rw, id),
		"row %s never reached status %q", id, want)
}

func TestWorkerDrainsPendingRowToReady(t *testing.T) {
	r := require.New(t)
	fx := newWorkerFixture(t)
	id := seedPhotoRow(t, fx, "2024/a-"+uuid.NewString()+".jpg")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := thumb.NewWorker(fx.queue, fx.store, thumb.Config{Content: fx.resolve,
		WorkerConcurrency: 2,
		PollInterval:      20 * time.Millisecond,
		LeaseTimeout:      5 * time.Minute,
	})
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	waitForStatus(t, fx.rw, id, "ready")

	// Read back the grid thumbnail — must be non-empty JPEG bytes.
	version := readThumbVersionFor(t, fx.rw, id)
	key := thumb.ThumbKey(id, version, thumb.SizeGrid)
	rc, err := fx.store.ReadRange(context.Background(), fx.owner, key, 0, -1)
	r.NoError(err)
	defer func() { _ = rc.Close() }()
	bs, err := io.ReadAll(rc)
	r.NoError(err)
	r.Greater(len(bs), 100, "grid thumb empty")

	cancel()
	<-done
}

func TestWorkerSkipsVideoAsNoPreview(t *testing.T) {
	fx := newWorkerFixture(t)
	id := seedVideoRow(t, fx)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := thumb.NewWorker(fx.queue, fx.store, thumb.Config{Content: fx.resolve,
		WorkerConcurrency: 1,
		PollInterval:      20 * time.Millisecond,
		LeaseTimeout:      5 * time.Minute,
	})
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	waitForStatus(t, fx.rw, id, "no_preview")

	cancel()
	<-done
}

// TestWorkerDrainsPNGPhotoToReady is the regression guard for the
// codex finding on b2f9463: ingest accepts .png as TypePhoto +
// image/png, so the worker must decode PNG rather than marking it
// failed with "unsupported mime". Reaching "ready" proves the PNG
// path ran the full decode → resize → encode pipeline.
func TestWorkerDrainsPNGPhotoToReady(t *testing.T) {
	r := require.New(t)
	fx := newWorkerFixture(t)
	id := seedPNGPhotoRow(t, fx, "2024/p-"+uuid.NewString()+".png")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := thumb.NewWorker(fx.queue, fx.store, thumb.Config{Content: fx.resolve,
		WorkerConcurrency: 1,
		PollInterval:      20 * time.Millisecond,
		LeaseTimeout:      5 * time.Minute,
	})
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	waitForStatus(t, fx.rw, id, "ready")

	version := readThumbVersionFor(t, fx.rw, id)
	key := thumb.ThumbKey(id, version, thumb.SizeGrid)
	rc, err := fx.store.ReadRange(context.Background(), fx.owner, key, 0, -1)
	r.NoError(err)
	defer func() { _ = rc.Close() }()
	bs, err := io.ReadAll(rc)
	r.NoError(err)
	r.Greater(len(bs), 100, "grid thumb empty for PNG source")

	cancel()
	<-done
}

// TestWorkerStaleWriteDoesNotCorruptReclaim verifies that after a sweep bumps
// thumb_version, the worker's next claim
// must write to the v+1 key, NOT retry the v0 key (which would hit
// storage.ErrPathOccupied because the prior attempt's bytes linger).
func TestWorkerStaleWriteDoesNotCorruptReclaim(t *testing.T) {
	r := require.New(t)
	fx := newWorkerFixture(t)
	id := seedPhotoRow(t, fx, "2024/a-"+uuid.NewString()+".jpg")

	// Simulate a prior worker that wrote v0/grid.jpg bytes before its
	// lease was swept. Those bytes must still exist after the sweep, so
	// a naive retry at v0 would collide.
	var buf bytes.Buffer
	stale := image.NewRGBA(image.Rect(0, 0, 16, 16))
	r.NoError(thumb.EncodeJPEG(&buf, thumb.Resize(stale, 256), 85))
	_, err := fx.store.Write(
		context.Background(), fx.owner,
		thumb.ThumbKey(id, 0, thumb.SizeGrid), &buf,
	)
	r.NoError(err)

	// Manually claim and sweep to bump the row to v1 pending.
	claims, err := fx.queue.ClaimBatch(context.Background(), 1)
	r.NoError(err)
	r.Len(claims, 1)
	n, err := fx.queue.SweepLeases(context.Background(), 0)
	r.NoError(err)
	r.Equal(1, n)
	r.Equal(1, readThumbVersionFor(t, fx.rw, id), "sweep must have bumped version")

	// Run the worker — it should claim at v1 and write v1 keys without
	// tripping over the pre-existing v0 bytes.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := thumb.NewWorker(fx.queue, fx.store, thumb.Config{Content: fx.resolve,
		WorkerConcurrency: 1,
		PollInterval:      20 * time.Millisecond,
		LeaseTimeout:      5 * time.Minute,
	})
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	waitForStatus(t, fx.rw, id, "ready")
	r.Equal(1, readThumbVersionFor(t, fx.rw, id))

	// v1 grid key must be present.
	rc, err := fx.store.ReadRange(
		context.Background(), fx.owner,
		thumb.ThumbKey(id, 1, thumb.SizeGrid), 0, -1,
	)
	r.NoError(err)
	defer func() { _ = rc.Close() }()
	bs, err := io.ReadAll(rc)
	r.NoError(err)
	r.Greater(len(bs), 100)

	cancel()
	<-done
}

// TestWorkerRunReturnsOnlyAfterDrainGoroutinesExit is the regression
// guard for the goroutine-leak on shutdown. Before the fix, drain's
// early return on ctx.Done() did not wait for already-launched
// processOne goroutines, so they would outlive Run and hold references
// to the SQL DB and Storage backend past teardown.
//
// The test seeds many pending rows so drain is back-pressured on the
// semaphore, cancels ctx mid-drain, waits for Run to return, then:
//  1. Asserts Run surfaces context.Canceled (not nil).
//  2. Verifies the DB handle is still usable by reading every seeded
//     row — if an inflight goroutine had corrupted the handle, GetByID
//     would error.
//  3. Checks goroutine count has returned to baseline within a small
//     tolerance — a missed wg.Wait would leave live processOne
//     goroutines above baseline.
func TestWorkerRunReturnsOnlyAfterDrainGoroutinesExit(t *testing.T) {
	r := require.New(t)
	fx := newWorkerFixture(t)

	const nRows = 20
	ids := make([]string, 0, nRows)
	for range nRows {
		ids = append(ids, seedPhotoRow(t, fx, "2024/s-"+uuid.NewString()+".jpg"))
	}

	baseline := runtime.NumGoroutine()

	// Concurrency=2 with a 20-row batch guarantees the drain loop
	// parks on the semaphore before the whole batch is launched,
	// giving cancel() a chance to trigger the early-return path.
	ctx, cancel := context.WithCancel(context.Background())
	w := thumb.NewWorker(fx.queue, fx.store, thumb.Config{Content: fx.resolve,
		WorkerConcurrency: 2,
		PollInterval:      10 * time.Millisecond,
		LeaseTimeout:      5 * time.Minute,
	})
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	// Let the poll tick fire at least once so drain is mid-flight.
	time.Sleep(30 * time.Millisecond)
	cancel()

	// Run must return within a reasonable window.
	select {
	case err := <-done:
		r.ErrorIs(err, context.Canceled,
			"Run must surface context.Canceled")
	case <-time.After(5 * time.Second):
		r.FailNow("Run did not return within 5s after ctx cancel")
	}

	// Prove the DB handle is alive: every seeded row must still be
	// readable. If an inflight goroutine had closed or corrupted the
	// handle, GetByID would error here.
	for _, id := range ids {
		_, err := fx.repo.GetByID(context.Background(), id)
		r.NoError(err, "repo.GetByID(%s) after Run returned", id)
	}

	// Goroutine count should be back to baseline. Allow a small
	// tolerance for transient runtime goroutines (finalizers, GC
	// assist, etc.) that the scheduler may not have reaped yet.
	// A missed wg.Wait would leave up to WorkerConcurrency=2
	// processOne goroutines alive here.
	const tolerance = 3
	after := runtime.NumGoroutine()
	r.LessOrEqualf(after, baseline+tolerance,
		"goroutine leak: baseline=%d after=%d (tolerance=%d)",
		baseline, after, tolerance)
}

func TestWorkerEmitsResultMetricsAndLeaseSweep(t *testing.T) {
	r := require.New(t)
	fx := newWorkerFixture(t)

	photoID := seedPhotoRow(t, fx, "2024/a-"+uuid.NewString()+".jpg")
	videoID := seedVideoRow(t, fx)

	// Insert a third row pre-claimed with a stale lease so SweepLeases
	// has a row to bump back to pending and increment the swept counter.
	staleID := uuid.NewString()
	staleM := media.Media{
		ID: staleID, Owner: fx.owner, Type: media.TypePhoto,
		MimeType: "image/jpeg", DocbankVirtualPath: "2024/stale-" + staleID + ".jpg",
		OriginalFilename: "stale.jpg",
		ImportedAt:       time.Now().UTC().Truncate(time.Second),
		Size:             1, SHA256: uuid.NewString(),
		ThumbStatus: "working",
	}
	assetfixture.InsertContent(t, fx.repo, fx.content, []byte("x"), staleM)
	_, err := fx.rw.ExecContext(context.Background(),
		`UPDATE assets SET thumb_claimed_at = ? WHERE id = ?`,
		time.Now().Add(-time.Hour).UTC(), staleID)
	r.NoError(err)

	m := obs.NewTestMetrics()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := thumb.NewWorker(fx.queue, fx.store, thumb.Config{Content: fx.resolve,
		WorkerConcurrency: 1,
		PollInterval:      20 * time.Millisecond,
		LeaseTimeout:      time.Minute,
		SweepInterval:     50 * time.Millisecond,
		Metrics:           m,
	})
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	waitForStatus(t, fx.rw, photoID, "ready")
	waitForStatus(t, fx.rw, videoID, "no_preview")

	require.Eventually(t, func() bool {
		return m.ThumbLeasesSwept().Get() >= 1
	}, 3*time.Second, 20*time.Millisecond,
		"stale-lease sweep counter must increment within the worker's tick")

	cancel()
	<-done

	r.GreaterOrEqual(m.ThumbJobs("ok").Get(), uint64(1))
	r.GreaterOrEqual(m.ThumbJobs("no_preview").Get(), uint64(1))
	r.GreaterOrEqual(m.ThumbLeasesSwept().Get(), uint64(1))
}

// TestWorkerEmitsAllSizesPerClaim is the contract guard for the F2.0
// vocabulary change: every Size returned by AllSizes() must land
// non-empty bytes at its versioned key after a single ready row, so
// the lightbox can rely on `?size=preview` and `?size=large` being
// present without falling back to the original bytes.
func TestWorkerEmitsAllSizesPerClaim(t *testing.T) {
	r := require.New(t)
	fx := newWorkerFixture(t)
	id := seedPhotoRow(t, fx, "2024/a-"+uuid.NewString()+".jpg")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := thumb.NewWorker(fx.queue, fx.store, thumb.Config{Content: fx.resolve,
		WorkerConcurrency: 2,
		PollInterval:      20 * time.Millisecond,
		LeaseTimeout:      5 * time.Minute,
	})
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	waitForStatus(t, fx.rw, id, "ready")

	version := readThumbVersionFor(t, fx.rw, id)
	for _, sz := range thumb.AllSizes() {
		key := thumb.ThumbKey(id, version, sz)
		rc, err := fx.store.ReadRange(context.Background(), fx.owner, key, 0, -1)
		r.NoErrorf(err, "open %s", sz)
		bs, err := io.ReadAll(rc)
		r.NoErrorf(err, "read %s", sz)
		r.NoError(rc.Close())
		// 512 bytes comfortably exceeds a baseline JPEG header (SOI +
		// APP0 + DQT + DHT + SOF + SOS ≈ 600 bytes including the
		// minimum entropy-coded data) but stays loose enough that the
		// 2x2 test fixture produces well over the floor at every size.
		// Looser thresholds (e.g. >100) would not catch a truncated or
		// wrong-codec write.
		r.Greaterf(len(bs), 512, "size %s emitted suspiciously small bytes (%d)", sz, len(bs))
		// jpeg.Decode (not image.Decode) is the load-bearing check:
		// the test file imports image/png, so image.Decode would
		// happily accept a PNG written under a .jpg key — defeating
		// the "wrong codec written" guard. jpeg.Decode rejects any
		// non-JPEG payload.
		_, err = jpeg.Decode(bytes.NewReader(bs))
		r.NoErrorf(err, "size %s did not decode as JPEG", sz)
	}

	cancel()
	<-done
}

func TestThumbWorkerLogsCarryComponent(t *testing.T) {
	r := require.New(t)
	fx := newWorkerFixture(t)
	id := seedPhotoRow(t, fx, "2024/c-"+uuid.NewString()+".jpg")

	// bytes.Buffer is safe here ONLY because the buffer is read AFTER
	// <-done joins the worker goroutine. If you adapt this template to
	// poll logBuf.String() inside require.Eventually, switch to a
	// mutex-wrapped buffer (see backup/worker_test.go syncBuf) to
	// avoid a -race failure.
	var logBuf bytes.Buffer
	base := slog.New(slog.NewJSONHandler(&logBuf, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := thumb.NewWorker(fx.queue, fx.store, thumb.Config{Content: fx.resolve,
		WorkerConcurrency: 1,
		PollInterval:      20 * time.Millisecond,
		LeaseTimeout:      time.Minute,
		Logger:            base.With("component", "thumb"),
		Metrics:           obs.NewTestMetrics(),
	})
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	waitForStatus(t, fx.rw, id, "ready")
	cancel()
	<-done
	// Per-line scan: every emitted line must carry component=thumb.
	for line := range strings.SplitSeq(strings.TrimSpace(logBuf.String()), "\n") {
		r.Contains(line, `"component":"thumb"`,
			"every thumb-worker log line must carry component=thumb")
	}
}
