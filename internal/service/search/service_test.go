package search_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/auth/hidden"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/search/hybrid"
	searchsvc "github.com/wesm/fotobank/internal/service/search"
)

// fakeEngine captures the hybrid.Request the service emits and returns
// a canned hybrid.Response. The captured request is what every test
// asserts against.
type fakeEngine struct {
	lastReq hybrid.Request
	resp    hybrid.Response
	err     error
}

func (f *fakeEngine) Search(_ context.Context, req hybrid.Request) (hybrid.Response, error) {
	f.lastReq = req
	if f.err != nil {
		return hybrid.Response{}, f.err
	}
	return f.resp, nil
}

// fakeSettings backs UserSettingsRepo. on is the value AIInspectionEnabled
// returns; err is what to return on the read attempt (covers the "read
// settings" failure path).
type fakeSettings struct {
	on  bool
	err error
}

func (f *fakeSettings) AIInspectionEnabled(_ context.Context, _ owners.Principal) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.on, nil
}

// fakeTagResolver maps labels through a pre-configured table. lastLabels
// captures the input slice; lastCaller pins the principal the service
// passed through. err lets a test exercise the error wrap path.
type fakeTagResolver struct {
	mapping    map[string]string
	lastLabels []string
	lastCaller owners.Principal
	err        error
}

func (f *fakeTagResolver) LabelsToKeys(_ context.Context, caller owners.Principal, labels []string) ([]string, error) {
	f.lastLabels = labels
	f.lastCaller = caller
	if f.err != nil {
		return nil, f.err
	}
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		if k, ok := f.mapping[l]; ok {
			out = append(out, k)
			continue
		}
		out = append(out, l) // pass-through; engine yields zero rows on unknown key
	}
	return out, nil
}

// fakeHiddenChecker drives the unlock-claim gate. valid is what Valid
// returns regardless of the claim contents — tests aim at the
// service's branching, not the checker's internal contract.
type fakeHiddenChecker struct {
	valid bool
}

func (f *fakeHiddenChecker) Valid(_ *hidden.UnlockClaim, _ owners.Principal) bool {
	return f.valid
}

// testCaller is the principal threaded through the service tests. The
// values are arbitrary; tests assert on identity rather than content.
var testCaller = owners.Principal{Hub: "test-hub", UserID: "alice"}

// newSvc bundles a default-wired service plus its captures. Tests
// override individual fakes via the returned struct before calling
// Search.
type svcFixture struct {
	svc      *searchsvc.Service
	engine   *fakeEngine
	settings *fakeSettings
	tags     *fakeTagResolver
	checker  *fakeHiddenChecker
}

func newFixture() svcFixture {
	eng := &fakeEngine{resp: hybrid.Response{}}
	st := &fakeSettings{on: false}
	tr := &fakeTagResolver{mapping: map[string]string{}}
	hc := &fakeHiddenChecker{valid: false}
	// gens and ro are nil — Search never consults either, so the
	// existing routing-pipeline tests don't need a real DB. The
	// completeness tests in completeness_test.go wire real values.
	return svcFixture{
		svc:      searchsvc.New(eng, st, tr, hc, nil, nil),
		engine:   eng,
		settings: st,
		tags:     tr,
		checker:  hc,
	}
}

// TestService_RejectsIncludeHiddenWithoutUnlockClaim — IncludeHidden=true
// with a nil UnlockClaim must short-circuit to ErrPermissionDenied
// before the engine is invoked. The hidden gate is fail-closed.
func TestService_RejectsIncludeHiddenWithoutUnlockClaim(t *testing.T) {
	r := require.New(t)
	fx := newFixture()
	// checker.valid=true would not save us — the nil claim must fail
	// the gate on its own. Set valid=true to prove that.
	fx.checker.valid = true

	_, err := fx.svc.Search(context.Background(), testCaller, searchsvc.Request{
		IncludeHidden: true,
		UnlockClaim:   nil,
	})

	r.ErrorIs(err, errs.ErrPermissionDenied)
	r.Equal(hybrid.Request{}, fx.engine.lastReq, "engine must not be invoked when hidden gate denies")
}

// TestService_RejectsIncludeHiddenWhenCheckerReturnsFalse — same shape
// but with a non-nil claim and a checker that rejects it (e.g.
// expired or wrong principal). Still ErrPermissionDenied; engine still
// untouched.
func TestService_RejectsIncludeHiddenWhenCheckerReturnsFalse(t *testing.T) {
	r := require.New(t)
	fx := newFixture()
	fx.checker.valid = false
	claim := &hidden.UnlockClaim{Principal: testCaller, ExpiresAt: time.Now().Add(time.Hour)}

	_, err := fx.svc.Search(context.Background(), testCaller, searchsvc.Request{
		IncludeHidden: true,
		UnlockClaim:   claim,
	})

	r.ErrorIs(err, errs.ErrPermissionDenied)
	r.Equal(hybrid.Request{}, fx.engine.lastReq, "engine must not be invoked when checker rejects")
}

// TestService_OwnerScopingIsAlwaysEnforced — even when IncludeHidden is
// false (no unlock-claim path), the engine receives the caller as
// Owner. The service must never honour an owner field from anywhere
// else; the caller principal is the single source of truth.
func TestService_OwnerScopingIsAlwaysEnforced(t *testing.T) {
	r := require.New(t)
	fx := newFixture()

	_, err := fx.svc.Search(context.Background(), testCaller, searchsvc.Request{
		IncludeHidden: false,
		Query:         "puppy",
	})

	r.NoError(err)
	r.Equal(testCaller, fx.engine.lastReq.Owner, "engine.Owner must be the caller principal")
	r.False(fx.engine.lastReq.IncludeHidden, "IncludeHidden must propagate as supplied")
}

// TestService_OwnerScopingPropagatesUnderUnlock — the unlock-claim path
// must also pin the caller, even when claim.Principal differs (defense
// in depth: a confused caller cannot inject a different owner via the
// claim payload). The checker is what validates the principal match;
// the service binds caller into engine.Owner regardless.
func TestService_OwnerScopingPropagatesUnderUnlock(t *testing.T) {
	r := require.New(t)
	fx := newFixture()
	fx.checker.valid = true
	other := owners.Principal{Hub: "test-hub", UserID: "bob"}
	claim := &hidden.UnlockClaim{Principal: other, ExpiresAt: time.Now().Add(time.Hour)}

	_, err := fx.svc.Search(context.Background(), testCaller, searchsvc.Request{
		IncludeHidden: true,
		UnlockClaim:   claim,
	})

	r.NoError(err)
	r.Equal(testCaller, fx.engine.lastReq.Owner, "engine.Owner must be caller, not claim.Principal")
	r.True(fx.engine.lastReq.IncludeHidden)
}

// TestService_ResolvesTagChipsToTagKey — TagLabels=["Dog"] flows
// through the resolver and reaches the engine as TagKeys=["dog"]. The
// resolver also receives caller so per-library label tables can be
// honoured by future implementations.
func TestService_ResolvesTagChipsToTagKey(t *testing.T) {
	r := require.New(t)
	fx := newFixture()
	fx.tags.mapping = map[string]string{"Dog": "dog", "Beach": "beach"}

	_, err := fx.svc.Search(context.Background(), testCaller, searchsvc.Request{
		TagLabels: []string{"Dog", "Beach"},
	})

	r.NoError(err)
	r.Equal(testCaller, fx.tags.lastCaller, "resolver must receive caller for owner-scoped lookup")
	r.Equal([]string{"Dog", "Beach"}, fx.tags.lastLabels)
	r.Equal([]string{"dog", "beach"}, fx.engine.lastReq.Filter.TagKeys, "engine sees canonical keys")
}

// TestService_PassesThroughExplainOnlyWhenSettingsAllow — Explain=true
// in the request is suppressed when AI Inspection is off; preserved
// when it is on. A settings lookup error must propagate, never silently
// flip explain to false.
func TestService_PassesThroughExplainOnlyWhenSettingsAllow(t *testing.T) {
	t.Run("settings off suppresses explain", func(t *testing.T) {
		r := require.New(t)
		fx := newFixture()
		fx.settings.on = false

		_, err := fx.svc.Search(context.Background(), testCaller, searchsvc.Request{
			Explain: true,
		})

		r.NoError(err)
		r.False(fx.engine.lastReq.Explain, "explain must be off when settings are off")
	})

	t.Run("settings on preserves explain", func(t *testing.T) {
		r := require.New(t)
		fx := newFixture()
		fx.settings.on = true

		_, err := fx.svc.Search(context.Background(), testCaller, searchsvc.Request{
			Explain: true,
		})

		r.NoError(err)
		r.True(fx.engine.lastReq.Explain, "explain must propagate when settings are on")
	})

	t.Run("explain=false bypasses settings lookup", func(t *testing.T) {
		// When the request did not ask for diagnostics, the settings
		// read is skipped — exercise that by having the fake error and
		// asserting Search still succeeds.
		r := require.New(t)
		fx := newFixture()
		fx.settings.err = errors.New("boom")

		_, err := fx.svc.Search(context.Background(), testCaller, searchsvc.Request{
			Explain: false,
		})

		r.NoError(err, "settings.err must not surface when Explain is false")
		r.False(fx.engine.lastReq.Explain)
	})

	t.Run("settings error propagates", func(t *testing.T) {
		// Explain=true plus a settings lookup error must surface the
		// error rather than silently flipping explain off — the caller
		// needs to know diagnostics could not be evaluated.
		r := require.New(t)
		fx := newFixture()
		fx.settings.err = errors.New("settings store down")

		_, err := fx.svc.Search(context.Background(), testCaller, searchsvc.Request{
			Explain: true,
		})

		r.Error(err)
		r.Equal(hybrid.Request{}, fx.engine.lastReq, "engine must not be invoked on settings error")
	})
}
