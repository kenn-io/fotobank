package testutil

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/owners"
)

// OpenTestDBPair returns the RW and RO *sql.DB handles from a fresh
// migrated test database. Adapter over OpenTestDB so test code can
// dual-use the two pools without poking at the wrapper.
func OpenTestDBPair(t *testing.T) (*sql.DB, *sql.DB) {
	t.Helper()
	d := OpenTestDB(t)
	return d.WriteDB(), d.ReadDB()
}

// SeedOwner inserts a row into owners and returns the principal.
func SeedOwner(t *testing.T, rw *sql.DB, hub, user string) owners.Principal {
	t.Helper()
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES (?,?,?,?)`,
		hub, user, hub+"/"+user, time.Now().UTC())
	require.NoError(t, err)
	return owners.Principal{Hub: hub, UserID: user}
}

// SeedPhoto inserts a minimal photo row and returns its ID.
func SeedPhoto(t *testing.T, rw *sql.DB, p owners.Principal, label string) string {
	t.Helper()
	id := uuid.NewString()
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO media(id, owner_hub, owner_user_id, media_type, mime_type, path,
		 imported_at, size, checksum, thumb_status, thumb_version, import_source_path)
		 VALUES (?,?,?, 'photo','image/jpeg', ?, ?, 0, ?, 'ready', 1, ?)`,
		id, p.Hub, p.UserID, "/photos/"+label+".jpg", time.Now().UTC(), label+"-checksum", label)
	require.NoError(t, err)
	return id
}
