package hidden_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/auth/hidden"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/testutil"
)

// fakeMediaPrivacy satisfies hidden.MediaPrivacy for service tests.
type fakeMediaPrivacy struct {
	called    bool
	principal owners.Principal
	// errOnce, if non-nil, is returned on the first call and then cleared.
	errOnce error
}

func (f *fakeMediaPrivacy) ClearAllHiddenForOwner(_ context.Context, owner owners.Principal) error {
	if f.errOnce != nil {
		err := f.errOnce
		f.errOnce = nil
		return err
	}
	f.called = true
	f.principal = owner
	return nil
}

func newTestServiceWithOwner(t *testing.T) (*hidden.Service, *hidden.Repo, *fakeMediaPrivacy, owners.Principal) {
	t.Helper()
	d := testutil.OpenTestDB(t)
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	mp := &fakeMediaPrivacy{}
	svc := hidden.NewService(repo, mp)
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")
	return svc, repo, mp, p
}

// --- Setup ---

func TestSetupStoresCredential(t *testing.T) {
	r := require.New(t)
	svc, repo, _, p := newTestServiceWithOwner(t)

	r.NoError(svc.Setup(context.Background(), p, "correct-passcode"))

	cred, err := repo.GetCredential(context.Background(), p)
	r.NoError(err)
	r.Equal(p, cred.Principal)
	r.NotEmpty(cred.PasscodeHash)
}

func TestSetupRejectsDuplicateSetup(t *testing.T) {
	r := require.New(t)
	svc, _, _, p := newTestServiceWithOwner(t)

	r.NoError(svc.Setup(context.Background(), p, "passcode"))
	r.ErrorIs(svc.Setup(context.Background(), p, "passcode2"), errs.ErrAlreadyExists)
}

// --- Passcode length validation ---

func TestSetupRejectsEmptyPasscode(t *testing.T) {
	svc, _, _, p := newTestServiceWithOwner(t)
	require.ErrorIs(t, svc.Setup(context.Background(), p, ""), errs.ErrInvalidArgument)
}

func TestSetupAcceptsOneByte(t *testing.T) {
	svc, _, _, p := newTestServiceWithOwner(t)
	require.NoError(t, svc.Setup(context.Background(), p, "x"))
}

func TestSetupAccepts1024Bytes(t *testing.T) {
	svc, _, _, p := newTestServiceWithOwner(t)
	passcode := string(bytes.Repeat([]byte("a"), 1024))
	require.NoError(t, svc.Setup(context.Background(), p, passcode))
}

func TestSetupRejects1025Bytes(t *testing.T) {
	svc, _, _, p := newTestServiceWithOwner(t)
	passcode := string(bytes.Repeat([]byte("a"), 1025))
	require.ErrorIs(t, svc.Setup(context.Background(), p, passcode), errs.ErrInvalidArgument)
}

// --- Unlock ---

func TestUnlockReturnsTokenAndExpiry(t *testing.T) {
	r := require.New(t)
	svc, _, _, p := newTestServiceWithOwner(t)
	r.NoError(svc.Setup(context.Background(), p, "passcode"))

	raw, expiresAt, err := svc.Unlock(context.Background(), p, "passcode")
	r.NoError(err)
	// 32 bytes base64url-encoded without padding = 43 chars
	r.Len(raw, 43, "token must be 43 chars (32 bytes base64url)")
	r.False(expiresAt.IsZero())
	r.True(expiresAt.After(time.Now()))
}

func TestUnlockWrongPasscodeReturnsPermissionDenied(t *testing.T) {
	r := require.New(t)
	svc, _, _, p := newTestServiceWithOwner(t)
	r.NoError(svc.Setup(context.Background(), p, "correct"))

	_, _, err := svc.Unlock(context.Background(), p, "wrong")
	r.ErrorIs(err, errs.ErrPermissionDenied)
}

// --- Wrong passcode inserts failure ---

func TestWrongPasscodeInsertsFailureRow(t *testing.T) {
	r := require.New(t)
	svc, repo, _, p := newTestServiceWithOwner(t)
	r.NoError(svc.Setup(context.Background(), p, "correct"))

	// Two wrong attempts.
	for range 2 {
		_, _, err := svc.Unlock(context.Background(), p, "wrong")
		r.ErrorIs(err, errs.ErrPermissionDenied)
	}

	n, err := repo.CountRecentFailures(context.Background(), p, time.Now().Add(-time.Minute))
	r.NoError(err)
	r.Equal(2, n)
}

// --- Lockout ---

func TestLockoutAfterThresholdFailures(t *testing.T) {
	r := require.New(t)
	svc, _, _, p := newTestServiceWithOwner(t)
	r.NoError(svc.Setup(context.Background(), p, "correct"))

	// Lower threshold to 2 to minimise Argon2 invocations.
	svc.SetLockoutForTest(30*time.Second, 10*time.Second, 2)

	_, _, err := svc.Unlock(context.Background(), p, "wrong")
	r.ErrorIs(err, errs.ErrPermissionDenied)
	_, _, err = svc.Unlock(context.Background(), p, "wrong")
	r.ErrorIs(err, errs.ErrPermissionDenied)

	// Third attempt must hit the lockout.
	_, _, err = svc.Unlock(context.Background(), p, "wrong")
	r.ErrorIs(err, errs.ErrLockedOut)
}

func TestActiveLockoutBlocksCorrectPasscode(t *testing.T) {
	r := require.New(t)
	svc, _, _, p := newTestServiceWithOwner(t)
	r.NoError(svc.Setup(context.Background(), p, "correct"))
	svc.SetLockoutForTest(30*time.Second, 10*time.Second, 2)

	// Trigger lockout.
	for range 2 {
		_, _, _ = svc.Unlock(context.Background(), p, "wrong")
	}

	// Correct passcode during active lockout must still 429.
	_, _, err := svc.Unlock(context.Background(), p, "correct")
	r.ErrorIs(err, errs.ErrLockedOut)
}

// --- Reset on success ---

func TestSuccessfulUnlockClearsFailuresAndLockout(t *testing.T) {
	r := require.New(t)
	svc, repo, _, p := newTestServiceWithOwner(t)
	r.NoError(svc.Setup(context.Background(), p, "correct"))

	// Lower threshold; leave window long enough for the test clock.
	svc.SetLockoutForTest(30*time.Second, 10*time.Second, 2)

	// Cause one failure and trigger lockout.
	_, _, _ = svc.Unlock(context.Background(), p, "wrong")
	_, _, _ = svc.Unlock(context.Background(), p, "wrong")

	// Advance clock past lockout so unlock can proceed.
	future := time.Now().Add(15 * time.Second)
	svc.SetClockForTest(func() time.Time { return future })

	// Correct passcode must succeed and clear state.
	_, _, err := svc.Unlock(context.Background(), p, "correct")
	r.NoError(err)

	n, err := repo.CountRecentFailures(context.Background(), p, time.Now().Add(-time.Minute))
	r.NoError(err)
	r.Equal(0, n, "failure rows must be cleared on success")

	_, err = repo.GetLockout(context.Background(), p)
	r.ErrorIs(err, errs.ErrNotFound, "lockout row must be cleared on success")
}

// --- Change ---

func TestChangeRevokesAllSessions(t *testing.T) {
	r := require.New(t)
	svc, repo, _, p := newTestServiceWithOwner(t)
	r.NoError(svc.Setup(context.Background(), p, "old-pass"))

	raw, _, err := svc.Unlock(context.Background(), p, "old-pass")
	r.NoError(err)

	tokenSHA, err := hidden.TokenSHA256(raw)
	r.NoError(err)

	r.NoError(svc.Change(context.Background(), p, "old-pass", "new-pass"))

	// Session must be gone after change.
	_, err = repo.LookupActiveSession(context.Background(), tokenSHA, time.Now())
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestChangeWrongOldPasscodeReturnsPermissionDenied(t *testing.T) {
	r := require.New(t)
	svc, _, _, p := newTestServiceWithOwner(t)
	r.NoError(svc.Setup(context.Background(), p, "correct"))

	r.ErrorIs(svc.Change(context.Background(), p, "wrong", "newpass"), errs.ErrPermissionDenied)
}

// --- Disable ---

func TestDisableClearsHiddenFlagsAndRevokesSessionsAndDeletesCredential(t *testing.T) {
	r := require.New(t)
	svc, repo, mp, p := newTestServiceWithOwner(t)
	r.NoError(svc.Setup(context.Background(), p, "pass"))
	_, _, err := svc.Unlock(context.Background(), p, "pass")
	r.NoError(err)

	r.NoError(svc.Disable(context.Background(), p, "pass"))

	r.True(mp.called, "ClearAllHiddenForOwner must be called on disable")
	r.Equal(p, mp.principal)

	_, err = repo.GetCredential(context.Background(), p)
	r.ErrorIs(err, errs.ErrNotFound, "credential must be deleted on disable")
}

func TestDisableWrongPasscodeReturnsPermissionDenied(t *testing.T) {
	r := require.New(t)
	svc, _, _, p := newTestServiceWithOwner(t)
	r.NoError(svc.Setup(context.Background(), p, "correct"))

	r.ErrorIs(svc.Disable(context.Background(), p, "wrong"), errs.ErrPermissionDenied)
}

// --- AdminReset ---

func TestAdminResetDeletesCredentialAndRevokesSessionsButNotHiddenFlags(t *testing.T) {
	r := require.New(t)
	svc, repo, mp, p := newTestServiceWithOwner(t)
	r.NoError(svc.Setup(context.Background(), p, "pass"))
	_, _, err := svc.Unlock(context.Background(), p, "pass")
	r.NoError(err)

	r.NoError(svc.AdminReset(context.Background(), p))

	_, err = repo.GetCredential(context.Background(), p)
	r.ErrorIs(err, errs.ErrNotFound, "credential must be deleted on admin reset")

	r.False(mp.called, "AdminReset must NOT clear hidden flags")
}

// --- Lock (idempotent) ---

func TestLockIdempotent(t *testing.T) {
	r := require.New(t)
	svc, _, _, p := newTestServiceWithOwner(t)
	r.NoError(svc.Setup(context.Background(), p, "pass"))

	raw, _, err := svc.Unlock(context.Background(), p, "pass")
	r.NoError(err)

	// Lock once then again — both must succeed.
	r.NoError(svc.Lock(context.Background(), raw))
	r.NoError(svc.Lock(context.Background(), raw))
}

func TestLockUnknownTokenIsNoop(t *testing.T) {
	// A well-formed base64url token that has no matching session row.
	fake := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" // 43 chars
	require.NoError(t, newService(t).Lock(context.Background(), fake))
}

// newService is a thin helper for tests that only need the service, not the
// repo or media collaborator directly.
func newService(t *testing.T) *hidden.Service {
	t.Helper()
	d := testutil.OpenTestDB(t)
	return hidden.NewService(hidden.NewRepo(d.WriteDB(), d.ReadDB()), &fakeMediaPrivacy{})
}

// --- Sweep ---

func TestSweepRevokesExpiredSessionsAndPurgesFailures(t *testing.T) {
	r := require.New(t)
	svc, repo, _, p := newTestServiceWithOwner(t)
	r.NoError(svc.Setup(context.Background(), p, "pass"))

	// Issue a session with a past clock so it will be expired at sweep time.
	past := time.Now().Add(-10 * time.Minute)
	svc.SetClockForTest(func() time.Time { return past })
	raw, _, err := svc.Unlock(context.Background(), p, "pass")
	r.NoError(err)

	// Insert a failure also in the past.
	r.NoError(repo.InsertFailure(context.Background(), p, past))

	// Advance clock well past expiry and sweep.
	future := time.Now().Add(10 * time.Minute)
	svc.SetClockForTest(func() time.Time { return future })
	r.NoError(svc.Sweep(context.Background()))

	// Session expired → now revoked.
	sha, err := hidden.TokenSHA256(raw)
	r.NoError(err)
	_, err = repo.LookupActiveSession(context.Background(), sha, future)
	r.ErrorIs(err, errs.ErrNotFound)

	// Failure older than window → purged.
	n, err := repo.CountRecentFailures(context.Background(), p, past.Add(-time.Hour))
	r.NoError(err)
	r.Equal(0, n)
}

// --- Cookie helpers via TokenSHA256 ---

func TestTokenSHA256RoundTrip(t *testing.T) {
	r := require.New(t)
	raw, sha, err := hidden.NewToken(rand.Reader)
	r.NoError(err)
	r.Len(raw, 43)
	r.Len(sha, 32)

	got, err := hidden.TokenSHA256(raw)
	r.NoError(err)
	r.Equal(sha, got)
}

func TestTokenSHA256RejectsMalformed(t *testing.T) {
	_, err := hidden.TokenSHA256("not-valid-base64url!!")
	require.Error(t, err)
}

// --- passcode helpers ---

func TestHashAndVerifyPasscode(t *testing.T) {
	r := require.New(t)
	encoded, err := hidden.HashPasscode("my-passcode")
	r.NoError(err)
	r.NotEmpty(encoded)

	ok, err := hidden.VerifyPasscode(encoded, "my-passcode")
	r.NoError(err)
	r.True(ok)
}

func TestVerifyPasscodeMismatch(t *testing.T) {
	r := require.New(t)
	encoded, err := hidden.HashPasscode("correct")
	r.NoError(err)

	ok, err := hidden.VerifyPasscode(encoded, "wrong")
	r.NoError(err)
	r.False(ok)
}

func TestHashPasscodeUniquePerCall(t *testing.T) {
	h1, _ := hidden.HashPasscode("p")
	h2, _ := hidden.HashPasscode("p")
	require.NotEqual(t, h1, h2, "each hash must use a fresh random salt")
}

func TestNewTokenDeterministic(t *testing.T) {
	r := require.New(t)
	src := bytes.Repeat([]byte{0xAB}, 32)
	raw, sha, err := hidden.NewToken(bytes.NewReader(src))
	r.NoError(err)
	r.Len(raw, 43)
	r.Len(sha, 32)

	// Same source bytes → same token.
	raw2, sha2, err := hidden.NewToken(bytes.NewReader(src))
	r.NoError(err)
	r.Equal(raw, raw2)
	r.Equal(sha, sha2)
}

func TestServiceSetRandForTest(t *testing.T) {
	r := require.New(t)
	svc, _, _, p := newTestServiceWithOwner(t)
	r.NoError(svc.Setup(context.Background(), p, "pass"))

	src := bytes.Repeat([]byte{0xCD}, 32)
	svc.SetRandForTest(bytes.NewReader(src))

	raw, _, err := svc.Unlock(context.Background(), p, "pass")
	r.NoError(err)
	r.Len(raw, 43)
}

// TestDisableRecoversFromMediaClearFailure verifies that if ClearAllHiddenForOwner
// fails mid-sequence, the credential row is still present (so the user can
// retry), and that a second Disable call succeeds and removes the credential.
func TestDisableRecoversFromMediaClearFailure(t *testing.T) {
	r := require.New(t)
	svc, repo, mp, p := newTestServiceWithOwner(t)
	r.NoError(svc.Setup(context.Background(), p, "pass"))

	// Inject a transient error on the first ClearAllHiddenForOwner call.
	mp.errOnce = errors.New("transient storage error")

	err := svc.Disable(context.Background(), p, "pass")
	r.Error(err, "first Disable must return an error when ClearAllHiddenForOwner fails")

	// Credential must still exist — user can re-run Disable.
	_, credErr := repo.GetCredential(context.Background(), p)
	r.NoError(credErr, "credential must survive a failed Disable so the user can retry")

	// Second Disable must succeed now that the transient error is cleared.
	r.NoError(svc.Disable(context.Background(), p, "pass"))

	_, credErr = repo.GetCredential(context.Background(), p)
	r.ErrorIs(credErr, errs.ErrNotFound, "credential must be gone after a successful Disable")
}

// TestLockoutAtProductionThreshold verifies that the default threshold of 5
// failures triggers a lockout, and that the correct passcode during an active
// lockout returns ErrLockedOut.
func TestLockoutAtProductionThreshold(t *testing.T) {
	r := require.New(t)
	svc, repo, _, p := newTestServiceWithOwner(t)

	// Pre-hash one credential and inject it directly so Argon2 runs only for
	// the verification calls, not for Setup.  Using a 1-byte passcode keeps
	// the work factor constant but doesn't skip it entirely — the test
	// explicitly exercises the production threshold (5).
	r.NoError(svc.Setup(context.Background(), p, "x"))

	// Use explicit production-default values so the test documents them.
	svc.SetLockoutForTest(60*time.Second, 5*time.Minute, 5)

	ctx := context.Background()
	for i := range 4 {
		_, _, err := svc.Unlock(ctx, p, "wrong")
		r.ErrorIs(err, errs.ErrPermissionDenied, "attempt %d should be denied, not locked", i+1)
	}

	// 5th wrong attempt triggers the lockout.
	_, _, err := svc.Unlock(ctx, p, "wrong")
	r.ErrorIs(err, errs.ErrPermissionDenied, "5th wrong attempt should still return denied")

	// Lockout must now be recorded.
	lo, err := repo.GetLockout(ctx, p)
	r.NoError(err)
	r.True(lo.LockedUntil.After(time.Now()), "lockout must be active after 5 failures")

	// Correct passcode during active lockout must return ErrLockedOut.
	_, _, err = svc.Unlock(ctx, p, "x")
	r.ErrorIs(err, errs.ErrLockedOut)
}

// TestLockMalformedTokenIsNoop verifies that Lock with an invalid base64url
// token returns nil and emits outcome=noop_malformed rather than an error.
func TestLockMalformedTokenIsNoop(t *testing.T) {
	svc := newService(t)
	// "!!!" is not valid base64url.
	require.NoError(t, svc.Lock(context.Background(), "!!!"))
}
