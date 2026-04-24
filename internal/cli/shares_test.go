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

func TestParseHubUser(t *testing.T) {
	r := require.New(t)
	cases := []struct {
		in      string
		wantHub string
		wantUID string
		wantErr bool
	}{
		{"h:u", "h", "u", false},
		{"alice.hub:bob-123", "alice.hub", "bob-123", false},
		// Multi-colon: first ':' splits; remaining colons belong to UserID.
		{"h:u:extra", "h", "u:extra", false},
		{"", "", "", true},
		{":u", "", "", true},
		{"h:", "", "", true},
		{"noColon", "", "", true},
	}
	for _, tc := range cases {
		got, err := cli.ParseHubUserForTest(tc.in)
		if tc.wantErr {
			r.Errorf(err, "in=%q", tc.in)
			continue
		}
		r.NoErrorf(err, "in=%q", tc.in)
		r.Equalf(tc.wantHub, got.Hub, "in=%q", tc.in)
		r.Equalf(tc.wantUID, got.UserID, "in=%q", tc.in)
	}
}

func TestSplitCSV(t *testing.T) {
	r := require.New(t)
	r.Nil(cli.SplitCSVForTest(""))
	r.Equal([]string{"a"}, cli.SplitCSVForTest("a"))
	r.Equal([]string{"a", "b"}, cli.SplitCSVForTest("a,b"))
	r.Equal([]string{"a", "b"}, cli.SplitCSVForTest(" a , b "))
	r.Empty(cli.SplitCSVForTest(",,,"))
	r.Empty(cli.SplitCSVForTest("  ,  "))
}

func TestShortUUID(t *testing.T) {
	r := require.New(t)
	r.Equal("abcd", cli.ShortUUIDForTest("abcd"))
	r.Equal("abcdefgh", cli.ShortUUIDForTest("abcdefgh"))
	r.Equal("abcd..7890", cli.ShortUUIDForTest("abcd12345677890"))
}
