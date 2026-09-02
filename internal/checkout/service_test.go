package checkout_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/album"
	"go.kenn.io/fotobank/internal/checkout"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/contentresolver"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil"
	"go.kenn.io/fotobank/internal/testutil/assetfixture"
)

func TestServiceMaterializesExactVersionAndRecordsEntry(t *testing.T) {
	r := require.New(t)
	fixture := newFixture(t)
	captured := time.Date(2024, time.March, 8, 12, 0, 0, 0, time.UTC)
	body := []byte("authoritative photo bytes")
	item := assetfixture.InsertContent(t, fixture.media, fixture.content, body, media.Media{
		Owner: fixture.owner, Timestamp: &captured, OriginalFilename: "IMG_0042.JPG",
	})
	root := t.TempDir()
	validatedRoot, err := fixture.content.ResolveCheckoutRoot(root)
	r.NoError(err)

	result, err := fixture.service.Create(t.Context(), fixture.owner, checkout.CreateRequest{
		Root: validatedRoot, Selection: checkout.Selection{AssetIDs: []string{item.ID}},
	})
	r.NoError(err)
	r.Equal(checkout.StateActive, result.Checkout.State)
	r.Equal(checkout.Estimate{Files: 1, Bytes: int64(len(body))}, result.Estimate)

	relative := filepath.Join("2024", "03", "08", item.ID, "IMG_0042.JPG")
	materialized, err := os.ReadFile(filepath.Join(root, relative))
	r.NoError(err)
	r.Equal(body, materialized)
	entries, err := fixture.checkouts.ListEntries(t.Context(), result.Checkout.ID)
	r.NoError(err)
	r.Len(entries, 1)
	r.Equal(filepath.ToSlash(relative), entries[0].RelativePath)
	r.Equal(item.CurrentVersionID, entries[0].BaseVersionID)
	r.Equal(item.SHA256, entries[0].BaseSHA256)
	r.Equal(checkout.EntryClean, entries[0].State)
	stored, err := fixture.checkouts.Get(t.Context(), result.Checkout.ID)
	r.NoError(err)
	r.Equal(checkout.StateActive, stored.State)
	r.Equal(result.Checkout.Selection, stored.Selection)
}

func TestCommitterPublishesTrackedEditAndInvalidatesPrimaryProjections(t *testing.T) {
	r := require.New(t)
	fixture := newFixture(t)
	original := []byte("original checkout bytes")
	replacement := []byte("edited checkout bytes with a different size")
	item, checkoutID, entry := createPendingEdit(t, fixture, original, replacement)
	_, err := fixture.db.WriteDB().ExecContext(t.Context(), `UPDATE assets SET
		timestamp = ?, make = 'Canon', model = 'EOS R5', latitude = 48.8566,
		longitude = 2.3522, location_label = 'Paris',
		source_metadata_version_id = ?,
		source_metadata_extractor_fingerprint = ?, source_metadata_checksum = ?
		WHERE id = ?`, time.Now().UTC(), item.CurrentVersionID,
		strings.Repeat("a", sha256.Size*2), strings.Repeat("b", sha256.Size*2), item.ID)
	r.NoError(err)

	tagResultID := uuid.NewString()
	captionResultID := uuid.NewString()
	_, err = fixture.db.WriteDB().ExecContext(t.Context(), `INSERT INTO ai_results
		(id, media_id, task, model_id, prompt_version, prompt_hash, input_profile, status, generated_at)
		VALUES (?, ?, 'tag', 'model', 'v1', 'hash', 'profile', 'active', ?),
		       (?, ?, 'caption', 'model', 'v1', 'hash', 'profile', 'active', ?)`,
		tagResultID, item.ID, time.Now().UTC(), captionResultID, item.ID, time.Now().UTC())
	r.NoError(err)
	_, err = fixture.db.WriteDB().ExecContext(t.Context(),
		`INSERT INTO media_tags(result_id, tag_key, tag_label, rank) VALUES (?, 'old', 'Old', 1)`,
		tagResultID)
	r.NoError(err)
	_, err = fixture.db.WriteDB().ExecContext(t.Context(),
		`INSERT INTO media_captions(result_id, text) VALUES (?, 'old caption')`, captionResultID)
	r.NoError(err)
	_, err = fixture.db.WriteDB().ExecContext(t.Context(), `INSERT INTO ai_jobs
		(id, media_id, task, fingerprint, status, attempts, enqueued_at)
		VALUES (?, ?, 'tag', 'claim', 'pending', 0, ?)`, uuid.NewString(), item.ID, time.Now().UTC())
	r.NoError(err)
	_, err = fixture.db.WriteDB().ExecContext(t.Context(), `INSERT INTO ai_failures
		(media_id, task, model_id, prompt_version, input_profile, last_error,
		 last_error_kind, attempt_count, failed_at)
		VALUES (?, 'caption', 'model', 'v1', 'profile', 'failed', 'provider', 1, ?)`,
		item.ID, time.Now().UTC())
	r.NoError(err)
	_, err = fixture.db.WriteDB().ExecContext(t.Context(), `INSERT INTO ai_skipped
		(media_id, task, reason, recorded_at) VALUES (?, 'embed', 'no_preview', ?)`,
		item.ID, time.Now().UTC())
	r.NoError(err)

	result, err := fixture.committer.Commit(t.Context(), fixture.owner, checkoutID)
	r.NoError(err)
	r.Equal(checkout.CommitResult{Pending: 1, Committed: 1}, result)

	entries, err := fixture.checkouts.ListEntries(t.Context(), checkoutID)
	r.NoError(err)
	r.Len(entries, 1)
	r.Equal(checkout.EntryClean, entries[0].State)
	r.NotEqual(entry.BaseVersionID, entries[0].BaseVersionID)
	r.Equal(entry.ObservedSHA256, entries[0].BaseSHA256)

	updated, err := fixture.media.GetByID(t.Context(), item.ID)
	r.NoError(err)
	r.Equal(entries[0].BaseVersionID, updated.CurrentVersionID)
	r.Equal(entry.ObservedSHA256, updated.SHA256)
	r.Equal(int64(len(replacement)), updated.Size)
	r.Equal("pending", updated.ThumbStatus)
	r.Equal(4, updated.ThumbVersion)
	r.Nil(updated.Timestamp)
	r.Empty(updated.Make)
	r.Nil(updated.Latitude)
	asset, err := media.NewAssetRepo(fixture.db.WriteDB(), fixture.db.ReadDB()).GetAsset(t.Context(), item.ID)
	r.NoError(err)
	r.Empty(asset.SourceMetadataVersionID)
	r.Empty(asset.SourceMetadataExtractorFingerprint)
	r.Empty(asset.SourceMetadataChecksum)

	prior, err := fixture.content.OpenVersion(t.Context(), item.CurrentVersionID)
	r.NoError(err)
	priorBytes, err := io.ReadAll(prior.Reader)
	r.NoError(err)
	r.NoError(prior.Reader.Verify())
	r.NoError(prior.Reader.Close())
	r.Equal(original, priorBytes)
	current, err := fixture.content.OpenVersion(t.Context(), updated.CurrentVersionID)
	r.NoError(err)
	currentBytes, err := io.ReadAll(current.Reader)
	r.NoError(err)
	r.NoError(current.Reader.Verify())
	r.NoError(current.Reader.Close())
	r.Equal(replacement, currentBytes)

	var activeResults, liveJobs, failures, skipped int
	r.NoError(fixture.db.ReadDB().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM ai_results WHERE media_id = ? AND status = 'active'`, item.ID).
		Scan(&activeResults))
	r.NoError(fixture.db.ReadDB().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM ai_jobs WHERE media_id = ? AND status IN ('pending','working','blocked')`, item.ID).
		Scan(&liveJobs))
	r.NoError(fixture.db.ReadDB().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM ai_failures WHERE media_id = ?`, item.ID).Scan(&failures))
	r.NoError(fixture.db.ReadDB().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM ai_skipped WHERE media_id = ?`, item.ID).Scan(&skipped))
	r.Zero(activeResults)
	r.Zero(liveJobs)
	r.Zero(failures)
	r.Zero(skipped)
	var captionText, tagLabels string
	r.NoError(fixture.db.ReadDB().QueryRowContext(t.Context(),
		`SELECT caption_text, tag_label FROM media_fts WHERE media_id = ?`, item.ID).
		Scan(&captionText, &tagLabels))
	r.Empty(captionText)
	r.Empty(tagLabels)
}

func TestCommitterMarksStaleDocbankBaseAsConflict(t *testing.T) {
	r := require.New(t)
	fixture := newFixture(t)
	original := []byte("original checkout bytes")
	localEdit := []byte("local checkout edit")
	item, checkoutID, entry := createPendingEdit(t, fixture, original, localEdit)
	target, err := fixture.checkouts.GetCommitTarget(t.Context(), checkoutID, entry.FileID)
	r.NoError(err)
	otherEdit := []byte("a different committed edit")
	otherDigest := sha256.Sum256(otherEdit)
	_, err = fixture.content.Replace(t.Context(), content.ReplaceRequest{
		VirtualPath: target.VirtualPath,
		NodeID:      target.NodeID, BaseVersionID: entry.BaseVersionID,
		Base:      content.Identity{SHA256: entry.BaseSHA256, Size: entry.BaseSize},
		MediaType: target.MediaType,
		Expected: content.Identity{
			SHA256: hex.EncodeToString(otherDigest[:]), Size: int64(len(otherEdit)),
		},
		Reader: bytes.NewReader(otherEdit),
	})
	r.NoError(err)

	result, err := fixture.committer.Commit(t.Context(), fixture.owner, checkoutID)
	r.NoError(err)
	r.Equal(checkout.CommitResult{Pending: 1, Conflicts: 1}, result)
	entries, err := fixture.checkouts.ListEntries(t.Context(), checkoutID)
	r.NoError(err)
	r.Len(entries, 1)
	r.Equal(checkout.EntryConflict, entries[0].State)
	unchanged, err := fixture.media.GetByID(t.Context(), item.ID)
	r.NoError(err)
	r.Equal(item.CurrentVersionID, unchanged.CurrentVersionID)
}

func TestCommitterAdoptsExpectedDocbankHeadAfterInterruptedReceipt(t *testing.T) {
	r := require.New(t)
	fixture := newFixture(t)
	original := []byte("original checkout bytes")
	replacement := []byte("edited checkout bytes")
	item, checkoutID, entry := createPendingEdit(t, fixture, original, replacement)
	target, err := fixture.checkouts.GetCommitTarget(t.Context(), checkoutID, entry.FileID)
	r.NoError(err)
	receipt, err := fixture.content.Replace(t.Context(), content.ReplaceRequest{
		VirtualPath: target.VirtualPath,
		NodeID:      target.NodeID, BaseVersionID: entry.BaseVersionID,
		Base:      content.Identity{SHA256: entry.BaseSHA256, Size: entry.BaseSize},
		MediaType: target.MediaType,
		Expected: content.Identity{
			SHA256: entry.ObservedSHA256, Size: entry.ObservedSize,
		},
		Reader: bytes.NewReader(replacement),
	})
	r.NoError(err)
	r.False(receipt.Adopted)

	result, err := fixture.committer.Commit(t.Context(), fixture.owner, checkoutID)
	r.NoError(err)
	r.Equal(checkout.CommitResult{Pending: 1, Committed: 1}, result)
	updated, err := fixture.media.GetByID(t.Context(), item.ID)
	r.NoError(err)
	r.Equal(receipt.Version.ID, updated.CurrentVersionID)
	entries, err := fixture.checkouts.ListEntries(t.Context(), checkoutID)
	r.NoError(err)
	r.Len(entries, 1)
	r.Equal(checkout.EntryClean, entries[0].State)
	r.Equal(receipt.Version.ID, entries[0].BaseVersionID)
}

func TestCommitterRejectsDuplicateOwnerContentBeforeDocbankWrite(t *testing.T) {
	r := require.New(t)
	fixture := newFixture(t)
	original := []byte("original checkout bytes")
	replacement := []byte("bytes already owned by another file")
	_, checkoutID, entry := createPendingEdit(t, fixture, original, replacement)
	target, err := fixture.checkouts.GetCommitTarget(t.Context(), checkoutID, entry.FileID)
	r.NoError(err)
	assetfixture.InsertContent(t, fixture.media, fixture.content, replacement, media.Media{
		Owner: fixture.owner, OriginalFilename: "duplicate.jpg",
	})

	result, err := fixture.committer.Commit(t.Context(), fixture.owner, checkoutID)
	r.NoError(err)
	r.Equal(checkout.CommitResult{Pending: 1, Conflicts: 1}, result)
	stored, err := fixture.content.Stat(t.Context(), target.VirtualPath)
	r.NoError(err)
	r.Equal(entry.BaseVersionID, stored.CurrentVersionID)
}

func TestCommitterRecordsConflictWhenIdentityBecomesDuplicateAfterDocbankWrite(t *testing.T) {
	r := require.New(t)
	fixture := newFixture(t)
	original := []byte("original checkout bytes")
	replacement := []byte("edited checkout bytes")
	item, checkoutID, entry := createPendingEdit(t, fixture, original, replacement)
	target, err := fixture.checkouts.GetCommitTarget(t.Context(), checkoutID, entry.FileID)
	r.NoError(err)
	other := assetfixture.InsertContent(
		t, fixture.media, fixture.content, []byte("other file bytes"), media.Media{
			Owner: fixture.owner, OriginalFilename: "other.jpg",
		})
	_, err = fixture.db.WriteDB().ExecContext(t.Context(), `CREATE TABLE checkout_commit_collision (
		source_file_id TEXT NOT NULL, collision_file_id TEXT NOT NULL
	)`)
	r.NoError(err)
	_, err = fixture.db.WriteDB().ExecContext(t.Context(),
		`INSERT INTO checkout_commit_collision VALUES (?, ?)`, item.PrimaryFileID, other.PrimaryFileID)
	r.NoError(err)
	_, err = fixture.db.WriteDB().ExecContext(t.Context(), `CREATE TRIGGER collide_checkout_commit
		BEFORE UPDATE OF sha256 ON media_files
		WHEN OLD.id = (SELECT source_file_id FROM checkout_commit_collision)
		BEGIN
			UPDATE media_files SET sha256 = NEW.sha256
			WHERE id = (SELECT collision_file_id FROM checkout_commit_collision);
		END`)
	r.NoError(err)

	result, err := fixture.committer.Commit(t.Context(), fixture.owner, checkoutID)
	r.NoError(err)
	r.Equal(checkout.CommitResult{Pending: 1, Conflicts: 1}, result)
	stored, err := fixture.content.Stat(t.Context(), target.VirtualPath)
	r.NoError(err)
	r.NotEqual(entry.BaseVersionID, stored.CurrentVersionID)
	r.Equal(entry.ObservedSHA256, stored.SHA256)
	entries, err := fixture.checkouts.ListEntries(t.Context(), checkoutID)
	r.NoError(err)
	r.Len(entries, 1)
	r.Equal(checkout.EntryConflict, entries[0].State)
}

func TestNonCleanCommitForcesFreshCheckoutObservation(t *testing.T) {
	r := require.New(t)
	fixture := newFixture(t)
	original := []byte("original checkout bytes")
	committedEdit := []byte("first edit bytes")
	laterEdit := []byte("other edit bytes")
	r.Len(laterEdit, len(committedEdit))
	_, checkoutID, entry := createPendingEdit(t, fixture, original, committedEdit)
	target, err := fixture.checkouts.GetCommitTarget(t.Context(), checkoutID, entry.FileID)
	r.NoError(err)
	receipt, err := fixture.content.Replace(t.Context(), content.ReplaceRequest{
		VirtualPath: target.VirtualPath,
		NodeID:      target.NodeID, BaseVersionID: entry.BaseVersionID,
		Base:      content.Identity{SHA256: entry.BaseSHA256, Size: entry.BaseSize},
		MediaType: target.MediaType,
		Expected: content.Identity{
			SHA256: entry.ObservedSHA256, Size: entry.ObservedSize,
		},
		Reader: bytes.NewReader(committedEdit),
	})
	r.NoError(err)
	storedCheckout, err := fixture.checkouts.Get(t.Context(), checkoutID)
	r.NoError(err)
	workingPath := filepath.Join(storedCheckout.Root, filepath.FromSlash(entry.RelativePath))
	r.NoError(os.WriteFile(workingPath, laterEdit, 0o600))
	r.NoError(os.Chtimes(workingPath, entry.ObservedMTime, entry.ObservedMTime))
	r.NoError(fixture.checkouts.ApplyCommit(t.Context(), target, checkout.CommitReceipt{
		NodeID: receipt.Node.ID, VersionID: receipt.Version.ID,
		SHA256: receipt.Identity.SHA256, Size: receipt.Identity.Size,
	}, false, time.Now().UTC()))

	scanner := checkout.NewScanner(fixture.checkouts, fixture.content, checkout.ScannerConfig{
		ScanInterval: time.Second, SettleInterval: 0,
	})
	_, err = scanner.Scan(t.Context())
	r.NoError(err)
	_, err = scanner.Scan(t.Context())
	r.NoError(err)
	entries, err := fixture.checkouts.ListEntries(t.Context(), checkoutID)
	r.NoError(err)
	r.Len(entries, 1)
	digest := sha256.Sum256(laterEdit)
	r.Equal(hex.EncodeToString(digest[:]), entries[0].ObservedSHA256)
	r.Equal(checkout.EntryPending, entries[0].State)
}

func TestServiceKeepsTemporaryFilesSeparateFromOriginalNames(t *testing.T) {
	r := require.New(t)
	fixture := newFixture(t)
	body := []byte("authoritative photo bytes")
	item := assetfixture.InsertContent(t, fixture.media, fixture.content, body, media.Media{
		Owner: fixture.owner, OriginalFilename: "initial.jpg",
	})
	collidingName := ".fotobank-" + item.PrimaryFileID + ".tmp"
	_, err := fixture.db.WriteDB().ExecContext(t.Context(),
		`UPDATE media_files SET original_filename = ? WHERE id = ?`,
		collidingName, item.PrimaryFileID)
	r.NoError(err)
	root := t.TempDir()
	validatedRoot, err := fixture.content.ResolveCheckoutRoot(root)
	r.NoError(err)

	result, err := fixture.service.Create(t.Context(), fixture.owner, checkout.CreateRequest{
		Root: validatedRoot, Selection: checkout.Selection{AssetIDs: []string{item.ID}},
	})
	r.NoError(err)
	r.Equal(checkout.StateActive, result.Checkout.State)
	materialized, err := os.ReadFile(filepath.Join(root, "undated", item.ID, collidingName))
	r.NoError(err)
	r.Equal(body, materialized)
	r.NoDirExists(filepath.Join(root, ".fotobank-staging"))
}

func TestServiceRejectsCheckoutRootMovedAfterValidation(t *testing.T) {
	r := require.New(t)
	fixture := newFixture(t)
	item := assetfixture.InsertContent(t, fixture.media, fixture.content, []byte("photo"), media.Media{
		Owner: fixture.owner, OriginalFilename: "IMG_0042.JPG",
	})
	parent := t.TempDir()
	root := filepath.Join(parent, "checkout")
	r.NoError(os.Mkdir(root, 0o700))
	validatedRoot, err := fixture.content.ResolveCheckoutRoot(root)
	r.NoError(err)
	t.Cleanup(func() { r.NoError(validatedRoot.Close()) })
	if err := os.Rename(root, filepath.Join(parent, "moved-checkout")); err != nil {
		t.Skipf("renaming an opened directory is unavailable: %v", err)
	}
	r.NoError(os.Mkdir(root, 0o700))

	_, err = fixture.service.Create(t.Context(), fixture.owner, checkout.CreateRequest{
		Root: validatedRoot, Selection: checkout.Selection{AssetIDs: []string{item.ID}},
	})
	r.ErrorIs(err, errs.ErrBadConfiguration)
	var checkouts int
	r.NoError(fixture.db.ReadDB().QueryRowContext(t.Context(), `SELECT count(*) FROM checkouts`).Scan(&checkouts))
	r.Zero(checkouts)
}

func TestServiceRejectsAssetHiddenAfterSelection(t *testing.T) {
	r := require.New(t)
	fixture := newFixture(t)
	item := assetfixture.InsertContent(t, fixture.media, fixture.content, []byte("photo"), media.Media{
		Owner: fixture.owner, OriginalFilename: "IMG_0042.JPG",
	})
	_, err := fixture.db.WriteDB().ExecContext(t.Context(), `
		CREATE TRIGGER hide_assets_after_checkout_insert
		AFTER INSERT ON checkouts
		BEGIN
			UPDATE assets SET hidden_at = CURRENT_TIMESTAMP;
		END`)
	r.NoError(err)
	root := t.TempDir()
	validatedRoot, err := fixture.content.ResolveCheckoutRoot(root)
	r.NoError(err)

	_, err = fixture.service.Create(t.Context(), fixture.owner, checkout.CreateRequest{
		Root: validatedRoot, Selection: checkout.Selection{AssetIDs: []string{item.ID}},
	})
	r.ErrorIs(err, errs.ErrNotFound)
	var state checkout.State
	r.NoError(fixture.db.ReadDB().QueryRowContext(t.Context(),
		`SELECT state FROM checkouts`).Scan(&state))
	r.Equal(checkout.StateError, state)
}

func TestCheckoutRetainsBindingsAfterSourceDeletion(t *testing.T) {
	r := require.New(t)
	fixture := newFixture(t)
	body := []byte("authoritative photo bytes")
	item := assetfixture.InsertContent(t, fixture.media, fixture.content, body, media.Media{
		Owner: fixture.owner, OriginalFilename: "IMG_0042.JPG",
	})
	now := time.Now().UTC()
	albumID := uuid.NewString()
	albums := album.NewRepo(fixture.db.WriteDB(), fixture.db.ReadDB())
	r.NoError(albums.Insert(t.Context(), album.Album{
		ID: albumID, Owner: fixture.owner, Name: "Working Copy", CreatedAt: now, UpdatedAt: now,
	}))
	added, present, err := albums.AddMedia(t.Context(), albumID, []string{item.ID}, now)
	r.NoError(err)
	r.Equal(1, added)
	r.Zero(present)
	root := t.TempDir()
	validatedRoot, err := fixture.content.ResolveCheckoutRoot(root)
	r.NoError(err)

	result, err := fixture.service.Create(t.Context(), fixture.owner, checkout.CreateRequest{
		Root: validatedRoot,
		Selection: checkout.Selection{
			AssetIDs: []string{item.ID}, AlbumIDs: []string{albumID},
		},
	})
	r.NoError(err)
	entries, err := fixture.checkouts.ListEntries(t.Context(), result.Checkout.ID)
	r.NoError(err)
	r.Len(entries, 1)
	r.NoError(albums.Delete(t.Context(), albumID))
	r.NoError(fixture.media.Delete(t.Context(), item.ID))

	stored, err := fixture.checkouts.Get(t.Context(), result.Checkout.ID)
	r.NoError(err)
	r.Equal([]string{item.ID}, stored.Selection.AssetIDs)
	r.Equal([]string{albumID}, stored.Selection.AlbumIDs)
	retainedEntries, err := fixture.checkouts.ListEntries(t.Context(), result.Checkout.ID)
	r.NoError(err)
	r.Equal(entries, retainedEntries)
	r.FileExists(filepath.Join(root, filepath.FromSlash(entries[0].RelativePath)))
}

func TestRepoResolveSelectionSupportsAlbumsYearsAndAll(t *testing.T) {
	r := require.New(t)
	fixture := newFixture(t)
	captured := time.Date(2022, time.July, 4, 0, 0, 0, 0, time.UTC)
	byYear := assetfixture.InsertContent(t, fixture.media, fixture.content, []byte("year"), media.Media{
		Owner: fixture.owner, Timestamp: &captured,
	})
	byAlbum := assetfixture.InsertContent(t, fixture.media, fixture.content, []byte("album"), media.Media{
		Owner: fixture.owner,
	})
	hiddenAt := time.Now().UTC()
	hidden := assetfixture.InsertContent(t, fixture.media, fixture.content, []byte("hidden"), media.Media{
		Owner: fixture.owner, HiddenAt: &hiddenAt,
	})
	now := time.Now().UTC()
	albumID := uuid.NewString()
	albums := album.NewRepo(fixture.db.WriteDB(), fixture.db.ReadDB())
	r.NoError(albums.Insert(t.Context(), album.Album{
		ID: albumID, Owner: fixture.owner, Name: "Working Copy", CreatedAt: now, UpdatedAt: now,
	}))
	added, present, err := albums.AddMedia(t.Context(), albumID, []string{byAlbum.ID}, now)
	r.NoError(err)
	r.Equal(1, added)
	r.Zero(present)

	candidates, err := fixture.checkouts.ResolveSelection(t.Context(), fixture.owner, checkout.Selection{
		AlbumIDs: []string{albumID}, Years: []checkout.YearRange{{Start: 2022, End: 2022}},
	})
	r.NoError(err)
	r.Len(candidates, 2)
	all, err := fixture.checkouts.ResolveSelection(t.Context(), fixture.owner, checkout.Selection{All: true})
	r.NoError(err)
	r.Len(all, 2)
	r.ElementsMatch([]string{byYear.ID, byAlbum.ID}, []string{candidates[0].AssetID, candidates[1].AssetID})
	_, err = fixture.checkouts.ResolveSelection(t.Context(), fixture.owner, checkout.Selection{
		AssetIDs: []string{hidden.ID},
	})
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestServiceRequiresCapacityAcceptanceForAllAssets(t *testing.T) {
	r := require.New(t)
	fixture := newFixture(t)
	assetfixture.InsertContent(t, fixture.media, fixture.content, []byte("photo"), media.Media{
		Owner: fixture.owner,
	})

	firstRoot, err := fixture.content.ResolveCheckoutRoot(t.TempDir())
	r.NoError(err)
	_, err = fixture.service.Create(t.Context(), fixture.owner, checkout.CreateRequest{
		Root: firstRoot, Selection: checkout.Selection{All: true},
	})
	r.ErrorIs(err, errs.ErrInvalidArgument)
	secondRoot, err := fixture.content.ResolveCheckoutRoot(t.TempDir())
	r.NoError(err)
	_, err = fixture.service.Create(t.Context(), fixture.owner, checkout.CreateRequest{
		Root: secondRoot, Selection: checkout.Selection{All: true}, CapacityLimit: 4,
	})
	r.ErrorIs(err, errs.ErrInvalidArgument)
	thirdRoot, err := fixture.content.ResolveCheckoutRoot(t.TempDir())
	r.NoError(err)
	result, err := fixture.service.Create(t.Context(), fixture.owner, checkout.CreateRequest{
		Root: thirdRoot, Selection: checkout.Selection{All: true}, CapacityLimit: 5,
	})
	r.NoError(err)
	r.Equal(int64(5), result.Estimate.Bytes)
}

func TestServiceRejectsCheckoutRootsOverlappingLiveCheckout(t *testing.T) {
	testCases := map[string]struct {
		liveIsParent bool
	}{
		"requested child":  {liveIsParent: true},
		"requested parent": {liveIsParent: false},
	}
	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			r := require.New(t)
			fixture := newFixture(t)
			parent := filepath.Join(t.TempDir(), "checkout")
			child := filepath.Join(parent, "child")
			r.NoError(os.MkdirAll(child, 0o700))
			liveRoot, requestedRoot := child, parent
			if testCase.liveIsParent {
				liveRoot, requestedRoot = parent, child
			}
			validatedLiveRoot, err := fixture.content.ResolveCheckoutRoot(liveRoot)
			r.NoError(err)
			canonicalLiveRoot := validatedLiveRoot.Path()
			r.NoError(validatedLiveRoot.Close())
			now := time.Now().UTC()
			r.NoError(fixture.checkouts.Insert(t.Context(), checkout.Checkout{
				ID: uuid.NewString(), Owner: fixture.owner, Root: canonicalLiveRoot, Layout: "capture_date",
				Selection: checkout.Selection{All: true}, State: checkout.StateActive,
				CreatedAt: now, UpdatedAt: now,
			}))
			validatedRoot, err := fixture.content.ResolveCheckoutRoot(requestedRoot)
			r.NoError(err)

			_, err = fixture.service.Create(t.Context(), fixture.owner, checkout.CreateRequest{
				Root: validatedRoot, Selection: checkout.Selection{All: true}, CapacityLimit: 1,
			})
			r.ErrorIs(err, errs.ErrAlreadyExists)
			r.Contains(err.Error(), "overlaps live checkout")
		})
	}
}

func TestServiceRejectsRenamedLiveCheckoutRoot(t *testing.T) {
	for _, replaceWithAlias := range []bool{false, true} {
		name := "missing catalog path"
		if replaceWithAlias {
			name = "replacement alias"
		}
		t.Run(name, func(t *testing.T) {
			r := require.New(t)
			fixture := newFixture(t)
			item := assetfixture.InsertContent(t, fixture.media, fixture.content, []byte("photo"), media.Media{
				Owner: fixture.owner, OriginalFilename: "IMG_0042.JPG",
			})
			parent := t.TempDir()
			liveRoot := filepath.Join(parent, "live")
			r.NoError(os.Mkdir(liveRoot, 0o700))
			validatedLiveRoot, err := fixture.content.ResolveCheckoutRoot(liveRoot)
			r.NoError(err)
			canonicalLiveRoot := validatedLiveRoot.Path()
			r.NoError(validatedLiveRoot.Close())
			now := time.Now().UTC()
			r.NoError(fixture.checkouts.Insert(t.Context(), checkout.Checkout{
				ID: uuid.NewString(), Owner: fixture.owner, Root: canonicalLiveRoot, Layout: "capture_date",
				Selection: checkout.Selection{All: true}, State: checkout.StateActive,
				CreatedAt: now, UpdatedAt: now,
			}))
			movedRoot := filepath.Join(parent, "moved")
			r.NoError(os.Rename(liveRoot, movedRoot))
			if replaceWithAlias {
				if err := os.Symlink(movedRoot, liveRoot); err != nil {
					t.Skipf("symlink creation unavailable: %v", err)
				}
			}
			requestedRoot, err := fixture.content.ResolveCheckoutRoot(movedRoot)
			r.NoError(err)

			_, err = fixture.service.Create(t.Context(), fixture.owner, checkout.CreateRequest{
				Root: requestedRoot, Selection: checkout.Selection{AssetIDs: []string{item.ID}},
			})
			r.ErrorIs(err, errs.ErrAlreadyExists)
			r.Contains(err.Error(), "overlaps live checkout")
		})
	}
}

type fixture struct {
	db        *db.DB
	owner     owners.Principal
	content   *content.Adapter
	media     *media.Repo
	checkouts *checkout.Repo
	service   *checkout.Materializer
	committer *checkout.Committer
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	r := require.New(t)
	database := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "local", UserID: "alice"}
	_, err := database.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES (?, ?, ?, ?)`,
		owner.Hub, owner.UserID, uuid.NewString(), time.Now().UTC())
	r.NoError(err)
	contentStore, err := content.Open(t.Context(), content.Config{Root: t.TempDir()})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(contentStore.Close()) })
	mediaRepo := media.NewRepo(database.WriteDB(), database.ReadDB())
	checkoutRepo := checkout.NewRepo(database.WriteDB(), database.ReadDB())
	resolver := contentresolver.New(mediaRepo, contentStore)
	lockDir := t.TempDir()
	return fixture{
		db: database, owner: owner, content: contentStore, media: mediaRepo,
		checkouts: checkoutRepo,
		service: checkout.NewMaterializer(
			checkoutRepo, resolver, filepath.Join(lockDir, "checkout.lock")),
		committer: checkout.NewCommitter(checkoutRepo, contentStore),
	}
}

func createPendingEdit(
	t *testing.T,
	fixture fixture,
	original []byte,
	replacement []byte,
) (media.Media, string, checkout.Entry) {
	t.Helper()
	r := require.New(t)
	item := assetfixture.InsertContent(t, fixture.media, fixture.content, original, media.Media{
		Owner: fixture.owner, OriginalFilename: "IMG_0042.JPG",
		ThumbStatus: "ready", ThumbVersion: 3,
	})
	root := t.TempDir()
	validatedRoot, err := fixture.content.ResolveCheckoutRoot(root)
	r.NoError(err)
	result, err := fixture.service.Create(t.Context(), fixture.owner, checkout.CreateRequest{
		Root: validatedRoot, Selection: checkout.Selection{AssetIDs: []string{item.ID}},
	})
	r.NoError(err)
	entries, err := fixture.checkouts.ListEntries(t.Context(), result.Checkout.ID)
	r.NoError(err)
	r.Len(entries, 1)
	workingPath := filepath.Join(root, filepath.FromSlash(entries[0].RelativePath))
	r.NoError(os.WriteFile(workingPath, replacement, 0o600))
	scanner := checkout.NewScanner(fixture.checkouts, fixture.content, checkout.ScannerConfig{
		ScanInterval: time.Second, SettleInterval: 0,
	})
	_, err = scanner.Scan(t.Context())
	r.NoError(err)
	_, err = scanner.Scan(t.Context())
	r.NoError(err)
	entries, err = fixture.checkouts.ListEntries(t.Context(), result.Checkout.ID)
	r.NoError(err)
	r.Len(entries, 1)
	r.Equal(checkout.EntryPending, entries[0].State)
	return item, result.Checkout.ID, entries[0]
}
