package usersettings

import (
	"context"

	"github.com/wesm/fotobank/internal/owners"
)

// Service is a thin caller-scoped wrapper around Repo. Every method
// takes the caller principal and forwards to the repo as that principal.
type Service struct {
	repo *Repo
}

func NewService(repo *Repo) *Service { return &Service{repo: repo} }

func (s *Service) Set(ctx context.Context, caller owners.Principal, key, valueJSON string) error {
	return s.repo.Upsert(ctx, caller, key, valueJSON)
}

func (s *Service) Get(ctx context.Context, caller owners.Principal, key string) (string, bool, error) {
	return s.repo.Get(ctx, caller, key)
}

func (s *Service) Delete(ctx context.Context, caller owners.Principal, key string) error {
	return s.repo.Delete(ctx, caller, key)
}
