package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/ai/embedding"
	airuntime "go.kenn.io/fotobank/internal/ai/runtime"
	aiservice "go.kenn.io/fotobank/internal/service/ai"
)

type healthRuntime struct{ snapshot airuntime.Snapshot }

func (p *healthRuntime) Effective() airuntime.Snapshot { return p.snapshot }

func TestEmbeddingHealthCache(t *testing.T) {
	r := require.New(t)
	var calls atomic.Int64
	var unavailable atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if unavailable.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1,0]}]}`))
	}))
	t.Cleanup(server.Close)
	runtime := &healthRuntime{snapshot: airuntime.Snapshot{Config: ai.Config{
		Embed: ai.EmbedConfig{Endpoint: server.URL, Model: "test-model", Dimension: 2, Timeout: time.Second},
	}}}
	probe := &realEmbedProbe{p: runtime}
	results := make([]aiservice.VisionPart, 10)
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() { results[i] = probe.Health(t.Context()) })
	}
	wg.Wait()
	r.Equal(int64(2), calls.Load(), "one image/text check shared by concurrent callers")
	for _, result := range results {
		r.True(result.Reachable)
		r.Equal(results[0], result)
	}
	r.Equal(results[0], probe.Health(t.Context()))
	r.Equal(int64(2), calls.Load())

	// A changed provider configuration must not reuse the previous result.
	runtime.snapshot.Config.Embed.Model = "updated-model"
	r.True(probe.Health(t.Context()).Reachable)
	r.Equal(int64(4), calls.Load())
	t.Setenv("FOTOBANK_TEST_EMBED_KEY", "synthetic-embed-key")
	runtime.snapshot.Config.Embed.APIKeyEnv = "FOTOBANK_TEST_EMBED_KEY"
	r.True(probe.Health(t.Context()).Reachable)
	r.Equal(int64(6), calls.Load())

	// Exercise expiration without delaying the suite for the production TTL.
	unavailable.Store(true)
	probe.expires = time.Now().Add(-time.Second)
	failed := probe.Health(t.Context())
	r.False(failed.Reachable)
	r.Equal("embedding provider unavailable", failed.LastError)
	r.Equal(int64(7), calls.Load())
	r.Equal(failed, probe.Health(t.Context()))
	r.Equal(int64(7), calls.Load(), "failed probes are cached too")

	unavailable.Store(false)
	probe.expires = time.Now().Add(-time.Second)
	recovered := probe.Health(t.Context())
	r.True(recovered.Reachable)
	r.Empty(recovered.LastError)
	r.True(recovered.LastCheckAt.After(failed.LastCheckAt))
	r.Equal(int64(9), calls.Load())
}

func TestEmbeddingHealthErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		cause error
		want  string
	}{
		{context.DeadlineExceeded, "embedding probe timed out"},
		{context.Canceled, "embedding probe canceled"},
		{embedding.ErrProvider4xx, "embedding provider rejected the request"},
		{embedding.ErrMalformed, "embedding provider returned an invalid response"},
		{embedding.ErrTransient, "embedding provider unavailable"},
		{errors.New("unknown provider error"), "embedding probe failed"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			err := fmt.Errorf("synthetic-private-diagnostic: %w", tc.cause)
			require.Equal(t, tc.want, embeddingHealthError(err))
		})
	}
}

func TestEmbeddingHealthCallerCancellationDoesNotPoisonCache(t *testing.T) {
	r := require.New(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		cancel() // The first caller leaves after the shared check starts.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1,0]}]}`))
	}))
	t.Cleanup(server.Close)
	runtime := &healthRuntime{snapshot: airuntime.Snapshot{Config: ai.Config{
		Embed: ai.EmbedConfig{Endpoint: server.URL, Model: "test-model", Dimension: 2, Timeout: time.Second},
	}}}
	probe := &realEmbedProbe{p: runtime}
	result := probe.Health(ctx)
	r.True(result.Reachable, result.LastError)
	r.Equal(result, probe.Health(t.Context()))
	r.Equal(int64(2), calls.Load())
}
