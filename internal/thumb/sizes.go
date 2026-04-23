// Package thumb implements the fotobank thumbnail pipeline: a DB-backed
// claim/lease Queue, a Worker that decodes/resizes/encodes, and the
// shared Size/key vocabulary used by the HTTP and CLI layers.
package thumb

import (
	"errors"
	"fmt"
)

// Size identifies one of the three thumbnail dimensions. The underlying
// string is the stable on-the-wire name (query param, filename stem).
type Size string

const (
	SizeGrid     Size = "grid"
	SizePreview  Size = "preview"
	SizeLightbox Size = "lightbox"
)

// ErrUnknownSize is returned by ParseSize when the input isn't a
// recognized size name. HTTP handlers translate this to 400.
var ErrUnknownSize = errors.New("thumb: unknown size")

// MaxEdge returns the target pixel length of the longest edge. Values
// match the vision spec §12.
func (s Size) MaxEdge() int {
	switch s {
	case SizeGrid:
		return 256
	case SizePreview:
		return 1024
	case SizeLightbox:
		return 2048
	}
	return 0
}

// ParseSize maps a query-param / filename string to a Size. Empty input
// is treated as invalid — the HTTP handler defaults to "grid" before
// calling in.
func ParseSize(s string) (Size, error) {
	switch s {
	case "grid":
		return SizeGrid, nil
	case "preview":
		return SizePreview, nil
	case "lightbox":
		return SizeLightbox, nil
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownSize, s)
}

// AllSizes returns every Size in the order the worker emits them.
// Returns a fresh slice so callers can iterate freely.
func AllSizes() []Size {
	return []Size{SizeGrid, SizePreview, SizeLightbox}
}

// ThumbKey is the storage key for one thumbnail. The versioned v{N}/
// subdirectory makes every write write-once — critical because
// storage.Store.Write rejects overwrites (see spec §7.1). Files are
// JPEG (see spec §7.2); format change would bump this function's
// suffix.
func ThumbKey(mediaID string, version int, size Size) string {
	return fmt.Sprintf(".thumbs/%s/v%d/%s.jpg", mediaID, version, size)
}
