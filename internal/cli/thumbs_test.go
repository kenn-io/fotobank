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

	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
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
		p.Hub, p.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC(),
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

func writeNonStubConfig(t *testing.T, tmp string) string {
	t.Helper()
	cfgPath := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[identity]
mode = "header"
`, filepath.Join(tmp, "nas"), filepath.Join(tmp, "flash")), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "nas"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "flash"), 0o700))
	return cfgPath
}

func seedRowForOwner(t *testing.T, dbPath string, p owners.Principal) media.Media {
	t.Helper()
	d, err := db.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = d.Close() }()
	_, err = d.WriteDB().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID,
		uuid.NewSHA1(uuid.NameSpaceOID, []byte(p.Hub+"\x00"+p.UserID)).String(),
		time.Now().UTC(),
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

func TestThumbsRegenerateOwnerScopeOnlyTouchesThatOwner(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	var out, eout bytes.Buffer
	r.Equal(0, cli.RunContext(context.Background(),
		[]string{"owners", "list"}, &out, &eout))
	alice := owners.Principal{Hub: "local", UserID: "alice"}
	bob := owners.Principal{Hub: "local", UserID: "bob"}
	mAlice := seedRowForOwner(t, dbPath, alice)
	mBob := seedRowForOwner(t, dbPath, bob)

	out.Reset()
	eout.Reset()
	code := cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate",
			"--owner", "local:alice", "--all", "--config", cfgPath},
		&out, &eout)
	r.Equal(0, code, "stderr=%s stdout=%s", eout.String(), out.String())
	r.Contains(out.String(), "1 rows enqueued for local:alice")

	d, err := db.Open(dbPath)
	r.NoError(err)
	defer func() { _ = d.Close() }()
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	gotA, err := repo.GetByID(context.Background(), mAlice.ID)
	r.NoError(err)
	r.Equal("pending", gotA.ThumbStatus)
	r.Equal(3, gotA.ThumbVersion)
	gotB, err := repo.GetByID(context.Background(), mBob.ID)
	r.NoError(err)
	r.Equal("ready", gotB.ThumbStatus, "bob's row must be untouched")
	r.Equal(2, gotB.ThumbVersion)
}

func TestThumbsRegenerateAllOwnersTouchesEveryRow(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	var out, eout bytes.Buffer
	r.Equal(0, cli.RunContext(context.Background(),
		[]string{"owners", "list"}, &out, &eout))
	owners3 := []owners.Principal{
		{Hub: "local", UserID: "alice"},
		{Hub: "local", UserID: "bob"},
		{Hub: "local", UserID: "carol"},
	}
	var rows []media.Media
	for _, p := range owners3 {
		rows = append(rows, seedRowForOwner(t, dbPath, p))
		rows = append(rows, seedRowForOwner(t, dbPath, p))
	}

	out.Reset()
	eout.Reset()
	code := cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate", "--all-owners", "--all",
			"--config", cfgPath}, &out, &eout)
	r.Equal(0, code, "stderr=%s stdout=%s", eout.String(), out.String())
	r.Contains(out.String(), "local:alice")
	r.Contains(out.String(), "local:bob")
	r.Contains(out.String(), "local:carol")

	d, err := db.Open(dbPath)
	r.NoError(err)
	defer func() { _ = d.Close() }()
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	for _, m := range rows {
		got, err := repo.GetByID(context.Background(), m.ID)
		r.NoError(err)
		r.Equal("pending", got.ThumbStatus, "row %s", m.ID)
		r.Equal(3, got.ThumbVersion, "row %s", m.ID)
	}
}

func TestThumbsRegenerateOwnerScopeWithoutContentSelectorErrors(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))

	var out, eout bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate", "--owner", "local:alice",
			"--config", cfgPath}, &out, &eout)
	r.Equal(2, code)
	r.Contains(eout.String(), "at least one")

	out.Reset()
	eout.Reset()
	code = cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate", "--all-owners",
			"--config", cfgPath}, &out, &eout)
	r.Equal(2, code)
	r.Contains(eout.String(), "at least one")
}

func TestThumbsRegenerateMalformedOwnerErrors(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))

	var out, eout bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate", "--owner", "alice", "--all",
			"--config", cfgPath}, &out, &eout)
	r.Equal(2, code)
	r.Contains(eout.String(), "owner must be hub:user")
}

func TestThumbsRegenerateOwnerAndAllOwnersAreMutuallyExclusive(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))

	var out, eout bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate",
			"--owner", "local:alice", "--all-owners", "--all",
			"--config", cfgPath}, &out, &eout)
	r.Equal(2, code)
	r.Contains(eout.String(), "mutually exclusive")
}

func TestThumbsRegenerateOwnerScopeBypassesStubModeRequirement(t *testing.T) {
	// --owner / --all-owners are admin maintenance modes that must
	// work outside identity.mode=stub. The default (no scope flag)
	// still requires stub.
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeNonStubConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	// Trigger migrations.
	var out, eout bytes.Buffer
	r.Equal(0, cli.RunContext(context.Background(),
		[]string{"owners", "list"}, &out, &eout))

	out.Reset()
	eout.Reset()
	code := cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate", "--all", "--config", cfgPath},
		&out, &eout)
	r.Equal(2, code, "default scope without stub must error")
	r.Contains(eout.String(), "stub")

	out.Reset()
	eout.Reset()
	code = cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate", "--owner", "local:alice", "--all",
			"--config", cfgPath}, &out, &eout)
	// 0 rows because no owner exists; the bypass behavior is the
	// thing under test, not the row count.
	r.Equal(0, code, "stderr=%s stdout=%s", eout.String(), out.String())
}

func TestThumbsRegenerateBadSinceErrorsBeforeOpeningDB(t *testing.T) {
	// `--since bad-date` must be rejected upfront, before any DB file
	// is created or migrated. Otherwise a typo on a fresh box silently
	// materializes the SQLite file, runs migrations, and only then
	// reports the usage error — confusing under cron-style invocations
	// (and a real bug in F1).
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	var out, eout bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate", "--all-owners",
			"--since", "not-a-date", "--config", cfgPath},
		&out, &eout)
	r.Equal(2, code)
	r.Contains(eout.String(), "--since must be RFC3339")
	_, statErr := os.Stat(dbPath)
	r.Truef(os.IsNotExist(statErr),
		"DB must not be created when --since fails to parse: stat=%v", statErr)
}
