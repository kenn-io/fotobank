package share_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/share"
	"github.com/wesm/fotobank/internal/testutil"
)

// seedOwner inserts a minimal owners row so scopes FK-checks pass.
func seedOwner(t *testing.T, rw *sql.DB, p owners.Principal, storageKey string) {
	t.Helper()
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, storageKey, time.Now().UTC(),
	)
	require.NoError(t, err)
}

// seedAlbum inserts a minimal album row owned by p.
func seedAlbum(t *testing.T, rw *sql.DB, p owners.Principal) string {
	t.Helper()
	id := uuid.NewString()
	now := time.Now().UTC()
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO albums(id, owner_hub, owner_user_id, name, created_at, updated_at)
         VALUES(?,?,?,?,?,?)`,
		id, p.Hub, p.UserID, "t", now, now)
	require.NoError(t, err)
	return id
}

// seedMedia inserts a minimal media row owned by p and returns its ID.
func seedMedia(t *testing.T, rw *sql.DB, p owners.Principal, checksum string) string {
	t.Helper()
	repo := media.NewRepo(rw, rw)
	m := media.Media{
		ID: uuid.NewString(), Owner: p, Type: media.TypePhoto,
		MimeType: "image/jpeg", Path: "2024/" + checksum + ".jpg",
		OriginalFilename: "x.jpg", ImportedAt: time.Now().UTC().Truncate(time.Second),
		Size: 100, Checksum: checksum, ThumbStatus: "pending",
	}
	require.NoError(t, repo.Insert(context.Background(), m))
	return m.ID
}

func TestRepoInsertAlbumLiveAndGet(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "o"}
	seedOwner(t, d.WriteDB(), owner, "sk")
	albumID := seedAlbum(t, d.WriteDB(), owner)

	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	s := share.Scope{
		UUID: uuid.NewString(), Owner: owner,
		Grantee:       owners.Principal{Hub: "h", UserID: "g"},
		TargetType:    share.TargetAlbumLive,
		TargetAlbumID: &albumID,
		Label:         "Summer", CreatedAt: time.Now().UTC().Truncate(time.Second),
		BrokerStatus: share.StatusPending,
	}
	r.NoError(repo.Insert(context.Background(), s, nil))

	got, err := repo.GetByUUID(context.Background(), s.UUID)
	r.NoError(err)
	r.Equal(s.UUID, got.UUID)
	r.Equal(owner, got.Owner)
	r.Equal(share.TargetAlbumLive, got.TargetType)
	r.NotNil(got.TargetAlbumID)
	r.Equal(albumID, *got.TargetAlbumID)
	r.Equal(share.StatusPending, got.BrokerStatus)
	r.Empty(got.MediaIDs)
}

func TestRepoInsertMediaSetAndGet(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "o"}
	seedOwner(t, d.WriteDB(), owner, "sk")
	m1 := seedMedia(t, d.WriteDB(), owner, "c1")
	m2 := seedMedia(t, d.WriteDB(), owner, "c2")

	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	s := share.Scope{
		UUID: uuid.NewString(), Owner: owner,
		Grantee:      owners.Principal{Hub: "h", UserID: "g"},
		TargetType:   share.TargetMediaSet,
		CreatedAt:    time.Now().UTC().Truncate(time.Second),
		BrokerStatus: share.StatusPending,
	}
	r.NoError(repo.Insert(context.Background(), s, []string{m1, m2}))

	got, err := repo.GetByUUID(context.Background(), s.UUID)
	r.NoError(err)
	r.Equal(share.TargetMediaSet, got.TargetType)
	r.Nil(got.TargetAlbumID)
	r.ElementsMatch([]string{m1, m2}, got.MediaIDs)
}

func TestRepoGetByUUIDNotFound(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	_, err := repo.GetByUUID(context.Background(), uuid.NewString())
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestRepoInsertRoundtripsAllColumns(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "o"}
	seedOwner(t, d.WriteDB(), owner, "sk")
	albumID := seedAlbum(t, d.WriteDB(), owner)

	expires := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	s := share.Scope{
		UUID: uuid.NewString(), Owner: owner,
		Grantee:       owners.Principal{Hub: "h", UserID: "g"},
		TargetType:    share.TargetAlbumLive,
		TargetAlbumID: &albumID,
		AllowDownload: true,
		Label:         "Trip",
		ExpiresAt:     &expires,
		CreatedAt:     time.Now().UTC().Truncate(time.Second),
		BrokerStatus:  share.StatusPending,
	}
	r.NoError(repo.Insert(context.Background(), s, nil))

	got, err := repo.GetByUUID(context.Background(), s.UUID)
	r.NoError(err)
	r.True(got.AllowDownload)
	r.Equal("Trip", got.Label)
	r.NotNil(got.ExpiresAt)
	r.True(got.ExpiresAt.Equal(expires))
}

func TestRepoListByOwnerDefaultHidesRevokedRemote(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "o"}
	seedOwner(t, d.WriteDB(), owner, "sk")
	albumID := seedAlbum(t, d.WriteDB(), owner)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())

	insertScope := func(status share.BrokerStatus, revokedAt *time.Time) string {
		s := share.Scope{
			UUID: uuid.NewString(), Owner: owner,
			Grantee:       owners.Principal{Hub: "h", UserID: "g"},
			TargetType:    share.TargetAlbumLive,
			TargetAlbumID: &albumID,
			CreatedAt:     time.Now().UTC().Truncate(time.Second),
			BrokerStatus:  status,
			RevokedAt:     revokedAt,
		}
		r.NoError(repo.Insert(context.Background(), s, nil))
		if status != share.StatusPending || revokedAt != nil {
			_, err := d.WriteDB().ExecContext(context.Background(),
				`UPDATE scopes SET broker_status = ?, revoked_at = ? WHERE uuid = ?`,
				string(status), nullableTime(revokedAt), s.UUID)
			r.NoError(err)
		}
		return s.UUID
	}
	now := time.Now().UTC()
	pendingID := insertScope(share.StatusPending, nil)
	activeID := insertScope(share.StatusActive, nil)
	failedID := insertScope(share.StatusFailed, nil)
	revokingID := insertScope(share.StatusRevoking, &now)
	remoteID := insertScope(share.StatusRevokedRemote, &now)

	// Default filter hides revoked_remote; everything else visible.
	got, err := repo.ListByOwner(context.Background(), owner, share.ScopeFilter{})
	r.NoError(err)
	gotIDs := ids(got)
	r.ElementsMatch([]string{pendingID, activeID, failedID, revokingID}, gotIDs)
	r.NotContains(gotIDs, remoteID)

	// IncludeSettled=true returns everything.
	got, err = repo.ListByOwner(context.Background(), owner, share.ScopeFilter{IncludeSettled: true})
	r.NoError(err)
	gotIDs = ids(got)
	r.Contains(gotIDs, remoteID)

	// Explicit status filter returns exactly those statuses.
	got, err = repo.ListByOwner(context.Background(), owner, share.ScopeFilter{
		Status: []share.BrokerStatus{share.StatusFailed, share.StatusRevokedRemote},
	})
	r.NoError(err)
	gotIDs = ids(got)
	r.ElementsMatch([]string{failedID, remoteID}, gotIDs)
}

func TestRepoListByOwnerScopedToCaller(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	a := owners.Principal{Hub: "h", UserID: "a"}
	b := owners.Principal{Hub: "h", UserID: "b"}
	seedOwner(t, d.WriteDB(), a, "ska")
	seedOwner(t, d.WriteDB(), b, "skb")
	alA := seedAlbum(t, d.WriteDB(), a)
	alB := seedAlbum(t, d.WriteDB(), b)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())

	insert := func(owner owners.Principal, albumID string) string {
		s := share.Scope{
			UUID: uuid.NewString(), Owner: owner,
			Grantee:       owners.Principal{Hub: "h", UserID: "g"},
			TargetType:    share.TargetAlbumLive,
			TargetAlbumID: &albumID,
			CreatedAt:     time.Now().UTC().Truncate(time.Second),
			BrokerStatus:  share.StatusPending,
		}
		r.NoError(repo.Insert(context.Background(), s, nil))
		return s.UUID
	}
	sA := insert(a, alA)
	sB := insert(b, alB)

	got, err := repo.ListByOwner(context.Background(), a, share.ScopeFilter{})
	r.NoError(err)
	gotIDs := ids(got)
	r.ElementsMatch([]string{sA}, gotIDs)
	r.NotContains(gotIDs, sB)
}

func TestRepoListReadyReturnsDueRowsOnly(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "o"}
	seedOwner(t, d.WriteDB(), owner, "sk")
	albumID := seedAlbum(t, d.WriteDB(), owner)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())

	future := time.Now().UTC().Add(1 * time.Hour)
	past := time.Now().UTC().Add(-1 * time.Hour)

	insert := func(status share.BrokerStatus, nextAt *time.Time) string {
		s := share.Scope{
			UUID: uuid.NewString(), Owner: owner,
			Grantee:       owners.Principal{Hub: "h", UserID: "g"},
			TargetType:    share.TargetAlbumLive,
			TargetAlbumID: &albumID,
			CreatedAt:     time.Now().UTC().Truncate(time.Second),
			BrokerStatus:  share.StatusPending,
		}
		r.NoError(repo.Insert(context.Background(), s, nil))
		_, err := d.WriteDB().ExecContext(context.Background(),
			`UPDATE scopes SET broker_status = ?, broker_next_attempt_at = ? WHERE uuid = ?`,
			string(status), nullableTime(nextAt), s.UUID)
		r.NoError(err)
		return s.UUID
	}
	pendingNow := insert(share.StatusPending, nil)
	pendingPast := insert(share.StatusPending, &past)
	pendingFuture := insert(share.StatusPending, &future)
	revokingNow := insert(share.StatusRevoking, nil)
	active := insert(share.StatusActive, nil)

	rows, err := repo.ListReady(context.Background(), time.Now().UTC(), 100)
	r.NoError(err)
	got := ids(rows)
	r.ElementsMatch([]string{pendingNow, pendingPast, revokingNow}, got)
	r.NotContains(got, pendingFuture)
	r.NotContains(got, active)

	// Ordering contract: NULL next_attempt_at first, then earliest
	// next_attempt_at, then created_at. pendingNow and revokingNow both
	// have NULL next_attempt_at, followed by pendingPast (next_attempt_at
	// in the past).
	r.Len(rows, 3)
	// The two NULL-next_attempt rows come first (order between them is by
	// created_at; we don't assert which is first since seeding timestamps
	// are close together). But pendingPast MUST be last.
	r.Equal(pendingPast, rows[2].UUID, "pendingPast should sort after the two NULL rows")
}

func TestRepoListReadyRespectsMaxBrokerAttempts(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "o"}
	seedOwner(t, d.WriteDB(), owner, "sk")
	albumID := seedAlbum(t, d.WriteDB(), owner)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())

	s := share.Scope{
		UUID: uuid.NewString(), Owner: owner,
		Grantee:       owners.Principal{Hub: "h", UserID: "g"},
		TargetType:    share.TargetAlbumLive,
		TargetAlbumID: &albumID,
		CreatedAt:     time.Now().UTC().Truncate(time.Second),
		BrokerStatus:  share.StatusPending,
	}
	r.NoError(repo.Insert(context.Background(), s, nil))
	_, err := d.WriteDB().ExecContext(context.Background(),
		`UPDATE scopes SET broker_attempts = ? WHERE uuid = ?`,
		share.MaxBrokerAttempts, s.UUID)
	r.NoError(err)

	rows, err := repo.ListReady(context.Background(), time.Now().UTC(), 100)
	r.NoError(err)
	r.NotContains(ids(rows), s.UUID)
}

// ids extracts UUIDs so callers can use ElementsMatch cleanly.
func ids(rows []share.Scope) []string {
	out := make([]string, len(rows))
	for i, s := range rows {
		out[i] = s.UUID
	}
	return out
}

// nullableTime returns t for UPDATE binding; nil maps to SQL NULL.
func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}

func TestRepoMarkPublishedPendingToActive(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	uuidStr := seedPendingAlbumScope(t, d, repo)

	now := time.Now().UTC().Truncate(time.Second)
	n, err := repo.MarkPublished(context.Background(), uuidStr, now)
	r.NoError(err)
	r.Equal(int64(1), n)

	got, err := repo.GetByUUID(context.Background(), uuidStr)
	r.NoError(err)
	r.Equal(share.StatusActive, got.BrokerStatus)
	r.NotNil(got.BrokerRegisteredAt)
	r.NotNil(got.BrokerGrantedAt)
	r.True(got.BrokerRegisteredAt.Equal(now))
	r.True(got.BrokerGrantedAt.Equal(now))
	r.Empty(got.BrokerLastError)
	r.Nil(got.BrokerNextAttemptAt)
}

func TestRepoMarkPublishedRevokingRecordsTimestampsButKeepsStatus(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	uuidStr := seedPendingAlbumScope(t, d, repo)

	// Owner-Revoke raced the worker: flip to revoking directly.
	revokedAt := time.Now().UTC().Truncate(time.Second)
	_, err := d.WriteDB().ExecContext(context.Background(),
		`UPDATE scopes SET broker_status='revoking', revoked_at=? WHERE uuid=?`,
		revokedAt, uuidStr)
	r.NoError(err)

	now := revokedAt.Add(1 * time.Second)
	n, err := repo.MarkPublished(context.Background(), uuidStr, now)
	r.NoError(err)
	r.Equal(int64(1), n) // update still lands; status is unchanged.

	got, err := repo.GetByUUID(context.Background(), uuidStr)
	r.NoError(err)
	r.Equal(share.StatusRevoking, got.BrokerStatus)
	r.NotNil(got.BrokerRegisteredAt)
	r.NotNil(got.BrokerGrantedAt)
	r.NotNil(got.RevokedAt)
}

func TestRepoMarkPublishedNoopOnTerminalStatus(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	uuidStr := seedPendingAlbumScope(t, d, repo)

	// Move to revoked_remote directly.
	_, err := d.WriteDB().ExecContext(context.Background(),
		`UPDATE scopes SET broker_status='revoked_remote' WHERE uuid=?`,
		uuidStr)
	r.NoError(err)

	n, err := repo.MarkPublished(context.Background(), uuidStr, time.Now().UTC())
	r.NoError(err)
	r.Equal(int64(0), n) // fence rejected — row was terminal.
}

func TestRepoMarkPublishedSecondCallIsNoop(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	uuidStr := seedPendingAlbumScope(t, d, repo)

	first := time.Now().UTC().Truncate(time.Second)
	n, err := repo.MarkPublished(context.Background(), uuidStr, first)
	r.NoError(err)
	r.Equal(int64(1), n)

	// Row is now 'active'; the fence 'pending' | 'revoking' rejects
	// the second call. Timestamps remain the originals.
	second := first.Add(1 * time.Hour)
	n, err = repo.MarkPublished(context.Background(), uuidStr, second)
	r.NoError(err)
	r.Equal(int64(0), n)

	got, err := repo.GetByUUID(context.Background(), uuidStr)
	r.NoError(err)
	r.True(got.BrokerRegisteredAt.Equal(first))
	r.True(got.BrokerGrantedAt.Equal(first))
}

func TestRepoMarkAttemptFailedIncrementsAttempts(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	uuidStr := seedPendingAlbumScope(t, d, repo)

	nextAt := time.Now().UTC().Add(30 * time.Second).Truncate(time.Second)
	n, err := repo.MarkAttemptFailed(context.Background(), uuidStr, share.StatusPending, "boom", nextAt)
	r.NoError(err)
	r.Equal(int64(1), n)

	got, err := repo.GetByUUID(context.Background(), uuidStr)
	r.NoError(err)
	r.Equal(share.StatusPending, got.BrokerStatus)
	r.Equal(1, got.BrokerAttempts)
	r.Equal("boom", got.BrokerLastError)
	r.NotNil(got.BrokerNextAttemptAt)
	r.True(got.BrokerNextAttemptAt.Equal(nextAt))
}

func TestRepoMarkAttemptFailedFencedToPhase(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	uuidStr := seedPendingAlbumScope(t, d, repo)

	// Row is pending; calling with phase=revoking must be a no-op.
	nextAt := time.Now().UTC().Add(30 * time.Second).Truncate(time.Second)
	n, err := repo.MarkAttemptFailed(context.Background(), uuidStr, share.StatusRevoking, "wrong", nextAt)
	r.NoError(err)
	r.Equal(int64(0), n)

	got, err := repo.GetByUUID(context.Background(), uuidStr)
	r.NoError(err)
	r.Equal(0, got.BrokerAttempts)
	r.Empty(got.BrokerLastError)
	r.Nil(got.BrokerNextAttemptAt)
}

func TestRepoMarkAttemptFailedRevokingPhase(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	uuidStr := seedPendingAlbumScope(t, d, repo)

	// Flip to revoking directly (owner called Revoke).
	_, err := d.WriteDB().ExecContext(context.Background(),
		`UPDATE scopes SET broker_status='revoking', revoked_at=? WHERE uuid=?`,
		time.Now().UTC(), uuidStr)
	r.NoError(err)

	nextAt := time.Now().UTC().Add(30 * time.Second).Truncate(time.Second)
	n, err := repo.MarkAttemptFailed(context.Background(), uuidStr, share.StatusRevoking, "broker down", nextAt)
	r.NoError(err)
	r.Equal(int64(1), n)

	got, err := repo.GetByUUID(context.Background(), uuidStr)
	r.NoError(err)
	r.Equal(share.StatusRevoking, got.BrokerStatus)
	r.Equal(1, got.BrokerAttempts)
	r.Equal("broker down", got.BrokerLastError)
	r.True(got.BrokerNextAttemptAt.Equal(nextAt))
}

func TestRepoMarkFailedFlipsToFailed(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	uuidStr := seedPendingAlbumScope(t, d, repo)

	n, err := repo.MarkFailed(context.Background(), uuidStr, share.StatusPending, "fatal")
	r.NoError(err)
	r.Equal(int64(1), n)

	got, err := repo.GetByUUID(context.Background(), uuidStr)
	r.NoError(err)
	r.Equal(share.StatusFailed, got.BrokerStatus)
	r.Equal(1, got.BrokerAttempts)
	r.Equal("fatal", got.BrokerLastError)
	r.Nil(got.BrokerNextAttemptAt)
	r.Nil(got.RevokedAt)
}

// seedPendingAlbumScope returns the UUID of a freshly-inserted album_live
// scope in status pending. Reused across state-transition tests.
func seedPendingAlbumScope(t *testing.T, d dbDB, repo *share.Repo) string {
	t.Helper()
	owner := owners.Principal{Hub: "h", UserID: "o"}
	// Insert owner once. INSERT OR IGNORE so repeated helper calls in the
	// same test don't trip PK uniqueness.
	_, _ = d.WriteDB().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO owners(hub, user_id, storage_key, created_at)
         VALUES(?,?,?,?)`, owner.Hub, owner.UserID, "sk", time.Now().UTC())
	albumID := seedAlbum(t, d.WriteDB(), owner)
	s := share.Scope{
		UUID: uuid.NewString(), Owner: owner,
		Grantee:       owners.Principal{Hub: "h", UserID: "g"},
		TargetType:    share.TargetAlbumLive,
		TargetAlbumID: &albumID,
		CreatedAt:     time.Now().UTC().Truncate(time.Second),
		BrokerStatus:  share.StatusPending,
	}
	require.NoError(t, repo.Insert(context.Background(), s, nil))
	return s.UUID
}

// dbDB is the subset of *db.DB that test helpers need. Defined as an
// interface so future fakes can satisfy it without importing the real
// db package transitively.
type dbDB interface {
	WriteDB() *sql.DB
	ReadDB() *sql.DB
}

func TestRepoSetRevokingFromPending(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	uuidStr := seedPendingAlbumScope(t, d, repo)

	now := time.Now().UTC().Truncate(time.Second)
	n, err := repo.SetRevoking(context.Background(), uuidStr, now)
	r.NoError(err)
	r.Equal(int64(1), n)

	got, err := repo.GetByUUID(context.Background(), uuidStr)
	r.NoError(err)
	r.Equal(share.StatusRevoking, got.BrokerStatus)
	r.NotNil(got.RevokedAt)
	r.True(got.RevokedAt.Equal(now))
	r.Equal(0, got.BrokerAttempts)
	r.Nil(got.BrokerNextAttemptAt)
}

func TestRepoSetRevokingFromFailedPublishPhase(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	uuidStr := seedPendingAlbumScope(t, d, repo)
	_, err := d.WriteDB().ExecContext(context.Background(),
		`UPDATE scopes SET broker_status='failed', broker_attempts=10, broker_last_error='gone' WHERE uuid=?`,
		uuidStr)
	r.NoError(err)

	n, err := repo.SetRevoking(context.Background(), uuidStr, time.Now().UTC())
	r.NoError(err)
	r.Equal(int64(1), n)

	got, err := repo.GetByUUID(context.Background(), uuidStr)
	r.NoError(err)
	r.Equal(share.StatusRevoking, got.BrokerStatus)
	r.Equal(0, got.BrokerAttempts)
	r.Empty(got.BrokerLastError)
}

func TestRepoSetRevokingFromActive(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	uuidStr := seedPendingAlbumScope(t, d, repo)

	// Drive pending -> active via MarkPublished (the real production path).
	grantedAt := time.Now().UTC().Truncate(time.Second)
	_, err := repo.MarkPublished(context.Background(), uuidStr, grantedAt)
	r.NoError(err)

	// Now revoke it.
	revokedAt := grantedAt.Add(time.Hour)
	n, err := repo.SetRevoking(context.Background(), uuidStr, revokedAt)
	r.NoError(err)
	r.Equal(int64(1), n)

	got, err := repo.GetByUUID(context.Background(), uuidStr)
	r.NoError(err)
	r.Equal(share.StatusRevoking, got.BrokerStatus)
	r.True(got.RevokedAt.Equal(revokedAt))
	r.Equal(0, got.BrokerAttempts)
	// broker_granted_at should still reflect the prior publish.
	r.NotNil(got.BrokerGrantedAt)
	r.True(got.BrokerGrantedAt.Equal(grantedAt))
}

func TestRepoSetRevokingRejectsRevokedRemote(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	uuidStr := seedPendingAlbumScope(t, d, repo)
	now := time.Now().UTC()
	_, err := d.WriteDB().ExecContext(context.Background(),
		`UPDATE scopes SET broker_status='revoked_remote', revoked_at=? WHERE uuid=?`,
		now, uuidStr)
	r.NoError(err)

	n, err := repo.SetRevoking(context.Background(), uuidStr, now.Add(time.Second))
	r.NoError(err)
	r.Equal(int64(0), n)
}

func TestRepoSetRevokingRejectsRevokingRow(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	uuidStr := seedPendingAlbumScope(t, d, repo)
	now := time.Now().UTC()
	_, err := d.WriteDB().ExecContext(context.Background(),
		`UPDATE scopes SET broker_status='revoking', revoked_at=? WHERE uuid=?`,
		now, uuidStr)
	r.NoError(err)

	n, err := repo.SetRevoking(context.Background(), uuidStr, now.Add(time.Second))
	r.NoError(err)
	r.Equal(int64(0), n)
}

func TestRepoMarkRevokedRevokingToRevokedRemote(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	uuidStr := seedPendingAlbumScope(t, d, repo)
	_, err := d.WriteDB().ExecContext(context.Background(),
		`UPDATE scopes SET broker_status='revoking', revoked_at=? WHERE uuid=?`,
		time.Now().UTC(), uuidStr)
	r.NoError(err)

	at := time.Now().UTC().Add(time.Second).Truncate(time.Second)
	n, err := repo.MarkRevoked(context.Background(), uuidStr, at)
	r.NoError(err)
	r.Equal(int64(1), n)

	got, err := repo.GetByUUID(context.Background(), uuidStr)
	r.NoError(err)
	r.Equal(share.StatusRevokedRemote, got.BrokerStatus)
	r.NotNil(got.BrokerRevokedAt)
	r.True(got.BrokerRevokedAt.Equal(at))
	r.Nil(got.BrokerNextAttemptAt)
}

func TestRepoMarkRevokedRejectsOtherStates(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	uuidStr := seedPendingAlbumScope(t, d, repo) // still 'pending'

	n, err := repo.MarkRevoked(context.Background(), uuidStr, time.Now().UTC())
	r.NoError(err)
	r.Equal(int64(0), n)
}

// A row in 'revoking' without revoked_at is a broken invariant
// (SetRevoking always sets both). MarkRevoked refuses to transition
// such a row rather than silently producing a revoked_remote scope
// with no local revoke timestamp.
func TestRepoMarkRevokedRejectsRevokingWithoutRevokedAt(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	uuidStr := seedPendingAlbumScope(t, d, repo)

	// Force the invariant violation: revoking without revoked_at.
	_, err := d.WriteDB().ExecContext(context.Background(),
		`UPDATE scopes SET broker_status='revoking' WHERE uuid=?`, uuidStr)
	r.NoError(err)

	n, err := repo.MarkRevoked(context.Background(), uuidStr, time.Now().UTC())
	r.NoError(err)
	r.Equal(int64(0), n)
}

func TestRepoRetryPublishOnlyFailedWithoutRevokedAt(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	uuidStr := seedPendingAlbumScope(t, d, repo)
	_, err := d.WriteDB().ExecContext(context.Background(),
		`UPDATE scopes SET broker_status='failed', broker_attempts=10, broker_last_error='x' WHERE uuid=?`,
		uuidStr)
	r.NoError(err)

	n, err := repo.RetryPublish(context.Background(), uuidStr)
	r.NoError(err)
	r.Equal(int64(1), n)
	got, err := repo.GetByUUID(context.Background(), uuidStr)
	r.NoError(err)
	r.Equal(share.StatusPending, got.BrokerStatus)
	r.Equal(0, got.BrokerAttempts)
	r.Empty(got.BrokerLastError)
	r.Nil(got.BrokerNextAttemptAt)
}

func TestRepoRetryPublishRejectsRevokedFailed(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	uuidStr := seedPendingAlbumScope(t, d, repo)
	now := time.Now().UTC()
	_, err := d.WriteDB().ExecContext(context.Background(),
		`UPDATE scopes SET broker_status='failed', broker_attempts=10, revoked_at=? WHERE uuid=?`,
		now, uuidStr)
	r.NoError(err)

	n, err := repo.RetryPublish(context.Background(), uuidStr)
	r.NoError(err)
	r.Equal(int64(0), n)
}

func TestRepoRetryRevokeRejectsPublishSideFailed(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	uuidStr := seedPendingAlbumScope(t, d, repo)
	_, err := d.WriteDB().ExecContext(context.Background(),
		`UPDATE scopes SET broker_status='failed', broker_attempts=10, broker_last_error='x' WHERE uuid=?`,
		uuidStr)
	r.NoError(err)

	n, err := repo.RetryRevoke(context.Background(), uuidStr)
	r.NoError(err)
	r.Equal(int64(0), n)
}

func TestRepoRetryRevokeOnlyFailedWithRevokedAt(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	uuidStr := seedPendingAlbumScope(t, d, repo)
	now := time.Now().UTC()
	_, err := d.WriteDB().ExecContext(context.Background(),
		`UPDATE scopes SET broker_status='failed', broker_attempts=10, revoked_at=? WHERE uuid=?`,
		now, uuidStr)
	r.NoError(err)

	n, err := repo.RetryRevoke(context.Background(), uuidStr)
	r.NoError(err)
	r.Equal(int64(1), n)
	got, err := repo.GetByUUID(context.Background(), uuidStr)
	r.NoError(err)
	r.Equal(share.StatusRevoking, got.BrokerStatus)
	r.Equal(0, got.BrokerAttempts)
}

func TestRepoPrepareAlbumDeleteTxBlocksOnLiveScopes(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	uuidStr := seedPendingAlbumScope(t, d, repo)

	got, err := repo.GetByUUID(context.Background(), uuidStr)
	r.NoError(err)
	r.NotNil(got.TargetAlbumID)
	albumID := *got.TargetAlbumID

	tx, err := d.WriteDB().BeginTx(context.Background(), nil)
	r.NoError(err)
	defer tx.Rollback()
	err = repo.PrepareAlbumDeleteTx(context.Background(), tx, albumID)
	r.ErrorIs(err, share.ErrAlbumHasLiveScopes)
}

func TestRepoPrepareAlbumDeleteTxPurgesOnlyRevokedRemote(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "o"}
	seedOwner(t, d.WriteDB(), owner, "sk")
	albumID := seedAlbum(t, d.WriteDB(), owner)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())

	insertStatus := func(status share.BrokerStatus, revokedAt *time.Time, brokerGrantedAt *time.Time) string {
		s := share.Scope{
			UUID: uuid.NewString(), Owner: owner,
			Grantee:       owners.Principal{Hub: "h", UserID: "g"},
			TargetType:    share.TargetAlbumLive,
			TargetAlbumID: &albumID,
			CreatedAt:     time.Now().UTC().Truncate(time.Second),
			BrokerStatus:  share.StatusPending,
		}
		r.NoError(repo.Insert(context.Background(), s, nil))
		_, err := d.WriteDB().ExecContext(context.Background(),
			`UPDATE scopes SET broker_status=?, revoked_at=?, broker_granted_at=? WHERE uuid=?`,
			string(status), nullableTime(revokedAt), nullableTime(brokerGrantedAt), s.UUID)
		r.NoError(err)
		return s.UUID
	}
	now := time.Now().UTC()
	remote1 := insertStatus(share.StatusRevokedRemote, &now, &now)
	remote2 := insertStatus(share.StatusRevokedRemote, &now, &now)

	tx, err := d.WriteDB().BeginTx(context.Background(), nil)
	r.NoError(err)
	defer tx.Rollback()
	err = repo.PrepareAlbumDeleteTx(context.Background(), tx, albumID)
	r.NoError(err)
	r.NoError(tx.Commit())

	// Both revoked_remote rows dropped.
	_, err = repo.GetByUUID(context.Background(), remote1)
	r.ErrorIs(err, errs.ErrNotFound)
	_, err = repo.GetByUUID(context.Background(), remote2)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestRepoPrepareAlbumDeleteTxMixedPurgeAndBlock(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "o"}
	seedOwner(t, d.WriteDB(), owner, "sk")
	albumID := seedAlbum(t, d.WriteDB(), owner)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())

	insertStatus := func(status share.BrokerStatus, revokedAt *time.Time) string {
		s := share.Scope{
			UUID: uuid.NewString(), Owner: owner,
			Grantee:       owners.Principal{Hub: "h", UserID: "g"},
			TargetType:    share.TargetAlbumLive,
			TargetAlbumID: &albumID,
			CreatedAt:     time.Now().UTC().Truncate(time.Second),
			BrokerStatus:  share.StatusPending,
		}
		r.NoError(repo.Insert(context.Background(), s, nil))
		_, err := d.WriteDB().ExecContext(context.Background(),
			`UPDATE scopes SET broker_status=?, revoked_at=? WHERE uuid=?`,
			string(status), nullableTime(revokedAt), s.UUID)
		r.NoError(err)
		return s.UUID
	}
	now := time.Now().UTC()
	remoteID := insertStatus(share.StatusRevokedRemote, &now)
	pendingID := insertStatus(share.StatusPending, nil)

	// First pass: blocks; purge not applied (tx rolled back by caller).
	tx, err := d.WriteDB().BeginTx(context.Background(), nil)
	r.NoError(err)
	err = repo.PrepareAlbumDeleteTx(context.Background(), tx, albumID)
	r.ErrorIs(err, share.ErrAlbumHasLiveScopes)
	r.NoError(tx.Rollback())

	// After rollback, both rows still present.
	_, err = repo.GetByUUID(context.Background(), remoteID)
	r.NoError(err)
	_, err = repo.GetByUUID(context.Background(), pendingID)
	r.NoError(err)

	// Drive pending to revoked_remote, retry delete.
	_, err = d.WriteDB().ExecContext(context.Background(),
		`UPDATE scopes SET broker_status='revoked_remote', revoked_at=? WHERE uuid=?`,
		now, pendingID)
	r.NoError(err)

	tx, err = d.WriteDB().BeginTx(context.Background(), nil)
	r.NoError(err)
	r.NoError(repo.PrepareAlbumDeleteTx(context.Background(), tx, albumID))
	r.NoError(tx.Commit())

	_, err = repo.GetByUUID(context.Background(), remoteID)
	r.ErrorIs(err, errs.ErrNotFound)
	_, err = repo.GetByUUID(context.Background(), pendingID)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestRepoPrepareAlbumDeleteTxEmptyIsNoop(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "o"}
	seedOwner(t, d.WriteDB(), owner, "sk")
	albumID := seedAlbum(t, d.WriteDB(), owner)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())

	tx, err := d.WriteDB().BeginTx(context.Background(), nil)
	r.NoError(err)
	defer tx.Rollback()
	r.NoError(repo.PrepareAlbumDeleteTx(context.Background(), tx, albumID))
}

func TestRepoHasBlockingScopesForAlbum(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "o"}
	seedOwner(t, d.WriteDB(), owner, "sk")
	albumID := seedAlbum(t, d.WriteDB(), owner)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())

	blocking, err := repo.HasBlockingScopesForAlbum(context.Background(), albumID)
	r.NoError(err)
	r.False(blocking)

	// Add a revoked_remote: still not blocking.
	s := share.Scope{
		UUID: uuid.NewString(), Owner: owner,
		Grantee:       owners.Principal{Hub: "h", UserID: "g"},
		TargetType:    share.TargetAlbumLive,
		TargetAlbumID: &albumID,
		CreatedAt:     time.Now().UTC().Truncate(time.Second),
		BrokerStatus:  share.StatusPending,
	}
	r.NoError(repo.Insert(context.Background(), s, nil))
	now := time.Now().UTC()
	_, err = d.WriteDB().ExecContext(context.Background(),
		`UPDATE scopes SET broker_status='revoked_remote', revoked_at=? WHERE uuid=?`,
		now, s.UUID)
	r.NoError(err)
	blocking, err = repo.HasBlockingScopesForAlbum(context.Background(), albumID)
	r.NoError(err)
	r.False(blocking)

	// Add a pending: now blocking.
	s2 := share.Scope{
		UUID: uuid.NewString(), Owner: owner,
		Grantee:       owners.Principal{Hub: "h", UserID: "g2"},
		TargetType:    share.TargetAlbumLive,
		TargetAlbumID: &albumID,
		CreatedAt:     time.Now().UTC().Truncate(time.Second),
		BrokerStatus:  share.StatusPending,
	}
	r.NoError(repo.Insert(context.Background(), s2, nil))
	blocking, err = repo.HasBlockingScopesForAlbum(context.Background(), albumID)
	r.NoError(err)
	r.True(blocking)
}

func TestRepoPrepareAlbumDeleteTxDoesNotTouchOtherAlbums(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "o"}
	seedOwner(t, d.WriteDB(), owner, "sk")
	albumA := seedAlbum(t, d.WriteDB(), owner)
	albumB := seedAlbum(t, d.WriteDB(), owner)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())

	// revoked_remote scope on album B — should survive a Prepare on album A.
	s := share.Scope{
		UUID: uuid.NewString(), Owner: owner,
		Grantee:       owners.Principal{Hub: "h", UserID: "g"},
		TargetType:    share.TargetAlbumLive,
		TargetAlbumID: &albumB,
		CreatedAt:     time.Now().UTC().Truncate(time.Second),
		BrokerStatus:  share.StatusPending,
	}
	r.NoError(repo.Insert(context.Background(), s, nil))
	now := time.Now().UTC()
	_, err := d.WriteDB().ExecContext(context.Background(),
		`UPDATE scopes SET broker_status='revoked_remote', revoked_at=? WHERE uuid=?`,
		now, s.UUID)
	r.NoError(err)

	tx, err := d.WriteDB().BeginTx(context.Background(), nil)
	r.NoError(err)
	defer tx.Rollback()
	r.NoError(repo.PrepareAlbumDeleteTx(context.Background(), tx, albumA))
	r.NoError(tx.Commit())

	// Album B's scope still present.
	_, err = repo.GetByUUID(context.Background(), s.UUID)
	r.NoError(err)
}

func TestRepoPrepareAlbumDeleteTxIgnoresMediaSetScopes(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "o"}
	seedOwner(t, d.WriteDB(), owner, "sk")
	albumID := seedAlbum(t, d.WriteDB(), owner)
	mediaID := seedMedia(t, d.WriteDB(), owner, "c1")
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())

	// media_set scope with broker_status = revoked_remote. target_album_id
	// is NULL, so no album-delete should touch it.
	s := share.Scope{
		UUID: uuid.NewString(), Owner: owner,
		Grantee:      owners.Principal{Hub: "h", UserID: "g"},
		TargetType:   share.TargetMediaSet,
		CreatedAt:    time.Now().UTC().Truncate(time.Second),
		BrokerStatus: share.StatusPending,
	}
	r.NoError(repo.Insert(context.Background(), s, []string{mediaID}))
	now := time.Now().UTC()
	_, err := d.WriteDB().ExecContext(context.Background(),
		`UPDATE scopes SET broker_status='revoked_remote', revoked_at=? WHERE uuid=?`,
		now, s.UUID)
	r.NoError(err)

	tx, err := d.WriteDB().BeginTx(context.Background(), nil)
	r.NoError(err)
	defer tx.Rollback()
	r.NoError(repo.PrepareAlbumDeleteTx(context.Background(), tx, albumID))
	r.NoError(tx.Commit())

	_, err = repo.GetByUUID(context.Background(), s.UUID)
	r.NoError(err, "media_set scope should not be purged by album-delete")
}

// The blocking SELECT must filter by target_album_id as well: a live
// scope on a *different* album must not cause PrepareAlbumDeleteTx
// for the target album to return ErrAlbumHasLiveScopes. This pins the
// predicate on the block path (its companion above exercises the
// purge path).
func TestRepoPrepareAlbumDeleteTxDoesNotBlockOnOtherAlbumLive(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "o"}
	seedOwner(t, d.WriteDB(), owner, "sk")
	albumA := seedAlbum(t, d.WriteDB(), owner)
	albumB := seedAlbum(t, d.WriteDB(), owner)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())

	// Pending (live) scope on album B; deleting album A must succeed.
	s := share.Scope{
		UUID: uuid.NewString(), Owner: owner,
		Grantee:       owners.Principal{Hub: "h", UserID: "g"},
		TargetType:    share.TargetAlbumLive,
		TargetAlbumID: &albumB,
		CreatedAt:     time.Now().UTC().Truncate(time.Second),
		BrokerStatus:  share.StatusPending,
	}
	r.NoError(repo.Insert(context.Background(), s, nil))

	tx, err := d.WriteDB().BeginTx(context.Background(), nil)
	r.NoError(err)
	defer tx.Rollback()
	r.NoError(repo.PrepareAlbumDeleteTx(context.Background(), tx, albumA))
	r.NoError(tx.Commit())

	_, err = repo.GetByUUID(context.Background(), s.UUID)
	r.NoError(err, "live scope on another album should survive")
}

// A live media_set scope (target_album_id IS NULL) must not block a
// separate album-delete. Pins that the blocking SELECT's
// target_album_id = ? predicate excludes NULL targets.
func TestRepoPrepareAlbumDeleteTxDoesNotBlockOnLiveMediaSet(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "o"}
	seedOwner(t, d.WriteDB(), owner, "sk")
	albumID := seedAlbum(t, d.WriteDB(), owner)
	mediaID := seedMedia(t, d.WriteDB(), owner, "c1")
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())

	s := share.Scope{
		UUID: uuid.NewString(), Owner: owner,
		Grantee:      owners.Principal{Hub: "h", UserID: "g"},
		TargetType:   share.TargetMediaSet,
		CreatedAt:    time.Now().UTC().Truncate(time.Second),
		BrokerStatus: share.StatusPending,
	}
	r.NoError(repo.Insert(context.Background(), s, []string{mediaID}))

	tx, err := d.WriteDB().BeginTx(context.Background(), nil)
	r.NoError(err)
	defer tx.Rollback()
	r.NoError(repo.PrepareAlbumDeleteTx(context.Background(), tx, albumID))
	r.NoError(tx.Commit())

	_, err = repo.GetByUUID(context.Background(), s.UUID)
	r.NoError(err, "live media_set scope should not be blocked by album-delete")
}

// bumpActive flips a freshly-inserted scope's broker_status to 'active'
// and stamps the broker timestamps, mirroring a successful PublishScope
// without going through the worker.
func bumpActive(t *testing.T, d dbDB, uuidStr string, at time.Time) {
	t.Helper()
	_, err := d.WriteDB().ExecContext(context.Background(),
		`UPDATE scopes SET broker_status='active', broker_granted_at=?, broker_registered_at=? WHERE uuid=?`,
		at, at, uuidStr)
	require.NoError(t, err)
}

// makeMediaSetScope inserts a pending media_set scope owned by owner and
// granted to grantee. Seeds a fresh media row so the scope has a valid
// membership entry; returns the inserted Scope.
func makeMediaSetScope(t *testing.T, d dbDB, repo *share.Repo,
	owner, grantee owners.Principal, expiresAt *time.Time, now time.Time,
) share.Scope {
	t.Helper()
	mediaID := seedMedia(t, d.WriteDB(), owner, uuid.NewString())
	s := share.Scope{
		UUID: uuid.NewString(), Owner: owner, Grantee: grantee,
		TargetType:   share.TargetMediaSet,
		CreatedAt:    now,
		ExpiresAt:    expiresAt,
		BrokerStatus: share.StatusPending,
	}
	require.NoError(t, repo.Insert(context.Background(), s, []string{mediaID}))
	return s
}

// makeMediaSetScopeOver inserts a pending media_set scope over the given
// already-seeded media ids, with download as configured.
func makeMediaSetScopeOver(t *testing.T, d dbDB, repo *share.Repo,
	owner, grantee owners.Principal, expiresAt *time.Time, now time.Time,
	download bool, mediaIDs ...string,
) share.Scope {
	t.Helper()
	s := share.Scope{
		UUID: uuid.NewString(), Owner: owner, Grantee: grantee,
		TargetType:    share.TargetMediaSet,
		AllowDownload: download,
		CreatedAt:     now,
		ExpiresAt:     expiresAt,
		BrokerStatus:  share.StatusPending,
	}
	require.NoError(t, repo.Insert(context.Background(), s, mediaIDs))
	return s
}

// seedAlbumWithMedia seeds an album owned by owner plus n fresh media
// rows and links them via album_media. Returns the album ID and the
// seeded media IDs in insertion order.
func seedAlbumWithMedia(t *testing.T, d dbDB, owner owners.Principal, n int) (string, []string) {
	t.Helper()
	albumID := seedAlbum(t, d.WriteDB(), owner)
	mediaIDs := make([]string, 0, n)
	for range n {
		mid := seedMedia(t, d.WriteDB(), owner, uuid.NewString())
		_, err := d.WriteDB().ExecContext(context.Background(),
			`INSERT INTO album_media(album_id, media_id, added_at) VALUES(?,?,?)`,
			albumID, mid, time.Now().UTC())
		require.NoError(t, err)
		mediaIDs = append(mediaIDs, mid)
	}
	return albumID, mediaIDs
}

// makeAlbumLiveScope inserts a pending album_live scope over albumID.
func makeAlbumLiveScope(t *testing.T, d dbDB, repo *share.Repo,
	owner, grantee owners.Principal, albumID string, expiresAt *time.Time,
	now time.Time, download bool,
) share.Scope {
	t.Helper()
	s := share.Scope{
		UUID: uuid.NewString(), Owner: owner, Grantee: grantee,
		TargetType:    share.TargetAlbumLive,
		TargetAlbumID: &albumID,
		AllowDownload: download,
		CreatedAt:     now,
		ExpiresAt:     expiresAt,
		BrokerStatus:  share.StatusPending,
	}
	require.NoError(t, repo.Insert(context.Background(), s, nil))
	return s
}

func TestValidateHeaderScopesFiltersByGranteeAndLivePredicate(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	charlie := owners.Principal{Hub: "h", UserID: "charlie"}
	seedOwner(t, d.WriteDB(), alice, "ska")
	seedOwner(t, d.WriteDB(), bob, "skb")
	seedOwner(t, d.WriteDB(), charlie, "skc")

	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// 1. live + granted to bob — kept.
	live := makeMediaSetScope(t, d, repo, alice, bob, nil, now)
	bumpActive(t, d, live.UUID, now)

	// 2. revoked — dropped.
	revoked := makeMediaSetScope(t, d, repo, alice, bob, nil, now)
	bumpActive(t, d, revoked.UUID, now)
	_, err := repo.SetRevoking(context.Background(), revoked.UUID, now)
	r.NoError(err)

	// 3. pending (not active yet) — dropped.
	pending := makeMediaSetScope(t, d, repo, alice, bob, nil, now)

	// 4. expired — dropped.
	past := now.Add(-time.Hour)
	expired := makeMediaSetScope(t, d, repo, alice, bob, &past, now)
	bumpActive(t, d, expired.UUID, now)

	// 5. granted to someone else — dropped.
	other := makeMediaSetScope(t, d, repo, alice, charlie, nil, now)
	bumpActive(t, d, other.UUID, now)

	got, err := repo.ValidateHeaderScopes(context.Background(), bob,
		[]string{live.UUID, revoked.UUID, pending.UUID, expired.UUID, other.UUID},
		now)
	r.NoError(err)
	r.Len(got, 1)
	r.Equal(live.UUID, got[0].UUID)
}

func TestValidateHeaderScopesEmptyInputReturnsEmpty(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	got, err := repo.ValidateHeaderScopes(context.Background(),
		owners.Principal{Hub: "h", UserID: "bob"}, nil, time.Now())
	r.NoError(err)
	r.Empty(got)
}
