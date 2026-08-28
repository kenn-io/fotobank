package share

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"

	"go.kenn.io/fotobank/internal/owners"
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
// re-querying scopes. Order is unspecified; callers who need a stable
// order sort themselves.
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
	logger *slog.Logger
}

// NewScopeResolver constructs a resolver. A nil now defaults to
// time.Now().UTC(). A nil logger defaults to slog.Default().
func NewScopeResolver(r *Repo, now func() time.Time, logger *slog.Logger) *ScopeResolver {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ScopeResolver{shares: r, now: now, logger: logger}
}

// ResolveAll validates the presented scopes and returns the fully-
// expanded view. Header input is sanitised (dedupe, drop empty,
// drop syntactically-invalid UUIDs, cap at MaxHeaderScopes) before
// being validated by the repo. When the validated set spans multiple
// owners, only the lexicographically smallest
// (owner_hub, owner_user_id) is retained.
func (r *ScopeResolver) ResolveAll(
	ctx context.Context,
	caller owners.Principal,
	headerScopes []string,
) (ResolvedScopes, error) {
	retained, owner, err := r.validateAndRetain(ctx, caller, headerScopes)
	if err != nil {
		return ResolvedScopes{}, err
	}
	if len(retained) == 0 {
		return ResolvedScopes{}, nil
	}
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
		Owner:         owner,
		AllowDownload: allowAny,
		Validated:     retained,
	}, nil
}

// CheckMediaAccess answers "can caller see media mediaID via one of
// these presented scopes?" It first sanitizes, validates, and retains one
// owner, then runs the per-item coverage query.
func (r *ScopeResolver) CheckMediaAccess(
	ctx context.Context,
	caller owners.Principal,
	headerScopes []string,
	mediaID string,
) (AccessDecision, error) {
	retained, owner, err := r.validateAndRetain(ctx, caller, headerScopes)
	if err != nil {
		return AccessDecision{}, err
	}
	if len(retained) == 0 {
		return AccessDecision{}, nil
	}
	return r.shares.CoverMediaByScopes(ctx, retained, owner, mediaID)
}

// CheckAlbumAccess answers "can caller see album albumID's metadata
// and contents?" It checks album_live only — a media_set scope does
// not imply album visibility even if its membership happens to belong
// to that album.
func (r *ScopeResolver) CheckAlbumAccess(
	ctx context.Context,
	caller owners.Principal,
	headerScopes []string,
	albumID string,
) (AccessDecision, error) {
	retained, owner, err := r.validateAndRetain(ctx, caller, headerScopes)
	if err != nil {
		return AccessDecision{}, err
	}
	if len(retained) == 0 {
		return AccessDecision{}, nil
	}
	return r.shares.CoverAlbumByScopes(ctx, retained, owner, albumID)
}

// validateAndRetain runs pass 1 of the resolver: sanitize + validate +
// single-owner degradation. Returns the retained-owner slice plus the
// retained owner principal. The warn log is emitted when degradation
// fires. Callers with no surviving scopes receive (nil, zero, nil).
func (r *ScopeResolver) validateAndRetain(
	ctx context.Context, caller owners.Principal, headerScopes []string,
) ([]Scope, owners.Principal, error) {
	sanitized := sanitizeHeaderScopes(headerScopes)
	if len(sanitized) == 0 {
		return nil, owners.Principal{}, nil
	}
	validated, err := r.shares.ValidateHeaderScopes(ctx, caller, sanitized, r.now())
	if err != nil {
		return nil, owners.Principal{}, err
	}
	if len(validated) == 0 {
		return nil, owners.Principal{}, nil
	}
	distinct := collectDistinctOwners(validated)
	retained := retainSmallestOwner(validated)
	if len(retained) == 0 {
		return nil, owners.Principal{}, nil
	}
	if len(distinct) > 1 {
		r.logMultiOwnerDegradation(caller, retained[0].Owner, distinct)
	}
	return retained, retained[0].Owner, nil
}

// logMultiOwnerDegradation warns when a multi-owner presentation is reduced
// to one retained owner.
func (r *ScopeResolver) logMultiOwnerDegradation(
	caller, retained owners.Principal,
	distinctOwners []owners.Principal,
) {
	dropped := make([]string, 0, len(distinctOwners)-1)
	for _, o := range distinctOwners {
		if o == retained {
			continue
		}
		dropped = append(dropped, o.Hub+"/"+o.UserID)
	}
	sort.Strings(dropped)
	r.logger.Warn("share: multi-owner scope presentation; dropping non-retained owners",
		"caller_hub", caller.Hub,
		"caller_user_id", caller.UserID,
		"retained_owner_hub", retained.Hub,
		"retained_owner_user_id", retained.UserID,
		"dropped_owners", dropped,
	)
}

// collectDistinctOwners returns the set of distinct owners present in
// scopes. Order matches first-seen in the input.
func collectDistinctOwners(scopes []Scope) []owners.Principal {
	if len(scopes) == 0 {
		return nil
	}
	seen := make(map[owners.Principal]struct{}, len(scopes))
	out := make([]owners.Principal, 0, len(scopes))
	for _, s := range scopes {
		if _, ok := seen[s.Owner]; ok {
			continue
		}
		seen[s.Owner] = struct{}{}
		out = append(out, s.Owner)
	}
	return out
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
// lexicographically smallest (hub, user_id) owner.
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
