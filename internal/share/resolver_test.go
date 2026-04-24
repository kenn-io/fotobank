package share_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/share"
	"github.com/wesm/fotobank/internal/testutil"
)

func newResolver(t *testing.T, now time.Time) (*share.ScopeResolver, *share.Repo, *db.DB) {
	t.Helper()
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	r := share.NewScopeResolver(repo, func() time.Time { return now })
	return r, repo, d
}

func TestResolveAllDropsMalformedDedupsAndCaps(t *testing.T) {
	r := require.New(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	resolver, repo, d := newResolver(t, now)

	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	seedOwner(t, d.WriteDB(), alice, "ska")
	seedOwner(t, d.WriteDB(), bob, "skb")

	live := makeMediaSetScope(t, d, repo, alice, bob, nil, now)
	bumpActive(t, d, live.UUID, now)

	headers := []string{
		"",               // empty, dropped
		"not-a-uuid",     // malformed, dropped
		live.UUID,        // kept
		live.UUID,        // duplicate, dropped
		uuid.NewString(), // syntactically valid but unknown, filtered by repo
	}

	got, err := resolver.ResolveAll(context.Background(), bob, headers)
	r.NoError(err)
	r.Equal([]string{live.UUID}, got.ScopeUUIDs)
	r.Equal(alice, got.Owner)
	r.Len(got.Validated, 1)
	r.Equal(live.UUID, got.Validated[0].UUID)
}

func TestResolveAllAppliesMaxHeaderScopesCap(t *testing.T) {
	r := require.New(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	resolver, repo, d := newResolver(t, now)

	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	seedOwner(t, d.WriteDB(), alice, "ska")
	seedOwner(t, d.WriteDB(), bob, "skb")

	total := share.MaxHeaderScopes + 5
	minted := make([]string, 0, total)
	for range total {
		s := makeMediaSetScope(t, d, repo, alice, bob, nil, now)
		bumpActive(t, d, s.UUID, now)
		minted = append(minted, s.UUID)
	}

	got, err := resolver.ResolveAll(context.Background(), bob, minted)
	r.NoError(err)
	r.Len(got.ScopeUUIDs, share.MaxHeaderScopes)
	r.True(sort.StringsAreSorted(got.ScopeUUIDs), "ScopeUUIDs must be sorted ascending")

	// Cap is applied pre-validation (earliest-in-input wins after dedupe).
	expected := append([]string(nil), minted[:share.MaxHeaderScopes]...)
	sort.Strings(expected)
	r.Equal(expected, got.ScopeUUIDs)
}

func TestResolveAllMultiOwnerKeepsLexSmallest(t *testing.T) {
	r := require.New(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	resolver, repo, d := newResolver(t, now)

	// Two owners with identical user_ids but different hubs. Bob is the
	// grantee. (hubA, alice) < (hubB, alice) lexicographically, so
	// hubA/alice is retained.
	aliceA := owners.Principal{Hub: "hubA", UserID: "alice"}
	aliceB := owners.Principal{Hub: "hubB", UserID: "alice"}
	bob := owners.Principal{Hub: "hubA", UserID: "bob"}
	seedOwner(t, d.WriteDB(), aliceA, "ska")
	seedOwner(t, d.WriteDB(), aliceB, "skb")
	seedOwner(t, d.WriteDB(), bob, "skbob")

	scopeA := makeMediaSetScope(t, d, repo, aliceA, bob, nil, now)
	bumpActive(t, d, scopeA.UUID, now)
	scopeB := makeMediaSetScope(t, d, repo, aliceB, bob, nil, now)
	bumpActive(t, d, scopeB.UUID, now)

	got, err := resolver.ResolveAll(context.Background(), bob,
		[]string{scopeA.UUID, scopeB.UUID})
	r.NoError(err)
	r.Equal(aliceA, got.Owner)
	r.Equal([]string{scopeA.UUID}, got.ScopeUUIDs)
	r.Len(got.Validated, 1)
	r.Equal(aliceA, got.Validated[0].Owner)
}
