package db_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/db"
)

func TestInitialSchemaCreatesAllTables(t *testing.T) {
	r := require.New(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "s.sqlite"))
	r.NoError(err)
	defer d.Close()

	tables := []string{
		"owners", "principal_display", "media",
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
	mustExec(`INSERT INTO owners VALUES('h1','u1','k1','u1',datetime('now'))`)
	mustExec(`INSERT INTO owners VALUES('h2','u2','k2','u2',datetime('now'))`)
	mustExec(`INSERT INTO albums (id,owner_hub,owner_user_id,name,created_at,updated_at)
	          VALUES('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa','h1','u1','a',datetime('now'),datetime('now'))`)
	mustExec(`INSERT INTO media (id,owner_hub,owner_user_id,media_type,mime_type,path,imported_at,size,checksum,thumb_status,thumb_version,thumb_updated_at)
	          VALUES('bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb','h2','u2','photo','image/jpeg','a.jpg',datetime('now'),1,'cs','pending',1,datetime('now'))`)

	_, err = rw.Exec(
		`INSERT INTO album_media VALUES('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa','bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb',datetime('now'),NULL)`,
	)
	r.Error(err)
	r.Contains(err.Error(), "album and media must share owner")
}
