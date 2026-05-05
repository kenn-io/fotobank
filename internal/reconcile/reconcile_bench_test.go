package reconcile_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/reconcile"
	"github.com/wesm/fotobank/internal/testutil/mediaseed"
)

// Scale benchmark for reconcile against a 10k-row library + matching
// NAS subtree. Run with:
//   go test -tags sqlite_fts5 -run '^$' -bench Reconcile_10k -benchmem ./internal/reconcile
//
// The bench is sized at 10k (not 100k like the read-only benches)
// because the fixture must materialize one empty file per seeded
// row on the host filesystem; at 100k the directory create + touch
// pass dominates wall time and the bench measures filesystem prep
// more than the reconcile pass itself.

const benchScale = 10_000

const benchStorageKey = "bench-storage"

var benchOwner = owners.Principal{Hub: "bench-hub", UserID: "bench-user"}

var (
	fixOnce    sync.Once
	fixDB      *db.DB
	fixNASRoot string
	fixErr     error
)

// loadReconcileFixture seeds the cached 10k-row library AND
// materializes one empty file per row under <NASRoot>/<StorageKey>/.
// First call captures the supplied *testing.B for failure reporting;
// subsequent calls reuse the cached state. The temp dir is
// deliberately leaked (os.MkdirTemp without registering Cleanup) so
// later benches in the same process see the same fixture; the OS
// reclaims /tmp on reboot.
func loadReconcileFixture(b *testing.B) (*db.DB, string) {
	b.Helper()
	fixOnce.Do(func() {
		db.RegisterSqliteVec()
		dir, err := os.MkdirTemp("", "fotobank-reconcile-bench-*")
		if err != nil {
			fixErr = fmt.Errorf("mkdir tmp: %w", err)
			return
		}
		dbPath := filepath.Join(dir, "scale.db")
		d, err := db.Open(dbPath)
		if err != nil {
			fixErr = fmt.Errorf("open db: %w", err)
			return
		}

		ids := mediaseed.SeedScaleLibrary(b, d.WriteDB(), benchOwner,
			mediaseed.DefaultScaleOpts(benchScale))

		nasRoot := filepath.Join(dir, "nas")
		ownerRoot := filepath.Join(nasRoot, benchStorageKey, "scale")
		if err := os.MkdirAll(ownerRoot, 0o755); err != nil {
			fixErr = fmt.Errorf("mkdir owner root: %w", err)
			return
		}
		// Materialize an empty file per seeded id matching the seed's
		// "scale/<id>.jpg" Path. Using os.WriteFile with a zero-length
		// payload because reconcile only reads sizes — content doesn't
		// matter, but the file must exist for the walk to find it.
		for _, id := range ids {
			full := filepath.Join(ownerRoot, id+".jpg")
			if err := os.WriteFile(full, nil, 0o644); err != nil {
				fixErr = fmt.Errorf("touch %s: %w", full, err)
				return
			}
		}
		fixDB = d
		fixNASRoot = nasRoot
	})
	require.NoError(b, fixErr)
	if fixDB == nil {
		panic("loadReconcileFixture: fixDB nil after error-free seed — fixture init is broken")
	}
	return fixDB, fixNASRoot
}

// BenchmarkReconcile_10k_Clean times one reconcile pass against a
// 10k-row library whose on-disk state matches the DB exactly: zero
// orphans, zero missing, zero size-mismatches. This is the steady-
// state cost the daemon pays at server boot and on the periodic
// reconcile schedule. CommitDeletes/CommitTemps are false because
// the bench is read-mostly — exercising the diff path, not the
// mutation path.
//
// The mediaseed seed sets media.Size=1000 but the touched files are
// 0 bytes, so this bench actually exercises the size-mismatch path
// at 10k scale (every row is a mismatch). That makes the bench slower
// than a "truly clean" library would be, but it pins the worst-case
// behavior: a freshly-imported library where the importer's recorded
// size and the on-disk size happen to disagree everywhere produces a
// 10k-element SizeMismatch slice. If that path regresses, this bench
// is where it shows.
func BenchmarkReconcile_10k_Clean(b *testing.B) {
	d, nasRoot := loadReconcileFixture(b)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	opts := reconcile.Options{
		Owner:      benchOwner,
		StorageKey: benchStorageKey,
		NASRoot:    nasRoot,
	}
	ctx := context.Background()

	probe, err := reconcile.Reconcile(ctx, repo, opts)
	require.NoError(b, err)
	// Pin the intended fixture shape: every seeded row has
	// media.Size=1000 in the DB but a 0-byte file on disk, so the
	// bench measures the worst-case size-mismatch path with all
	// 10k rows in the SizeMismatch slice. orphans/missing/stale
	// must all be zero — anything else means the fixture
	// drifted (e.g. mediaseed stopped writing files for some rows,
	// or the walk picked up an unrelated file). Asserting before
	// ResetTimer means a drift fails the bench rather than
	// silently changing what we're measuring.
	require.Empty(b, probe.Orphans, "orphans must be empty for the clean fixture")
	require.Empty(b, probe.Missing, "missing must be empty for the clean fixture")
	require.Empty(b, probe.StaleTemps, "stale_temps must be empty for the clean fixture")
	require.Len(b, probe.SizeMismatch, benchScale,
		"size_mismatch must cover every seeded row (Size=1000 vs 0-byte file)")
	b.Logf("Reconcile_10k_Clean: orphans=%d missing=%d size_mismatch=%d stale_temps=%d",
		len(probe.Orphans), len(probe.Missing),
		len(probe.SizeMismatch), len(probe.StaleTemps))

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, err := reconcile.Reconcile(ctx, repo, opts)
		require.NoError(b, err)
	}
}
