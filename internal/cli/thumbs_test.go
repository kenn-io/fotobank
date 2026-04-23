package cli_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/cli"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
)

func writeBasicConfig(t *testing.T, tmp string) string {
	t.Helper()
	cfgPath := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[identity]
mode = "stub"
[identity.stub]
hub = "h"
user_id = "u"
`, filepath.Join(tmp, "nas"), filepath.Join(tmp, "flash")), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "nas"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "flash"), 0o700))
	return cfgPath
}

func seedReadyRow(t *testing.T, dbPath string) media.Media {
	t.Helper()
	d, err := db.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = d.Close() }()
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err = d.WriteDB().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "u", time.Now().UTC(),
	)
	require.NoError(t, err)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	m := media.Media{
		ID: uuid.NewString(), Owner: p, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x-" + uuid.NewString() + ".jpg", ImportedAt: time.Now().UTC(),
		Size: 1, Checksum: uuid.NewString(), ThumbStatus: "ready", ThumbVersion: 2,
	}
	require.NoError(t, repo.Insert(context.Background(), m))
	return m
}

func TestThumbsRegenerateAllBumpsVersion(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	// First invocation triggers migrations. owners subcommands honor
	// FOTOBANK_CONFIG (set above) rather than --config.
	var out, eout bytes.Buffer
	r.Equal(0, cli.RunContext(context.Background(),
		[]string{"owners", "list"}, &out, &eout),
		"stderr=%s stdout=%s", eout.String(), out.String())
	m := seedReadyRow(t, dbPath)

	out.Reset()
	eout.Reset()
	code := cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate", "--all", "--config", cfgPath}, &out, &eout)
	r.Equal(0, code, "stderr=%s stdout=%s", eout.String(), out.String())
	r.Contains(out.String(), "1 rows enqueued")

	// Confirm version bumped.
	d, err := db.Open(dbPath)
	r.NoError(err)
	defer func() { _ = d.Close() }()
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	got, err := repo.GetByID(context.Background(), m.ID)
	r.NoError(err)
	r.Equal(3, got.ThumbVersion)
	r.Equal("pending", got.ThumbStatus)
}

func TestThumbsRegenerateWithNoSelectorErrors(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))

	var out, eout bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate", "--config", cfgPath}, &out, &eout)
	r.Equal(2, code)
	r.Contains(eout.String(), "at least one")
}

func TestThumbsRegenerateByIDTargetsOnlyMatch(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	// Trigger migrations before seeding rows. owners subcommands honor
	// FOTOBANK_CONFIG (set above) rather than --config.
	var out, eout bytes.Buffer
	_ = cli.RunContext(context.Background(),
		[]string{"owners", "list"}, &out, &eout)

	m1 := seedReadyRow(t, dbPath)
	m2 := seedReadyRow(t, dbPath)

	out.Reset()
	eout.Reset()
	code := cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate", "--id", m1.ID, "--config", cfgPath}, &out, &eout)
	r.Equal(0, code, "stderr=%s stdout=%s", eout.String(), out.String())
	r.Contains(out.String(), "1 rows enqueued")

	d, err := db.Open(dbPath)
	r.NoError(err)
	defer func() { _ = d.Close() }()
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	got1, err := repo.GetByID(context.Background(), m1.ID)
	r.NoError(err)
	r.Equal("pending", got1.ThumbStatus)

	got2, err := repo.GetByID(context.Background(), m2.ID)
	r.NoError(err)
	r.Equal("ready", got2.ThumbStatus)
}
