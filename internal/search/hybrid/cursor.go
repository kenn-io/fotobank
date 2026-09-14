package hybrid

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"go.kenn.io/fotobank/internal/owners"
)

// Cursor is the opaque pagination token round-tripped between server
// and client. ReqHash binds the cursor to one specific normalized
// request shape so a client cannot re-use a cursor produced for query
// "dog" against query "cat" and accidentally page through unrelated
// rows.
//
// Offset counts results already returned. Pages are live reads; changes to
// the catalog between requests can shift results.
type Cursor struct {
	ReqHash string `json:"req_hash"`
	Offset  int    `json:"offset"`
}

// EncodeCursor renders c as a URL-safe base64-encoded JSON blob. The
// RawURLEncoding form drops padding so cursors compose cleanly into
// query strings without %3D escaping.
//
// Marshal failure on a fixed-shape struct of primitives is effectively
// impossible; we return the empty string in that pathological case
// rather than panic so the engine's "produce no cursor when something
// went wrong" branch stays honest.
func EncodeCursor(c Cursor) string {
	b, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor parses a cursor string produced by EncodeCursor. An
// empty input is rejected with a non-nil error so callers don't have
// to special-case that themselves; the engine treats an empty cursor
// at the request level as "first page" before ever calling Decode.
func DecodeCursor(s string) (Cursor, error) {
	if s == "" {
		return Cursor{}, fmt.Errorf("cursor: empty")
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, fmt.Errorf("cursor: base64: %w", err)
	}
	var c Cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return Cursor{}, fmt.Errorf("cursor: json: %w", err)
	}
	return c, nil
}

// DecodeCursorAndCheck is the engine-side helper: decode the cursor
// then refuse to honor it unless ReqHash matches the freshly-computed
// hash of the current request. Mismatch most likely means the client
// changed Q / Sort / Filter between page 1 and page 2 — handing a
// stale cursor would page through a result set that no longer matches
// the visible query, which is worse than 400-ing the request.
func DecodeCursorAndCheck(s, expectedHash string) (Cursor, error) {
	c, err := DecodeCursor(s)
	if err != nil {
		return Cursor{}, err
	}
	if c.ReqHash != expectedHash {
		return Cursor{}, fmt.Errorf("cursor: req hash mismatch")
	}
	return c, nil
}

// NormalizedReq is the canonical projection of a search request the
// cursor binds to. EngineMode and Sort are the *effective* values the
// engine resolved (e.g. Sort=relevance with empty Q has been coerced
// to "newest") so two requests that differ only in inputs the engine
// folds together produce the same hash.
//
// Filter is a flat string→string map; the production caller flattens
// the structured Input before hashing so each filter (date_after,
// tag, media_type, …) appears as one key. Multi-valued keys (e.g.
// tags) encode sorted values as JSON arrays before going into the map.
type NormalizedReq struct {
	Q             string
	Sort          string
	IncludeHidden bool
	EngineMode    string
	Filter        map[string]string
	Owner         owners.Principal
	GenerationID  int64
	KPerSignal    int
	RRFK          int
}

// NormalizedHash binds the request without delimiter ambiguity. The JSON
// encoder sorts map keys; flattenFilter sorts multi-valued fields.
func NormalizedHash(r NormalizedReq) string {
	encoded, _ := json.Marshal(r)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
