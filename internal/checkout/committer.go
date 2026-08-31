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
	"time"

	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
)

// Committer advances settled tracked working files through conditional
// Docbank replacements. The checkout entry itself is the durable operation:
// pending records the exact base and expected local identity before Docbank is
// touched, and an exact current Docbank head can be adopted after interruption.
type Committer struct {
	repo    *Repo
	content *content.Adapter
	now     func() time.Time
}

func NewCommitter(repo *Repo, contentStore *content.Adapter) *Committer {
	return &Committer{
		repo: repo, content: contentStore,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (c *Committer) Commit(
	ctx context.Context,
	caller owners.Principal,
	checkoutID string,
) (CommitResult, error) {
	if c == nil || c.repo == nil || c.content == nil {
		return CommitResult{}, fmt.Errorf("commit checkout: %w: committer is not configured",
			errs.ErrContentUnavailable)
	}
	checkout, err := c.repo.Get(ctx, checkoutID)
	if err != nil {
		return CommitResult{}, err
	}
	if checkout.Owner != caller || checkout.State != StateActive {
		return CommitResult{}, fmt.Errorf("commit checkout: %w", errs.ErrNotFound)
	}
	validatedRoot, err := c.content.ResolveCheckoutRoot(checkout.Root)
	if err != nil {
		return CommitResult{}, err
	}
	defer validatedRoot.Close()
	root, err := validatedRoot.Take()
	if err != nil {
		return CommitResult{}, err
	}
	defer root.Close()

	entries, err := c.repo.ListPendingEntries(ctx, checkoutID)
	if err != nil {
		return CommitResult{}, err
	}
	result := CommitResult{Pending: len(entries)}
	var commitErrors []error
	for _, entry := range entries {
		committed, conflicted, commitErr := c.commitEntry(ctx, validatedRoot, root, entry)
		if committed {
			result.Committed++
		}
		if conflicted {
			result.Conflicts++
		}
		if commitErr != nil {
			commitErrors = append(commitErrors,
				fmt.Errorf("commit checkout file %s: %w", entry.RelativePath, commitErr))
		}
	}
	return result, errors.Join(commitErrors...)
}

func (c *Committer) commitEntry(
	ctx context.Context,
	validatedRoot *content.CheckoutRoot,
	root *os.Root,
	entry Entry,
) (bool, bool, error) {
	target, err := c.repo.GetCommitTarget(ctx, entry.CheckoutID, entry.FileID)
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			marked, markErr := c.repo.MarkCommitConflict(ctx, entry, err, c.now())
			return false, marked, markErr
		}
		return false, false, err
	}
	if target.Entry.State != EntryPending || target.Entry.BaseVersionID == "" ||
		target.Entry.ObservedSHA256 == "" || target.NodeID <= 0 || target.VirtualPath == "" {
		return false, false, fmt.Errorf("%w: incomplete pending checkout entry", errs.ErrInvalidArgument)
	}
	if err := validatedRoot.Revalidate(); err != nil {
		return false, false, err
	}
	file, err := openCommitSource(root, target.Entry)
	if err != nil {
		return false, false, err
	}
	if err := validatedRoot.Revalidate(); err != nil {
		return false, false, errors.Join(err, file.Close())
	}
	stopCancel := context.AfterFunc(ctx, func() { _ = file.Close() })
	receipt, replaceErr := c.content.Replace(ctx, content.ReplaceRequest{
		VirtualPath: target.VirtualPath,
		NodeID:      target.NodeID, BaseVersionID: target.Entry.BaseVersionID,
		Base: content.Identity{
			SHA256: target.Entry.BaseSHA256,
			Size:   target.Entry.BaseSize,
		},
		MediaType: target.MediaType,
		Expected: content.Identity{
			SHA256: target.Entry.ObservedSHA256,
			Size:   target.Entry.ObservedSize,
		},
		Reader: file,
	})
	if replaceErr != nil {
		stopCancel()
		closeErr := file.Close()
		if errors.Is(replaceErr, errs.ErrContentConflict) {
			marked, markErr := c.repo.MarkCommitConflict(ctx, entry, replaceErr, c.now())
			return false, marked, errors.Join(closeErr, markErr)
		}
		return false, false, errors.Join(replaceErr, closeErr)
	}
	clean, observationErr := commitSourceStillCurrent(ctx, root, file, target.Entry)
	stopCancel()
	closeErr := file.Close()
	rootErr := validatedRoot.Revalidate()
	if observationErr != nil || closeErr != nil || rootErr != nil {
		clean = false
	}
	applyErr := c.repo.ApplyCommit(ctx, target, CommitReceipt{
		NodeID: receipt.Node.ID, VersionID: receipt.Version.ID,
		SHA256: receipt.Identity.SHA256, Size: receipt.Identity.Size,
	}, clean, c.now())
	if applyErr != nil {
		if errors.Is(applyErr, errs.ErrContentConflict) {
			marked, markErr := c.repo.MarkCommitConflict(ctx, target.Entry, applyErr, c.now())
			return false, marked, errors.Join(observationErr, closeErr, rootErr, markErr)
		}
		return false, false, errors.Join(observationErr, closeErr, rootErr, applyErr)
	}
	return true, false, errors.Join(observationErr, closeErr, rootErr)
}

func openCommitSource(root *os.Root, entry Entry) (*os.File, error) {
	pathInfo, err := root.Lstat(entry.RelativePath)
	if err != nil {
		return nil, fmt.Errorf("inspect pending checkout file: %w", err)
	}
	if !pathInfo.Mode().IsRegular() || pathInfo.Size() != entry.ObservedSize ||
		!pathInfo.ModTime().Equal(entry.ObservedMTime) {
		return nil, errScanObservationChanged
	}
	file, err := root.Open(entry.RelativePath)
	if err != nil {
		return nil, fmt.Errorf("open pending checkout file: %w", err)
	}
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("inspect pending checkout file: %w", err), file.Close())
	}
	if !os.SameFile(pathInfo, openedInfo) || openedInfo.Size() != entry.ObservedSize ||
		!openedInfo.ModTime().Equal(entry.ObservedMTime) {
		return nil, errors.Join(errScanObservationChanged, file.Close())
	}
	identity, err := filesystemIdentity(file)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("identify pending checkout file: %w", err), file.Close())
	}
	if entry.ObservedIdentity != "" && identity != entry.ObservedIdentity {
		return nil, errors.Join(errScanObservationChanged, file.Close())
	}
	return file, nil
}

func commitSourceStillCurrent(
	ctx context.Context,
	root *os.Root,
	file *os.File,
	entry Entry,
) (bool, error) {
	before, err := file.Stat()
	if err != nil {
		return false, fmt.Errorf("inspect committed checkout file: %w", err)
	}
	pathInfo, err := root.Lstat(entry.RelativePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect committed checkout path: %w", err)
	}
	if !os.SameFile(before, pathInfo) || before.Size() != entry.ObservedSize ||
		!before.ModTime().Equal(entry.ObservedMTime) {
		return false, nil
	}
	identity, err := filesystemIdentity(file)
	if err != nil {
		return false, fmt.Errorf("identify committed checkout file: %w", err)
	}
	if entry.ObservedIdentity != "" && identity != entry.ObservedIdentity {
		return false, nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return false, fmt.Errorf("rewind committed checkout file: %w", err)
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, contextReader{ctx: ctx, reader: file}); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		return false, fmt.Errorf("hash committed checkout file: %w", err)
	}
	after, err := file.Stat()
	if err != nil {
		return false, fmt.Errorf("inspect rehashed checkout file: %w", err)
	}
	finalPathInfo, err := root.Lstat(entry.RelativePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect rehashed checkout path: %w", err)
	}
	if !os.SameFile(before, after) || !os.SameFile(after, finalPathInfo) ||
		after.Size() != entry.ObservedSize || !after.ModTime().Equal(entry.ObservedMTime) {
		return false, nil
	}
	return hex.EncodeToString(digest.Sum(nil)) == entry.ObservedSHA256, nil
}
