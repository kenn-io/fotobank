package scalecache_test

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/testutil/mediaseed"
	"go.kenn.io/fotobank/internal/testutil/scalecache"
)

// TestKey_Stable_Across_Calls pins the cache-key contract: the same
// opts produce the same key on every invocation. Without this, the
// hash inputs would have to be order-stable (json.Marshal of a struct
// already is) but a future refactor could regress it.
func TestKey_Stable_Across_Calls(t *testing.T) {
	r := require.New(t)
	opts := mediaseed.DefaultScaleOpts(100)
	a := scalecache.Key(opts, false)
	b := scalecache.Key(opts, false)
	r.Equal(a, b, "Key must be deterministic across calls")
	r.Len(a, 16, "Key prefix length must match the documented contract")
}

// TestKey_Differs_For_Real_Thumbs covers the realThumbs flag's effect
// on the key. Without this the cache would conflate a default-mode
// entry (no thumb files) with a real-thumbs-mode entry (100k thumb
// blobs), and a Restore from one mode into the other would silently
// produce a degenerate fixture.
func TestKey_Differs_For_Real_Thumbs(t *testing.T) {
	r := require.New(t)
	opts := mediaseed.DefaultScaleOpts(100)
	r.NotEqual(
		scalecache.Key(opts, false),
		scalecache.Key(opts, true),
		"realThumbs flag must change the cache key",
	)
}

// TestKey_Differs_When_Opts_Change covers the per-field sensitivity
// of the cache key. A regression that drops a field from the hash
// (e.g. the new HotZoneFraction added in PS-4) would let two
// distribution-distinct fixtures collide.
func TestKey_Differs_When_Opts_Change(t *testing.T) {
	r := require.New(t)
	base := mediaseed.DefaultScaleOpts(100)

	bumpTotal := base
	bumpTotal.Total = 200
	r.NotEqual(scalecache.Key(base, false), scalecache.Key(bumpTotal, false))

	bumpHotZone := base
	bumpHotZone.HotZoneFraction = 0.10
	r.NotEqual(scalecache.Key(base, false), scalecache.Key(bumpHotZone, false))

	bumpSeed := base
	bumpSeed.Seed = 99
	r.NotEqual(scalecache.Key(base, false), scalecache.Key(bumpSeed, false))
}

// TestRestore_MissReturnsFalse confirms the cache-miss contract: no
// error, no side effects, false hit. Callers branch on this to fall
// back to the slow seed path.
func TestRestore_MissReturnsFalse(t *testing.T) {
	r := require.New(t)
	withTempCache(t)
	dbPath := filepath.Join(t.TempDir(), "scale.db")
	hit, err := scalecache.Restore("nope-no-such-key", dbPath, "", false)
	r.NoError(err)
	r.False(hit)
	_, statErr := os.Stat(dbPath)
	r.True(os.IsNotExist(statErr), "Restore on miss must not create dbPath")
}

// TestPersist_Then_Restore_RoundTrip is the cache's main contract:
// what Persist writes, Restore returns byte-identical. This is the
// regression test for any future change to copyFile/hardlinkTree —
// silent corruption of the cached bytes would make every downstream
// scale spec read a stale-but-plausible DB.
func TestPersist_Then_Restore_RoundTrip(t *testing.T) {
	r := require.New(t)
	withTempCache(t)

	srcDir := t.TempDir()
	srcDB := filepath.Join(srcDir, "scale.db")
	srcNAS := filepath.Join(srcDir, "nas")
	r.NoError(os.MkdirAll(srcNAS, 0o700))
	dbPayload := []byte("fake-sqlite-bytes-for-round-trip-test")
	r.NoError(os.WriteFile(srcDB, dbPayload, 0o600))
	thumbPath := filepath.Join(srcNAS, "550e8400-e29b-41d4-a716-44665544000e", ".thumbs", "scale-0000001", "v0", "grid.jpg")
	r.NoError(os.MkdirAll(filepath.Dir(thumbPath), 0o700))
	thumbPayload := []byte("\xff\xd8\xff fake jpeg")
	r.NoError(os.WriteFile(thumbPath, thumbPayload, 0o600))

	opts := mediaseed.DefaultScaleOpts(100)
	key := scalecache.Key(opts, true)
	r.NoError(scalecache.Persist(key, srcDB, srcNAS, true))

	// Persist into a non-existent restore target. The path must be
	// created by Restore; SeedScaleLibrary never gets to populate it.
	dstDir := t.TempDir()
	dstDB := filepath.Join(dstDir, "flash", "fotobank.sqlite")
	dstNAS := filepath.Join(dstDir, "nas")
	hit, err := scalecache.Restore(key, dstDB, dstNAS, true)
	r.NoError(err)
	r.True(hit, "Persist'd entry must come back as a hit")

	gotDB, err := os.ReadFile(dstDB)
	r.NoError(err)
	r.Equal(dbPayload, gotDB)

	gotThumb, err := os.ReadFile(filepath.Join(dstNAS, "550e8400-e29b-41d4-a716-44665544000e", ".thumbs", "scale-0000001", "v0", "grid.jpg"))
	r.NoError(err)
	r.Equal(thumbPayload, gotThumb)
}

// TestRestore_RealThumbsWithoutNAS_TreatsAsMiss covers the corrupted-
// entry path: a cache directory that contains scale.db but no nas/
// subtree (e.g. an interrupted Persist crashed between the DB copy
// and the hardlink walk) must NOT come back as a hit when realThumbs
// is true. Booting against the half-restored cache would silently
// 404 every /thumb request — surfacing an error here forces the
// caller to reseed and overwrite the bad entry.
func TestRestore_RealThumbsWithoutNAS_TreatsAsMiss(t *testing.T) {
	r := require.New(t)
	withTempCache(t)
	cacheDir, err := scalecache.Dir()
	r.NoError(err)

	opts := mediaseed.DefaultScaleOpts(100)
	key := scalecache.Key(opts, true)

	// Hand-fabricate a corrupted entry: the directory and scale.db
	// exist but the nas/ subtree is missing. Persist would never
	// produce this naturally — it does an atomic rename — but a
	// crash between the DB write and the hardlink would leave a
	// tmp dir, and a sufficiently determined operator could mv it
	// into place.
	entry := filepath.Join(cacheDir, key)
	r.NoError(os.MkdirAll(entry, 0o700))
	r.NoError(os.WriteFile(filepath.Join(entry, "scale.db"), []byte("x"), 0o600))

	dstDir := t.TempDir()
	dstDB := filepath.Join(dstDir, "scale.db")
	dstNAS := filepath.Join(dstDir, "nas")
	hit, err := scalecache.Restore(key, dstDB, dstNAS, true)
	r.False(hit, "corrupted realThumbs entry must not return as a hit")
	r.Error(err, "corrupted realThumbs entry must surface an error so the caller reseeds")
}

// TestPersist_Twice_NoOpsSecond covers the race-resolution case:
// after a successful Persist, a second call with the same key
// returns nil without churning the cache. Two concurrent e2e-server
// runs racing on the same fixture must converge on a single entry,
// not corrupt each other.
func TestPersist_Twice_NoOpsSecond(t *testing.T) {
	r := require.New(t)
	withTempCache(t)

	src := t.TempDir()
	srcDB := filepath.Join(src, "scale.db")
	r.NoError(os.WriteFile(srcDB, []byte("payload-v1"), 0o600))

	opts := mediaseed.DefaultScaleOpts(100)
	key := scalecache.Key(opts, false)
	r.NoError(scalecache.Persist(key, srcDB, "", false))
	cacheDir, err := scalecache.Dir()
	r.NoError(err)
	first, err := digest(filepath.Join(cacheDir, key, "scale.db"))
	r.NoError(err)

	// Re-Persist with different bytes. Because the entry exists, the
	// second call must NOT overwrite the cached file.
	r.NoError(os.WriteFile(srcDB, []byte("payload-v2-LATER"), 0o600))
	r.NoError(scalecache.Persist(key, srcDB, "", false))
	second, err := digest(filepath.Join(cacheDir, key, "scale.db"))
	r.NoError(err)
	r.Equal(first, second, "second Persist must not overwrite an existing cache entry")
}

// withTempCache redirects the cache root to a per-test temp dir via
// XDG_CACHE_HOME, so the tests neither read from nor pollute the
// developer's real ~/.cache.
func withTempCache(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func digest(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return string(sum[:]), nil
}
