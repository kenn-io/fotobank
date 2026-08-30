package checkout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil"
)

func TestScannerQueuesSettledTrackedChangeAcrossRestart(t *testing.T) {
	r := require.New(t)
	fixture := newScannerFixture(t)
	changed := []byte("lightroom changed the xmp metadata")
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

func TestScannerQueuesSettledUntrackedFileAndIgnoresTransientPaths(t *testing.T) {
	r := require.New(t)
	fixture := newScannerFixture(t)
	untracked := []byte("new xmp sidecar")
	untrackedPath := filepath.Join(fixture.root, "new-sidecar.xmp")
	r.NoError(os.WriteFile(untrackedPath, untracked, 0o600))
	r.NoError(os.WriteFile(filepath.Join(fixture.root, "catalog.lrcat.lock"), []byte("lock"), 0o600))
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
		ObservedSHA256: digestOf(base), State: EntryClean,
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
		IgnorePatterns: []string{"*.lrcat.lock"},
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
