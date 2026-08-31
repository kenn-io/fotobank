package service_test

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service"
	"go.kenn.io/fotobank/internal/storage"
	"go.kenn.io/fotobank/internal/testutil"
	"go.kenn.io/fotobank/internal/testutil/assetfixture"
	"go.kenn.io/fotobank/internal/thumb"
)

// thumbServiceFixture bundles the collaborators the ThumbService tests
// need: the service under test, the seeded owner, the underlying store,
// queue, and repo for arranging state, the writable DB for seeding a
// second owner, and the on-disk NAS root.
type thumbServiceFixture struct {
	svc   *service.ThumbService
	owner owners.Principal
	store *storage.NASOnly
	repo  *media.Repo
	queue *thumb.Queue
	rw    *sql.DB
	root  string
}

func newThumbServiceFixture(t *testing.T) thumbServiceFixture {
	t.Helper()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	q := thumb.NewQueue(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC(),
	)
	require.NoError(t, err)
	root := filepath.Join(t.TempDir(), "nas")
	require.NoError(t, os.Mkdir(root, 0o700))
	store := storage.NewNASOnly(root, map[owners.Principal]string{p: "550e8400-e29b-41d4-a716-446655440000"})
	svc := service.NewThumbService(repo, q, store)
	return thumbServiceFixture{
		svc: svc, owner: p, store: store, repo: repo, queue: q,
		rw: d.WriteDB(), root: root,
	}
}

// insertThumbMedia inserts a media row with the given thumb status and
// version. Checksum/path/id are all set to the returned uuid so rows
// are uniquely identified without extra ceremony.
func insertThumbMedia(
	t *testing.T, repo *media.Repo, p owners.Principal,
	status string, version int,
) media.Media {
	t.Helper()
	id := uuid.NewString()
	m := media.Media{
		ID:           id,
		Owner:        p,
		Type:         media.TypePhoto,
		MimeType:     "image/jpeg",
		ImportedAt:   time.Now().UTC(),
		ThumbStatus:  status,
		ThumbVersion: version,
	}
	return assetfixture.Insert(t, repo, m)
}

func TestThumbServiceGetReturnsBytesForOwnedReadyRow(t *testing.T) {
	r := require.New(t)
	fx := newThumbServiceFixture(t)
	ctx := context.Background()

	m := insertThumbMedia(t, fx.repo, fx.owner, "ready", 3)
	key := thumb.ThumbKey(m.ID, 3, thumb.SizeGrid)
	_, err := fx.store.Write(ctx, fx.owner, key, bytes.NewReader([]byte("thumb bytes")))
	r.NoError(err)

	rc, got, err := fx.svc.Get(ctx, m.ID, thumb.SizeGrid, 3, fx.owner)
	r.NoError(err)
	defer func() { _ = rc.Close() }()
	bs, err := io.ReadAll(rc)
	r.NoError(err)
	r.Equal([]byte("thumb bytes"), bs)
	r.Equal(m.ID, got.ID)
	r.Equal(3, got.ThumbVersion)
}

func TestThumbServiceGetReturnsNotFoundOnVersionMismatch(t *testing.T) {
	r := require.New(t)
	fx := newThumbServiceFixture(t)
	ctx := context.Background()

	m := insertThumbMedia(t, fx.repo, fx.owner, "ready", 5)
	// Write the v5 thumb so the only reason Get fails is the version
	// mismatch, not a missing blob.
	key := thumb.ThumbKey(m.ID, 5, thumb.SizeGrid)
	_, err := fx.store.Write(ctx, fx.owner, key, bytes.NewReader([]byte("v5")))
	r.NoError(err)

	rc, _, err := fx.svc.Get(ctx, m.ID, thumb.SizeGrid, 4, fx.owner)
	r.Nil(rc)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestThumbServiceGetReturnsNotFoundWhenBlobMissing(t *testing.T) {
	r := require.New(t)
	fx := newThumbServiceFixture(t)
	ctx := context.Background()

	// Seed a ready row at version=3 but do NOT write bytes at the
	// versioned key — simulates the regenerate-vs-read race where the
	// old blob was deleted before the reader resolved the row. The
	// store surfaces os.ErrNotExist; Get must translate that to
	// ErrNotFound so T10 returns 404, not 500.
	m := insertThumbMedia(t, fx.repo, fx.owner, "ready", 3)

	rc, _, err := fx.svc.Get(ctx, m.ID, thumb.SizeGrid, 3, fx.owner)
	r.Nil(rc)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestThumbServiceGetReturnsNotFoundWhenNotReady(t *testing.T) {
	r := require.New(t)
	fx := newThumbServiceFixture(t)
	ctx := context.Background()

	m := insertThumbMedia(t, fx.repo, fx.owner, "pending", 0)

	rc, _, err := fx.svc.Get(ctx, m.ID, thumb.SizeGrid, 0, fx.owner)
	r.Nil(rc)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestThumbServiceGetReturnsNotFoundForOtherOwner(t *testing.T) {
	r := require.New(t)
	fx := newThumbServiceFixture(t)
	ctx := context.Background()

	// Seed a second owner B and register a ready row for them.
	ownerB := owners.Principal{Hub: "h", UserID: "b"}
	_, err := fx.rw.ExecContext(ctx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		ownerB.Hub, ownerB.UserID, "550e8400-e29b-41d4-a716-446655440002", time.Now().UTC(),
	)
	r.NoError(err)
	m := insertThumbMedia(t, fx.repo, ownerB, "ready", 1)

	// Caller A asks for B's row: ErrNotFound, not ErrPermissionDenied,
	// so the handler surface cannot distinguish "exists but forbidden"
	// from "does not exist".
	rc, _, err := fx.svc.Get(ctx, m.ID, thumb.SizeGrid, 1, fx.owner)
	r.Nil(rc)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestThumbServiceGetReturnsNotFoundForHiddenRowWithoutFlag(t *testing.T) {
	r := require.New(t)
	fx := newThumbServiceFixture(t)
	ctx := context.Background()

	m := insertThumbMedia(t, fx.repo, fx.owner, "ready", 3)
	key := thumb.ThumbKey(m.ID, 3, thumb.SizeGrid)
	_, err := fx.store.Write(ctx, fx.owner, key, bytes.NewReader([]byte("thumb")))
	r.NoError(err)
	// Mark hidden directly.
	_, err = fx.rw.ExecContext(ctx,
		`UPDATE assets SET hidden_at = ? WHERE id = ?`,
		time.Now().UTC(), m.ID,
	)
	r.NoError(err)

	rc, _, err := fx.svc.Get(ctx, m.ID, thumb.SizeGrid, 3, fx.owner)
	r.Nil(rc)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestThumbServiceGetSucceedsForHiddenRowWithFlag(t *testing.T) {
	r := require.New(t)
	fx := newThumbServiceFixture(t)
	ctx := context.Background()

	m := insertThumbMedia(t, fx.repo, fx.owner, "ready", 3)
	key := thumb.ThumbKey(m.ID, 3, thumb.SizeGrid)
	payload := []byte("thumb bytes hidden")
	_, err := fx.store.Write(ctx, fx.owner, key, bytes.NewReader(payload))
	r.NoError(err)
	// Mark hidden.
	_, err = fx.rw.ExecContext(ctx,
		`UPDATE assets SET hidden_at = ? WHERE id = ?`,
		time.Now().UTC(), m.ID,
	)
	r.NoError(err)

	rc, got, err := fx.svc.Get(ctx, m.ID, thumb.SizeGrid, 3, fx.owner, true)
	r.NoError(err)
	defer func() { _ = rc.Close() }()
	bs, err := io.ReadAll(rc)
	r.NoError(err)
	r.Equal(payload, bs)
	r.Equal(m.ID, got.ID)
}

func TestThumbServiceEnqueueScopesToOwner(t *testing.T) {
	r := require.New(t)
	fx := newThumbServiceFixture(t)
	ctx := context.Background()

	// Seed owner A with two rows and owner B with one. If the service
	// forgot to clamp filter.Owner back to the caller, Enqueue would
	// touch B's single row (n=1); when the clamp is in place it touches
	// A's two rows (n=2). The row-count asymmetry is what makes this
	// test regression-tight.
	ownerB := owners.Principal{Hub: "h", UserID: "b"}
	_, err := fx.rw.ExecContext(ctx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		ownerB.Hub, ownerB.UserID, "550e8400-e29b-41d4-a716-446655440002", time.Now().UTC(),
	)
	r.NoError(err)
	mA1 := insertThumbMedia(t, fx.repo, fx.owner, "ready", 0)
	mA2 := insertThumbMedia(t, fx.repo, fx.owner, "ready", 0)
	mB := insertThumbMedia(t, fx.repo, ownerB, "ready", 0)

	// Caller A calls Enqueue with Owner=B set on the filter — the
	// service must clamp filter.Owner back to the caller so only A's
	// two rows are affected.
	n, err := fx.svc.Enqueue(ctx, fx.owner,
		thumb.EnqueueFilter{All: true, Owner: ownerB})
	r.NoError(err)
	r.Equal(2, n)

	// Belt-and-suspenders: A's rows bumped to version=1, B's row
	// untouched at version=0.
	gotA1, err := fx.repo.GetByID(ctx, mA1.ID)
	r.NoError(err)
	r.Equal(1, gotA1.ThumbVersion)
	gotA2, err := fx.repo.GetByID(ctx, mA2.ID)
	r.NoError(err)
	r.Equal(1, gotA2.ThumbVersion)
	gotB, err := fx.repo.GetByID(ctx, mB.ID)
	r.NoError(err)
	r.Equal(0, gotB.ThumbVersion)
}
