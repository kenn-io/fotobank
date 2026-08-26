package share_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/share"
	"go.kenn.io/fotobank/internal/testutil"
)

// TestOwnerConsistencyTriggerOnScopeMedia ensures the SQL trigger fires
// when a scope_media row is inserted with a cross-owner media_id. The
// service layer's per-ID pre-flight is the primary guard; this trigger
// is defence-in-depth and Repo.Insert bypasses the service, so we test
// the trigger directly here.
func TestOwnerConsistencyTriggerOnScopeMedia(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	a := owners.Principal{Hub: "h", UserID: "a"}
	b := owners.Principal{Hub: "h", UserID: "b"}
	seedOwner(t, d.WriteDB(), a, "00000000-0000-4000-8000-15d3a52f3a69")
	seedOwner(t, d.WriteDB(), b, "00000000-0000-4000-8000-7f8246cf1abe")
	mBID := seedMedia(t, d.WriteDB(), b, "cB")

	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	s := share.Scope{
		UUID: uuid.NewString(), Owner: a,
		Grantee:      owners.Principal{Hub: "h", UserID: "g"},
		TargetType:   share.TargetMediaSet,
		CreatedAt:    time.Now().UTC().Truncate(time.Second),
		BrokerStatus: share.StatusPending,
	}
	err := repo.Insert(context.Background(), s, []string{mBID})
	r.Error(err)
	r.True(strings.Contains(err.Error(), "album and media must share owner") ||
		strings.Contains(err.Error(), "scope and media must share owner"),
		"expected SQLite trigger ABORT to surface, got %v", err)

	// Confirm nothing was written (rollback).
	row := d.ReadDB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM scopes WHERE uuid = ?`, s.UUID)
	var n int
	r.NoError(row.Scan(&n))
	r.Equal(0, n)
}
