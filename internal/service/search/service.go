// Package search is the auth boundary for hybrid search. The repo and
// engine layers (internal/search/hybrid, internal/search/index) are
// owner-agnostic by design — they take an Owner principal as a
// straight bind value and never reach for it from anywhere else. This
// service is what stamps the caller principal onto every request,
// enforces the hidden-unlock gate, resolves tag-label chips into
// tag-key bind values, and gates the diagnostics flag behind the
// per-caller AI Inspection setting.
//
// The package name "search" intentionally collides with internal/search
// (the [search] config package). Distinct import paths
// — internal/service/search vs internal/search — keep them
// disambiguated; callers that import both alias one of them.
package search

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/wesm/fotobank/internal/ai/embedding"
	"github.com/wesm/fotobank/internal/auth/hidden"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/search/hybrid"
)

// EngineIface is the subset of *hybrid.Engine that the service uses.
// Defined as an interface so tests can substitute a fake that captures
// the hybrid.Request the service emits without spinning up a real
// backend / embedding client. *hybrid.Engine satisfies EngineIface in
// production.
type EngineIface interface {
	Search(ctx context.Context, req hybrid.Request) (hybrid.Response, error)
}

// Compile-time check: *hybrid.Engine satisfies EngineIface. A
// regression in either signature lights up at build time rather than
// at the boot wiring.
var _ EngineIface = (*hybrid.Engine)(nil)

// UserSettingsRepo is the typed accessor the service needs from the
// user-settings store. AIInspectionEnabled returns the per-caller
// boolean for the "ai.inspection" key (default false when the row is
// absent). The plan adds a concrete implementation alongside the
// SettingsAI toggle work; tests here use a fake.
type UserSettingsRepo interface {
	AIInspectionEnabled(ctx context.Context, caller owners.Principal) (bool, error)
}

// TagResolver maps user-typed tag chip labels (e.g. "Dog") to canonical
// tag_key bind values (e.g. "dog") for the caller's library. The
// engine's filter CTE matches on tag_key with exact equality, so chip
// labels must be canonicalized before they reach the engine.
//
// Behaviour for unresolvable labels is implementation-defined but must
// be deterministic: the v1 expectation is that unresolved labels are
// passed through (the autocomplete pipeline only commits chips for
// labels that exist, so the unresolved branch is a defense-in-depth
// case rather than a routine occurrence). A label with no matching
// media simply yields zero hits via the EXISTS predicate's exact
// tag_key match — there is no silent drop.
type TagResolver interface {
	LabelsToKeys(ctx context.Context, caller owners.Principal, labels []string) ([]string, error)
}

// HiddenChecker validates an unlock claim against the caller. Returns
// true only when the claim's principal matches caller and the claim
// has not expired. The service treats a nil claim or a Valid==false
// outcome as a hard deny — the gate is fail-closed.
type HiddenChecker interface {
	Valid(claim *hidden.UnlockClaim, caller owners.Principal) bool
}

// Service is the auth-scoped search wrapper. Fields are unexported so
// the only construction path is New, which accepts the dependencies
// the production wiring assembles at boot. Engine, hiddenChecker, and
// the two repos are all required; passing nil dereferences at call
// time on the dependent code path.
//
// gens and ro back EmbeddingCompleteness: gens resolves the active
// generation row (the numerator's generation_id bind), ro runs the two
// COUNT queries against the read pool. The Search path does not consult
// either field — tests targeting the routing pipeline can pass nil for
// both. EmbeddingCompleteness dereferences gens, so its tests must wire
// a real *embedding.Generations.
type Service struct {
	engine        EngineIface
	usersettings  UserSettingsRepo
	tagResolver   TagResolver
	hiddenChecker HiddenChecker
	gens          *embedding.Generations
	ro            *sql.DB
}

// New constructs a Service from its collaborators. gens and ro are
// only consulted by EmbeddingCompleteness; callers that exercise only
// Search may pass nil for both. Production wiring supplies all six.
func New(
	engine EngineIface,
	settings UserSettingsRepo,
	tags TagResolver,
	hc HiddenChecker,
	gens *embedding.Generations,
	ro *sql.DB,
) *Service {
	return &Service{
		engine:        engine,
		usersettings:  settings,
		tagResolver:   tags,
		hiddenChecker: hc,
		gens:          gens,
		ro:            ro,
	}
}

// Request is the service-facing search request shape. Owner is
// intentionally absent — the caller principal supplied to Search is
// the only owner ever bound into the engine query, so a malicious or
// confused caller cannot widen the scope by stuffing a principal into
// the request body. TagLabels are user-typed chip labels; the service
// resolves them to canonical tag_keys before invoking the engine.
type Request struct {
	Query         string
	Sort          string
	DateAfter     *time.Time
	DateBefore    *time.Time
	TagLabels     []string
	LocationLabel *string
	MediaType     *string
	IncludeHidden bool
	Limit         int
	Cursor        string
	Explain       bool
	UnlockClaim   *hidden.UnlockClaim
}

// Search runs the full caller-scoped pipeline:
//
//  1. Hidden gate: IncludeHidden=true requires a valid UnlockClaim. A
//     nil claim or a checker rejection returns ErrPermissionDenied.
//     Fail-closed: any failure path denies.
//  2. Tag resolution: TagLabels are resolved to tag_keys. A resolver
//     error is wrapped and propagated.
//  3. Diagnostics gate: Explain=true is honoured only when the caller
//     has AI Inspection enabled. A settings lookup error is wrapped
//     and propagated; the caller sees the failure rather than a
//     silently-disabled diagnostic flag.
//  4. Build hybrid.Request with caller pinned as Owner and forward to
//     the engine.
func (s *Service) Search(ctx context.Context, caller owners.Principal, req Request) (hybrid.Response, error) {
	if req.IncludeHidden {
		if req.UnlockClaim == nil || !s.hiddenChecker.Valid(req.UnlockClaim, caller) {
			return hybrid.Response{}, errs.ErrPermissionDenied
		}
	}

	tagKeys, err := s.tagResolver.LabelsToKeys(ctx, caller, req.TagLabels)
	if err != nil {
		return hybrid.Response{}, fmt.Errorf("resolve tags: %w", err)
	}

	explain := req.Explain
	if explain {
		on, sErr := s.usersettings.AIInspectionEnabled(ctx, caller)
		if sErr != nil {
			return hybrid.Response{}, fmt.Errorf("read settings: %w", sErr)
		}
		if !on {
			explain = false
		}
	}

	hreq := hybrid.Request{
		Owner: caller,
		Query: req.Query,
		Sort:  req.Sort,
		Filter: hybrid.Input{
			DateAfter:     req.DateAfter,
			DateBefore:    req.DateBefore,
			TagKeys:       tagKeys,
			LocationLabel: req.LocationLabel,
			MediaType:     req.MediaType,
		},
		IncludeHidden: req.IncludeHidden,
		Limit:         req.Limit,
		Cursor:        req.Cursor,
		Explain:       explain,
	}

	return s.engine.Search(ctx, hreq)
}
