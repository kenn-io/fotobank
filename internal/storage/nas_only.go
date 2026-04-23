package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/wesm/fotobank/internal/owners"
)

// NASOnly is a Store backed by a single local filesystem root. Writes
// use a no-clobber finalize (tmp file + os.Link) so concurrent workers
// cannot silently overwrite each other's bytes.
type NASOnly struct {
	root        string
	storageKeys map[owners.Principal]string
}

// NewNASOnly constructs a NASOnly Store rooted at root. storageKeys maps
// known principals to their on-disk subdirectory (owners.storage_key).
// Unknown principals are rejected at request time — callers must
// re-initialise when a new owner is added.
func NewNASOnly(root string, storageKeys map[owners.Principal]string) *NASOnly {
	return &NASOnly{root: root, storageKeys: storageKeys}
}

func (s *NASOnly) ownerPath(p owners.Principal, key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}
	sk, ok := s.storageKeys[p]
	if !ok {
		return "", fmt.Errorf("storage: unknown owner %s", p)
	}
	return filepath.Join(s.root, sk, filepath.FromSlash(key)), nil
}

func (s *NASOnly) Stat(_ context.Context, p owners.Principal, key string) (StoreInfo, error) {
	full, err := s.ownerPath(p, key)
	if err != nil {
		return StoreInfo{}, err
	}
	fi, err := os.Stat(full)
	if err != nil {
		return StoreInfo{}, err
	}
	return StoreInfo{Size: fi.Size(), ModTime: fi.ModTime(), Tier: TierNAS}, nil
}

func (s *NASOnly) ReadRange(_ context.Context, p owners.Principal, key string, offset, length int64) (io.ReadCloser, error) {
	full, err := s.ownerPath(p, key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	if length < 0 {
		return f, nil
	}
	return &limitedReadCloser{R: io.LimitReader(f, length), C: f}, nil
}

type limitedReadCloser struct {
	R io.Reader
	C io.Closer
}

func (lrc *limitedReadCloser) Read(p []byte) (int, error) { return lrc.R.Read(p) }
func (lrc *limitedReadCloser) Close() error               { return lrc.C.Close() }

func (s *NASOnly) Write(_ context.Context, p owners.Principal, key string, src io.Reader) (string, error) {
	full, err := s.ownerPath(p, key)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return "", fmt.Errorf("storage: mkdir: %w", err)
	}
	tmp := full + tmpSuffix()
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("storage: open tmp: %w", err)
	}
	if _, err := io.Copy(f, src); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("storage: write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("storage: sync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("storage: close tmp: %w", err)
	}
	if err := os.Link(tmp, full); err != nil {
		_ = os.Remove(tmp)
		if errors.Is(err, os.ErrExist) {
			return "", ErrPathOccupied
		}
		return "", fmt.Errorf("storage: link: %w", err)
	}
	_ = os.Remove(tmp)
	return key, nil
}

func (s *NASOnly) Delete(_ context.Context, p owners.Principal, key string) error {
	full, err := s.ownerPath(p, key)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func tmpSuffix() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf(".tmp-%d-%d-%s", os.Getpid(), time.Now().UnixNano(), hex.EncodeToString(b[:]))
}
