package embedding_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai/embedding"
)

// vec returns a JSON array literal of length n with every entry set to v.
// Used by tests that need a server reply with a known dimension.
func vec(n int, v float32) string {
	s := strconv.FormatFloat(float64(v), 'g', -1, 32)
	parts := make([]string, n)
	for i := range parts {
		parts[i] = s
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func TestClient_BatchImagesReturnsVectorsByIndex(t *testing.T) {
	r := require.New(t)
	type observed struct {
		path  string
		input []string
		model string
		err   error
	}
	var seen observed
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seen.path = req.URL.Path
		var body struct {
			Input []string `json:"input"`
			Model string   `json:"model"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			seen.err = err
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		seen.input = body.Input
		seen.model = body.Model
		// Reply with two distinct 768-dim vectors keyed by index.
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w,
			`{"data":[{"embedding":`+vec(768, 0.1)+`,"index":0},`+
				`{"embedding":`+vec(768, 0.2)+`,"index":1}],"model":"siglip2"}`)
	}))
	defer srv.Close()

	c := embedding.NewClient(embedding.Config{
		Endpoint:  srv.URL + "/v1",
		Model:     "siglip2",
		Dimension: 768,
		Timeout:   5 * time.Second,
	})

	out, err := c.EmbedImages(context.Background(), [][]byte{[]byte("a-bytes"), []byte("b-bytes")})
	r.NoError(err)
	r.NoError(seen.err)
	r.Equal("/v1/embeddings", seen.path)
	r.Len(seen.input, 2)
	r.Equal("siglip2", seen.model)
	r.Len(out, 2)
	r.Len(out[0], 768)
	r.Len(out[1], 768)
	r.NotEqual(out[0], out[1])
}

func TestClient_RejectsDimensionMismatchAsMalformed(t *testing.T) {
	r := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"embedding":[0.1,0.2,0.3],"index":0}],"model":"siglip2"}`)
	}))
	defer srv.Close()

	c := embedding.NewClient(embedding.Config{
		Endpoint:  srv.URL + "/v1",
		Model:     "siglip2",
		Dimension: 768,
		Timeout:   5 * time.Second,
	})
	_, err := c.EmbedImages(context.Background(), [][]byte{[]byte("x")})
	r.ErrorIs(err, embedding.ErrMalformed)
}

func TestClient_4xxIsPermanent(t *testing.T) {
	r := require.New(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, `{"error":"bad model"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	c := embedding.NewClient(embedding.Config{
		Endpoint:   srv.URL + "/v1",
		Model:      "x",
		Dimension:  768,
		Timeout:    5 * time.Second,
		MaxRetries: 3, // would-be retries: 4xx must NOT trigger them.
	})
	_, err := c.EmbedImages(context.Background(), [][]byte{[]byte("x")})
	r.ErrorIs(err, embedding.ErrProvider4xx)
	r.EqualValues(1, hits.Load(), "no retries on 4xx")
}

func TestClient_5xxRetriedAndEventuallyTransient(t *testing.T) {
	r := require.New(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := embedding.NewClient(embedding.Config{
		Endpoint:   srv.URL + "/v1",
		Model:      "x",
		Dimension:  768,
		Timeout:    5 * time.Second,
		MaxRetries: 1,
	})
	_, err := c.EmbedImages(context.Background(), [][]byte{[]byte("x")})
	r.ErrorIs(err, embedding.ErrTransient)
	r.GreaterOrEqual(hits.Load(), int32(2), "must retry once on 5xx")
}

// TestClient_ContextCancelDoesNotWrapAsTransient verifies that a
// pre-cancelled context surfaces context.Canceled, not ErrTransient.
// Wrapping the cancel as transient would make a deliberate shutdown
// indistinguishable from a 5xx and cause callers to retry instead of
// quietly stopping.
func TestClient_ContextCancelDoesNotWrapAsTransient(t *testing.T) {
	r := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Slow handler — should not be reached if cancellation works.
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(w, `{"data":[{"embedding":`+vec(768, 0.1)+`,"index":0}],"model":"m"}`)
	}))
	defer srv.Close()

	c := embedding.NewClient(embedding.Config{
		Endpoint:  srv.URL + "/v1",
		Model:     "m",
		Dimension: 768,
		Timeout:   5 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.EmbedImages(ctx, [][]byte{[]byte("x")})
	r.Error(err)
	r.ErrorIs(err, context.Canceled, "got %v", err)
	r.NotErrorIs(err, embedding.ErrTransient, "must not be wrapped as transient")
}

// TestClient_DeadlineExceededDoesNotWrapAsTransient is the deadline twin
// of the cancel test: a tiny per-call deadline that fires mid-flight
// returns context.DeadlineExceeded, not ErrTransient.
func TestClient_DeadlineExceededDoesNotWrapAsTransient(t *testing.T) {
	r := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = io.WriteString(w, `{"data":[{"embedding":`+vec(768, 0.1)+`,"index":0}],"model":"m"}`)
	}))
	defer srv.Close()

	c := embedding.NewClient(embedding.Config{
		Endpoint:  srv.URL + "/v1",
		Model:     "m",
		Dimension: 768,
		Timeout:   5 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	_, err := c.EmbedImages(ctx, [][]byte{[]byte("x")})
	r.Error(err)
	r.ErrorIs(err, context.DeadlineExceeded, "got %v", err)
	r.NotErrorIs(err, embedding.ErrTransient, "must not be wrapped as transient")
}
