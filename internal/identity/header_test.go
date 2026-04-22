package identity_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/identity"
)

func TestHeaderProviderReadsConfiguredHeaders(t *testing.T) {
	r := require.New(t)
	guard := identity.NewGuard(identity.GuardConfig{ListenAddress: "127.0.0.1:8090"})
	hp := identity.NewHeader(identity.HeaderConfig{
		UserIDHeader: "X-Auth-User-Id", HubHeader: "X-Auth-Hub",
		HandleHeader: "X-Auth-Handle", ScopesHeader: "X-Auth-Scopes",
		RequestIDHeader: "X-Auth-Request-Id",
	}, guard)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Auth-Hub", "h")
	req.Header.Set("X-Auth-User-Id", "u")
	req.Header.Set("X-Auth-Handle", "User")
	req.Header.Set("X-Auth-Scopes", "s1 s2 s3")
	req.Header.Set("X-Auth-Request-Id", "rq-1")

	id, err := hp.FromRequest(context.Background(), req)
	r.NoError(err)
	r.Equal("h", id.Principal.Hub)
	r.Equal("u", id.Principal.UserID)
	r.Equal("User", id.Principal.Handle)
	r.Equal([]string{"s1", "s2", "s3"}, id.Scopes)
	r.Equal("rq-1", id.RequestID)
}

func TestHeaderProviderRequiresUserIDAndHub(t *testing.T) {
	hp := identity.NewHeader(identity.HeaderConfig{
		UserIDHeader: "X-Auth-User-Id", HubHeader: "X-Auth-Hub",
	}, identity.NewGuard(identity.GuardConfig{ListenAddress: "127.0.0.1:8090"}))

	req := httptest.NewRequest("GET", "/", nil)
	_, err := hp.FromRequest(context.Background(), req)
	require.ErrorIs(t, err, errs.ErrIdentityMissing)
}

func TestHeaderProviderCallsGuard(t *testing.T) {
	hp := identity.NewHeader(identity.HeaderConfig{
		UserIDHeader: "X-Auth-User-Id", HubHeader: "X-Auth-Hub",
	}, identity.NewGuard(identity.GuardConfig{ListenAddress: "0.0.0.0:8090"})) // public bind with no CIDR/secret/mTLS → guard rejects

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Auth-Hub", "h")
	req.Header.Set("X-Auth-User-Id", "u")
	_, err := hp.FromRequest(context.Background(), req)
	require.ErrorIs(t, err, errs.ErrDirectAccessBlocked)
}
