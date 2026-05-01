package prompts_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai/prompts"
)

// pinned hashes — bump these when you bump the version string.
var pinned = map[string]string{
	"tags-v1":    "e03e0ba87ab4198eee65a4459d1df36223ece1f74a48eb658cdbf6e5576fdb84",
	"caption-v1": "9834a24369cbdb6ff913380e9ede3b355baaea00b05d1a33087ace9290cc9924",
}

func TestPromptHashesArePinned(t *testing.T) {
	require := require.New(t)
	tagP := prompts.Tag()
	capP := prompts.Caption()

	require.Equal("tags-v1", tagP.Version)
	require.Equal("caption-v1", capP.Version)
	require.NotEmpty(tagP.Text)
	require.NotEmpty(capP.Text)
	require.Equal(hashHex(tagP.Text), tagP.Hash)
	require.Equal(hashHex(capP.Text), capP.Hash)

	require.Containsf(pinned, tagP.Version, "tag prompt version %q has no pinned hash; add one to pinned", tagP.Version)
	require.Equalf(pinned[tagP.Version], tagP.Hash, "tag prompt drifted from pinned hash (bump version)")
	require.Containsf(pinned, capP.Version, "caption prompt version %q has no pinned hash; add one to pinned", capP.Version)
	require.Equalf(pinned[capP.Version], capP.Hash, "caption prompt drifted from pinned hash (bump version)")
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
