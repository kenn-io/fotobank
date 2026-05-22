package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai/embedding"
	airuntime "go.kenn.io/fotobank/internal/ai/runtime"
	store "go.kenn.io/fotobank/internal/appsettings"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/identity"
	"go.kenn.io/fotobank/internal/owners"
	appsettingssvc "go.kenn.io/fotobank/internal/service/appsettings"
	"go.kenn.io/fotobank/internal/testutil"
)

func TestAdminSettingsAuthFailures(t *testing.T) {
	r := require.New(t)
	admin := owners.Principal{Hub: "local", UserID: "admin"}
	h := newAdminSettingsHandler(t, nil, []owners.Principal{admin})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil))
	r.Equal(http.StatusUnauthorized, rec.Code)

	nonAdmin := owners.Principal{Hub: "local", UserID: "alice"}
	h = newAdminSettingsHandler(t, identity.NewStub(nonAdmin, "Alice"), []owners.Principal{admin})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil))
	r.Equal(http.StatusForbidden, rec.Code)
	r.NotContains(rec.Body.String(), "admin")
	r.NotContains(rec.Body.String(), "role")
}

func TestAdminSettingsGETAndApplyReset(t *testing.T) {
	r := require.New(t)
	admin := owners.Principal{Hub: "local", UserID: "admin"}
	h := newAdminSettingsHandler(t, identity.NewStub(admin, "Admin"), []owners.Principal{admin})

	rec := doJSON(h, http.MethodGet, "/api/v1/admin/settings", "")
	r.Equal(http.StatusOK, rec.Code)
	var got struct {
		Effective   map[string]any `json:"effective"`
		FileDefault map[string]any `json:"file_default"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &got))
	r.Len(got.Effective, 13)
	r.Equal("tag-file", got.Effective[appsettingssvc.KeyTagModel])

	rec = doJSON(h, http.MethodPut, "/api/v1/admin/settings/sections/tag",
		`{"values":{"ai.tag.model":"tag-db","ai.tag.enabled":false}}`)
	r.Equal(http.StatusOK, rec.Code)
	var applied struct {
		Effective map[string]any `json:"effective"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &applied))
	r.Equal("tag-db", applied.Effective[appsettingssvc.KeyTagModel])
	r.Equal(false, applied.Effective[appsettingssvc.KeyTagEnabled])

	rec = doJSON(h, http.MethodDelete, "/api/v1/admin/settings/keys/ai.tag.model", "")
	r.Equal(http.StatusOK, rec.Code)
	var reset struct {
		Effective map[string]any `json:"effective"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &reset))
	r.Equal("tag-file", reset.Effective[appsettingssvc.KeyTagModel])
}

func TestAdminSettingsEmbedApplyReturnsGenerationID(t *testing.T) {
	r := require.New(t)
	admin := owners.Principal{Hub: "local", UserID: "admin"}
	h := newAdminSettingsHandler(t, identity.NewStub(admin, "Admin"), []owners.Principal{admin})

	rec := doJSON(h, http.MethodPut, "/api/v1/admin/settings/sections/embed",
		`{"values":{"ai.embed.dimension":768}}`)
	r.Equal(http.StatusOK, rec.Code)
	var got struct {
		GenerationID int64          `json:"generation_id"`
		Effective    map[string]any `json:"effective"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &got))
	r.Positive(got.GenerationID)
	r.InDelta(768, got.Effective[appsettingssvc.KeyEmbedDimension], 0)
}

func TestAdminSettingsProbeRoutes(t *testing.T) {
	r := require.New(t)
	admin := owners.Principal{Hub: "local", UserID: "admin"}
	h := newAdminSettingsHandler(t, identity.NewStub(admin, "Admin"), []owners.Principal{admin})
	probeTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/v1/chat/completions":
			_, _ = io.WriteString(w, `{"model":"vision-model","choices":[{"message":{"content":"ok"}}]}`)
		case "/v1/embeddings":
			_, _ = io.WriteString(w, `{"data":[{"embedding":[0.1,0.2,0.3]}]}`)
		default:
			http.NotFound(w, req)
		}
	}))
	defer probeTarget.Close()

	rec := doJSON(h, http.MethodPost, "/api/v1/admin/settings/test/vision",
		`{"endpoint":`+jsonString(probeTarget.URL+"/v1")+`,"model":"vision-model","api_key_env":""}`)
	r.Equal(http.StatusOK, rec.Code)
	var vision struct {
		OK bool `json:"ok"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &vision))
	r.True(vision.OK)

	rec = doJSON(h, http.MethodPost, "/api/v1/admin/settings/test/embed",
		`{"endpoint":`+jsonString(probeTarget.URL+"/v1")+`,"model":"embed-model","api_key_env":"","dimension":3}`)
	r.Equal(http.StatusOK, rec.Code)
	var embed struct {
		OK bool `json:"ok"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &embed))
	r.True(embed.OK)
}

func TestAdminSettingsMeFeature(t *testing.T) {
	r := require.New(t)
	admin := owners.Principal{Hub: "local", UserID: "admin"}
	h := newAdminSettingsHandler(t, identity.NewStub(admin, "Admin"), []owners.Principal{admin})
	rec := doJSON(h, http.MethodGet, "/api/v1/me", "")
	r.Equal(http.StatusOK, rec.Code)
	r.Contains(rec.Body.String(), `"admin_settings_enabled":true`)
}

func newAdminSettingsHandler(t *testing.T, idp identity.Provider, admins []owners.Principal) http.Handler {
	t.Helper()
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	repo := store.NewRepo(d.WriteDB(), d.ReadDB())
	provider, err := airuntime.NewProvider(ctx, airuntime.Source{
		FilePath: newAdminSettingsConfigFile(t),
		Repo:     repo,
	})
	require.NoError(t, err)
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	settings := appsettingssvc.NewService(d.WriteDB(), provider, gens)
	h, err := httpapi.New(httpapi.Deps{
		IdentityProvider: idp,
		AdminSettings:    settings,
		AdminPrincipals:  admins,
		AdminProbeLogger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)
	return h
}

func doJSON(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	var r io.Reader
	if body != "" {
		r = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func newAdminSettingsConfigFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	toml := `
[nas]
root = "/tmp/nas"

[ai]
enabled = true

[ai.vision]
endpoint = "http://127.0.0.1:11434/v1"
api_key_env = ""

[ai.tag]
enabled = true
model = "tag-file"

[ai.caption]
enabled = true
model = "caption-file"

[ai.embed]
enabled = true
endpoint = "http://127.0.0.1:11435/v1"
api_key_env = ""
model = "embed-file"
dimension = 512
input_edge = 384
`
	require.NoError(t, os.WriteFile(p, []byte(toml), 0o600))
	return p
}

func jsonString(v string) string {
	b, _ := json.Marshal(v)
	return string(b)
}
