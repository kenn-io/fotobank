// Package service holds orchestration types that compose repositories
// and cross-cutting concerns. OwnerService wraps owners.Repo with the
// policy rules needed by CLI and HTTP callers (idempotent Ensure, safe
// Remove, display-handle updates).
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
)

// OwnerService orchestrates owner lifecycle operations on top of an
// owners.Repo. The now field is a seam for deterministic timestamps in
// tests; production callers get time.Now().UTC() via NewOwnerService.
type OwnerService struct {
	repo *owners.Repo
	now  func() time.Time
}

// NewOwnerService constructs an OwnerService that uses repo for
// persistence and time.Now().UTC() as its clock.
func NewOwnerService(repo *owners.Repo) *OwnerService {
	return &OwnerService{repo: repo, now: func() time.Time { return time.Now().UTC() }}
}

// Ensure inserts an owners row if missing; is a no-op if the same
// principal+storage_key already exists; returns ErrAlreadyExists if
// the principal exists with a different storage_key. Safe under
// concurrent callers: an Insert that races and loses to another caller
// inserting the same row is reinterpreted via a re-read.
func (s *OwnerService) Ensure(ctx context.Context, p owners.Principal, storageKey string) error {
	existing, err := s.repo.GetByPrincipal(ctx, p)
	switch {
	case err == nil:
		return s.reconcileStorageKey(existing, p, storageKey)
	case errors.Is(err, errs.ErrNotFound):
		insertErr := s.repo.Insert(ctx, owners.Owner{
			Principal: p, StorageKey: storageKey, CreatedAt: s.now(),
		})
		if insertErr == nil {
			return nil
		}
		// A concurrent caller may have inserted a row between our
		// GetByPrincipal probe and this Insert. Re-read: if the stored
		// storage_key matches, the caller's intent was already realised.
		existing, getErr := s.repo.GetByPrincipal(ctx, p)
		if getErr != nil {
			return insertErr
		}
		return s.reconcileStorageKey(existing, p, storageKey)
	default:
		return err
	}
}

func (s *OwnerService) reconcileStorageKey(existing owners.Owner, p owners.Principal, storageKey string) error {
	if existing.StorageKey == storageKey {
		return nil
	}
	return fmt.Errorf("%w: owner %s has storage_key %q, got %q",
		errs.ErrAlreadyExists, p, existing.StorageKey, storageKey)
}

// List returns all registered owners ordered by (hub, user_id).
func (s *OwnerService) List(ctx context.Context) ([]owners.Owner, error) {
	return s.repo.List(ctx)
}

// Remove deletes the owner row for p. With purge=false, it refuses if
// any media rows still reference the owner. Purge=true is reserved for
// Plan B (storage-layer byte deletion) and is rejected here.
func (s *OwnerService) Remove(ctx context.Context, p owners.Principal, purge bool) error {
	if purge {
		return fmt.Errorf("%w: --purge requires the storage layer (Plan B)", errs.ErrInvalidArgument)
	}
	var n int
	row := s.repo.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM media WHERE owner_hub=? AND owner_user_id=?`, p.Hub, p.UserID)
	if err := row.Scan(&n); err != nil {
		return fmt.Errorf("count media for owner: %w", err)
	}
	if n > 0 {
		return fmt.Errorf("%w: owner %s has %d media rows (use --purge)",
			errs.ErrInvalidArgument, p, n)
	}
	return s.repo.Delete(ctx, p)
}

// UpdateDisplay sets the display_handle for an existing owner.
// Returns errs.ErrNotFound if no such owner is registered.
func (s *OwnerService) UpdateDisplay(ctx context.Context, p owners.Principal, handle string) error {
	return s.repo.UpdateDisplayHandle(ctx, p, handle)
}
