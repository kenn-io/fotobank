package ai

import (
	"context"
	"fmt"
	"time"

	"go.kenn.io/fotobank/internal/ai/embedding"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
)

// GenerationAdmin is exposed only to host operators in stub deployments.
// It shares the daemon's registry, activation counter, and compactor.
type GenerationAdmin struct {
	owner      owners.Principal
	gens       *embedding.Generations
	activator  *embedding.Activator
	compactor  *embedding.Compactor
	retainDays int
}

func NewGenerationAdmin(owner owners.Principal, gens *embedding.Generations, activator *embedding.Activator, compactor *embedding.Compactor, retainDays int) *GenerationAdmin {
	return &GenerationAdmin{owner, gens, activator, compactor, retainDays}
}

type GenerationInfo struct {
	ID            int64      `json:"id"`
	Fingerprint   string     `json:"fingerprint"`
	ModelID       string     `json:"model_id"`
	InputProfile  string     `json:"input_profile"`
	State         string     `json:"state"`
	Dimension     int        `json:"dimension"`
	EmbeddedCount int        `json:"embedded_count"`
	EligibleCount int        `json:"eligible_count"`
	CreatedAt     time.Time  `json:"created_at"`
	ActivatedAt   *time.Time `json:"activated_at,omitempty"`
	RetiredAt     *time.Time `json:"retired_at,omitempty"`
}

type GenerationDetails struct {
	Generation        GenerationInfo `json:"generation"`
	RetainRetiredDays int            `json:"retain_retired_days"`
}

func generationInfo(row embedding.Row, eligible int) GenerationInfo {
	return GenerationInfo{row.ID, row.Fingerprint, row.ModelID, row.InputProfile, row.State, row.Dimension, row.EmbeddedCount, eligible, row.CreatedAt, row.ActivatedAt, row.RetiredAt}
}

func (s *GenerationAdmin) List(ctx context.Context, caller owners.Principal, state string) ([]GenerationInfo, error) {
	if caller.IsZero() || caller != s.owner {
		return nil, errs.ErrNotFound
	}
	states := []string{state}
	switch state {
	case "":
		states = []string{"building", "active", "retired"}
	case "building", "active", "retired":
	default:
		return nil, errs.ErrInvalidArgument
	}
	eligible, err := s.activator.EligibleCount(ctx)
	if err != nil {
		return nil, err
	}
	items := []GenerationInfo{}
	for _, state := range states {
		rows, err := s.gens.List(ctx, state)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			items = append(items, generationInfo(row, eligible))
		}
	}
	return items, nil
}

func (s *GenerationAdmin) Get(ctx context.Context, caller owners.Principal, id int64) (GenerationDetails, error) {
	if caller.IsZero() || caller != s.owner {
		return GenerationDetails{}, errs.ErrNotFound
	}
	if id <= 0 {
		return GenerationDetails{}, errs.ErrInvalidArgument
	}
	row, err := s.gens.GetByID(ctx, id)
	if err != nil {
		return GenerationDetails{}, err
	}
	eligible, err := s.activator.EligibleCount(ctx)
	if err != nil {
		return GenerationDetails{}, err
	}
	return GenerationDetails{generationInfo(*row, eligible), s.retainDays}, nil
}

type GenerationPromotion struct {
	ID          int64
	Fingerprint string
}

func (s *GenerationAdmin) Promote(ctx context.Context, caller owners.Principal, id int64) (GenerationPromotion, error) {
	if caller.IsZero() || caller != s.owner {
		return GenerationPromotion{}, errs.ErrNotFound
	}
	if id <= 0 {
		return GenerationPromotion{}, errs.ErrInvalidArgument
	}
	row, err := s.gens.GetByID(ctx, id)
	if err != nil {
		return GenerationPromotion{}, err
	}
	if row.State != "retired" {
		return GenerationPromotion{}, fmt.Errorf("%w: promotion requires a retired generation; generation %d is %s", errs.ErrInvalidArgument, id, row.State)
	}
	if err := s.gens.PromoteRetired(ctx, id); err != nil {
		return GenerationPromotion{}, err
	}
	return GenerationPromotion{ID: row.ID, Fingerprint: row.Fingerprint}, nil
}

func (s *GenerationAdmin) Compact(ctx context.Context, caller owners.Principal, dryRun bool) ([]embedding.CompactCandidate, int, error) {
	if caller.IsZero() || caller != s.owner {
		return nil, 0, errs.ErrNotFound
	}
	if dryRun {
		rows, err := s.compactor.Candidates(ctx)
		return rows, 0, err
	}
	n, err := s.compactor.SweepOnce(ctx)
	return []embedding.CompactCandidate{}, n, err
}
