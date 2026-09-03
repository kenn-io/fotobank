package ingest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/ingest"
	"go.kenn.io/fotobank/internal/media"
)

func TestDiscoverClassifies(t *testing.T) {
	r := require.New(t)
	root := t.TempDir()
	for _, name := range []string{"a.jpg", "b.JPG", "c.mov", "d.heic", "e.txt", "f.webp", "sub/g.png"} {
		full := filepath.Join(root, name)
		r.NoError(os.MkdirAll(filepath.Dir(full), 0o700))
		r.NoError(os.WriteFile(full, []byte("x"), 0o600))
	}

	var got []ingest.Candidate
	r.NoError(ingest.Discover(root, func(c ingest.Candidate) error {
		got = append(got, c)
		return nil
	}))
	r.Len(got, 6) // a.jpg, b.JPG, c.mov, d.heic, f.webp, sub/g.png

	photos, videos := 0, 0
	for _, c := range got {
		switch c.Type {
		case media.TypePhoto:
			photos++
		case media.TypeVideo:
			videos++
		}
	}
	r.Equal(5, photos)
	r.Equal(1, videos)
}

func TestDiscoverRejectsEmptyRoot(t *testing.T) {
	err := ingest.Discover("", func(ingest.Candidate) error { return nil })
	require.Error(t, err)
}

func TestDiscoverRejectsSupportedFileSymlink(t *testing.T) {
	r := require.New(t)
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside.jpg")
	r.NoError(os.WriteFile(target, []byte("outside"), 0o600))
	link := filepath.Join(root, "linked.jpg")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	visited := false
	err := ingest.Discover(root, func(ingest.Candidate) error {
		visited = true
		return nil
	})
	r.ErrorIs(err, errs.ErrInvalidArgument)
	r.False(visited)
}

func TestDiscoverSkipsAppleDoubleAndSystemFiles(t *testing.T) {
	// Pins the macOS/Windows filesystem-junk filter. AppleDouble files
	// ("._*") are Finder-emitted resource forks that share the
	// original's extension but contain only metadata; without this
	// filter they get ingested as 4096-byte non-decodable photos
	// (observed in production: three "._L1000xxx.JPG" rows from a
	// Leica SD-card import). .DS_Store / Thumbs.db / desktop.ini are
	// the equivalent metadata droppings on the file side.
	r := require.New(t)
	root := t.TempDir()
	for _, name := range []string{
		"real.jpg", "._real.jpg", // AppleDouble shadows the real file
		"sub/photo.jpg", "sub/._photo.jpg",
		".DS_Store", "Thumbs.db", "desktop.ini",
		"._L1000817.JPG", // exact pattern from the live regression
	} {
		full := filepath.Join(root, name)
		r.NoError(os.MkdirAll(filepath.Dir(full), 0o700))
		r.NoError(os.WriteFile(full, []byte("x"), 0o600))
	}

	var got []string
	r.NoError(ingest.Discover(root, func(c ingest.Candidate) error {
		rel, err := filepath.Rel(root, c.Path)
		require.NoError(t, err)
		got = append(got, rel)
		return nil
	}))
	r.ElementsMatch([]string{"real.jpg", filepath.Join("sub", "photo.jpg")}, got)
}

func TestDiscoverSkipsSystemDirectories(t *testing.T) {
	// Pins the directory-skip path. macOS write .Trashes, .fseventsd,
	// .Spotlight-V100 etc. on removable drives; without SkipDir,
	// Discover would descend into them. The filter is name-only so the
	// root is exempt — a user explicitly pointing the importer at
	// .Trashes/ still scans it.
	r := require.New(t)
	root := t.TempDir()
	for _, p := range []string{
		"top.jpg",
		".Trashes/junk.jpg",
		".Spotlight-V100/index.jpg",
		".fseventsd/log.jpg",
		"$RECYCLE.BIN/win.jpg",
	} {
		full := filepath.Join(root, p)
		r.NoError(os.MkdirAll(filepath.Dir(full), 0o700))
		r.NoError(os.WriteFile(full, []byte("x"), 0o600))
	}

	var got []string
	r.NoError(ingest.Discover(root, func(c ingest.Candidate) error {
		rel, err := filepath.Rel(root, c.Path)
		require.NoError(t, err)
		got = append(got, rel)
		return nil
	}))
	r.Equal([]string{"top.jpg"}, got)
}

func TestDiscoverScansSkippableRootDirectly(t *testing.T) {
	// Pins the "name-only filter, root is exempt" contract from
	// Discover's docstring. A user may legitimately point the importer
	// at a directory whose basename matches one of the skip-list
	// entries (case in point: someone deliberately importing photos
	// they recovered from a $RECYCLE.BIN folder, or a sibling test
	// pointing at a dir that happens to be named ".Trashes"). The
	// filter is a recursion guard; the root itself was explicitly
	// requested.
	r := require.New(t)
	parent := t.TempDir()
	root := filepath.Join(parent, ".Trashes")
	r.NoError(os.MkdirAll(root, 0o700))
	r.NoError(os.WriteFile(filepath.Join(root, "rescued.jpg"), []byte("x"), 0o600))

	var got []string
	r.NoError(ingest.Discover(root, func(c ingest.Candidate) error {
		rel, err := filepath.Rel(root, c.Path)
		require.NoError(t, err)
		got = append(got, rel)
		return nil
	}))
	r.Equal([]string{"rescued.jpg"}, got)
}

func TestDiscoverSkipsRecycleBinCaseInsensitively(t *testing.T) {
	// Pins the case-insensitive Windows-side filter. NTFS preserves the
	// user's casing for $RECYCLE.BIN but treats the name as
	// case-insensitive, so a removable drive formatted on a different
	// machine may surface "$Recycle.Bin" or "$recycle.bin"; the filter
	// must match all three.
	r := require.New(t)
	root := t.TempDir()
	for _, p := range []string{
		"top.jpg",
		"$Recycle.Bin/win-mixed.jpg",
		"$recycle.bin/win-lower.jpg",
		"system volume information/win-svi.jpg",
	} {
		full := filepath.Join(root, p)
		r.NoError(os.MkdirAll(filepath.Dir(full), 0o700))
		r.NoError(os.WriteFile(full, []byte("x"), 0o600))
	}

	var got []string
	r.NoError(ingest.Discover(root, func(c ingest.Candidate) error {
		rel, err := filepath.Rel(root, c.Path)
		require.NoError(t, err)
		got = append(got, rel)
		return nil
	}))
	r.Equal([]string{"top.jpg"}, got)
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
