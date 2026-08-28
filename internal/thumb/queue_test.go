package thumb_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil"
	"go.kenn.io/fotobank/internal/testutil/assetfixture"
	"go.kenn.io/fotobank/internal/thumb"
)

// queueFixture seeds an owner + N pending rows and returns them plus
// the opened Queue.
type queueFixture struct {
	q     *thumb.Queue
	rw    *sql.DB
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
		p.Hub, p.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC(),
	)
	require.NoError(t, err)
	ids := make([]string, nRows)
	for i := range nRows {
		m := media.Media{
			ID:                 uuid.NewString(),
			Owner:              p,
			Type:               media.TypePhoto,
			MimeType:           "image/jpeg",
			DocbankVirtualPath: "2024/a.jpg",
			OriginalFilename:   "a.jpg",
			ImportedAt:         time.Now().UTC().Add(time.Duration(i) * time.Second),
			Size:               100,
			SHA256:             uuid.NewString(),
			ThumbStatus:        "pending",
		}
		m.DocbankVirtualPath = "2024/" + m.ID + ".jpg"
		assetfixture.Insert(t, repo, m)
		ids[i] = m.ID
	}
	return queueFixture{q: q, rw: d.WriteDB(), owner: p, ids: ids}
}

func claimOne(t *testing.T, q *thumb.Queue) thumb.Claim {
	t.Helper()
	claims, err := q.ClaimBatch(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	return claims[0]
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

func TestClaimBatchPartitionsRowsUnderConcurrency(t *testing.T) {
	r := require.New(t)
	const (
		goroutines = 4
		totalRows  = 3
		claimSize  = 10
	)
	fx := newQueueFixture(t, totalRows)

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		seen   = map[string]int{}
		dups   []string
		errs   []error
		counts = make([]int, goroutines)
	)
	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			claims, err := fx.q.ClaimBatch(context.Background(), claimSize)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			counts[idx] = len(claims)
			for _, c := range claims {
				if _, ok := seen[c.Media.ID]; ok {
					dups = append(dups, c.Media.ID)
				}
				seen[c.Media.ID] = idx
			}
		}(i)
	}
	wg.Wait()

	r.Empty(errs, "no goroutine should error")
	r.Empty(dups, "rows claimed by more than one goroutine")
	total := 0
	for _, c := range counts {
		total += c
	}
	r.Equal(totalRows, total, "sum of claims must equal total pending rows")
	r.Len(seen, totalRows, "every pending row must be claimed by exactly one goroutine")
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
	c := claimOne(t, fx.q)

	stale := c.ClaimedAt.Add(-time.Hour)
	err := fx.q.MarkReady(context.Background(), c.Media.ID, c.Media.ThumbVersion, stale)
	r.ErrorIs(err, thumb.ErrClaimLost)
}

func TestSweepLeasesBumpsVersionAndResetsClaim(t *testing.T) {
	r := require.New(t)
	fx := newQueueFixture(t, 1)
	c := claimOne(t, fx.q)

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

func TestEnqueueSkipsHiddenAndNonReadyAssets(t *testing.T) {
	r := require.New(t)
	fx := newQueueFixture(t, 3)
	_, err := fx.rw.ExecContext(t.Context(),
		`UPDATE assets SET hidden_at = ? WHERE id = ?`, time.Now().UTC(), fx.ids[1])
	r.NoError(err)
	_, err = fx.rw.ExecContext(t.Context(),
		`UPDATE assets SET state = 'pending' WHERE id = ?`, fx.ids[2])
	r.NoError(err)

	n, err := fx.q.Enqueue(t.Context(), thumb.EnqueueFilter{All: true, Owner: fx.owner})
	r.NoError(err)
	r.Equal(1, n)

	for i, id := range fx.ids {
		var version int
		r.NoError(fx.rw.QueryRowContext(t.Context(),
			`SELECT thumb_version FROM assets WHERE id = ?`, id).Scan(&version))
		if i == 0 {
			r.Equal(1, version)
		} else {
			r.Zero(version)
		}
	}
}

func TestRegenerateWhileWorkingLosesClaim(t *testing.T) {
	r := require.New(t)
	fx := newQueueFixture(t, 1)
	c := claimOne(t, fx.q)

	_, err := fx.q.Enqueue(context.Background(),
		thumb.EnqueueFilter{IDs: []string{c.Media.ID}, Owner: fx.owner})
	r.NoError(err)

	err = fx.q.MarkReady(context.Background(), c.Media.ID, c.Media.ThumbVersion, c.ClaimedAt)
	r.ErrorIs(err, thumb.ErrClaimLost)

	next, err := fx.q.ClaimBatch(context.Background(), 1)
	r.NoError(err)
	r.Len(next, 1)
	r.Equal(c.Media.ThumbVersion+1, next[0].Media.ThumbVersion,
		"Enqueue must bump thumb_version, not merely reset status")
}

func TestMarkNoPreviewSucceedsAndSetsStatus(t *testing.T) {
	r := require.New(t)
	fx := newQueueFixture(t, 1)
	c := claimOne(t, fx.q)

	r.NoError(fx.q.MarkNoPreview(
		context.Background(), c.Media.ID, c.Media.ThumbVersion, c.ClaimedAt))
	r.Equal("no_preview", readThumbStatus(t, fx.rw, c.Media.ID))
}

func TestMarkFailedSucceedsAndSetsStatus(t *testing.T) {
	r := require.New(t)
	fx := newQueueFixture(t, 1)
	c := claimOne(t, fx.q)

	r.NoError(fx.q.MarkFailed(
		context.Background(), c.Media.ID, c.Media.ThumbVersion, c.ClaimedAt, nil))
	r.Equal("failed", readThumbStatus(t, fx.rw, c.Media.ID))
}

func readThumbStatus(t *testing.T, rw *sql.DB, id string) string {
	t.Helper()
	var status string
	err := rw.QueryRowContext(context.Background(),
		`SELECT thumb_status FROM assets WHERE id = ?`, id).Scan(&status)
	require.NoError(t, err)
	return status
}

// TestClaimBatchOrdersNewestFirst pins the user-facing contract that
// thumbnails drain in newest-first order. The library opens to most-
// recent photos by default, so a thumbnail backlog should hydrate the
// top of the grid first; the previous FIFO order by imported_at made
// post-import shoots the LAST thing the worker reached, manifesting
// as a "broken" library on first open.
//
// Test seeds three rows: one with timestamp = today, one with
// timestamp = a year ago, one with NULL timestamp (no EXIF date).
// Worker claims must come back as today, year-ago, no-EXIF — i.e.
// timestamp DESC first, with imported_at as the fallback sort key.
func TestClaimBatchOrdersNewestFirst(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	q := thumb.NewQueue(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC(),
	)
	r.NoError(err)

	// Insert in a deliberately-shuffled order so a regression to
	// "FIFO by insert" or "FIFO by imported_at" produces a different
	// claim sequence than the contract demands.
	old := time.Now().UTC().Add(-365 * 24 * time.Hour)
	new := time.Now().UTC()
	noExif := time.Now().UTC().Add(-2 * time.Hour) // imported_at fallback
	rows := []struct {
		id        string
		timestamp *time.Time
		imported  time.Time
	}{
		{id: "a-old", timestamp: &old, imported: time.Now().UTC().Add(-3 * time.Hour)},
		{id: "b-no-exif", timestamp: nil, imported: noExif},
		{id: "z-new", timestamp: &new, imported: time.Now().UTC().Add(-1 * time.Hour)},
	}
	for _, row := range rows {
		m := media.Media{
			ID: row.id, Owner: p,
			Type: media.TypePhoto, MimeType: "image/jpeg",
			DocbankVirtualPath: "2024/" + row.id + ".jpg",
			OriginalFilename:   row.id + ".jpg",
			ImportedAt:         row.imported,
			Timestamp:          row.timestamp,
			Size:               100, SHA256: row.id,
			ThumbStatus: "pending",
		}
		assetfixture.Insert(t, repo, m)
	}

	claims, err := q.ClaimBatch(context.Background(), 3)
	r.NoError(err)
	r.Len(claims, 3)
	got := []string{claims[0].Media.ID, claims[1].Media.ID, claims[2].Media.ID}
	// COALESCE(timestamp, imported_at) DESC:
	//   new      → timestamp = now           (rank 1)
	//   no-exif  → imported_at = now - 2h    (rank 2)
	//   old      → timestamp = now - 365d    (rank 3)
	// Note "no-exif" outranks "old" because its imported_at is more
	// recent than old's EXIF timestamp — newness the user perceives
	// (when the row entered the system) matches what the worker
	// drains, regardless of whether EXIF dates are present.
	r.Equal([]string{"z-new", "b-no-exif", "a-old"}, got,
		"claim order must be newest-first by COALESCE(timestamp, imported_at) DESC")
}
