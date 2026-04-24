package shareworker_test

import (
	"context"
	"database/sql"
	"math/rand"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/broker"
	"github.com/wesm/fotobank/internal/broker/brokertest"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/share"
	"github.com/wesm/fotobank/internal/shareworker"
	"github.com/wesm/fotobank/internal/testutil"
)

type workerFixture struct {
	repo  *share.Repo
	fake  *brokertest.Fake
	w     *shareworker.Worker
	owner owners.Principal
	db    *sql.DB
	now   time.Time
}

func newWorkerFixture(t *testing.T) *workerFixture {
	t.Helper()
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "o"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "sk", time.Now().UTC())
	require.NoError(t, err)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	fake := &brokertest.Fake{}
	now := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)
	w := shareworker.New(shareworker.Config{
		Repo:   repo,
		Broker: fake,
		Now:    func() time.Time { return now },
		Rand:   rand.New(rand.NewSource(1)),
	})
	return &workerFixture{repo: repo, fake: fake, w: w, owner: owner, db: d.WriteDB(), now: now}
}

func (fx *workerFixture) insertPending(t *testing.T) string {
	t.Helper()
	id := uuid.NewString()
	_, err := fx.db.ExecContext(context.Background(),
		`INSERT INTO albums(id, owner_hub, owner_user_id, name, created_at, updated_at)
         VALUES(?,?,?,?,?,?)`,
		id, fx.owner.Hub, fx.owner.UserID, "t", fx.now, fx.now)
	require.NoError(t, err)
	s := share.Scope{
		UUID: uuid.NewString(), Owner: fx.owner,
		Grantee:       owners.Principal{Hub: "h", UserID: "g"},
		TargetType:    share.TargetAlbumLive,
		TargetAlbumID: &id,
		CreatedAt:     fx.now,
		BrokerStatus:  share.StatusPending,
	}
	require.NoError(t, fx.repo.Insert(context.Background(), s, nil))
	return s.UUID
}

func TestWorkerPendingToActiveOnSuccess(t *testing.T) {
	r := require.New(t)
	fx := newWorkerFixture(t)
	id := fx.insertPending(t)

	processed, err := fx.w.RunOnce(context.Background())
	r.NoError(err)
	r.Equal(1, processed)

	got, err := fx.repo.GetByUUID(context.Background(), id)
	r.NoError(err)
	r.Equal(share.StatusActive, got.BrokerStatus)
	r.NotNil(got.BrokerGrantedAt)
	r.Equal([]string{id}, fx.fake.ObservedPublishes())
}

func TestWorkerPendingTransientFailSchedulesRetry(t *testing.T) {
	r := require.New(t)
	fx := newWorkerFixture(t)
	id := fx.insertPending(t)
	fx.fake.QueuePublishError(id, broker.ErrBrokerTransient)

	processed, err := fx.w.RunOnce(context.Background())
	r.NoError(err)
	r.Equal(1, processed)

	got, err := fx.repo.GetByUUID(context.Background(), id)
	r.NoError(err)
	r.Equal(share.StatusPending, got.BrokerStatus)
	r.Equal(1, got.BrokerAttempts)
	r.NotEmpty(got.BrokerLastError)
	r.NotNil(got.BrokerNextAttemptAt)
	r.True(got.BrokerNextAttemptAt.After(fx.now))
}

func TestWorkerPendingPermanentFailMarksFailed(t *testing.T) {
	r := require.New(t)
	fx := newWorkerFixture(t)
	id := fx.insertPending(t)
	fx.fake.QueuePublishError(id, broker.ErrBrokerPermanent)

	_, err := fx.w.RunOnce(context.Background())
	r.NoError(err)

	got, err := fx.repo.GetByUUID(context.Background(), id)
	r.NoError(err)
	r.Equal(share.StatusFailed, got.BrokerStatus)
	r.Nil(got.RevokedAt)
	r.Equal(1, got.BrokerAttempts)
}

func TestWorkerPendingTransientAtMaxAttemptsMarksFailed(t *testing.T) {
	r := require.New(t)
	fx := newWorkerFixture(t)
	id := fx.insertPending(t)

	// Simulate 9 prior failed attempts; this tick is the 10th.
	_, err := fx.db.ExecContext(context.Background(),
		`UPDATE scopes SET broker_attempts = ?, broker_next_attempt_at = NULL WHERE uuid = ?`,
		share.MaxBrokerAttempts-1, id)
	r.NoError(err)
	fx.fake.QueuePublishError(id, broker.ErrBrokerTransient)

	_, err = fx.w.RunOnce(context.Background())
	r.NoError(err)

	got, err := fx.repo.GetByUUID(context.Background(), id)
	r.NoError(err)
	r.Equal(share.StatusFailed, got.BrokerStatus)
	r.Equal(share.MaxBrokerAttempts, got.BrokerAttempts)
}

func TestWorkerRevokingToRevokedRemoteOnSuccess(t *testing.T) {
	r := require.New(t)
	fx := newWorkerFixture(t)
	id := fx.insertPending(t)
	_, err := fx.db.ExecContext(context.Background(),
		`UPDATE scopes SET broker_status='revoking', revoked_at=? WHERE uuid=?`,
		fx.now, id)
	r.NoError(err)

	_, err = fx.w.RunOnce(context.Background())
	r.NoError(err)

	got, err := fx.repo.GetByUUID(context.Background(), id)
	r.NoError(err)
	r.Equal(share.StatusRevokedRemote, got.BrokerStatus)
	r.NotNil(got.BrokerRevokedAt)
	r.Equal([]string{id}, fx.fake.ObservedRevokes())
}

func TestWorkerRevokingTransientFailSchedulesRetry(t *testing.T) {
	r := require.New(t)
	fx := newWorkerFixture(t)
	id := fx.insertPending(t)
	_, err := fx.db.ExecContext(context.Background(),
		`UPDATE scopes SET broker_status='revoking', revoked_at=? WHERE uuid=?`,
		fx.now, id)
	r.NoError(err)
	fx.fake.QueueRevokeError(id, broker.ErrBrokerTransient)

	_, err = fx.w.RunOnce(context.Background())
	r.NoError(err)

	got, err := fx.repo.GetByUUID(context.Background(), id)
	r.NoError(err)
	r.Equal(share.StatusRevoking, got.BrokerStatus) // still revoking
	r.Equal(1, got.BrokerAttempts)
	r.NotNil(got.BrokerNextAttemptAt)
}

func TestWorkerSkipsFutureNextAttempt(t *testing.T) {
	r := require.New(t)
	fx := newWorkerFixture(t)
	id := fx.insertPending(t)
	future := fx.now.Add(time.Hour)
	_, err := fx.db.ExecContext(context.Background(),
		`UPDATE scopes SET broker_next_attempt_at = ? WHERE uuid = ?`,
		future, id)
	r.NoError(err)

	processed, err := fx.w.RunOnce(context.Background())
	r.NoError(err)
	r.Equal(0, processed)
	r.Empty(fx.fake.ObservedPublishes())
}

func TestWorkerContextErrorFromBrokerAbortsDrain(t *testing.T) {
	r := require.New(t)
	fx := newWorkerFixture(t)
	// Seed two pending rows so we can verify the drain aborts after the first.
	id1 := fx.insertPending(t)
	id2 := fx.insertPending(t)
	fx.fake.QueuePublishError(id1, context.Canceled)

	n, err := fx.w.RunOnce(context.Background())
	r.ErrorIs(err, context.Canceled)
	r.Equal(0, n) // the cancelled row is not counted

	// id1's state is unchanged — still pending, no attempts recorded.
	got, err := fx.repo.GetByUUID(context.Background(), id1)
	r.NoError(err)
	r.Equal(share.StatusPending, got.BrokerStatus)
	r.Equal(0, got.BrokerAttempts)

	// id2 was never reached; still pending, no publish observed.
	got2, err := fx.repo.GetByUUID(context.Background(), id2)
	r.NoError(err)
	r.Equal(share.StatusPending, got2.BrokerStatus)
	r.NotContains(fx.fake.ObservedPublishes(), id2)
}

func TestWorkerRunExitsOnContextDeadline(t *testing.T) {
	r := require.New(t)
	fx := newWorkerFixture(t)
	// Override the tick to something tiny so the first drain runs and we
	// don't wait seconds for the test.
	w := shareworker.New(shareworker.Config{
		Repo:   fx.repo,
		Broker: fx.fake,
		Now:    func() time.Time { return fx.now },
		Rand:   rand.New(rand.NewSource(1)),
		Tick:   1 * time.Millisecond,
	})
	id := fx.insertPending(t)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := w.Run(ctx)
	r.ErrorIs(err, context.DeadlineExceeded)

	// The immediate drain should have processed the pending row.
	r.Contains(fx.fake.ObservedPublishes(), id)
	got, err := fx.repo.GetByUUID(context.Background(), id)
	r.NoError(err)
	r.Equal(share.StatusActive, got.BrokerStatus)
}
