package content

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.kenn.io/docbank"

	"go.kenn.io/fotobank/internal/errs"
)

type Config struct {
	Root         string
	ManagedRoots []string
}

type Identity struct {
	SHA256 string
	Size   int64
}

type Node struct {
	ID               int64
	VirtualPath      string
	Kind             string
	CurrentVersionID string
	SHA256           string
	Size             int64
	MediaType        string
	Revision         int64
}

// Walk visits every live node beneath virtualRoot using Docbank's bounded,
// stable traversal snapshot. The callback receives Fotobank projections only;
// Docbank types remain inside this package.
func (a *Adapter) Walk(ctx context.Context, virtualRoot string, visit func(Node) error) error {
	walker, err := a.vault.Walk(ctx, virtualRoot, docbank.WalkOptions{})
	if err != nil {
		return translateError(err)
	}
	defer walker.Close()
	for {
		page, nextErr := walker.Next(ctx)
		if nextErr != nil {
			if nextErr == io.EOF {
				return translateError(walker.Close())
			}
			return translateError(nextErr)
		}
		for _, entry := range page {
			if err := visit(projectNode(entry.Node, entry.Path)); err != nil {
				return err
			}
		}
	}
}

type Version struct {
	ID        string
	NodeID    int64
	SHA256    string
	Size      int64
	MediaType string
}

type Source struct {
	Kind        string
	Description string
	Reference   string
	ModifiedAt  *time.Time
}

type CreateRequest struct {
	VirtualPath string
	MediaType   string
	Expected    Identity
	Source      Source
	Reader      io.Reader
}

type CreateReceipt struct {
	Node     Node
	Version  Version
	Identity Identity
	Created  bool
}

type VerifiedReadCloser interface {
	io.ReadCloser
	Verify() error
}

type Read struct {
	NodeID    int64
	VersionID string
	SHA256    string
	MediaType string
	Size      int64
	Reader    VerifiedReadCloser
}

type RangeRead struct {
	NodeID    int64
	VersionID string
	SHA256    string
	MediaType string
	Size      int64
	Offset    int64
	Length    int64
	Reader    io.ReadCloser
}

type Adapter struct {
	vault        *docbank.Vault
	mutation     sync.Mutex
	root         string
	managedRoots []string
	closeOnce    sync.Once
	closeErr     error
}

func Open(ctx context.Context, cfg Config) (*Adapter, error) {
	vault, err := docbank.New(ctx, docbank.Config{Root: cfg.Root})
	if err != nil {
		return nil, translateError(err)
	}
	root, err := filepath.EvalSymlinks(cfg.Root)
	if err != nil {
		_ = vault.Close()
		return nil, fmt.Errorf("resolve opened Docbank root: %w", err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		_ = vault.Close()
		return nil, fmt.Errorf("make Docbank root absolute: %w", err)
	}
	managedRoots := make([]string, len(cfg.ManagedRoots))
	for i, managedRoot := range cfg.ManagedRoots {
		if !filepath.IsAbs(managedRoot) {
			_ = vault.Close()
			return nil, fmt.Errorf("%w: managed root must be absolute", errs.ErrBadConfiguration)
		}
		resolvedManagedRoot := managedRoot
		if evaluated, evalErr := filepath.EvalSymlinks(managedRoot); evalErr == nil {
			resolvedManagedRoot = evaluated
		} else if !os.IsNotExist(evalErr) {
			_ = vault.Close()
			return nil, fmt.Errorf("resolve managed root: %w", evalErr)
		}
		resolvedManagedRoot, err := filepath.Abs(resolvedManagedRoot)
		if err != nil {
			_ = vault.Close()
			return nil, fmt.Errorf("make managed root absolute: %w", err)
		}
		managedRoots[i] = filepath.Clean(resolvedManagedRoot)
	}
	return &Adapter{
		vault: vault, root: filepath.Clean(root), managedRoots: managedRoots,
	}, nil
}

func (a *Adapter) Close() error {
	if a == nil || a.vault == nil {
		return nil
	}
	a.closeOnce.Do(func() {
		a.closeErr = translateError(a.vault.Close())
	})
	return a.closeErr
}

// ResolveImportRoot returns the canonical import tree after rejecting a path
// that is equal to, contains, or is contained by the opened Docbank vault.
// Discovery must use the returned path so validation and traversal observe the
// same directory when the configured root is a symlink.
func (a *Adapter) ResolveImportRoot(sourceRoot string) (string, error) {
	if a == nil || a.vault == nil {
		return "", fmt.Errorf("%w: Docbank vault is not open", errs.ErrContentUnavailable)
	}
	resolved, err := filepath.EvalSymlinks(sourceRoot)
	if err != nil {
		return "", fmt.Errorf("resolve import root: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("make import root absolute: %w", err)
	}
	resolved = filepath.Clean(resolved)
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat import root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: import root is not a directory", errs.ErrInvalidArgument)
	}
	if pathsOverlap(resolved, a.root) {
		return "", fmt.Errorf("%w: import root overlaps Docbank vault", errs.ErrBadConfiguration)
	}
	for _, managedRoot := range a.managedRoots {
		if pathsOverlap(resolved, managedRoot) {
			return "", fmt.Errorf("%w: import root overlaps managed storage", errs.ErrBadConfiguration)
		}
	}
	return resolved, nil
}

func pathsOverlap(left, right string) bool {
	return pathContains(left, right) || pathContains(right, left)
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err == nil && (rel == "." || (rel != ".." && !filepath.IsAbs(rel) &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
		return true
	}
	parentVolume, parentParts := pathParts(parent)
	childVolume, childParts := pathParts(child)
	if !strings.EqualFold(parentVolume, childVolume) || len(parentParts) > len(childParts) {
		return false
	}
	for i, part := range parentParts {
		if !strings.EqualFold(part, childParts[i]) {
			return false
		}
	}
	return true
}

func pathParts(value string) (string, []string) {
	clean := filepath.Clean(value)
	volume := filepath.VolumeName(clean)
	remainder := strings.Trim(strings.TrimPrefix(clean, volume), string(filepath.Separator))
	if remainder == "" {
		return volume, []string{}
	}
	return volume, strings.Split(remainder, string(filepath.Separator))
}

func (a *Adapter) Stat(ctx context.Context, virtualPath string) (Node, error) {
	node, err := a.vault.Stat(ctx, virtualPath)
	if err != nil {
		return Node{}, translateError(err)
	}
	return projectNode(node, virtualPath), nil
}

func (a *Adapter) OpenCurrent(ctx context.Context, virtualPath string) (*Read, error) {
	opened, err := a.vault.OpenContent(ctx, virtualPath)
	if err != nil {
		return nil, translateError(err)
	}
	return &Read{
		NodeID:    opened.Node.ID,
		VersionID: opened.Node.CurrentVersionID,
		SHA256:    opened.Node.BlobHash,
		MediaType: opened.Node.MediaType,
		Size:      opened.Node.Size,
		Reader:    translateReader(opened.Reader),
	}, nil
}

func (a *Adapter) OpenVersion(ctx context.Context, versionID string) (*Read, error) {
	opened, err := a.vault.OpenVersionContent(ctx, versionID)
	if err != nil {
		return nil, translateError(err)
	}
	return &Read{
		NodeID:    opened.Version.NodeID,
		VersionID: opened.Version.ID,
		SHA256:    opened.Version.BlobHash,
		MediaType: opened.Version.MediaType,
		Size:      opened.Version.Size,
		Reader:    translateReader(opened.Reader),
	}, nil
}

func (a *Adapter) OpenVersionRange(
	ctx context.Context,
	versionID string,
	offset int64,
	length int64,
) (*RangeRead, error) {
	opened, err := a.vault.OpenVersionContentRange(ctx, versionID, docbank.ContentRangeOptions{
		Offset: offset,
		Length: length,
	})
	if err != nil {
		return nil, translateError(err)
	}
	return &RangeRead{
		NodeID:    opened.Version.NodeID,
		VersionID: opened.Version.ID,
		SHA256:    opened.Version.BlobHash,
		MediaType: opened.Version.MediaType,
		Size:      opened.Version.Size,
		Offset:    opened.Offset,
		Length:    opened.Length,
		Reader:    &translatedReadCloser{ReadCloser: opened.Reader},
	}, nil
}

type translatedReader struct {
	docbank.VerifiedReadCloser
}

type translatedReadCloser struct {
	io.ReadCloser
}

func (r *translatedReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	return n, translateReaderError(err)
}

func (r *translatedReadCloser) Close() error {
	return translateReaderError(r.ReadCloser.Close())
}

func translateReader(reader docbank.VerifiedReadCloser) VerifiedReadCloser {
	return &translatedReader{VerifiedReadCloser: reader}
}

func (r *translatedReader) Read(p []byte) (int, error) {
	n, err := r.VerifiedReadCloser.Read(p)
	return n, translateReaderError(err)
}

func (r *translatedReader) Verify() error {
	return translateReaderError(r.VerifiedReadCloser.Verify())
}

func (r *translatedReader) Close() error {
	return translateReaderError(r.VerifiedReadCloser.Close())
}

func (a *Adapter) Create(ctx context.Context, request CreateRequest) (CreateReceipt, error) {
	options := docbank.CreateOptions{
		MediaType: request.MediaType,
		Expected: docbank.ContentIdentity{
			SHA256: request.Expected.SHA256,
			Size:   request.Expected.Size,
		},
	}
	if request.Source.Kind != "" || request.Source.Description != "" ||
		request.Source.Reference != "" || request.Source.ModifiedAt != nil {
		options.Provenance = &docbank.ProvenanceSource{
			Kind:        request.Source.Kind,
			Description: request.Source.Description,
			Reference:   request.Source.Reference,
			ModifiedAt:  request.Source.ModifiedAt,
		}
	}

	a.mutation.Lock()
	defer a.mutation.Unlock()
	receipt, err := a.vault.Create(ctx, request.VirtualPath, request.Reader, options)
	if err != nil {
		return CreateReceipt{}, translateError(err)
	}
	return CreateReceipt{
		Node:     projectNode(receipt.Node, request.VirtualPath),
		Version:  projectVersion(receipt.Version),
		Identity: Identity{SHA256: receipt.Computed.SHA256, Size: receipt.Computed.Size},
		Created:  receipt.Created,
	}, nil
}

func projectNode(node docbank.Node, virtualPath string) Node {
	return Node{
		ID:               node.ID,
		VirtualPath:      virtualPath,
		Kind:             node.Kind,
		CurrentVersionID: node.CurrentVersionID,
		SHA256:           node.BlobHash,
		Size:             node.Size,
		MediaType:        node.MediaType,
		Revision:         node.Revision,
	}
}

func projectVersion(version docbank.ContentVersion) Version {
	return Version{
		ID:        version.ID,
		NodeID:    version.NodeID,
		SHA256:    version.BlobHash,
		Size:      version.Size,
		MediaType: version.MediaType,
	}
}
