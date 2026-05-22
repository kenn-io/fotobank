package identity_test

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/identity"
)

func TestGuardAcceptsLoopbackBind(t *testing.T) {
	g := identity.NewGuard(identity.GuardConfig{ListenAddress: "127.0.0.1:8090"})
	require.NoError(t, g.Check(httptest.NewRequest("GET", "/", nil)))
}

func TestGuardAcceptsUDSBind(t *testing.T) {
	g := identity.NewGuard(identity.GuardConfig{ListenAddress: "unix:/tmp/x.sock"})
	require.NoError(t, g.Check(httptest.NewRequest("GET", "/", nil)))
}

func TestGuardRejectsPublicBindWithoutOtherChecks(t *testing.T) {
	g := identity.NewGuard(identity.GuardConfig{ListenAddress: "0.0.0.0:8090"})
	err := g.Check(httptest.NewRequest("GET", "/", nil))
	require.Error(t, err)
}

func TestGuardAllowsTrustedCIDR(t *testing.T) {
	g := identity.NewGuard(identity.GuardConfig{
		ListenAddress:     "0.0.0.0:8090",
		TrustedProxyCIDRs: []string{"10.0.0.0/24"},
	})
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.7:33445"
	require.NoError(t, g.Check(r))

	r.RemoteAddr = "8.8.8.8:33445"
	require.Error(t, g.Check(r))
}

func TestGuardChecksProxySecretConstantTime(t *testing.T) {
	g := identity.NewGuard(identity.GuardConfig{
		ListenAddress:     "0.0.0.0:8090",
		ProxySecretHeader: "X-Proxy-Secret",
		ProxySecret:       "topsecret",
	})
	r := httptest.NewRequest("GET", "/", nil)
	require.Error(t, g.Check(r))

	r.Header.Set("X-Proxy-Secret", "wrong")
	require.Error(t, g.Check(r))

	r.Header.Set("X-Proxy-Secret", "topsecret")
	require.NoError(t, g.Check(r))
}

func TestGuardAcceptsWhenMTLSConfigured(t *testing.T) {
	// With mTLS configured, the guard trusts the TLS layer to reject
	// unverified clients; Check returns nil.
	g := identity.NewGuard(identity.GuardConfig{
		ListenAddress:   "0.0.0.0:8090",
		ProxyMTLSCAFile: "/etc/ssl/ca.pem",
	})
	require.NoError(t, g.Check(httptest.NewRequest("GET", "/", nil)))
}

func TestGuardAdditiveChecks(t *testing.T) {
	// When CIDR + proxy secret both set, both must pass.
	g := identity.NewGuard(identity.GuardConfig{
		ListenAddress:     "0.0.0.0:8090",
		TrustedProxyCIDRs: []string{"10.0.0.0/24"},
		ProxySecretHeader: "X-Proxy-Secret",
		ProxySecret:       "t",
	})
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:1"
	require.Error(t, g.Check(r), "needs secret too")
	r.Header.Set("X-Proxy-Secret", "t")
	require.NoError(t, g.Check(r))
}
