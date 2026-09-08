package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/ingest"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
)

type ImportConfig struct {
	LockPath       string
	Workers        int
	SettleInterval time.Duration
}

// ImportService imports host files as the configured local operator, using
// only the daemon's existing storage handles. It owns no background jobs.
type ImportService struct {
	owner    owners.Principal
	database *db.DB
	vault    *content.Adapter
	places   media.PlaceResolver
	config   ImportConfig
	enqueuer func() ingest.AIEnqueuer
}

func NewImportService(owner owners.Principal, database *db.DB, vault *content.Adapter, places media.PlaceResolver, config ImportConfig, enqueuer func() ingest.AIEnqueuer) *ImportService {
	return &ImportService{owner: owner, database: database, vault: vault, places: places, config: config, enqueuer: enqueuer}
}

func (s *ImportService) Import(ctx context.Context, caller owners.Principal, source string, workers int, wait time.Duration, progress func(ingest.ProgressEvent)) (ingest.Result, error) {
	if caller != s.owner {
		return ingest.Result{}, fmt.Errorf("import operator does not match: %w", errs.ErrPermissionDenied)
	}
	if !filepath.IsAbs(source) || workers < 0 || wait < 0 {
		return ingest.Result{}, fmt.Errorf("%w: import requires an absolute source path and non-negative workers and wait", errs.ErrInvalidArgument)
	}
	unlock, err := ingest.Acquire(ctx, s.config.LockPath, wait)
	if err != nil {
		return ingest.Result{}, err
	}
	defer unlock()
	owner, err := owners.NewRepo(s.database.WriteDB(), s.database.ReadDB()).GetByPrincipal(ctx, caller)
	if err != nil {
		return ingest.Result{}, err
	}
	importer := ingest.NewImporter(s.vault,
		media.NewAssetRepo(s.database.WriteDB(), s.database.ReadDB()),
		media.NewRepo(s.database.WriteDB(), s.database.ReadDB()), owner.StorageKey, s.places)
	// Capture the effective settings once for this import, after waiting for
	// the lock; later imports see any settings changed through the daemon.
	if s.enqueuer != nil {
		importer.SetAIEnqueuer(s.enqueuer())
	}
	if workers == 0 {
		workers = s.config.Workers
	}
	result, err := importer.ImportDirectory(ctx, source, ingest.Options{
		Owner: caller, ConcurrentWorkers: workers,
		SettleInterval: s.config.SettleInterval, Progress: progress,
	})
	if err == nil && len(result.Failures) > 0 {
		err = fmt.Errorf("import completed with %d failure(s)", len(result.Failures))
	}
	return result, errors.Join(err, ctx.Err())
}
