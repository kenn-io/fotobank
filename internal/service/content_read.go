package service

import (
	"context"
	"fmt"
	"io"

	"go.kenn.io/fotobank/internal/content"
)

func openExactVersion(ctx context.Context, store *content.Adapter, versionID string, offset, length int64) (io.ReadCloser, error) {
	if offset == 0 && length < 0 {
		opened, err := store.OpenVersion(ctx, versionID)
		if err != nil {
			return nil, err
		}
		return &verifyOnEOF{reader: opened.Reader}, nil
	}
	opened, err := store.OpenVersionRange(ctx, versionID, offset, length)
	if err != nil {
		return nil, err
	}
	return opened.Reader, nil
}

// verifyOnEOF turns the explicit verified-reader contract into ordinary stream
// error semantics so HTTP copies cannot silently accept corrupt full content.
type verifyOnEOF struct {
	reader   content.VerifiedReadCloser
	verified bool
}

func (r *verifyOnEOF) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if err == io.EOF && !r.verified {
		r.verified = true
		if verifyErr := r.reader.Verify(); verifyErr != nil {
			return n, fmt.Errorf("verify exact content: %w", verifyErr)
		}
	}
	return n, err
}

func (r *verifyOnEOF) Close() error { return r.reader.Close() }
