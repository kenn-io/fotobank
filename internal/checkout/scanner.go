package checkout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"strings"
	"time"

	"go.kenn.io/fotobank/internal/content"
)

const stagingDirectory = ".fotobank-staging"

var errScanObservationChanged = errors.New("checkout file changed during observation")

type ScannerConfig struct {
	ScanInterval   time.Duration
	SettleInterval time.Duration
	IgnorePatterns []string
	Logger         *slog.Logger
}

type ScanResult struct {
	Checkouts int
	Files     int
	Clean     int
	Pending   int
	Missing   int
	Untracked int
}

// Scanner periodically reconstructs checkout changes from the working tree.
// Filesystem notifications may ask it to run sooner, but the catalog and a
// complete tree walk are the correctness boundary.
type Scanner struct {
	repo    *Repo
	content *content.Adapter
	config  ScannerConfig
	now     func() time.Time
}

func NewScanner(repo *Repo, contentStore *content.Adapter, config ScannerConfig) *Scanner {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &Scanner{
		repo: repo, content: contentStore, config: config,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (s *Scanner) Run(ctx context.Context) error {
	if err := s.validate(); err != nil {
		return err
	}
	s.scanAndLog(ctx)
	ticker := time.NewTicker(s.config.ScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			s.scanAndLog(ctx)
		}
	}
}

func (s *Scanner) scanAndLog(ctx context.Context) {
	result, err := s.Scan(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		s.config.Logger.Error("checkout scan failed", "err", err)
		return
	}
	if result.Pending > 0 || result.Missing > 0 || result.Untracked > 0 {
		s.config.Logger.Info("checkout scan completed",
			"checkouts", result.Checkouts,
			"files", result.Files,
			"pending", result.Pending,
			"missing", result.Missing,
			"untracked", result.Untracked)
	}
}

func (s *Scanner) Scan(ctx context.Context) (ScanResult, error) {
	if err := s.validate(); err != nil {
		return ScanResult{}, err
	}
	checkouts, err := s.repo.ListActive(ctx)
	if err != nil {
		return ScanResult{}, err
	}
	result := ScanResult{Checkouts: len(checkouts)}
	var scanErrors []error
	for _, checkout := range checkouts {
		checkoutResult, err := s.scanCheckout(ctx, checkout)
		result.Files += checkoutResult.Files
		result.Clean += checkoutResult.Clean
		result.Pending += checkoutResult.Pending
		result.Missing += checkoutResult.Missing
		result.Untracked += checkoutResult.Untracked
		if err != nil {
			scanErrors = append(scanErrors, fmt.Errorf("scan checkout %s: %w", checkout.ID, err))
		}
	}
	return result, errors.Join(scanErrors...)
}

func (s *Scanner) validate() error {
	switch {
	case s == nil || s.repo == nil || s.content == nil:
		return errors.New("checkout scanner is not configured")
	case s.config.ScanInterval <= 0:
		return errors.New("checkout scan interval must be positive")
	case s.config.SettleInterval < 0:
		return errors.New("checkout settle interval cannot be negative")
	}
	for _, pattern := range s.config.IgnorePatterns {
		if _, err := path.Match(pattern, "candidate"); err != nil {
			return fmt.Errorf("invalid checkout ignore pattern %q: %w", pattern, err)
		}
	}
	return nil
}

func (s *Scanner) scanCheckout(ctx context.Context, checkout Checkout) (ScanResult, error) {
	validatedRoot, err := s.content.ResolveCheckoutRoot(checkout.Root)
	if err != nil {
		return ScanResult{}, err
	}
	defer validatedRoot.Close()
	root, err := validatedRoot.Take()
	if err != nil {
		return ScanResult{}, err
	}
	defer root.Close()

	entries, err := s.repo.ListEntries(ctx, checkout.ID)
	if err != nil {
		return ScanResult{}, err
	}
	candidates, err := s.repo.ListScanCandidates(ctx, checkout.ID)
	if err != nil {
		return ScanResult{}, err
	}
	entriesByPath := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		entriesByPath[entry.RelativePath] = entry
	}
	candidatesByPath := make(map[string]ScanCandidate, len(candidates))
	for _, candidate := range candidates {
		candidatesByPath[candidate.RelativePath] = candidate
	}
	seen := make(map[string]struct{}, len(entries)+len(candidates))
	result := ScanResult{}
	err = fs.WalkDir(root.FS(), ".", func(relativePath string, dirEntry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if relativePath == "." {
			return nil
		}
		if relativePath == stagingDirectory {
			if dirEntry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		relativePath = path.Clean(strings.TrimPrefix(relativePath, "./"))
		entry, tracked := entriesByPath[relativePath]
		if dirEntry.IsDir() {
			if !tracked {
				return nil
			}
			seen[relativePath] = struct{}{}
			result.Files++
			if entry.State != EntryConflict {
				if err := s.markTrackedError(ctx, validatedRoot, checkout.ID, entry.FileID,
					errors.New("checkout path is not a regular file")); err != nil {
					return err
				}
			}
			return fs.SkipDir
		}
		if !tracked && s.ignored(relativePath) {
			return nil
		}
		seen[relativePath] = struct{}{}
		result.Files++
		if tracked && entry.State == EntryConflict {
			return nil
		}
		info, err := root.Lstat(relativePath)
		if err != nil {
			observationErr := scanObservationError("inspect checkout file", err)
			if errors.Is(observationErr, errScanObservationChanged) {
				return nil
			}
			return observationErr
		}
		if !info.Mode().IsRegular() {
			if tracked {
				return s.markTrackedError(ctx, validatedRoot, checkout.ID, entry.FileID,
					errors.New("checkout path is not a regular file"))
			}
			return nil
		}
		observedIdentity, err := observeFileIdentity(root, relativePath, info)
		if errors.Is(err, errScanObservationChanged) {
			return nil
		}
		if err != nil {
			if tracked {
				return s.markTrackedError(
					ctx, validatedRoot, checkout.ID, entry.FileID, err)
			}
			return err
		}
		if tracked && entry.State != EntryMissing && entry.State != EntryError &&
			entry.ObservedSize == info.Size() && entry.ObservedMTime.Equal(info.ModTime()) &&
			entry.ObservedIdentity != "" && observedIdentity != "" &&
			entry.ObservedIdentity == observedIdentity {
			if _, exists := candidatesByPath[relativePath]; exists {
				if err := validatedRoot.Revalidate(); err != nil {
					return err
				}
				if err := s.repo.DeleteScanCandidate(ctx, checkout.ID, relativePath); err != nil {
					return err
				}
			}
			return nil
		}
		candidate := ScanCandidate{
			CheckoutID: checkout.ID, RelativePath: relativePath,
			ObservedSize: info.Size(), ObservedMTime: info.ModTime().UTC(),
			ObservedIdentity: observedIdentity,
		}
		if tracked {
			candidate.FileID = entry.FileID
		}
		if err := validatedRoot.Revalidate(); err != nil {
			return err
		}
		settled, err := s.repo.ObserveScanCandidate(
			ctx, candidate, s.config.SettleInterval, s.now())
		if err != nil || !settled {
			return err
		}
		sha, err := hashSettledFile(ctx, root, candidate)
		if errors.Is(err, errScanObservationChanged) {
			return nil
		}
		if err != nil {
			if tracked {
				return s.markTrackedError(
					ctx, validatedRoot, checkout.ID, entry.FileID, err)
			}
			return err
		}
		if err := validatedRoot.Revalidate(); err != nil {
			return err
		}
		if tracked {
			state, err := s.repo.FinalizeTrackedScanCandidate(ctx, candidate, sha, s.now())
			if err != nil {
				return err
			}
			if state == EntryClean {
				result.Clean++
			} else {
				result.Pending++
			}
			return nil
		}
		if err := s.repo.FinalizeUntrackedScanCandidate(ctx, candidate, sha, s.now()); err != nil {
			return err
		}
		result.Untracked++
		result.Pending++
		return nil
	})
	if err != nil {
		return result, err
	}

	for _, entry := range entries {
		if _, ok := seen[entry.RelativePath]; ok || entry.State == EntryMissing || entry.State == EntryConflict {
			continue
		}
		if err := validatedRoot.Revalidate(); err != nil {
			return result, err
		}
		if err := s.repo.MarkEntryMissing(ctx, checkout.ID, entry.FileID, s.now()); err != nil {
			return result, err
		}
		result.Missing++
	}
	for _, candidate := range candidates {
		if candidate.FileID != "" {
			continue
		}
		if _, ok := seen[candidate.RelativePath]; ok {
			continue
		}
		if err := validatedRoot.Revalidate(); err != nil {
			return result, err
		}
		if err := s.repo.DeleteScanCandidate(ctx, checkout.ID, candidate.RelativePath); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (s *Scanner) markTrackedError(
	ctx context.Context,
	root *content.CheckoutRoot,
	checkoutID string,
	fileID string,
	cause error,
) error {
	if err := root.Revalidate(); err != nil {
		return errors.Join(cause, err)
	}
	return s.repo.MarkEntryScanError(ctx, checkoutID, fileID, cause.Error(), s.now())
}

func (s *Scanner) ignored(relativePath string) bool {
	for _, pattern := range s.config.IgnorePatterns {
		matched, _ := path.Match(pattern, relativePath)
		if matched {
			return true
		}
		matched, _ = path.Match(pattern, path.Base(relativePath))
		if matched {
			return true
		}
	}
	return false
}

func observeFileIdentity(root *os.Root, relativePath string, expected os.FileInfo) (string, error) {
	file, err := root.Open(relativePath)
	if err != nil {
		return "", scanObservationError("open checkout file", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect opened checkout file: %w", err)
	}
	pathInfo, err := root.Lstat(relativePath)
	if err != nil {
		return "", scanObservationError("inspect checkout path again", err)
	}
	if !os.SameFile(expected, openedInfo) || !os.SameFile(openedInfo, pathInfo) {
		return "", errScanObservationChanged
	}
	identity, err := filesystemIdentity(file)
	if err != nil {
		return "", fmt.Errorf("identify checkout file: %w", err)
	}
	return identity, nil
}

func hashSettledFile(ctx context.Context, root *os.Root, candidate ScanCandidate) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	pathInfo, err := root.Lstat(candidate.RelativePath)
	if err != nil {
		return "", scanObservationError("inspect settled checkout file", err)
	}
	if !pathInfo.Mode().IsRegular() || pathInfo.Size() != candidate.ObservedSize ||
		!pathInfo.ModTime().Equal(candidate.ObservedMTime) {
		return "", errScanObservationChanged
	}
	file, err := root.Open(candidate.RelativePath)
	if err != nil {
		return "", scanObservationError("open settled checkout file", err)
	}
	defer file.Close()
	stopCancel := context.AfterFunc(ctx, func() { _ = file.Close() })
	defer stopCancel()
	before, err := file.Stat()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("stat settled checkout file: %w", err)
	}
	if !os.SameFile(pathInfo, before) || before.Size() != candidate.ObservedSize ||
		!before.ModTime().Equal(candidate.ObservedMTime) {
		return "", errScanObservationChanged
	}
	identity, err := filesystemIdentity(file)
	if err != nil {
		return "", fmt.Errorf("identify settled checkout file: %w", err)
	}
	if candidate.ObservedIdentity != "" && identity != candidate.ObservedIdentity {
		return "", errScanObservationChanged
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, contextReader{ctx: ctx, reader: file}); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("hash settled checkout file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	after, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat hashed checkout file: %w", err)
	}
	finalPathInfo, err := root.Lstat(candidate.RelativePath)
	if err != nil {
		return "", scanObservationError("inspect hashed checkout path", err)
	}
	if !os.SameFile(before, after) || !os.SameFile(after, finalPathInfo) ||
		after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return "", errScanObservationChanged
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func scanObservationError(operation string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return errScanObservationChanged
	}
	return fmt.Errorf("%s: %w", operation, err)
}
