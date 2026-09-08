package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"

	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/search/index"
)

type Options struct {
	Owner             owners.Principal
	ConcurrentWorkers int
	SettleInterval    time.Duration
	Progress          func(ProgressEvent)
}

type ProgressEvent struct {
	Done       int
	Total      int
	Imported   int
	Duplicates int
	Conflicts  int
	Failures   int
	Path       string
}

type Result struct {
	Imported   int
	Duplicates int
	Conflicts  int
	Failures   []error
}

type Importer struct {
	content         *content.Adapter
	assets          *media.AssetRepo
	repo            *media.Repo
	ownerStorageKey string
	places          media.PlaceResolver
	now             func() time.Time
	ai              AIEnqueuer
}

func NewImporter(contentStore *content.Adapter, assets *media.AssetRepo, repo *media.Repo, ownerStorageKey string, places media.PlaceResolver) *Importer {
	return &Importer{
		content: contentStore, assets: assets, repo: repo,
		ownerStorageKey: ownerStorageKey, places: places,
		now: func() time.Time { return time.Now().UTC() }, ai: NoopAIEnqueuer{},
	}
}

func (imp *Importer) SetAIEnqueuer(e AIEnqueuer) { imp.ai = e }

func (imp *Importer) refreshFTS(ctx context.Context, assetID string) {
	err := imp.repo.WithWriteTx(ctx, func(tx *sql.Tx) error {
		return index.RefreshMediaFTS(ctx, tx, assetID)
	})
	if err != nil {
		slog.Default().Warn("ingest: refresh media_fts failed", "media_id", assetID, "err", err)
	}
}

type candidateGroup struct {
	key        string
	candidates []Candidate
}

type fileObservation struct {
	size    int64
	modTime time.Time
	sha256  string
}

type preparedFile struct {
	candidate       Candidate
	file            media.File
	operationID     string
	operationStatus string
	virtualPath     string
	observation     fileObservation
}

type reservationDisposition int

const (
	reservationNone reservationDisposition = iota
	reservationPending
	reservationDuplicate
	reservationConflict
)

type groupOutcome struct {
	imported  bool
	duplicate bool
	conflict  bool
	err       error
	path      string
}

// ImportDirectory imports stable source-file groups into Docbank. Related
// JPEG/RAW/XMP files become one asset; a video is always its own asset.
func (imp *Importer) ImportDirectory(ctx context.Context, root string, opts Options) (Result, error) {
	if root == "" {
		return Result{}, fmt.Errorf("import root is empty")
	}
	sourceRoot, err := imp.content.ResolveImportRoot(root)
	if err != nil {
		return Result{}, err
	}
	var candidates []Candidate
	if err := Discover(ctx, sourceRoot, func(candidate Candidate) error {
		candidates = append(candidates, candidate)
		return nil
	}); err != nil {
		return Result{}, fmt.Errorf("discover: %w", err)
	}
	groups, groupingFailures := groupCandidates(candidates)
	total := len(groups) + len(groupingFailures)
	if opts.Progress != nil {
		opts.Progress(ProgressEvent{Total: total})
	}
	result := Result{Failures: groupingFailures}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if len(groups) == 0 {
		return result, nil
	}

	interval := opts.SettleInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	jobs := make(chan candidateGroup)
	outcomes := make(chan groupOutcome, len(groups))
	var wg sync.WaitGroup
	for range max(opts.ConcurrentWorkers, 1) {
		wg.Go(func() {
			for group := range jobs {
				if ctx.Err() != nil {
					return
				}
				outcomes <- imp.processGroup(ctx, sourceRoot, group, opts.Owner, interval)
			}
		})
	}
	go func() {
		defer func() {
			close(jobs)
			wg.Wait()
			close(outcomes)
		}()
		for _, group := range groups {
			select {
			case jobs <- group:
			case <-ctx.Done():
				return
			}
		}
	}()

	done := len(groupingFailures)
	for outcome := range outcomes {
		switch {
		case outcome.imported:
			result.Imported++
		case outcome.duplicate:
			result.Duplicates++
		case outcome.conflict:
			result.Conflicts++
		}
		if outcome.err != nil {
			result.Failures = append(result.Failures, outcome.err)
		}
		done++
		if opts.Progress != nil {
			opts.Progress(ProgressEvent{
				Done: done, Total: total, Imported: result.Imported,
				Duplicates: result.Duplicates, Conflicts: result.Conflicts,
				Failures: len(result.Failures), Path: outcome.path,
			})
		}
	}
	return result, ctx.Err()
}

func groupCandidates(candidates []Candidate) ([]candidateGroup, []error) {
	grouped := make(map[string][]Candidate)
	for _, candidate := range candidates {
		key := candidateGroupKey(candidate)
		grouped[key] = append(grouped[key], candidate)
	}
	keys := make([]string, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	groups := make([]candidateGroup, 0, len(keys))
	var failures []error
	for _, key := range keys {
		members := grouped[key]
		if err := validateCandidateGroup(members); err != nil {
			failures = append(failures, fmt.Errorf("group %s: %w", key, err))
			continue
		}
		sort.Slice(members, func(i, j int) bool { return members[i].Path < members[j].Path })
		groups = append(groups, candidateGroup{key: key, candidates: members})
	}
	return groups, failures
}

func candidateGroupKey(candidate Candidate) string {
	if candidate.Kind == CandidateVideo {
		return "video\x00" + norm.NFC.String(candidate.Path)
	}
	dir := norm.NFC.String(filepath.Dir(candidate.Path))
	base := strings.TrimSuffix(filepath.Base(candidate.Path), filepath.Ext(candidate.Path))
	if candidate.Kind == CandidateSidecar {
		if ext := filepath.Ext(base); isPhotoSourceExtension(ext) {
			base = strings.TrimSuffix(base, ext)
		}
	}
	base = strings.ToLower(norm.NFC.String(base))
	return "photo\x00" + dir + "\x00" + base
}

func isPhotoSourceExtension(ext string) bool {
	_, _, kind, ok := classify(strings.ToLower(ext))
	return ok && (kind == CandidateImage || kind == CandidateRAW)
}

func validateCandidateGroup(candidates []Candidate) error {
	counts := map[CandidateKind]int{}
	for _, candidate := range candidates {
		counts[candidate.Kind]++
	}
	if counts[CandidateVideo] > 0 {
		if len(candidates) != 1 {
			return fmt.Errorf("%w: video group must contain one file", errs.ErrInvalidArgument)
		}
		return nil
	}
	if counts[CandidateImage] == 0 && counts[CandidateRAW] == 0 {
		return fmt.Errorf("%w: sidecar has no primary media file", errs.ErrInvalidArgument)
	}
	if counts[CandidateImage] > 1 || counts[CandidateRAW] > 1 || counts[CandidateSidecar] > 1 {
		return fmt.Errorf("%w: ambiguous files with the same source name", errs.ErrInvalidArgument)
	}
	return nil
}

func (imp *Importer) processGroup(ctx context.Context, sourceRoot string, group candidateGroup, owner owners.Principal, settleInterval time.Duration) groupOutcome {
	out := groupOutcome{path: group.candidates[0].Path}
	prepared, asset, relationships, err := imp.prepareGroup(ctx, sourceRoot, group, owner, settleInterval)
	if err != nil {
		out.err = err
		return out
	}

	prepared, reservedAsset, disposition, err := imp.resolveReservations(ctx, prepared, owner)
	if err != nil {
		out.conflict = disposition == reservationConflict
		out.err = err
		return out
	}
	if disposition == reservationDuplicate {
		out.duplicate = true
		return out
	}
	if disposition == reservationConflict {
		out.conflict = true
		out.err = fmt.Errorf("%w: source group conflicts with an existing reservation", errs.ErrContentConflict)
		return out
	}
	if disposition == reservationPending {
		asset = reservedAsset
	}

	if disposition == reservationNone {
		pending := make([]media.PendingContent, len(prepared))
		for i := range prepared {
			pending[i] = media.PendingContent{
				OperationID: prepared[i].operationID, File: prepared[i].file,
				SHA256: prepared[i].observation.sha256, Size: prepared[i].observation.size,
				VirtualPath: prepared[i].virtualPath,
			}
		}
		if reserveErr := imp.assets.ReserveImport(ctx, asset, pending, relationships); reserveErr != nil {
			// Another importer can win the unique content reservation while
			// this transaction waits for SQLite's writer lock. Resolve the
			// committed winner and resume its IDs instead of creating another
			// Docbank path.
			prepared, reservedAsset, disposition, err = imp.resolveReservations(ctx, prepared, owner)
			if err != nil {
				out.conflict = disposition == reservationConflict
				out.err = err
				return out
			}
			switch disposition {
			case reservationPending:
				asset = reservedAsset
			case reservationDuplicate:
				out.duplicate = true
				return out
			case reservationConflict:
				out.conflict = true
				out.err = fmt.Errorf("%w: source group conflicts with an existing reservation", errs.ErrContentConflict)
				return out
			default:
				out.err = reserveErr
				return out
			}
		}
	}

	for _, item := range prepared {
		if item.operationStatus == "applied" {
			continue
		}
		if err := revalidateObservation(item.candidate.Path, item.observation); err != nil {
			conflicted, markErr := imp.assets.MarkContentConflict(ctx, asset.ID, err)
			if markErr != nil {
				out.err = errors.Join(err, markErr)
				return out
			}
			if !conflicted {
				out.duplicate = true
				return out
			}
			out.conflict, out.err = true, err
			return out
		}
		file, err := os.Open(item.candidate.Path)
		if err != nil {
			out.err = fmt.Errorf("open source %s: %w", item.candidate.Path, err)
			return out
		}
		modified := item.observation.modTime
		receipt, createErr := imp.content.Create(ctx, content.CreateRequest{
			VirtualPath: item.virtualPath, MediaType: item.candidate.MimeType,
			Expected: content.Identity{SHA256: item.observation.sha256, Size: item.observation.size},
			Source: content.Source{
				Kind: "filesystem-import", Description: "Fotobank source import",
				Reference: item.file.ImportSourcePath, ModifiedAt: &modified,
			}, Reader: file,
		})
		closeErr := file.Close()
		if createErr != nil {
			if errors.Is(createErr, errs.ErrContentConflict) || errors.Is(createErr, errs.ErrContentIdentityMismatch) {
				conflicted, markErr := imp.assets.MarkContentConflict(ctx, asset.ID, createErr)
				if markErr != nil {
					out.err = errors.Join(createErr, markErr)
					return out
				}
				if !conflicted {
					out.duplicate = true
					return out
				}
				out.conflict = true
			}
			out.err = fmt.Errorf("create Docbank content for %s: %w", item.candidate.Path, createErr)
			return out
		}
		if closeErr != nil {
			out.err = fmt.Errorf("close source %s: %w", item.candidate.Path, closeErr)
			return out
		}
		if err := imp.assets.ApplyContentReceipt(ctx, media.ContentReceipt{
			OperationID: item.operationID, NodeID: receipt.Node.ID,
			VersionID: receipt.Version.ID, SHA256: receipt.Identity.SHA256,
			Size: receipt.Identity.Size,
		}); err != nil {
			out.err = err
			return out
		}
	}
	if err := imp.projectAssetMetadata(ctx, asset.ID); err != nil {
		out.err = err
		return out
	}
	if err := imp.assets.FinalizeReady(ctx, asset.ID); err != nil {
		out.err = err
		return out
	}
	imp.refreshFTS(ctx, asset.ID)
	if asset.Type == media.TypePhoto {
		if err := imp.ai.EnqueueForPhoto(ctx, asset.ID); err != nil {
			slog.Default().Warn("ai enqueue for photo failed", "media_id", asset.ID, "err", err)
		}
	} else if err := imp.ai.RecordVideoSkip(ctx, asset.ID); err != nil {
		slog.Default().Warn("ai video skip record failed", "media_id", asset.ID, "err", err)
	}
	out.imported = true
	return out
}

func (imp *Importer) resolveReservations(
	ctx context.Context,
	prepared []preparedFile,
	owner owners.Principal,
) ([]preparedFile, media.Asset, reservationDisposition, error) {
	reservations := make([]media.ContentReservation, len(prepared))
	seenDigests := make(map[string]struct{}, len(prepared))
	found := 0
	for i := range prepared {
		digest := prepared[i].observation.sha256
		if _, exists := seenDigests[digest]; exists {
			return prepared, media.Asset{}, reservationConflict,
				fmt.Errorf("%w: source group contains duplicate file content", errs.ErrContentConflict)
		}
		seenDigests[digest] = struct{}{}
		reservation, err := imp.assets.FindContentReservation(ctx, owner, digest)
		if errors.Is(err, errs.ErrNotFound) {
			continue
		}
		if err != nil {
			return prepared, media.Asset{}, reservationNone, err
		}
		reservations[i] = reservation
		found++
	}
	if found == 0 {
		return prepared, media.Asset{}, reservationNone, nil
	}
	if found != len(prepared) {
		return prepared, media.Asset{}, reservationConflict,
			fmt.Errorf("%w: only part of source group was already reserved", errs.ErrContentConflict)
	}

	assetID := reservations[0].File.AssetID
	for _, reservation := range reservations {
		if reservation.File.AssetID != assetID || reservation.Status == "conflict" {
			return prepared, media.Asset{}, reservationConflict,
				fmt.Errorf("%w: source group belongs to incompatible reservations", errs.ErrContentConflict)
		}
	}
	asset, err := imp.assets.GetAsset(ctx, assetID)
	if err != nil {
		return prepared, media.Asset{}, reservationNone, err
	}
	switch asset.State {
	case media.AssetReady:
		return prepared, asset, reservationDuplicate, nil
	case media.AssetConflict:
		return prepared, asset, reservationConflict, nil
	case media.AssetPending:
		for i, reservation := range reservations {
			if reservation.Status != "pending" && reservation.Status != "applied" {
				return prepared, asset, reservationConflict,
					fmt.Errorf("%w: import operation is terminal", errs.ErrContentConflict)
			}
			prepared[i].file = reservation.File
			prepared[i].operationID = reservation.OperationID
			prepared[i].operationStatus = reservation.Status
			prepared[i].virtualPath = reservation.VirtualPath
		}
		return prepared, asset, reservationPending, nil
	default:
		return prepared, asset, reservationConflict,
			fmt.Errorf("%w: invalid reserved asset state", errs.ErrContentConflict)
	}
}

func (imp *Importer) prepareGroup(ctx context.Context, sourceRoot string, group candidateGroup, owner owners.Principal, settleInterval time.Duration) ([]preparedFile, media.Asset, []media.FileRelationship, error) {
	assetID := uuid.NewString()
	primaryIndex := 0
	for i, candidate := range group.candidates {
		if candidate.Kind == CandidateImage ||
			(candidate.Kind == CandidateRAW && group.candidates[primaryIndex].Kind != CandidateImage) ||
			candidate.Kind == CandidateVideo {
			primaryIndex = i
		}
	}
	primary := group.candidates[primaryIndex]
	observations := make([]fileObservation, len(group.candidates))
	for i, candidate := range group.candidates {
		observation, err := settleCandidate(ctx, candidate.Path, settleInterval)
		if err != nil {
			return nil, media.Asset{}, nil, err
		}
		observations[i] = observation
	}
	asset := media.Asset{
		ID: assetID, Owner: owner, State: media.AssetPending, Type: primary.Type,
		ImportedAt: imp.now(), ThumbStatus: "pending",
	}
	prepared := make([]preparedFile, len(group.candidates))
	fileIDs := make(map[CandidateKind]string)
	for i, candidate := range group.candidates {
		observation := observations[i]
		fileID := uuid.NewString()
		role := media.RoleAlternate
		switch {
		case i == primaryIndex:
			role = media.RolePrimary
		case candidate.Kind == CandidateRAW:
			role = media.RoleOriginal
		case candidate.Kind == CandidateSidecar:
			role = media.RoleSidecar
		}
		rel, err := filepath.Rel(sourceRoot, candidate.Path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, media.Asset{}, nil, fmt.Errorf("source path escaped import root: %s", candidate.Path)
		}
		virtualPath, err := content.VirtualPath(imp.ownerStorageKey, fileID, filepath.Base(candidate.Path))
		if err != nil {
			return nil, media.Asset{}, nil, err
		}
		prepared[i] = preparedFile{
			candidate: candidate, operationID: uuid.NewString(), virtualPath: virtualPath,
			observation: observation,
			file: media.File{
				ID: fileID, AssetID: assetID, Owner: owner, Role: role,
				MimeType: candidate.MimeType, OriginalFilename: filepath.Base(candidate.Path),
				ImportSourcePath: filepath.ToSlash(rel), Size: observation.size,
			},
		}
		fileIDs[candidate.Kind] = fileID
	}
	var relationships []media.FileRelationship
	if rawID, ok := fileIDs[CandidateRAW]; ok {
		if imageID, paired := fileIDs[CandidateImage]; paired {
			relationships = append(relationships, media.FileRelationship{
				SourceFileID: rawID, TargetFileID: imageID, Kind: media.PairedWith,
			})
		}
	}
	if sidecarID, ok := fileIDs[CandidateSidecar]; ok {
		targetID := prepared[primaryIndex].file.ID
		if rawID, hasRAW := fileIDs[CandidateRAW]; hasRAW {
			targetID = rawID
		}
		relationships = append(relationships, media.FileRelationship{
			SourceFileID: sidecarID, TargetFileID: targetID, Kind: media.SidecarOf,
		})
	}
	return prepared, asset, relationships, nil
}

func settleCandidate(ctx context.Context, path string, interval time.Duration) (fileObservation, error) {
	first, err := os.Stat(path)
	if err != nil {
		return fileObservation{}, fmt.Errorf("observe source %s: %w", path, err)
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fileObservation{}, ctx.Err()
	case <-timer.C:
	}
	second, err := os.Stat(path)
	if err != nil {
		return fileObservation{}, fmt.Errorf("observe source %s again: %w", path, err)
	}
	if first.Size() != second.Size() || !first.ModTime().Equal(second.ModTime()) {
		return fileObservation{}, fmt.Errorf("%w: source file is still changing: %s", errs.ErrContentConflict, path)
	}
	digest, err := SHA256(ctx, path)
	if err != nil {
		return fileObservation{}, fmt.Errorf("hash source %s: %w", path, err)
	}
	return fileObservation{size: second.Size(), modTime: second.ModTime(), sha256: digest}, nil
}

func revalidateObservation(path string, expected fileObservation) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("revalidate source %s: %w", path, err)
	}
	if info.Size() != expected.size || !info.ModTime().Equal(expected.modTime) {
		return fmt.Errorf("%w: source changed after reservation: %s", errs.ErrContentIdentityMismatch, path)
	}
	return nil
}

func (imp *Importer) projectAssetMetadata(ctx context.Context, assetID string) error {
	primary, err := imp.assets.GetPrimaryFile(ctx, assetID)
	if err != nil {
		return fmt.Errorf("read primary file for source metadata: %w", err)
	}
	if primary.CurrentVersionID == "" {
		return fmt.Errorf("%w: primary file has no Docbank version", errs.ErrContentUnavailable)
	}
	metadata, err := imp.content.EnsureSourceMetadata(ctx, primary.CurrentVersionID)
	if err != nil {
		return fmt.Errorf("ensure Docbank source metadata: %w", err)
	}
	projection, err := media.ProjectSourceMetadata(metadata, imp.places)
	if err != nil {
		return fmt.Errorf("project Docbank source metadata: %w", err)
	}
	if err := imp.assets.ApplySourceMetadata(ctx, assetID, projection); err != nil {
		return err
	}
	return nil
}
