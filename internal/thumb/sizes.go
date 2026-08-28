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
	SizeGrid    Size = "grid"
	SizePreview Size = "preview"
	SizeLarge   Size = "large"
)

// ErrUnknownSize is returned by ParseSize when the input isn't a
// recognized size name. HTTP handlers translate this to 400.
var ErrUnknownSize = errors.New("thumb: unknown size")

// MaxEdge returns the target pixel length of the longest edge.
//   - grid (256) drives the library grid thumbnails.
//   - preview (2560) is the lightbox "fit" source (covers most laptop
//     and mobile displays at native pixel ratio without RAW decode).
//   - large (4096) is the lightbox 1:1 source for high-DPI / 4K
//     displays. Heavy to encode (~1s/row on Apple Silicon RAW), but
//     produced eagerly inside emitSizes alongside grid and preview
//     rather than computed on demand at request time. The lightbox UI
//     can therefore assume ?size=large&v=N is present whenever the
//     row's thumb_status is "ready".
func (s Size) MaxEdge() int {
	switch s {
	case SizeGrid:
		return 256
	case SizePreview:
		return 2560
	case SizeLarge:
		return 4096
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
	case "large":
		return SizeLarge, nil
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownSize, s)
}

// AllSizes returns every Size in the order the worker emits them.
// Returns a fresh slice so callers can iterate freely.
func AllSizes() []Size {
	return []Size{SizeGrid, SizePreview, SizeLarge}
}

// ThumbKey is the storage key for one thumbnail. The versioned v{N}/
// subdirectory makes every write write-once — critical because
// storage.Store.Write rejects overwrites. Files are JPEG; a format
// change must change this function's suffix.
func ThumbKey(mediaID string, version int, size Size) string {
	return fmt.Sprintf(".thumbs/%s/v%d/%s.jpg", mediaID, version, size)
}
