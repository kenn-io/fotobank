// Package content owns Fotobank's boundary with the authoritative Docbank vault.
package content

import (
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.kenn.io/fotobank/internal/errs"
	"golang.org/x/text/unicode/norm"
)

// VirtualPath returns the stable Docbank path for one media file.
func VirtualPath(ownerStorageKey, fileID, originalBasename string) (string, error) {
	if err := validateCanonicalUUID("owner storage key", ownerStorageKey); err != nil {
		return "", err
	}
	if err := validateCanonicalUUID("file ID", fileID); err != nil {
		return "", err
	}
	if !utf8.ValidString(originalBasename) || originalBasename == "" ||
		originalBasename == "." || originalBasename == ".." ||
		strings.ContainsAny(originalBasename, "/\\\x00") {
		return "", fmt.Errorf("%w: invalid original basename", errs.ErrInvalidArgument)
	}

	basename := norm.NFC.String(originalBasename)
	return path.Join("/owners", ownerStorageKey, "media", fileID, basename), nil
}

// OwnerMediaRoot returns the subtree that contains one owner's authoritative
// media files.
func OwnerMediaRoot(ownerStorageKey string) (string, error) {
	if err := validateCanonicalUUID("owner storage key", ownerStorageKey); err != nil {
		return "", err
	}
	return path.Join("/owners", ownerStorageKey, "media"), nil
}

func validateCanonicalUUID(name, value string) error {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value {
		return fmt.Errorf("%w: %s must be a canonical lowercase UUID", errs.ErrInvalidArgument, name)
	}
	return nil
}
