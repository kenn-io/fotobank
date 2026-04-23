package thumb_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/thumb"
)

func TestSizeMaxEdge(t *testing.T) {
	r := require.New(t)
	r.Equal(256, thumb.SizeGrid.MaxEdge())
	r.Equal(1024, thumb.SizePreview.MaxEdge())
	r.Equal(2048, thumb.SizeLightbox.MaxEdge())
}

func TestParseSizeValid(t *testing.T) {
	cases := []struct {
		in   string
		want thumb.Size
	}{
		{"grid", thumb.SizeGrid},
		{"preview", thumb.SizePreview},
		{"lightbox", thumb.SizeLightbox},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := thumb.ParseSize(c.in)
			require.NoError(t, err)
			require.Equal(t, c.want, got)
		})
	}
}

func TestParseSizeInvalid(t *testing.T) {
	_, err := thumb.ParseSize("huge")
	require.ErrorIs(t, err, thumb.ErrUnknownSize)
}

func TestAllSizes(t *testing.T) {
	require.Equal(t,
		[]thumb.Size{thumb.SizeGrid, thumb.SizePreview, thumb.SizeLightbox},
		thumb.AllSizes(),
	)
}

func TestThumbKey(t *testing.T) {
	require.Equal(t,
		".thumbs/abc-123/v5/grid.jpg",
		thumb.ThumbKey("abc-123", 5, thumb.SizeGrid),
	)
	require.Equal(t,
		".thumbs/abc-123/v0/preview.jpg",
		thumb.ThumbKey("abc-123", 0, thumb.SizePreview),
	)
}
