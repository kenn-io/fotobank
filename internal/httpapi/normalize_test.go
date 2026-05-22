package httpapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeRouteTemplate(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "unmatched"},
		{"GET ", "unmatched"},
		{"/api/v1/healthz", "/api/v1/healthz"},
		{"GET /api/v1/healthz", "/api/v1/healthz"},
		{"POST /api/v1/media/{id}", "/api/v1/media/:id"},
		{"GET /api/v1/media/{id}/thumb/{size}", "/api/v1/media/:id/thumb/:size"},
		{"GET /static/{path...}", "/static/:path"},
		{"GET example.com/x", "/x"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			require.Equal(t, c.want, normalizeRouteTemplate(c.in))
		})
	}
}
