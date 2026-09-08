package service

import (
	"context"
	"fmt"
	"time"

	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/contentresolver"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
)

type GPSFailure struct {
	Owner owners.Principal `json:"owner"`
	ID    string           `json:"id"`
	Error string           `json:"error"`
}
type GPSResult struct {
	Processed int          `json:"processed"`
	Updated   int          `json:"updated"`
	Unchanged int          `json:"unchanged"`
	Failed    int          `json:"failed"`
	Failures  []GPSFailure `json:"failures"`
}
type GPSOptions struct {
	Mode      media.GPSBackfillMode
	Since     *time.Time
	AllOwners bool
}

// GPSService is a host-operator service, never exposed to photo-user requests.
// An authenticated host operator may select any owner or all registered owners.
type GPSService struct {
	database *db.DB
	content  *content.Adapter
	resolve  *contentresolver.Resolver
	places   media.PlaceResolver
}

func NewGPSService(database *db.DB, vault *content.Adapter, resolver *contentresolver.Resolver, places media.PlaceResolver) *GPSService {
	return &GPSService{database: database, content: vault, resolve: resolver, places: places}
}
func (s *GPSService) Backfill(ctx context.Context, caller owners.Principal, options GPSOptions) (GPSResult, error) {
	result := GPSResult{Failures: []GPSFailure{}}
	if options.Mode != media.GPSBackfillModeFull && options.Mode != media.GPSBackfillModeFillMissing && options.Mode != media.GPSBackfillModeRelabel {
		return result, fmt.Errorf("%w: invalid GPS backfill mode", errs.ErrInvalidArgument)
	}
	if !options.AllOwners && (caller.Hub == "" || caller.UserID == "") {
		return result, fmt.Errorf("%w: an owner is required", errs.ErrInvalidArgument)
	}
	principals := []owners.Principal{caller}
	if options.AllOwners {
		registered, err := owners.NewRepo(s.database.WriteDB(), s.database.ReadDB()).List(ctx)
		if err != nil {
			return result, err
		}
		principals = nil
		for _, owner := range registered {
			principals = append(principals, owner.Principal)
		}
	}
	repo := media.NewRepo(s.database.WriteDB(), s.database.ReadDB())
	b := backfiller{svc: NewMediaService(repo, s.resolve), repo: repo, content: s.content, resolve: s.resolve, places: s.places, mode: options.Mode, since: options.Since, tally: &result}
	for _, principal := range principals {
		if err := b.runFor(ctx, principal); err != nil {
			return result, err
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if result.Failed > 0 {
		return result, fmt.Errorf("GPS backfill completed with %d failure(s)", result.Failed)
	}
	return result, nil
}

type backfiller struct {
	svc     *MediaService
	repo    *media.Repo
	content *content.Adapter
	resolve *contentresolver.Resolver
	places  media.PlaceResolver
	mode    media.GPSBackfillMode
	since   *time.Time
	tally   *GPSResult
}

// runFor pages through the candidate set for owner using keyset
// pagination. Always advance the cursor past the last seen ID. For
// Full and Relabel this is required because rows stay in the candidate
// set after being processed; for FillMissing it's required because
// rows that fail to gain GPS (no EXIF segment, IO error) also stay
// candidates and would otherwise loop the fetched page indefinitely.
func (b *backfiller) runFor(ctx context.Context, owner owners.Principal) error {
	afterID := ""
	for {
		page, err := b.repo.ListGPSBackfillCandidates(
			ctx, owner, b.mode, b.since, afterID, 500)
		if err != nil {
			return fmt.Errorf("list gps candidates: %w", err)
		}
		if len(page) == 0 {
			return nil
		}
		for _, row := range page {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := backfillOne(ctx, b, owner, row); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				// Per-row failures are tallied but don't abort the run.
				b.tally.Failures = append(b.tally.Failures, GPSFailure{Owner: owner, ID: row.ID, Error: err.Error()})
				b.tally.Failed++
			}
			b.tally.Processed++
		}
		afterID = page[len(page)-1].ID
		if len(page) < 500 {
			return nil
		}
	}
}

// backfillOne dispatches to the validated mode-specific helper.
func backfillOne(
	ctx context.Context,
	b *backfiller,
	owner owners.Principal,
	row media.Media,
) error {
	switch b.mode {
	case media.GPSBackfillModeRelabel:
		return relabelOne(ctx, b, owner, row)
	case media.GPSBackfillModeFull, media.GPSBackfillModeFillMissing:
		return reextractOne(ctx, b, owner, row)
	default:
		return fmt.Errorf("%w: gps backfill mode %d", errs.ErrInvalidArgument, b.mode)
	}
}

// relabelOne re-resolves the gazetteer label for a row that already has
// coords, without touching EXIF. Rows missing either coordinate are
// counted as unchanged: the relabel candidate query already filters
// them out, but the guard keeps this helper safe in isolation.
func relabelOne(ctx context.Context, b *backfiller, owner owners.Principal, row media.Media) error {
	if row.Latitude == nil || row.Longitude == nil {
		b.tally.Unchanged++
		return nil
	}
	label := ""
	if l, ok := b.places.Resolve(*row.Latitude, *row.Longitude); ok {
		label = l
	}
	if label == row.LocationLabel {
		b.tally.Unchanged++
		return nil
	}
	if err := b.svc.UpdateGPS(
		ctx, owner, row.ID, row.CurrentVersionID,
		row.Latitude, row.Longitude, row.GPSAt, label,
	); err != nil {
		return fmt.Errorf("update gps for row %s: %w", row.ID, err)
	}
	b.tally.Updated++
	return nil
}

// reextractOne handles the Full and FillMissing modes: ensure metadata for the
// exact Docbank version and reconcile its GPS projection with the row. Full is
// authoritative when the source metadata has no GPS; FillMissing leaves the
// row alone in that case.
func reextractOne(
	ctx context.Context,
	b *backfiller,
	owner owners.Principal,
	row media.Media,
) error {
	ref, err := b.resolve.ValidateCurrent(ctx, row.ID, row.PrimaryFileID)
	if err != nil {
		return fmt.Errorf("validate current Docbank content: %w", err)
	}
	metadata, err := b.content.EnsureSourceMetadata(ctx, ref.VersionID)
	if err != nil {
		return fmt.Errorf("ensure Docbank source metadata: %w", err)
	}
	projection, err := media.ProjectSourceMetadata(metadata, b.places)
	if err != nil {
		return fmt.Errorf("project Docbank source metadata: %w", err)
	}
	if projection.Latitude == nil || projection.Longitude == nil {
		return reextractMissing(ctx, b, owner, row)
	}
	if err := b.svc.UpdateGPS(
		ctx, owner, row.ID, ref.VersionID,
		projection.Latitude, projection.Longitude,
		projection.GPSAt, projection.LocationLabel,
	); err != nil {
		return fmt.Errorf("update gps for row %s: %w", row.ID, err)
	}
	b.tally.Updated++
	return nil
}

// reextractMissing is the EXIF-has-no-GPS branch of reextractOne. Full
// clears any existing coords; FillMissing leaves the row alone. Pulled
// out to keep reextractOne under the cyclomatic limit.
func reextractMissing(
	ctx context.Context,
	b *backfiller,
	owner owners.Principal,
	row media.Media,
) error {
	if b.mode == media.GPSBackfillModeFillMissing {
		b.tally.Unchanged++
		return nil
	}
	// Full is authoritative: clear if EXIF has no GPS.
	if row.Latitude == nil && row.Longitude == nil &&
		row.GPSAt == nil && row.LocationLabel == "" {
		b.tally.Unchanged++
		return nil
	}
	if err := b.svc.UpdateGPS(ctx, owner, row.ID, row.CurrentVersionID, nil, nil, nil, ""); err != nil {
		return fmt.Errorf("update gps for row %s: %w", row.ID, err)
	}
	b.tally.Updated++
	return nil
}
