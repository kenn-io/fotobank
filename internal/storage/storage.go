// Package storage persists media bytes under per-owner prefixes across
// one or more backing tiers (flash, NAS). Implementations are safe for
// concurrent use by multiple goroutines within a single process; the
// import pipeline additionally serialises multiprocess access via a
// file lock (see internal/ingest).
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"go.kenn.io/fotobank/internal/owners"
)

// Tier indicates which backing tier served a read or the logical
// location of a key.
type Tier string

const (
	TierFlash Tier = "flash"
	TierNAS   Tier = "nas"
)

// StoreInfo describes a stored object.
type StoreInfo struct {
	Size    int64
	ModTime time.Time
	Tier    Tier
}

// ErrPathOccupied indicates that the no-clobber finalize step saw an
// existing file at the final path. Callers decide whether to retry
// with a bumped sequence number (photo path) or adopt the orphan
// (content-addressed video path).
var ErrPathOccupied = errors.New("storage: path already occupied")

// Store persists media bytes. Implementations must be safe for
// concurrent use by multiple goroutines within a single process; the
// import pipeline additionally serialises multiprocess access via a
// file lock (see internal/ingest).
type Store interface {
	Stat(ctx context.Context, owner owners.Principal, key string) (StoreInfo, error)
	ReadRange(ctx context.Context, owner owners.Principal, key string, offset, length int64) (io.ReadCloser, error)
	Write(ctx context.Context, owner owners.Principal, key string, src io.Reader) (string, error)
	Delete(ctx context.Context, owner owners.Principal, key string) error
}

// ErrInvalidKey indicates a storage key that could escape its owner
// prefix (absolute path, contains "..", contains backslash, empty, or
// has empty/"."/".." segments).
var ErrInvalidKey = errors.New("storage: invalid key")

// ErrInvalidStorageKey indicates an owner storage_key that would
// unsafely join — empty, absolute, or containing any path separator
// or traversal segment. Storage keys must be a single filesystem name
// (e.g., a UUID), not a path.
var ErrInvalidStorageKey = errors.New("storage: invalid storage key")

// validateKey verifies that key is a relative POSIX path that cannot
// escape its owner prefix via traversal. Keys must use forward slashes,
// be non-empty, not absolute, and contain no "", ".", or ".." segments.
func validateKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: empty", ErrInvalidKey)
	}
	if strings.ContainsRune(key, '\\') {
		return fmt.Errorf("%w: contains backslash: %q", ErrInvalidKey, key)
	}
	if strings.HasPrefix(key, "/") {
		return fmt.Errorf("%w: absolute path: %q", ErrInvalidKey, key)
	}
	for seg := range strings.SplitSeq(key, "/") {
		switch seg {
		case "", ".", "..":
			return fmt.Errorf("%w: invalid segment %q in %q", ErrInvalidKey, seg, key)
		}
	}
	return nil
}

// ValidateStorageKey rejects anything that could make the per-owner
// subdirectory escape its parent root. Storage keys are registered at
// owner-creation time and are expected to be a single opaque filesystem
// name (empty, ".", "..", or any path separator is refused). Exported
// so sibling packages (reconcile, cli) can apply the same check before
// joining storage keys into filesystem paths.
func ValidateStorageKey(sk string) error {
	switch sk {
	case "":
		return fmt.Errorf("%w: empty", ErrInvalidStorageKey)
	case ".", "..":
		return fmt.Errorf("%w: traversal %q", ErrInvalidStorageKey, sk)
	}
	if strings.ContainsAny(sk, "/\\") {
		return fmt.Errorf("%w: contains path separator: %q", ErrInvalidStorageKey, sk)
	}
	return nil
}
