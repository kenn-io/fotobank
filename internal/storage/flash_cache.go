package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/wesm/fotobank/internal/owners"
)

// FlashCacheOptions tunes the recency janitor.
type FlashCacheOptions struct {
	OriginalsCacheDays     int
	OriginalsCacheMaxMedia int
}

// FlashCache wraps a NAS-backed Store with a flash-tier mirror. Writes
// go through to the wrapped Store then populate flash best-effort;
// reads hit flash first and fall back to the wrapped Store, populating
// flash on the way back.
type FlashCache struct {
	nas         Store
	flashRoot   string
	storageKeys map[owners.Principal]string
	opts        FlashCacheOptions
}

// NewFlashCache builds a FlashCache. storageKeys mirrors the map given
// to the wrapped Store so the cache can compute its own file paths.
func NewFlashCache(nas Store, flashRoot string, storageKeys map[owners.Principal]string, opts FlashCacheOptions) *FlashCache {
	return &FlashCache{nas: nas, flashRoot: flashRoot, storageKeys: storageKeys, opts: opts}
}

func (c *FlashCache) flashPath(p owners.Principal, key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}
	sk, ok := c.storageKeys[p]
	if !ok {
		return "", fmt.Errorf("storage: unknown owner %s", p)
	}
	if err := ValidateStorageKey(sk); err != nil {
		return "", err
	}
	return filepath.Join(c.flashRoot, sk, filepath.FromSlash(key)), nil
}

// Stat returns authoritative metadata from NAS but reports TierFlash
// when a cached copy exists on the flash tier.
func (c *FlashCache) Stat(ctx context.Context, p owners.Principal, key string) (StoreInfo, error) {
	info, err := c.nas.Stat(ctx, p, key)
	if err != nil {
		return StoreInfo{}, err
	}
	flash, perr := c.flashPath(p, key)
	if perr == nil {
		if _, ferr := os.Stat(flash); ferr == nil {
			info.Tier = TierFlash
			return info, nil
		}
	}
	return info, nil
}

// ReadRange serves reads from flash when available, falling back to
// NAS on miss. Full-file misses trigger a background flash populate.
func (c *FlashCache) ReadRange(ctx context.Context, p owners.Principal, key string, offset, length int64) (io.ReadCloser, error) {
	flash, perr := c.flashPath(p, key)
	if perr == nil {
		if f, err := os.Open(flash); err == nil {
			return readerFromFile(f, offset, length)
		}
	}
	rc, err := c.nas.ReadRange(ctx, p, key, offset, length)
	if err != nil {
		return nil, err
	}
	// Populate flash best-effort for full-file reads only. Partial
	// reads would cache an incomplete file and corrupt future hits.
	if offset == 0 && length < 0 && perr == nil {
		go c.populate(p, key)
	}
	return rc, nil
}

func (c *FlashCache) populate(p owners.Principal, key string) {
	rc, err := c.nas.ReadRange(context.Background(), p, key, 0, -1)
	if err != nil {
		return
	}
	defer func() { _ = rc.Close() }()
	flash, err := c.flashPath(p, key)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(flash), 0o700); err != nil {
		return
	}
	tmp := flash + tmpSuffix()
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return
	}
	if _, err := io.Copy(f, rc); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return
	}
	// Overwrite is fine: NAS holds the authoritative bytes.
	_ = os.Rename(tmp, flash)
}

// Write persists to NAS and best-effort populates flash in the
// background. The NAS write is authoritative — a failed flash copy
// does not fail the call.
func (c *FlashCache) Write(ctx context.Context, p owners.Principal, key string, src io.Reader) (string, error) {
	resKey, err := c.nas.Write(ctx, p, key, src)
	if err != nil {
		return resKey, err
	}
	go c.populate(p, resKey)
	return resKey, nil
}

// Delete removes the key from NAS and best-effort evicts the flash copy.
func (c *FlashCache) Delete(ctx context.Context, p owners.Principal, key string) error {
	flash, perr := c.flashPath(p, key)
	if perr == nil {
		_ = os.Remove(flash)
	}
	return c.nas.Delete(ctx, p, key)
}

// Evict runs the recency janitor synchronously. Returns after pruning
// completes; callers decide how often to invoke it (fotobank server
// schedules it daily).
func (c *FlashCache) Evict(_ context.Context) error {
	if c.opts.OriginalsCacheDays <= 0 && c.opts.OriginalsCacheMaxMedia <= 0 {
		return nil
	}
	cutoff := time.Now().Add(-time.Duration(c.opts.OriginalsCacheDays) * 24 * time.Hour)
	type entry struct {
		path    string
		modTime time.Time
	}
	var entries []entry
	_ = filepath.WalkDir(c.flashRoot, func(p string, d os.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return nil
		}
		if filepath.Base(p) == ".fotobank" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if c.opts.OriginalsCacheDays > 0 && info.ModTime().Before(cutoff) {
			_ = os.Remove(p)
			return nil
		}
		entries = append(entries, entry{path: p, modTime: info.ModTime()})
		return nil
	})
	if c.opts.OriginalsCacheMaxMedia > 0 && len(entries) > c.opts.OriginalsCacheMaxMedia {
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].modTime.Before(entries[j].modTime)
		})
		excess := len(entries) - c.opts.OriginalsCacheMaxMedia
		for i := range excess {
			_ = os.Remove(entries[i].path)
		}
	}
	return nil
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
