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

func TestDiscoverReturnsAbsoluteCandidatePaths(t *testing.T) {
	// Candidate.Path is documented as absolute. Pin that contract by
	// passing a relative root and asserting the callback sees absolute
	// paths regardless of the caller's cwd.
	r := require.New(t)
	base := t.TempDir()
	r.NoError(os.WriteFile(filepath.Join(base, "a.jpg"), []byte("x"), 0o600))

	// Switch cwd to the temp dir so "a.jpg" resolves relative to it.
	cwd, err := os.Getwd()
	r.NoError(err)
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	r.NoError(os.Chdir(base))

	var got []ingest.Candidate
	r.NoError(ingest.Discover(".", func(c ingest.Candidate) error {
		got = append(got, c)
		return nil
	}))
	r.Len(got, 1)
	r.True(filepath.IsAbs(got[0].Path), "expected absolute path, got %q", got[0].Path)
}
