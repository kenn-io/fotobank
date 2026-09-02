package contentresolver_test

import (
	"database/sql"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/contentresolver"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil"
	"go.kenn.io/fotobank/internal/testutil/assetfixture"
)

func TestResolverOpensCurrentAndExactFileVersions(t *testing.T) {
	require := require.New(t)
	resolver, repo, store, owner := newResolverFixture(t)
	body := []byte("exact version bytes")
	item := assetfixture.InsertContent(t, repo, store, body, media.Media{Owner: owner})

	current, err := resolver.ResolveCurrent(t.Context(), item.ID, "")
	require.NoError(err)
	require.Equal(item.PrimaryFileID, current.File.ID)
	require.Equal(item.CurrentVersionID, current.VersionID)

	opened, err := resolver.Open(t.Context(), current, 0, -1)
	require.NoError(err)
	got, err := io.ReadAll(opened.Reader)
	require.NoError(err)
	require.NoError(opened.Reader.Close())
	require.Equal(body, got)
	require.Equal(item.SHA256, opened.SHA256)

	exact, err := resolver.ResolveVersion(t.Context(), item.ID, item.PrimaryFileID, item.CurrentVersionID)
	require.NoError(err)
	ranged, err := resolver.Open(t.Context(), exact, 6, 7)
	require.NoError(err)
	got, err = io.ReadAll(ranged.Reader)
	require.NoError(err)
	require.NoError(ranged.Reader.Close())
	require.Equal([]byte("version"), got)
}

func TestResolverRejectsVersionsAndFilesFromAnotherAsset(t *testing.T) {
	resolver, repo, store, owner := newResolverFixture(t)
	first := assetfixture.InsertContent(t, repo, store, []byte("first"), media.Media{Owner: owner})
	second := assetfixture.InsertContent(t, repo, store, []byte("second"), media.Media{Owner: owner})

	_, err := resolver.ResolveCurrent(t.Context(), first.ID, second.PrimaryFileID)
	require.ErrorIs(t, err, errs.ErrNotFound)

	ref, err := resolver.ResolveVersion(t.Context(), first.ID, first.PrimaryFileID, second.CurrentVersionID)
	require.NoError(t, err)
	_, err = resolver.Open(t.Context(), ref, 0, -1)
	require.ErrorIs(t, err, errs.ErrNotFound)
}

func TestResolverRejectsStaleCurrentIdentityProjection(t *testing.T) {
	r := require.New(t)
	resolver, repo, store, owner := newResolverFixture(t)
	item := assetfixture.InsertContent(t, repo, store, []byte("authority"), media.Media{Owner: owner})

	err := repo.WithWriteTx(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(),
			`UPDATE media_files SET sha256 = ? WHERE id = ?`,
			"0000000000000000000000000000000000000000000000000000000000000000",
			item.PrimaryFileID,
		)
		return err
	})
	r.NoError(err)
	ref, err := resolver.ResolveCurrent(t.Context(), item.ID, "")
	r.NoError(err)
	_, err = resolver.Open(t.Context(), ref, 0, -1)
	r.ErrorIs(err, errs.ErrContentIdentityMismatch)
	_, err = resolver.ValidateCurrent(t.Context(), item.ID, "")
	r.ErrorIs(err, errs.ErrContentIdentityMismatch)
}

func newResolverFixture(
	t *testing.T,
) (*contentresolver.Resolver, *media.Repo, *content.Adapter, owners.Principal) {
	t.Helper()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	store, err := content.Open(t.Context(), content.Config{Root: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return contentresolver.New(repo, store), repo, store, owner
}
