package identity

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strings"

	"go.kenn.io/fotobank/internal/errs"
)

// GuardConfig declares the ingress checks the direct-access guard enforces.
// At least one of loopback/UDS bind, trusted CIDRs, proxy secret, or mTLS
// must be configured; otherwise every request is rejected.
type GuardConfig struct {
	// ListenAddress is the server bind address (e.g. "127.0.0.1:8090" or
	// "unix:/run/fotobank.sock"). Loopback and UDS binds are trusted by
	// construction and require no further checks.
	ListenAddress string
	// TrustedProxyCIDRs lists CIDR ranges whose client IPs are accepted.
	// Invalid entries are silently dropped at construction time.
	TrustedProxyCIDRs []string
	// ProxySecretHeader names the HTTP header that carries the shared
	// secret set by a fronting proxy. Empty disables the check.
	ProxySecretHeader string
	// ProxySecret is the expected value of ProxySecretHeader. Empty
	// disables the check. Comparison is constant-time.
	ProxySecret string
	// ProxyMTLSCAFile is the path to the CA bundle that validates client
	// certificates. A non-empty value signals the TLS layer enforces
	// mTLS, so the guard trusts the transport to reject bad clients.
	ProxyMTLSCAFile string
}

// Guard decides whether an incoming request is permitted to reach a
// handler based on the configured ingress checks. Construct with NewGuard
// and invoke Check per request.
type Guard struct {
	cfg        GuardConfig
	cidrs      []*net.IPNet
	mode       guardMode
	secretHash [sha256.Size]byte
}

type guardMode struct {
	loopbackBind bool
	cidrCheck    bool
	secretCheck  bool
	mtls         bool
}

// NewGuard builds a Guard from cfg, parsing the configured CIDR list and
// determining which checks are active. Invalid CIDR entries are dropped.
func NewGuard(cfg GuardConfig) *Guard {
	var nets []*net.IPNet
	for _, c := range cfg.TrustedProxyCIDRs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			nets = append(nets, n)
		}
	}
	g := &Guard{
		cfg:   cfg,
		cidrs: nets,
		mode: guardMode{
			loopbackBind: isLoopbackOrUDS(cfg.ListenAddress),
			cidrCheck:    len(nets) > 0,
			secretCheck:  cfg.ProxySecretHeader != "" && cfg.ProxySecret != "",
			mtls:         cfg.ProxyMTLSCAFile != "",
		},
	}
	if g.mode.secretCheck {
		g.secretHash = sha256.Sum256([]byte(cfg.ProxySecret))
	}
	return g
}

// Check returns nil when the request satisfies the configured ingress
// checks and an error wrapping errs.ErrDirectAccessBlocked otherwise.
// Loopback/UDS bind and mTLS are treated as trusted transports; CIDR and
// proxy-secret checks, when configured, are additive and must all pass.
func (g *Guard) Check(r *http.Request) error {
	// At least one ingress check must be configured, otherwise a public
	// bind with no CIDR/secret/mTLS would expose the server.
	if !g.mode.loopbackBind && !g.mode.mtls && !g.mode.cidrCheck && !g.mode.secretCheck {
		return fmt.Errorf("%w: no ingress check satisfied", errs.ErrDirectAccessBlocked)
	}

	if g.mode.cidrCheck {
		ip := remoteIP(r.RemoteAddr)
		if ip == nil || !anyContains(g.cidrs, ip) {
			return fmt.Errorf("%w: remote %s not in trusted CIDRs",
				errs.ErrDirectAccessBlocked, r.RemoteAddr)
		}
	}
	if g.mode.secretCheck {
		// Hash both sides to fixed length before constant-time compare:
		// raw ConstantTimeCompare short-circuits on length mismatch and
		// would leak the expected secret's length.
		got := sha256.Sum256([]byte(r.Header.Get(g.cfg.ProxySecretHeader)))
		if subtle.ConstantTimeCompare(got[:], g.secretHash[:]) != 1 {
			return fmt.Errorf("%w: proxy secret mismatch", errs.ErrDirectAccessBlocked)
		}
	}
	return nil
}

func isLoopbackOrUDS(addr string) bool {
	if strings.HasPrefix(addr, "unix:") {
		return true
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func remoteIP(addr string) net.IP {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return net.ParseIP(addr)
	}
	return net.ParseIP(host)
}

func anyContains(nets []*net.IPNet, ip net.IP) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
