package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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
}
