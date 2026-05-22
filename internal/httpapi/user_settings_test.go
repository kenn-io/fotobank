package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/identity"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service/usersettings"
	"go.kenn.io/fotobank/internal/testutil"
)

func TestUserSettingsRoundtrip(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	svc := usersettings.NewService(usersettings.NewRepo(d.WriteDB(), d.ReadDB()))
	prov := identity.NewStub(owners.Principal{Hub: "local", UserID: "alice"}, "Alice")
	h, err := httpapi.New(httpapi.Deps{
		IdentityProvider: prov,
		UserSettings:     svc,
	})
	r.NoError(err)

	put := httptest.NewRequest(http.MethodPut, "/api/v1/settings/user/theme",
		bytes.NewBufferString(`{"value":"\"dark\""}`))
	put.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, put)
	r.Equalf(http.StatusNoContent, rr.Code, "PUT body=%s", rr.Body.String())

	get := httptest.NewRequest(http.MethodGet, "/api/v1/settings/user/theme", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, get)
	r.Equalf(http.StatusOK, rr.Code, "GET body=%s", rr.Body.String())
	var body struct {
		Value string `json:"value"`
	}
	r.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	r.JSONEq(`"dark"`, body.Value)

	get404 := httptest.NewRequest(http.MethodGet, "/api/v1/settings/user/missing", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, get404)
	r.Equal(http.StatusNotFound, rr.Code)

	del := httptest.NewRequest(http.MethodDelete, "/api/v1/settings/user/theme", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, del)
	r.Equalf(http.StatusNoContent, rr.Code, "DELETE body=%s", rr.Body.String())

	getAfterDel := httptest.NewRequest(http.MethodGet, "/api/v1/settings/user/theme", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, getAfterDel)
	r.Equal(http.StatusNotFound, rr.Code)
}
