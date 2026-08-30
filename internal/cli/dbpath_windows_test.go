//go:build windows

package cli

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalDBPathAcceptsForwardSlashes(t *testing.T) {
	r := require.New(t)
	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(root)
	r.NoError(err)

	got, err := canonicalDBPath(filepath.ToSlash(filepath.Join(root, "missing", "fotobank.sqlite")))
	r.NoError(err)
	r.Equal(filepath.Join(resolvedRoot, "missing", "fotobank.sqlite"), got)
}
