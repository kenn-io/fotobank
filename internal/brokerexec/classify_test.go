package brokerexec

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/broker"
)

func TestClassifyExitPermanent(t *testing.T) {
	r := require.New(t)
	err := classifyExit("publish", 65, "scope already exists", nil)
	r.ErrorIs(err, broker.ErrBrokerPermanent)
	r.NotErrorIs(err, broker.ErrBrokerTransient)
	r.Contains(err.Error(), "scope already exists")
	r.Contains(err.Error(), "publish")
}

func TestClassifyExitTransient(t *testing.T) {
	r := require.New(t)
	err := classifyExit("publish", 75, "upstream busy", nil)
	r.ErrorIs(err, broker.ErrBrokerTransient)
	r.NotErrorIs(err, broker.ErrBrokerPermanent)
	r.Contains(err.Error(), "upstream busy")
}

func TestClassifyExitUnknownIsTransient(t *testing.T) {
	r := require.New(t)
	err := classifyExit("publish", 1, "boom", nil)
	r.ErrorIs(err, broker.ErrBrokerTransient)
	r.Contains(err.Error(), "exit=1")
	r.Contains(err.Error(), "boom")
}

func TestClassifyExitProcessNeverStarted(t *testing.T) {
	r := require.New(t)
	runErr := errors.New(`exec: "fb-broker": executable file not found in $PATH`)
	err := classifyExit("publish", -1, "", runErr)
	r.ErrorIs(err, broker.ErrBrokerTransient)
	r.Contains(err.Error(), "executable file not found")
}

func TestTailForErrorTruncates(t *testing.T) {
	in := strings.Repeat("X", 300)
	got := tailForError([]byte(in))
	require.Len(t, got, 256)
	require.Equal(t, strings.Repeat("X", 256), got)
}

func TestTailForErrorNormalizes(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{"strips control bytes", []byte("hello\x00world"), "helloworld"},
		{"replaces tab with space", []byte("a\tb"), "a b"},
		{"collapses whitespace runs", []byte("a   \n\n b"), "a b"},
		{"strips DEL byte", []byte("foo\x7fbar"), "foobar"},
		{"keeps printable ascii", []byte("hello world!"), "hello world!"},
		{"mixed", []byte("hello\x00\nworld\x07\t!"), "hello world !"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tailForError(tc.in))
		})
	}
}
