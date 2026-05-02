// Package hybrid resolves a structured search request into the SQL
// fragments and bind args the index.Backend consumes. M1 supplies the
// `filter` CTE body that scopes a request to one owner's media,
// optional date range, optional tag set (AND-composed across multiple
// keys), optional location label / media type, and the hidden-row
// predicate. The CTE projects (id, timestamp, imported_at) so the
// Backend's BM25 / ANN / FilterOnly CTEs can JOIN against `f.id` and
// reuse the timestamps for sort and pagination without revisiting the
// media row.
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
// tag_keys..., location?, media_type?). IncludeHidden contributes no
// arg in either branch.
func Resolve(in Input) (cte string, args []any) {
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

	if !in.IncludeHidden {
		conds = append(conds, "m.hidden_at IS NULL")
	}

	cte = fmt.Sprintf(
		`SELECT m.id, m.timestamp, m.imported_at FROM media m WHERE %s`,
		strings.Join(conds, " AND "),
	)
	return cte, args
}
