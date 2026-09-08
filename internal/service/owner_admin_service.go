package service

import (
	"context"
	"fmt"
	"sync"

	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/storage"
)

// OwnerAdminService coordinates catalog changes with the running artifact store.
// It is exposed only through the host-operator API, never photo-user routes.
type OwnerAdminService struct {
	mu          sync.Mutex
	owners      *OwnerService
	store       *storage.NASOnly
	activeOwner owners.Principal
}

func NewOwnerAdminService(svc *OwnerService, store *storage.NASOnly, activeOwner owners.Principal) *OwnerAdminService {
	return &OwnerAdminService{owners: svc, store: store, activeOwner: activeOwner}
}

func (s *OwnerAdminService) Register(ctx context.Context, principal owners.Principal, key, handle string) (owners.Owner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner, err := s.owners.Ensure(ctx, principal, key)
	if err != nil {
		return owners.Owner{}, err
	}
	s.store.SetOwnerKey(principal, owner.StorageKey)
	if handle != "" {
		if err := s.owners.UpdateDisplay(ctx, principal, handle); err != nil {
			return owners.Owner{}, err
		}
		owner.DisplayHandle = handle
	}
	return owner, nil
}

func (s *OwnerAdminService) List(ctx context.Context) ([]owners.Owner, error) {
	return s.owners.List(ctx)
}

func (s *OwnerAdminService) Remove(ctx context.Context, principal owners.Principal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.activeOwner.IsZero() && principal == s.activeOwner {
		return fmt.Errorf("%w: cannot remove the configured stub owner; change the identity configuration and restart first", errs.ErrAlreadyExists)
	}
	if err := s.owners.Remove(ctx, principal, false); err != nil {
		return err
	}
	s.store.RemoveOwnerKey(principal)
	return nil
}
