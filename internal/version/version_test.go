package version_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/version"
)

func TestDefaultsPopulated(t *testing.T) {
	require := require.New(t)
	require.NotEmpty(version.Short)
	require.NotEmpty(version.Commit)
	require.NotEmpty(version.BuildDate)
}

func TestFormatIncludesAllThreeFields(t *testing.T) {
	require := require.New(t)
	// Override temporarily to avoid coupling to ldflag-injected defaults.
	oldV, oldC, oldB := version.Short, version.Commit, version.BuildDate
	t.Cleanup(func() {
		version.Short = oldV
		version.Commit = oldC
		version.BuildDate = oldB
	})
	version.Short, version.Commit, version.BuildDate = "v1.2.3", "abc1234", "2026-04-22T00:00:00Z"

	out := version.Format()
	require.Contains(out, "v1.2.3")
	require.Contains(out, "abc1234")
	require.Contains(out, "2026-04-22T00:00:00Z")
}
