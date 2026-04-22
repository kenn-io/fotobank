package identity

import (
	"context"
	"net/http"

	"github.com/wesm/fotobank/internal/owners"
)

// Stub is a Provider that returns a preconfigured Principal for every
// request. It is intended for local development and tests where real
// authentication is out of scope.
type Stub struct {
	principal Principal
}

// NewStub constructs a Stub that always returns the given owners.Principal
// with the supplied display handle.
func NewStub(p owners.Principal, handle string) *Stub {
	return &Stub{principal: Principal{Hub: p.Hub, UserID: p.UserID, Handle: handle}}
}

// FromRequest returns the configured principal without inspecting the
// request. The context and request are accepted to satisfy the Provider
// interface but are ignored.
func (s *Stub) FromRequest(_ context.Context, _ *http.Request) (Identity, error) {
	return Identity{Principal: s.principal}, nil
}
