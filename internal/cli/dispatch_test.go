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

func TestOwnersStubReportsNotImplemented(t *testing.T) {
	// Regression: the dispatcher used to silently exit 0 for owners
	// while the real implementation was pending, misleading users. It
	// should surface a not-implemented message and exit non-zero until
	// Task 31 lands the real subcommand.
	r := require.New(t)
	var stdout, stderr bytes.Buffer
	r.Equal(2, cli.Run([]string{"owners"}, &stdout, &stderr))
	r.Contains(stderr.String(), "not implemented")
}
