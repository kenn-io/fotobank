package cli

import (
	"context"
	"sync"
	"time"

	"go.kenn.io/fotobank/internal/ai/embedding"
	aiservice "go.kenn.io/fotobank/internal/service/ai"
)

const embeddingHealthTTL = 30 * time.Second
const embeddingHealthTimeout = 5 * time.Second

// realEmbedProbe shares one synthetic image/text check across health callers.
// Serializing the bounded check prevents concurrent cache misses from starting
// duplicate inference. Only provider results are cached, never owner state.
type realEmbedProbe struct {
	p       aiRuntimeProvider
	mu      sync.Mutex
	config  embedding.Config
	expires time.Time
	result  aiservice.VisionPart
}

func (p *realEmbedProbe) Health(ctx context.Context) aiservice.VisionPart {
	p.mu.Lock()
	defer p.mu.Unlock()
	cfg := p.p.Effective().Config.Embed
	key := embedding.Config{
		Endpoint: cfg.Endpoint, APIKey: cfg.APIKey(), Model: cfg.Model,
		Dimension: cfg.Dimension, Timeout: cfg.Timeout,
	}
	if key == p.config && time.Now().Before(p.expires) {
		return p.result
	}
	// Do not start new inference for a caller that left while waiting.
	if err := ctx.Err(); err != nil {
		return aiservice.VisionPart{LastError: err.Error()}
	}
	// This observation is shared by other callers: finish it within our own
	// budget even if the initiating client disconnects. No detached goroutine
	// is needed; the handler owns and joins the bounded check.
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), embeddingHealthTimeout)
	defer cancel()
	result := aiservice.VisionPart{LastCheckAt: time.Now().UTC()}
	if err := embedding.Probe(probeCtx, key); err != nil {
		result.LastError = err.Error()
	} else {
		result.Reachable = true
	}
	p.config, p.result = key, result
	p.expires = time.Now().Add(embeddingHealthTTL)
	return result
}
