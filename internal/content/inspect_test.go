package content_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/content"
)

func TestInspectVaultReadsExistingLayoutWithoutInitializing(t *testing.T) {
	r := require.New(t)
	parent := t.TempDir()
	missing := filepath.Join(parent, "missing")

	err := content.InspectVault(missing)
	r.Error(err)
	r.NoDirExists(missing)

	root := filepath.Join(parent, "vault")
	adapter, err := content.Open(t.Context(), content.Config{Root: root})
	r.NoError(err)
	r.NoError(adapter.Close())
	r.NoError(content.InspectVault(root))
}
