package mediaseed

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/owners"
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
// Determinism: opts.Seed seeds the RNG, so the same opts produce the
// same DB content. Tests that assert on row counts or distributions
// can rely on this.
func SeedScaleLibrary(tb testing.TB, rw *sql.DB, p owners.Principal, opts ScaleOpts) []string {
	tb.Helper()
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
	require.NoError(tb, err, "seed: begin tx")

	// Owner row first — media has FK to owners(hub, user_id). INSERT
	// OR IGNORE so callers can call SeedScaleLibrary multiple times
	// against the same DB (e.g. re-seed for a different bench) or
	// stack a small fixture on top of an already-seeded owner.
	_, err = tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO owners(hub, user_id, storage_key, created_at)
		 VALUES (?, ?, ?, ?)`,
		p.Hub, p.UserID, "scale-storage", time.Now().UTC(),
	)
	require.NoError(tb, err, "seed: insert owner")

	mediaStmt, err := tx.PrepareContext(ctx, mediaInsertSQL)
	require.NoError(tb, err, "seed: prepare media")
	defer mediaStmt.Close()

	resultsStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO ai_results
			(id, media_id, task, model_id, prompt_version, prompt_hash,
			 input_profile, status, generated_at)
		VALUES (?, ?, 'tag', ?, ?, ?, ?, 'active', ?)`)
	require.NoError(tb, err, "seed: prepare ai_results")
	defer resultsStmt.Close()

	tagStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO media_tags (result_id, tag_key, tag_label, rank)
		VALUES (?, ?, ?, ?)`)
	require.NoError(tb, err, "seed: prepare media_tags")
	defer tagStmt.Close()

	base := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	ids := make([]string, opts.Total)

	for i := 0; i < opts.Total; i++ {
		id := fmt.Sprintf("scale-%07d", i)
		ids[i] = id

		c := cameras[cameraPick.Uint64()]
		l := lenses[lensPick.Uint64()]

		var lat, lon any // any so nullable values flow as NULL
		if rng.Float64() < opts.GPSFraction {
			lat = -90 + rng.Float64()*180
			lon = -180 + rng.Float64()*360
		}

		var hiddenAt any
		if rng.Float64() < opts.HiddenFraction {
			hiddenAt = base.Add(-time.Duration(i) * time.Minute)
		}

		_, err := mediaStmt.ExecContext(ctx,
			id, p.Hub, p.UserID, "photo", "image/jpeg",
			"scale/"+id+".jpg", id+".jpg",
			base.Add(-time.Duration(i)*time.Minute), nil, // imported_at, timestamp
			int64(1000), "cs-"+id,
			c.Make, c.Model, l.Model, nil, nil, nil, nil, nil, nil, // EXIF detail nulled
			nil,                // duration_ms
			lat, lon, nil, nil, // gps_at, location_label nulled
			"ready", 0, nil, // thumb_status, thumb_version, thumb_updated_at
			"scale/"+id+".jpg", nil, // import_source_path, paired_with_id
			hiddenAt,
		)
		require.NoErrorf(tb, err, "seed: insert media row %d", i)

		// Tag fan-out: ~50% of rows get tags, those that do get 1-3.
		// Skip the rest so the population isn't 100% tagged (real
		// libraries have un-AI'd rows).
		if rng.Float64() >= 0.50 {
			continue
		}
		resultID := uuid.NewString()
		_, err = resultsStmt.ExecContext(ctx,
			resultID, id,
			"scale-model", "tag-v1", "scale-hash", "scale-profile",
			base.Add(-time.Duration(i)*time.Minute),
		)
		require.NoErrorf(tb, err, "seed: insert ai_results row %d", i)
		nTags := 1 + rng.Intn(3)
		seen := make(map[string]struct{}, nTags)
		for j := range nTags {
			tagKey := tagKeys[tagPick.Uint64()]
			if _, dup := seen[tagKey]; dup {
				continue // PK is (result_id, tag_key) — skip duplicates rather than retry
			}
			seen[tagKey] = struct{}{}
			_, err := tagStmt.ExecContext(ctx, resultID, tagKey, tagKey, j+1)
			require.NoErrorf(tb, err, "seed: insert media_tags row %d/%d", i, j)
		}
	}

	require.NoError(tb, tx.Commit(), "seed: commit")
	return ids
}

// ScaleOpts controls SeedScaleLibrary's distribution shape.
type ScaleOpts struct {
	Total          int     // total media rows
	NumCameras     int     // distinct (make,model) pairs
	NumLenses      int     // distinct lens model names
	NumTags        int     // distinct tag keys
	GPSFraction    float64 // fraction with non-null lat/lon, 0..1
	HiddenFraction float64 // fraction with non-null hidden_at, 0..1
	Seed           int64   // RNG seed; same opts → same DB content
}

// DefaultScaleOpts returns balanced defaults for a library of total
// rows: ~50 cameras / 150 lenses / 500 tags, 30% GPS, 5% hidden,
// deterministic seed=42. Callers can override individual fields.
func DefaultScaleOpts(total int) ScaleOpts {
	return ScaleOpts{
		Total:          total,
		NumCameras:     50,
		NumLenses:      150,
		NumTags:        500,
		GPSFraction:    0.30,
		HiddenFraction: 0.05,
		Seed:           42,
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
