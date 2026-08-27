package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/owners"
)

func TestOwnersAddGeneratedAndExplicitKeys(t *testing.T) {
	r := require.New(t)
	tmp := newCLITempEnv(t)

	var out, eout bytes.Buffer
	code := cli.Run([]string{"owners", "add",
		"--hub", "h", "--user-id", "u", "--handle", "User",
	}, &out, &eout)
	r.Equal(0, code, eout.String())
	outputFields := strings.Fields(out.String())
	r.NotEmpty(outputFields)
	generatedKey := outputFields[len(outputFields)-1]
	_, err := uuid.Parse(generatedKey)
	r.NoError(err)

	// Repeating an add without an explicit key returns the stored winner.
	out.Reset()
	eout.Reset()
	code = cli.Run([]string{"owners", "add",
		"--hub", "h", "--user-id", "u",
	}, &out, &eout)
	r.Equal(0, code, eout.String())
	r.Contains(out.String(), generatedKey)

	explicitKey := "660e8400-e29b-41d4-a716-446655440000"
	out.Reset()
	eout.Reset()
	code = cli.Run([]string{"owners", "add",
		"--hub", "h", "--user-id", "explicit", "--storage-key", explicitKey,
	}, &out, &eout)
	r.Equal(0, code, eout.String())
	r.Contains(out.String(), explicitKey)

	out.Reset()
	eout.Reset()
	code = cli.Run([]string{"owners", "add",
		"--hub", "h", "--user-id", "invalid", "--storage-key", "not-a-uuid",
	}, &out, &eout)
	r.Equal(1, code)
	r.Contains(eout.String(), "invalid argument")

	d, err := db.Open(filepath.Join(tmp, "fotobank.sqlite"))
	r.NoError(err)
	stored, err := owners.NewRepo(d.WriteDB(), d.ReadDB()).GetByPrincipal(
		t.Context(), owners.Principal{Hub: "h", UserID: "u"})
	r.NoError(err)
	r.Equal(generatedKey, stored.StorageKey)
	r.NoError(d.Close())
}

func TestOwnersListShowsAddedRow(t *testing.T) {
	r := require.New(t)
	_ = newCLITempEnv(t)

	var out, eout bytes.Buffer
	r.Equal(0, cli.Run([]string{"owners", "add",
		"--hub", "h", "--user-id", "u", "--storage-key", "550e8400-e29b-41d4-a716-446655440000",
	}, &out, &eout))

	out.Reset()
	eout.Reset()
	r.Equal(0, cli.Run([]string{"owners", "list", "--json"}, &out, &eout))
	var rows []map[string]any
	r.NoError(json.Unmarshal(out.Bytes(), &rows))
	r.Len(rows, 1)
	r.Equal("h", rows[0]["hub"])
}

func TestOwnersListRejectsBadFlags(t *testing.T) {
	// Regression: owners list used to discard fs.Parse errors, so an
	// invalid flag would silently open the DB and list rows. It must
	// now exit 2 with usage before doing any work.
	r := require.New(t)
	_ = newCLITempEnv(t)

	var out, eout bytes.Buffer
	r.Equal(2, cli.Run([]string{"owners", "list", "--bad"}, &out, &eout))
	r.Contains(eout.String(), "usage")

	out.Reset()
	eout.Reset()
	r.Equal(2, cli.Run([]string{"owners", "list", "extra-positional"}, &out, &eout))
	r.Contains(eout.String(), "usage")
}

func TestOwnersRemoveSucceedsWhenEmpty(t *testing.T) {
	r := require.New(t)
	_ = newCLITempEnv(t)
	var out, eout bytes.Buffer
	r.Equal(0, cli.Run([]string{"owners", "add",
		"--hub", "h", "--user-id", "u", "--storage-key", "550e8400-e29b-41d4-a716-446655440000",
	}, &out, &eout))
	out.Reset()
	eout.Reset()
	r.Equal(0, cli.Run([]string{"owners", "remove",
		"--hub", "h", "--user-id", "u",
	}, &out, &eout))
}

// newCLITempEnv sets FOTOBANK_CONFIG + FOTOBANK_DB_PATH to t.TempDir()-backed
// values with a valid minimal config; returns the tempdir.
func newCLITempEnv(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(cfg, fmt.Appendf(nil, `
[nas]
root = %q
`, filepath.Join(tmp, "nas")), 0o600))
	t.Setenv("FOTOBANK_CONFIG", cfg)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))
	return tmp
}
