package testutil

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/content"
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
		hub, user, uuid.NewSHA1(uuid.NameSpaceOID, []byte(hub+"\x00"+user)).String(), time.Now().UTC())
	require.NoError(t, err)
	return owners.Principal{Hub: hub, UserID: user}
}

// SeedPhoto inserts a minimal photo row and returns its ID.
func SeedPhoto(t *testing.T, rw *sql.DB, p owners.Principal, label string) string {
	t.Helper()
	id := uuid.NewString()
	ctx := context.Background()
	_, err := rw.ExecContext(ctx,
		`INSERT INTO assets(id, owner_hub, owner_user_id, state, media_type,
		 imported_at, thumb_status, thumb_version)
		 VALUES (?,?,?, 'pending','photo', ?, 'ready', 1)`,
		id, p.Hub, p.UserID, time.Now().UTC())
	require.NoError(t, err)
	var storageKey string
	require.NoError(t, rw.QueryRowContext(ctx,
		`SELECT storage_key FROM owners WHERE hub=? AND user_id=?`, p.Hub, p.UserID,
	).Scan(&storageKey))
	fileID := uuid.NewString()
	virtualPath, err := content.VirtualPath(storageKey, fileID, label+".jpg")
	require.NoError(t, err)
	digest := sha256.Sum256([]byte(label))
	_, err = rw.ExecContext(ctx,
		`INSERT INTO media_files(id, asset_id, owner_hub, owner_user_id, role,
		 mime_type, original_filename, size, docbank_node_id, docbank_virtual_path,
		 current_version_id, sha256)
		 VALUES (?,?,?,?, 'primary','image/jpeg', ?, 0, ?, ?, ?, ?)`,
		fileID, id, p.Hub, p.UserID, label+".jpg", NextSyntheticDocbankNodeID(), virtualPath,
		uuid.NewString(), hex.EncodeToString(digest[:]))
	require.NoError(t, err)
	_, err = rw.ExecContext(ctx, `UPDATE assets SET state='ready' WHERE id=?`, id)
	require.NoError(t, err)
	return id
}
