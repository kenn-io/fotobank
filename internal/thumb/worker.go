package thumb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"sync"
	"time"

	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/storage"
)

// defaultSweepInterval is how often Run triggers SweepLeases. Independent
// of PollInterval so a stuck decode can't delay sweep.
const defaultSweepInterval = time.Minute

// Config tunes the Worker's scheduling knobs. Zero values are treated as
// "use a conservative default" so callers can leave unused fields blank.
type Config struct {
	// WorkerConcurrency caps how many rows are decoded/encoded
	// concurrently inside drain. Each claim runs in its own goroutine
	// bounded by a semaphore. Defaults to 1.
	WorkerConcurrency int
	// PollInterval is how often runPoll triggers a drain cycle.
	// Defaults to 1s.
	PollInterval time.Duration
	// LeaseTimeout is how long a working row may remain un-finalized
	// before SweepLeases reclaims it. Per-item processing is bounded
	// to LeaseTimeout/2 so the worker surrenders gracefully before a
	// sweep bumps the version. Defaults to 5m.
	LeaseTimeout time.Duration
}

func (c Config) concurrency() int {
	if c.WorkerConcurrency <= 0 {
		return 1
	}
	return c.WorkerConcurrency
}

func (c Config) pollInterval() time.Duration {
	if c.PollInterval <= 0 {
		return time.Second
	}
	return c.PollInterval
}

func (c Config) leaseTimeout() time.Duration {
	if c.LeaseTimeout <= 0 {
		return 5 * time.Minute
	}
	return c.LeaseTimeout
}

// Worker drains the thumbnail Queue: it claims pending rows in batches,
// decodes + resizes + encodes each one, writes all sizes to the Store,
// and finalizes the row. A background sweep returns rows whose lease
// expired so a crashed or stuck worker never leaves rows wedged.
type Worker struct {
	q     *Queue
	store storage.Store
	cfg   Config

	// drainLock single-flights drain so overlapping poll ticks do not
	// stack up concurrent claims against SQLite's single-writer lock.
	drainLock sync.Mutex
}

// NewWorker constructs a Worker. Call Run to start processing; Run blocks
// until ctx is cancelled or a fatal sweep error is observed.
func NewWorker(q *Queue, store storage.Store, cfg Config) *Worker {
	return &Worker{q: q, store: store, cfg: cfg}
}

// Run drives the worker until ctx is cancelled. Spawns two goroutines:
// runPoll (drains on PollInterval) and runSweep (calls SweepLeases every
// defaultSweepInterval). Sweep runs independently so a stuck decode in
// drain cannot delay lease recovery. Returns nil on graceful shutdown.
func (w *Worker) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		w.runPoll(ctx)
	}()
	go func() {
		defer wg.Done()
		w.runSweep(ctx)
	}()
	wg.Wait()
	return nil
}

func (w *Worker) runPoll(ctx context.Context) {
	t := time.NewTicker(w.cfg.pollInterval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !w.drainLock.TryLock() {
				continue
			}
			w.drain(ctx)
			w.drainLock.Unlock()
		}
	}
}

func (w *Worker) runSweep(ctx context.Context) {
	t := time.NewTicker(defaultSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := w.q.SweepLeases(ctx, w.cfg.leaseTimeout()); err != nil {
				slog.Error("thumb: sweep leases", "err", err)
			}
		}
	}
}

// drain claims up to 2*WorkerConcurrency rows and processes them in
// parallel, bounded by a semaphore. Per-item contexts cap each
// processOne at LeaseTimeout/2 so a slow decoder releases its slot
// before the sweep would bump its version.
func (w *Worker) drain(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	conc := w.cfg.concurrency()
	claims, err := w.q.ClaimBatch(ctx, 2*conc)
	if err != nil {
		slog.Error("thumb: claim batch", "err", err)
		return
	}
	if len(claims) == 0 {
		return
	}
	itemTimeout := w.cfg.leaseTimeout() / 2
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for _, c := range claims {
		select {
		case <-ctx.Done():
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(c Claim) {
			defer wg.Done()
			defer func() { <-sem }()
			itemCtx, cancel := context.WithTimeout(ctx, itemTimeout)
			defer cancel()
			w.processOne(itemCtx, c)
		}(c)
	}
	wg.Wait()
}

// processOne handles a single claim end-to-end: choose a decode path,
// emit every Size into the Store at ThumbKey(id, version, size), and
// transition the row to its terminal status. Errors are categorised into
// MarkNoPreview (known un-decodable formats) vs MarkFailed (transient or
// unexpected); ErrClaimLost from Mark* is tolerated so a stale worker
// does not overwrite a fresher claim.
func (w *Worker) processOne(ctx context.Context, c Claim) {
	m := c.Media
	if skipFormat(m) {
		w.finalizeNoPreview(ctx, c)
		return
	}
	img, err := w.decodeSource(ctx, m)
	if err != nil {
		if errors.Is(err, ErrNoPreview) {
			w.finalizeNoPreview(ctx, c)
			return
		}
		w.finalizeFailed(ctx, c, err)
		return
	}
	if err := w.emitSizes(ctx, m, img); err != nil {
		w.finalizeFailed(ctx, c, err)
		return
	}
	if err := w.q.MarkReady(ctx, m.ID, m.ThumbVersion, c.ClaimedAt); err != nil {
		logClaimFinalize("mark ready", m.ID, err)
	}
}

func (w *Worker) finalizeNoPreview(ctx context.Context, c Claim) {
	err := w.q.MarkNoPreview(ctx, c.Media.ID, c.Media.ThumbVersion, c.ClaimedAt)
	if err != nil {
		logClaimFinalize("mark no_preview", c.Media.ID, err)
	}
}

func (w *Worker) finalizeFailed(ctx context.Context, c Claim, cause error) {
	slog.Error("thumb: process failed", "id", c.Media.ID, "err", cause)
	err := w.q.MarkFailed(ctx, c.Media.ID, c.Media.ThumbVersion, c.ClaimedAt, cause)
	if err != nil {
		logClaimFinalize("mark failed", c.Media.ID, err)
	}
}

// logClaimFinalize swallows ErrClaimLost (the claim was superseded by a
// sweep or Enqueue, nothing to do) and surfaces every other error.
func logClaimFinalize(op, id string, err error) {
	if errors.Is(err, ErrClaimLost) {
		return
	}
	slog.Error("thumb: "+op, "id", id, "err", err)
}

// decodeSource picks the correct decoder for m and returns the decoded
// image. RAW files are dispatched to ExtractPreview; JPEG/GIF go through
// Decode (which applies EXIF orientation).
func (w *Worker) decodeSource(ctx context.Context, m media.Media) (image.Image, error) {
	rc, err := w.store.ReadRange(ctx, m.Owner, m.Path, 0, -1)
	if err != nil {
		return nil, fmt.Errorf("read source: %w", err)
	}
	defer func() { _ = rc.Close() }()
	if isRAWMime(m.MimeType) {
		return ExtractPreview(rc)
	}
	return Decode(m.MimeType, rc)
}

// emitSizes encodes img to JPEG at quality 85 for every Size and writes
// each to its versioned key. An existing key (ErrPathOccupied) is
// tolerated: it means a prior attempt at this same version finished the
// write before crashing, and the bytes are valid.
func (w *Worker) emitSizes(ctx context.Context, m media.Media, img image.Image) error {
	for _, sz := range AllSizes() {
		var buf bytes.Buffer
		if err := EncodeJPEG(&buf, Resize(img, sz.MaxEdge()), 85); err != nil {
			return fmt.Errorf("encode %s: %w", sz, err)
		}
		key := ThumbKey(m.ID, m.ThumbVersion, sz)
		if _, err := w.store.Write(ctx, m.Owner, key, &buf); err != nil {
			if errors.Is(err, storage.ErrPathOccupied) {
				continue
			}
			return fmt.Errorf("write %s: %w", sz, err)
		}
	}
	return nil
}

// skipFormat reports whether the media type is one we decline to decode
// without reading the source (videos always; HEIC/HEIF pending CGO).
func skipFormat(m media.Media) bool {
	if m.Type == media.TypeVideo {
		return true
	}
	switch m.MimeType {
	case "image/heic", "image/heif":
		return true
	}
	return false
}

// isRAWMime reports whether mime identifies a camera RAW format whose
// display preview must be extracted from an embedded JPEG rather than
// decoded directly.
func isRAWMime(mime string) bool {
	switch mime {
	case "image/x-sony-arw",
		"image/x-fuji-raf",
		"image/x-adobe-dng",
		"image/x-canon-cr2":
		return true
	}
	return false
}
