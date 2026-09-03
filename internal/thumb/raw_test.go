package thumb_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/thumb"
)

func TestWorkerBuildsCameraRAWThumbnailFromDocbankPreview(t *testing.T) {
	fx := newWorkerFixture(t)
	source := cameraRAWWithJPEGPreview(t)
	id := seedEncodedPhotoRow(t, fx, "2024/source-"+uuid.NewString()+".nef",
		"image/x-nikon-nef", "source.nef", source)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	worker := thumb.NewWorker(fx.queue, fx.store, thumb.Config{
		Content: fx.resolve, WorkerConcurrency: 1,
		PollInterval: 20 * time.Millisecond, LeaseTimeout: 5 * time.Minute,
	})
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()

	waitForStatus(t, fx.rw, id, "ready")
	item, err := fx.repo.GetByID(t.Context(), id)
	require.NoError(t, err)
	preview, err := fx.content.VisualPreview(t.Context(), item.CurrentVersionID)
	require.NoError(t, err)
	require.Equal(t, content.VisualPreviewReady, preview.State)

	cancel()
	<-done
}

func cameraRAWWithJPEGPreview(t *testing.T) []byte {
	t.Helper()
	preview := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := range 32 {
		for x := range 32 {
			preview.Set(x, y, color.RGBA{R: uint8(x * 8), G: uint8(y * 8), B: 255, A: 255})
		}
	}
	var encoded bytes.Buffer
	require.NoError(t, jpeg.Encode(&encoded, preview, &jpeg.Options{Quality: 80}))
	jpegBytes := encoded.Bytes()

	var raw bytes.Buffer
	raw.WriteString("II")
	require.NoError(t, binary.Write(&raw, binary.LittleEndian, uint16(42)))
	require.NoError(t, binary.Write(&raw, binary.LittleEndian, uint32(8)))
	require.NoError(t, binary.Write(&raw, binary.LittleEndian, uint16(2)))
	jpegOffset := uint32(8 + 2 + 2*12 + 4)
	for _, entry := range []struct {
		tag, kind uint16
		value     uint32
	}{
		{tag: 0x0201, kind: 4, value: jpegOffset},
		{tag: 0x0202, kind: 4, value: uint32(len(jpegBytes))},
	} {
		require.NoError(t, binary.Write(&raw, binary.LittleEndian, entry.tag))
		require.NoError(t, binary.Write(&raw, binary.LittleEndian, entry.kind))
		require.NoError(t, binary.Write(&raw, binary.LittleEndian, uint32(1)))
		require.NoError(t, binary.Write(&raw, binary.LittleEndian, entry.value))
	}
	require.NoError(t, binary.Write(&raw, binary.LittleEndian, uint32(0)))
	raw.Write(jpegBytes)
	return raw.Bytes()
}
