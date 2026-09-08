package service

import (
	"context"
	"fmt"
	"time"

	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/ingest"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
)

type OwnerRecoveryReport struct {
	Owner       owners.Principal `json:"owner"`
	Adopted     int              `json:"adopted"`
	Finalized   int              `json:"finalized"`
	Pending     int              `json:"pending"`
	Conflicts   int              `json:"conflicts"`
	OrphanPaths []string         `json:"orphan_paths"`
}

// Recover grants the configured host operator recovery across all owners.
// It uses the import lock and the daemon's existing storage, never a new vault.
func (s *ImportService) Recover(ctx context.Context, caller owners.Principal, wait time.Duration) ([]OwnerRecoveryReport, error) {
	reports := []OwnerRecoveryReport{}
	if caller != s.owner {
		return reports, fmt.Errorf("recovery operator does not match: %w", errs.ErrPermissionDenied)
	}
	if wait < 0 {
		return reports, fmt.Errorf("%w: wait must be non-negative", errs.ErrInvalidArgument)
	}
	unlock, err := ingest.Acquire(ctx, s.config.LockPath, wait)
	if err != nil {
		return reports, err
	}
	defer unlock()
	registered, err := owners.NewRepo(s.database.WriteDB(), s.database.ReadDB()).List(ctx)
	if err != nil {
		return reports, err
	}
	var enqueuer ingest.AIEnqueuer
	if s.enqueuer != nil {
		enqueuer = s.enqueuer()
	}
	assets := media.NewAssetRepo(s.database.WriteDB(), s.database.ReadDB())
	mediaRepo := media.NewRepo(s.database.WriteDB(), s.database.ReadDB())
	for _, owner := range registered {
		if err := ctx.Err(); err != nil {
			return reports, err
		}
		importer := ingest.NewImporter(s.vault, assets, mediaRepo, owner.StorageKey, s.places)
		if enqueuer != nil {
			importer.SetAIEnqueuer(enqueuer)
		}
		result, err := importer.RecoverOwner(ctx, owner.Principal)
		report := OwnerRecoveryReport{Owner: owner.Principal, Adopted: result.Adopted,
			Finalized: result.Finalized, Pending: result.Pending, Conflicts: result.Conflicts,
			OrphanPaths: []string{}}
		for _, orphan := range result.Orphans {
			report.OrphanPaths = append(report.OrphanPaths, orphan.VirtualPath)
		}
		reports = append(reports, report)
		if err != nil {
			return reports, fmt.Errorf("recover owner %s: %w", owner.Principal, err)
		}
	}
	return reports, ctx.Err()
}
