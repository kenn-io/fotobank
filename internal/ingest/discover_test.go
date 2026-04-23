package ingest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ingest"
	"github.com/wesm/fotobank/internal/media"
)

func TestDiscoverClassifies(t *testing.T) {
	r := require.New(t)
	root := t.TempDir()
	for _, name := range []string{"a.jpg", "b.JPG", "c.mov", "d.heic", "e.txt", "sub/f.png"} {
		full := filepath.Join(root, name)
		r.NoError(os.MkdirAll(filepath.Dir(full), 0o700))
		r.NoError(os.WriteFile(full, []byte("x"), 0o600))
	}

	var got []ingest.Candidate
	r.NoError(ingest.Discover(root, func(c ingest.Candidate) error {
		got = append(got, c)
		return nil
	}))
	r.Len(got, 5) // a.jpg, b.JPG, c.mov, d.heic, sub/f.png

	photos, videos := 0, 0
	for _, c := range got {
		switch c.Type {
		case media.TypePhoto:
			photos++
		case media.TypeVideo:
			videos++
		}
	}
	r.Equal(4, photos)
	r.Equal(1, videos)
}
