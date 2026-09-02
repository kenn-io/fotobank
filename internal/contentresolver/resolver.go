// Package contentresolver binds Fotobank assets and files to immutable
// Docbank versions before opening authoritative bytes.
package contentresolver

import (
	"context"
	"errors"
	"fmt"
	"io"

	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/media"
)

// Reference identifies one immutable version belonging to a ready asset file.
// Asset and File carry the product and Docbank mapping snapshots used when the
// reference was resolved.
type Reference struct {
	Asset     media.Media
	File      media.File
	VersionID string
	current   bool
}

// Read is an opened exact version plus the authoritative metadata Docbank
// returned for it. The caller owns Reader and must close it.
type Read struct {
	Reference Reference
	SHA256    string
	Size      int64
	MediaType string
	Offset    int64
	Length    int64
	Reader    io.ReadCloser
}

// Resolver maps ready Fotobank assets and files to catalog-authorized Docbank
// versions. Authorization and hidden-media policy remain service concerns.
type Resolver struct {
	repo  *media.Repo
	store *content.Adapter
}

// New constructs a Resolver over the product catalog and embedded Docbank
// boundary used by the deployment.
func New(repo *media.Repo, store *content.Adapter) *Resolver {
	return &Resolver{repo: repo, store: store}
}

// ResolveCurrent returns the exact version currently recorded for a file. An
// empty fileID selects the asset's primary file.
func (r *Resolver) ResolveCurrent(ctx context.Context, assetID, fileID string) (Reference, error) {
	ref, err := r.resolveFile(ctx, assetID, fileID)
	if err != nil {
		return Reference{}, err
	}
	if ref.File.CurrentVersionID == "" {
		return Reference{}, fmt.Errorf("resolve current content: %w: file has no current version", errs.ErrContentUnavailable)
	}
	ref.VersionID = ref.File.CurrentVersionID
	ref.current = true
	return ref, nil
}

// ValidateCurrent resolves a file and checks its recorded node, current
// version, SHA-256, and size against Docbank without opening the content bytes.
func (r *Resolver) ValidateCurrent(ctx context.Context, assetID, fileID string) (Reference, error) {
	ref, err := r.ResolveCurrent(ctx, assetID, fileID)
	if err != nil {
		return Reference{}, err
	}
	node, err := r.store.Stat(ctx, ref.File.DocbankVirtualPath)
	if err != nil {
		return Reference{}, fmt.Errorf("validate current content: %w", err)
	}
	if ref.File.DocbankNodeID == nil || node.ID != *ref.File.DocbankNodeID {
		return Reference{}, fmt.Errorf("validate current content: %w: path does not name the recorded file",
			errs.ErrNotFound)
	}
	if node.CurrentVersionID != ref.VersionID || node.SHA256 != ref.File.SHA256 || node.Size != ref.File.Size {
		return Reference{}, fmt.Errorf("validate current content: %w: current projection differs from Docbank",
			errs.ErrContentIdentityMismatch)
	}
	return ref, nil
}

// ResolveVersion binds an immutable Docbank version to a file. Open verifies
// that the version belongs to the file's recorded Docbank node, so a version
// from another asset or file is never accepted.
func (r *Resolver) ResolveVersion(
	ctx context.Context,
	assetID, fileID, versionID string,
) (Reference, error) {
	if versionID == "" {
		return Reference{}, fmt.Errorf("resolve exact content: %w: version ID is required", errs.ErrInvalidArgument)
	}
	ref, err := r.resolveFile(ctx, assetID, fileID)
	if err != nil {
		return Reference{}, err
	}
	ref.VersionID = versionID
	return ref, nil
}

func (r *Resolver) resolveFile(ctx context.Context, assetID, fileID string) (Reference, error) {
	if r == nil || r.repo == nil || r.store == nil {
		return Reference{}, fmt.Errorf("resolve content: %w: resolver is not configured", errs.ErrContentUnavailable)
	}
	asset, err := r.repo.GetByID(ctx, assetID)
	if err != nil {
		return Reference{}, fmt.Errorf("resolve content asset: %w", err)
	}
	if fileID == "" {
		fileID = asset.PrimaryFileID
	}
	file, err := r.repo.GetFile(ctx, fileID)
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			return Reference{}, fmt.Errorf("resolve content file: %w", errs.ErrNotFound)
		}
		return Reference{}, fmt.Errorf("resolve content file: %w", err)
	}
	if file.AssetID != asset.ID || file.Owner != asset.Owner || file.DocbankNodeID == nil {
		return Reference{}, fmt.Errorf("resolve content file: %w", errs.ErrNotFound)
	}
	return Reference{Asset: asset, File: file}, nil
}

// Open opens a full verified stream for offset=0,length<0 or an exact logical
// byte range otherwise. Full reads report verification failures through
// ordinary Reader.Read error semantics at EOF.
func (r *Resolver) Open(ctx context.Context, ref Reference, offset, length int64) (*Read, error) {
	if r == nil || r.store == nil {
		return nil, fmt.Errorf("open exact content: %w: resolver is not configured", errs.ErrContentUnavailable)
	}
	if ref.File.DocbankNodeID == nil || ref.VersionID == "" {
		return nil, fmt.Errorf("open exact content: %w: incomplete file mapping", errs.ErrInvalidArgument)
	}
	if offset == 0 && length < 0 {
		opened, err := r.store.OpenVersion(ctx, ref.VersionID)
		if err != nil {
			return nil, fmt.Errorf("open exact content: %w", err)
		}
		if err := validateOpened(ref, opened.NodeID, opened.SHA256, opened.Size); err != nil {
			return nil, errors.Join(err, opened.Reader.Close())
		}
		return &Read{
			Reference: ref,
			SHA256:    opened.SHA256,
			Size:      opened.Size,
			MediaType: opened.MediaType,
			Offset:    0,
			Length:    opened.Size,
			Reader:    &verifyOnEOF{reader: opened.Reader},
		}, nil
	}
	if offset < 0 || length <= 0 {
		return nil, fmt.Errorf("open exact content: %w: invalid byte range", errs.ErrInvalidArgument)
	}
	opened, err := r.store.OpenVersionRange(ctx, ref.VersionID, offset, length)
	if err != nil {
		return nil, fmt.Errorf("open exact content range: %w", err)
	}
	if err := validateOpened(ref, opened.NodeID, opened.SHA256, opened.Size); err != nil {
		return nil, errors.Join(err, opened.Reader.Close())
	}
	return &Read{
		Reference: ref,
		SHA256:    opened.SHA256,
		Size:      opened.Size,
		MediaType: opened.MediaType,
		Offset:    opened.Offset,
		Length:    opened.Length,
		Reader:    opened.Reader,
	}, nil
}

// OpenCurrent resolves and opens the version currently recorded for a file.
func (r *Resolver) OpenCurrent(
	ctx context.Context,
	assetID, fileID string,
	offset, length int64,
) (*Read, error) {
	ref, err := r.ResolveCurrent(ctx, assetID, fileID)
	if err != nil {
		return nil, err
	}
	return r.Open(ctx, ref, offset, length)
}

func validateOpened(ref Reference, nodeID int64, sha256 string, size int64) error {
	if ref.File.DocbankNodeID == nil || nodeID != *ref.File.DocbankNodeID {
		return fmt.Errorf("open exact content: %w: version does not belong to file", errs.ErrNotFound)
	}
	if ref.current && (sha256 != ref.File.SHA256 || size != ref.File.Size) {
		return fmt.Errorf("open exact content: %w: current projection differs from Docbank", errs.ErrContentIdentityMismatch)
	}
	return nil
}

// verifyOnEOF turns Docbank's explicit verified-reader contract into normal
// stream error semantics for callers such as HTTP and projection workers.
type verifyOnEOF struct {
	reader   content.VerifiedReadCloser
	verified bool
}

func (r *verifyOnEOF) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if errors.Is(err, io.EOF) && !r.verified {
		r.verified = true
		if verifyErr := r.reader.Verify(); verifyErr != nil {
			return n, fmt.Errorf("verify exact content: %w", verifyErr)
		}
	}
	return n, err
}

func (r *verifyOnEOF) Close() error { return r.reader.Close() }
