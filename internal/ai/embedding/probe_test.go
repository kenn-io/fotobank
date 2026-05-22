package embedding_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai/embedding"
)

// probeBody is the request body shape Probe sends to the server. The
// handlers below decode into this type to inspect the first input and
// route image vs text replies.
type probeBody struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

// writeVecReply emits a single-vector OpenAI-shaped reply at the
// requested dimension. dim controls the embedding length; matches and
// mismatches are constructed by passing the right number.
func writeVecReply(w http.ResponseWriter, dim int, fill float32) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w,
		`{"data":[{"embedding":`+vec(dim, fill)+`,"index":0}],"model":"siglip2"}`)
}

func TestProbe_PassesWhenBothModalitiesReturnExpectedDim(t *testing.T) {
	r := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body probeBody
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Both image and text probe calls receive a single-input batch
		// with a 768-dim reply. The probe doesn't care which side the
		// vectors describe — it only checks shape.
		writeVecReply(w, 768, 0.1)
	}))
	defer srv.Close()

	err := embedding.Probe(context.Background(), embedding.Config{
		Endpoint:  srv.URL + "/v1",
		Model:     "siglip2",
		Dimension: 768,
		Timeout:   5 * time.Second,
	})
	r.NoError(err)
}

func TestProbe_FailsOnDimensionMismatch(t *testing.T) {
	r := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body probeBody
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Server insists on 256-dim regardless of what the client expects.
		writeVecReply(w, 256, 0.1)
	}))
	defer srv.Close()

	err := embedding.Probe(context.Background(), embedding.Config{
		Endpoint:  srv.URL + "/v1",
		Model:     "siglip2",
		Dimension: 768,
		Timeout:   5 * time.Second,
	})
	r.Error(err)
	r.Contains(err.Error(), "dimension")
}

func TestProbe_FailsOnImageRejected(t *testing.T) {
	r := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body probeBody
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Distinguish image vs text by inspecting the first input. The
		// embedding client serialises image bytes as "data:image/jpeg;
		// base64,..." data URLs (see client.go EmbedImages); text is
		// passed verbatim.
		if len(body.Input) > 0 && strings.HasPrefix(body.Input[0], "data:image/") {
			http.Error(w, `{"error":"image inputs not supported"}`, http.StatusBadRequest)
			return
		}
		writeVecReply(w, 768, 0.1)
	}))
	defer srv.Close()

	err := embedding.Probe(context.Background(), embedding.Config{
		Endpoint:  srv.URL + "/v1",
		Model:     "siglip2",
		Dimension: 768,
		Timeout:   5 * time.Second,
	})
	r.Error(err)
	r.Contains(err.Error(), "image")
}
