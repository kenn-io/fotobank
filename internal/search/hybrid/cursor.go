package hybrid

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Cursor is the opaque pagination token round-tripped between server
// and client. ReqHash binds the cursor to one specific normalized
// request shape so a client cannot re-use a cursor produced for query
// "dog" against query "cat" and accidentally page through unrelated
// rows.
//
// K1 / K2 / ID are placeholders for the per-mode last-page sort keys
// (e.g. RRF score, timestamp, media id). v1's engine populates them on
// the way out but the page-skip math is not yet wired — DecodeCursor
// returns the values for the future engine to consume. See the package
// doc on Engine.Search for the v1 simplification.
type Cursor struct {
	ReqHash string  `json:"req_hash"`
	K1      float64 `json:"k1"`
	K2      int64   `json:"k2"`
	ID      string  `json:"id"`
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
// tags) join their values with "," before going into the map.
type NormalizedReq struct {
	Q             string
	Sort          string
	IncludeHidden bool
	EngineMode    string
	Filter        map[string]string
}

// NormalizedHash returns the hex sha256 of the normalized request. The
// hash input is a single string assembled from the fields in fixed
// order with `|` separators; map keys are sorted before joining so the
// hash is independent of map iteration order. Sha256 (not a faster
// 64-bit hash) is fine — cursor hashing happens once per request and
// is not on a hot loop. The returned hash is hex-encoded so it round-
// trips through JSON without escaping concerns.
func NormalizedHash(r NormalizedReq) string {
	keys := make([]string, 0, len(r.Filter))
	for k := range r.Filter {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	sb.WriteString(r.Q)
	sb.WriteByte('|')
	sb.WriteString(r.Sort)
	sb.WriteByte('|')
	sb.WriteString(strconv.FormatBool(r.IncludeHidden))
	sb.WriteByte('|')
	sb.WriteString(r.EngineMode)
	sb.WriteByte('|')
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(r.Filter[k])
	}

	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:])
}
