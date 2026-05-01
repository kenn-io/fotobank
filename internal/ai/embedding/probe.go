package embedding

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
)

// embeddedTinyJPEG is a 1x1 white-pixel JPEG built once at process
// start. Probe uses it as the smallest possible image input it can send
// without depending on the storage layer or a fixture file. About 125
// bytes after encoding. Built at init so a malformed standard library
// would surface at boot rather than mid-probe — and so the cost is
// paid once per process, not per probe call.
var embeddedTinyJPEG = mustBuildTinyJPEG()

// mustBuildTinyJPEG encodes a single white pixel as a JPEG. It panics
// only on a stdlib regression that breaks jpeg.Encode on a trivial
// 1x1 RGBA image — an outcome we'd want surfaced at boot anyway.
func mustBuildTinyJPEG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, G: 255, B: 255, A: 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		panic(fmt.Sprintf("embedding: encode tiny JPEG: %v", err))
	}
	return buf.Bytes()
}

// Probe sends one image data URL and one short text string to the
// configured embeddings endpoint and asserts that both responses come
// back with a vector at cfg.Dimension. Alignment between the two
// modalities is a model contract — Probe cannot verify it.
//
// The probe runs synchronously at server boot. Its job is to fail
// fast on three concrete misconfigurations the operator can fix:
//
//   - The endpoint or model is wrong (4xx, 5xx, network).
//   - The endpoint accepts text but rejects image data URLs (one-modality
//     model misconfigured for a two-modality pipeline).
//   - The model returns a vector at a different dimension than configured
//     (e.g. ai.embed.dimension=768 but the server returns 256).
//
// The returned error message contains either the word "image" or "text"
// so the operator knows which modality failed without re-reading logs.
// MaxRetries is forced to 0: a boot probe should not delay startup.
func Probe(ctx context.Context, cfg Config) error {
	cfg.MaxRetries = 0
	c := NewClient(cfg)

	imgs, err := c.EmbedImages(ctx, [][]byte{embeddedTinyJPEG})
	if err != nil {
		// ErrMalformed from the client is an in-band dimension/shape
		// mismatch (the response decoded fine but had the wrong vector
		// length). Reword so the operator-facing message states
		// "dimension" plainly without depending on the client's wording.
		if errors.Is(err, ErrMalformed) {
			return fmt.Errorf("embed probe (image): dimension mismatch (configured %d): %w", cfg.Dimension, err)
		}
		return fmt.Errorf("embed probe (image): %w", err)
	}
	if len(imgs) != 1 || dimOf(imgs) != cfg.Dimension {
		return fmt.Errorf("embed probe (image): expected 1 vector of dimension %d, got %d vectors of dim %d",
			cfg.Dimension, len(imgs), dimOf(imgs))
	}

	texts, err := c.EmbedTexts(ctx, []string{"a small dog on a beach"})
	if err != nil {
		if errors.Is(err, ErrMalformed) {
			return fmt.Errorf("embed probe (text): dimension mismatch (configured %d): %w", cfg.Dimension, err)
		}
		return fmt.Errorf("embed probe (text): %w", err)
	}
	if len(texts) != 1 || dimOf(texts) != cfg.Dimension {
		return fmt.Errorf("embed probe (text): expected 1 vector of dimension %d, got %d vectors of dim %d",
			cfg.Dimension, len(texts), dimOf(texts))
	}
	return nil
}

// dimOf returns the dimension of the first vector in v, or 0 when v is
// empty. Used in error messages so an "expected N got 0" line is
// distinguishable from a successful but wrong-dim response.
func dimOf(v [][]float32) int {
	if len(v) == 0 {
		return 0
	}
	return len(v[0])
}
