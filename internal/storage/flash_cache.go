package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.kenn.io/fotobank/internal/owners"
)

// ThumbCache adds a local cache for versioned thumbnail artifacts. The wrapped
// store remains authoritative for artifacts; Docbank remains authoritative for
// original content and never passes through this cache.
type ThumbCache struct {
	backing    *NASOnly
	thumbsRoot string
}

// NewThumbCache shares the backing store's current owner-key mapping.
func NewThumbCache(backing *NASOnly, thumbsRoot string) *ThumbCache {
	return &ThumbCache{backing: backing, thumbsRoot: thumbsRoot}
}

func (c *ThumbCache) flashPath(p owners.Principal, key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}
	if !strings.HasPrefix(key, ".thumbs/") {
		return "", fmt.Errorf("%w: flash cache accepts thumbnail keys only: %q", ErrInvalidKey, key)
	}
	relative, err := c.backing.ownerKey(p, key)
	if err != nil {
		return "", err
	}
	return filepath.Join(c.thumbsRoot, relative), nil
}

// Stat returns authoritative metadata from the backing store but reports TierFlash
// when a cached copy exists on the flash tier.
func (c *ThumbCache) Stat(ctx context.Context, p owners.Principal, key string) (StoreInfo, error) {
	flash, err := c.flashPath(p, key)
	if err != nil {
		return StoreInfo{}, err
	}
	info, err := c.backing.Stat(ctx, p, key)
	if err != nil {
		return StoreInfo{}, err
	}
	if _, err := os.Stat(flash); err == nil {
		info.Tier = TierFlash
	}
	return info, nil
}

// ReadRange serves reads from flash when available. A full-file miss populates
// the cache synchronously before opening the cached copy; partial reads go
// directly to the backing store.
func (c *ThumbCache) ReadRange(ctx context.Context, p owners.Principal, key string, offset, length int64) (io.ReadCloser, error) {
	flash, err := c.flashPath(p, key)
	if err != nil {
		return nil, err
	}
	if f, err := os.Open(flash); err == nil {
		return readerFromFile(f, offset, length)
	}
	if offset == 0 && length < 0 {
		if err := c.populate(ctx, p, key); err == nil {
			if f, err := os.Open(flash); err == nil {
				return f, nil
			}
		}
	}
	return c.backing.ReadRange(ctx, p, key, offset, length)
}

func (c *ThumbCache) populate(ctx context.Context, p owners.Principal, key string) (retErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	rc, err := c.backing.ReadRange(ctx, p, key, 0, -1)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, rc.Close()) }()
	flash, err := c.flashPath(p, key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(flash), 0o700); err != nil {
		return err
	}
	tmp := flash + tmpSuffix()
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, rc); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// Overwrite is safe because the backing artifact is authoritative and
	// thumbnail keys include their generation.
	if err := os.Rename(tmp, flash); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Write persists to the backing store and then populates flash best-effort.
// A failed cache copy does not fail an authoritative artifact write.
func (c *ThumbCache) Write(ctx context.Context, p owners.Principal, key string, src io.Reader) (string, error) {
	if _, err := c.flashPath(p, key); err != nil {
		return "", err
	}
	resKey, err := c.backing.Write(ctx, p, key, src)
	if err != nil {
		return resKey, err
	}
	_ = c.populate(ctx, p, resKey)
	return resKey, nil
}

// Delete removes the key from the backing store and best-effort evicts the
// cached copy.
func (c *ThumbCache) Delete(ctx context.Context, p owners.Principal, key string) error {
	flash, err := c.flashPath(p, key)
	if err != nil {
		return err
	}
	_ = os.Remove(flash)
	return c.backing.Delete(ctx, p, key)
}

func readerFromFile(f *os.File, offset, length int64) (io.ReadCloser, error) {
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
