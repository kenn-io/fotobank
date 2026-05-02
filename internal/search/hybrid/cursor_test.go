package hybrid_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/embedding"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/search/hybrid"
	"github.com/wesm/fotobank/internal/search/index"
	"github.com/wesm/fotobank/internal/testutil"
)

// TestCursor_RoundTrip pins the encode/decode contract: every field
// the engine populates survives the base64+json round-trip with the
// same value. The float and int are deliberately non-zero so a
// silently dropped field would surface as a zero on decode.
func TestCursor_RoundTrip(t *testing.T) {
	r := require.New(t)
	in := hybrid.Cursor{
		ReqHash: "abcdef1234567890",
		K1:      0.0125,
		K2:      1730000000,
		ID:      "media-123",
	}

	enc := hybrid.EncodeCursor(in)
	r.NotEmpty(enc)

	out, err := hybrid.DecodeCursor(enc)
	r.NoError(err)
	r.Equal(in, out)
}

// TestCursor_RejectsHashMismatch confirms DecodeCursorAndCheck refuses
// to hand back a cursor whose ReqHash does not match the caller's
// expected hash. This is the defence that prevents a client from
// pasting page 1's cursor onto a page-2 request whose Q / Sort /
// Filter has shifted in the meantime.
func TestCursor_RejectsHashMismatch(t *testing.T) {
	r := require.New(t)
	enc := hybrid.EncodeCursor(hybrid.Cursor{
		ReqHash: "hash-from-page-1",
		K1:      1.0,
		K2:      42,
		ID:      "m-7",
	})

	_, err := hybrid.DecodeCursorAndCheck(enc, "different-hash-from-page-2")
	r.Error(err)
	r.Contains(err.Error(), "req hash mismatch")

	// Sanity: the same cursor decodes cleanly when the hash matches.
	c, err := hybrid.DecodeCursorAndCheck(enc, "hash-from-page-1")
	r.NoError(err)
	r.Equal("m-7", c.ID)
}

// TestCursor_ReqHashUsesEffectiveSort pins the contract that the hash
// is over the *effective* request — two requests that differ only in
// inputs the engine folds together (e.g. raw Sort=relevance with empty
// Q is coerced to Sort=newest before hashing) must produce the same
// hash, so a server-side coercion does not invalidate the cursor on
// the next page.
//
// We construct two NormalizedReq values with identical effective
// fields and confirm NormalizedHash returns the same digest. The
// negative half of the contract (different effective fields → different
// hashes) is also exercised so the test catches a regression that
// flattens the hash to a constant.
func TestCursor_ReqHashUsesEffectiveSort(t *testing.T) {
	r := require.New(t)

	a := hybrid.NormalizedReq{
		Q:             "dog",
		Sort:          "newest",
		IncludeHidden: false,
		EngineMode:    "bm25_only",
		Filter:        map[string]string{"media_type": "photo"},
	}
	b := hybrid.NormalizedReq{
		Q:             "dog",
		Sort:          "newest",
		IncludeHidden: false,
		EngineMode:    "bm25_only",
		Filter:        map[string]string{"media_type": "photo"},
	}
	r.Equal(hybrid.NormalizedHash(a), hybrid.NormalizedHash(b))

	// Map-iteration-order independence: extra key/value pairs added in
	// arbitrary order produce the same hash because the hash sorts
	// keys before joining.
	c1 := hybrid.NormalizedReq{
		Q:             "dog",
		Sort:          "newest",
		IncludeHidden: false,
		EngineMode:    "bm25_only",
		Filter:        map[string]string{"a": "1", "b": "2", "c": "3"},
	}
	c2 := hybrid.NormalizedReq{
		Q:             "dog",
		Sort:          "newest",
		IncludeHidden: false,
		EngineMode:    "bm25_only",
		Filter:        map[string]string{"c": "3", "a": "1", "b": "2"},
	}
	r.Equal(hybrid.NormalizedHash(c1), hybrid.NormalizedHash(c2))

	// Differing effective sort does shift the hash — a defence against
	// a regression that hashes a constant.
	d := hybrid.NormalizedReq{
		Q:             "dog",
		Sort:          "oldest",
		IncludeHidden: false,
		EngineMode:    "bm25_only",
		Filter:        map[string]string{"media_type": "photo"},
	}
	r.NotEqual(hybrid.NormalizedHash(a), hybrid.NormalizedHash(d))
}

// TestNormalizedHash_TagKeysOrderIndependent pins the tag-key
// permutation invariant: two engine requests that differ only in the
// order of TagKeys must produce the same cursor hash. The engine
// flattens TagKeys into the "tags" entry of the NormalizedReq.Filter
// map by joining sorted keys; without the sort, page 1's hash would
// not match page 2's if the resolver re-emitted the same labels in a
// different slice order.
//
// Driven via the engine end-to-end so the test is faithful to the
// production hashing path (engine.flattenFilter → NormalizedHash) —
// asserting on engine.Search's NextCursor catches any regression
// that disagrees with the input flattening, not just NormalizedHash
// itself.
func TestNormalizedHash_TagKeysOrderIndependent(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	row, err := gens.FindOrCreateBuilding(context.Background(), ai.Fingerprint{
		ModelID:      "test-model",
		InputProfile: "test-profile",
	}, 768)
	r.NoError(err)
	r.NoError(gens.Promote(context.Background(), row.ID))

	be := &fakeBackend{hits: []index.Hit{
		{MediaID: "m1", Score: 0.9},
		{MediaID: "m2", Score: 0.8},
	}}
	tc := &fakeTextClient{vec: make([]float32, 768)}
	eng := hybrid.NewEngine(be, tc, gens, engineCfg())

	owner := owners.Principal{Hub: "h", UserID: "u"}

	respA, err := eng.Search(context.Background(), hybrid.Request{
		Owner: owner,
		Query: "puppy",
		Sort:  "relevance",
		Limit: 2,
		Filter: hybrid.Input{
			TagKeys: []string{"beach", "sunset"},
		},
	})
	r.NoError(err)
	r.NotEmpty(respA.NextCursor, "page 1 must mint a cursor when len(hits)==Limit")

	respB, err := eng.Search(context.Background(), hybrid.Request{
		Owner: owner,
		Query: "puppy",
		Sort:  "relevance",
		Limit: 2,
		Filter: hybrid.Input{
			// Same set, opposite order. The cursor hash must be
			// identical so a client that paginates with this cursor
			// against a re-shuffled chip set on the next page does
			// not get a 400.
			TagKeys: []string{"sunset", "beach"},
		},
	})
	r.NoError(err)
	r.Equal(respA.NextCursor, respB.NextCursor,
		"TagKeys permutation must not shift the cursor hash")
}
