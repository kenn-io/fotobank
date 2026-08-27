package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
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

// reconcileFixture captures the paths and identity a reconcile test needs
// to drive the CLI against a pre-seeded DB. The shape mirrors what the
// stub-identity branch of runReconcile reads at runtime.
type reconcileFixture struct {
	cfgPath string
	dbPath  string
	nasRoot string
	owner   owners.Principal
}

// newReconcileFixture writes a minimal stub-identity config, sets the env
// vars runReconcile consults, and registers the stub owner in the DB so
// the command finds a resolved storage key.
func newReconcileFixture(t *testing.T) reconcileFixture {
	t.Helper()
	r := require.New(t)

	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	storageKey := "550e8400-e29b-41d4-a716-446655440000"
	r.NoError(os.MkdirAll(filepath.Join(nasRoot, storageKey), 0o700))

	cfgPath := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[identity]
mode = "stub"
[identity.stub]
hub = "local"
user_id = "alice"
storage_key = %q
`, nasRoot, filepath.Join(tmp, "flash"), storageKey), 0o600))

	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	owner := owners.Principal{Hub: "local", UserID: "alice"}

	// Pre-register the owner so the first reconcile run observes a
	// storage_key exactly matching the NAS subdirectory created above.
	d, err := db.Open(dbPath)
	r.NoError(err)
	t.Cleanup(func() { _ = d.Close() })
	_, err = d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, storageKey, time.Now().UTC(),
	)
	r.NoError(err)

	return reconcileFixture{
		cfgPath: cfgPath,
		dbPath:  dbPath,
		nasRoot: nasRoot,
		owner:   owner,
	}
}

// seedPhantom inserts a media row whose bytes do not exist on disk — the
// canonical case for a Missing entry.
func (f reconcileFixture) seedPhantom(t *testing.T, path, checksum string) media.Media {
	t.Helper()
	r := require.New(t)
	d, err := db.Open(f.dbPath)
	r.NoError(err)
	defer d.Close()
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	m := media.Media{
		ID:          uuid.NewString(),
		Owner:       f.owner,
		Type:        media.TypePhoto,
		MimeType:    "image/jpeg",
		Path:        path,
		ImportedAt:  time.Now().UTC().Truncate(time.Second),
		Size:        11,
		Checksum:    checksum,
		ThumbStatus: "pending",
	}
	r.NoError(repo.Insert(context.Background(), m))
	return m
}

func TestFotobankReconcileReportsPhantoms(t *testing.T) {
	r := require.New(t)
	f := newReconcileFixture(t)
	phantom := f.seedPhantom(t, "2024/phantom.jpg", "cs-phantom")

	var out, eout bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"reconcile", "--config", f.cfgPath, "--json"},
		&out, &eout)
	r.Equal(0, code, "stderr=%s stdout=%s", eout.String(), out.String())

	var rep struct {
		Orphans      []map[string]any `json:"orphans"`
		Missing      []map[string]any `json:"missing"`
		SizeMismatch []map[string]any `json:"size_mismatch"`
		StaleTemps   []string         `json:"stale_temps"`
		DeletedRows  int              `json:"deleted_rows"`
		DeletedTemps int              `json:"deleted_temps"`
	}
	r.NoError(json.Unmarshal(out.Bytes(), &rep))

	r.Len(rep.Missing, 1)
	r.Equal(phantom.ID, rep.Missing[0]["ID"])
	r.Empty(rep.Orphans)
	r.Empty(rep.SizeMismatch)
	r.Empty(rep.StaleTemps)
	r.Zero(rep.DeletedRows)
	r.Zero(rep.DeletedTemps)
}

func TestFotobankReconcileCommitDeletesRemovesPhantoms(t *testing.T) {
	r := require.New(t)
	f := newReconcileFixture(t)
	f.seedPhantom(t, "2024/phantom.jpg", "cs-phantom")

	var out, eout bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"reconcile", "--config", f.cfgPath, "--json", "--commit-deletes"},
		&out, &eout)
	r.Equal(0, code, "stderr=%s stdout=%s", eout.String(), out.String())

	var first struct {
		Missing     []map[string]any `json:"missing"`
		DeletedRows int              `json:"deleted_rows"`
	}
	r.NoError(json.Unmarshal(out.Bytes(), &first))
	r.Equal(1, first.DeletedRows)
	r.Len(first.Missing, 1)

	// Re-run without --commit-deletes; the phantom row should be gone.
	out.Reset()
	eout.Reset()
	code = cli.RunContext(context.Background(),
		[]string{"reconcile", "--config", f.cfgPath, "--json"},
		&out, &eout)
	r.Equal(0, code, "stderr=%s stdout=%s", eout.String(), out.String())

	var second struct {
		Missing     []map[string]any `json:"missing"`
		DeletedRows int              `json:"deleted_rows"`
	}
	r.NoError(json.Unmarshal(out.Bytes(), &second))
	r.Empty(second.Missing)
	r.Zero(second.DeletedRows)
}

func TestFotobankReconcileHumanOutput(t *testing.T) {
	r := require.New(t)
	f := newReconcileFixture(t)
	f.seedPhantom(t, "2024/phantom.jpg", "cs-phantom")

	var out, eout bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"reconcile", "--config", f.cfgPath},
		&out, &eout)
	r.Equal(0, code, "stderr=%s stdout=%s", eout.String(), out.String())

	s := out.String()
	r.Contains(s, "fotobank reconcile report")
	r.Contains(s, "Orphans:")
	r.Contains(s, "Missing:")
	r.Contains(s, "Size mismatches:")
	r.Contains(s, "Stale temps:")
	r.Contains(s, "2024/phantom.jpg")
	// Without --commit-deletes the report must not advertise deletions.
	r.NotContains(s, "Deleted rows:")
	r.NotContains(s, "Deleted temps:")
}

func TestFotobankReconcileRejectsHeaderIdentityMode(t *testing.T) {
	r := require.New(t)

	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	r.NoError(os.MkdirAll(nasRoot, 0o700))

	cfgPath := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[identity]
mode = "header"
[http]
listen_address = "127.0.0.1:8090"
`, nasRoot, filepath.Join(tmp, "flash")), 0o600))

	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))

	var out, eout bytes.Buffer
	code := cli.Run([]string{"reconcile", "--config", cfgPath}, &out, &eout)
	r.Equal(1, code)
	r.Contains(eout.String(), "identity.mode = stub")
}
