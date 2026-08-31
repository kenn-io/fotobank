package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/checkout"
	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil/assetfixture"
)

func TestCheckoutEstimateAndCreate(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	database, err := db.Open(dbPath)
	r.NoError(err)
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err = database.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES (?, ?, ?, ?)`,
		owner.Hub, owner.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC())
	r.NoError(err)
	contentStore, err := content.Open(t.Context(), content.Config{
		Root: filepath.Join(tmp, "flash", "docbank"),
	})
	r.NoError(err)
	body := []byte("checkout bytes")
	item := assetfixture.InsertContent(t,
		media.NewRepo(database.WriteDB(), database.ReadDB()), contentStore, body,
		media.Media{Owner: owner, OriginalFilename: "IMG_0100.JPG"})
	r.NoError(contentStore.Close())
	r.NoError(database.Close())

	var stdout, stderr bytes.Buffer
	code := cli.RunContext(t.Context(), []string{
		"checkout", "estimate", "--config", cfgPath, "--asset", item.ID,
	}, &stdout, &stderr)
	r.Equal(0, code, "stderr=%s", stderr.String())
	r.Equal("files=1\tbytes=14\n", stdout.String())

	root := filepath.Join(tmp, "checkout")
	r.NoError(os.Mkdir(root, 0o700))
	databaseReplacement := flock.New(dbPath + ".lock")
	locked, err := databaseReplacement.TryLock()
	r.NoError(err)
	r.True(locked)
	stdout.Reset()
	stderr.Reset()
	code = cli.RunContext(t.Context(), []string{
		"checkout", "estimate", "--config", cfgPath, "--asset", item.ID,
	}, &stdout, &stderr)
	r.NotZero(code)
	r.Contains(stderr.String(), "another process is replacing the database")
	stdout.Reset()
	stderr.Reset()
	code = cli.RunContext(t.Context(), []string{
		"checkout", "create", "--config", cfgPath, "--asset", item.ID, root,
	}, &stdout, &stderr)
	r.NotZero(code)
	r.Contains(stderr.String(), "another process is replacing the database")
	r.NoError(databaseReplacement.Unlock())
	stdout.Reset()
	stderr.Reset()
	code = cli.RunContext(t.Context(), []string{
		"checkout", "create", "--config", cfgPath, "--asset", item.ID, root,
	}, &stdout, &stderr)
	r.Equal(0, code, "stderr=%s", stderr.String())
	r.Contains(stdout.String(), "files=1\tbytes=14")
	got, err := os.ReadFile(filepath.Join(root, "undated", item.ID, "IMG_0100.JPG"))
	r.NoError(err)
	r.Equal(body, got)

	edited := []byte("edited checkout bytes")
	r.NoError(os.WriteFile(
		filepath.Join(root, "undated", item.ID, "IMG_0100.JPG"), edited, 0o600))
	database, err = db.Open(dbPath)
	r.NoError(err)
	contentStore, err = content.Open(t.Context(), content.Config{
		Root: filepath.Join(tmp, "flash", "docbank"),
	})
	r.NoError(err)
	checkoutRepo := checkout.NewRepo(database.WriteDB(), database.ReadDB())
	scanner := checkout.NewScanner(checkoutRepo, contentStore, checkout.ScannerConfig{
		ScanInterval: time.Second, SettleInterval: 0,
	})
	_, err = scanner.Scan(t.Context())
	r.NoError(err)
	_, err = scanner.Scan(t.Context())
	r.NoError(err)
	var checkoutID string
	r.NoError(database.ReadDB().QueryRowContext(t.Context(),
		`SELECT id FROM checkouts WHERE state = 'active'`).Scan(&checkoutID))
	r.NoError(contentStore.Close())
	r.NoError(database.Close())

	stdout.Reset()
	stderr.Reset()
	code = cli.RunContext(t.Context(), []string{
		"checkout", "commit", "--config", cfgPath, checkoutID,
	}, &stdout, &stderr)
	r.Zero(code, "stderr=%s", stderr.String())
	r.Equal("pending=1\tcommitted=1\tconflicts=0\n", stdout.String())
	database, err = db.Open(dbPath)
	r.NoError(err)
	updated, err := media.NewRepo(database.WriteDB(), database.ReadDB()).GetByID(t.Context(), item.ID)
	r.NoError(err)
	r.NotEqual(item.CurrentVersionID, updated.CurrentVersionID)
	r.NoError(database.Close())
}

func TestCheckoutEstimateUsesCanonicalDatabaseLock(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	database, err := db.Open(dbPath)
	r.NoError(err)
	r.NoError(database.Close())
	aliasPath := filepath.Join(tmp, "database-alias.sqlite")
	if err := os.Symlink(dbPath, aliasPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Setenv("FOTOBANK_DB_PATH", aliasPath)
	databaseReplacement := flock.New(dbPath + ".lock")
	locked, err := databaseReplacement.TryLock()
	r.NoError(err)
	r.True(locked)
	t.Cleanup(func() { _ = databaseReplacement.Unlock() })

	var stdout, stderr bytes.Buffer
	code := cli.RunContext(t.Context(), []string{
		"checkout", "estimate", "--config", cfgPath, "--all",
	}, &stdout, &stderr)
	r.NotZero(code)
	r.Contains(stderr.String(), "another process is replacing the database")
}

func TestCheckoutCreateRecoversInterruptedCheckoutAfterTakingLock(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	database, err := db.Open(dbPath)
	r.NoError(err)
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err = database.WriteDB().ExecContext(t.Context(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES (?, ?, ?, ?)`,
		owner.Hub, owner.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC())
	r.NoError(err)
	contentStore, err := content.Open(t.Context(), content.Config{
		Root: filepath.Join(tmp, "flash", "docbank"),
	})
	r.NoError(err)
	item := assetfixture.InsertContent(t,
		media.NewRepo(database.WriteDB(), database.ReadDB()), contentStore,
		[]byte("checkout bytes"), media.Media{Owner: owner, OriginalFilename: "IMG_0100.JPG"})
	root := filepath.Join(tmp, "checkout")
	r.NoError(os.Mkdir(root, 0o700))
	validatedRoot, err := contentStore.ResolveCheckoutRoot(root)
	r.NoError(err)
	canonicalRoot := validatedRoot.Path()
	r.NoError(validatedRoot.Close())
	r.NoError(contentStore.Close())
	interruptedID := uuid.NewString()
	now := time.Now().UTC()
	r.NoError(checkout.NewRepo(database.WriteDB(), database.ReadDB()).Insert(t.Context(), checkout.Checkout{
		ID: interruptedID, Owner: owner, Root: canonicalRoot, Layout: "capture_date",
		Selection: checkout.Selection{AssetIDs: []string{item.ID}}, State: checkout.StateBuilding,
		CreatedAt: now, UpdatedAt: now,
	}))
	r.NoError(database.Close())

	holder := flock.New(dbPath + ".checkout.lock")
	locked, err := holder.TryLock()
	r.NoError(err)
	r.True(locked)
	t.Cleanup(func() { _ = holder.Unlock() })
	var stdout, stderr bytes.Buffer
	code := cli.RunContext(t.Context(), []string{
		"checkout", "create", "--config", cfgPath, "--asset", item.ID, root,
	}, &stdout, &stderr)
	r.NotZero(code)
	r.Contains(stderr.String(), "another checkout creation is in progress")
	database, err = db.Open(dbPath)
	r.NoError(err)
	var state string
	r.NoError(database.ReadDB().QueryRowContext(t.Context(),
		`SELECT state FROM checkouts WHERE id = ?`, interruptedID).Scan(&state))
	r.Equal(string(checkout.StateBuilding), state)
	r.NoError(database.Close())
	r.NoError(holder.Unlock())

	stdout.Reset()
	stderr.Reset()
	code = cli.RunContext(t.Context(), []string{
		"checkout", "create", "--config", cfgPath, "--asset", item.ID, root,
	}, &stdout, &stderr)
	r.Zero(code, "stderr=%s", stderr.String())
	database, err = db.Open(dbPath)
	r.NoError(err)
	t.Cleanup(func() { r.NoError(database.Close()) })
	var lastError string
	r.NoError(database.ReadDB().QueryRowContext(t.Context(),
		`SELECT state, last_error FROM checkouts WHERE id = ?`, interruptedID).
		Scan(&state, &lastError))
	r.Equal(string(checkout.StateError), state)
	r.Contains(lastError, "interrupted")
	var active int
	r.NoError(database.ReadDB().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM checkouts WHERE root = ? AND state = 'active'`, canonicalRoot).Scan(&active))
	r.Equal(1, active)
}
