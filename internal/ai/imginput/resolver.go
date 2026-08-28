package imginput

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"

	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/storage"
	"go.kenn.io/fotobank/internal/thumb"
)

// Resolver is the production ImageResolver implementation. It reads the
// owner, thumb_status, and thumb_version from the media row, then
// streams the versioned preview blob through storage.Store. This
// matches the on-disk layout the thumb worker writes
// (thumb.ThumbKey(id, version, thumb.SizePreview)) so a regenerate that
// bumps thumb_version doesn't leave the resolver pointing at a stale or
// missing file.
type Resolver struct {
	ro    *sql.DB
	store storage.Store
}

// NewResolver constructs a Resolver. The Store is used to read the
// versioned preview blob; pass the same Store the thumb worker writes
// to so the keys match.
func NewResolver(ro *sql.DB, store storage.Store) *Resolver {
	return &Resolver{ro: ro, store: store}
}

// ResolvePreviewJPEG reads the media row and loads the versioned
// preview blob verbatim — no re-encoding. When thumb_status is not
// "ready", returns the status verbatim so the worker can pick the right
// branch (block on pending/working, skip on no_preview).
func (r *Resolver) ResolvePreviewJPEG(ctx context.Context, mediaID string) ([]byte, string, error) {
	var (
		hub     string
		userID  string
		status  string
		version int
	)
	row := r.ro.QueryRowContext(ctx,
		`SELECT owner_hub, owner_user_id, thumb_status, thumb_version
		   FROM assets WHERE id=? AND state='ready'`, mediaID)
	switch err := row.Scan(&hub, &userID, &status, &version); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, "", fmt.Errorf("media %s not found", mediaID)
	case err != nil:
		return nil, "", fmt.Errorf("read media %s: %w", mediaID, err)
	}
	if status != "ready" {
		return nil, status, nil
	}
	owner := owners.Principal{Hub: hub, UserID: userID}
	key := thumb.ThumbKey(mediaID, version, thumb.SizePreview)
	rc, err := r.store.ReadRange(ctx, owner, key, 0, -1)
	if err != nil {
		return nil, status, fmt.Errorf("read preview %s: %w", key, err)
	}
	defer func() { _ = rc.Close() }()
	jpg, err := io.ReadAll(rc)
	if err != nil {
		return nil, status, fmt.Errorf("read preview %s: %w", key, err)
	}
	return jpg, status, nil
}
