package imginput

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
)

// LocateFunc returns the on-disk path for a media's preview blob.
// Production wiring should use the thumb storage helper; tests inject
// a func that points at a temp dir.
type LocateFunc func(mediaID string) string

// Resolver is the production ImageResolver implementation.
type Resolver struct {
	rw     *sql.DB
	ro     *sql.DB
	locate LocateFunc
}

// NewResolver constructs a Resolver. `locate` returns the absolute
// filesystem path of the preview blob for a given media id.
func NewResolver(rw, ro *sql.DB, locate LocateFunc) *Resolver {
	return &Resolver{rw: rw, ro: ro, locate: locate}
}

// ResolveAndEncode reads thumb_status, loads the preview blob, and
// re-encodes it to the AI input profile. When thumb_status is not
// "ready", returns the status verbatim so the worker can pick the
// right branch (block on pending/working, skip on no_preview).
func (r *Resolver) ResolveAndEncode(ctx context.Context, mediaID string) ([]byte, string, error) {
	var status string
	row := r.ro.QueryRowContext(ctx, `SELECT thumb_status FROM media WHERE id=?`, mediaID)
	switch err := row.Scan(&status); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, "", fmt.Errorf("media %s not found", mediaID)
	case err != nil:
		return nil, "", fmt.Errorf("read thumb_status: %w", err)
	}
	if status != "ready" {
		return nil, status, nil
	}
	path := r.locate(mediaID)
	srcJPEG, err := os.ReadFile(path)
	if err != nil {
		return nil, status, fmt.Errorf("read preview %s: %w", path, err)
	}
	out, err := Encode(srcJPEG)
	if err != nil {
		return nil, status, fmt.Errorf("encode profile: %w", err)
	}
	return out, status, nil
}
