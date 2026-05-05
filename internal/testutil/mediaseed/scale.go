package mediaseed

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/owners"
)

// baseTime is the fixed timestamp anchor for every time-valued column
// the seed writes (owner.created_at, media.imported_at, ai_results.
// generated_at, optional hidden_at). Pinning a constant keeps the
// determinism contract honest: two seeds with the same opts produce
// byte-identical DBs even when the wall clock has moved between
// runs. The chosen instant is arbitrary; only its constancy matters.
var baseTime = time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

// rowStride is the per-row decrement applied to imported_at,
// hidden_at, and ai_results.generated_at — every seeded row gets a
// timestamp `rowStride` older than its predecessor. 30 minutes spreads
// 100k rows over ~5.7 years (~70 months), which (a) is plausible for a
// long-lived photo library and (b) gives the /library scale spec
// enough month-chunks to actually exercise VirtualGrid's chunk-level
// windowing IO. A 1-minute stride packed 100k rows into ~58 days
// (2-3 calendar months), creating chunks tall enough to never exit
// the windowing rootMargin buffer.
const rowStride = 30 * time.Minute

// Hot-zone bounding box for the GPS bias — a 2°×2° square centered on
// the SF Bay Area (37.5°N, -122.0°W). HotZoneFraction of geotagged
// rows fall inside; the rest spread uniformly. The /map scale spec
// (PS-4) opens at this center to exercise the cluster-split boundary.
// Plausible for a real library: a US photographer's home city
// dominates the marker distribution.
const (
	hotZoneLatMin = 36.5
	hotZoneLatMax = 38.5
	hotZoneLonMin = -123.0
	hotZoneLonMax = -121.0
)

// SeedScaleLibrary inserts opts.Total media rows for owner under the
// supplied skewed distributions and writes them in a single
// transaction. Returns the slice of inserted IDs in insertion order
// so benchmarks can sample without re-reading the DB.
//
// Why not Repo.Insert: at 100k rows in WAL mode each Repo.Insert
// autocommits and fsyncs once per row — the seed alone would dominate
// any -benchtime budget. The raw INSERT below mirrors mediaInsert in
// internal/media/repo.go column-for-column; if the schema gains a
// column, both this function and repo.go must update together. The
// schema is single-migration pre-alpha, so the duplication cost is
// low and the transaction wrapping is the whole point.
//
// Distribution shape (defaults via DefaultScaleOpts):
//   - cameras  : Zipfian(s=1.16) over NumCameras distinct (make,model)
//     pairs — a few cameras dominate the library.
//   - lenses   : Zipfian(s=1.16) over NumLenses distinct lens names.
//   - tags     : ~50% of rows get one ai_results(task=tag, status=active)
//     row with 1-3 media_tags children drawn Zipfian over
//     NumTags keys. Mirrors a real library where popular
//     concepts ("portrait") tag many photos and obscure
//     ones tag few.
//   - GPS      : GPSFraction of rows have non-null lat/lon (uniform
//     random globally — not realistic but exercises the
//     same index paths).
//   - hidden   : HiddenFraction of rows have non-null hidden_at.
//
// Determinism: opts.Seed seeds the RNG and the entire byte-level
// content of every inserted row — IDs, timestamps, ai_results UUIDs,
// and tag fan-out — is derived from the seed plus the row index.
// Two SeedScaleLibrary calls with identical opts (and the same
// principal) produce byte-identical DBs. The fixed-base timestamp
// (baseTime below) seals the determinism: no time.Now() leaks in.
//
// Repeat seeding: SeedScaleLibrary uses fixed `scale-NNNNNNN` IDs so
// a second call against the same DB will collide on media.id. The
// `INSERT OR IGNORE` on owners covers the FK parent, but the media
// inserts themselves are not idempotent. Callers wanting to seed
// multiple owners should use distinct principals AND avoid re-using
// the same DB without a fresh schema, or extend ScaleOpts with an
// ID prefix if the use case actually requires stacked fixtures.
func SeedScaleLibrary(tb testing.TB, rw *sql.DB, p owners.Principal, opts ScaleOpts) []string {
	tb.Helper()
	ids, err := SeedScaleLibraryToDB(rw, p, opts)
	require.NoError(tb, err, "seed scale library")
	return ids
}

// SeedScaleLibraryToDB is the error-returning variant of
// SeedScaleLibrary, suitable for non-test callers (e.g. cmd/e2e-server's
// scale-mode boot path). The contract — distribution shape, byte-level
// determinism, multi-call collision behavior — is identical.
func SeedScaleLibraryToDB(rw *sql.DB, p owners.Principal, opts ScaleOpts) ([]string, error) {
	opts = opts.withDefaults()

	rng := rand.New(rand.NewSource(opts.Seed))
	cameras := generateCameras(opts.NumCameras)
	lenses := generateLenses(opts.NumLenses)
	tagKeys := generateTagKeys(opts.NumTags)

	// Zipf-driven indices. NewZipf requires imax >= 1; clamp the
	// pool sizes (we still emit the requested cardinality, but the
	// generator caps to len-1).
	cameraPick := rand.NewZipf(rng, 1.16, 1, uint64(len(cameras)-1))
	lensPick := rand.NewZipf(rng, 1.16, 1, uint64(len(lenses)-1))
	tagPick := rand.NewZipf(rng, 1.16, 1, uint64(len(tagKeys)-1))

	ctx := context.Background()
	tx, err := rw.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("seed: begin tx: %w", err)
	}
	// rollback on error path; explicit Commit below disarms it on success
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Owner row first — media has FK to owners(hub, user_id). INSERT
	// OR IGNORE so callers can call SeedScaleLibrary multiple times
	// against the same DB (e.g. re-seed for a different bench) or
	// stack a small fixture on top of an already-seeded owner.
	// owners.created_at uses the fixed baseTime so the determinism
	// contract holds across runs.
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO owners(hub, user_id, storage_key, created_at)
		 VALUES (?, ?, ?, ?)`,
		p.Hub, p.UserID, "scale-storage", baseTime,
	); err != nil {
		return nil, fmt.Errorf("seed: insert owner: %w", err)
	}

	mediaStmt, err := tx.PrepareContext(ctx, mediaInsertSQL)
	if err != nil {
		return nil, fmt.Errorf("seed: prepare media: %w", err)
	}
	defer mediaStmt.Close()

	resultsStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO ai_results
			(id, media_id, task, model_id, prompt_version, prompt_hash,
			 input_profile, status, generated_at)
		VALUES (?, ?, 'tag', ?, ?, ?, ?, 'active', ?)`)
	if err != nil {
		return nil, fmt.Errorf("seed: prepare ai_results: %w", err)
	}
	defer resultsStmt.Close()

	tagStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO media_tags (result_id, tag_key, tag_label, rank)
		VALUES (?, ?, ?, ?)`)
	if err != nil {
		return nil, fmt.Errorf("seed: prepare media_tags: %w", err)
	}
	defer tagStmt.Close()

	base := baseTime
	ids := make([]string, opts.Total)

	for i := 0; i < opts.Total; i++ {
		id := fmt.Sprintf("scale-%07d", i)
		ids[i] = id

		c := cameras[cameraPick.Uint64()]
		l := lenses[lensPick.Uint64()]

		var lat, lon any // any so nullable values flow as NULL
		if rng.Float64() < opts.GPSFraction {
			// HotZoneFraction of geotagged rows fall inside a 2°×2°
			// box centered on the SF Bay Area; the rest spread
			// uniformly across the globe. Without the bias, 30k rows
			// over 180°×360° give ~0.5 markers per 1° square — too
			// sparse for the /map scale spec to exercise the
			// cluster-split boundary. The bias creates a dense region
			// (e.g. ~12k markers in 2°×2° at GPSFraction=0.30,
			// HotZoneFraction=0.40, total=100k) so zoom-in stress is
			// measurable.
			if rng.Float64() < opts.HotZoneFraction {
				lat = hotZoneLatMin + rng.Float64()*(hotZoneLatMax-hotZoneLatMin)
				lon = hotZoneLonMin + rng.Float64()*(hotZoneLonMax-hotZoneLonMin)
			} else {
				lat = -90 + rng.Float64()*180
				lon = -180 + rng.Float64()*360
			}
		}

		var hiddenAt any
		if rng.Float64() < opts.HiddenFraction {
			hiddenAt = base.Add(-time.Duration(i) * rowStride)
		}

		if _, err := mediaStmt.ExecContext(ctx,
			id, p.Hub, p.UserID, "photo", "image/jpeg",
			"scale/"+id+".jpg", id+".jpg",
			base.Add(-time.Duration(i)*rowStride), nil, // imported_at, timestamp
			int64(1000), "cs-"+id,
			c.Make, c.Model, l.Model, nil, nil, nil, nil, nil, nil, // EXIF detail nulled
			nil,                // duration_ms
			lat, lon, nil, nil, // gps_at, location_label nulled
			"ready", 0, nil, // thumb_status, thumb_version, thumb_updated_at
			"scale/"+id+".jpg", nil, // import_source_path, paired_with_id
			hiddenAt,
		); err != nil {
			return nil, fmt.Errorf("seed: insert media row %d: %w", i, err)
		}

		// Tag fan-out: ~50% of rows get tags, those that do get 1-3.
		// Skip the rest so the population isn't 100% tagged (real
		// libraries have un-AI'd rows).
		if rng.Float64() >= 0.50 {
			continue
		}
		// Deterministic result ID from the row index. uuid.NewString
		// would be nondeterministic and break the byte-level
		// determinism contract documented at the top of this function.
		resultID := fmt.Sprintf("scale-result-%07d", i)
		if _, err := resultsStmt.ExecContext(ctx,
			resultID, id,
			"scale-model", "tag-v1", "scale-hash", "scale-profile",
			base.Add(-time.Duration(i)*rowStride),
		); err != nil {
			return nil, fmt.Errorf("seed: insert ai_results row %d: %w", i, err)
		}
		nTags := 1 + rng.Intn(3)
		seen := make(map[string]struct{}, nTags)
		for j := range nTags {
			tagKey := tagKeys[tagPick.Uint64()]
			if _, dup := seen[tagKey]; dup {
				continue // PK is (result_id, tag_key) — skip duplicates rather than retry
			}
			seen[tagKey] = struct{}{}
			if _, err := tagStmt.ExecContext(ctx, resultID, tagKey, tagKey, j+1); err != nil {
				return nil, fmt.Errorf("seed: insert media_tags row %d/%d: %w", i, j, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("seed: commit: %w", err)
	}
	committed = true
	return ids, nil
}

// ScaleOpts controls SeedScaleLibrary's distribution shape.
type ScaleOpts struct {
	Total           int     // total media rows
	NumCameras      int     // distinct (make,model) pairs
	NumLenses       int     // distinct lens model names
	NumTags         int     // distinct tag keys
	GPSFraction     float64 // fraction with non-null lat/lon, 0..1
	HotZoneFraction float64 // of geotagged rows, fraction inside the SF Bay Area hot zone, 0..1
	HiddenFraction  float64 // fraction with non-null hidden_at, 0..1
	Seed            int64   // RNG seed; same opts → same DB content
}

// DefaultScaleOpts returns balanced defaults for a library of total
// rows: ~50 cameras / 150 lenses / 500 tags, 30% GPS, 40% of geotagged
// rows in the hot zone, 5% hidden, deterministic seed=42. Callers can
// override individual fields.
func DefaultScaleOpts(total int) ScaleOpts {
	return ScaleOpts{
		Total:           total,
		NumCameras:      50,
		NumLenses:       150,
		NumTags:         500,
		GPSFraction:     0.30,
		HotZoneFraction: 0.40,
		HiddenFraction:  0.05,
		Seed:            42,
	}
}

func (o ScaleOpts) withDefaults() ScaleOpts {
	if o.Total <= 0 {
		o.Total = 1
	}
	if o.NumCameras < 2 {
		o.NumCameras = 2 // Zipf imax must be >= 1
	}
	if o.NumLenses < 2 {
		o.NumLenses = 2
	}
	if o.NumTags < 2 {
		o.NumTags = 2
	}
	if o.GPSFraction < 0 {
		o.GPSFraction = 0
	}
	if o.HotZoneFraction < 0 {
		o.HotZoneFraction = 0
	} else if o.HotZoneFraction > 1 {
		o.HotZoneFraction = 1
	}
	if o.HiddenFraction < 0 {
		o.HiddenFraction = 0
	}
	if o.Seed == 0 {
		o.Seed = 42
	}
	return o
}

// mediaInsertSQL mirrors mediaInsert in internal/media/repo.go. The
// duplication is intentional: keeping the seed independent of the
// repo lets the seed run inside a single transaction (Repo.Insert is
// not tx-aware) without forcing a public InsertTx surface.
const mediaInsertSQL = `INSERT INTO media (
	id, owner_hub, owner_user_id, media_type, mime_type, path, original_filename,
	imported_at, timestamp, size, checksum,
	make, model, lens_model, focal_length, shutter, width, height, iso, aperture,
	duration_ms,
	latitude, longitude, gps_at, location_label,
	thumb_status, thumb_version, thumb_updated_at,
	import_source_path, paired_with_id,
	hidden_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

type cameraSeed struct {
	Make, Model string
}

type lensSeed struct {
	Model string
}

func generateCameras(n int) []cameraSeed {
	// Use a fixed roster of plausible (make, model) prefixes and pad
	// with a numeric suffix for the long tail. The first 5 entries
	// cover the most common real-world bodies so the head of the
	// Zipf distribution maps onto recognizable names; the rest are
	// synthetic.
	makes := []string{"Sony", "Canon", "Nikon", "Fujifilm", "Leica", "Panasonic", "Olympus", "Pentax"}
	out := make([]cameraSeed, n)
	for i := range n {
		out[i] = cameraSeed{
			Make:  makes[i%len(makes)],
			Model: fmt.Sprintf("scale-cam-%03d", i),
		}
	}
	return out
}

func generateLenses(n int) []lensSeed {
	out := make([]lensSeed, n)
	for i := range n {
		out[i] = lensSeed{Model: fmt.Sprintf("scale-lens-%03d", i)}
	}
	return out
}

func generateTagKeys(n int) []string {
	out := make([]string, n)
	for i := range n {
		out[i] = fmt.Sprintf("scale-tag-%04d", i)
	}
	return out
}

// FTSOpts controls SeedFTSCorpus's caption fan-out and word picks.
type FTSOpts struct {
	// CaptionFraction is the fraction of media rows that get a caption
	// ai_results row + media_captions row (0..1). Real libraries have
	// only a slice of media captioned; the bench only needs enough
	// caption rows for BM25 to have a non-trivial result set against
	// "sunset"-like queries. 30% is a reasonable mid-point.
	CaptionFraction float64
	// Seed seeds the caption-word RNG. Different from
	// ScaleOpts.Seed by default so caption picks are independent of
	// tag picks; same Seed → byte-identical caption text.
	Seed int64
}

// DefaultFTSOpts returns 30% caption coverage, seed=43.
func DefaultFTSOpts() FTSOpts {
	return FTSOpts{CaptionFraction: 0.30, Seed: 43}
}

func (o FTSOpts) withDefaults() FTSOpts {
	if o.CaptionFraction < 0 {
		o.CaptionFraction = 0
	}
	if o.CaptionFraction > 1 {
		o.CaptionFraction = 1
	}
	if o.Seed == 0 {
		o.Seed = 43
	}
	return o
}

// captionWords is the deterministic vocabulary SeedFTSCorpus draws on.
// Picked uniformly per caption-word slot. "sunset" is intentionally at
// index 0 (the canonical bench query); the rest are common
// photography-adjacent words that produce a varied corpus without
// any one word dominating.
//
// Each caption is six words pulled with replacement from this 30-word
// pool. P("sunset" appears) = 1 - (29/30)^6 ≈ 0.18, so with
// CaptionFraction=0.30 ~5% of all rows will surface for a "sunset"
// BM25 query — enough to fill multiple result pages but well short of
// the trivial "matches everything" shape that would hide the cost
// difference between BM25Only and the filter-only fast path.
var captionWords = []string{
	"sunset", "mountain", "portrait", "beach", "river", "forest",
	"city", "street", "sky", "cloud", "rain", "snow",
	"dog", "cat", "child", "family", "friend", "wedding",
	"morning", "evening", "winter", "spring", "summer", "autumn",
	"flower", "tree", "rock", "lake", "bridge", "road",
}

const captionWordsPerRow = 6

// SeedFTSCorpus extends a previously-seeded scale library (created
// via SeedScaleLibrary) with caption rows and bulk-populates media_fts
// from the joined corpus columns. Designed for benchmarks that
// exercise the BM25 path: SeedScaleLibrary alone leaves media_fts
// empty (no caption seeding, no FTS refresh), so a hybrid bench would
// otherwise measure an empty corpus.
//
// Why a separate helper: callers that don't need FTS5 (the existing
// repo / facets / embedding benches) shouldn't pay caption + FTS seed
// cost. Splitting the helper also keeps SeedScaleLibrary's
// determinism contract from coupling to FTS-internal vocabulary
// choices.
//
// The bulk INSERT projection mirrors the per-row index.RefreshMediaFTS
// call the importer/reconcile/AI-promotion pipeline drives in
// production, modulo the GROUP_CONCAT-vs-rank-ordered loop the
// production path uses. The tokenizer (porter unicode61) sees the
// same text either way, so BM25 numbers transfer.
//
// Determinism: caption word picks come from a seed-driven RNG; same
// FTSOpts → byte-identical caption text → byte-identical media_fts
// rows. The owner argument scopes the bulk INSERT so a multi-owner DB
// only refreshes the seeded slice.
func SeedFTSCorpus(tb testing.TB, rw *sql.DB, p owners.Principal, opts FTSOpts) {
	tb.Helper()
	opts = opts.withDefaults()

	ctx := context.Background()
	tx, err := rw.BeginTx(ctx, nil)
	require.NoError(tb, err, "fts seed: begin tx")

	// Pull the IDs of media rows owned by p. Iterating against the
	// IDs slice instead of running a fan-out INSERT...SELECT keeps
	// the caption fraction deterministic — the SQL CASE-driven path
	// would need a deterministic hash of m.id to avoid leaking
	// SQLite's random() across runs.
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM media WHERE owner_hub = ? AND owner_user_id = ? ORDER BY id`,
		p.Hub, p.UserID,
	)
	require.NoError(tb, err, "fts seed: select media ids")
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(tb, rows.Scan(&id), "fts seed: scan media id")
		ids = append(ids, id)
	}
	require.NoError(tb, rows.Err(), "fts seed: iterate media ids")
	require.NoError(tb, rows.Close(), "fts seed: close media-id cursor")

	// Caption fan-out. ai_results carries the same task='caption' /
	// status='active' shape as production AI promotion, so the FTS
	// JOIN sees the same predicate path it would in a real library.
	resultsStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO ai_results
			(id, media_id, task, model_id, prompt_version, prompt_hash,
			 input_profile, status, generated_at)
		VALUES (?, ?, 'caption', ?, ?, ?, ?, 'active', ?)`)
	require.NoError(tb, err, "fts seed: prepare ai_results")
	defer resultsStmt.Close()

	captionStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO media_captions (result_id, text) VALUES (?, ?)`)
	require.NoError(tb, err, "fts seed: prepare media_captions")
	defer captionStmt.Close()

	rng := rand.New(rand.NewSource(opts.Seed))
	for i, id := range ids {
		if rng.Float64() >= opts.CaptionFraction {
			continue
		}
		resultID := fmt.Sprintf("scale-cap-%07d", i)
		_, err := resultsStmt.ExecContext(ctx,
			resultID, id,
			"scale-cap-model", "cap-v1", "scale-cap-hash", "scale-cap-profile",
			baseTime.Add(-time.Duration(i)*rowStride),
		)
		require.NoErrorf(tb, err, "fts seed: insert caption ai_results row %d", i)

		var sb strings.Builder
		for w := range captionWordsPerRow {
			if w > 0 {
				sb.WriteByte(' ')
			}
			sb.WriteString(captionWords[rng.Intn(len(captionWords))])
		}
		_, err = captionStmt.ExecContext(ctx, resultID, sb.String())
		require.NoErrorf(tb, err, "fts seed: insert media_captions row %d", i)
	}

	// Bulk-populate media_fts. The projection mirrors RefreshMediaFTS:
	//   - filename, camera, lens, location_label come from the media row.
	//   - caption_text comes from the active media_captions row (LEFT JOIN
	//     yields '' when no caption row exists).
	//   - tag_label is the rank-ordered tag labels GROUP_CONCAT'd. We
	//     pre-order in a CTE because GROUP_CONCAT(... ORDER BY ...) is
	//     a SQLite-3.44+ syntax that we can't assume; SQLite's
	//     implementation honors the inner ORDER BY for the aggregate
	//     in practice but we sort by (media_id, rank) explicitly to
	//     keep the determinism contract honest across versions.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO media_fts (
			media_id, caption_text, tag_label, filename, camera, lens, location_label)
		 SELECT
			m.id,
			COALESCE(cap.text, ''),
			COALESCE(tags.labels, ''),
			COALESCE(m.original_filename, ''),
			COALESCE(m.make, '')
				|| CASE WHEN m.make IS NOT NULL AND m.model IS NOT NULL THEN ' ' ELSE '' END
				|| COALESCE(m.model, ''),
			COALESCE(m.lens_model, ''),
			COALESCE(m.location_label, '')
		 FROM media m
		 LEFT JOIN (
			SELECT r.media_id, mc.text
			  FROM ai_results r
			  JOIN media_captions mc ON mc.result_id = r.id
			 WHERE r.task = 'caption' AND r.status = 'active'
		 ) cap ON cap.media_id = m.id
		 LEFT JOIN (
			SELECT media_id, GROUP_CONCAT(tag_label, ' ') AS labels FROM (
				SELECT r.media_id, mt.tag_label
				  FROM ai_results r
				  JOIN media_tags mt ON mt.result_id = r.id
				 WHERE r.task = 'tag' AND r.status = 'active'
				 ORDER BY r.media_id, mt.rank
			) GROUP BY media_id
		 ) tags ON tags.media_id = m.id
		 WHERE m.owner_hub = ? AND m.owner_user_id = ?`,
		p.Hub, p.UserID,
	); err != nil {
		require.NoError(tb, err, "fts seed: bulk insert media_fts")
	}

	require.NoError(tb, tx.Commit(), "fts seed: commit")
}
