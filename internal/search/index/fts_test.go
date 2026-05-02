package index_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/search/index"
	"github.com/wesm/fotobank/internal/testutil"
)

// ftsRow mirrors the six corpus columns of media_fts (media_id is
// excluded from the round-trip checks; UNINDEXED but not corpus).
type ftsRow struct {
	CaptionText   string
	TagLabel      string
	Filename      string
	Camera        string
	Lens          string
	LocationLabel string
}

// mediaDetails is the optional-fields shape used by seedMediaWithDetails.
// All fields are simple strings; empty string means "leave NULL where the
// schema column is nullable, otherwise the empty string." This matches
// the application's representation of optional EXIF columns.
type mediaDetails struct {
	OriginalFilename string
	Make             string
	Model            string
	LensModel        string
	LocationLabel    string
}

// seedMediaWithDetails inserts a single media row with the given optional
// fields and returns its id. NULLs are written for empty strings on
// nullable columns so the helper exercises the COALESCE / CASE concat
// path in RefreshMediaFTS.
func seedMediaWithDetails(t *testing.T, d *db.DB, p owners.Principal, det mediaDetails) string {
	t.Helper()
	id := uuid.NewString()
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO media(
			id, owner_hub, owner_user_id, media_type, mime_type, path,
			original_filename, imported_at, size, checksum,
			make, model, lens_model, location_label,
			thumb_status, thumb_version, import_source_path
		) VALUES (?,?,?, 'photo','image/jpeg', ?,
			?, ?, 0, ?,
			?, ?, ?, ?,
			'ready', 1, ?)`,
		id, p.Hub, p.UserID, "/photos/"+id+".jpg",
		nullIfEmpty(det.OriginalFilename), time.Now().UTC(), id+"-checksum",
		nullIfEmpty(det.Make), nullIfEmpty(det.Model), nullIfEmpty(det.LensModel), nullIfEmpty(det.LocationLabel),
		id,
	)
	require.NoError(t, err)
	return id
}

// nullIfEmpty turns "" into a SQL NULL so the test exercises the
// COALESCE branches; non-empty strings pass through unchanged.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// mustWriteActiveCaption inserts an ai_results + media_captions pair with
// status='active' for the given media. Mirrors the shape produced by
// internal/ai/results.WriteCaptionResultTx.
func mustWriteActiveCaption(t *testing.T, d *db.DB, mediaID, text string) {
	t.Helper()
	resultID := uuid.NewString()
	ctx := context.Background()
	_, err := d.WriteDB().ExecContext(ctx,
		`INSERT INTO ai_results(id, media_id, task, model_id, prompt_version, prompt_hash,
		 input_profile, status, generated_at) VALUES (?,?, 'caption', ?, ?, ?, ?, 'active', ?)`,
		resultID, mediaID, "test-model", "caption-v1", "test-hash", "test-profile", time.Now().UTC())
	require.NoError(t, err)
	_, err = d.WriteDB().ExecContext(ctx,
		`INSERT INTO media_captions(result_id, text) VALUES (?, ?)`, resultID, text)
	require.NoError(t, err)
}

// mustWriteActiveTags inserts an ai_results + media_tags rows with
// status='active'; ranks are assigned 1..N in the order of labels.
func mustWriteActiveTags(t *testing.T, d *db.DB, mediaID string, labels []string) {
	t.Helper()
	resultID := uuid.NewString()
	ctx := context.Background()
	_, err := d.WriteDB().ExecContext(ctx,
		`INSERT INTO ai_results(id, media_id, task, model_id, prompt_version, prompt_hash,
		 input_profile, status, generated_at) VALUES (?,?, 'tag', ?, ?, ?, ?, 'active', ?)`,
		resultID, mediaID, "test-model", "tag-v1", "test-hash", "test-profile", time.Now().UTC())
	require.NoError(t, err)
	for i, label := range labels {
		// tag_key uses an arbitrary deterministic string per row; the
		// helper only needs uniqueness within (result_id, tag_key).
		_, err := d.WriteDB().ExecContext(ctx,
			`INSERT INTO media_tags(result_id, tag_key, tag_label, rank) VALUES (?,?,?,?)`,
			resultID, fmt.Sprintf("k%d", i), label, i+1)
		require.NoError(t, err)
	}
}

// readFTSRow returns the six corpus columns of the media_fts row whose
// media_id matches mediaID. Fails the test when there is no such row.
func readFTSRow(t *testing.T, d *db.DB, mediaID string) ftsRow {
	t.Helper()
	var r ftsRow
	err := d.ReadDB().QueryRowContext(context.Background(),
		`SELECT caption_text, tag_label, filename, camera, lens, location_label
		   FROM media_fts WHERE media_id = ?`, mediaID,
	).Scan(&r.CaptionText, &r.TagLabel, &r.Filename, &r.Camera, &r.Lens, &r.LocationLabel)
	require.NoError(t, err)
	return r
}

// withTx runs fn inside a write transaction on d, committing on success
// and rolling back on error. Mirrors the helper in
// internal/ai/embedding/on_thumb_regen_test.go.
func withTx(d *db.DB, fn func(tx *sql.Tx) error) error {
	tx, err := d.WriteDB().BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func TestRefreshMediaFTS_BuildsRowFromMediaCaptionTags(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")

	mid := seedMediaWithDetails(t, d, owner, mediaDetails{
		OriginalFilename: "IMG_0001.jpg",
		Make:             "Canon",
		Model:            "EOS R5",
		LensModel:        "RF24-105mm F4 L IS USM",
		LocationLabel:    "Paris, France",
	})
	mustWriteActiveCaption(t, d, mid, "small dog on a beach")
	mustWriteActiveTags(t, d, mid, []string{"dog", "beach"})

	r.NoError(withTx(d, func(tx *sql.Tx) error {
		return index.RefreshMediaFTS(ctx, tx, mid)
	}))

	row := readFTSRow(t, d, mid)
	r.Equal("small dog on a beach", row.CaptionText)
	r.Equal("dog beach", row.TagLabel)
	r.Equal("IMG_0001.jpg", row.Filename)
	r.Equal("Canon EOS R5", row.Camera)
	r.Equal("RF24-105mm F4 L IS USM", row.Lens)
	r.Equal("Paris, France", row.LocationLabel)
}

func TestRefreshMediaFTS_ReplacesPriorRow(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")

	// First state: caption "first", one tag.
	mid := seedMediaWithDetails(t, d, owner, mediaDetails{
		OriginalFilename: "first.jpg",
		Make:             "Canon",
		Model:            "EOS R5",
	})
	mustWriteActiveCaption(t, d, mid, "first caption")
	mustWriteActiveTags(t, d, mid, []string{"alpha"})

	r.NoError(withTx(d, func(tx *sql.Tx) error {
		return index.RefreshMediaFTS(ctx, tx, mid)
	}))

	// Sanity: first state is in the FTS row.
	first := readFTSRow(t, d, mid)
	r.Equal("first caption", first.CaptionText)
	r.Equal("alpha", first.TagLabel)
	r.Equal("first.jpg", first.Filename)

	// Mutate underlying state: stale prior actives, write a new caption
	// and new tags. Mirrors the shape of an AI worker re-promotion.
	_, err := d.WriteDB().ExecContext(ctx,
		`UPDATE ai_results SET status='stale' WHERE media_id=? AND status='active'`, mid)
	r.NoError(err)
	mustWriteActiveCaption(t, d, mid, "second caption")
	mustWriteActiveTags(t, d, mid, []string{"beta", "gamma"})

	r.NoError(withTx(d, func(tx *sql.Tx) error {
		return index.RefreshMediaFTS(ctx, tx, mid)
	}))

	second := readFTSRow(t, d, mid)
	r.Equal("second caption", second.CaptionText)
	r.Equal("beta gamma", second.TagLabel)
	r.Equal("first.jpg", second.Filename)

	// And there is exactly one media_fts row for this media id.
	var n int
	r.NoError(d.ReadDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM media_fts WHERE media_id = ?`, mid).Scan(&n))
	r.Equal(1, n)
}

func TestRefreshMediaFTS_EmptyOnUnsetMediaCaptionTags(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")

	// Media row with no optional EXIF fields and no AI results.
	mid := seedMediaWithDetails(t, d, owner, mediaDetails{})

	r.NoError(withTx(d, func(tx *sql.Tx) error {
		return index.RefreshMediaFTS(ctx, tx, mid)
	}))

	row := readFTSRow(t, d, mid)
	r.Empty(row.CaptionText)
	r.Empty(row.TagLabel)
	r.Empty(row.Filename)
	r.Empty(row.Camera)
	r.Empty(row.Lens)
	r.Empty(row.LocationLabel)
}
