package checkout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofrs/flock"
	"github.com/google/uuid"

	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/contentresolver"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
)

// Materializer owns checkout selection and filesystem publication. Transport
// callers reach it through internal/service, which remains the authorization
// boundary.
type Materializer struct {
	repo             *Repo
	resolver         *contentresolver.Resolver
	creationLockPath string
	now              func() time.Time
	newID            func() string
}

func NewMaterializer(
	repo *Repo,
	resolver *contentresolver.Resolver,
	creationLockPath string,
) *Materializer {
	return &Materializer{
		repo: repo, resolver: resolver,
		creationLockPath: creationLockPath,
		now:              time.Now, newID: uuid.NewString,
	}
}

type CreateRequest struct {
	// Root is consumed and closed by Create, including when request validation fails.
	Root          *content.CheckoutRoot
	Selection     Selection
	CapacityLimit int64
}

type CreateResult struct {
	Checkout Checkout
	Estimate Estimate
}

func (s *Materializer) Estimate(
	ctx context.Context,
	caller owners.Principal,
	selection Selection,
) (Estimate, error) {
	if s == nil || s.repo == nil {
		return Estimate{}, fmt.Errorf("estimate checkout: %w: service is not configured", errs.ErrInvalidArgument)
	}
	selection = normalizeSelection(selection)
	candidates, err := s.repo.ResolveSelection(ctx, caller, selection)
	if err != nil {
		return Estimate{}, err
	}
	return estimateCandidates(candidates), nil
}

func (s *Materializer) recoverInterrupted(
	ctx context.Context,
	caller owners.Principal,
) (int64, error) {
	return s.repo.markBuildingInterrupted(
		ctx, caller, "checkout creation was interrupted before completion", s.now().UTC())
}

func (s *Materializer) Create(
	ctx context.Context,
	caller owners.Principal,
	request CreateRequest,
) (CreateResult, error) {
	if s == nil || s.repo == nil || s.resolver == nil {
		return CreateResult{}, fmt.Errorf("create checkout: %w: service is not configured", errs.ErrInvalidArgument)
	}
	defer request.Root.Close()
	if s.creationLockPath == "" {
		return CreateResult{}, fmt.Errorf("create checkout: %w: creation lock is not configured", errs.ErrInvalidArgument)
	}
	creationLock := flock.New(s.creationLockPath)
	locked, err := creationLock.TryLock()
	if err != nil {
		return CreateResult{}, fmt.Errorf("create checkout: lock creation: %w", err)
	}
	if !locked {
		return CreateResult{}, fmt.Errorf("create checkout: %w: another checkout creation is in progress", errs.ErrAlreadyExists)
	}
	defer func() { _ = creationLock.Unlock() }()
	if _, err := s.recoverInterrupted(ctx, caller); err != nil {
		return CreateResult{}, err
	}
	if request.CapacityLimit < 0 {
		return CreateResult{}, fmt.Errorf("create checkout: %w: capacity limit cannot be negative", errs.ErrInvalidArgument)
	}
	if request.Selection.All && request.CapacityLimit == 0 {
		return CreateResult{}, fmt.Errorf("create checkout: %w: all-assets checkout requires --max-bytes", errs.ErrInvalidArgument)
	}
	request.Selection = normalizeSelection(request.Selection)
	rootPath := request.Root.Path()
	liveRoots, err := s.repo.LiveRoots(ctx)
	if err != nil {
		return CreateResult{}, err
	}
	if slices.ContainsFunc(liveRoots, request.Root.Overlaps) {
		return CreateResult{}, fmt.Errorf(
			"create checkout: %w: root overlaps live checkout", errs.ErrAlreadyExists)
	}
	candidates, err := s.repo.ResolveSelection(ctx, caller, request.Selection)
	if err != nil {
		return CreateResult{}, err
	}
	estimate := estimateCandidates(candidates)
	if request.CapacityLimit > 0 && estimate.Bytes > request.CapacityLimit {
		return CreateResult{}, fmt.Errorf(
			"create checkout: %w: selection needs %d bytes, limit is %d",
			errs.ErrInvalidArgument, estimate.Bytes, request.CapacityLimit)
	}
	workingRoot, err := request.Root.Take()
	if err != nil {
		return CreateResult{}, fmt.Errorf("create checkout: open root: %w", err)
	}
	defer workingRoot.Close()
	contents, err := fs.ReadDir(workingRoot.FS(), ".")
	if err != nil {
		return CreateResult{}, fmt.Errorf("create checkout: read root: %w", err)
	}
	if len(contents) != 0 {
		return CreateResult{}, fmt.Errorf("create checkout: %w: root must be empty", errs.ErrAlreadyExists)
	}
	now := s.now().UTC()
	checkout := Checkout{
		ID: s.newID(), Owner: caller, Root: rootPath, Layout: "capture_date",
		Selection: request.Selection, State: StateBuilding,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repo.Insert(ctx, checkout); err != nil {
		return CreateResult{}, err
	}
	const stagingDirectory = ".fotobank-staging"
	if err := workingRoot.Mkdir(stagingDirectory, 0o700); err != nil {
		return CreateResult{}, s.fail(ctx, checkout.ID,
			fmt.Errorf("create checkout: create staging directory: %w", err))
	}
	removeStaging := true
	defer func() {
		if removeStaging {
			_ = workingRoot.Remove(stagingDirectory)
		}
	}()

	paths := make(map[string]string, len(candidates))
	for _, candidate := range candidates {
		relativePath, err := workingPath(candidate)
		if err != nil {
			return CreateResult{}, s.fail(ctx, checkout.ID, err)
		}
		if existing, ok := paths[relativePath]; ok {
			err := fmt.Errorf("create checkout: %w: files %s and %s map to %s",
				errs.ErrContentConflict, existing, candidate.FileID, relativePath)
			return CreateResult{}, s.fail(ctx, checkout.ID, err)
		}
		paths[relativePath] = candidate.FileID
		if err := s.materialize(
			ctx, request.Root, workingRoot, checkout, candidate, relativePath, stagingDirectory,
		); err != nil {
			return CreateResult{}, s.fail(ctx, checkout.ID, err)
		}
	}
	if err := workingRoot.Remove(stagingDirectory); err != nil {
		return CreateResult{}, s.fail(ctx, checkout.ID,
			fmt.Errorf("activate checkout: remove staging directory: %w", err))
	}
	removeStaging = false
	if err := syncCheckoutDirectories(workingRoot, "."); err != nil {
		return CreateResult{}, s.fail(ctx, checkout.ID,
			fmt.Errorf("activate checkout: sync staging removal: %w", err))
	}
	if err := request.Root.Revalidate(); err != nil {
		return CreateResult{}, s.fail(ctx, checkout.ID,
			fmt.Errorf("activate checkout: root changed during materialization: %w", err))
	}
	if err := s.repo.SetState(ctx, checkout.ID, StateActive, "", s.now().UTC()); err != nil {
		return CreateResult{}, s.fail(ctx, checkout.ID, fmt.Errorf("activate checkout: %w", err))
	}
	checkout.State = StateActive
	checkout.UpdatedAt = s.now().UTC()
	return CreateResult{Checkout: checkout, Estimate: estimate}, nil
}

func (s *Materializer) fail(ctx context.Context, checkoutID string, cause error) error {
	stateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	stateErr := s.repo.SetState(stateCtx, checkoutID, StateError, cause.Error(), s.now().UTC())
	return errors.Join(cause, stateErr)
}

func (s *Materializer) materialize(
	ctx context.Context,
	checkoutRoot *content.CheckoutRoot,
	workingRoot *os.Root,
	checkout Checkout,
	candidate Candidate,
	relativePath string,
	stagingDirectory string,
) error {
	ref, err := s.resolver.ResolveVersion(
		ctx, candidate.AssetID, candidate.FileID, candidate.VersionID)
	if err != nil {
		return fmt.Errorf("materialize %s: resolve version: %w", relativePath, err)
	}
	if ref.Asset.Owner != checkout.Owner || ref.Asset.HiddenAt != nil {
		return fmt.Errorf("materialize %s: %w: asset is no longer available",
			relativePath, errs.ErrNotFound)
	}
	opened, err := s.resolver.Open(ctx, ref, 0, -1)
	if err != nil {
		return fmt.Errorf("materialize %s: open version: %w", relativePath, err)
	}
	destination := filepath.FromSlash(relativePath)
	directory := filepath.Dir(destination)
	if err := workingRoot.MkdirAll(directory, 0o700); err != nil {
		return errors.Join(fmt.Errorf("materialize %s: create directory: %w", relativePath, err), opened.Reader.Close())
	}
	tempName := filepath.Join(stagingDirectory, candidate.FileID+".tmp")
	temp, err := workingRoot.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.Join(fmt.Errorf("materialize %s: create temporary file: %w", relativePath, err), opened.Reader.Close())
	}
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = workingRoot.Remove(tempName)
		}
	}()

	digest := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(temp, digest), opened.Reader)
	readerCloseErr := opened.Reader.Close()
	syncErr := temp.Sync()
	closeErr := temp.Close()
	if err := errors.Join(copyErr, readerCloseErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("materialize %s: copy exact version: %w", relativePath, err)
	}
	observedSHA := hex.EncodeToString(digest.Sum(nil))
	if observedSHA != candidate.SHA256 {
		return fmt.Errorf("materialize %s: %w: copied digest differs from catalog",
			relativePath, errs.ErrContentIdentityMismatch)
	}
	published, err := publish(workingRoot, tempName, destination)
	if err != nil {
		return fmt.Errorf("materialize %s: %w", relativePath, err)
	}
	removeTemp = false
	if err := syncCheckoutDirectories(workingRoot, directory); err != nil {
		return errors.Join(
			fmt.Errorf("materialize %s: sync published path: %w", relativePath, err),
			published.Close())
	}
	digest.Reset()
	if _, err := io.Copy(digest, published); err != nil {
		return errors.Join(
			fmt.Errorf("materialize %s: hash published file: %w", relativePath, err),
			published.Close())
	}
	info, err := published.Stat()
	if err != nil {
		return errors.Join(
			fmt.Errorf("materialize %s: stat published file: %w", relativePath, err),
			published.Close())
	}
	pathInfo, err := workingRoot.Stat(destination)
	if err != nil {
		return errors.Join(
			fmt.Errorf("materialize %s: stat final checkout path: %w", relativePath, err),
			published.Close())
	}
	if !os.SameFile(info, pathInfo) {
		return errors.Join(
			fmt.Errorf("materialize %s: %w: published file was replaced during observation",
				relativePath, errs.ErrContentConflict),
			published.Close())
	}
	observedIdentity, err := filesystemIdentity(published)
	if err != nil {
		return errors.Join(
			fmt.Errorf("materialize %s: identify published file: %w", relativePath, err),
			published.Close())
	}
	if err := published.Close(); err != nil {
		return fmt.Errorf("materialize %s: close published file: %w", relativePath, err)
	}
	observedSHA = hex.EncodeToString(digest.Sum(nil))
	if observedSHA != candidate.SHA256 {
		return fmt.Errorf("materialize %s: %w: published file differs from exact version",
			relativePath, errs.ErrContentConflict)
	}
	now := s.now().UTC()
	entry := Entry{
		CheckoutID: checkout.ID, FileID: candidate.FileID, RelativePath: relativePath,
		BaseVersionID: candidate.VersionID, BaseSHA256: candidate.SHA256, BaseSize: candidate.Size,
		ObservedSize: info.Size(), ObservedMTime: info.ModTime().UTC(),
		ObservedIdentity: observedIdentity, ObservedSHA256: observedSHA,
		State: EntryClean, CreatedAt: now, UpdatedAt: now,
	}
	if err := checkoutRoot.Revalidate(); err != nil {
		return fmt.Errorf("materialize %s: checkout root changed before recording: %w",
			relativePath, err)
	}
	if err := s.repo.InsertEntry(ctx, entry); err != nil {
		return fmt.Errorf("materialize %s: record entry: %w", relativePath, err)
	}
	return nil
}

// publish atomically gives a completed temporary copy its final name without
// replacing an untracked working file. Both names are inside the root-bound
// filesystem view; the temporary hardlink is removed after publication and is
// never linked to Docbank content-addressed storage. The returned handle was
// opened from the final root-bound path so the caller can derive one pinned
// initial observation even if another process later replaces the path.
func publish(workingRoot *os.Root, tempName, destination string) (*os.File, error) {
	if err := workingRoot.Link(tempName, destination); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("%w: destination already exists", errs.ErrAlreadyExists)
		}
		return nil, fmt.Errorf("publish checkout file: %w", err)
	}
	if err := workingRoot.Remove(tempName); err != nil {
		return nil, fmt.Errorf("remove published temporary link: %w", err)
	}
	published, err := workingRoot.Open(destination)
	if err != nil {
		return nil, fmt.Errorf("open published checkout file: %w", err)
	}
	return published, nil
}

func workingPath(candidate Candidate) (string, error) {
	name := candidate.OriginalFilename
	if err := validateCheckoutFilename(name); err != nil {
		return "", fmt.Errorf("checkout path: %w", err)
	}
	directory := "undated"
	if candidate.CapturedAt != nil {
		directory = candidate.CapturedAt.Format("2006/01/02")
	}
	return path.Join(directory, candidate.AssetID, name), nil
}

func validateCheckoutFilename(name string) error {
	switch {
	case !utf8.ValidString(name), name == "", name == ".", name == "..":
		return fmt.Errorf("%w: invalid original filename", errs.ErrInvalidArgument)
	case strings.ContainsAny(name, `<>:"/\|?*`+"\x00"):
		return fmt.Errorf("%w: original filename is not portable to Windows", errs.ErrInvalidArgument)
	case strings.HasSuffix(name, ".") || strings.HasSuffix(name, " "):
		return fmt.Errorf("%w: original filename ends in a dot or space", errs.ErrInvalidArgument)
	case isWindowsDeviceName(name):
		return fmt.Errorf("%w: original filename uses a reserved Windows device name", errs.ErrInvalidArgument)
	}
	for _, r := range name {
		if r >= 1 && r <= 31 {
			return fmt.Errorf("%w: original filename contains a control character", errs.ErrInvalidArgument)
		}
	}
	return nil
}

func isWindowsDeviceName(name string) bool {
	base, _, _ := strings.Cut(name, ".")
	base = strings.TrimRight(base, " ")
	upper := strings.ToUpper(base)
	switch upper {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	if !strings.HasPrefix(upper, "COM") && !strings.HasPrefix(upper, "LPT") {
		return false
	}
	switch upper[3:] {
	case "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
		return true
	default:
		return false
	}
}

func estimateCandidates(candidates []Candidate) Estimate {
	estimate := Estimate{Files: len(candidates)}
	for _, candidate := range candidates {
		estimate.Bytes += candidate.Size
	}
	return estimate
}
