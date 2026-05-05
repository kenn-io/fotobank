// Package facets is the auth boundary for the FilterSidebar's five
// facet aggregations: Cameras, Lenses, Tags, Places (with/without
// GPS), and Media Types. Each facet runs its own query built from a
// hybrid.Resolve CTE; the Lightroom exclude-self rule clears the
// caller's selection of THAT facet before resolving so the dropdown
// continues to surface every option in the caller's library while
// the others honour the active scope.
//
// Owner scoping happens here exactly once: Aggregate stamps the
// caller principal onto hybrid.Input. Clients of this package never
// reach into the underlying CTE.
package facets

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/wesm/fotobank/internal/auth/hidden"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/search/hybrid"
)

// Filters is the auth-stamped facet input the HTTP transport assembles
// from query parameters. The caller principal is supplied separately
// to Aggregate (and stamped onto hybrid.Input there) so transport code
// can't accidentally request another owner's facets by setting an
// Owner field on the input. The Filters fields mirror /search's filter
// surface plus the four sidebar-facet selections (Cameras, Lenses,
// AnyTagKeys, HasGPS) and the MediaType bucket.
type Filters struct {
	// Sidebar facet selections. Each, when non-empty/non-nil, narrows
	// the four other facets via hybrid.Resolve; the matching facet's
	// own aggregation clears its corresponding field (exclude-self).
	Cameras    []string
	Lenses     []string
	AnyTagKeys []string
	HasGPS     *bool
	MediaType  *string

	// /search-only filters that /facets honours when present so the
	// sidebar reflects the active /search scope.
	DateAfter     *time.Time
	DateBefore    *time.Time
	TagKeys       []string // AND-composed (typed-chip resolver path)
	LocationLabel *string
	IncludeHidden bool

	// UnlockClaim, when non-nil, is the caller's hidden-unlock claim.
	// Aggregate validates it via the injected HiddenChecker before
	// honouring IncludeHidden=true; an invalid or nil claim with
	// IncludeHidden=true returns errs.ErrPermissionDenied. The gate
	// lives at the service layer (mirroring search.Service.Search) so
	// non-HTTP callers (CLI, internal jobs) cannot bypass it.
	UnlockClaim *hidden.UnlockClaim
}

// ValueCount is the (value, count) shape used by Cameras, Lenses, and
// MediaTypes. value is the canonical bucket label (e.g. "Sony A7R IV"
// or "photo"); count is the row count produced by the aggregation
// query.
type ValueCount struct {
	Value string
	Count int
}

// TagCount carries the canonical tag_key (the URL param the FilterChip
// strip and /search both bind on) plus the human-readable tag_label
// the FilterSidebar renders. Each (key, count) pair appears at most
// once; label is picked via MAX so a single canonical label survives
// when multiple ai_results rows happen to disagree on capitalisation.
type TagCount struct {
	Key   string
	Label string
	Count int
}

// PlacesCount aggregates the (lat, lng) presence into two buckets so
// the Places facet can render "with GPS" / "without GPS" toggles
// without a per-coordinate breakdown. The reverse-geocoded
// LocationLabel facet — when v2 lands — will surface as a separate
// list; this Plan A shape stays minimal.
type PlacesCount struct {
	WithGPS    int
	WithoutGPS int
}

// Response is the bundle a single Aggregate call returns. Every field
// is non-nil even when no rows match: the slices default to empty so
// the JSON transport produces "[]" rather than "null", matching how
// the existing /search response shapes its empty arrays.
type Response struct {
	Cameras    []ValueCount
	Lenses     []ValueCount
	Tags       []TagCount
	Places     PlacesCount
	MediaTypes []ValueCount
}

// facetTopN caps each list-shaped facet (Cameras, Lenses, Tags) so a
// caller with thousands of distinct cameras can't drag the response
// into the megabyte range. The MediaTypes facet has only two possible
// buckets (photo, video) so it intentionally skips the LIMIT.
const facetTopN = 200

// HiddenChecker validates an unlock claim against the caller. Returns
// true only when the claim's principal matches caller and the claim
// has not expired. The service treats a nil claim or a Valid==false
// outcome as a hard deny — the gate is fail-closed. Mirrors the
// HiddenChecker shape used by search.Service so tests and production
// wiring can share a single concrete implementation.
type HiddenChecker interface {
	Valid(claim *hidden.UnlockClaim, caller owners.Principal) bool
}

// Service is the auth-scoped facet aggregator. ro is the read-pool
// *sql.DB (writes never happen on this surface); hiddenChecker gates
// the IncludeHidden filter so non-HTTP callers cannot bypass the
// unlock check by routing around the transport. New is the only
// construction path; both fields are unexported so transport code
// can't reach in directly.
type Service struct {
	ro            *sql.DB
	hiddenChecker HiddenChecker
}

// New constructs a Service backed by the supplied read-pool handle
// and HiddenChecker. The handle is consulted exclusively from
// Aggregate (one query per facet); pass the same *sql.DB used
// elsewhere in the daemon's read pool so SQLite's WAL semantics give
// consistent snapshots across the five queries. hiddenChecker must
// be non-nil — Aggregate dereferences it on every IncludeHidden=true
// request.
func New(ro *sql.DB, hiddenChecker HiddenChecker) *Service {
	return &Service{ro: ro, hiddenChecker: hiddenChecker}
}

// Aggregate runs the five facet queries — Cameras, Lenses, Tags,
// Places, MediaTypes — against the caller's library and returns one
// bundle. Each query reuses hybrid.ResolveWhere to apply the same
// predicate set directly on `media`; the matching facet's own
// selection is cleared first so the dropdown still surfaces every
// option in the library (the Lightroom exclude-self rule). The five
// queries run concurrently via errgroup — they are independent reads
// and SQLite WAL allows concurrent readers, so wall time is bounded
// by the slowest query rather than their sum. Errors from any one
// query short-circuit the whole call and are wrapped with the facet
// name for diagnosis.
//
// IncludeHidden=true requires a valid UnlockClaim: a nil claim or a
// hiddenChecker rejection short-circuits to errs.ErrPermissionDenied
// before any query runs. The gate is fail-closed and adjacent to
// where IncludeHidden actually mutates the SQL (hybrid.ResolveWhere),
// so non-HTTP callers (CLI, internal jobs) inherit the same
// protection.
func (s *Service) Aggregate(
	ctx context.Context, caller owners.Principal, f Filters,
) (Response, error) {
	if f.IncludeHidden {
		if f.UnlockClaim == nil || !s.hiddenChecker.Valid(f.UnlockClaim, caller) {
			return Response{}, errs.ErrPermissionDenied
		}
	}
	in := s.toHybridInput(caller, f)

	var (
		cameras    []ValueCount
		lenses     []ValueCount
		tags       []TagCount
		places     PlacesCount
		mediaTypes []ValueCount
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var e error
		cameras, e = s.aggregateCameras(gctx, withoutCameras(in))
		if e != nil {
			return fmt.Errorf("facets cameras: %w", e)
		}
		return nil
	})
	g.Go(func() error {
		var e error
		lenses, e = s.aggregateLenses(gctx, withoutLenses(in))
		if e != nil {
			return fmt.Errorf("facets lenses: %w", e)
		}
		return nil
	})
	g.Go(func() error {
		var e error
		tags, e = s.aggregateTags(gctx, withoutAnyTagKeys(in))
		if e != nil {
			return fmt.Errorf("facets tags: %w", e)
		}
		return nil
	})
	g.Go(func() error {
		var e error
		places, e = s.aggregatePlaces(gctx, withoutHasGPS(in))
		if e != nil {
			return fmt.Errorf("facets places: %w", e)
		}
		return nil
	})
	g.Go(func() error {
		var e error
		mediaTypes, e = s.aggregateMediaTypes(gctx, withoutMediaType(in))
		if e != nil {
			return fmt.Errorf("facets media types: %w", e)
		}
		return nil
	})
	if err := g.Wait(); err != nil {
		return Response{}, err
	}

	return Response{
		Cameras:    cameras,
		Lenses:     lenses,
		Tags:       tags,
		Places:     places,
		MediaTypes: mediaTypes,
	}, nil
}

// toHybridInput projects a Filters into the hybrid.Input shape. The
// caller principal stamps onto Owner here (not at the transport edge)
// so a future Filters field can't leak into the wrong scope.
func (s *Service) toHybridInput(caller owners.Principal, f Filters) hybrid.Input {
	return hybrid.Input{
		Owner:         caller,
		DateAfter:     f.DateAfter,
		DateBefore:    f.DateBefore,
		TagKeys:       f.TagKeys,
		LocationLabel: f.LocationLabel,
		MediaType:     f.MediaType,
		IncludeHidden: f.IncludeHidden,
		Cameras:       f.Cameras,
		Lenses:        f.Lenses,
		AnyTagKeys:    f.AnyTagKeys,
		HasGPS:        f.HasGPS,
	}
}

// withoutCameras returns a copy of in with the Cameras selection
// cleared, used by the Cameras aggregation to honour the exclude-self
// rule.
func withoutCameras(in hybrid.Input) hybrid.Input {
	in.Cameras = nil
	return in
}

// withoutLenses clears Lenses for the Lenses aggregation.
func withoutLenses(in hybrid.Input) hybrid.Input {
	in.Lenses = nil
	return in
}

// withoutAnyTagKeys clears AnyTagKeys for the Tags aggregation.
func withoutAnyTagKeys(in hybrid.Input) hybrid.Input {
	in.AnyTagKeys = nil
	return in
}

// withoutHasGPS clears HasGPS for the Places aggregation.
func withoutHasGPS(in hybrid.Input) hybrid.Input {
	in.HasGPS = nil
	return in
}

// withoutMediaType clears MediaType for the MediaTypes aggregation.
func withoutMediaType(in hybrid.Input) hybrid.Input {
	in.MediaType = nil
	return in
}

// aggregateCameras runs the Cameras facet query: each (make, model)
// pair becomes one bucket via the same `make || ' ' || model` shape
// the Cameras filter binds against, so values round-trip cleanly
// between facet count and selection.
//
// Predicates apply directly to `media m` via hybrid.ResolveWhere — an
// earlier version wrapped the same conditions in a `filter` CTE and
// JOINed `media` back to it on PK, costing one extra B-tree lookup
// per row at 100k scale.
func (s *Service) aggregateCameras(ctx context.Context, in hybrid.Input) ([]ValueCount, error) {
	where, args := hybrid.ResolveWhere(in)
	q := fmt.Sprintf(`
SELECT (m.make || ' ' || m.model) AS value, COUNT(*) AS count
FROM media m
WHERE %s AND m.make IS NOT NULL AND m.model IS NOT NULL
GROUP BY value
ORDER BY count DESC, value ASC
LIMIT %d`, where, facetTopN)
	return scanValueCount(ctx, s.ro, q, args)
}

// aggregateLenses runs the Lenses facet query: one bucket per distinct
// lens_model. Rows with NULL lens_model are excluded so the dropdown
// doesn't surface a meaningless "no lens" bucket.
func (s *Service) aggregateLenses(ctx context.Context, in hybrid.Input) ([]ValueCount, error) {
	where, args := hybrid.ResolveWhere(in)
	q := fmt.Sprintf(`
SELECT m.lens_model AS value, COUNT(*) AS count
FROM media m
WHERE %s AND m.lens_model IS NOT NULL
GROUP BY value
ORDER BY count DESC, value ASC
LIMIT %d`, where, facetTopN)
	return scanValueCount(ctx, s.ro, q, args)
}

// aggregateTags runs the Tags facet query. Joins ai_results +
// media_tags so tags from stale generations don't surface. Uses
// COUNT(DISTINCT m.id) so a media row that carries multiple tags
// doesn't inflate any individual bucket. Uses MAX(mt.tag_label) to
// pick a canonical label per key — in practice each tag_key has a
// single label, but MAX is deterministic when one row disagrees.
func (s *Service) aggregateTags(ctx context.Context, in hybrid.Input) ([]TagCount, error) {
	where, args := hybrid.ResolveWhere(in)
	q := fmt.Sprintf(`
SELECT mt.tag_key AS key, MAX(mt.tag_label) AS label, COUNT(DISTINCT m.id) AS count
FROM media m
JOIN ai_results r ON r.media_id = m.id
                 AND r.task = 'tag' AND r.status = 'active'
JOIN media_tags mt ON mt.result_id = r.id
WHERE %s
GROUP BY mt.tag_key
ORDER BY count DESC, key ASC
LIMIT %d`, where, facetTopN)
	rows, err := s.ro.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []TagCount{}
	for rows.Next() {
		var tc TagCount
		if err := rows.Scan(&tc.Key, &tc.Label, &tc.Count); err != nil {
			return nil, err
		}
		out = append(out, tc)
	}
	return out, rows.Err()
}

// aggregatePlaces runs the Places facet query as a single SELECT with
// two FILTER-on-aggregate clauses, each scanning the filter set once.
// Requires SQLite >= 3.30 for FILTER (WHERE …); the project pins a
// recent mattn/go-sqlite3 build that more than satisfies that floor.
func (s *Service) aggregatePlaces(ctx context.Context, in hybrid.Input) (PlacesCount, error) {
	where, args := hybrid.ResolveWhere(in)
	q := fmt.Sprintf(`
SELECT
  COUNT(*) FILTER (WHERE m.latitude IS NOT NULL AND m.longitude IS NOT NULL) AS with_gps,
  COUNT(*) FILTER (WHERE m.latitude IS NULL OR m.longitude IS NULL) AS without_gps
FROM media m
WHERE %s`, where)
	var pc PlacesCount
	err := s.ro.QueryRowContext(ctx, q, args...).Scan(&pc.WithGPS, &pc.WithoutGPS)
	if err != nil {
		return PlacesCount{}, err
	}
	return pc, nil
}

// aggregateMediaTypes runs the MediaTypes facet query. Two buckets
// (photo, video) are guaranteed by the schema's CHECK constraint, so
// no LIMIT is needed.
func (s *Service) aggregateMediaTypes(ctx context.Context, in hybrid.Input) ([]ValueCount, error) {
	where, args := hybrid.ResolveWhere(in)
	q := fmt.Sprintf(`
SELECT m.media_type AS value, COUNT(*) AS count
FROM media m
WHERE %s
GROUP BY value
ORDER BY count DESC, value ASC`, where)
	return scanValueCount(ctx, s.ro, q, args)
}

// scanValueCount scans a (value, count) result set into a slice. Used
// by the Cameras, Lenses, and MediaTypes facets which share the same
// row shape.
func scanValueCount(ctx context.Context, ro *sql.DB, q string, args []any) ([]ValueCount, error) {
	rows, err := ro.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []ValueCount{}
	for rows.Next() {
		var vc ValueCount
		if err := rows.Scan(&vc.Value, &vc.Count); err != nil {
			return nil, err
		}
		out = append(out, vc)
	}
	return out, rows.Err()
}
