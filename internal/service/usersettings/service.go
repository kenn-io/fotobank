package usersettings

import (
	"context"
	"fmt"
	"strings"

	"go.kenn.io/fotobank/internal/owners"
)

// AIInspectionKey is the canonical user_settings key for the per-caller
// "AI Inspection" toggle. Surfacing the constant from the package keeps
// callers (the search service's UserSettingsRepo adapter, the SettingsAI
// frontend route, this service's own typed accessor) reading the same
// string. The value is a JSON boolean literal: "true" enables, anything
// else disables (fail-closed).
const AIInspectionKey = "ai.inspection"

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

// AIInspectionEnabled is the typed accessor for the AIInspectionKey
// toggle. Returns false when the row is missing (the default for an
// untouched account) and false for any payload that is not the literal
// JSON `true` — a malformed value (e.g. `"yes"`, `1`, `null`) is
// treated as off rather than parsed permissively. This matches the
// adapter shape consumed by the search service's UserSettingsRepo
// interface (see internal/service/search/service.go) so production
// wiring can pass *Service directly without an extra adapter type.
func (s *Service) AIInspectionEnabled(ctx context.Context, caller owners.Principal) (bool, error) {
	val, ok, err := s.repo.Get(ctx, caller, AIInspectionKey)
	if err != nil {
		return false, fmt.Errorf("read ai inspection setting: %w", err)
	}
	if !ok {
		return false, nil
	}
	return strings.TrimSpace(val) == "true", nil
}
