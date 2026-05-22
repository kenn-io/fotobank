// Package index owns the application-managed FTS5 corpus that
// fotobank's lexical search runs against. The package's primary export
// is RefreshMediaFTS, called by every writer that mutates a media row's
// searchable surface (importer, AI promotion, reconcile, media-update)
// so the FTS row always reflects the same snapshot as the row that
// caused the refresh. The FTS table itself is created in the schema
// migration (see internal/db/migrations/000001_initial_schema.up.sql);
// this package does not own DDL.
package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// RefreshMediaFTS rebuilds the media_fts row for mediaID by reading the
// current state of the media row, the active caption (if any), and the
// active tag labels (rank-ordered). The DELETE + INSERT pair runs in
// the supplied transaction so a failure mid-refresh leaves the prior
// row in place and the FTS row stays consistent with whatever else the
// caller writes in the same tx (e.g. an AI promotion that flips a
// result's status to 'active' immediately before this refresh runs).
//
// The function returns an error if mediaID does not resolve to a media
// row; callers should treat that as a data-integrity bug rather than a
// missing-row case. A media row with no active caption / no active tags
// / no optional EXIF metadata produces a row with empty strings in all
// six corpus columns; the FTS row still exists so the next refresh has
// something to delete.
func RefreshMediaFTS(ctx context.Context, tx *sql.Tx, mediaID string) error {
	// Pull the four media-derived corpus columns. The CASE-aware
	// concatenation between make and model produces a single space
	// only when both halves are non-NULL, so a row with just make set
	// yields "Canon" rather than "Canon ".
	const mediaQ = `
SELECT
  COALESCE(original_filename, ''),
  COALESCE(make, '') || CASE WHEN make IS NOT NULL AND model IS NOT NULL THEN ' ' ELSE '' END
                     || COALESCE(model, ''),
  COALESCE(lens_model, ''),
  COALESCE(location_label, '')
FROM media WHERE id = ?`

	var filename, camera, lens, locationLabel string
	if err := tx.QueryRowContext(ctx, mediaQ, mediaID).Scan(
		&filename, &camera, &lens, &locationLabel,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("media %s not found", mediaID)
		}
		return fmt.Errorf("read media: %w", err)
	}

	// Pull the active caption text. LEFT JOIN so an ai_results row
	// without a child media_captions row scans NULL → ""; QueryRow
	// itself returns ErrNoRows when there is no active ai_results row
	// at all, which is a legitimate "no caption yet" case and not an
	// error.
	const captionQ = `
SELECT COALESCE(mc.text, '')
FROM ai_results r
LEFT JOIN media_captions mc ON mc.result_id = r.id
WHERE r.media_id = ? AND r.task = 'caption' AND r.status = 'active'`

	var captionText string
	if err := tx.QueryRowContext(ctx, captionQ, mediaID).Scan(&captionText); err != nil &&
		!errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read caption: %w", err)
	}

	// Pull the active tag labels in rank order. Joining with a single
	// space matches the FTS5 default tokenizer's whitespace splitter so
	// each label becomes its own token in the corpus.
	const tagsQ = `
SELECT mt.tag_label
FROM ai_results r
JOIN media_tags mt ON mt.result_id = r.id
WHERE r.media_id = ? AND r.task = 'tag' AND r.status = 'active'
ORDER BY mt.rank`

	rows, err := tx.QueryContext(ctx, tagsQ, mediaID)
	if err != nil {
		return fmt.Errorf("read tags: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var labels []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return fmt.Errorf("scan tag: %w", err)
		}
		labels = append(labels, label)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate tags: %w", err)
	}
	tagLabel := strings.Join(labels, " ")

	// Replace the existing row. DELETE first so a row with all-empty
	// corpus columns still ends up with one row (the INSERT does not
	// upsert against the unindexed media_id column).
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM media_fts WHERE media_id = ?`, mediaID,
	); err != nil {
		return fmt.Errorf("delete fts row: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO media_fts (media_id, caption_text, tag_label, filename, camera, lens, location_label)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		mediaID, captionText, tagLabel, filename, camera, lens, locationLabel,
	); err != nil {
		return fmt.Errorf("insert fts row: %w", err)
	}
	return nil
}
