package checkout

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/contentresolver"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil"
)

func TestCreateRecordsActivationCancellationAsError(t *testing.T) {
	r := require.New(t)
	database := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "local", UserID: "alice"}
	_, err := database.WriteDB().ExecContext(t.Context(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES (?, ?, ?, ?)`,
		owner.Hub, owner.UserID, uuid.NewString(), time.Now().UTC())
	r.NoError(err)
	adapter, err := content.Open(t.Context(), content.Config{Root: t.TempDir()})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(adapter.Close()) })
	root, err := adapter.ResolveCheckoutRoot(t.TempDir())
	r.NoError(err)
	repo := NewRepo(database.WriteDB(), database.ReadDB())
	resolver := contentresolver.New(media.NewRepo(database.WriteDB(), database.ReadDB()), adapter)
	lockDir := t.TempDir()
	materializer := NewMaterializer(
		repo, resolver,
		filepath.Join(lockDir, "checkout.lock"), filepath.Join(lockDir, "database.lock"))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	materializer.now = func() time.Time {
		calls++
		if calls == 3 {
			cancel()
		}
		return time.Now()
	}

	_, err = materializer.Create(ctx, owner, CreateRequest{
		Root: root, Selection: Selection{All: true}, CapacityLimit: 1,
	})
	r.ErrorIs(err, context.Canceled)
	var state string
	r.NoError(database.ReadDB().QueryRowContext(t.Context(),
		`SELECT state FROM checkouts`).Scan(&state))
	r.Equal(string(StateError), state)
}
