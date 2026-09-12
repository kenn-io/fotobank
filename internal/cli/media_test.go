package cli_test

import (
	"bytes"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil/assetfixture"
)

func TestMediaDiscovery(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	cfg := writeBasicConfig(t, dir)
	dbPath := filepath.Join(dir, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	first := seedReadyRow(t, dbPath)
	second := seedReadyRow(t, dbPath)
	hidden := seedReadyRow(t, dbPath)
	database, err := db.Open(dbPath)
	r.NoError(err)
	_, err = database.WriteDB().Exec(`UPDATE assets SET make='Example', model='Camera', lens_model='Wide', latitude=1, longitude=2 WHERE id=?`, first.ID)
	r.NoError(err)
	_, err = database.WriteDB().Exec(`UPDATE assets SET hidden_at=? WHERE id=?`, time.Now().UTC(), hidden.ID)
	r.NoError(err)
	otherOwner := owners.Principal{Hub: "h", UserID: "other"}
	_, err = database.WriteDB().Exec(`INSERT INTO owners(hub,user_id,storage_key,created_at) VALUES(?,?,?,?)`, otherOwner.Hub, otherOwner.UserID, uuid.New().String(), time.Now().UTC())
	r.NoError(err)
	other := assetfixture.Insert(t, media.NewRepo(database.WriteDB(), database.ReadDB()), media.Media{Owner: otherOwner, ThumbStatus: "ready"})
	attachmentID := uuid.New()
	_, err = database.WriteDB().Exec(`INSERT INTO media_files
		(id,asset_id,owner_hub,owner_user_id,role,mime_type,original_filename,size,docbank_node_id,docbank_virtual_path,current_version_id,sha256)
		VALUES (?,?,'h','u','sidecar','application/xml','x.jpg.xmp',3,999999,?, ?, ?)`,
		attachmentID.String(), first.ID, "/owners/550e8400-e29b-41d4-a716-446655440000/media/"+attachmentID.String()+"/x.jpg.xmp", uuid.New().String(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	r.NoError(err)
	r.NoError(database.Close())
	startCheckoutServer(t, cfg, dbPath)

	run := func(args ...string) string {
		t.Helper()
		var out, stderr bytes.Buffer
		args = append(args, "--config", cfg)
		r.Zero(cli.RunContext(t.Context(), args, &out, &stderr), "%s", stderr.String())
		return out.String()
	}
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
		NextOffset *int `json:"next_offset"`
	}
	r.NoError(json.Unmarshal([]byte(run("media", "list", "--limit", "1", "--json")), &page))
	r.Len(page.Items, 1)
	r.NotNil(page.NextOffset)
	r.Equal(1, *page.NextOffset)
	pageOne := page.Items[0].ID
	r.NoError(json.Unmarshal([]byte(run("media", "list", "--limit", "1", "--offset", "1", "--json")), &page))
	r.Len(page.Items, 1)
	r.ElementsMatch([]string{first.ID, second.ID}, []string{pageOne, page.Items[0].ID})

	r.NoError(json.Unmarshal([]byte(run("media", "list", "--type", "photo", "--camera", "Other", "--camera", "Example Camera", "--lens", "Wide", "--has-gps", "--json")), &page))
	r.Len(page.Items, 1)
	r.Equal(first.ID, page.Items[0].ID)
	r.NoError(json.Unmarshal([]byte(run("media", "list", "--has-gps=false", "--json")), &page))
	r.Len(page.Items, 1)
	r.Equal(second.ID, page.Items[0].ID)
	var detail struct {
		ID     string `json:"id"`
		SHA256 string `json:"sha256"`
		Files  []struct {
			ID   string `json:"id"`
			Role string `json:"role"`
		} `json:"files"`
	}
	r.NoError(json.Unmarshal([]byte(run("media", "show", first.ID, "--json")), &detail))
	r.Equal(first.ID, detail.ID)
	r.Equal(first.SHA256, detail.SHA256)
	r.Len(detail.Files, 1)
	r.Equal(attachmentID.String(), detail.Files[0].ID)
	r.Equal("sidecar", detail.Files[0].Role)
	r.Contains(run("media", "show", first.ID), attachmentID.String())
	single := run("media", "show", second.ID, "--json")
	r.Contains(single, `"files":[]`)
	r.NoError(json.Unmarshal([]byte(single), &detail))
	r.Empty(detail.Files)
	r.Contains(run("media", "show", first.ID), first.SHA256)
	r.Contains(run("media", "list"), first.ID)
	for _, inaccessible := range []string{hidden.ID, other.ID} {
		var out, stderr bytes.Buffer
		r.Equal(1, cli.RunContext(t.Context(), []string{"media", "show", inaccessible, "--json", "--config", cfg}, &out, &stderr))
		r.Empty(out.String())
		r.Contains(stderr.String(), "404")
	}
}

func TestMediaInvalidArgumentsDoNotStartDaemon(t *testing.T) {
	for _, args := range [][]string{
		{"media", "show", "not-an-id"},
		{"media", "list", "--type", "raw"},
		{"media", "list", "--limit", "0"},
		{"media", "list", "--limit", "1001"},
		{"media", "list", "--offset", "-1"},
	} {
		t.Run(args[1]+args[len(args)-1], func(t *testing.T) {
			r := require.New(t)
			cfg := filepath.Join(t.TempDir(), "missing.toml")
			var out, stderr bytes.Buffer
			r.Equal(2, cli.RunContext(t.Context(), append(args, "--config", cfg), &out, &stderr))
			r.Empty(out.String())
			_, err := os.Stat(cfg + ".operator")
			r.ErrorIs(err, os.ErrNotExist)
		})
	}
}
