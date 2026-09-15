// Package scalecache caches the output of mediaseed.SeedScaleLibrary
// so repeat invocations of the e2e-server (and any future scale-mode
// caller) skip the multi-second seed cost. The cache is keyed by a
// hash of the seed inputs plus mediaseed.Fingerprint, so any change
// to the seed semantics or the media schema shape automatically
// invalidates prior entries.
//
// Layout under the cache root (XDG_CACHE_HOME or ~/.cache):
//
//	fotobank/scale-fixtures/<keyPrefix>/
//	    scale.db        — copy of the seeded SQLite database
//	    nas/            — copy of the seeded NAS subtree (only when
//	                      realThumbs=true; the default scale spec
//	                      doesn't seed thumb blobs)
//
// Restore copies scale.db into the runtime DB path (a write to it
// must NOT propagate back into the cache, e.g. from WAL checkpoints
// or schema_migrations re-application). The NAS subtree is hardlinked
// because thumb blobs are read-only at runtime — sharing inodes saves
// both wall time and disk on the 100k-file path.
//
// # Cache busting
//
// Three ways to invalidate or skip the cache, in order of preference:
//
//  1. Bump mediaseed.SeedVersion. Any change to seed semantics that
//     should produce different bytes (new RNG reseed point, changed
//     distribution shape, additional ai_results columns) needs a
//     SeedVersion bump. Schema-shape changes are covered automatically
//     because Fingerprint hashes mediaInsertSQL.
//  2. Set FOTOBANK_E2E_SCALE_NO_CACHE=1. Bypasses both Restore and
//     Persist for the run, so every boot pays the full seed cost and
//     the cache stays untouched. Use when debugging the seed itself.
//  3. trash ~/.cache/fotobank/scale-fixtures. Manual nuclear option.
//     The directory rebuilds on the next miss.
package scalecache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.kenn.io/fotobank/internal/testutil/mediaseed"
)

// keyPrefixLen is how many hex chars of the SHA-256 we use as the
// cache directory name. 16 chars = 64 bits; collisions across a
// developer's lifetime of cache entries are implausible, and shorter
// directory names are easier to inspect by hand.
const keyPrefixLen = 16

const (
	dbName  = "scale.db"
	nasName = "nas"
)

// keyInputs is the JSON-serializable shape that goes into the cache
// hash. Adding a field here implicitly busts every prior cache entry
// because the new field's zero value lands in old keys' hashes.
type keyInputs struct {
	Fingerprint     string  `json:"fingerprint"`
	Total           int     `json:"total"`
	NumCameras      int     `json:"num_cameras"`
	NumLenses       int     `json:"num_lenses"`
	NumTags         int     `json:"num_tags"`
	GPSFraction     float64 `json:"gps_fraction"`
	HotZoneFraction float64 `json:"hot_zone_fraction"`
	HiddenFraction  float64 `json:"hidden_fraction"`
	Seed            int64   `json:"seed"`
	RealThumbs      bool    `json:"real_thumbs"`
}

// Key returns a stable cache key for the given seed inputs. Two
// callers with byte-identical opts (and identical mediaseed.Fingerprint)
// receive the same key on every machine.
func Key(opts mediaseed.ScaleOpts, realThumbs bool) string {
	in := keyInputs{
		Fingerprint:     mediaseed.Fingerprint(),
		Total:           opts.Total,
		NumCameras:      opts.NumCameras,
		NumLenses:       opts.NumLenses,
		NumTags:         opts.NumTags,
		GPSFraction:     opts.GPSFraction,
		HotZoneFraction: opts.HotZoneFraction,
		HiddenFraction:  opts.HiddenFraction,
		Seed:            opts.Seed,
		RealThumbs:      realThumbs,
	}
	blob, err := json.Marshal(in)
	if err != nil {
		// json.Marshal of a flat struct with primitive fields cannot
		// fail; if the encoder is broken the program is in worse
		// trouble than this cache.
		panic(fmt.Errorf("scalecache key marshal: %w", err))
	}
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:])[:keyPrefixLen]
}

// Dir returns the cache root directory. Honors XDG_CACHE_HOME, falls
// back to $HOME/.cache. Does not create the directory; Persist does
// that on first write.
func Dir() (string, error) {
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, "fotobank", "scale-fixtures"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home dir: %w", err)
	}
	return filepath.Join(home, ".cache", "fotobank", "scale-fixtures"), nil
}

// Restore copies a cached fixture (if present) into dbPath and (if
// realThumbs) nasRoot. Returns true on hit. A miss returns
// (false, nil) so the caller can seed normally and Persist after.
//
// Errors during restore are returned to the caller — they likely
// indicate a corrupted cache entry, in which case the safe response
// is to log + reseed rather than abort the run.
func Restore(key, dbPath, nasRoot string, realThumbs bool) (bool, error) {
	cacheRoot, err := Dir()
	if err != nil {
		return false, err
	}
	entry := filepath.Join(cacheRoot, key)
	cachedDB := filepath.Join(entry, dbName)
	if _, err := os.Stat(cachedDB); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat cache entry: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return false, fmt.Errorf("mkdir db dir: %w", err)
	}
	if err := copyFile(cachedDB, dbPath); err != nil {
		return false, fmt.Errorf("copy cached db: %w", err)
	}
	if realThumbs {
		cachedNAS := filepath.Join(entry, nasName)
		switch _, err := os.Stat(cachedNAS); {
		case err == nil:
			if err := hardlinkTree(cachedNAS, nasRoot); err != nil {
				return false, fmt.Errorf("hardlink cached nas tree: %w", err)
			}
		case errors.Is(err, os.ErrNotExist):
			// realThumbs=true with no cached nas/ subtree is a
			// corrupted prior Persist (interrupted between dbName
			// and nasName, or rolled forward against an old code
			// path that didn't write nas/). Booting the runtime
			// against the half-restored cache would silently
			// 404 every /thumb request — surface a miss instead so
			// the caller reseeds and overwrites the bad entry.
			return false, fmt.Errorf(
				"scalecache: cache entry %s lacks %s subtree but realThumbs=true; "+
					"treating as miss (delete the cache dir to fully recover)",
				key, nasName,
			)
		default:
			return false, fmt.Errorf("stat cached nas tree: %w", err)
		}
	}
	return true, nil
}

// Persist copies the freshly-seeded fixture into the cache. Errors
// are returned so the caller can log; they should NOT abort the run
// because the seed already populated the runtime DB. Two writers
// racing on the same key is benign: the second writer abandons its
// tmp dir and the cache ends up with whichever entry won.
func Persist(key, dbPath, nasRoot string, realThumbs bool) error {
	cacheRoot, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		return fmt.Errorf("mkdir cache root: %w", err)
	}
	entry := filepath.Join(cacheRoot, key)
	if _, err := os.Stat(entry); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat cache entry: %w", err)
	}
	tmp, err := os.MkdirTemp(cacheRoot, key+".tmp.")
	if err != nil {
		return fmt.Errorf("mkdir tmp cache: %w", err)
	}
	abandoned := true
	defer func() {
		if abandoned {
			_ = os.RemoveAll(tmp)
		}
	}()
	if err := copyFile(dbPath, filepath.Join(tmp, dbName)); err != nil {
		return fmt.Errorf("copy db into cache: %w", err)
	}
	if realThumbs {
		if err := hardlinkTree(nasRoot, filepath.Join(tmp, nasName)); err != nil {
			return fmt.Errorf("hardlink nas tree into cache: %w", err)
		}
	}
	if err := os.Rename(tmp, entry); err != nil {
		// POSIX directory rename refuses to overwrite an existing
		// non-empty target; if another process beat us between the
		// stat above and the rename, we land here. Treat that as a
		// benign no-op.
		if _, statErr := os.Stat(entry); statErr == nil {
			return nil
		}
		return fmt.Errorf("rename cache entry: %w", err)
	}
	abandoned = false
	return nil
}

// copyFile is a straightforward stream-copy. The cache and the
// runtime tmp dir live on the same filesystem on every supported
// platform we run e2e on (macOS/Linux, both volumes under $HOME or
// $TMPDIR), so io.Copy is fine without sendfile or APFS-clone tricks.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// hardlinkTree mirrors src into dst, creating directories as needed
// and hardlinking every file. Used for the thumbs subtree where files
// are read-only at runtime so sharing inodes saves both time and
// disk. Falls back to copy if hardlink fails (e.g. cross-filesystem).
func hardlinkTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		// Best-effort hardlink. On the same filesystem this is O(1)
		// per file and shares the inode; cross-fs it errors with
		// EXDEV (or similar) and we fall back to copying.
		if err := os.Link(path, target); err == nil {
			return nil
		}
		return copyFile(path, target)
	})
}
