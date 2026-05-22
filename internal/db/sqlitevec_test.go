package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/db"
)

// TestRegisterSqliteVec_LoadsExtension proves that calling
// RegisterSqliteVec() before the first sql.Open registers the
// sqlite-vec extension on every subsequent connection. The smoke test
// is `SELECT vec_version()`, which is only available when the
// extension is loaded.
func TestRegisterSqliteVec_LoadsExtension(t *testing.T) {
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
// RegisterSqliteVec twice is a no-op: a sync.Once guards
// sqlite_vec.Auto so the second call must not return a "extension
// already registered" error from sqlite3, AND a fresh connection
// opened afterward must still see the extension. The post-second-call
// `SELECT vec_version()` smoke is the assertion that catches any
// future regression where the registration is lost (or doubled into
// an error) on the second call.
func TestRegisterSqliteVec_Idempotent(t *testing.T) {
	db.RegisterSqliteVec()
	db.RegisterSqliteVec()

	dsn := "file:" + filepath.Join(t.TempDir(), "vec_idem.sqlite") +
		"?_busy_timeout=5000&_fk=1"
	conn, err := sql.Open("sqlite3", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	var version string
	err = conn.QueryRow(`SELECT vec_version()`).Scan(&version)
	require.NoError(t, err, "vec_version() must still work after second RegisterSqliteVec call")
	require.NotEmpty(t, version)
}
