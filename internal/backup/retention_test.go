package backup

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func mkSnap(t *testing.T, dir string, ts time.Time) string {
	t.Helper()
	name := ts.UTC().Format(StampLayout) + SnapshotExt
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
	require.NoError(t, os.Chtimes(p, ts, ts))
	return p
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func surviving(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	slices.Sort(out)
	return out
}

func TestSweepKeeps15MinTier(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

	// Five snapshots at 15-min intervals; Keep15Min=4 means oldest is dropped.
	for i := range 5 {
		mkSnap(t, dir, now.Add(-time.Duration(i)*15*time.Minute))
	}
	policy := Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7}
	res, err := Sweep(dir, policy, now, quietLogger())
	r.NoError(err)
	r.Equal(4, res.Kept15Min)
	// The 5th snapshot at age=60min (exactly 1h) is in the hourly tier,
	// not the 15-min tier. It survives as the only hourly representative.
	r.Equal(1, res.KeptHourly)
	r.Equal(0, res.KeptDaily)
	r.Equal(0, res.Deleted)
	r.Len(surviving(t, dir), 5)
}

func TestSweepDoesNotPromoteYoungSnapshotPastFullTier(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	// Six snapshots within the last hour, two per 15-min slot.
	// Keep15Min=4 means we keep four (one per slot, newest each).
	mkSnap(t, dir, now.Add(-1*time.Minute))
	mkSnap(t, dir, now.Add(-2*time.Minute))
	mkSnap(t, dir, now.Add(-16*time.Minute))
	mkSnap(t, dir, now.Add(-17*time.Minute))
	mkSnap(t, dir, now.Add(-31*time.Minute))
	mkSnap(t, dir, now.Add(-46*time.Minute))
	policy := Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7}
	res, err := Sweep(dir, policy, now, quietLogger())
	r.NoError(err)
	r.Equal(4, res.Kept15Min)
	r.Equal(0, res.KeptHourly, "young snapshot whose 15-min slot is full must NOT be promoted to hourly")
	r.Equal(2, res.Deleted) // the two duplicates in the -1/-2 and -16/-17 slots
}

func TestSweepDeletesFutureSnapshots(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	future := mkSnap(t, dir, now.Add(time.Hour))
	policy := Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7}
	_, err := Sweep(dir, policy, now, quietLogger())
	r.NoError(err)
	_, statErr := os.Stat(future)
	r.True(os.IsNotExist(statErr), "future snapshot must be deleted")
}

func TestSweepDeletesOlderThanWeek(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	old := mkSnap(t, dir, now.Add(-8*24*time.Hour))
	policy := Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7}
	res, err := Sweep(dir, policy, now, quietLogger())
	r.NoError(err)
	r.Equal(1, res.Deleted)
	_, statErr := os.Stat(old)
	r.True(os.IsNotExist(statErr))
}

func TestSweepCleansPartialsOlderThan24h(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

	// Two .partial files: one 25h old (should be deleted), one 23h old (kept).
	old := filepath.Join(dir, "old.sqlite.partial")
	r.NoError(os.WriteFile(old, []byte("x"), 0o600))
	r.NoError(os.Chtimes(old, now.Add(-25*time.Hour), now.Add(-25*time.Hour)))

	young := filepath.Join(dir, "young.sqlite.partial")
	r.NoError(os.WriteFile(young, []byte("x"), 0o600))
	r.NoError(os.Chtimes(young, now.Add(-23*time.Hour), now.Add(-23*time.Hour)))

	policy := Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7}
	_, err := Sweep(dir, policy, now, quietLogger())
	r.NoError(err)

	_, statErr := os.Stat(old)
	r.True(os.IsNotExist(statErr), "25h-old partial must be deleted")
	_, statErr = os.Stat(young)
	r.NoError(statErr, "23h-old partial must be kept")
}

func TestSweepNilLoggerOK(t *testing.T) {
	dir := t.TempDir()
	policy := Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7}
	_, err := Sweep(dir, policy, time.Now(), nil)
	require.NoError(t, err)
}

func TestSweepRootStaysBoundAfterRootRename(t *testing.T) {
	r := require.New(t)
	parent := t.TempDir()
	original := filepath.Join(parent, "nas")
	moved := filepath.Join(parent, "nas-moved")
	snapshotDir := filepath.Join(original, "snapshots")
	r.NoError(os.MkdirAll(snapshotDir, 0o700))
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	oldName := filepath.Base(mkSnap(t, snapshotDir, now.Add(-8*24*time.Hour)))
	root, err := os.OpenRoot(original)
	r.NoError(err)
	defer root.Close()
	if err := os.Rename(original, moved); err != nil {
		t.Skipf("platform cannot rename an opened directory: %v", err)
	}
	replacementDir := filepath.Join(original, "snapshots")
	r.NoError(os.MkdirAll(replacementDir, 0o700))
	replacement := filepath.Join(replacementDir, oldName)
	r.NoError(os.WriteFile(replacement, []byte("replacement"), 0o600))

	res, err := sweepRoot(
		root,
		"snapshots",
		Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7},
		now,
		quietLogger(),
	)
	r.NoError(err)
	r.Equal(1, res.Deleted)
	r.NoFileExists(filepath.Join(moved, "snapshots", oldName))
	r.FileExists(replacement)
}
