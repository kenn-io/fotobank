package backup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// makeFile writes name in dir with the given mtime via os.Chtimes,
// content is two random-looking bytes for size assertions.
func makeFile(t *testing.T, dir, name string, mtime time.Time) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte("xx"), 0o600))
	require.NoError(t, os.Chtimes(p, mtime, mtime))
	return p
}

func TestListReturnsNewestFirst(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

	makeFile(t, dir, "2026-04-25T11:00:00.000Z.sqlite", now.Add(-time.Hour))
	makeFile(t, dir, "2026-04-25T11:30:00.000Z.sqlite", now.Add(-30*time.Minute))
	makeFile(t, dir, "2026-04-25T11:45:00.000Z.sqlite", now.Add(-15*time.Minute))

	got, err := List(dir)
	r.NoError(err)
	r.Len(got, 3)
	r.Equal("2026-04-25T11:45:00.000Z.sqlite", filepath.Base(got[0].Path))
	r.Equal("2026-04-25T11:30:00.000Z.sqlite", filepath.Base(got[1].Path))
	r.Equal("2026-04-25T11:00:00.000Z.sqlite", filepath.Base(got[2].Path))
}

func TestListSkipsMalformedAndPartials(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

	makeFile(t, dir, "2026-04-25T11:00:00.000Z.sqlite", now)
	makeFile(t, dir, "not-a-snapshot.txt", now)
	makeFile(t, dir, "2026-04-25T11:00:00.000Z.sqlite.partial", now)
	makeFile(t, dir, "garbage.sqlite", now)

	got, err := List(dir)
	r.NoError(err)
	r.Len(got, 1)
	r.Equal("2026-04-25T11:00:00.000Z.sqlite", filepath.Base(got[0].Path))
}

func TestListEmptyDir(t *testing.T) {
	dir := t.TempDir()
	got, err := List(dir)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestListMissingDirIsEmpty(t *testing.T) {
	got, err := List(filepath.Join(t.TempDir(), "no-such"))
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestListParsesTimestampsCorrectly(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	makeFile(t, dir, "2026-04-25T11:30:45.123Z.sqlite", time.Now())
	got, err := List(dir)
	r.NoError(err)
	r.Len(got, 1)
	r.Equal(time.Date(2026, 4, 25, 11, 30, 45, 123_000_000, time.UTC), got[0].Timestamp)
}
