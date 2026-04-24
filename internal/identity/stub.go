package identity

import (
	"context"
	"net/http"
	"slices"

	"github.com/wesm/fotobank/internal/owners"
)

// Stub is a Provider that returns a preconfigured Principal for every
// request. It is intended for local development and tests where real
// authentication is out of scope.
type Stub struct {
	principal Principal
	scopes    []string
}

// NewStub constructs a Stub that always returns the given owners.Principal
// with the supplied display handle.
func NewStub(p owners.Principal, handle string) *Stub {
	return &Stub{principal: Principal{Hub: p.Hub, UserID: p.UserID, Handle: handle}}
}

// NewStubWithScopes is a test-only constructor: the returned stub
// annotates every Identity with a fixed Scopes slice, letting handler
// tests exercise /api/v1/shared/* without spinning up the full
// header-mode chain. Production wiring never uses this — fotobank
// server only calls NewStub or identity.Header.
func NewStubWithScopes(p owners.Principal, handle string, scopes []string) *Stub {
	return &Stub{
		principal: Principal{Hub: p.Hub, UserID: p.UserID, Handle: handle},
		scopes:    slices.Clone(scopes),
	}
}

// FromRequest returns the configured principal and (if set) scopes
// without inspecting the request. The returned Scopes slice is always
// freshly cloned so callers may mutate it without perturbing later
// requests. nil-vs-empty is preserved: NewStub yields nil, and an
// explicit []string{} passed to NewStubWithScopes round-trips as a
// non-nil empty slice.
func (s *Stub) FromRequest(_ context.Context, _ *http.Request) (Identity, error) {
	return Identity{Principal: s.principal, Scopes: slices.Clone(s.scopes)}, nil
}
