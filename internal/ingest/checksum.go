package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// SHA256 returns the lowercase SHA-256 content identity of path.
func SHA256(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	stopCancel := context.AfterFunc(ctx, func() { _ = f.Close() })
	defer stopCancel()
	h := sha256.New()
	var buffer [32 * 1024]byte
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := f.Read(buffer[:])
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		_, _ = h.Write(buffer[:n]) // hash.Hash.Write never returns an error.
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("sha256: %w", err)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
