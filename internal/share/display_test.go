package share_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/identity"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/share"
	"go.kenn.io/fotobank/internal/testutil"
)

func TestPrincipalDisplayUpsertAndGet(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewPrincipalDisplayRepo(d.WriteDB(), d.ReadDB())

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	err := repo.Upsert(context.Background(),
		identity.Principal{Hub: "h", UserID: "alice", Handle: "Alice"}, now)
	r.NoError(err)

	handle, ok, err := repo.Get(context.Background(),
		owners.Principal{Hub: "h", UserID: "alice"})
	r.NoError(err)
	r.True(ok)
	r.Equal("Alice", handle)
}

func TestPrincipalDisplayUpsertKeepsNewest(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewPrincipalDisplayRepo(d.WriteDB(), d.ReadDB())

	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	r.NoError(repo.Upsert(context.Background(),
		identity.Principal{Hub: "h", UserID: "alice", Handle: "New"}, t2))
	// Earlier cached_at must not overwrite the newer row.
	r.NoError(repo.Upsert(context.Background(),
		identity.Principal{Hub: "h", UserID: "alice", Handle: "Stale"}, t1))

	handle, ok, err := repo.Get(context.Background(),
		owners.Principal{Hub: "h", UserID: "alice"})
	r.NoError(err)
	r.True(ok)
	r.Equal("New", handle)
}

func TestPrincipalDisplayGetMissingReturnsFalse(t *testing.T) {
	d := testutil.OpenTestDB(t)
	repo := share.NewPrincipalDisplayRepo(d.WriteDB(), d.ReadDB())
	_, ok, err := repo.Get(context.Background(),
		owners.Principal{Hub: "h", UserID: "ghost"})
	require.NoError(t, err)
	require.False(t, ok)
}

func TestPrincipalDisplayGetBatchReturnsKnown(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewPrincipalDisplayRepo(d.WriteDB(), d.ReadDB())
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	r.NoError(repo.Upsert(context.Background(),
		identity.Principal{Hub: "h", UserID: "alice", Handle: "Alice"}, now))
	r.NoError(repo.Upsert(context.Background(),
		identity.Principal{Hub: "h", UserID: "bob", Handle: "Bob"}, now))

	got, err := repo.GetBatch(context.Background(), []owners.Principal{
		{Hub: "h", UserID: "alice"},
		{Hub: "h", UserID: "ghost"},
		{Hub: "h", UserID: "bob"},
	})
	r.NoError(err)
	r.Equal("Alice", got[owners.Principal{Hub: "h", UserID: "alice"}])
	r.Equal("Bob", got[owners.Principal{Hub: "h", UserID: "bob"}])
	_, ok := got[owners.Principal{Hub: "h", UserID: "ghost"}]
	r.False(ok)
}

func TestPrincipalDisplayGetBatchMapsNullHandleToEmpty(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)

	// Insert a row directly with NULL handle — Upsert's API cannot
	// produce this, but the schema allows it and GetBatch documents
	// the behavior, so exercise the branch explicitly.
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO principal_display(hub, user_id, handle, cached_at)
         VALUES (?, ?, NULL, ?)`,
		"h", "alice", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	r.NoError(err)

	repo := share.NewPrincipalDisplayRepo(d.WriteDB(), d.ReadDB())
	got, err := repo.GetBatch(context.Background(),
		[]owners.Principal{{Hub: "h", UserID: "alice"}})
	r.NoError(err)
	v, ok := got[owners.Principal{Hub: "h", UserID: "alice"}]
	r.True(ok, "null-handle row should appear in the result map")
	r.Empty(v, "null handle maps to empty string")
}
