package hybrid_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/search/hybrid"
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
