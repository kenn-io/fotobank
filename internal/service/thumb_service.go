package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/storage"
	"go.kenn.io/fotobank/internal/thumb"
)

// ThumbService wraps thumb.Queue + storage.Store + media.Repo. HTTP
// handlers and CLI commands go through this so auth scoping and
// versioned-key translation live in one place. Every "caller can't see
// this" case returns errs.ErrNotFound so the handler surface cannot be
// used to probe for the existence of other owners' media via a
// 403-vs-404 distinction.
type ThumbService struct {
	repo  *media.Repo
	queue *thumb.Queue
	store storage.Store
}

// NewThumbService constructs a ThumbService backed by repo, q, and s.
func NewThumbService(repo *media.Repo, q *thumb.Queue, s storage.Store) *ThumbService {
	return &ThumbService{repo: repo, queue: q, store: s}
}

// Get returns a reader for the (id, size, version) thumb when caller
// owns the row, the row is ready, and the stored version matches.
// Returns errs.ErrNotFound when any of: row missing, caller doesn't own
// row, status != "ready", version mismatch, or the row is hidden and
// includeHidden is not true. The variadic includeHidden keeps existing
// call sites compiling without change; pass true to allow returning
// hidden rows. The media row is returned alongside the reader so callers
// can derive response headers (e.g. ETag from ThumbVersion) without a
// second DB round-trip.
func (s *ThumbService) Get(
	ctx context.Context,
	id string, size thumb.Size, version int,
	caller owners.Principal,
	includeHidden ...bool,
) (io.ReadCloser, media.Media, error) {
	wantHidden := len(includeHidden) > 0 && includeHidden[0]
	m, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, media.Media{}, err
	}
	if m.Owner != caller {
		return nil, media.Media{}, fmt.Errorf("%w: media id=%s", errs.ErrNotFound, id)
	}
	if m.HiddenAt != nil && !wantHidden {
		return nil, media.Media{}, fmt.Errorf("%w: media id=%s", errs.ErrNotFound, id)
	}
	if m.ThumbStatus != "ready" {
		return nil, media.Media{}, fmt.Errorf("%w: media id=%s not ready (%s)",
			errs.ErrNotFound, id, m.ThumbStatus)
	}
	if m.ThumbVersion != version {
		return nil, media.Media{}, fmt.Errorf("%w: media id=%s version mismatch (want %d have %d)",
			errs.ErrNotFound, id, version, m.ThumbVersion)
	}
	key := thumb.ThumbKey(id, version, size)
	rc, err := s.store.ReadRange(ctx, m.Owner, key, 0, -1)
	if err != nil {
		// A regenerate can bump thumb_version between our repo read and
		// our store read — the old blob gets deleted while our reader
		// still holds the stale version. Translating os.ErrNotExist (raw
		// filesystem) and errs.ErrNotFound (future translating Stores)
		// to ErrNotFound tells the handler "gone, retry" → 404, not 500.
		if errors.Is(err, errs.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return nil, media.Media{}, fmt.Errorf("%w: media id=%s thumb blob missing",
				errs.ErrNotFound, id)
		}
		return nil, media.Media{}, fmt.Errorf("read thumb: %w", err)
	}
	return rc, m, nil
}

// Enqueue scopes filter.Owner to caller unconditionally before
// delegating to the queue. Callers cannot regenerate thumbs for other
// owners' rows regardless of what filter.Owner was set to.
func (s *ThumbService) Enqueue(
	ctx context.Context,
	caller owners.Principal,
	filter thumb.EnqueueFilter,
) (int, error) {
	filter.Owner = caller
	return s.queue.Enqueue(ctx, filter)
}
