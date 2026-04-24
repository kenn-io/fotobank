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
