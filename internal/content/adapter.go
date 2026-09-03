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
	ManagedRoots []ManagedRoot
}

type ManagedRoot struct {
	Path            string
	CreateIfMissing bool
	// AllowUnavailable defers only a missing-path check at Open. Import and
	// checkout root validation still requires every managed root to resolve.
	AllowUnavailable bool
}

// CheckoutRoot is an existing canonical working directory that the opened
// adapter verified does not overlap Docbank or Fotobank-managed storage. Its
// path cannot be constructed outside this package.
type CheckoutRoot struct {
	mu           sync.Mutex
	path         string
	root         *os.Root
	taken        bool
	docbankRoot  string
	managedRoots []string
}

// Path returns the canonical path recorded in Fotobank's checkout catalog.
func (r *CheckoutRoot) Path() string {
	if r == nil {
		return ""
	}
	return r.path
}

// Overlaps reports whether another path currently names, contains, or is
// contained by this checkout root. It compares filesystem identities as well
// as path strings so a live checkout cannot be reused through a later alias.
// An unresolved live root remains reserved because distinct trees cannot be
// proven without its filesystem identity.
func (r *CheckoutRoot) Overlaps(other string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.root == nil {
		return false
	}
	if pathsOverlap(r.path, other) {
		return true
	}
	resolved, err := filepath.EvalSymlinks(other)
	if err != nil {
		return true
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return true
	}
	resolved = filepath.Clean(resolved)
	if pathsOverlap(r.path, resolved) {
		return true
	}
	boundInfo, err := r.root.Stat(".")
	if err != nil {
		return true
	}
	otherInfo, err := os.Stat(resolved)
	if err != nil {
		return true
	}
	return pathTreeContainsFile(resolved, boundInfo) || pathTreeContainsFile(r.path, otherInfo)
}

func pathTreeContainsFile(current string, target os.FileInfo) bool {
	for {
		info, err := os.Stat(current)
		if err != nil {
			return true
		}
		if os.SameFile(info, target) {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
		current = parent
	}
}

// Take verifies that the catalog path still names the bound directory, then
// transfers ownership to the materializer. A validated root is single-use so
// no later operation can reopen its path.
func (r *CheckoutRoot) Take() (*os.Root, error) {
	if r == nil {
		return nil, fmt.Errorf("%w: checkout root is not validated", errs.ErrInvalidArgument)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.root == nil || r.taken {
		return nil, fmt.Errorf("%w: checkout root is not validated or was already consumed", errs.ErrInvalidArgument)
	}
	if err := r.validateLocked(); err != nil {
		return nil, err
	}
	r.taken = true
	return r.root, nil
}

// Revalidate confirms that the catalog path still names the retained working
// directory and remains outside Docbank and Fotobank-managed storage.
func (r *CheckoutRoot) Revalidate() error {
	if r == nil {
		return fmt.Errorf("%w: checkout root is not validated", errs.ErrInvalidArgument)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.root == nil {
		return fmt.Errorf("%w: checkout root is not validated", errs.ErrInvalidArgument)
	}
	return r.validateLocked()
}

func (r *CheckoutRoot) validateLocked() error {
	boundInfo, err := r.root.Stat(".")
	if err != nil {
		return fmt.Errorf("inspect bound checkout root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(r.path)
	if err != nil {
		return fmt.Errorf("%w: resolve checkout root again: %w", errs.ErrBadConfiguration, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return fmt.Errorf("make checkout root absolute again: %w", err)
	}
	resolved = filepath.Clean(resolved)
	if resolved != r.path {
		return fmt.Errorf("%w: checkout root canonical path changed after validation", errs.ErrBadConfiguration)
	}
	docbankRoot, managedRoots, err := resolveBoundaryRoots(r.docbankRoot, r.managedRoots)
	if err != nil {
		return err
	}
	if pathsOverlap(resolved, docbankRoot) {
		return fmt.Errorf("%w: checkout root now overlaps Docbank vault", errs.ErrBadConfiguration)
	}
	for _, managedRoot := range managedRoots {
		if pathsOverlap(resolved, managedRoot) {
			return fmt.Errorf("%w: checkout root now overlaps managed storage", errs.ErrBadConfiguration)
		}
	}
	pathInfo, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("%w: checkout root path changed after validation: %w", errs.ErrBadConfiguration, err)
	}
	if !os.SameFile(boundInfo, pathInfo) {
		return fmt.Errorf("%w: checkout root path changed after validation", errs.ErrBadConfiguration)
	}
	return nil
}

// Close releases a validated root that was not transferred to a materializer.
// It is safe to call after Take or more than once.
func (r *CheckoutRoot) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.root == nil || r.taken {
		return nil
	}
	err := r.root.Close()
	r.root = nil
	return err
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

type MetadataTimestamp struct {
	Normalized string
	Precision  string
	Timezone   string
}

type MetadataValue struct {
	Kind      string
	String    string
	Integer   *int64
	Number    *float64
	Timestamp *MetadataTimestamp
}

// SourceMetadata is the subset of Docbank's exact-version metadata contract
// needed by Fotobank's product projections. The version, extractor, and
// checksum together fence every derived asset field to immutable source
// evidence.
type SourceMetadata struct {
	VersionID            string
	ExtractorFingerprint string
	Checksum             string
	Fields               map[string]MetadataValue
}

type VisualPreviewState string

const (
	VisualPreviewReady       VisualPreviewState = "ready"
	VisualPreviewUnsupported VisualPreviewState = "unsupported"
	VisualPreviewFailed      VisualPreviewState = "failed"
)

// VisualPreview is Fotobank's dependency-free view of Docbank's canonical
// preview for one exact content version.
type VisualPreview struct {
	Version     Version
	State       VisualPreviewState
	FailureCode string
	MediaType   string
	Width       int
	Height      int
}

// VisualPreviewRead binds a ready preview result to its verified bytes.
type VisualPreviewRead struct {
	Preview VisualPreview
	Reader  VerifiedReadCloser
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

type ReplaceRequest struct {
	VirtualPath   string
	NodeID        int64
	BaseVersionID string
	Base          Identity
	MediaType     string
	Expected      Identity
	Reader        io.Reader
}

type ReplaceReceipt struct {
	Node     Node
	Version  Version
	Identity Identity
	Adopted  bool
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
		if !filepath.IsAbs(managedRoot.Path) {
			_ = vault.Close()
			return nil, fmt.Errorf("%w: managed root must be absolute", errs.ErrBadConfiguration)
		}
		if managedRoot.CreateIfMissing {
			if err := os.MkdirAll(managedRoot.Path, 0o700); err != nil {
				_ = vault.Close()
				return nil, fmt.Errorf("create local managed root: %w", err)
			}
		}
		resolvedManagedRoot, evalErr := filepath.EvalSymlinks(managedRoot.Path)
		if evalErr != nil {
			if managedRoot.AllowUnavailable && os.IsNotExist(evalErr) {
				managedRoots[i] = filepath.Clean(managedRoot.Path)
				continue
			}
			_ = vault.Close()
			return nil, fmt.Errorf("%w: resolve managed root: %w", errs.ErrBadConfiguration, evalErr)
		}
		info, statErr := os.Stat(resolvedManagedRoot)
		if statErr != nil {
			if managedRoot.AllowUnavailable && os.IsNotExist(statErr) {
				managedRoots[i] = filepath.Clean(managedRoot.Path)
				continue
			}
			_ = vault.Close()
			return nil, fmt.Errorf("%w: inspect managed root: %w", errs.ErrBadConfiguration, statErr)
		}
		if !info.IsDir() {
			_ = vault.Close()
			return nil, fmt.Errorf("%w: managed root is not a directory", errs.ErrBadConfiguration)
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
	return a.resolveExternalRoot(sourceRoot, "import")
}

// ResolveCheckoutRoot binds an existing canonical directory after rejecting
// overlap with Docbank authority or Fotobank-managed storage. Materializers
// consume the returned capability as the working-copy root.
func (a *Adapter) ResolveCheckoutRoot(checkoutRoot string) (*CheckoutRoot, error) {
	if a == nil || a.vault == nil {
		return nil, fmt.Errorf("%w: Docbank vault is not open", errs.ErrContentUnavailable)
	}
	bound, err := os.OpenRoot(checkoutRoot)
	if err != nil {
		return nil, fmt.Errorf("open checkout root: %w", err)
	}
	closeBound := true
	defer func() {
		if closeBound {
			_ = bound.Close()
		}
	}()
	openedInfo, err := bound.Stat(".")
	if err != nil {
		return nil, fmt.Errorf("inspect opened checkout root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(checkoutRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve checkout root: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, fmt.Errorf("make checkout root absolute: %w", err)
	}
	resolved = filepath.Clean(resolved)
	resolvedInfo, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("stat checkout root: %w", err)
	}
	if !os.SameFile(openedInfo, resolvedInfo) {
		return nil, fmt.Errorf("%w: checkout root changed during validation", errs.ErrBadConfiguration)
	}
	if !openedInfo.IsDir() {
		return nil, fmt.Errorf("%w: checkout root is not a directory", errs.ErrInvalidArgument)
	}
	docbankRoot, managedRoots, err := resolveBoundaryRoots(a.root, a.managedRoots)
	if err != nil {
		return nil, err
	}
	if pathsOverlap(resolved, docbankRoot) {
		return nil, fmt.Errorf("%w: checkout root overlaps Docbank vault", errs.ErrBadConfiguration)
	}
	for _, managedRoot := range managedRoots {
		if pathsOverlap(resolved, managedRoot) {
			return nil, fmt.Errorf("%w: checkout root overlaps managed storage", errs.ErrBadConfiguration)
		}
	}
	closeBound = false
	return &CheckoutRoot{
		path: resolved, root: bound, docbankRoot: a.root,
		managedRoots: append([]string(nil), a.managedRoots...),
	}, nil
}

func (a *Adapter) resolveExternalRoot(sourceRoot, kind string) (string, error) {
	if a == nil || a.vault == nil {
		return "", fmt.Errorf("%w: Docbank vault is not open", errs.ErrContentUnavailable)
	}
	resolved, err := filepath.EvalSymlinks(sourceRoot)
	if err != nil {
		return "", fmt.Errorf("resolve %s root: %w", kind, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("make %s root absolute: %w", kind, err)
	}
	resolved = filepath.Clean(resolved)
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat %s root: %w", kind, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s root is not a directory", errs.ErrInvalidArgument, kind)
	}
	docbankRoot, managedRoots, err := resolveBoundaryRoots(a.root, a.managedRoots)
	if err != nil {
		return "", err
	}
	if pathsOverlap(resolved, docbankRoot) {
		return "", fmt.Errorf("%w: %s root overlaps Docbank vault", errs.ErrBadConfiguration, kind)
	}
	for _, managedRoot := range managedRoots {
		if pathsOverlap(resolved, managedRoot) {
			return "", fmt.Errorf("%w: %s root overlaps managed storage", errs.ErrBadConfiguration, kind)
		}
	}
	return resolved, nil
}

func resolveBoundaryRoots(docbankRoot string, managedRoots []string) (string, []string, error) {
	resolvedDocbank, err := resolveBoundaryRoot(docbankRoot)
	if err != nil {
		return "", nil, fmt.Errorf("%w: resolve current Docbank root: %w",
			errs.ErrBadConfiguration, err)
	}
	resolvedManaged := make([]string, len(managedRoots))
	for i, managedRoot := range managedRoots {
		resolvedManaged[i], err = resolveBoundaryRoot(managedRoot)
		if err != nil {
			return "", nil, fmt.Errorf("%w: resolve current managed root: %w",
				errs.ErrBadConfiguration, err)
		}
	}
	return resolvedDocbank, resolvedManaged, nil
}

func resolveBoundaryRoot(root string) (string, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
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

// EnsureSourceMetadata returns current local metadata for one exact immutable
// content version, processing it synchronously when needed.
func (a *Adapter) EnsureSourceMetadata(ctx context.Context, versionID string) (SourceMetadata, error) {
	// Docbank holds its vault mutation lock across the version lookup, verified
	// processing, publication, and readback performed by this call.
	metadata, err := a.vault.EnsureSourceMetadata(ctx, versionID)
	if err != nil {
		return SourceMetadata{}, translateError(err)
	}
	fields := make(map[string]MetadataValue, len(metadata.Fields))
	for _, field := range metadata.Fields {
		value := MetadataValue{
			Kind:    string(field.Value.Kind),
			Integer: field.Value.Integer, Number: field.Value.Number,
		}
		if field.Value.String != nil {
			value.String = *field.Value.String
		}
		if field.Value.Timestamp != nil {
			value.Timestamp = &MetadataTimestamp{
				Normalized: field.Value.Timestamp.Normalized,
				Precision:  string(field.Value.Timestamp.Precision),
				Timezone:   string(field.Value.Timestamp.Timezone),
			}
		}
		fields[field.Key] = value
	}
	return SourceMetadata{
		VersionID:            metadata.Version.ID,
		ExtractorFingerprint: metadata.ExtractorFingerprint,
		Checksum:             metadata.Checksum,
		Fields:               fields,
	}, nil
}

// VisualPreview returns the current canonical preview result for one exact
// immutable content version without producing it.
func (a *Adapter) VisualPreview(ctx context.Context, versionID string) (VisualPreview, error) {
	preview, err := a.vault.VisualPreview(ctx, versionID)
	if err != nil {
		return VisualPreview{}, translateError(err)
	}
	return projectVisualPreview(preview), nil
}

// EnsureVisualPreview returns the current canonical preview result for one
// exact immutable content version, producing it synchronously when needed.
func (a *Adapter) EnsureVisualPreview(ctx context.Context, versionID string) (VisualPreview, error) {
	preview, err := a.vault.EnsureVisualPreview(ctx, versionID)
	if err != nil {
		return VisualPreview{}, translateError(err)
	}
	return projectVisualPreview(preview), nil
}

// OpenVisualPreview opens the verified bytes of a ready exact-version preview.
func (a *Adapter) OpenVisualPreview(ctx context.Context, versionID string) (*VisualPreviewRead, error) {
	opened, err := a.vault.OpenVisualPreview(ctx, versionID)
	if err != nil {
		return nil, translateError(err)
	}
	return &VisualPreviewRead{
		Preview: projectVisualPreview(opened.Preview),
		Reader:  translateReader(opened.Reader),
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

// Replace appends one immutable version only while the requested base remains
// current. An exact current head matching Expected is adopted so a caller can
// recover after Docbank committed but its receipt was not recorded locally.
func (a *Adapter) Replace(ctx context.Context, request ReplaceRequest) (ReplaceReceipt, error) {
	if request.NodeID <= 0 || request.BaseVersionID == "" || request.Reader == nil {
		return ReplaceReceipt{}, fmt.Errorf("replace content: %w: incomplete request", errs.ErrInvalidArgument)
	}
	a.mutation.Lock()
	defer a.mutation.Unlock()
	node, err := a.vault.Stat(ctx, request.VirtualPath)
	if err != nil {
		return ReplaceReceipt{}, translateError(err)
	}
	if node.ID != request.NodeID || node.Kind != "file" {
		return ReplaceReceipt{}, fmt.Errorf("replace content: %w: virtual path no longer names the recorded file",
			errs.ErrContentConflict)
	}
	if node.CurrentVersionID != request.BaseVersionID {
		if node.BlobHash == request.Expected.SHA256 && node.Size == request.Expected.Size &&
			node.MediaType == request.MediaType {
			return ReplaceReceipt{
				Node: projectNode(node, request.VirtualPath),
				Version: Version{
					ID: node.CurrentVersionID, NodeID: node.ID, SHA256: node.BlobHash,
					Size: node.Size, MediaType: node.MediaType,
				},
				Identity: request.Expected,
				Adopted:  true,
			}, nil
		}
		return ReplaceReceipt{}, fmt.Errorf("replace content: %w: base version is no longer current",
			errs.ErrContentConflict)
	}
	if node.BlobHash != request.Base.SHA256 || node.Size != request.Base.Size ||
		node.MediaType != request.MediaType {
		return ReplaceReceipt{}, fmt.Errorf("replace content: %w: base identity differs from Docbank",
			errs.ErrContentConflict)
	}
	receipt, err := a.vault.Put(ctx, request.VirtualPath, request.Reader, docbank.PutOptions{
		MediaType: request.MediaType,
		Expected: &docbank.ContentIdentity{
			SHA256: request.Expected.SHA256,
			Size:   request.Expected.Size,
		},
		IfRevision: node.Revision,
	})
	if err != nil {
		return ReplaceReceipt{}, translateError(err)
	}
	return ReplaceReceipt{
		Node:     projectNode(receipt.Node, request.VirtualPath),
		Version:  projectVersion(receipt.Version),
		Identity: Identity{SHA256: receipt.Computed.SHA256, Size: receipt.Computed.Size},
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

func projectVisualPreview(preview docbank.VisualPreview) VisualPreview {
	projected := VisualPreview{
		Version: projectVersion(preview.Version),
		State:   VisualPreviewState(preview.State),
	}
	if preview.Failure != nil {
		projected.FailureCode = preview.Failure.Code
	}
	if preview.Output != nil {
		projected.MediaType = preview.Output.MediaType
		projected.Width = preview.Output.Width
		projected.Height = preview.Output.Height
	}
	return projected
}
