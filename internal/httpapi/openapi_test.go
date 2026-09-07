package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/identity"
)

// These operations must remain discoverable without opening databases or
// configuring providers. Missing services must not leave callable nil handlers.
func TestOptionalServiceAPIContract(t *testing.T) {
	spec := httpapi.OpenAPISpec()
	handler, err := httpapi.New(httpapi.Deps{})
	require.NoError(t, err)
	caller := httpapi.ContextWithIdentity(context.Background(), identity.Identity{
		Principal: identity.Principal{Hub: "example", UserID: "user-a"},
	})
	for _, tc := range []struct {
		operation string
		method    string
		path      string
		body      string
	}{
		{"search", http.MethodGet, "/api/v1/search", ""},
		{"search-autocomplete-tags", http.MethodGet, "/api/v1/search/autocomplete/tags", ""},
		{"search-autocomplete-locations", http.MethodGet, "/api/v1/search/autocomplete/locations", ""},
		{"facets", http.MethodGet, "/api/v1/facets", ""},
		{"ai-health", http.MethodGet, "/api/v1/ai/health", ""},
		{"ai-failures", http.MethodGet, "/api/v1/ai/failures", ""},
		{"ai-backfill", http.MethodPost, "/api/v1/ai/backfill", `{"task":"tag"}`},
		{"ai-retry-failed", http.MethodPost, "/api/v1/ai/retry-failed", `{"task":"tag"}`},
		{"ai-retry-photo", http.MethodPost, "/api/v1/ai/retry-photo", `{"task":"tag","media_id":"a6000000-0000-4000-8000-000000000001"}`},
		{"ai-acknowledge", http.MethodPost, "/api/v1/ai/acknowledge", `{"kind":"hidden_processing"}`},
		{"ai-media-view", http.MethodGet, "/api/v1/media/{media_id}/ai", ""},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			t.Run("schema", func(t *testing.T) {
				require := require.New(t)
				item := spec.Paths[tc.path]
				require.NotNil(item)
				op := item.Get
				if tc.method == http.MethodPost {
					op = item.Post
				}
				require.NotNil(op)
				require.Equal(tc.operation, op.OperationID)
				require.Contains(op.Responses, "200")
				require.Contains(op.Responses["200"].Content, "application/json")
				if tc.body != "" {
					require.NotNil(op.RequestBody)
					require.Contains(op.RequestBody.Content, "application/json")
				}
			})
			for _, authenticated := range []bool{false, true} {
				name, want := "unauthenticated", http.StatusUnauthorized
				if authenticated {
					name, want = "service-unavailable", http.StatusServiceUnavailable
				}
				t.Run(name, func(t *testing.T) {
					path := strings.ReplaceAll(tc.path, "{media_id}", "a6000000-0000-4000-8000-000000000001")
					req := httptest.NewRequest(tc.method, path, strings.NewReader(tc.body))
					if tc.body != "" {
						req.Header.Set("Content-Type", "application/json")
					}
					if authenticated {
						req = req.WithContext(caller)
					}
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, req)
					require.Equal(t, want, rec.Code, rec.Body.String())
					require.Contains(t, rec.Header().Get("Content-Type"), "application/problem+json")
				})
			}
		})
	}
}

func TestOperatorOperationsShareAPIContract(t *testing.T) {
	spec := httpapi.OpenAPISpec()
	handler, err := httpapi.New(httpapi.Deps{})
	require.NoError(t, err)
	for _, tc := range []struct{ method, path, operation, body string }{
		{"GET", "/api/v1/operator/checkouts", "list-checkouts", ""},
		{"GET", "/api/v1/operator/checkouts/{checkout_id}", "checkout-status", ""},
		{"POST", "/api/v1/operator/checkouts/estimate", "estimate-checkout", `{"hub":"h","user_id":"u","selection":{"all":true,"asset_ids":[],"album_ids":[],"years":[]}}`},
		{"POST", "/api/v1/operator/checkouts", "create-checkout", `{"hub":"h","user_id":"u","selection":{"all":true,"asset_ids":[],"album_ids":[],"years":[]},"root":"/tmp/working","max_bytes":10}`},
		{"POST", "/api/v1/operator/checkouts/{id}/commit", "commit-checkout", `{"hub":"h","user_id":"u"}`},
		{"POST", "/api/v1/operator/backups", "create-backup", `{"hub":"h","user_id":"u","repository":"/tmp/archive","tag":"manual"}`},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			r := require.New(t)
			item := spec.Paths[tc.path]
			r.NotNil(item, "operator operation missing from the shared schema")
			op := item.Get
			if tc.method == http.MethodPost {
				op = item.Post
			}
			r.NotNil(op)
			r.Equal(tc.operation, op.OperationID)
			path := strings.ReplaceAll(strings.ReplaceAll(tc.path, "{id}", "checkout-a"), "{checkout_id}", "checkout-a")
			req := httptest.NewRequest(tc.method, path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req = req.WithContext(httpapi.ContextWithIdentity(req.Context(), identity.Identity{
				Principal: identity.Principal{Hub: "h", UserID: "u"},
			}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			r.Equal(http.StatusForbidden, response.Code, "photo identity must not grant local operator access: %s", response.Body.String())
		})
	}
}
