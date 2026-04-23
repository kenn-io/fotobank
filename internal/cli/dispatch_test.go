package cli_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/cli"
)

func TestUnknownSubcommandIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"nosuch"}, &stdout, &stderr)
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "usage")
}

func TestNoArgsIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Run(nil, &stdout, &stderr)
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "usage")
}

func TestHelpExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	require.Equal(t, 0, cli.Run([]string{"help"}, &stdout, &stderr))
	require.NotEmpty(t, stdout.String())
}

func TestVersionPrintsVersionInfo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	require.Equal(t, 0, cli.Run([]string{"version"}, &stdout, &stderr))
	require.Contains(t, stdout.String(), "fotobank")
}

// TestCobraArgValidationIsUsageError pins the exit-code contract for
// cobra's built-in positional-arg validators: a too-few/too-many args
// failure from ExactArgs/NoArgs/etc must map to exit 2, not 1.
func TestCobraArgValidationIsUsageError(t *testing.T) {
	// config read takes exactly one positional (cobra.ExactArgs(1)).
	// Passing zero args triggers ExactArgs and must exit 2.
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"config", "read"}, &stdout, &stderr)
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "usage")
}
