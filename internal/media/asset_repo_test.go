package media_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil"
)

func TestAssetRepoInsertGraphReady(t *testing.T) {
	d, repo, asset, files := newAssetRepoFixture(t, media.AssetReady)

	require.NoError(t, repo.InsertGraph(t.Context(), asset, files, nil))
	requireAssetGraphCounts(t, d, 1, 1, 0)
}

func TestAssetRepoInsertGraphRollsBack(t *testing.T) {
	t.Run("missing relationship target", func(t *testing.T) {
		d, repo, asset, files := newAssetRepoFixture(t, media.AssetPending)
		relationships := []media.FileRelationship{{
			SourceFileID: files[0].ID,
			TargetFileID: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
			Kind:         media.PairedWith,
		}}

		require.Error(t, repo.InsertGraph(t.Context(), asset, files, relationships))
		requireAssetGraphCounts(t, d, 0, 0, 0)
	})

	tests := map[string]func(*media.File){
		"zero node ID": func(file *media.File) {
			zero := int64(0)
			file.DocbankNodeID = &zero
		},
		"malformed version UUID": func(file *media.File) {
			file.CurrentVersionID = "not-a-version"
		},
		"malformed SHA-256": func(file *media.File) {
			file.SHA256 = "not-a-digest"
		},
		"wrong owner storage key in path": func(file *media.File) {
			file.DocbankVirtualPath = "/owners/ffffffff-ffff-4fff-8fff-ffffffffffff/media/" +
				file.ID + "/" + file.OriginalFilename
		},
		"wrong file ID in path": func(file *media.File) {
			file.DocbankVirtualPath = "/owners/550e8400-e29b-41d4-a716-446655440000/media/" +
				"ffffffff-ffff-4fff-8fff-ffffffffffff/" + file.OriginalFilename
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			d, repo, asset, files := newAssetRepoFixture(t, media.AssetReady)
			mutate(&files[0])

			err := repo.InsertGraph(t.Context(), asset, files, nil)
			require.ErrorIs(t, err, errs.ErrInvalidArgument)
			requireAssetGraphCounts(t, d, 0, 0, 0)
		})
	}
}

func TestAssetRepoInsertGraphPending(t *testing.T) {
	d, repo, asset, files := newAssetRepoFixture(t, media.AssetPending)
	files = append(files,
		media.File{
			ID:               "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
			AssetID:          asset.ID,
			Owner:            asset.Owner,
			Role:             media.RoleOriginal,
			MimeType:         "image/x-raw",
			OriginalFilename: "photo.raw",
			Size:             100,
		},
		media.File{
			ID:               "ffffffff-ffff-4fff-8fff-ffffffffffff",
			AssetID:          asset.ID,
			Owner:            asset.Owner,
			Role:             media.RoleSidecar,
			MimeType:         "application/rdf+xml",
			OriginalFilename: "photo.xmp",
			Size:             10,
		},
	)
	relationships := []media.FileRelationship{
		{SourceFileID: files[1].ID, TargetFileID: files[0].ID, Kind: media.PairedWith},
		{SourceFileID: files[2].ID, TargetFileID: files[1].ID, Kind: media.SidecarOf},
	}

	require.NoError(t, repo.InsertGraph(t.Context(), asset, files, relationships))
	requireAssetGraphCounts(t, d, 1, 3, 2)

	var state string
	require.NoError(t, d.ReadDB().QueryRow(`SELECT state FROM assets WHERE id = ?`, asset.ID).Scan(&state))
	require.Equal(t, string(media.AssetPending), state)
}

func TestAssetRepoGetAsset(t *testing.T) {
	r := require.New(t)
	_, repo, asset, files := newAssetRepoFixture(t, media.AssetReady)
	asset.Timestamp = new(time.Date(2026, 8, 25, 10, 30, 0, 0, time.UTC))
	asset.Make = "Canon"
	asset.Model = "R5"
	asset.LensModel = "RF 50mm"
	asset.FocalLength = "50mm"
	asset.Shutter = "1/250"
	asset.Width = new(8192)
	asset.Height = new(5464)
	asset.ISO = new(200)
	asset.Aperture = new(2.8)
	asset.DurationMs = new(int64(1234))
	asset.Latitude = new(41.8781)
	asset.Longitude = new(-87.6298)
	asset.GPSAt = new(time.Date(2026, 8, 25, 10, 30, 0, 0, time.UTC))
	asset.LocationLabel = "Chicago"
	asset.ThumbStatus = "ready"
	asset.ThumbVersion = 3
	asset.ThumbUpdatedAt = new(time.Date(2026, 8, 26, 14, 0, 0, 0, time.UTC))
	asset.HiddenAt = new(time.Date(2026, 8, 26, 15, 0, 0, 0, time.UTC))
	r.NoError(repo.InsertGraph(t.Context(), asset, files, nil))

	got, err := repo.GetAsset(t.Context(), asset.ID)
	r.NoError(err)
	r.Equal(asset, got)

	_, err = repo.GetAsset(t.Context(), "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee")
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestAssetRepoFileReads(t *testing.T) {
	r := require.New(t)
	_, repo, asset, files := newAssetRepoFixture(t, media.AssetPending)
	alternate := media.File{
		ID:               "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
		AssetID:          asset.ID,
		Owner:            asset.Owner,
		Role:             media.RoleAlternate,
		MimeType:         "image/dng",
		OriginalFilename: "photo.dng",
		ImportSourcePath: "camera/photo.dng",
		Size:             84,
	}
	sidecar := media.File{
		ID:               "ffffffff-ffff-4fff-8fff-ffffffffffff",
		AssetID:          asset.ID,
		Owner:            asset.Owner,
		Role:             media.RoleSidecar,
		MimeType:         "application/rdf+xml",
		OriginalFilename: "photo.xmp",
		ImportSourcePath: "camera/photo.xmp",
		Size:             12,
	}
	files = append(files, sidecar, alternate)
	r.NoError(repo.InsertGraph(t.Context(), asset, files, nil))

	gotFiles, err := repo.ListFiles(t.Context(), asset.ID)
	r.NoError(err)
	r.Equal([]media.File{alternate, files[0], sidecar}, gotFiles)

	gotFile, err := repo.GetFile(t.Context(), files[0].ID)
	r.NoError(err)
	r.Equal(files[0], gotFile)

	primary, err := repo.GetPrimaryFile(t.Context(), asset.ID)
	r.NoError(err)
	r.Equal(files[0], primary)

	missingID := "99999999-9999-4999-8999-999999999999"
	_, err = repo.ListFiles(t.Context(), missingID)
	r.ErrorIs(err, errs.ErrNotFound)
	_, err = repo.GetFile(t.Context(), missingID)
	r.ErrorIs(err, errs.ErrNotFound)

	_, noPrimaryRepo, noPrimaryAsset, noPrimaryFiles := newAssetRepoFixture(t, media.AssetPending)
	noPrimaryFiles[0].Role = media.RoleOriginal
	r.NoError(noPrimaryRepo.InsertGraph(t.Context(), noPrimaryAsset, noPrimaryFiles, nil))
	_, err = noPrimaryRepo.GetPrimaryFile(t.Context(), noPrimaryAsset.ID)
	r.ErrorIs(err, errs.ErrNotFound)
}

func newAssetRepoFixture(
	t *testing.T,
	state media.AssetState,
) (*db.DB, *media.AssetRepo, media.Asset, []media.File) {
	t.Helper()
	d := testutil.OpenTestDB(t)
	owner := owners.Owner{
		Principal:  owners.Principal{Hub: "hub", UserID: "owner"},
		StorageKey: "550e8400-e29b-41d4-a716-446655440000",
		CreatedAt:  time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC),
	}
	require.NoError(t, owners.NewRepo(d.WriteDB(), d.ReadDB()).Insert(t.Context(), owner))

	asset := media.Asset{
		ID:          "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		Owner:       owner.Principal,
		State:       state,
		Type:        media.TypePhoto,
		ImportedAt:  time.Date(2026, 8, 26, 13, 0, 0, 0, time.UTC),
		ThumbStatus: "pending",
	}
	nodeID := int64(1)
	files := []media.File{{
		ID:                 "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		AssetID:            asset.ID,
		Owner:              asset.Owner,
		Role:               media.RolePrimary,
		MimeType:           "image/jpeg",
		OriginalFilename:   "photo.jpg",
		Size:               42,
		DocbankNodeID:      &nodeID,
		DocbankVirtualPath: "/owners/550e8400-e29b-41d4-a716-446655440000/media/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/photo.jpg",
		CurrentVersionID:   "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		SHA256:             "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}}
	return d, media.NewAssetRepo(d.WriteDB(), d.ReadDB()), asset, files
}

func requireAssetGraphCounts(t *testing.T, d *db.DB, assets, files, relationships int) {
	t.Helper()
	var got int
	require.NoError(t, d.ReadDB().QueryRow(`SELECT COUNT(*) FROM assets`).Scan(&got))
	require.Equal(t, assets, got, "assets")
	require.NoError(t, d.ReadDB().QueryRow(`SELECT COUNT(*) FROM media_files`).Scan(&got))
	require.Equal(t, files, got, "media_files")
	require.NoError(t, d.ReadDB().QueryRow(`SELECT COUNT(*) FROM media_file_relationships`).Scan(&got))
	require.Equal(t, relationships, got, "media_file_relationships")
}
