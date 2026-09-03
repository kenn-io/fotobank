package thumb_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/thumb"
)

func TestDecodeHEICReturnsErrNoPreview(t *testing.T) {
	// HEIC returns no_preview without reading src.
	_, err := thumb.Decode("image/heic", bytes.NewReader(nil))
	require.ErrorIs(t, err, thumb.ErrNoPreview)
}

func TestDecodeHEIFReturnsErrNoPreview(t *testing.T) {
	_, err := thumb.Decode("image/heif", bytes.NewReader(nil))
	require.ErrorIs(t, err, thumb.ErrNoPreview)
}

func TestDecodeUnknownMIMERejected(t *testing.T) {
	_, err := thumb.Decode("application/octet-stream", bytes.NewReader(nil))
	require.Error(t, err)
	require.NotErrorIs(t, err, thumb.ErrNoPreview)
}
