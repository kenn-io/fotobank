package share_test

import (
	"context"
	"log/slog"
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

// recordingHandler captures slog records for assertion in tests. Local
// to this file; not exported.
type recordingHandler struct {
	records []slog.Record
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r)
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *recordingHandler) WithGroup(string) slog.Handler { return h }

// attrMap collects a record's top-level attributes into a map for
// easier assertion.
func attrMap(r slog.Record) map[string]slog.Value {
	out := make(map[string]slog.Value, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		out[a.Key] = a.Value
		return true
	})
	return out
}

func newResolver(t *testing.T, now time.Time) (*share.ScopeResolver, *share.Repo, *db.DB) {
	t.Helper()
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	r := share.NewScopeResolver(repo, func() time.Time { return now }, nil)
	return r, repo, d
}

func newResolverWithLogger(t *testing.T, now time.Time, logger *slog.Logger) (*share.ScopeResolver, *share.Repo, *db.DB) {
	t.Helper()
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	r := share.NewScopeResolver(repo, func() time.Time { return now }, logger)
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
	handler := &recordingHandler{}
	resolver, repo, d := newResolverWithLogger(t, now, slog.New(handler))

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

	// Spec §6.1 step 4: a warn log records caller, retained owner, and
	// dropped owner tuples whenever degradation actually fires.
	r.Len(handler.records, 1, "expected exactly one warn log for multi-owner degradation")
	rec := handler.records[0]
	r.Equal(slog.LevelWarn, rec.Level)
	r.Contains(rec.Message, "multi-owner")
	attrs := attrMap(rec)
	r.Equal(bob.Hub, attrs["caller_hub"].String())
	r.Equal(bob.UserID, attrs["caller_user_id"].String())
	r.Equal(aliceA.Hub, attrs["retained_owner_hub"].String())
	r.Equal(aliceA.UserID, attrs["retained_owner_user_id"].String())
	dropped, ok := attrs["dropped_owners"].Any().([]string)
	r.True(ok, "dropped_owners must be []string")
	r.Equal([]string{aliceB.Hub + "/" + aliceB.UserID}, dropped)
}

func TestResolveAllSingleOwnerDoesNotLog(t *testing.T) {
	r := require.New(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	handler := &recordingHandler{}
	resolver, repo, d := newResolverWithLogger(t, now, slog.New(handler))

	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	seedOwner(t, d.WriteDB(), alice, "ska")
	seedOwner(t, d.WriteDB(), bob, "skb")

	live := makeMediaSetScope(t, d, repo, alice, bob, nil, now)
	bumpActive(t, d, live.UUID, now)

	_, err := resolver.ResolveAll(context.Background(), bob, []string{live.UUID})
	r.NoError(err)
	r.Empty(handler.records, "single-owner presentations must not emit warn logs")
}

func TestResolveAllDedupesBeforeCap(t *testing.T) {
	r := require.New(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	resolver, repo, d := newResolver(t, now)

	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	seedOwner(t, d.WriteDB(), alice, "ska")
	seedOwner(t, d.WriteDB(), bob, "skb")

	// Seed MaxHeaderScopes unique live scopes.
	minted := make([]string, 0, share.MaxHeaderScopes)
	for range share.MaxHeaderScopes {
		s := makeMediaSetScope(t, d, repo, alice, bob, nil, now)
		bumpActive(t, d, s.UUID, now)
		minted = append(minted, s.UUID)
	}

	// Present each UUID TWICE (input length = 2*MaxHeaderScopes). Spec
	// §6.5 contract: dedupe runs before cap counting, so we must still
	// get all MaxHeaderScopes back, not MaxHeaderScopes/2.
	headers := make([]string, 0, 2*share.MaxHeaderScopes)
	headers = append(headers, minted...)
	headers = append(headers, minted...)

	got, err := resolver.ResolveAll(context.Background(), bob, headers)
	r.NoError(err)
	r.Len(got.ScopeUUIDs, share.MaxHeaderScopes)

	expected := append([]string(nil), minted...)
	sort.Strings(expected)
	r.Equal(expected, got.ScopeUUIDs)
}

func TestCheckMediaAccessViaMediaSet(t *testing.T) {
	r := require.New(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	resolver, repo, d := newResolver(t, now)

	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	seedOwner(t, d.WriteDB(), alice, "alice-sk")
	seedOwner(t, d.WriteDB(), bob, "bob-sk")

	mediaID := seedMedia(t, d.WriteDB(), alice, "media-set-c1")
	s := makeMediaSetScopeOver(t, d, repo, alice, bob, nil, now, false, mediaID)
	bumpActive(t, d, s.UUID, now)

	dec, err := resolver.CheckMediaAccess(context.Background(), bob, []string{s.UUID}, mediaID)
	r.NoError(err)
	r.True(dec.Authorized)
	r.Len(dec.Paths, 1)
	r.Equal(s.UUID, dec.Paths[0].ScopeUUID)
	r.Nil(dec.Paths[0].AlbumID)
}

func TestCheckMediaAccessViaAlbumLive(t *testing.T) {
	r := require.New(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	resolver, repo, d := newResolver(t, now)

	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	seedOwner(t, d.WriteDB(), alice, "alice-sk")
	seedOwner(t, d.WriteDB(), bob, "bob-sk")

	albumID, mediaIDs := seedAlbumWithMedia(t, d, alice, 2)
	s := makeAlbumLiveScope(t, d, repo, alice, bob, albumID, nil, now, false)
	bumpActive(t, d, s.UUID, now)

	dec, err := resolver.CheckMediaAccess(context.Background(), bob, []string{s.UUID}, mediaIDs[0])
	r.NoError(err)
	r.True(dec.Authorized)
	r.Len(dec.Paths, 1)
	r.Equal(s.UUID, dec.Paths[0].ScopeUUID)
	r.NotNil(dec.Paths[0].AlbumID)
	r.Equal(albumID, *dec.Paths[0].AlbumID)
}

func TestCheckMediaAccessUnauthorized(t *testing.T) {
	r := require.New(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	resolver, repo, d := newResolver(t, now)

	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	seedOwner(t, d.WriteDB(), alice, "alice-sk")
	seedOwner(t, d.WriteDB(), bob, "bob-sk")

	covered := seedMedia(t, d.WriteDB(), alice, "covered-c1")
	other := seedMedia(t, d.WriteDB(), alice, "other-c1")
	s := makeMediaSetScopeOver(t, d, repo, alice, bob, nil, now, false, covered)
	bumpActive(t, d, s.UUID, now)

	dec, err := resolver.CheckMediaAccess(context.Background(), bob, []string{s.UUID}, other)
	r.NoError(err)
	r.False(dec.Authorized)
	r.Empty(dec.Paths)
}

func TestCheckMediaAccessOverlappingScopesORsDownload(t *testing.T) {
	r := require.New(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	resolver, repo, d := newResolver(t, now)

	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	seedOwner(t, d.WriteDB(), alice, "alice-sk")
	seedOwner(t, d.WriteDB(), bob, "bob-sk")

	albumID, mediaIDs := seedAlbumWithMedia(t, d, alice, 1)
	albumScope := makeAlbumLiveScope(t, d, repo, alice, bob, albumID, nil, now, false)
	bumpActive(t, d, albumScope.UUID, now)

	mediaSetScope := makeMediaSetScopeOver(t, d, repo, alice, bob, nil, now, true, mediaIDs[0])
	bumpActive(t, d, mediaSetScope.UUID, now)

	dec, err := resolver.CheckMediaAccess(context.Background(), bob,
		[]string{albumScope.UUID, mediaSetScope.UUID}, mediaIDs[0])
	r.NoError(err)
	r.True(dec.Authorized)
	r.Len(dec.Paths, 2)
	r.True(dec.CanDownload())
}
