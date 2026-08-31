package checkout

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil"
)

func TestScannerQueuesSettledTrackedChangeAcrossRestart(t *testing.T) {
	r := require.New(t)
	fixture := newScannerFixture(t)
	changed := []byte("external editor changed the xmp metadata")
	fixture.writeTracked(t, changed, fixture.now.Add(time.Minute))

	result, err := fixture.scanner().Scan(t.Context())
	r.NoError(err)
	r.Zero(result.Pending)
	r.Equal(EntryClean, fixture.entry(t).State)

	fixture.now = fixture.now.Add(3 * time.Second)
	result, err = fixture.scanner().Scan(t.Context())
	r.NoError(err)
	r.Equal(1, result.Pending)
	entry := fixture.entry(t)
	r.Equal(EntryPending, entry.State)
	r.Equal(digestOf(changed), entry.ObservedSHA256)
	candidates, err := fixture.repo.ListScanCandidates(t.Context(), fixture.checkout.ID)
	r.NoError(err)
	r.Empty(candidates)
}

func TestScannerKeepsTimestampOnlyChangeClean(t *testing.T) {
	r := require.New(t)
	fixture := newScannerFixture(t)
	changedMTime := fixture.now.Add(time.Minute)
	r.NoError(os.Chtimes(fixture.trackedPath(), changedMTime, changedMTime))

	_, err := fixture.scanner().Scan(t.Context())
	r.NoError(err)
	fixture.now = fixture.now.Add(3 * time.Second)
	result, err := fixture.scanner().Scan(t.Context())
	r.NoError(err)
	r.Equal(1, result.Clean)
	entry := fixture.entry(t)
	r.Equal(EntryClean, entry.State)
	r.True(entry.ObservedMTime.Equal(changedMTime))
}

func TestScannerDetectsReplacementWithSameSizeAndMTime(t *testing.T) {
	r := require.New(t)
	fixture := newScannerFixture(t)
	entry := fixture.entry(t)
	if entry.ObservedIdentity == "" {
		t.Skip("filesystem does not expose a stable file identity")
	}
	replacement := bytes.Repeat([]byte("x"), len(fixture.base))
	temporary := filepath.Join(fixture.root, "replacement.tmp")
	r.NoError(os.WriteFile(temporary, replacement, 0o600))
	r.NoError(os.Chtimes(temporary, entry.ObservedMTime, entry.ObservedMTime))
	r.NoError(os.Remove(fixture.trackedPath()))
	r.NoError(os.Rename(temporary, fixture.trackedPath()))
	replacedInfo, err := os.Stat(fixture.trackedPath())
	r.NoError(err)
	r.Equal(entry.ObservedSize, replacedInfo.Size())
	r.True(entry.ObservedMTime.Equal(replacedInfo.ModTime()))

	result, err := fixture.scanner().Scan(t.Context())
	r.NoError(err)
	r.Zero(result.Pending)
	fixture.now = fixture.now.Add(3 * time.Second)
	result, err = fixture.scanner().Scan(t.Context())
	r.NoError(err)
	r.Equal(1, result.Pending)
	r.Equal(digestOf(replacement), fixture.entry(t).ObservedSHA256)
}

func TestScannerRecordsMissingWithoutChangingAuthorityBinding(t *testing.T) {
	r := require.New(t)
	fixture := newScannerFixture(t)
	r.NoError(os.Remove(fixture.trackedPath()))

	result, err := fixture.scanner().Scan(t.Context())
	r.NoError(err)
	r.Equal(1, result.Missing)
	entry := fixture.entry(t)
	r.Equal(EntryMissing, entry.State)
	r.Equal("version-1", entry.BaseVersionID)
	r.Equal(digestOf(fixture.base), entry.BaseSHA256)
}

func TestScannerMarksTrackedDirectoryAsErrorWithoutScanningChildren(t *testing.T) {
	r := require.New(t)
	fixture := newScannerFixture(t)
	r.NoError(os.Remove(fixture.trackedPath()))
	r.NoError(os.Mkdir(fixture.trackedPath(), 0o700))
	r.NoError(os.WriteFile(filepath.Join(fixture.trackedPath(), "child.xmp"), []byte("child"), 0o600))

	result, err := fixture.scanner().Scan(t.Context())
	r.NoError(err)
	r.Zero(result.Missing)
	r.Zero(result.Untracked)
	entry := fixture.entry(t)
	r.Equal(EntryError, entry.State)
	r.Contains(entry.LastError, "not a regular file")
	candidates, err := fixture.repo.ListScanCandidates(t.Context(), fixture.checkout.ID)
	r.NoError(err)
	r.Empty(candidates)
}

func TestScannerContinuesMissingReconciliationAfterUnreadableSubtree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows permission bits do not make a directory unreadable")
	}
	r := require.New(t)
	fixture := newScannerFixture(t)
	missing := fixture.entryRow
	missing.FileID = uuid.NewString()
	missing.RelativePath = "missing.xmp"
	r.NoError(fixture.repo.InsertEntry(t.Context(), missing))

	unreadable := filepath.Join(fixture.root, "2026")
	r.NoError(os.Chmod(unreadable, 0))
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o700) })
	result, err := fixture.scanner().Scan(t.Context())
	if err == nil {
		t.Skip("current user can traverse a directory without permission bits")
	}
	r.ErrorIs(err, fs.ErrPermission)
	r.Equal(1, result.Missing)

	entries, err := fixture.repo.ListEntries(t.Context(), fixture.checkout.ID)
	r.NoError(err)
	r.Len(entries, 2)
	states := make(map[string]EntryState, len(entries))
	for _, entry := range entries {
		states[entry.RelativePath] = entry.State
	}
	r.Equal(EntryError, states[fixture.entryRow.RelativePath])
	r.Equal(EntryMissing, states[missing.RelativePath])
}

func TestScannerContinuesMissingReconciliationAfterUnreadableUntrackedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows permission bits do not make a file unreadable")
	}
	r := require.New(t)
	fixture := newScannerFixture(t)
	r.NoError(os.Remove(fixture.trackedPath()))
	unreadable := filepath.Join(fixture.root, "unreadable.xmp")
	r.NoError(os.WriteFile(unreadable, []byte("metadata"), 0o600))
	r.NoError(os.Chmod(unreadable, 0))
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })

	result, err := fixture.scanner().Scan(t.Context())
	if err == nil {
		t.Skip("current user can read a file without permission bits")
	}
	r.ErrorIs(err, fs.ErrPermission)
	r.Equal(1, result.Missing)
	r.Equal(EntryMissing, fixture.entry(t).State)
}

func TestScannerContinuesMissingReconciliationAfterInvalidUntrackedPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows treats a backslash as a path separator")
	}
	r := require.New(t)
	fixture := newScannerFixture(t)
	r.NoError(os.Remove(fixture.trackedPath()))
	r.NoError(os.WriteFile(filepath.Join(fixture.root, `invalid\name.xmp`), []byte("metadata"), 0o600))

	result, err := fixture.scanner().Scan(t.Context())
	r.ErrorIs(err, errs.ErrInvalidArgument)
	r.Equal(1, result.Missing)
	r.Equal(EntryMissing, fixture.entry(t).State)
	candidates, err := fixture.repo.ListScanCandidates(t.Context(), fixture.checkout.ID)
	r.NoError(err)
	r.Empty(candidates)
}

func TestScannerQueuesSettledUntrackedFileAndIgnoresTransientPaths(t *testing.T) {
	r := require.New(t)
	fixture := newScannerFixture(t)
	untracked := []byte("new xmp sidecar")
	untrackedPath := filepath.Join(fixture.root, "new-sidecar.xmp")
	r.NoError(os.WriteFile(untrackedPath, untracked, 0o600))
	r.NoError(os.WriteFile(filepath.Join(fixture.root, "editor.catalog.lock"), []byte("lock"), 0o600))
	r.NoError(os.Mkdir(filepath.Join(fixture.root, stagingDirectory), 0o700))
	r.NoError(os.WriteFile(
		filepath.Join(fixture.root, stagingDirectory, "partial.tmp"), []byte("partial"), 0o600))

	_, err := fixture.scanner().Scan(t.Context())
	r.NoError(err)
	fixture.now = fixture.now.Add(3 * time.Second)
	result, err := fixture.scanner().Scan(t.Context())
	r.NoError(err)
	r.Equal(1, result.Untracked)
	candidates, err := fixture.repo.ListScanCandidates(t.Context(), fixture.checkout.ID)
	r.NoError(err)
	r.Len(candidates, 1)
	r.Equal("new-sidecar.xmp", candidates[0].RelativePath)
	r.Empty(candidates[0].FileID)
	r.Equal(ScanCandidatePending, candidates[0].State)
	r.Equal(digestOf(untracked), candidates[0].ObservedSHA256)
}

func TestPendingUntrackedCandidateWithoutIdentityReentersSettling(t *testing.T) {
	r := require.New(t)
	fixture := newScannerFixture(t)
	candidate := ScanCandidate{
		CheckoutID: fixture.checkout.ID, RelativePath: "new-sidecar.xmp",
		ObservedSize: 4, ObservedMTime: fixture.now, ObservedIdentity: "",
	}
	settled, err := fixture.repo.ObserveScanCandidate(
		t.Context(), candidate, 2*time.Second, fixture.now)
	r.NoError(err)
	r.False(settled)
	settled, err = fixture.repo.ObserveScanCandidate(
		t.Context(), candidate, 2*time.Second, fixture.now.Add(3*time.Second))
	r.NoError(err)
	r.True(settled)
	r.NoError(fixture.repo.FinalizeUntrackedScanCandidate(
		t.Context(), candidate, digestOf([]byte("body")), fixture.now.Add(3*time.Second)))

	settled, err = fixture.repo.ObserveScanCandidate(
		t.Context(), candidate, 2*time.Second, fixture.now.Add(4*time.Second))
	r.NoError(err)
	r.False(settled)
	candidates, err := fixture.repo.ListScanCandidates(t.Context(), fixture.checkout.ID)
	r.NoError(err)
	r.Len(candidates, 1)
	r.Equal(ScanCandidateSettling, candidates[0].State)
	r.Empty(candidates[0].ObservedSHA256)
}

func TestHashSettledFileHonorsCanceledContext(t *testing.T) {
	r := require.New(t)
	fixture := newScannerFixture(t)
	root, err := os.OpenRoot(fixture.root)
	r.NoError(err)
	t.Cleanup(func() { r.NoError(root.Close()) })
	info, err := root.Lstat(fixture.entryRow.RelativePath)
	r.NoError(err)
	observedInfo, identity, err := observeFileIdentity(root, fixture.entryRow.RelativePath, info)
	r.NoError(err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = hashSettledFile(ctx, root, ScanCandidate{
		RelativePath: fixture.entryRow.RelativePath,
		ObservedSize: observedInfo.Size(), ObservedMTime: observedInfo.ModTime().UTC(),
		ObservedIdentity: identity,
	})
	r.ErrorIs(err, context.Canceled)
}

func TestScanObservationErrorRetriesOnlyMissingPaths(t *testing.T) {
	permissionErr := scanObservationError("open", fs.ErrPermission)
	require.ErrorIs(t, permissionErr, fs.ErrPermission)
	require.NotErrorIs(t, permissionErr, errScanObservationChanged)
	require.ErrorIs(t,
		scanObservationError("open", fs.ErrNotExist), errScanObservationChanged)
}

type scannerFixture struct {
	db       *db.DB
	repo     *Repo
	content  *content.Adapter
	checkout Checkout
	entryRow Entry
	root     string
	base     []byte
	now      time.Time
}

func newScannerFixture(t *testing.T) *scannerFixture {
	t.Helper()
	database := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "local", UserID: "alice"}
	now := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	_, err := database.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES (?, ?, ?, ?)`,
		owner.Hub, owner.UserID, uuid.NewString(), now)
	require.NoError(t, err)
	contentStore, err := content.Open(t.Context(), content.Config{Root: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, contentStore.Close()) })
	root := t.TempDir()
	base := []byte("authoritative xmp metadata")
	checkoutID := uuid.NewString()
	fileID := uuid.NewString()
	relativePath := filepath.ToSlash(filepath.Join("2026", "08", "30", uuid.NewString(), "photo.xmp"))
	absolutePath := filepath.Join(root, filepath.FromSlash(relativePath))
	require.NoError(t, os.MkdirAll(filepath.Dir(absolutePath), 0o700))
	require.NoError(t, os.WriteFile(absolutePath, base, 0o600))
	require.NoError(t, os.Chtimes(absolutePath, now, now))
	info, err := os.Stat(absolutePath)
	require.NoError(t, err)
	opened, err := os.Open(absolutePath)
	require.NoError(t, err)
	observedIdentity, err := filesystemIdentity(opened)
	require.NoError(t, err)
	require.NoError(t, opened.Close())
	repo := NewRepo(database.WriteDB(), database.ReadDB())
	checkout := Checkout{
		ID: checkoutID, Owner: owner, Root: root, Layout: "capture_date",
		Selection: Selection{All: true}, State: StateActive,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, repo.Insert(t.Context(), checkout))
	entry := Entry{
		CheckoutID: checkoutID, FileID: fileID, RelativePath: relativePath,
		BaseVersionID: "version-1", BaseSHA256: digestOf(base), BaseSize: int64(len(base)),
		ObservedSize: info.Size(), ObservedMTime: info.ModTime().UTC(),
		ObservedIdentity: observedIdentity, ObservedSHA256: digestOf(base), State: EntryClean,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, repo.InsertEntry(t.Context(), entry))
	return &scannerFixture{
		db: database, repo: repo, content: contentStore, checkout: checkout,
		entryRow: entry, root: root, base: base, now: now,
	}
}

func (f *scannerFixture) scanner() *Scanner {
	scanner := NewScanner(f.repo, f.content, ScannerConfig{
		ScanInterval: time.Hour, SettleInterval: 2 * time.Second,
		IgnorePatterns: []string{"*.catalog.lock"},
	})
	scanner.now = func() time.Time { return f.now }
	return scanner
}

func (f *scannerFixture) trackedPath() string {
	return filepath.Join(f.root, filepath.FromSlash(f.entryRow.RelativePath))
}

func (f *scannerFixture) writeTracked(t *testing.T, body []byte, mtime time.Time) {
	t.Helper()
	require.NoError(t, os.WriteFile(f.trackedPath(), body, 0o600))
	require.NoError(t, os.Chtimes(f.trackedPath(), mtime, mtime))
}

func (f *scannerFixture) entry(t *testing.T) Entry {
	t.Helper()
	entries, err := f.repo.ListEntries(t.Context(), f.checkout.ID)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	return entries[0]
}

func digestOf(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}
