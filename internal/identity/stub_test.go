package identity_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/identity"
	"go.kenn.io/fotobank/internal/owners"
)

func TestStubWithScopesCarriesScopesOnEveryRequest(t *testing.T) {
	r := require.New(t)
	p := owners.Principal{Hub: "h", UserID: "bob"}
	stub := identity.NewStubWithScopes(p, "Bob", []string{"scope-a", "scope-b"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/shared/scopes", nil)
	id, err := stub.FromRequest(context.Background(), req)
	r.NoError(err)
	r.Equal("bob", id.Principal.UserID)
	r.Equal([]string{"scope-a", "scope-b"}, id.Scopes)

	// Second request must carry the same scopes (not mutated on read).
	id2, err := stub.FromRequest(context.Background(), req)
	r.NoError(err)
	r.Equal([]string{"scope-a", "scope-b"}, id2.Scopes)
}

// Mutating the returned Scopes slice must not leak into a subsequent
// call's result. Both directions (stub → caller, caller → stub) are
// defended by defensive copies in NewStubWithScopes and FromRequest.
func TestStubWithScopesReturnIsIsolatedFromCallerMutation(t *testing.T) {
	r := require.New(t)
	p := owners.Principal{Hub: "h", UserID: "bob"}
	stub := identity.NewStubWithScopes(p, "Bob", []string{"a", "b"})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	id, err := stub.FromRequest(context.Background(), req)
	r.NoError(err)
	id.Scopes[0] = "MUTATED"

	id2, err := stub.FromRequest(context.Background(), req)
	r.NoError(err)
	r.Equal([]string{"a", "b"}, id2.Scopes, "mutation must not leak across calls")
}

func TestStubReturnsConfigured(t *testing.T) {
	req := require.New(t)
	sp := identity.NewStub(owners.Principal{Hub: "h", UserID: "u"}, "User")
	r := httptest.NewRequest("GET", "/x", nil)
	id, err := sp.FromRequest(context.Background(), r)
	req.NoError(err)
	req.Equal("h", id.Principal.Hub)
	req.Equal("u", id.Principal.UserID)
	req.Equal("User", id.Principal.Handle)
	req.Empty(id.Scopes)
}
