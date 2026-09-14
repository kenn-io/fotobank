package cli_test

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/ai/embedding"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/testutil"
	"go.kenn.io/kit/daemon"
)

func TestSearchUsesCurrentEmbeddingSettings(t *testing.T) {
	for _, enabledAtBoot := range []bool{true, false} {
		t.Run(fmt.Sprintf("enabled_at_boot_%t", enabledAtBoot), func(t *testing.T) {
			r := require.New(t)
			var firstCalls, secondCalls atomic.Int32
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var input struct {
					Model string   `json:"model"`
					Input []string `json:"input"`
				}
				if err := json.UnmarshalRead(req.Body, &input); err != nil || input.Model != "active-model" ||
					len(input.Input) != 1 || input.Input[0] != "sunset" {
					http.Error(w, "unexpected query model or input", http.StatusBadRequest)
					return
				}
				switch req.URL.Path {
				case "/first/embeddings":
					firstCalls.Add(1)
					if req.Header.Get("Authorization") != "Bearer synthetic-first-key" {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
				case "/second/embeddings":
					secondCalls.Add(1)
					if req.Header.Get("Authorization") != "Bearer synthetic-second-key" {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
				default:
					http.NotFound(w, req)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[1,0,0,0]}]}`)
			}))
			t.Cleanup(gateway.Close)
			t.Setenv("FOTOBANK_TEST_SEARCH_FIRST_KEY", "synthetic-first-key")
			t.Setenv("FOTOBANK_TEST_SEARCH_SECOND_KEY", "synthetic-second-key")

			tmp := t.TempDir()
			cfgPath := writeBasicConfig(t, tmp)
			data, err := os.ReadFile(cfgPath)
			r.NoError(err)
			data = fmt.Appendf(data, `
[admin]
principals = [{hub = "h", user_id = "u"}]
[ai]
enabled = true
[ai.embed]
enabled = %t
model = "active-model"
dimension = 4
endpoint = %q
api_key_env = "FOTOBANK_TEST_SEARCH_FIRST_KEY"
timeout = "1s"
`, enabledAtBoot, gateway.URL+"/first")
			r.NoError(os.WriteFile(cfgPath, data, 0o600))
			dbPath := filepath.Join(tmp, "catalog.sqlite")
			t.Setenv("FOTOBANK_DB_PATH", dbPath)
			d, err := db.Open(dbPath)
			r.NoError(err)
			t.Cleanup(func() { _ = d.Close() })
			owner := testutil.SeedOwner(t, d.WriteDB(), "h", "u")
			id := testutil.SeedPhoto(t, d.WriteDB(), owner, "sunset")
			_, err = d.WriteDB().ExecContext(t.Context(), `INSERT INTO media_fts(media_id, filename) VALUES (?, 'sunset.jpg')`, id)
			r.NoError(err)
			gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
			gen, err := gens.FindOrCreateBuilding(t.Context(), ai.Fingerprint{
				ModelID: "active-model", InputProfile: "jpeg-384-q85-metadata-stripped-embed-v1",
			}, 4)
			r.NoError(err)
			_, err = embedding.NewMapping(d.WriteDB()).WriteVector(t.Context(), gen, id, []float32{1, 0, 0, 0})
			r.NoError(err)
			r.NoError(gens.Promote(t.Context(), gen.ID))
			r.NoError(d.Close())

			record := startCheckoutServer(t, cfgPath, dbPath)
			ep := record.Endpoint()
			client := ep.HTTPClient(daemon.HTTPClientOptions{DisableKeepAlives: true})
			request := func(method, path, body string) []byte {
				req, err := http.NewRequestWithContext(t.Context(), method, ep.BaseURL()+path, strings.NewReader(body))
				r.NoError(err)
				req.Header.Set("Authorization", "Bearer "+record.Metadata["token"])
				req.Header.Set("Content-Type", "application/json")
				resp, err := client.Do(req)
				r.NoError(err)
				raw, err := io.ReadAll(resp.Body)
				r.NoError(resp.Body.Close())
				r.NoError(err)
				r.Equal(http.StatusOK, resp.StatusCode, "%s", raw)
				return raw
			}
			search := func(disabled bool) {
				var result struct {
					Results []struct {
						MediaID string `json:"media_id"`
					} `json:"results"`
					SemanticUnavailable bool   `json:"semantic_unavailable"`
					Reason              string `json:"semantic_unavailable_reason"`
				}
				raw := request(http.MethodGet, "/api/v1/search?q=sunset", "")
				r.NoError(json.UnmarshalRead(bytes.NewReader(raw), &result))
				r.Len(result.Results, 1)
				r.Equal(id, result.Results[0].MediaID)
				r.Equal(disabled, result.SemanticUnavailable, "%s", raw)
				if disabled {
					r.Equal("embeddings_disabled", result.Reason)
				}
			}

			search(!enabledAtBoot)
			request(http.MethodPut, "/api/v1/admin/settings/sections/embed", `{"values":{"ai.embed.enabled":true}}`)
			search(false)
			before := firstCalls.Load()
			request(http.MethodPut, "/api/v1/admin/settings/sections/embed", `{"values":{"ai.embed.enabled":false}}`)
			search(true)
			r.Equal(before, firstCalls.Load(), "disabled search must not call the provider")

			// New settings create a building generation. Queries must still use
			// the active generation's model and four dimensions at the new endpoint.
			body := fmt.Sprintf(`{"values":{"ai.embed.enabled":true,"ai.embed.endpoint":%q,"ai.embed.api_key_env":"FOTOBANK_TEST_SEARCH_SECOND_KEY","ai.embed.model":"next-model","ai.embed.dimension":8}}`, gateway.URL+"/second")
			request(http.MethodPut, "/api/v1/admin/settings/sections/embed", body)
			search(false)
			r.Equal(before, firstCalls.Load())
			r.Equal(int32(1), secondCalls.Load())
			request(http.MethodPut, "/api/v1/admin/settings/sections/master", `{"values":{"ai.enabled":false}}`)
			search(true)
			r.Equal(int32(1), secondCalls.Load())
			request(http.MethodDelete, "/api/v1/admin/settings/keys/ai.enabled", "")
			search(false)
			r.Equal(int32(2), secondCalls.Load())
			request(http.MethodDelete, "/api/v1/admin/settings/sections/embed", "")
			search(!enabledAtBoot)
			r.Equal(int32(2), secondCalls.Load(), "reset must stop using the overridden endpoint")
			if enabledAtBoot {
				r.Equal(before+1, firstCalls.Load())
			} else {
				r.Equal(before, firstCalls.Load())
			}
		})
	}
}
