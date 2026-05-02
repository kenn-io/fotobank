package search_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/auth/hidden"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/errs"
	searchsvc "github.com/wesm/fotobank/internal/service/search"
	"github.com/wesm/fotobank/internal/testutil"
)

// autocompleteSvc wires a Service against a test DB. The service-level
// auth-side dependencies (engine/settings/tags) are nil because the
// autocomplete path does not consult them; the hidden checker accepts
// every claim so happy-path tests can flip includeHidden=true. ro is
// the read pool the autocomplete queries fan into.
func autocompleteSvc(t *testing.T, d *db.DB) *searchsvc.Service {
	t.Helper()
	return searchsvc.New(nil, nil, nil, completenessFakeChecker{}, nil, d.ReadDB())
}

// seedTagsForMedia inserts an ai_results+media_tags pair for one
// media. status='active' so autocomplete picks it up. Each (label, key)
// pair becomes one tag row; label and key map 1:1 because v1's tag
// resolver is the only path that canonicalises chip labels and the
// autocomplete query keys off tag_key.
func seedTagsForMedia(t *testing.T, rw *sql.DB, mediaID string, tags map[string]string) {
	t.Helper()
	resultID := uuid.NewString()
	ctx := context.Background()
	_, err := rw.ExecContext(ctx,
		`INSERT INTO ai_results(id, media_id, task, model_id, prompt_version, prompt_hash,
		 input_profile, status, generated_at) VALUES (?,?, 'tag', ?, ?, ?, ?, 'active', ?)`,
		resultID, mediaID, "test-model", "tag-v1", "test-hash", "test-profile", time.Now().UTC())
	require.NoError(t, err)
	rank := 1
	for key, label := range tags {
		_, err := rw.ExecContext(ctx,
			`INSERT INTO media_tags(result_id, tag_key, tag_label, rank) VALUES (?,?,?,?)`,
			resultID, key, label, rank)
		require.NoError(t, err)
		rank++
	}
}

// setLocation stamps a location_label onto a media row. Used by the
// location-autocomplete tests to build a small library of distinct
// labels (the SeedPhoto helper does not take a location).
func setLocation(t *testing.T, rw *sql.DB, mediaID, label string) {
	t.Helper()
	_, err := rw.ExecContext(context.Background(),
		`UPDATE media SET location_label = ? WHERE id = ?`, label, mediaID)
	require.NoError(t, err)
}

// hideMediaID stamps hidden_at on a row so autocomplete tests can
// exercise the hidden predicate. Mirrors the helper in completeness_test
// but kept local so the two files don't collide on the symbol name.
func hideMediaID(t *testing.T, rw *sql.DB, mediaID string) {
	t.Helper()
	_, err := rw.ExecContext(context.Background(),
		`UPDATE media SET hidden_at = ? WHERE id = ?`, time.Now().UTC(), mediaID)
	require.NoError(t, err)
}

// TestAutocompleteTags_PrefixMatchOwnerScopedHiddenAware pins the core
// invariant: prefix matching is owner-scoped, so owner B's "dog" never
// shows up for caller A. The fixture seeds owner A with three "dog*"
// tags and owner B with one "dog"; the query "dog" for caller A returns
// exactly the three A-owned tags.
func TestAutocompleteTags_PrefixMatchOwnerScopedHiddenAware(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	rw := d.WriteDB()
	ownerA := testutil.SeedOwner(t, rw, "test-hub", "alice")
	ownerB := testutil.SeedOwner(t, rw, "test-hub", "bob")

	// Owner A: three media, each with one of the dog* tags.
	mA1 := testutil.SeedPhoto(t, rw, ownerA, "a1")
	mA2 := testutil.SeedPhoto(t, rw, ownerA, "a2")
	mA3 := testutil.SeedPhoto(t, rw, ownerA, "a3")
	seedTagsForMedia(t, rw, mA1, map[string]string{"dog": "dog"})
	seedTagsForMedia(t, rw, mA2, map[string]string{"doggo": "doggo"})
	seedTagsForMedia(t, rw, mA3, map[string]string{"doglike": "doglike"})

	// Owner B: one media with "dog". The autocomplete must not surface
	// it for caller A.
	mB := testutil.SeedPhoto(t, rw, ownerB, "b1")
	seedTagsForMedia(t, rw, mB, map[string]string{"dog": "dog"})

	svc := autocompleteSvc(t, d)
	got, err := svc.AutocompleteTags(context.Background(), ownerA, "dog", 10, false, nil)
	r.NoError(err)
	r.Len(got, 3, "owner A has three dog* tags; owner B's dog must not leak")

	labels := make([]string, len(got))
	for i, s := range got {
		labels[i] = s.Label
	}
	r.ElementsMatch([]string{"dog", "doggo", "doglike"}, labels)
}

// TestAutocompleteTags_HiddenWithoutUnlockExcluded — a tag attached
// only to a hidden photo must not appear in autocomplete unless the
// caller presents a valid unlock claim. Mirrors the search-design
// hidden invariant: visibility is uniform across query and chip
// surfaces.
func TestAutocompleteTags_HiddenWithoutUnlockExcluded(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	rw := d.WriteDB()
	owner := testutil.SeedOwner(t, rw, "test-hub", "alice")

	visibleMedia := testutil.SeedPhoto(t, rw, owner, "v")
	hiddenMedia := testutil.SeedPhoto(t, rw, owner, "h")
	seedTagsForMedia(t, rw, visibleMedia, map[string]string{"cat": "cat"})
	seedTagsForMedia(t, rw, hiddenMedia, map[string]string{"unicorn": "unicorn"})
	hideMediaID(t, rw, hiddenMedia)

	svc := autocompleteSvc(t, d)

	// Without unlock claim: only the visible-media tag.
	visible, err := svc.AutocompleteTags(context.Background(), owner, "", 10, false, nil)
	r.NoError(err)
	r.Len(visible, 1, "hidden-only tags must not appear without unlock")
	r.Equal("cat", visible[0].Label)

	// With unlock: both tags surface.
	claim := &hidden.UnlockClaim{Principal: owner, ExpiresAt: time.Now().Add(time.Hour)}
	all, err := svc.AutocompleteTags(context.Background(), owner, "", 10, true, claim)
	r.NoError(err)
	r.Len(all, 2, "unlock claim must reveal hidden-only tags")

	// includeHidden=true with nil claim is a hard deny — same gate as Search.
	_, err = svc.AutocompleteTags(context.Background(), owner, "", 10, true, nil)
	r.ErrorIs(err, errs.ErrPermissionDenied)
}

// TestAutocompleteTags_LIKEEscapesPercentAndUnderscore pins the SQL
// injection / LIKE-glob defense: input "100_%" is treated as a literal
// string, not as the LIKE-wildcard pattern that matches everything.
//
// The fixture is built so the assertion actually exercises the escape:
//
//   - Negative seed "100abc" — would match the unescaped pattern
//     "100_%%" ("100" + any-char + zero-or-more-chars + zero-or-
//     more-chars from the trailing prefix wildcard). With escaping, the
//     pattern becomes "100\_\%%" — literal "100_%" then any suffix —
//     which "100abc" does NOT contain, so the row stays out.
//
//   - Positive seed "100_%abc" — the literal-underscore-percent prefix
//     is exactly what the user typed; with escaping it matches.
//
// Without both seeds the test would still pass even if escaping were
// removed (the original "doggo" seed didn't match either pattern).
func TestAutocompleteTags_LIKEEscapesPercentAndUnderscore(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	rw := d.WriteDB()
	owner := testutil.SeedOwner(t, rw, "test-hub", "alice")

	// Negative seed: matches the un-escaped wildcard pattern
	// (100 + any single char + anything) but NOT the escaped literal
	// pattern (100 + literal "_%" + anything).
	mNeg := testutil.SeedPhoto(t, rw, owner, "neg")
	seedTagsForMedia(t, rw, mNeg, map[string]string{"100abc": "100abc"})

	svc := autocompleteSvc(t, d)
	got, err := svc.AutocompleteTags(context.Background(), owner, "100_%", 10, false, nil)
	r.NoError(err)
	r.Empty(got,
		`"100abc" must NOT match the prefix "100_%": underscore and percent must be escaped to literals`)

	// Positive control: a label whose first five characters are exactly
	// the user's literal input. With escaping, "100\_\%%" matches —
	// proves the helper isn't over-escaping into a pattern that matches
	// nothing.
	mPos := testutil.SeedPhoto(t, rw, owner, "pos")
	seedTagsForMedia(t, rw, mPos, map[string]string{"100_%abc": "100_%abc"})

	got, err = svc.AutocompleteTags(context.Background(), owner, "100_%", 10, false, nil)
	r.NoError(err)
	r.Len(got, 1, `literal "100_%abc" must match the escaped prefix "100\_\%%"`)
	r.Equal("100_%abc", got[0].Label)
}

// TestAutocompleteLocations_SubstringMatch — substring matching means
// "Paris" inside "Paris, France" and "Paris, Texas, USA" both surface,
// while "Berlin, Germany" stays out. Order is by frequency desc.
func TestAutocompleteLocations_SubstringMatch(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	rw := d.WriteDB()
	owner := testutil.SeedOwner(t, rw, "test-hub", "alice")

	// Two media in Paris, France (so it ranks above the other),
	// one in Paris, Texas, one in Berlin.
	m1 := testutil.SeedPhoto(t, rw, owner, "p1")
	m2 := testutil.SeedPhoto(t, rw, owner, "p2")
	m3 := testutil.SeedPhoto(t, rw, owner, "p3")
	m4 := testutil.SeedPhoto(t, rw, owner, "p4")
	setLocation(t, rw, m1, "Paris, France")
	setLocation(t, rw, m2, "Paris, France")
	setLocation(t, rw, m3, "Paris, Texas, USA")
	setLocation(t, rw, m4, "Berlin, Germany")

	svc := autocompleteSvc(t, d)
	got, err := svc.AutocompleteLocations(context.Background(), owner, "Paris", 10, false, nil)
	r.NoError(err)
	r.Len(got, 2, "Paris must match two distinct labels")

	labels := []string{got[0].Label, got[1].Label}
	r.ElementsMatch([]string{"Paris, France", "Paris, Texas, USA"}, labels)
	// "Paris, France" has 2 photos so it ranks first.
	r.Equal("Paris, France", got[0].Label)
	r.Equal(2, got[0].Count)
	r.Equal("Paris, Texas, USA", got[1].Label)
	r.Equal(1, got[1].Count)
}
