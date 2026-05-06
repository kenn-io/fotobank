package runtime_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai/runtime"
	"github.com/wesm/fotobank/internal/appsettings"
)

type fakeRepo struct {
	rows  []appsettings.Row
	calls atomic.Int64
}

func (f *fakeRepo) List(_ context.Context) ([]appsettings.Row, error) {
	f.calls.Add(1)
	out := make([]appsettings.Row, len(f.rows))
	copy(out, f.rows)
	return out, nil
}

func TestProviderLoadsFileDefaults(t *testing.T) {
	require := require.New(t)
	p := newConfigFile(t)
	provider, err := runtime.NewProvider(context.Background(), runtime.Source{
		FilePath: p,
		Repo:     &fakeRepo{},
	})
	require.NoError(err)

	snap := provider.Effective()
	require.False(snap.Config.Enabled)
	require.Equal("tag-file", snap.Config.Tag.Model)
	require.Equal("caption-file", snap.Config.Caption.Model)
	require.Empty(snap.Overrides)
}

func TestProviderDBOverridesWin(t *testing.T) {
	require := require.New(t)
	p := newConfigFile(t)
	provider, err := runtime.NewProvider(context.Background(), runtime.Source{
		FilePath: p,
		Repo: &fakeRepo{rows: []appsettings.Row{
			{Key: "ai.enabled", Value: `true`},
			{Key: "ai.tag.model", Value: `"tag-db"`},
		}},
	})
	require.NoError(err)

	snap := provider.Effective()
	require.True(snap.Config.Enabled)
	require.Equal("tag-db", snap.Config.Tag.Model)
	require.Equal("tag-file", snap.FileDefault.Tag.Model)
	require.Contains(snap.Overrides, "ai.tag.model")
}

func TestProviderInvalidReloadKeepsPreviousSnapshot(t *testing.T) {
	require := require.New(t)
	p := newConfigFile(t)
	repo := &fakeRepo{}
	provider, err := runtime.NewProvider(context.Background(), runtime.Source{
		FilePath: p,
		Repo:     repo,
	})
	require.NoError(err)
	before := provider.Effective()

	repo.rows = []appsettings.Row{{Key: "ai.embed.dimension", Value: `0`}}
	err = provider.Reload(context.Background())
	require.Error(err)

	after := provider.Effective()
	require.Equal(before.Config.Embed.Dimension, after.Config.Embed.Dimension)
	require.Equal(before.Claim.Embed, after.Claim.Embed)
}

func TestProviderReloadSerializesReads(t *testing.T) {
	require := require.New(t)
	p := newConfigFile(t)
	repo := &fakeRepo{}
	provider, err := runtime.NewProvider(context.Background(), runtime.Source{
		FilePath: p,
		Repo:     repo,
	})
	require.NoError(err)

	const n = 8
	errs := make(chan error, n)
	for range n {
		go func() { errs <- provider.Reload(context.Background()) }()
	}
	for range n {
		require.NoError(<-errs)
	}
	require.Equal(int64(n+1), repo.calls.Load())
}

func TestProviderFingerprintsChangeForRuntimeInputs(t *testing.T) {
	require := require.New(t)
	p := newConfigFile(t)
	repo := &fakeRepo{}
	provider, err := runtime.NewProvider(context.Background(), runtime.Source{
		FilePath: p,
		Repo:     repo,
	})
	require.NoError(err)
	before := provider.Effective()

	repo.rows = []appsettings.Row{{Key: "ai.embed.model", Value: `"embed-db"`}}
	require.NoError(provider.Reload(context.Background()))
	embedChanged := provider.Effective()
	require.NotEqual(before.Claim.Embed, embedChanged.Claim.Embed)
	require.NotEqual(before.Result.Embed.String(), embedChanged.Result.Embed.String())

	repo.rows = []appsettings.Row{{Key: "ai.vision.endpoint", Value: `"http://127.0.0.1:9999/v1"`}}
	require.NoError(provider.Reload(context.Background()))
	visionChanged := provider.Effective()
	require.NotEqual(before.Claim.Tag, visionChanged.Claim.Tag)
	require.NotEqual(before.Claim.Caption, visionChanged.Claim.Caption)
	require.Equal(before.Result.Tag.String(), visionChanged.Result.Tag.String())
}

func newConfigFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	toml := `
[nas]
root = "/tmp/nas"

[ai]
enabled = false

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
	require.NoError(t, writeFile(p, toml))
	return p
}

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o600)
}
