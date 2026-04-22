package identity_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/owners"
)

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
