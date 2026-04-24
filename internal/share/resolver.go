package share

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/wesm/fotobank/internal/owners"
)

// AccessPath is one authorisation route from a caller to a specific
// target. Overlapping scopes are common and callers must OR across
// paths.
type AccessPath struct {
	ScopeUUID     string
	AlbumID       *string
	AllowDownload bool
}

// AccessDecision is the answer to a targeted access check.
type AccessDecision struct {
	Authorized bool
	Paths      []AccessPath
}

// CanDownload reports whether any retained path carries
// allow_download=true. Callers must OR across paths because the same
// caller may reach a target through multiple overlapping scopes.
func (d AccessDecision) CanDownload() bool {
	for _, p := range d.Paths {
		if p.AllowDownload {
			return true
		}
	}
	return false
}

// ResolvedScopes is the fully-materialised view used by listing
// endpoints. ScopeUUIDs is sorted ascending post-degradation so
// callers get deterministic output. Validated is the retained-owner
// slice; downstream repo calls use it as a VALUES-CTE input without
// re-querying scopes.
type ResolvedScopes struct {
	ScopeUUIDs    []string
	Owner         owners.Principal
	AllowDownload bool
	Validated     []Scope
}

// MaxHeaderScopes caps the number of distinct scope UUIDs the resolver
// considers per request. Presentations beyond the cap are truncated
// (earliest-in-input wins after dedupe).
const MaxHeaderScopes = 100

// ScopeResolver validates presented scopes and answers access
// questions. It reads only scopes / scope_media; it never touches
// media, album, or storage state.
type ScopeResolver struct {
	shares *Repo
	now    func() time.Time
}

// NewScopeResolver constructs a resolver. A nil now defaults to
// time.Now().UTC().
func NewScopeResolver(r *Repo, now func() time.Time) *ScopeResolver {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &ScopeResolver{shares: r, now: now}
}

// ResolveAll validates the presented scopes and returns the fully-
// expanded view. Header input is sanitised (dedupe, drop empty,
// drop syntactically-invalid UUIDs, cap at MaxHeaderScopes) before
// being validated by the repo. The single-owner degradation rule
// from spec §6.1 is applied to the repo result: when the validated
// set spans multiple owners, only the lexicographically smallest
// (owner_hub, owner_user_id) is retained.
func (r *ScopeResolver) ResolveAll(
	ctx context.Context,
	caller owners.Principal,
	headerScopes []string,
) (ResolvedScopes, error) {
	sanitized := sanitizeHeaderScopes(headerScopes)
	if len(sanitized) == 0 {
		return ResolvedScopes{}, nil
	}
	validated, err := r.shares.ValidateHeaderScopes(ctx, caller, sanitized, r.now())
	if err != nil {
		return ResolvedScopes{}, err
	}
	if len(validated) == 0 {
		return ResolvedScopes{}, nil
	}
	retained := retainSmallestOwner(validated)
	uuids := make([]string, 0, len(retained))
	allowAny := false
	for _, s := range retained {
		uuids = append(uuids, s.UUID)
		if s.AllowDownload {
			allowAny = true
		}
	}
	sort.Strings(uuids)
	return ResolvedScopes{
		ScopeUUIDs:    uuids,
		Owner:         retained[0].Owner,
		AllowDownload: allowAny,
		Validated:     retained,
	}, nil
}

// sanitizeHeaderScopes dedupes, caps, and drops syntactically-invalid
// UUIDs. Order is preserved so "earliest-in-input wins" after dedupe.
// Empty input returns nil.
func sanitizeHeaderScopes(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, err := uuid.Parse(s); err != nil {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
		if len(out) == MaxHeaderScopes {
			break
		}
	}
	return out
}

// retainSmallestOwner returns the subset of scopes belonging to the
// lexicographically smallest (hub, user_id) owner. See spec §6.1.
func retainSmallestOwner(scopes []Scope) []Scope {
	if len(scopes) == 0 {
		return nil
	}
	smallest := scopes[0].Owner
	for _, s := range scopes[1:] {
		if lessOwner(s.Owner, smallest) {
			smallest = s.Owner
		}
	}
	out := make([]Scope, 0, len(scopes))
	for _, s := range scopes {
		if s.Owner == smallest {
			out = append(out, s)
		}
	}
	return out
}

// lessOwner compares two principals by (hub, user_id) lexicographic
// order.
func lessOwner(a, b owners.Principal) bool {
	if a.Hub != b.Hub {
		return a.Hub < b.Hub
	}
	return a.UserID < b.UserID
}
