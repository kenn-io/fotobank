package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/db"
)

// TestRegisterSqliteVec_LoadsExtension proves that calling
// RegisterSqliteVec() before the first sql.Open registers the
// sqlite-vec extension on every subsequent connection. The smoke test
// is `SELECT vec_version()`, which is only available when the
// extension is loaded.
func TestRegisterSqliteVec_LoadsExtension(t *testing.T) {
	t.Helper()
	db.RegisterSqliteVec()

	dsn := "file:" + filepath.Join(t.TempDir(), "vec_smoke.sqlite") +
		"?_busy_timeout=5000&_fk=1"
	conn, err := sql.Open("sqlite3", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	var version string
	err = conn.QueryRow(`SELECT vec_version()`).Scan(&version)
	require.NoError(t, err, "vec_version() must work after RegisterSqliteVec")
	require.NotEmpty(t, version)
}

// TestRegisterSqliteVec_Idempotent proves that calling
// RegisterSqliteVec twice is a no-op.
func TestRegisterSqliteVec_Idempotent(t *testing.T) {
	db.RegisterSqliteVec()
	db.RegisterSqliteVec()
}
