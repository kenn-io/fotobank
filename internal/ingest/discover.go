// Package ingest walks source directories and drives the fotobank
// import pipeline. Discover classifies files; importer.go wires it to
// the storage + media layers.
package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wesm/fotobank/internal/media"
)

// Candidate is one file the import pipeline plans to ingest.
type Candidate struct {
	Path     string // absolute source path
	Type     media.Type
	MimeType string
}

// Discover walks root and invokes visit for every supported file. Non-
// supported files are skipped silently; errors from visit abort the
// walk. The root is resolved to an absolute path before walking, so
// Candidate.Path is always absolute even when callers pass a relative
// root. An empty root is rejected so a caller that forgot to pass one
// does not accidentally scan the process's current working directory.
//
// Filesystem-junk filter: Discover skips macOS AppleDouble files
// (basename "._*"), .DS_Store, Thumbs.db, and desktop.ini regardless
// of extension; AppleDouble resource forks shadow real files with the
// same extension (e.g. "._IMG.JPG" for "IMG.JPG") and would otherwise
// import as 4096-byte non-decodable photos. Walks also skip the
// well-known macOS/Windows system directories (.Trashes, .fseventsd,
// .Spotlight-V100, .DocumentRevisions-V100, .TemporaryItems,
// $RECYCLE.BIN) so an SD-card import doesn't recurse into them.
func Discover(root string, visit func(Candidate) error) error {
	if root == "" {
		return fmt.Errorf("discover: root is empty")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve root: %w", err)
	}
	return filepath.WalkDir(absRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			// Don't filter the root itself even if its basename happens
			// to match (the user explicitly asked to scan it).
			if p != absRoot && skipDir(name) {
				return filepath.SkipDir
			}
			return nil
		}
		if skipFile(name) {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		t, mime, ok := classify(ext)
		if !ok {
			return nil
		}
		return visit(Candidate{Path: p, Type: t, MimeType: mime})
	})
}

// skipFile reports whether a regular file's basename identifies it as
// filesystem metadata that should never be ingested as media.
// AppleDouble files (basename "._*") are macOS resource forks that
// Finder writes as siblings when copying to non-HFS+ filesystems; they
// share the original's extension but contain only Mach metadata.
func skipFile(name string) bool {
	if strings.HasPrefix(name, "._") {
		return true
	}
	switch name {
	case ".DS_Store", "Thumbs.db", "desktop.ini":
		return true
	}
	return false
}

// skipDir reports whether a directory basename identifies it as a
// macOS or Windows system directory that should not be recursed into.
func skipDir(name string) bool {
	switch name {
	case ".Trashes", ".Spotlight-V100", ".fseventsd",
		".DocumentRevisions-V100", ".TemporaryItems",
		"$RECYCLE.BIN", "System Volume Information":
		return true
	}
	return false
}

func classify(ext string) (media.Type, string, bool) {
	switch ext {
	case ".jpg", ".jpeg":
		return media.TypePhoto, "image/jpeg", true
	case ".png":
		return media.TypePhoto, "image/png", true
	case ".gif":
		return media.TypePhoto, "image/gif", true
	case ".heic":
		return media.TypePhoto, "image/heic", true
	case ".arw":
		return media.TypePhoto, "image/x-sony-arw", true
	case ".raf":
		return media.TypePhoto, "image/x-fuji-raf", true
	case ".dng":
		return media.TypePhoto, "image/x-adobe-dng", true
	case ".cr2":
		return media.TypePhoto, "image/x-canon-cr2", true
	case ".nef":
		return media.TypePhoto, "image/x-nikon-nef", true
	case ".mp4":
		return media.TypeVideo, "video/mp4", true
	case ".mov":
		return media.TypeVideo, "video/quicktime", true
	case ".m4v":
		return media.TypeVideo, "video/x-m4v", true
	case ".avi":
		return media.TypeVideo, "video/x-msvideo", true
	case ".mpg", ".mp2":
		return media.TypeVideo, "video/mpeg", true
	}
	return "", "", false
}
