package service_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/checkout"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service"
	"go.kenn.io/fotobank/internal/testutil"
)

func TestRetireCheckoutKeepsHistoryAndFiles(t *testing.T) {
	r := require.New(t)
	database := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := database.WriteDB().ExecContext(t.Context(),
		`INSERT INTO owners(hub,user_id,storage_key,created_at) VALUES (?,?,?,?)`,
		owner.Hub, owner.UserID, uuid.New(), time.Now())
	r.NoError(err)
	repo := checkout.NewRepo(database.WriteDB(), database.ReadDB())
	lifecycle := &sync.RWMutex{}
	svc := service.NewCheckoutService(repo, nil, nil, nil, service.CheckoutServiceOptions{Lifecycle: lifecycle})
	for _, state := range []checkout.State{checkout.StateActive, checkout.StateError, checkout.StateBuilding} {
		t.Run(string(state), func(t *testing.T) {
			r := require.New(t)
			root := t.TempDir()
			file := filepath.Join(root, "working.jpg")
			r.NoError(os.WriteFile(file, []byte("uncommitted edits"), 0o600))
			id := uuid.New().String()
			now := time.Now().UTC()
			r.NoError(repo.Insert(t.Context(), checkout.Checkout{ID: id, Owner: owner,
				Root: root, Layout: "capture_date", State: state, Selection: checkout.Selection{All: true},
				CreatedAt: now, UpdatedAt: now}))
			for _, entryState := range []checkout.EntryState{checkout.EntryClean, checkout.EntryPending, checkout.EntryConflict, checkout.EntryMissing, checkout.EntryError} {
				r.NoError(repo.InsertEntry(t.Context(), checkout.Entry{
					CheckoutID: id, FileID: uuid.New().String(), RelativePath: string(entryState) + ".jpg",
					BaseVersionID: "version-1", BaseSHA256: strings.Repeat("a", 64),
					ObservedSHA256: strings.Repeat("a", 64), ObservedMTime: now,
					State: entryState, CreatedAt: now, UpdatedAt: now,
				}))
			}
			before, err := svc.Status(t.Context(), owner, id)
			r.NoError(err)
			missingRoot := state == checkout.StateError
			if missingRoot {
				r.NoError(os.Remove(file))
				r.NoError(os.Remove(root))
			}
			_, err = svc.Retire(t.Context(), owners.Principal{Hub: "h", UserID: "other"}, id)
			r.ErrorIs(err, errs.ErrNotFound)
			// Model an in-flight scanner/committer holding the daemon's read
			// lease. Retirement must join that work before changing state.
			lifecycle.RLock()
			release := sync.OnceFunc(lifecycle.RUnlock)
			t.Cleanup(release)
			type retirement struct {
				status checkout.Status
				err    error
			}
			done := make(chan retirement, 1)
			go func() {
				status, err := svc.Retire(t.Context(), owner, id)
				done <- retirement{status, err}
			}()
			r.Eventually(func() bool {
				if lifecycle.TryRLock() {
					lifecycle.RUnlock()
					return false
				}
				return true
			}, 5*time.Second, time.Millisecond, "retirement must acquire the exclusive lifecycle lease")
			stored, err := repo.Get(t.Context(), id)
			r.NoError(err)
			r.Equal(state, stored.State)
			release()
			retired := <-done
			r.NoError(retired.err)
			result := retired.status
			r.Equal(checkout.StateRetired, result.Checkout.State)
			r.Equal(before.Checkout.Entries, result.Checkout.Entries)
			r.Len(result.Problems, 4)
			r.Equal(before.Problems, result.Problems)
			if !missingRoot {
				body, err := os.ReadFile(file)
				r.NoError(err)
				r.Equal("uncommitted edits", string(body))
			} else {
				r.NoDirExists(root)
			}
			roots, err := repo.LiveRoots(t.Context())
			r.NoError(err)
			r.NotContains(roots, root)
			// Repeating retirement must not need the old working directory.
			if !missingRoot {
				r.NoError(os.Remove(file))
				r.NoError(os.Remove(root))
			}
			again, err := svc.Retire(t.Context(), owner, id)
			r.NoError(err)
			r.Equal(result, again)
		})
	}
}
