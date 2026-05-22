package ai_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai"
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
	r := require.New(t)
	r.True(ai.TaskTag.Valid())
	r.True(ai.TaskCaption.Valid())
	r.True(ai.TaskEmbed.Valid())
	r.False(ai.Task("nonsense").Valid())
}
