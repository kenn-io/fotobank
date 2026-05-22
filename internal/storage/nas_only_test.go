package storage_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/storage"
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

func TestNASOnlyRejectsInvalidStorageKey(t *testing.T) {
	// Storage keys are the per-owner subdirectory names. If the owners
	// map is ever populated with a traversal or absolute value, every
	// lookup must refuse it so media keys cannot escape the NAS root.
	r := require.New(t)
	root := t.TempDir()
	p := owners.Principal{Hub: "h", UserID: "u"}

	for _, sk := range []string{"", ".", "..", "../escape", "a/b", "a\\b"} {
		s := storage.NewNASOnly(root, map[owners.Principal]string{p: sk})
		_, err := s.Write(context.Background(), p, "a.jpg", bytes.NewReader([]byte("x")))
		r.ErrorIs(err, storage.ErrInvalidStorageKey, "Write should reject storage key %q", sk)
		_, err = s.Stat(context.Background(), p, "a.jpg")
		r.ErrorIs(err, storage.ErrInvalidStorageKey, "Stat should reject storage key %q", sk)
		_, err = s.ReadRange(context.Background(), p, "a.jpg", 0, -1)
		r.ErrorIs(err, storage.ErrInvalidStorageKey, "ReadRange should reject storage key %q", sk)
		r.ErrorIs(s.Delete(context.Background(), p, "a.jpg"), storage.ErrInvalidStorageKey, "Delete should reject storage key %q", sk)
	}
}

func TestNASOnlyRejectsTraversalAndAbsoluteKeys(t *testing.T) {
	r := require.New(t)
	s, root, p := newNASStore(t)

	// Seed a file the traversal would be aimed at — proves the guard
	// blocks the access rather than merely failing for other reasons.
	target := filepath.Join(root, "secret.txt")
	r.NoError(os.WriteFile(target, []byte("do not read"), 0o600))

	for _, key := range []string{
		"../secret.txt",
		"a/../../secret.txt",
		"/etc/passwd",
		"a//b",
		"a/./b",
		"a\\b",
		"",
	} {
		_, err := s.Write(context.Background(), p, key, bytes.NewReader([]byte("x")))
		r.ErrorIs(err, storage.ErrInvalidKey, "Write should reject key %q", key)
		_, err = s.ReadRange(context.Background(), p, key, 0, -1)
		r.ErrorIs(err, storage.ErrInvalidKey, "ReadRange should reject key %q", key)
		_, err = s.Stat(context.Background(), p, key)
		r.ErrorIs(err, storage.ErrInvalidKey, "Stat should reject key %q", key)
		r.ErrorIs(s.Delete(context.Background(), p, key), storage.ErrInvalidKey, "Delete should reject key %q", key)
	}

	// Seeded file untouched.
	b, err := os.ReadFile(target)
	r.NoError(err)
	r.Equal("do not read", string(b))
}

func TestNASOnlyConcurrentWriteResolvesToSingleWinner(t *testing.T) {
	// Two goroutines racing on the same canonical path. Exactly one
	// should win with a nil error; the other must see ErrPathOccupied.
	// This is the key invariant that makes the import pipeline safe.
	r := require.New(t)
	s, _, p := newNASStore(t)

	type result struct {
		err error
	}
	results := make(chan result, 2)
	for i := range 2 {
		go func() {
			_, err := s.Write(context.Background(), p, "race.bin",
				bytes.NewReader(fmt.Appendf(nil, "payload-%d", i)))
			results <- result{err: err}
		}()
	}
	var okCount, occCount int
	for range 2 {
		res := <-results
		switch {
		case res.err == nil:
			okCount++
		case errors.Is(res.err, storage.ErrPathOccupied):
			occCount++
		default:
			r.Fail("unexpected error", res.err.Error())
		}
	}
	r.Equal(1, okCount)
	r.Equal(1, occCount)
}
