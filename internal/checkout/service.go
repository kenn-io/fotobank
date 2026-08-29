package checkout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"go.kenn.io/fotobank/internal/contentresolver"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
)

// Materializer owns checkout selection and filesystem publication. Transport
// callers reach it through internal/service, which remains the authorization
// boundary.
type Materializer struct {
	repo     *Repo
	resolver *contentresolver.Resolver
	now      func() time.Time
	newID    func() string
}

func NewMaterializer(repo *Repo, resolver *contentresolver.Resolver) *Materializer {
	return &Materializer{repo: repo, resolver: resolver, now: time.Now, newID: uuid.NewString}
}

type CreateRequest struct {
	Root          string
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

func (s *Materializer) Create(
	ctx context.Context,
	caller owners.Principal,
	request CreateRequest,
) (CreateResult, error) {
	if s == nil || s.repo == nil || s.resolver == nil {
		return CreateResult{}, fmt.Errorf("create checkout: %w: service is not configured", errs.ErrInvalidArgument)
	}
	if !filepath.IsAbs(request.Root) || filepath.Clean(request.Root) != request.Root {
		return CreateResult{}, fmt.Errorf("create checkout: %w: root must be a canonical absolute path", errs.ErrInvalidArgument)
	}
	if request.CapacityLimit < 0 {
		return CreateResult{}, fmt.Errorf("create checkout: %w: capacity limit cannot be negative", errs.ErrInvalidArgument)
	}
	if request.Selection.All && request.CapacityLimit == 0 {
		return CreateResult{}, fmt.Errorf("create checkout: %w: all-assets checkout requires --max-bytes", errs.ErrInvalidArgument)
	}
	request.Selection = normalizeSelection(request.Selection)
	contents, err := os.ReadDir(request.Root)
	if err != nil {
		return CreateResult{}, fmt.Errorf("create checkout: read root: %w", err)
	}
	if len(contents) != 0 {
		return CreateResult{}, fmt.Errorf("create checkout: %w: root must be empty", errs.ErrAlreadyExists)
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
	now := s.now().UTC()
	checkout := Checkout{
		ID: s.newID(), Owner: caller, Root: request.Root, Layout: "capture_date",
		Selection: request.Selection, State: StateBuilding,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repo.Insert(ctx, checkout); err != nil {
		return CreateResult{}, err
	}

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
		if err := s.materialize(ctx, checkout, candidate, relativePath); err != nil {
			return CreateResult{}, s.fail(ctx, checkout.ID, err)
		}
	}
	if err := s.repo.SetState(ctx, checkout.ID, StateActive, "", s.now().UTC()); err != nil {
		return CreateResult{}, err
	}
	checkout.State = StateActive
	checkout.UpdatedAt = s.now().UTC()
	return CreateResult{Checkout: checkout, Estimate: estimate}, nil
}

func (s *Materializer) fail(ctx context.Context, checkoutID string, cause error) error {
	stateErr := s.repo.SetState(ctx, checkoutID, StateError, cause.Error(), s.now().UTC())
	return errors.Join(cause, stateErr)
}

func (s *Materializer) materialize(
	ctx context.Context,
	checkout Checkout,
	candidate Candidate,
	relativePath string,
) error {
	ref, err := s.resolver.ResolveVersion(
		ctx, candidate.AssetID, candidate.FileID, candidate.VersionID)
	if err != nil {
		return fmt.Errorf("materialize %s: resolve version: %w", relativePath, err)
	}
	opened, err := s.resolver.Open(ctx, ref, 0, -1)
	if err != nil {
		return fmt.Errorf("materialize %s: open version: %w", relativePath, err)
	}
	destination := filepath.Join(checkout.Root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return errors.Join(fmt.Errorf("materialize %s: create directory: %w", relativePath, err), opened.Reader.Close())
	}
	if _, err := os.Lstat(destination); err == nil {
		return errors.Join(
			fmt.Errorf("materialize %s: %w: destination already exists", relativePath, errs.ErrAlreadyExists),
			opened.Reader.Close())
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.Join(fmt.Errorf("materialize %s: inspect destination: %w", relativePath, err), opened.Reader.Close())
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), ".fotobank-materialize-*")
	if err != nil {
		return errors.Join(fmt.Errorf("materialize %s: create temporary file: %w", relativePath, err), opened.Reader.Close())
	}
	tempName := temp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tempName)
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
	if err := os.Rename(tempName, destination); err != nil {
		return fmt.Errorf("materialize %s: publish: %w", relativePath, err)
	}
	removeTemp = false
	info, err := os.Stat(destination)
	if err != nil {
		return fmt.Errorf("materialize %s: stat published file: %w", relativePath, err)
	}
	now := s.now().UTC()
	entry := Entry{
		CheckoutID: checkout.ID, FileID: candidate.FileID, RelativePath: relativePath,
		BaseVersionID: candidate.VersionID, BaseSHA256: candidate.SHA256, BaseSize: candidate.Size,
		ObservedSize: info.Size(), ObservedMTime: info.ModTime().UTC(), ObservedSHA256: observedSHA,
		State: EntryClean, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repo.InsertEntry(ctx, entry); err != nil {
		return fmt.Errorf("materialize %s: record entry: %w", relativePath, err)
	}
	return nil
}

func workingPath(candidate Candidate) (string, error) {
	name := candidate.OriginalFilename
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, "/\\\x00") {
		return "", fmt.Errorf("checkout path: %w: invalid original filename", errs.ErrInvalidArgument)
	}
	directory := "undated"
	if candidate.CapturedAt != nil {
		directory = candidate.CapturedAt.Format("2006/01/02")
	}
	return path.Join(directory, candidate.AssetID, name), nil
}

func estimateCandidates(candidates []Candidate) Estimate {
	estimate := Estimate{Files: len(candidates)}
	for _, candidate := range candidates {
		estimate.Bytes += candidate.Size
	}
	return estimate
}
