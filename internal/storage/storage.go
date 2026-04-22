// Package storage persists media bytes under per-owner prefixes across
// one or more backing tiers (flash, NAS). Implementations are safe for
// concurrent use by multiple goroutines within a single process; the
// import pipeline additionally serialises multiprocess access via a
// file lock (see internal/ingest).
package storage

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/wesm/fotobank/internal/owners"
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
