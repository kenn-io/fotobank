package content

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPathsOverlapRejectsCaseOnlyAliases(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "archive", "Docbank")
	alias := filepath.Join(string(filepath.Separator), "ARCHIVE", "docbank", "imports")
	require.True(t, pathsOverlap(root, alias))
}
