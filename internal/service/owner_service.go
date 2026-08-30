// Package service holds orchestration types that compose repositories
// and cross-cutting concerns. OwnerService wraps owners.Repo with the
// policy rules needed by CLI and HTTP callers (idempotent Ensure, safe
// Remove, display-handle updates).
package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
)

// OwnerRepo is the subset of owners.Repo that OwnerService depends on.
// Accepting an interface here lets tests inject fakes that simulate
// concurrency races without spinning real goroutines.
type OwnerRepo interface {
	Insert(ctx context.Context, o owners.Owner) error
	GetByPrincipal(ctx context.Context, p owners.Principal) (owners.Owner, error)
	List(ctx context.Context) ([]owners.Owner, error)
	Delete(ctx context.Context, p owners.Principal) error
	UpdateDisplayHandle(ctx context.Context, p owners.Principal, handle string) error
	DB() *sql.DB
}

// OwnerService orchestrates owner lifecycle operations on top of an
// OwnerRepo. The now field is a seam for deterministic timestamps in
// tests; production callers get time.Now().UTC() via NewOwnerService.
type OwnerService struct {
	repo OwnerRepo
	now  func() time.Time
}

// NewOwnerService constructs an OwnerService that uses repo for
// persistence and time.Now().UTC() as its clock.
func NewOwnerService(repo OwnerRepo) *OwnerService {
	return &OwnerService{repo: repo, now: func() time.Time { return time.Now().UTC() }}
}

// Ensure inserts an owners row if missing; is a no-op if the same
// principal+storage_key already exists; returns ErrAlreadyExists if
// the principal exists with a different storage_key. Safe under
// concurrent callers: an Insert that races and loses to another caller
// inserting the same row is reinterpreted via a re-read.
func (s *OwnerService) Ensure(
	ctx context.Context,
	p owners.Principal,
	requestedStorageKey string,
) (owners.Owner, error) {
	existing, err := s.repo.GetByPrincipal(ctx, p)
	switch {
	case err == nil:
		return reconcileStorageKey(existing, p, requestedStorageKey)
	case errors.Is(err, errs.ErrNotFound):
		requestedWasEmpty := requestedStorageKey == ""
		storageKey, normalizeErr := normalizeStorageKey(requestedStorageKey)
		if normalizeErr != nil {
			return owners.Owner{}, normalizeErr
		}
		owner := owners.Owner{Principal: p, StorageKey: storageKey, CreatedAt: s.now()}
		insertErr := s.repo.Insert(ctx, owner)
		if insertErr == nil {
			return owner, nil
		}
		// A concurrent caller may have inserted a row between our
		// GetByPrincipal probe and this Insert. Re-read: if the stored
		// storage_key matches, the caller's intent was already realised.
		existing, getErr := s.repo.GetByPrincipal(ctx, p)
		if getErr != nil {
			return owners.Owner{}, insertErr
		}
		if requestedWasEmpty {
			return existing, nil
		}
		return reconcileStorageKey(existing, p, storageKey)
	default:
		return owners.Owner{}, err
	}
}

func normalizeStorageKey(requested string) (string, error) {
	if requested == "" {
		return uuid.NewString(), nil
	}
	parsed, err := uuid.Parse(requested)
	if err != nil {
		return "", fmt.Errorf("%w: storage key must be a UUID", errs.ErrInvalidArgument)
	}
	return parsed.String(), nil
}

func reconcileStorageKey(
	existing owners.Owner,
	p owners.Principal,
	requestedStorageKey string,
) (owners.Owner, error) {
	if requestedStorageKey == "" {
		return existing, nil
	}
	storageKey, err := normalizeStorageKey(requestedStorageKey)
	if err != nil {
		return owners.Owner{}, err
	}
	if existing.StorageKey == storageKey {
		return existing, nil
	}
	return owners.Owner{}, fmt.Errorf("%w: owner %s has storage_key %q, got %q",
		errs.ErrAlreadyExists, p, existing.StorageKey, storageKey)
}

// List returns all registered owners ordered by (hub, user_id).
func (s *OwnerService) List(ctx context.Context) ([]owners.Owner, error) {
	return s.repo.List(ctx)
}

// Remove deletes the owner row for p. With purge=false, it refuses if
// any asset or checkout rows still reference the owner. Purge=true is reserved
// for a future operation that also deletes owned content, and is rejected here.
func (s *OwnerService) Remove(ctx context.Context, p owners.Principal, purge bool) error {
	if purge {
		return fmt.Errorf("%w: --purge is not implemented", errs.ErrInvalidArgument)
	}
	var assetCount int
	row := s.repo.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM assets WHERE owner_hub=? AND owner_user_id=?`,
		p.Hub, p.UserID)
	if err := row.Scan(&assetCount); err != nil {
		return fmt.Errorf("count content for owner: %w", err)
	}
	if assetCount > 0 {
		return fmt.Errorf("%w: owner %s has %d assets (use --purge)",
			errs.ErrInvalidArgument, p, assetCount)
	}
	var checkoutCount int
	row = s.repo.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM checkouts WHERE owner_hub=? AND owner_user_id=?`,
		p.Hub, p.UserID)
	if err := row.Scan(&checkoutCount); err != nil {
		return fmt.Errorf("count checkouts for owner: %w", err)
	}
	if checkoutCount > 0 {
		return fmt.Errorf("%w: owner %s has %d checkouts",
			errs.ErrInvalidArgument, p, checkoutCount)
	}
	return s.repo.Delete(ctx, p)
}

// UpdateDisplay sets the display_handle for an existing owner.
// Returns errs.ErrNotFound if no such owner is registered.
func (s *OwnerService) UpdateDisplay(ctx context.Context, p owners.Principal, handle string) error {
	return s.repo.UpdateDisplayHandle(ctx, p, handle)
}
