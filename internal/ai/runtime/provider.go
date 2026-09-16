// Package runtime resolves the live AI config from TOML defaults plus
// DB-backed runtime overrides.
package runtime

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"

	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/appsettings"
	"go.kenn.io/fotobank/internal/config"
)

// SettingsRepo is the read surface the provider needs from appsettings.
type SettingsRepo interface {
	List(context.Context) ([]appsettings.Row, error)
}

// Source identifies the static config file and runtime override store.
type Source struct {
	FilePath string
	Repo     SettingsRepo
}

// Snapshot is one validated effective AI config publication.
type Snapshot struct {
	Config      ai.Config
	FileDefault ai.Config
	Overrides   map[string]appsettings.Row
	Claim       ClaimFingerprints
	Result      ResultFingerprints
}

// Provider publishes validated effective AI config snapshots.
type Provider struct {
	mu       sync.Mutex
	snapshot atomic.Pointer[Snapshot]
	source   Source
}

// NewProvider constructs a provider and publishes the initial snapshot.
func NewProvider(ctx context.Context, source Source) (*Provider, error) {
	p := &Provider{source: source}
	if err := p.Reload(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

// Effective returns the current validated snapshot. The returned maps
// are copied so callers cannot mutate the provider's stored state.
func (p *Provider) Effective() Snapshot {
	s := p.snapshot.Load()
	if s == nil {
		return Snapshot{Overrides: map[string]appsettings.Row{}}
	}
	out := *s
	out.Overrides = copyRows(s.Overrides)
	return out
}

// Reload merges TOML defaults with DB overrides and publishes only if
// the merged config validates.
func (p *Provider) Reload(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	fileCfg, err := config.LoadUnchecked(p.source.FilePath)
	if err != nil {
		return err
	}
	effective := fileCfg.AI

	rows := []appsettings.Row{}
	if p.source.Repo != nil {
		rows, err = p.source.Repo.List(ctx)
		if err != nil {
			return fmt.Errorf("list app settings: %w", err)
		}
	}
	overrides := map[string]appsettings.Row{}
	for _, row := range rows {
		if err := applyOverride(&effective, row.Key, []byte(row.Value)); err != nil {
			return err
		}
		overrides[row.Key] = row
	}
	if err := effective.Validate(); err != nil {
		return fmt.Errorf("validate effective ai config: %w", err)
	}
	claim, result := deriveFingerprints(effective)
	next := &Snapshot{
		Config:      effective,
		FileDefault: fileCfg.AI,
		Overrides:   overrides,
		Claim:       claim,
		Result:      result,
	}
	p.snapshot.Store(next)
	return nil
}

func copyRows(in map[string]appsettings.Row) map[string]appsettings.Row {
	out := make(map[string]appsettings.Row, len(in))
	maps.Copy(out, in)
	return out
}

func applyOverride(cfg *ai.Config, key string, raw []byte) error {
	switch key {
	case "ai.enabled":
		return decode(raw, &cfg.Enabled, key)
	case "ai.vision.endpoint":
		return decode(raw, &cfg.Vision.Endpoint, key)
	case "ai.vision.api_key_env":
		return decode(raw, &cfg.Vision.APIKeyEnv, key)
	case "ai.tag.enabled":
		return decode(raw, &cfg.Tag.Enabled, key)
	case "ai.tag.model":
		return decode(raw, &cfg.Tag.Model, key)
	case "ai.caption.enabled":
		return decode(raw, &cfg.Caption.Enabled, key)
	case "ai.caption.model":
		return decode(raw, &cfg.Caption.Model, key)
	case "ai.embed.enabled":
		return decode(raw, &cfg.Embed.Enabled, key)
	case "ai.embed.endpoint":
		return decode(raw, &cfg.Embed.Endpoint, key)
	case "ai.embed.api_key_env":
		return decode(raw, &cfg.Embed.APIKeyEnv, key)
	case "ai.embed.model":
		return decode(raw, &cfg.Embed.Model, key)
	case "ai.embed.dimension":
		return decode(raw, &cfg.Embed.Dimension, key)
	case "ai.embed.input_edge":
		return decode(raw, &cfg.Embed.InputEdge, key)
	default:
		// Unknown DB rows are ignored by the provider so a future binary
		// can roll back without failing to boot on newer settings.
		return nil
	}
}

func decode[T any](raw []byte, dest *T, key string) error {
	if err := json.Unmarshal(raw, dest); err != nil {
		return fmt.Errorf("decode override %s: %w", key, err)
	}
	return nil
}
