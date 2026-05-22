package identity_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/identity"
)

func TestLocalAdminSynthesizesRequestID(t *testing.T) {
	p := identity.Principal{Hub: "h", UserID: "u", Handle: "a"}
	id := identity.LocalAdmin(p)
	require.Equal(t, p, id.Principal)
	require.Empty(t, id.Scopes)
	require.Contains(t, id.RequestID, "cli-")
}
