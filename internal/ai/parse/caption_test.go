package parse_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai/parse"
)

func TestParseCaption_Happy(t *testing.T) {
	out, err := parse.Caption(`{"caption":"A small dog stands on the beach at sunset."}`)
	require.NoError(t, err)
	require.Equal(t, "A small dog stands on the beach at sunset.", out)
}

func TestParseCaption_RejectsMarkdown(t *testing.T) {
	cases := []string{
		`{"caption":"A **dog** on a beach."}`,        // bold
		`{"caption":"A *dog* on a beach."}`,          // emphasis multi-char
		`{"caption":"A *d* on a beach."}`,            // emphasis single char
		`{"caption":"A _dog_ on a beach."}`,          // underscore emphasis multi-char
		`{"caption":"A _d_ on a beach."}`,            // underscore emphasis single char
		`{"caption":"# Heading\nA dog on a beach."}`, // heading
		`{"caption":"[link](http://x)"}`,             // link
		"{\"caption\":\"```code```\"}",               // code fence
		`{"caption":"- item one"}`,                   // list
		`{"caption":"> blockquote"}`,                 // blockquote
		`{"caption":"|a|b|c|"}`,                      // table
		"{\"caption\":\"with `inline` code\"}",       // inline backtick
	}
	for _, in := range cases {
		_, err := parse.Caption(in)
		require.ErrorIsf(t, err, parse.ErrMalformed, "want ErrMalformed for %q, got %v", in, err)
	}
}

func TestParseCaption_AllowsBareApostrophe(t *testing.T) {
	out, err := parse.Caption(`{"caption":"It's three o'clock."}`)
	require.NoError(t, err)
	require.Equal(t, "It's three o'clock.", out)
}

func TestParseCaption_RejectsEmpty(t *testing.T) {
	_, err := parse.Caption(`{"caption":"   "}`)
	require.ErrorIs(t, err, parse.ErrMalformed)
}

func TestParseCaption_RejectsTooLong(t *testing.T) {
	long := strings.Repeat("a", 301)
	_, err := parse.Caption(`{"caption":"` + long + `"}`)
	require.ErrorIs(t, err, parse.ErrMalformed)
}

func TestParseCaption_TrimsWhitespace(t *testing.T) {
	out, err := parse.Caption(`{"caption":"   A dog on a beach.   "}`)
	require.NoError(t, err)
	require.Equal(t, "A dog on a beach.", out)
}

func TestParseCaption_StripsCodefencedJSON(t *testing.T) {
	out, err := parse.Caption("```json\n{\"caption\":\"A dog on a beach.\"}\n```")
	require.NoError(t, err)
	require.Equal(t, "A dog on a beach.", out)
}

func TestParseCaption_MalformedJSON(t *testing.T) {
	_, err := parse.Caption("not json")
	require.ErrorIs(t, err, parse.ErrMalformed)
}
