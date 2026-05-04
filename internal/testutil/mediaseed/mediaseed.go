// Package mediaseed supplies shared media + tag fixture helpers for
// tests that need facet-shape rows (camera, lens, GPS, tag) seeded
// via a small Media template overlay. Lives in a subpackage so it
// can import internal/media without re-introducing a cycle into the
// general internal/testutil package, which deliberately stays
// dependency-light.
package mediaseed

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
)

// InsertMedia inserts a primary photo for owner with fields from the
// supplied template overlaid on a minimally-valid base row. The path
// and checksum are derived from id so multiple calls in one test
// don't collide on the (owner, checksum) or (owner, path) unique
// indexes. Use it to stage rows for facet aggregations or filter
// list tests where only a few EXIF columns matter.
func InsertMedia(t *testing.T, rw *sql.DB, p owners.Principal, id string, m media.Media) {
	t.Helper()
	repo := media.NewRepo(rw, rw)
	row := baseMedia(id, p)
	if m.Type != "" {
		row.Type = m.Type
	}
	if m.Make != "" {
		row.Make = m.Make
	}
	if m.Model != "" {
		row.Model = m.Model
	}
	if m.LensModel != "" {
		row.LensModel = m.LensModel
	}
	if m.Latitude != nil {
		row.Latitude = m.Latitude
	}
	if m.Longitude != nil {
		row.Longitude = m.Longitude
	}
	require.NoError(t, repo.Insert(context.Background(), row))
}

// InsertTag inserts an active ai_results row for (owner, mediaID) plus
// one media_tags row keyed on (key, label). The owner principal is
// accepted but unused — ai_results inherits its owner via media_id;
// callers pass it for symmetry with other seed helpers and to make
// ownership obvious at the call site.
func InsertTag(t *testing.T, rw *sql.DB, _ owners.Principal, mediaID, key, label string) {
	t.Helper()
	ctx := context.Background()
	resultID := uuid.NewString()
	_, err := rw.ExecContext(ctx,
		`INSERT INTO ai_results(id, media_id, task, model_id, prompt_version, prompt_hash,
			input_profile, status, generated_at) VALUES (?,?, 'tag', ?, ?, ?, ?, 'active', ?)`,
		resultID, mediaID, "test-model", "tag-v1", "test-hash", "test-profile", time.Now().UTC())
	require.NoError(t, err)
	_, err = rw.ExecContext(ctx,
		`INSERT INTO media_tags(result_id, tag_key, tag_label, rank) VALUES (?,?,?,?)`,
		resultID, key, label, 1)
	require.NoError(t, err)
}

// baseMedia returns a minimally-valid media template suitable for
// overlay in InsertMedia. Path and Checksum are derived from id so
// fixtures don't trip the (owner, checksum) / (owner, path) unique
// indexes.
func baseMedia(id string, p owners.Principal) media.Media {
	return media.Media{
		ID:               id,
		Owner:            p,
		Type:             media.TypePhoto,
		MimeType:         "image/jpeg",
		Path:             "2024/" + id + ".jpg",
		OriginalFilename: id + ".jpg",
		ImportedAt:       time.Now().UTC().Truncate(time.Second),
		Size:             100,
		Checksum:         "cs-" + id,
		ThumbStatus:      "pending",
	}
}
