package cli

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gofrs/flock"
	"go.kenn.io/fotobank/internal/db"
)

func TestDatabaseHandleRetainsLifetimeLock(t *testing.T) {
	r := require.New(t)
	dbPath := filepath.Join(t.TempDir(), "fotobank.sqlite")
	database, err := db.Open(dbPath)
	r.NoError(err)
	r.NoError(database.Close())
	handle, err := openDatabasePath(dbPath)
	r.NoError(err)
	t.Cleanup(func() { r.NoError(handle.Close()) })

	lock := flock.New(lockPathFor(handle.Path()))
	acquired, err := lock.TryLock()
	r.NoError(err)
	r.False(acquired)
	r.NoError(handle.Close())
	acquired, err = lock.TryLock()
	r.NoError(err)
	r.True(acquired)
	r.NoError(lock.Unlock())
}
