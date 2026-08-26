package hidden_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/auth/hidden"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil"
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
	seedOwner(t, d.WriteDB(), p, "550e8400-e29b-41d4-a716-446655440000")

	_, err := repo.GetCredential(context.Background(), p)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestCredentialUpsertAndGet(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	p := testPrincipal()
	seedOwner(t, d.WriteDB(), p, "550e8400-e29b-41d4-a716-446655440000")

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
	seedOwner(t, d.WriteDB(), p, "550e8400-e29b-41d4-a716-446655440000")

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
	seedOwner(t, d.WriteDB(), p, "550e8400-e29b-41d4-a716-446655440000")

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
	seedOwner(t, d.WriteDB(), p, "550e8400-e29b-41d4-a716-446655440000")

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
	seedOwner(t, d.WriteDB(), p, "550e8400-e29b-41d4-a716-446655440000")

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
	seedOwner(t, d.WriteDB(), p, "550e8400-e29b-41d4-a716-446655440000")

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
	seedOwner(t, d.WriteDB(), p, "550e8400-e29b-41d4-a716-446655440000")

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
	seedOwner(t, d.WriteDB(), p, "550e8400-e29b-41d4-a716-446655440000")

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

func TestRevokeSessionIdempotent(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	p := testPrincipal()
	seedOwner(t, d.WriteDB(), p, "550e8400-e29b-41d4-a716-446655440000")

	tok := tokenSHA256("token-idempotent")
	r.NoError(repo.InsertSession(context.Background(), hidden.Session{
		TokenSHA256: tok, Principal: p,
		IssuedAt:  fixedNow.Add(-time.Minute),
		ExpiresAt: fixedNow.Add(time.Hour),
	}))

	firstRevoke := fixedNow
	r.NoError(repo.RevokeSession(context.Background(), tok, firstRevoke))

	// Second call with a later timestamp must not overwrite the original.
	laterRevoke := fixedNow.Add(time.Minute)
	r.NoError(repo.RevokeSession(context.Background(), tok, laterRevoke))

	var revokedAt time.Time
	err := d.WriteDB().QueryRowContext(context.Background(),
		`SELECT revoked_at FROM auth_hidden_session WHERE token_sha256 = ?`, tok,
	).Scan(&revokedAt)
	r.NoError(err)
	r.Equal(firstRevoke, revokedAt, "original revoke timestamp must win")
}

func TestLookupActiveSessionBoundaryAtExactNow(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	p := testPrincipal()
	seedOwner(t, d.WriteDB(), p, "550e8400-e29b-41d4-a716-446655440000")

	now := fixedNow
	tok := tokenSHA256("token-boundary-exact")
	r.NoError(repo.InsertSession(context.Background(), hidden.Session{
		TokenSHA256: tok, Principal: p,
		IssuedAt:  now.Add(-time.Minute),
		ExpiresAt: now, // exactly now — not strictly after
	}))

	_, err := repo.LookupActiveSession(context.Background(), tok, now)
	r.ErrorIs(err, errs.ErrNotFound, "expires_at == now must be rejected (strict >)")
}

func TestLookupActiveSessionBoundaryOneNsAfterNow(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	p := testPrincipal()
	seedOwner(t, d.WriteDB(), p, "550e8400-e29b-41d4-a716-446655440000")

	now := fixedNow
	tok := tokenSHA256("token-boundary-1ns")
	r.NoError(repo.InsertSession(context.Background(), hidden.Session{
		TokenSHA256: tok, Principal: p,
		IssuedAt:  now.Add(-time.Minute),
		ExpiresAt: now.Add(time.Nanosecond), // one nanosecond after now
	}))

	got, err := repo.LookupActiveSession(context.Background(), tok, now)
	r.NoError(err, "expires_at == now+1ns must be accepted (strict >)")
	r.Equal(p, got.Principal)
}

// TestSweepExpiredSessionsBoundaryAtExactNow ensures the sweep predicate
// matches LookupActiveSession's strict > at the boundary: a row with
// expires_at == now is inactive to lookups, so the sweep must reclaim it
// (otherwise it sits forever as un-revoked, un-active limbo).
func TestSweepExpiredSessionsBoundaryAtExactNow(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	p := testPrincipal()
	seedOwner(t, d.WriteDB(), p, "550e8400-e29b-41d4-a716-446655440000")

	now := fixedNow
	tok := tokenSHA256("token-sweep-boundary")
	r.NoError(repo.InsertSession(context.Background(), hidden.Session{
		TokenSHA256: tok, Principal: p,
		IssuedAt:  now.Add(-time.Minute),
		ExpiresAt: now,
	}))

	r.NoError(repo.SweepExpiredSessions(context.Background(), now))

	var revokedAt sql.NullTime
	err := d.WriteDB().QueryRowContext(context.Background(),
		`SELECT revoked_at FROM auth_hidden_session WHERE token_sha256 = ?`, tok,
	).Scan(&revokedAt)
	r.NoError(err)
	r.True(revokedAt.Valid, "session at expires_at == now must be revoked by sweep")
	r.Equal(now, revokedAt.Time)
}

// --- Failure tests ---

func TestFailureInsertAndCountRecent(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	p := testPrincipal()
	seedOwner(t, d.WriteDB(), p, "550e8400-e29b-41d4-a716-446655440000")

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
	seedOwner(t, d.WriteDB(), p, "550e8400-e29b-41d4-a716-446655440000")

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
	seedOwner(t, d.WriteDB(), p, "550e8400-e29b-41d4-a716-446655440000")

	_, err := repo.GetLockout(context.Background(), p)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestLockoutUpsertAndGet(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	p := testPrincipal()
	seedOwner(t, d.WriteDB(), p, "550e8400-e29b-41d4-a716-446655440000")

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
	seedOwner(t, d.WriteDB(), p, "550e8400-e29b-41d4-a716-446655440000")

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
	seedOwner(t, d.WriteDB(), p, "550e8400-e29b-41d4-a716-446655440000")

	r.NoError(repo.UpsertLockout(context.Background(), p, fixedNow.Add(time.Hour), fixedNow))
	r.NoError(repo.DeleteLockout(context.Background(), p))

	_, err := repo.GetLockout(context.Background(), p)
	r.ErrorIs(err, errs.ErrNotFound)
}
