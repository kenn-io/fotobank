package cli_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/cli"
)

func TestSharesCmdUsageErrorWithoutSub(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"shares"}, &stdout, &stderr)
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "usage")
}

func TestSharesCreateMissingGrantee(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"shares", "create"}, &stdout, &stderr)
	// Cobra's MarkFlagRequired("grantee") produces a flag parse error,
	// which root.go maps to usageError → exit 2.
	require.Equal(t, 2, code)
}
