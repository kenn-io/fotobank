package thumb_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/testutil"
	"github.com/wesm/fotobank/internal/thumb"
)

// queueFixture seeds an owner + N pending rows and returns them plus
// the opened Queue.
type queueFixture struct {
	q     *thumb.Queue
	owner owners.Principal
	ids   []string
}

func newQueueFixture(t *testing.T, nRows int) queueFixture {
	t.Helper()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	q := thumb.NewQueue(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk", time.Now().UTC(),
	)
	require.NoError(t, err)
	ids := make([]string, nRows)
	for i := range nRows {
		m := media.Media{
			ID:               uuid.NewString(),
			Owner:            p,
			Type:             media.TypePhoto,
			MimeType:         "image/jpeg",
			Path:             "2024/a.jpg",
			OriginalFilename: "a.jpg",
			ImportedAt:       time.Now().UTC().Add(time.Duration(i) * time.Second),
			Size:             100,
			Checksum:         uuid.NewString(),
			ThumbStatus:      "pending",
		}
		m.Path = "2024/" + m.ID + ".jpg"
		require.NoError(t, repo.Insert(context.Background(), m))
		ids[i] = m.ID
	}
	return queueFixture{q: q, owner: p, ids: ids}
}

func TestClaimBatchReturnsRowsAndMarksWorking(t *testing.T) {
	r := require.New(t)
	fx := newQueueFixture(t, 3)
	claims, err := fx.q.ClaimBatch(context.Background(), 2)
	r.NoError(err)
	r.Len(claims, 2)
	for _, c := range claims {
		r.NotZero(c.ClaimedAt)
		r.Equal(fx.owner, c.Media.Owner)
	}
}

func TestClaimBatchIsAtomicAcrossCallers(t *testing.T) {
	r := require.New(t)
	fx := newQueueFixture(t, 3)

	a, err := fx.q.ClaimBatch(context.Background(), 10)
	r.NoError(err)
	b, err := fx.q.ClaimBatch(context.Background(), 10)
	r.NoError(err)
	r.Equal(3, len(a)+len(b))

	seen := map[string]bool{}
	for _, c := range append(a, b...) {
		r.False(seen[c.Media.ID], "row %s claimed twice", c.Media.ID)
		seen[c.Media.ID] = true
	}
}

func TestMarkReadySucceedsWithMatchingToken(t *testing.T) {
	r := require.New(t)
	fx := newQueueFixture(t, 1)
	claims, err := fx.q.ClaimBatch(context.Background(), 1)
	r.NoError(err)
	r.Len(claims, 1)

	c := claims[0]
	r.NoError(fx.q.MarkReady(context.Background(), c.Media.ID, c.Media.ThumbVersion, c.ClaimedAt))
}

func TestMarkReadyReturnsErrClaimLostOnStaleToken(t *testing.T) {
	r := require.New(t)
	fx := newQueueFixture(t, 1)
	claims, err := fx.q.ClaimBatch(context.Background(), 1)
	r.NoError(err)
	c := claims[0]

	stale := c.ClaimedAt.Add(-time.Hour)
	err = fx.q.MarkReady(context.Background(), c.Media.ID, c.Media.ThumbVersion, stale)
	r.ErrorIs(err, thumb.ErrClaimLost)
}

func TestSweepLeasesBumpsVersionAndResetsClaim(t *testing.T) {
	r := require.New(t)
	fx := newQueueFixture(t, 1)
	claims, err := fx.q.ClaimBatch(context.Background(), 1)
	r.NoError(err)
	c := claims[0]

	n, err := fx.q.SweepLeases(context.Background(), 0)
	r.NoError(err)
	r.Equal(1, n)

	err = fx.q.MarkReady(context.Background(), c.Media.ID, c.Media.ThumbVersion, c.ClaimedAt)
	r.ErrorIs(err, thumb.ErrClaimLost)

	next, err := fx.q.ClaimBatch(context.Background(), 1)
	r.NoError(err)
	r.Len(next, 1)
	r.Equal(c.Media.ThumbVersion+1, next[0].Media.ThumbVersion)
}

func TestEnqueueBumpsVersionForMatchingRows(t *testing.T) {
	r := require.New(t)
	fx := newQueueFixture(t, 2)

	n, err := fx.q.Enqueue(context.Background(), thumb.EnqueueFilter{All: true, Owner: fx.owner})
	r.NoError(err)
	r.Equal(2, n)

	claims, err := fx.q.ClaimBatch(context.Background(), 2)
	r.NoError(err)
	r.Len(claims, 2)
	for _, c := range claims {
		r.Equal(1, c.Media.ThumbVersion)
	}
}

func TestRegenerateWhileWorkingLosesClaim(t *testing.T) {
	r := require.New(t)
	fx := newQueueFixture(t, 1)
	claims, err := fx.q.ClaimBatch(context.Background(), 1)
	r.NoError(err)
	c := claims[0]

	_, err = fx.q.Enqueue(context.Background(),
		thumb.EnqueueFilter{IDs: []string{c.Media.ID}, Owner: fx.owner})
	r.NoError(err)

	err = fx.q.MarkReady(context.Background(), c.Media.ID, c.Media.ThumbVersion, c.ClaimedAt)
	r.ErrorIs(err, thumb.ErrClaimLost)
}
