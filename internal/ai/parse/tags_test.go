package parse_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai/parse"
)

func TestParseTags_Happy(t *testing.T) {
	require := require.New(t)
	out, err := parse.Tags(`{"tags":["Dog","Beach","Sunset"]}`)
	require.NoError(err)
	require.Len(out, 3)
	require.Equal("dog", out[0].Key)
	require.Equal("Dog", out[0].Label)
	require.Equal(1, out[0].Rank)
	require.Equal("sunset", out[2].Key)
	require.Equal(3, out[2].Rank)
}

func TestParseTags_PreservesInternalPunctuation(t *testing.T) {
	out, err := parse.Tags(`{"tags":["Black-and-White","T-Shirt","O'clock"]}`)
	require.NoError(t, err)
	keys := []string{out[0].Key, out[1].Key, out[2].Key}
	require.Equal(t, []string{"black-and-white", "t-shirt", "o'clock"}, keys)
}

func TestParseTags_StripsLeadingTrailingPunct(t *testing.T) {
	out, err := parse.Tags(`{"tags":["  ! Beach .. "]}`)
	require.NoError(t, err)
	require.Equal(t, "beach", out[0].Key)
}

func TestParseTags_DedupesPostNormalize(t *testing.T) {
	require := require.New(t)
	out, err := parse.Tags(`{"tags":["Dog","DOG","dog"]}`)
	require.NoError(err)
	require.Len(out, 1)
	require.Equal("dog", out[0].Key)
	require.Equal("Dog", out[0].Label) // first-seen label wins
	require.Equal(1, out[0].Rank)
}

func TestParseTags_DropsEmptyAfterNorm(t *testing.T) {
	require := require.New(t)
	out, err := parse.Tags(`{"tags":["Beach", "   ", "!!!", "Sunset"]}`)
	require.NoError(err)
	require.Len(out, 2)
	require.Equal("beach", out[0].Key)
	require.Equal("sunset", out[1].Key)
}

func TestParseTags_CapsAtTen(t *testing.T) {
	in := `{"tags":["t1","t2","t3","t4","t5","t6","t7","t8","t9","t10","t11","t12"]}`
	out, err := parse.Tags(in)
	require.NoError(t, err)
	require.Len(t, out, 10)
}

func TestParseTags_MalformedJSON(t *testing.T) {
	_, err := parse.Tags(`not json`)
	require.ErrorIs(t, err, parse.ErrMalformed)
}

func TestParseTags_StripsCodefencedJSON(t *testing.T) {
	// Some VLMs wrap JSON in ```json fences despite the prompt; tolerate.
	out, err := parse.Tags("```json\n{\"tags\":[\"Dog\"]}\n```")
	require.NoError(t, err)
	require.Equal(t, "dog", out[0].Key)
}
