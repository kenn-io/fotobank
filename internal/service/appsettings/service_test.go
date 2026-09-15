package appsettings_test

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/ai/embedding"
	airuntime "go.kenn.io/fotobank/internal/ai/runtime"
	store "go.kenn.io/fotobank/internal/appsettings"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/owners"
	svc "go.kenn.io/fotobank/internal/service/appsettings"
	"go.kenn.io/fotobank/internal/testutil"
)

func TestEditableKeysIncludesV1Allowlist(t *testing.T) {
	require.ElementsMatch(t, []string{
		"ai.enabled",
		"ai.vision.endpoint",
		"ai.vision.api_key_env",
		"ai.tag.enabled",
		"ai.tag.model",
		"ai.caption.enabled",
		"ai.caption.model",
		"ai.embed.enabled",
		"ai.embed.endpoint",
		"ai.embed.api_key_env",
		"ai.embed.model",
		"ai.embed.dimension",
		"ai.embed.input_edge",
	}, svc.EditableKeys())
}

func TestKeysForSection(t *testing.T) {
	tests := []struct {
		section svc.Section
		want    []string
	}{
		{svc.SectionMaster, []string{"ai.enabled"}},
		{svc.SectionVision, []string{"ai.vision.endpoint", "ai.vision.api_key_env"}},
		{svc.SectionTag, []string{"ai.tag.enabled", "ai.tag.model"}},
		{svc.SectionCaption, []string{"ai.caption.enabled", "ai.caption.model"}},
		{svc.SectionEmbed, []string{
			"ai.embed.enabled", "ai.embed.endpoint", "ai.embed.api_key_env",
			"ai.embed.model", "ai.embed.dimension", "ai.embed.input_edge",
		}},
	}
	for _, tt := range tests {
		t.Run(string(tt.section), func(t *testing.T) {
			require := require.New(t)
			require.Equal(tt.want, svc.KeysForSection(tt.section))
			for _, key := range tt.want {
				got, ok := svc.SectionForKey(key)
				require.True(ok, key)
				require.Equal(tt.section, got)
			}
		})
	}
}

func TestValidateValueRejectsWrongJSONType(t *testing.T) {
	require := require.New(t)
	require.NoError(svc.ValidateValue("ai.enabled", jsontext.Value(`true`)))
	require.Error(svc.ValidateValue("ai.enabled", jsontext.Value(`"true"`)))
	require.NoError(svc.ValidateValue("ai.embed.dimension", jsontext.Value(`512`)))
	require.Error(svc.ValidateValue("ai.embed.dimension", jsontext.Value(`"512"`)))
	require.Error(svc.ValidateValue("ai.embed.dimension", jsontext.Value(`0`)))
	require.NoError(svc.ValidateValue("ai.embed.model", jsontext.Value(`"clip"`)))
	require.Error(svc.ValidateValue("ai.embed.model", jsontext.Value(`true`)))
}

func TestNonEditableKeyReturnsSentinel(t *testing.T) {
	err := svc.ValidateValue("http.listen_address", jsontext.Value(`"127.0.0.1:1"`))
	require.ErrorIs(t, err, svc.ErrKeyNotEditable)
	require.False(t, svc.Editable("http.listen_address"))
}

func TestAPIKeyEnvStatusEmptyMeansNoAuthRequired(t *testing.T) {
	require := require.New(t)
	t.Setenv("FOTOBANK_KEY", "secret")

	empty := svc.APIKeyEnvStatus("")
	require.Empty(empty.Name)
	require.False(empty.IsSet)
	require.False(empty.Required)

	set := svc.APIKeyEnvStatus("FOTOBANK_KEY")
	require.Equal("FOTOBANK_KEY", set.Name)
	require.True(set.IsSet)
	require.True(set.Required)

	unset := svc.APIKeyEnvStatus("MISSING_KEY")
	require.Equal("MISSING_KEY", unset.Name)
	require.False(unset.IsSet)
	require.True(unset.Required)
}

func TestServiceEffectiveIncludesDefaultsOverridesAndEnvStatus(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	t.Setenv("FOTOBANK_VISION_KEY", "secret")
	d := testutil.OpenTestDB(t)
	repo := store.NewRepo(d.WriteDB(), d.ReadDB())
	admin := owners.Principal{Hub: "dev-local", UserID: "owner"}
	require.NoError(repo.Upsert(ctx, svc.KeyTagModel, `"tag-db"`, &admin))

	provider, err := airuntime.NewProvider(ctx, airuntime.Source{
		FilePath: newAppSettingsConfigFile(t),
		Repo:     repo,
	})
	require.NoError(err)
	service := svc.NewService(d.WriteDB(), provider, embedding.NewGenerations(d.WriteDB(), d.ReadDB()))

	got, err := service.Effective(ctx)
	require.NoError(err)
	require.Len(got.Effective, 13)
	require.Len(got.FileDefault, 13)
	require.Equal("tag-db", got.Effective[svc.KeyTagModel])
	require.Equal("tag-file", got.FileDefault[svc.KeyTagModel])
	require.Nil(got.CurrentEmbedGeneration)

	override := got.Overrides[svc.KeyTagModel]
	require.Equal("tag-db", override.Value)
	require.NotZero(override.UpdatedAt)
	require.NotNil(override.UpdatedBy)
	require.Equal("dev-local", override.UpdatedBy.Hub)
	require.Equal("owner", override.UpdatedBy.UserID)

	status := got.APIKeyEnvStatus[svc.KeyVisionAPIKeyEnv]
	require.Equal("FOTOBANK_VISION_KEY", status.Name)
	require.True(status.IsSet)
	require.True(status.Required)
}

func TestServiceApplySectionUpsertsAndReloads(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	d, repo, provider := newServiceHarness(t)
	service := svc.NewService(d.WriteDB(), provider, embedding.NewGenerations(d.WriteDB(), d.ReadDB()))
	admin := owners.Principal{Hub: "dev-local", UserID: "owner"}

	resp, err := service.ApplySection(ctx, admin, svc.SectionTag, map[string]any{
		svc.KeyTagEnabled: false,
		svc.KeyTagModel:   "tag-db",
	})
	require.NoError(err)
	require.Nil(resp.GenerationID)
	require.Equal(false, resp.Effective[svc.KeyTagEnabled])
	require.Equal("tag-db", resp.Effective[svc.KeyTagModel])

	row, ok, err := repo.Get(ctx, svc.KeyTagModel)
	require.NoError(err)
	require.True(ok)
	require.Equal(`"tag-db"`, row.Value)
	require.Equal("tag-db", provider.Effective().Config.Tag.Model)
	require.False(provider.Effective().Config.Tag.Enabled)
}

func TestServiceResetKeyAndSectionDeleteOverrides(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	d, repo, provider := newServiceHarness(t)
	admin := owners.Principal{Hub: "dev-local", UserID: "owner"}
	require.NoError(repo.Upsert(ctx, svc.KeyTagModel, `"tag-db"`, &admin))
	require.NoError(repo.Upsert(ctx, svc.KeyCaptionModel, `"caption-db"`, &admin))
	require.NoError(provider.Reload(ctx))

	service := svc.NewService(d.WriteDB(), provider, embedding.NewGenerations(d.WriteDB(), d.ReadDB()))
	resp, err := service.ResetKey(ctx, admin, svc.KeyTagModel)
	require.NoError(err)
	require.Equal("tag-file", resp.Effective[svc.KeyTagModel])
	_, ok, err := repo.Get(ctx, svc.KeyTagModel)
	require.NoError(err)
	require.False(ok)

	resp, err = service.ResetSection(ctx, admin, svc.SectionCaption)
	require.NoError(err)
	require.Equal("caption-file", resp.Effective[svc.KeyCaptionModel])
	_, ok, err = repo.Get(ctx, svc.KeyCaptionModel)
	require.NoError(err)
	require.False(ok)
}

func TestServiceApplyEmbedCreatesGenerationInApplyTransaction(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	d, _, provider := newServiceHarness(t)
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	service := svc.NewService(d.WriteDB(), provider, gens)
	admin := owners.Principal{Hub: "dev-local", UserID: "owner"}

	initial, err := gens.FindOrCreateBuilding(ctx, embedding.Fingerprint(provider.Effective().Config.Embed), 512)
	require.NoError(err)
	require.NoError(gens.Promote(ctx, initial.ID))

	resp, err := service.ApplySection(ctx, admin, svc.SectionEmbed, map[string]any{
		svc.KeyEmbedDimension: 768,
	})
	require.NoError(err)
	require.NotNil(resp.GenerationID)
	require.Positive(*resp.GenerationID)
	require.NotEqual(initial.ID, *resp.GenerationID)
	require.Equal("embed-file", resp.Effective[svc.KeyEmbedModel])

	row, err := gens.GetByID(ctx, *resp.GenerationID)
	require.NoError(err)
	require.NotNil(row)
	require.Equal("building", row.State)
	require.Equal("embed-file", row.ModelID)
	require.Equal(768, row.Dimension)
}

func TestServiceApplyReloadFailurePersistsOverrideAndReturnsError(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	reloadErr := errors.New("reload boom")
	provider := &fakeProvider{
		snap:      newSnapshotForServiceTest(),
		reloadErr: reloadErr,
	}
	service := svc.NewService(d.WriteDB(), provider, embedding.NewGenerations(d.WriteDB(), d.ReadDB()))

	_, err := service.ApplySection(ctx, owners.Principal{Hub: "dev-local", UserID: "owner"}, svc.SectionTag, map[string]any{
		svc.KeyTagModel: "tag-db",
	})
	require.ErrorIs(err, svc.ErrReloadFailed)
	require.Equal(1, provider.reloads)

	repo := store.NewRepo(d.WriteDB(), d.ReadDB())
	row, ok, err := repo.Get(ctx, svc.KeyTagModel)
	require.NoError(err)
	require.True(ok)
	require.Equal(`"tag-db"`, row.Value)
	require.Equal("tag-file", provider.Effective().Config.Tag.Model)
}

func TestServiceApplyRejectsInvalidValue(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	d, _, provider := newServiceHarness(t)
	service := svc.NewService(d.WriteDB(), provider, embedding.NewGenerations(d.WriteDB(), d.ReadDB()))

	_, err := service.ApplySection(ctx, owners.Principal{Hub: "dev-local", UserID: "owner"}, svc.SectionEmbed, map[string]any{
		svc.KeyEmbedDimension: 0,
	})
	require.ErrorIs(err, svc.ErrValidationFailed)
}

type fakeProvider struct {
	snap      airuntime.Snapshot
	reloadErr error
	reloads   int
}

func (f *fakeProvider) Effective() airuntime.Snapshot {
	return f.snap
}

func (f *fakeProvider) Reload(context.Context) error {
	f.reloads++
	return f.reloadErr
}

func newServiceHarness(t *testing.T) (*db.DB, *store.Repo, *airuntime.Provider) {
	t.Helper()
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	repo := store.NewRepo(d.WriteDB(), d.ReadDB())
	provider, err := airuntime.NewProvider(ctx, airuntime.Source{
		FilePath: newAppSettingsConfigFile(t),
		Repo:     repo,
	})
	require.NoError(t, err)
	return d, repo, provider
}

func newSnapshotForServiceTest() airuntime.Snapshot {
	cfg := ai.Config{
		Enabled: true,
		Vision:  ai.VisionConfig{Endpoint: "http://127.0.0.1:11434/v1", APIKeyEnv: "FOTOBANK_VISION_KEY"},
		Tag:     ai.TaskConfig{Enabled: true, Model: "tag-file"},
		Caption: ai.TaskConfig{
			Enabled: true,
			Model:   "caption-file",
		},
		Embed: ai.EmbedConfig{
			Enabled:   true,
			Endpoint:  "http://127.0.0.1:11435/v1",
			Model:     "embed-file",
			Dimension: 512,
			InputEdge: 384,
		},
	}
	return airuntime.Snapshot{
		Config:      cfg,
		FileDefault: cfg,
		Overrides:   map[string]store.Row{},
	}
}

func newAppSettingsConfigFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	toml := `
[nas]
root = "/tmp/nas"

[ai]
enabled = true

[ai.vision]
endpoint = "http://127.0.0.1:11434/v1"
api_key_env = "FOTOBANK_VISION_KEY"

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
