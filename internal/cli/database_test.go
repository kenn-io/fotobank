package cli

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/backup"
	"go.kenn.io/fotobank/internal/db"
)

func TestDatabaseHandleBlocksRestore(t *testing.T) {
	r := require.New(t)
	dbPath := filepath.Join(t.TempDir(), "fotobank.sqlite")
	database, err := db.Open(dbPath)
	r.NoError(err)
	r.NoError(database.Close())
	snapshotPath := filepath.Join(t.TempDir(), "snapshot.sqlite")
	r.NoError(backup.SnapshotPath(t.Context(), dbPath, snapshotPath))

	handle, err := openDatabasePath(dbPath)
	r.NoError(err)
	t.Cleanup(func() { r.NoError(handle.Close()) })

	_, err = backup.Restore(t.Context(), snapshotPath, dbPath, lockPathFor(handle.Path()))
	r.ErrorIs(err, backup.ErrServerHoldsLock)
}
