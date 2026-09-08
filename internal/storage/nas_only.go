package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.kenn.io/fotobank/internal/owners"
)

// NASOnly is an artifact Store backed by a single filesystem root. Writes use
// a no-clobber finalize (tmp file + os.Link) so concurrent workers cannot
// silently overwrite each other's bytes.
type NASOnly struct {
	root        string
	keysMu      sync.RWMutex
	storageKeys map[owners.Principal]string
}

// NewNASOnly constructs a NASOnly Store rooted at root. storageKeys maps
// known principals to their on-disk subdirectory (owners.storage_key).
// Unknown principals are rejected at request time. Owner administration updates
// the mapping before returning a successful registration or removal.
func NewNASOnly(root string, storageKeys map[owners.Principal]string) *NASOnly {
	return &NASOnly{root: root, storageKeys: maps.Clone(storageKeys)}
}

func (s *NASOnly) SetOwnerKey(p owners.Principal, key string) {
	s.keysMu.Lock()
	defer s.keysMu.Unlock()
	if s.storageKeys == nil {
		s.storageKeys = make(map[owners.Principal]string)
	}
	s.storageKeys[p] = key
}

func (s *NASOnly) RemoveOwnerKey(p owners.Principal) {
	s.keysMu.Lock()
	defer s.keysMu.Unlock()
	delete(s.storageKeys, p)
}

func (s *NASOnly) ownerKey(p owners.Principal, key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}
	s.keysMu.RLock()
	sk, ok := s.storageKeys[p]
	s.keysMu.RUnlock()
	if !ok {
		return "", fmt.Errorf("storage: unknown owner %s", p)
	}
	if err := ValidateStorageKey(sk); err != nil {
		return "", err
	}
	return filepath.Join(sk, filepath.FromSlash(key)), nil
}

func (s *NASOnly) ownerPath(p owners.Principal, key string) (string, error) {
	relative, err := s.ownerKey(p, key)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.root, relative), nil
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
	relative, err := s.ownerKey(p, key)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return "", fmt.Errorf("storage: open NAS root: %w", err)
	}
	defer root.Close()
	if err := root.MkdirAll(filepath.Dir(relative), 0o700); err != nil {
		return "", fmt.Errorf("storage: mkdir: %w", err)
	}
	tmp := relative + tmpSuffix()
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("storage: open tmp: %w", err)
	}
	if _, err := io.Copy(f, src); err != nil {
		_ = f.Close()
		_ = root.Remove(tmp)
		return "", fmt.Errorf("storage: write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = root.Remove(tmp)
		return "", fmt.Errorf("storage: sync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = root.Remove(tmp)
		return "", fmt.Errorf("storage: close tmp: %w", err)
	}
	if err := root.Link(tmp, relative); err != nil {
		_ = root.Remove(tmp)
		if errors.Is(err, os.ErrExist) {
			return "", ErrPathOccupied
		}
		return "", fmt.Errorf("storage: link: %w", err)
	}
	_ = root.Remove(tmp)
	return key, nil
}

func (s *NASOnly) Delete(_ context.Context, p owners.Principal, key string) error {
	relative, err := s.ownerKey(p, key)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return fmt.Errorf("storage: open NAS root: %w", err)
	}
	defer root.Close()
	if err := root.Remove(relative); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func tmpSuffix() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf(".tmp-%d-%d-%s", os.Getpid(), time.Now().UnixNano(), hex.EncodeToString(b[:]))
}
