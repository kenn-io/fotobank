package hidden_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/auth/hidden"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/testutil"
)

func seedOwner(t *testing.T, rw *sql.DB, p owners.Principal, sk string) {
	t.Helper()
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, sk, time.Now().UTC())
	require.NoError(t, err)
}

func testPrincipal() owners.Principal {
	return owners.Principal{Hub: "h", UserID: "u"}
}

var fixedNow = time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)

// --- Credential tests ---

func TestCredentialGetNotFound(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	p := testPrincipal()
	seedOwner(t, d.WriteDB(), p, "sk")

	_, err := repo.GetCredential(context.Background(), p)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestCredentialUpsertAndGet(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	p := testPrincipal()
	seedOwner(t, d.WriteDB(), p, "sk")

	r.NoError(repo.UpsertCredential(context.Background(), p, "hash1", fixedNow))

	got, err := repo.GetCredential(context.Background(), p)
	r.NoError(err)
	r.Equal(p, got.Principal)
	r.Equal("hash1", got.PasscodeHash)
	r.Equal(fixedNow, got.CreatedAt)
	r.Equal(fixedNow, got.UpdatedAt)
}

func TestCredentialUpsertUpdatesHash(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	p := testPrincipal()
	seedOwner(t, d.WriteDB(), p, "sk")

	r.NoError(repo.UpsertCredential(context.Background(), p, "hash1", fixedNow))

	later := fixedNow.Add(time.Hour)
	r.NoError(repo.UpsertCredential(context.Background(), p, "hash2", later))

	got, err := repo.GetCredential(context.Background(), p)
	r.NoError(err)
	r.Equal("hash2", got.PasscodeHash)
	// created_at must stay pinned to the original insert time
	r.Equal(fixedNow, got.CreatedAt)
	r.Equal(later, got.UpdatedAt)
}

func TestCredentialDelete(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	p := testPrincipal()
	seedOwner(t, d.WriteDB(), p, "sk")

	r.NoError(repo.UpsertCredential(context.Background(), p, "hash1", fixedNow))
	r.NoError(repo.DeleteCredential(context.Background(), p))

	_, err := repo.GetCredential(context.Background(), p)
	r.ErrorIs(err, errs.ErrNotFound)
}

// --- Session tests ---

func tokenSHA256(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

func TestSessionInsertAndLookupActive(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	p := testPrincipal()
	seedOwner(t, d.WriteDB(), p, "sk")

	tok := tokenSHA256("token1")
	sess := hidden.Session{
		TokenSHA256: tok,
		Principal:   p,
		IssuedAt:    fixedNow.Add(-time.Minute),
		ExpiresAt:   fixedNow.Add(time.Hour),
	}
	r.NoError(repo.InsertSession(context.Background(), sess))

	got, err := repo.LookupActiveSession(context.Background(), tok, fixedNow)
	r.NoError(err)
	r.Equal(p, got.Principal)
	r.Nil(got.RevokedAt)
}

func TestSessionLookupActiveRejectsExpired(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	p := testPrincipal()
	seedOwner(t, d.WriteDB(), p, "sk")

	tok := tokenSHA256("token-expired")
	sess := hidden.Session{
		TokenSHA256: tok,
		Principal:   p,
		IssuedAt:    fixedNow.Add(-2 * time.Hour),
		ExpiresAt:   fixedNow.Add(-time.Minute), // already expired
	}
	r.NoError(repo.InsertSession(context.Background(), sess))

	_, err := repo.LookupActiveSession(context.Background(), tok, fixedNow)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestRepoLookupActiveSessionRejectsExpiredAndRevoked(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	p := testPrincipal()
	seedOwner(t, d.WriteDB(), p, "sk")

	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	tok := tokenSHA256("token")

	r.NoError(repo.InsertSession(context.Background(), hidden.Session{
		TokenSHA256: tok, Principal: p,
		IssuedAt:  now.Add(-time.Minute),
		ExpiresAt: now.Add(time.Minute),
	}))
	got, err := repo.LookupActiveSession(context.Background(), tok, now)
	r.NoError(err)
	r.Equal(p, got.Principal)

	r.NoError(repo.RevokeSession(context.Background(), tok, now))
	_, err = repo.LookupActiveSession(context.Background(), tok, now)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestSessionRevokeAllForPrincipal(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	p := testPrincipal()
	seedOwner(t, d.WriteDB(), p, "sk")

	tok1 := tokenSHA256("t1")
	tok2 := tokenSHA256("t2")
	for _, tok := range [][]byte{tok1, tok2} {
		r.NoError(repo.InsertSession(context.Background(), hidden.Session{
			TokenSHA256: tok, Principal: p,
			IssuedAt:  fixedNow.Add(-time.Minute),
			ExpiresAt: fixedNow.Add(time.Hour),
		}))
	}

	r.NoError(repo.RevokeAllSessionsForPrincipal(context.Background(), p, fixedNow))

	for _, tok := range [][]byte{tok1, tok2} {
		_, err := repo.LookupActiveSession(context.Background(), tok, fixedNow)
		r.ErrorIs(err, errs.ErrNotFound)
	}
}

func TestSessionSweepExpired(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	p := testPrincipal()
	seedOwner(t, d.WriteDB(), p, "sk")

	expiredTok := tokenSHA256("expired")
	activeTok := tokenSHA256("active")

	r.NoError(repo.InsertSession(context.Background(), hidden.Session{
		TokenSHA256: expiredTok, Principal: p,
		IssuedAt:  fixedNow.Add(-2 * time.Hour),
		ExpiresAt: fixedNow.Add(-time.Minute),
	}))
	r.NoError(repo.InsertSession(context.Background(), hidden.Session{
		TokenSHA256: activeTok, Principal: p,
		IssuedAt:  fixedNow.Add(-time.Minute),
		ExpiresAt: fixedNow.Add(time.Hour),
	}))

	r.NoError(repo.SweepExpiredSessions(context.Background(), fixedNow))

	// active session unaffected
	got, err := repo.LookupActiveSession(context.Background(), activeTok, fixedNow)
	r.NoError(err)
	r.Equal(p, got.Principal)

	// expired session now has revoked_at set — lookup returns ErrNotFound
	_, err = repo.LookupActiveSession(context.Background(), expiredTok, fixedNow)
	r.ErrorIs(err, errs.ErrNotFound)
}

// --- Failure tests ---

func TestFailureInsertAndCountRecent(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	p := testPrincipal()
	seedOwner(t, d.WriteDB(), p, "sk")

	since := fixedNow.Add(-10 * time.Minute)
	n, err := repo.CountRecentFailures(context.Background(), p, since)
	r.NoError(err)
	r.Equal(0, n)

	r.NoError(repo.InsertFailure(context.Background(), p, fixedNow.Add(-5*time.Minute)))
	r.NoError(repo.InsertFailure(context.Background(), p, fixedNow))
	// one failure before the window
	r.NoError(repo.InsertFailure(context.Background(), p, fixedNow.Add(-20*time.Minute)))

	n, err = repo.CountRecentFailures(context.Background(), p, since)
	r.NoError(err)
	r.Equal(2, n)
}

func TestFailurePurgeOld(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	p := testPrincipal()
	seedOwner(t, d.WriteDB(), p, "sk")

	r.NoError(repo.InsertFailure(context.Background(), p, fixedNow.Add(-2*time.Hour)))
	r.NoError(repo.InsertFailure(context.Background(), p, fixedNow.Add(-30*time.Minute)))

	cutoff := fixedNow.Add(-time.Hour)
	r.NoError(repo.PurgeOldFailures(context.Background(), cutoff))

	n, err := repo.CountRecentFailures(context.Background(), p, fixedNow.Add(-24*time.Hour))
	r.NoError(err)
	r.Equal(1, n) // only the -30m failure remains
}

// --- Lockout tests ---

func TestLockoutGetNotFound(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	p := testPrincipal()
	seedOwner(t, d.WriteDB(), p, "sk")

	_, err := repo.GetLockout(context.Background(), p)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestLockoutUpsertAndGet(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	p := testPrincipal()
	seedOwner(t, d.WriteDB(), p, "sk")

	until := fixedNow.Add(5 * time.Minute)
	r.NoError(repo.UpsertLockout(context.Background(), p, until, fixedNow))

	got, err := repo.GetLockout(context.Background(), p)
	r.NoError(err)
	r.Equal(p, got.Principal)
	r.Equal(until, got.LockedUntil)
	r.Equal(fixedNow, got.UpdatedAt)
}

func TestLockoutUpsertExtends(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	p := testPrincipal()
	seedOwner(t, d.WriteDB(), p, "sk")

	until1 := fixedNow.Add(5 * time.Minute)
	r.NoError(repo.UpsertLockout(context.Background(), p, until1, fixedNow))

	until2 := fixedNow.Add(10 * time.Minute)
	later := fixedNow.Add(time.Second)
	r.NoError(repo.UpsertLockout(context.Background(), p, until2, later))

	got, err := repo.GetLockout(context.Background(), p)
	r.NoError(err)
	r.Equal(until2, got.LockedUntil)
	r.Equal(later, got.UpdatedAt)
}

func TestLockoutDelete(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	p := testPrincipal()
	seedOwner(t, d.WriteDB(), p, "sk")

	r.NoError(repo.UpsertLockout(context.Background(), p, fixedNow.Add(time.Hour), fixedNow))
	r.NoError(repo.DeleteLockout(context.Background(), p))

	_, err := repo.GetLockout(context.Background(), p)
	r.ErrorIs(err, errs.ErrNotFound)
}
