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
// root.
func Discover(root string, visit func(Candidate) error) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve root: %w", err)
	}
	return filepath.WalkDir(absRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
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
