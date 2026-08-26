package content

import (
	"context"
	"io"
	"sync"
	"time"

	"go.kenn.io/docbank"
)

type Config struct {
	Root string
}

type Identity struct {
	SHA256 string
	Size   int64
}

type Node struct {
	ID               int64
	VirtualPath      string
	CurrentVersionID string
	SHA256           string
	Size             int64
	MediaType        string
	Revision         int64
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

type Adapter struct {
	vault    *docbank.Vault
	mutation sync.Mutex
}

func Open(ctx context.Context, cfg Config) (*Adapter, error) {
	vault, err := docbank.New(ctx, docbank.Config{Root: cfg.Root})
	if err != nil {
		return nil, translateError(err)
	}
	return &Adapter{vault: vault}, nil
}

func (a *Adapter) Close() error {
	if a == nil || a.vault == nil {
		return nil
	}
	return translateError(a.vault.Close())
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

type translatedReader struct {
	docbank.VerifiedReadCloser
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
