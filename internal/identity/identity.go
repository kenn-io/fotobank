// Package identity defines the caller identity model (Principal, Identity)
// and the Provider interface responsible for resolving an Identity from an
// inbound HTTP request.
package identity

import (
	"context"
	"net/http"

	"github.com/wesm/fotobank/internal/owners"
)

// Principal carries the display handle alongside the owners Principal fields,
// so callers can present a human-readable identity without a second lookup.
type Principal struct {
	Hub    string
	UserID string
	Handle string
}

// OwnersPrincipal returns the owners.Principal view of this principal,
// dropping the display handle that is not part of the ownership identity.
func (p Principal) OwnersPrincipal() owners.Principal {
	return owners.Principal{Hub: p.Hub, UserID: p.UserID}
}

// Identity is the authenticated caller as resolved from a single request.
// Scopes enumerate granted capabilities; RequestID ties logs and responses
// back to the originating request.
type Identity struct {
	Principal Principal
	Scopes    []string
	RequestID string
}

// Provider resolves the Identity associated with an inbound HTTP request.
// Implementations may consult cookies, bearer tokens, or other signals; they
// return an error when the request cannot be authenticated.
type Provider interface {
	FromRequest(ctx context.Context, r *http.Request) (Identity, error)
}
