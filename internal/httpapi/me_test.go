package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/httpapi"
	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/owners"
)

func TestMeReturnsStubPrincipal(t *testing.T) {
	r := require.New(t)
	idp := identity.NewStub(owners.Principal{Hub: "h", UserID: "u"}, "User")
	h, err := httpapi.New(httpapi.Deps{IdentityProvider: idp})
	r.NoError(err)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/me")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	var body struct {
		Principal struct {
			Hub    string `json:"hub"`
			UserID string `json:"user_id"`
			Handle string `json:"handle"`
		} `json:"principal"`
		Scopes []string `json:"scopes"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	r.Equal("h", body.Principal.Hub)
	r.Equal("u", body.Principal.UserID)
	r.Equal("User", body.Principal.Handle)
}
