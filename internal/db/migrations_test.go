package db_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil"
)

type schemaFileMapping struct {
	nodeID      any
	virtualPath any
	versionID   any
	sha256      any
}

func openAssetSchemaDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "asset-schema.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	return d
}

func insertSchemaOwner(t *testing.T, rw *sql.DB, hub, userID, storageKey string) {
	t.Helper()
	_, err := rw.ExecContext(t.Context(), `INSERT INTO owners
		(hub, user_id, storage_key, display_handle, created_at)
		VALUES (?, ?, ?, NULL, datetime('now'))`, hub, userID, storageKey)
	require.NoError(t, err)
}

func insertSchemaAsset(
	t *testing.T,
	rw *sql.DB,
	id, hub, userID, state string,
) error {
	t.Helper()
	_, err := rw.ExecContext(t.Context(), `INSERT INTO assets
		(id, owner_hub, owner_user_id, state, media_type, imported_at, thumb_status)
		VALUES (?, ?, ?, ?, 'photo', datetime('now'), 'pending')`,
		id, hub, userID, state)
	return err
}

func insertSchemaFile(
	t *testing.T,
	rw *sql.DB,
	id, assetID, hub, userID, role string,
	mapping schemaFileMapping,
) error {
	t.Helper()
	_, err := rw.ExecContext(t.Context(), `INSERT INTO media_files
		(id, asset_id, owner_hub, owner_user_id, role, mime_type,
		 original_filename, size, docbank_node_id, docbank_virtual_path,
		 current_version_id, sha256)
		VALUES (?, ?, ?, ?, ?, 'image/jpeg', 'file.jpg', 1, ?, ?, ?, ?)`,
		id, assetID, hub, userID, role, mapping.nodeID, mapping.virtualPath,
		mapping.versionID, mapping.sha256)
	return err
}

func validSchemaMapping(fileID string, nodeID int64, fill string) schemaFileMapping {
	return schemaFileMapping{
		nodeID:      nodeID,
		virtualPath: "/owners/550e8400-e29b-41d4-a716-446655440000/media/" + fileID + "/file.jpg",
		versionID:   fill + fill + fill + fill + fill + fill + fill + fill + "-" + fill + fill + fill + fill + "-4" + fill + fill + fill + "-8" + fill + fill + fill + "-" + strings.Repeat(fill, 12),
		sha256:      strings.Repeat(fill, 64),
	}
}

func TestSchemaOwnerStorageKey(t *testing.T) {
	tests := map[string]string{
		"empty":            "",
		"short":            "550e8400",
		"uppercase":        "550E8400-E29B-41D4-A716-446655440000",
		"non-hex":          "550e8400-e29b-41d4-a716-44665544000g",
		"misplaced hyphen": "550e840-0e29b-41d4-a716-446655440000",
		"extra suffix":     "550e8400-e29b-41d4-a716-446655440000-x",
	}
	for name, storageKey := range tests {
		t.Run(name, func(t *testing.T) {
			d := openAssetSchemaDB(t)
			_, err := d.WriteDB().ExecContext(t.Context(), `INSERT INTO owners
				(hub, user_id, storage_key, created_at)
				VALUES ('hub', 'owner', ?, datetime('now'))`, storageKey)
			require.Error(t, err)
		})
	}

	d := openAssetSchemaDB(t)
	insertSchemaOwner(t, d.WriteDB(), "hub", "owner", "550e8400-e29b-41d4-a716-446655440000")
}

func TestSchemaUsesAssetGraphWithoutLegacyMediaTable(t *testing.T) {
	r := require.New(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "asset-schema.sqlite"))
	r.NoError(err)
	defer d.Close()

	rows, err := d.ReadDB().Query(`
		SELECT name
		FROM sqlite_master
		WHERE type = 'table'
		  AND name IN ('media', 'assets', 'media_files',
		               'media_file_relationships')
		ORDER BY name`)
	r.NoError(err)
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		r.NoError(rows.Scan(&name))
		tables = append(tables, name)
	}
	r.NoError(rows.Err())
	r.Equal([]string{"assets", "media_file_relationships", "media_files"}, tables)

	columns, err := d.ReadDB().Query(`PRAGMA table_info(media_files)`)
	r.NoError(err)
	defer columns.Close()
	mappingColumns := map[string]bool{
		"docbank_node_id":      false,
		"docbank_virtual_path": false,
		"current_version_id":   false,
		"sha256":               false,
	}
	for columns.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		r.NoError(columns.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey))
		if _, ok := mappingColumns[name]; ok {
			mappingColumns[name] = true
		}
	}
	r.NoError(columns.Err())
	for name, present := range mappingColumns {
		r.True(present, "mapping column %q missing", name)
	}
}

func TestSchemaAssetExactlyOnePrimary(t *testing.T) {
	d := openAssetSchemaDB(t)
	rw := d.WriteDB()
	insertSchemaOwner(t, rw, "hub", "owner", "550e8400-e29b-41d4-a716-446655440000")
	require.NoError(t, insertSchemaAsset(t, rw,
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "hub", "owner", "pending"))
	require.NoError(t, insertSchemaFile(t, rw,
		"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "hub", "owner", "primary",
		schemaFileMapping{}))

	err := insertSchemaFile(t, rw,
		"cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "hub", "owner", "primary",
		schemaFileMapping{})
	require.Error(t, err)
}

func TestSchemaAssetReadyInvariant(t *testing.T) {
	const (
		assetID      = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		primaryID    = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		secondaryID  = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
		additionalID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
		storageKey   = "550e8400-e29b-41d4-a716-446655440000"
	)

	makeReady := func(t *testing.T) (*db.DB, *sql.DB) {
		t.Helper()
		d := openAssetSchemaDB(t)
		rw := d.WriteDB()
		insertSchemaOwner(t, rw, "hub", "owner", storageKey)
		require.NoError(t, insertSchemaAsset(t, rw, assetID, "hub", "owner", "pending"))
		require.NoError(t, insertSchemaFile(t, rw, primaryID, assetID,
			"hub", "owner", "primary", validSchemaMapping(primaryID, 1, "1")))
		_, err := rw.ExecContext(t.Context(),
			`UPDATE assets SET state = 'ready' WHERE id = ?`, assetID)
		require.NoError(t, err)
		return d, rw
	}

	t.Run("ready insert starts pending", func(t *testing.T) {
		d := openAssetSchemaDB(t)
		rw := d.WriteDB()
		insertSchemaOwner(t, rw, "hub", "owner", storageKey)
		err := insertSchemaAsset(t, rw, assetID, "hub", "owner", "ready")
		require.ErrorContains(t, err, "asset graphs start pending")
	})

	t.Run("ready requires one primary", func(t *testing.T) {
		d := openAssetSchemaDB(t)
		rw := d.WriteDB()
		insertSchemaOwner(t, rw, "hub", "owner", storageKey)
		require.NoError(t, insertSchemaAsset(t, rw, assetID, "hub", "owner", "pending"))
		_, err := rw.ExecContext(t.Context(),
			`UPDATE assets SET state = 'ready' WHERE id = ?`, assetID)
		require.ErrorContains(t, err, "ready asset requires exactly one primary")
	})

	t.Run("ready requires mapped files", func(t *testing.T) {
		d := openAssetSchemaDB(t)
		rw := d.WriteDB()
		insertSchemaOwner(t, rw, "hub", "owner", storageKey)
		require.NoError(t, insertSchemaAsset(t, rw, assetID, "hub", "owner", "pending"))
		require.NoError(t, insertSchemaFile(t, rw, primaryID, assetID,
			"hub", "owner", "primary", schemaFileMapping{}))
		_, err := rw.ExecContext(t.Context(),
			`UPDATE assets SET state = 'ready' WHERE id = ?`, assetID)
		require.ErrorContains(t, err, "ready asset requires mapped files")
	})

	t.Run("ready keeps primary", func(t *testing.T) {
		_, rw := makeReady(t)
		_, err := rw.ExecContext(t.Context(),
			`DELETE FROM media_files WHERE id = ?`, primaryID)
		require.ErrorContains(t, err, "cannot remove primary from ready asset")
		_, err = rw.ExecContext(t.Context(),
			`UPDATE media_files SET role = 'original' WHERE id = ?`, primaryID)
		require.ErrorContains(t, err, "cannot remove primary from ready asset")
	})

	t.Run("file asset and owner are immutable", func(t *testing.T) {
		_, rw := makeReady(t)
		for name, query := range map[string]string{
			"asset": `UPDATE media_files SET asset_id =
				'ffffffff-ffff-4fff-8fff-ffffffffffff' WHERE id = ?`,
			"hub":  `UPDATE media_files SET owner_hub = 'other' WHERE id = ?`,
			"user": `UPDATE media_files SET owner_user_id = 'other' WHERE id = ?`,
		} {
			t.Run(name, func(t *testing.T) {
				_, err := rw.ExecContext(t.Context(), query, primaryID)
				require.ErrorContains(t, err, "file asset and owner are immutable")
			})
		}
	})

	t.Run("ready rejects an unmapped new file", func(t *testing.T) {
		_, rw := makeReady(t)
		require.NoError(t, insertSchemaFile(t, rw, secondaryID, assetID,
			"hub", "owner", "original", validSchemaMapping(secondaryID, 2, "2")))
		err := insertSchemaFile(t, rw, additionalID, assetID,
			"hub", "owner", "sidecar", schemaFileMapping{})
		require.ErrorContains(t, err, "ready asset requires mapped files")
	})

	t.Run("ready mappings advance but cannot be cleared", func(t *testing.T) {
		_, rw := makeReady(t)
		advanced := validSchemaMapping(primaryID, 2, "2")
		_, err := rw.ExecContext(t.Context(), `UPDATE media_files
			SET docbank_node_id = ?, docbank_virtual_path = ?,
			    current_version_id = ?, sha256 = ? WHERE id = ?`,
			advanced.nodeID, advanced.virtualPath, advanced.versionID,
			advanced.sha256, primaryID)
		require.NoError(t, err)

		_, err = rw.ExecContext(t.Context(), `UPDATE media_files
			SET docbank_node_id = NULL, docbank_virtual_path = NULL,
			    current_version_id = NULL, sha256 = NULL WHERE id = ?`, primaryID)
		require.ErrorContains(t, err, "ready asset requires mapped files")
	})
}

func TestSchemaMediaFileRelationshipConsistency(t *testing.T) {
	r := require.New(t)
	const (
		assetOne   = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		assetTwo   = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		assetThree = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
		fileOne    = "11111111-1111-4111-8111-111111111111"
		fileTwo    = "22222222-2222-4222-8222-222222222222"
		fileThree  = "33333333-3333-4333-8333-333333333333"
		fileFour   = "44444444-4444-4444-8444-444444444444"
	)
	d := openAssetSchemaDB(t)
	rw := d.WriteDB()
	insertSchemaOwner(t, rw, "hub", "owner-one", "550e8400-e29b-41d4-a716-446655440000")
	insertSchemaOwner(t, rw, "hub", "owner-two", "550e8400-e29b-41d4-a716-446655440001")
	r.NoError(insertSchemaAsset(t, rw, assetOne, "hub", "owner-one", "pending"))
	r.NoError(insertSchemaAsset(t, rw, assetTwo, "hub", "owner-one", "pending"))
	r.NoError(insertSchemaAsset(t, rw, assetThree, "hub", "owner-two", "pending"))
	r.NoError(insertSchemaFile(t, rw, fileOne, assetOne,
		"hub", "owner-one", "primary", schemaFileMapping{}))
	r.NoError(insertSchemaFile(t, rw, fileTwo, assetOne,
		"hub", "owner-one", "sidecar", schemaFileMapping{}))
	r.NoError(insertSchemaFile(t, rw, fileThree, assetTwo,
		"hub", "owner-one", "primary", schemaFileMapping{}))
	r.NoError(insertSchemaFile(t, rw, fileFour, assetThree,
		"hub", "owner-two", "primary", schemaFileMapping{}))

	_, err := rw.ExecContext(t.Context(), `INSERT INTO media_file_relationships
		(source_file_id, target_file_id, kind) VALUES (?, ?, 'sidecar_of')`,
		fileTwo, fileOne)
	r.NoError(err)

	_, err = rw.ExecContext(t.Context(), `INSERT INTO media_file_relationships
		(source_file_id, target_file_id, kind) VALUES (?, ?, 'paired_with')`,
		fileTwo, fileThree)
	r.ErrorContains(err, "relationship files must share asset and owner")

	_, err = rw.ExecContext(t.Context(), `INSERT INTO media_file_relationships
		(source_file_id, target_file_id, kind) VALUES (?, ?, 'paired_with')`,
		fileTwo, fileFour)
	r.ErrorContains(err, "relationship files must share asset and owner")

	_, err = rw.ExecContext(t.Context(), `UPDATE media_file_relationships
		SET target_file_id = ?
		WHERE source_file_id = ? AND target_file_id = ? AND kind = 'sidecar_of'`,
		fileThree, fileTwo, fileOne)
	r.ErrorContains(err, "relationship files must share asset and owner")
}

func TestSchemaAssetMappingInvariant(t *testing.T) {
	r := require.New(t)
	const (
		assetID   = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		absentID  = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		validID   = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
		invalidID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	)
	d := openAssetSchemaDB(t)
	rw := d.WriteDB()
	insertSchemaOwner(t, rw, "hub", "owner", "550e8400-e29b-41d4-a716-446655440000")
	r.NoError(insertSchemaAsset(t, rw, assetID, "hub", "owner", "pending"))
	r.NoError(insertSchemaFile(t, rw, absentID, assetID,
		"hub", "owner", "primary", schemaFileMapping{}))
	r.NoError(insertSchemaFile(t, rw, validID, assetID,
		"hub", "owner", "original", validSchemaMapping(validID, 1, "1")))

	valid := validSchemaMapping(invalidID, 99, "f")
	for mask := 1; mask < 15; mask++ {
		t.Run(fmt.Sprintf("partial-%04b", mask), func(t *testing.T) {
			mapping := schemaFileMapping{}
			if mask&1 != 0 {
				mapping.nodeID = valid.nodeID
			}
			if mask&2 != 0 {
				mapping.virtualPath = valid.virtualPath
			}
			if mask&4 != 0 {
				mapping.versionID = valid.versionID
			}
			if mask&8 != 0 {
				mapping.sha256 = valid.sha256
			}
			err := insertSchemaFile(t, rw, invalidID, assetID,
				"hub", "owner", "alternate", mapping)
			r.Error(err)
		})
	}

	for name, nodeID := range map[string]int64{"zero": 0, "negative": -1} {
		t.Run("node-"+name, func(t *testing.T) {
			mapping := valid
			mapping.nodeID = nodeID
			err := insertSchemaFile(t, rw, invalidID, assetID,
				"hub", "owner", "alternate", mapping)
			r.Error(err)
		})
	}

	validPath := valid.virtualPath.(string)
	for name, virtualPath := range map[string]string{
		"empty":              "",
		"relative":           "owners/key/media/" + invalidID + "/file.jpg",
		"backslash":          "/owners/key/media/" + invalidID + `/dir\file.jpg`,
		"repeated-separator": "/owners/key/media/" + invalidID + "//file.jpg",
		"dot-segment":        "/owners/key/media/" + invalidID + "/./file.jpg",
		"dot-dot-segment":    "/owners/key/media/" + invalidID + "/../file.jpg",
		"dot-suffix":         "/owners/key/media/" + invalidID + "/.",
		"dot-dot-suffix":     "/owners/key/media/" + invalidID + "/..",
		"trailing-slash":     "/owners/key/media/" + invalidID + "/",
		"wrong-prefix":       "/archive/key/media/" + invalidID + "/file.jpg",
		"wrong-file-id":      strings.Replace(validPath, invalidID, validID, 1),
	} {
		t.Run("path-"+name, func(t *testing.T) {
			mapping := valid
			mapping.virtualPath = virtualPath
			err := insertSchemaFile(t, rw, invalidID, assetID,
				"hub", "owner", "alternate", mapping)
			r.Error(err)
		})
	}

	validVersion := valid.versionID.(string)
	for name, versionID := range map[string]string{
		"empty":        "",
		"noncanonical": "ffffffffffff4fff8fffffffffffffff",
		"non-v4":       "ffffffff-ffff-3fff-8fff-ffffffffffff",
		"uppercase":    strings.ToUpper(validVersion),
	} {
		t.Run("version-"+name, func(t *testing.T) {
			mapping := valid
			mapping.versionID = versionID
			err := insertSchemaFile(t, rw, invalidID, assetID,
				"hub", "owner", "alternate", mapping)
			r.Error(err)
		})
	}

	for name, digest := range map[string]string{
		"empty":     "",
		"short":     strings.Repeat("f", 63),
		"uppercase": strings.Repeat("F", 64),
		"non-hex":   strings.Repeat("g", 64),
	} {
		t.Run("sha256-"+name, func(t *testing.T) {
			mapping := valid
			mapping.sha256 = digest
			err := insertSchemaFile(t, rw, invalidID, assetID,
				"hub", "owner", "alternate", mapping)
			r.Error(err)
		})
	}
}

func TestInitialSchemaCreatesAllTables(t *testing.T) {
	r := require.New(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "s.sqlite"))
	r.NoError(err)
	defer d.Close()

	tables := []string{
		"owners", "principal_display", "assets", "media_files", "media_file_relationships",
		"albums", "album_media",
		"scopes", "scope_media",
	}
	for _, name := range tables {
		var count int
		r.NoError(d.ReadDB().QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name,
		).Scan(&count))
		r.Equal(1, count, "table %q missing", name)
	}
}

func TestSchemaAppSettingsExists(t *testing.T) {
	r := require.New(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "app-settings.sqlite"))
	r.NoError(err)
	defer d.Close()

	var name string
	err = d.ReadDB().QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='app_settings'`,
	).Scan(&name)
	r.NoError(err)
	r.Equal("app_settings", name)
}

func TestAlbumMediaOwnerConsistencyTrigger(t *testing.T) {
	r := require.New(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "t.sqlite"))
	r.NoError(err)
	defer d.Close()

	rw := d.WriteDB()
	mustExec := func(q string, args ...any) { _, err := rw.Exec(q, args...); r.NoError(err) }
	mustExec(`INSERT INTO owners VALUES('h1','u1','550e8400-e29b-41d4-a716-446655440000','u1',datetime('now'))`)
	mustExec(`INSERT INTO owners VALUES('h2','u2','660e8400-e29b-41d4-a716-446655440000','u2',datetime('now'))`)
	mustExec(`INSERT INTO albums (id,owner_hub,owner_user_id,name,created_at,updated_at)
	          VALUES('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa','h1','u1','a',datetime('now'),datetime('now'))`)
	mediaID := testutil.SeedPhoto(t, rw, owners.Principal{Hub: "h2", UserID: "u2"}, "a")

	_, err = rw.Exec(
		`INSERT INTO album_media VALUES('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',?,datetime('now'),NULL)`, mediaID,
	)
	r.Error(err)
	r.Contains(err.Error(), "album and media must share owner")
}
