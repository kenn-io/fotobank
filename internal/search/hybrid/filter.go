// Package hybrid resolves a structured search request into the SQL
// fragments and bind args the index.Backend consumes. M1 supplies the
// `filter` CTE body that scopes a request to one owner's media,
// optional date range, optional tag set (AND-composed across multiple
// keys), optional cameras / lenses (OR-composed), optional any-tag
// set (OR-composed), optional location label / media type, optional
// GPS-presence tri-state, and the hidden-row predicate. The CTE
// projects (id, timestamp, imported_at) so the Backend's BM25 / ANN /
// FilterOnly CTEs can JOIN against `f.id` and reuse the timestamps
// for sort and pagination without revisiting the media row.
package hybrid

import (
	"fmt"
	"strings"
	"time"

	"github.com/wesm/fotobank/internal/owners"
)

// Input is the structured request shape Resolve consumes. Every field
// except Owner is optional; the zero value of *time.Time / *string /
// nil-slice means "filter not applied". Owner is mandatory — the CTE
// always scopes to a single principal.
type Input struct {
	// Owner is the principal whose media may appear in the result set.
	// Both Hub and UserID bind into the CTE in that order.
	Owner owners.Principal
	// DateAfter, when non-nil, adds `m.timestamp >= ?` (inclusive
	// lower bound). The half-open shape (>=, <) lets a "month"-style
	// range be expressed without inclusive-end ambiguity.
	DateAfter *time.Time
	// DateBefore, when non-nil, adds `m.timestamp < ?` (exclusive
	// upper bound).
	DateBefore *time.Time
	// TagKeys lists tag stems that must all be present (AND-composed)
	// on the media. Each key adds an EXISTS subquery against
	// media_tags joined to ai_results filtered to task='tag' AND
	// status='active'; only the active generation's tags are
	// considered.
	TagKeys []string
	// LocationLabel, when non-nil, exact-matches m.location_label.
	// (FTS-style fuzzy match is the lexical signal's job, not the
	// filter's.)
	LocationLabel *string
	// MediaType, when non-nil, exact-matches m.media_type. The
	// schema's CHECK constraint pins the domain to {'photo','video'}.
	MediaType *string
	// Cameras, when non-empty, exact-matches (make || ' ' || model)
	// against any value (OR-composed). Each value adds one bind in
	// input order to the args slice.
	Cameras []string
	// Lenses, when non-empty, exact-matches lens_model against any
	// value (OR-composed). Each value adds one bind in input order.
	Lenses []string
	// AnyTagKeys lists tag stems where a media is a hit if it carries
	// ANY of them (OR-composed). Distinct from TagKeys which is
	// AND-composed across multiple typed search chips. The sidebar
	// facet drives this field; the chip resolver drives TagKeys.
	AnyTagKeys []string
	// HasGPS, when non-nil, narrows on latitude/longitude presence.
	// *true means latitude AND longitude are both NOT NULL; *false
	// means either is NULL. nil omits the predicate entirely.
	HasGPS *bool
	// IncludeHidden flips the default-on `m.hidden_at IS NULL`
	// predicate. When false (the zero value), hidden rows are
	// excluded; when true, the predicate is omitted so hidden rows
	// flow through. The unlock-claim check that gates IncludeHidden
	// happens at the service layer (see N1) — Resolve only honors the
	// already-validated input flag.
	IncludeHidden bool
}

// WithOwner returns a copy of in with Owner set to the supplied
// principal. Provided as a builder so the engine can compose an
// owner-less Input from request parsing and stamp the principal on
// just before Resolve.
func (in Input) WithOwner(p owners.Principal) Input {
	in.Owner = p
	return in
}

// WithHidden returns a copy of in with IncludeHidden set to v. The
// service layer (N1) is the gatekeeper that rejects IncludeHidden=true
// without an unlock claim; this builder only carries the flag through
// the engine pipeline.
func (in Input) WithHidden(v bool) Input {
	in.IncludeHidden = v
	return in
}

// Resolve returns the body of a `filter` CTE (without the leading
// "WITH filter AS (...)") and its bind arguments. The CTE projects
// (id, timestamp, imported_at) FROM media so the hybrid Engine can
// wrap it as a SELECT-only subquery. The arg order is deterministic
// and load-bearing: hub, userID, then (date_after?, date_before?,
// tag_keys..., location?, media_type?, cameras..., lenses...,
// any_tag_keys...). HasGPS and IncludeHidden contribute no arg in
// either branch.
func Resolve(in Input) (cte string, args []any) {
	where, args := ResolveWhere(in)
	cte = fmt.Sprintf(
		`SELECT m.id, m.timestamp, m.imported_at FROM media m WHERE %s`,
		where,
	)
	return cte, args
}

// ResolveWhere returns the same predicate body as Resolve but without
// the `SELECT m.id, m.timestamp, m.imported_at FROM media m WHERE`
// wrapper, so callers that already join `media m` directly (the facets
// aggregations) can apply the predicates inline instead of materialising
// a `filter` CTE and joining `media` back to it on PK. The conditions
// are joined with " AND " and bind to columns on alias `m`. Argument
// order matches Resolve.
func ResolveWhere(in Input) (where string, args []any) {
	conds := []string{"m.owner_hub = ?", "m.owner_user_id = ?"}
	args = []any{in.Owner.Hub, in.Owner.UserID}

	if in.DateAfter != nil {
		conds = append(conds, "m.timestamp >= ?")
		args = append(args, *in.DateAfter)
	}
	if in.DateBefore != nil {
		conds = append(conds, "m.timestamp < ?")
		args = append(args, *in.DateBefore)
	}

	for _, key := range in.TagKeys {
		conds = append(conds,
			`EXISTS (SELECT 1 FROM media_tags mt
                      JOIN ai_results r ON mt.result_id = r.id
                     WHERE r.media_id = m.id AND r.task = 'tag' AND r.status = 'active'
                       AND mt.tag_key = ?)`,
		)
		args = append(args, key)
	}

	if in.LocationLabel != nil {
		conds = append(conds, "m.location_label = ?")
		args = append(args, *in.LocationLabel)
	}
	if in.MediaType != nil {
		conds = append(conds, "m.media_type = ?")
		args = append(args, *in.MediaType)
	}

	if len(in.Cameras) > 0 {
		conds = append(conds,
			fmt.Sprintf("(m.make || ' ' || m.model) IN (%s)", placeholders(len(in.Cameras))))
		for _, v := range in.Cameras {
			args = append(args, v)
		}
	}

	if len(in.Lenses) > 0 {
		conds = append(conds,
			fmt.Sprintf("m.lens_model IN (%s)", placeholders(len(in.Lenses))))
		for _, v := range in.Lenses {
			args = append(args, v)
		}
	}

	if len(in.AnyTagKeys) > 0 {
		conds = append(conds, fmt.Sprintf(
			`EXISTS (SELECT 1 FROM media_tags mt
                      JOIN ai_results r ON mt.result_id = r.id
                     WHERE r.media_id = m.id AND r.task = 'tag' AND r.status = 'active'
                       AND mt.tag_key IN (%s))`,
			placeholders(len(in.AnyTagKeys))))
		for _, v := range in.AnyTagKeys {
			args = append(args, v)
		}
	}

	if in.HasGPS != nil {
		if *in.HasGPS {
			conds = append(conds, "m.latitude IS NOT NULL AND m.longitude IS NOT NULL")
		} else {
			conds = append(conds, "(m.latitude IS NULL OR m.longitude IS NULL)")
		}
	}

	if !in.IncludeHidden {
		conds = append(conds, "m.hidden_at IS NULL")
	}

	return strings.Join(conds, " AND "), args
}

// placeholders returns "?, ?, ..., ?" with n question marks. Used by
// the IN-list cond emitters; n must be > 0.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	out := strings.Repeat("?, ", n)
	return out[:len(out)-2]
}
