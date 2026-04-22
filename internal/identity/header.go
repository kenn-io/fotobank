package identity

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/wesm/fotobank/internal/errs"
)

// HeaderConfig names the HTTP headers a fronting reverse proxy uses to
// convey authenticated identity claims. The Header provider reads these
// headers after the guard has confirmed the request came through the
// trusted ingress path.
type HeaderConfig struct {
	// UserIDHeader carries the authenticated user ID.
	UserIDHeader string
	// HubHeader carries the hub the user belongs to.
	HubHeader string
	// HandleHeader carries the human-readable display handle.
	HandleHeader string
	// ScopesHeader carries granted scopes as whitespace-separated tokens.
	ScopesHeader string
	// RequestIDHeader carries the proxy-assigned request correlation ID.
	RequestIDHeader string
}

// Header is a Provider that extracts identity claims from configurable
// request headers populated by a trusted fronting proxy. It delegates
// ingress enforcement to the supplied Guard and reads headers only after
// the guard accepts the request.
type Header struct {
	cfg   HeaderConfig
	guard *Guard
}

// NewHeader constructs a Header provider that uses cfg to locate identity
// headers and guard to enforce that the request traversed the trusted
// ingress path.
func NewHeader(cfg HeaderConfig, guard *Guard) *Header {
	return &Header{cfg: cfg, guard: guard}
}

// FromRequest runs the direct-access guard and, on success, extracts the
// Identity from the configured headers. It returns an error wrapping
// errs.ErrDirectAccessBlocked when the guard rejects the request, or an
// error wrapping errs.ErrIdentityMissing when either the hub or user ID
// header is empty. The context is accepted to satisfy the Provider
// interface but is not consulted.
func (h *Header) FromRequest(_ context.Context, r *http.Request) (Identity, error) {
	if err := h.guard.Check(r); err != nil {
		return Identity{}, err
	}
	hub := r.Header.Get(h.cfg.HubHeader)
	uid := r.Header.Get(h.cfg.UserIDHeader)
	if hub == "" || uid == "" {
		return Identity{}, fmt.Errorf("%w: missing %s or %s", errs.ErrIdentityMissing,
			h.cfg.HubHeader, h.cfg.UserIDHeader)
	}
	return Identity{
		Principal: Principal{
			Hub: hub, UserID: uid, Handle: r.Header.Get(h.cfg.HandleHeader),
		},
		Scopes:    splitScopes(r.Header.Get(h.cfg.ScopesHeader)),
		RequestID: r.Header.Get(h.cfg.RequestIDHeader),
	}, nil
}

func splitScopes(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return strings.Fields(s)
}
