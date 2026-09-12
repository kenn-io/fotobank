package shareworker_test

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/broker"
	"go.kenn.io/fotobank/internal/broker/brokertest"
	"go.kenn.io/fotobank/internal/obs"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/share"
	"go.kenn.io/fotobank/internal/shareworker"
	"go.kenn.io/fotobank/internal/testutil"
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
		owner.Hub, owner.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC())
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
	// Pin ListReady order: id1 must drain before id2 so the cancelled
	// broker call aborts the drain on the very first row. ListReady
	// orders by created_at ASC as its tiebreaker, so push id1 earlier.
	_, err := fx.db.ExecContext(context.Background(),
		`UPDATE scopes SET created_at = ? WHERE uuid = ?`,
		fx.now.Add(-time.Second), id1)
	r.NoError(err)
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
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	r.ErrorIs(fx.w.Run(ctx), context.DeadlineExceeded)
}

func TestWorkerRunDrainsBeforeFirstTick(t *testing.T) {
	r := require.New(t)
	fx := newWorkerFixture(t)
	w := shareworker.New(shareworker.Config{
		Repo:   fx.repo,
		Broker: fx.fake,
		Now:    func() time.Time { return fx.now },
		Rand:   rand.New(rand.NewSource(1)),
		Tick:   time.Hour,
	})
	id := fx.insertPending(t)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			r.ErrorIs(err, context.Canceled)
		case <-time.After(5 * time.Second):
			r.Fail("worker did not stop after cancellation")
		}
	})

	// Wait for the result, not an assumed database completion time. The
	// hour-long tick ensures this is the immediate drain rather than a retry.
	r.Eventually(func() bool {
		got, err := fx.repo.GetByUUID(ctx, id)
		return err == nil && got.BrokerStatus == share.StatusActive
	}, 5*time.Second, 10*time.Millisecond)
	r.Contains(fx.fake.ObservedPublishes(), id)
}

func TestWorkerEmitsPublishResultMetrics(t *testing.T) {
	r := require.New(t)

	// ok: clean publish, no scripted error.
	fxOK := newWorkerFixture(t)
	mOK := obs.NewTestMetrics()
	fxOK.w = shareworker.New(shareworker.Config{
		Repo:    fxOK.repo,
		Broker:  fxOK.fake,
		Now:     func() time.Time { return fxOK.now },
		Rand:    rand.New(rand.NewSource(1)),
		Metrics: mOK,
	})
	_ = fxOK.insertPending(t)
	_, err := fxOK.w.RunOnce(context.Background())
	r.NoError(err)
	r.EqualValues(1, mOK.SharePublishes("ok").Get())

	// retry: transient error scripted.
	fxRetry := newWorkerFixture(t)
	mRetry := obs.NewTestMetrics()
	fxRetry.w = shareworker.New(shareworker.Config{
		Repo:    fxRetry.repo,
		Broker:  fxRetry.fake,
		Now:     func() time.Time { return fxRetry.now },
		Rand:    rand.New(rand.NewSource(1)),
		Metrics: mRetry,
	})
	idR := fxRetry.insertPending(t)
	fxRetry.fake.QueuePublishError(idR, broker.ErrBrokerTransient)
	_, err = fxRetry.w.RunOnce(context.Background())
	r.NoError(err)
	r.EqualValues(1, mRetry.SharePublishes("retry").Get())
	r.EqualValues(0, mRetry.SharePublishes("terminal_fail").Get(),
		"transient under MaxBrokerAttempts must classify as retry, not terminal_fail")

	// terminal_fail (permanent): scripted permanent error.
	fxPerm := newWorkerFixture(t)
	mPerm := obs.NewTestMetrics()
	fxPerm.w = shareworker.New(shareworker.Config{
		Repo:    fxPerm.repo,
		Broker:  fxPerm.fake,
		Now:     func() time.Time { return fxPerm.now },
		Rand:    rand.New(rand.NewSource(1)),
		Metrics: mPerm,
	})
	idP := fxPerm.insertPending(t)
	fxPerm.fake.QueuePublishError(idP, broker.ErrBrokerPermanent)
	_, err = fxPerm.w.RunOnce(context.Background())
	r.NoError(err)
	r.EqualValues(1, mPerm.SharePublishes("terminal_fail").Get())

	// terminal_fail (max-attempts): transient at the boundary.
	fxMax := newWorkerFixture(t)
	mMax := obs.NewTestMetrics()
	fxMax.w = shareworker.New(shareworker.Config{
		Repo:    fxMax.repo,
		Broker:  fxMax.fake,
		Now:     func() time.Time { return fxMax.now },
		Rand:    rand.New(rand.NewSource(1)),
		Metrics: mMax,
	})
	idM := fxMax.insertPending(t)
	_, err = fxMax.db.ExecContext(context.Background(),
		`UPDATE scopes SET broker_attempts = ?, broker_next_attempt_at = NULL WHERE uuid = ?`,
		share.MaxBrokerAttempts-1, idM)
	r.NoError(err)
	fxMax.fake.QueuePublishError(idM, broker.ErrBrokerTransient)
	_, err = fxMax.w.RunOnce(context.Background())
	r.NoError(err)
	r.EqualValues(1, mMax.SharePublishes("terminal_fail").Get(),
		"transient at MaxBrokerAttempts must classify as terminal_fail")
}

func TestShareWorkerLogsCarryComponent(t *testing.T) {
	r := require.New(t)
	fx := newWorkerFixture(t)

	// bytes.Buffer is safe here because RunOnce returns synchronously
	// before the buffer is read — single goroutine throughout. If you
	// adapt this to a goroutine + Eventually pattern, switch to a
	// mutex-wrapped buffer (see backup/worker_test.go syncBuf).
	var logBuf bytes.Buffer
	base := slog.New(slog.NewJSONHandler(&logBuf, nil))
	fx.w = shareworker.New(shareworker.Config{
		Repo:    fx.repo,
		Broker:  fx.fake,
		Now:     func() time.Time { return fx.now },
		Rand:    rand.New(rand.NewSource(1)),
		Logger:  base.With("component", "share"),
		Metrics: obs.NewTestMetrics(),
	})
	_ = fx.insertPending(t)
	_, err := fx.w.RunOnce(context.Background())
	r.NoError(err)
	// Per-line scan: every emitted line must carry component=share.
	for line := range strings.SplitSeq(strings.TrimSpace(logBuf.String()), "\n") {
		r.Contains(line, `"component":"share"`,
			"every share-worker log line must carry component=share")
	}
}
