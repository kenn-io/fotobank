package checkout_test

import (
	"context"
	"os"
	"path/filepath"
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
		ID: albumID, Owner: fixture.owner, Name: "Lightroom", CreatedAt: now, UpdatedAt: now,
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
		ID: albumID, Owner: fixture.owner, Name: "Lightroom", CreatedAt: now, UpdatedAt: now,
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

type fixture struct {
	db        *db.DB
	owner     owners.Principal
	content   *content.Adapter
	media     *media.Repo
	checkouts *checkout.Repo
	service   *checkout.Materializer
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
	}
}
