package appsettings_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	svc "github.com/wesm/fotobank/internal/service/appsettings"
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
	require.NoError(svc.ValidateValue("ai.enabled", json.RawMessage(`true`)))
	require.Error(svc.ValidateValue("ai.enabled", json.RawMessage(`"true"`)))
	require.NoError(svc.ValidateValue("ai.embed.dimension", json.RawMessage(`512`)))
	require.Error(svc.ValidateValue("ai.embed.dimension", json.RawMessage(`"512"`)))
	require.Error(svc.ValidateValue("ai.embed.dimension", json.RawMessage(`0`)))
	require.NoError(svc.ValidateValue("ai.embed.model", json.RawMessage(`"clip"`)))
	require.Error(svc.ValidateValue("ai.embed.model", json.RawMessage(`true`)))
}

func TestNonEditableKeyReturnsSentinel(t *testing.T) {
	err := svc.ValidateValue("http.listen_address", json.RawMessage(`"127.0.0.1:1"`))
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
