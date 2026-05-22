package brokerexec

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrefixBufferKeepsLastNBytes(t *testing.T) {
	cases := []struct {
		name   string
		cap    int
		writes [][]byte
		want   []byte
	}{
		{"under cap", 10, [][]byte{[]byte("abc")}, []byte("abc")},
		{"exact cap", 4, [][]byte{[]byte("abcd")}, []byte("abcd")},
		{"single write over cap", 4, [][]byte{[]byte("abcdef")}, []byte("cdef")},
		{"multiple writes overflow", 4, [][]byte{[]byte("ab"), []byte("cdef")}, []byte("cdef")},
		{"many small writes", 3, [][]byte{
			[]byte("a"), []byte("b"), []byte("c"), []byte("d"), []byte("e"),
		}, []byte("cde")},
		{"single write equal to cap multiple of input", 4, [][]byte{
			bytes.Repeat([]byte("X"), 12),
		}, bytes.Repeat([]byte("X"), 4)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := newPrefixBuffer(tc.cap)
			for _, w := range tc.writes {
				n, err := buf.Write(w)
				require.NoError(t, err)
				require.Equal(t, len(w), n)
			}
			require.Equal(t, tc.want, buf.Bytes())
		})
	}
}
