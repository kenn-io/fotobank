package httpapi

import (
	"context"
	"net/http"
	"slices"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/fotobank/internal/owners"
)

func requireAdmin(ctx context.Context, admins []owners.Principal) (owners.Principal, error) {
	id, ok := IdentityFromContext(ctx)
	if !ok {
		return owners.Principal{}, huma.Error401Unauthorized(http.StatusText(http.StatusUnauthorized))
	}
	caller := id.Principal.OwnersPrincipal()
	if !isAdmin(caller, admins) {
		return owners.Principal{}, huma.Error403Forbidden(http.StatusText(http.StatusForbidden))
	}
	return caller, nil
}

func isAdmin(caller owners.Principal, admins []owners.Principal) bool {
	return slices.Contains(admins, caller)
}
