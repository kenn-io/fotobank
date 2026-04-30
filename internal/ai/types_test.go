package ai_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
)

func TestFingerprintString(t *testing.T) {
	fp := ai.Fingerprint{
		ModelID:       "qwen2.5-vl:3b",
		PromptVersion: "tags-v1",
		InputProfile:  "jpeg-1024-q85-metadata-stripped-v1",
	}
	require.Equal(t,
		"qwen2.5-vl:3b|tags-v1|jpeg-1024-q85-metadata-stripped-v1",
		fp.String(),
	)
}

func TestTaskValid(t *testing.T) {
	require.True(t, ai.TaskTag.Valid())
	require.True(t, ai.TaskCaption.Valid())
	require.False(t, ai.Task("nonsense").Valid())
}
