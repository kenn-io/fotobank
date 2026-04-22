package storage_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/storage"
)

func newNASStore(t *testing.T) (*storage.NASOnly, string, owners.Principal) {
	t.Helper()
	root := t.TempDir()
	p := owners.Principal{Hub: "h", UserID: "u"}
	return storage.NewNASOnly(root, storageKeyFor(p, "key1")), root, p
}

// storageKeyFor resolves a (principal -> storage_key) lookup in tests.
// NASOnly takes a static map so tests can stub without the full owners repo.
func storageKeyFor(p owners.Principal, key string) map[owners.Principal]string {
	return map[owners.Principal]string{p: key}
}

func TestNASOnlyWriteThenRead(t *testing.T) {
	r := require.New(t)
	s, root, p := newNASStore(t)

	_, err := s.Write(context.Background(), p, "2024/a.jpg", bytes.NewReader([]byte("hello")))
	r.NoError(err)

	// File landed at {root}/{storage_key}/2024/a.jpg.
	b, err := os.ReadFile(filepath.Join(root, "key1", "2024", "a.jpg"))
	r.NoError(err)
	r.Equal("hello", string(b))

	rc, err := s.ReadRange(context.Background(), p, "2024/a.jpg", 0, -1)
	r.NoError(err)
	defer rc.Close()
	got, err := io.ReadAll(rc)
	r.NoError(err)
	r.Equal("hello", string(got))
}

func TestNASOnlyReadRange(t *testing.T) {
	r := require.New(t)
	s, _, p := newNASStore(t)
	_, err := s.Write(context.Background(), p, "f.bin", bytes.NewReader([]byte("0123456789")))
	r.NoError(err)

	rc, err := s.ReadRange(context.Background(), p, "f.bin", 3, 4)
	r.NoError(err)
	defer rc.Close()
	got, err := io.ReadAll(rc)
	r.NoError(err)
	r.Equal("3456", string(got))
}

func TestNASOnlyWriteNoClobberReturnsErrPathOccupied(t *testing.T) {
	r := require.New(t)
	s, _, p := newNASStore(t)
	_, err := s.Write(context.Background(), p, "f.jpg", bytes.NewReader([]byte("first")))
	r.NoError(err)

	_, err = s.Write(context.Background(), p, "f.jpg", bytes.NewReader([]byte("second")))
	r.ErrorIs(err, storage.ErrPathOccupied)

	// First bytes are unchanged.
	rc, err := s.ReadRange(context.Background(), p, "f.jpg", 0, -1)
	r.NoError(err)
	defer rc.Close()
	got, err := io.ReadAll(rc)
	r.NoError(err)
	r.Equal("first", string(got))
}

func TestNASOnlyStat(t *testing.T) {
	r := require.New(t)
	s, _, p := newNASStore(t)
	_, err := s.Write(context.Background(), p, "s.bin", bytes.NewReader([]byte("abc")))
	r.NoError(err)

	info, err := s.Stat(context.Background(), p, "s.bin")
	r.NoError(err)
	r.Equal(int64(3), info.Size)
	r.Equal(storage.TierNAS, info.Tier)
}

func TestNASOnlyDeleteIsIdempotent(t *testing.T) {
	r := require.New(t)
	s, _, p := newNASStore(t)
	_, err := s.Write(context.Background(), p, "d.bin", bytes.NewReader([]byte("x")))
	r.NoError(err)
	r.NoError(s.Delete(context.Background(), p, "d.bin"))
	r.NoError(s.Delete(context.Background(), p, "d.bin")) // idempotent
	_, err = s.Stat(context.Background(), p, "d.bin")
	r.ErrorIs(err, os.ErrNotExist)
}
