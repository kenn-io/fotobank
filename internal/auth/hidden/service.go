package hidden

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
)

// MediaPrivacy is the subset of the media layer that Service needs for
// the Disable operation. Injected so service tests do not depend on the
// real media repo.
type MediaPrivacy interface {
	ClearAllHiddenForOwner(ctx context.Context, owner owners.Principal) error
}

// Service orchestrates hidden-auth operations: credential setup/change/
// disable, token issuance, lockout enforcement, and background sweeps.
type Service struct {
	repo             *Repo
	media            MediaPrivacy
	now              func() time.Time
	rand             io.Reader
	ttl              time.Duration
	failureWindow    time.Duration
	failureThreshold int
	lockoutDuration  time.Duration
}

// NewService constructs a Service with production defaults.
//
// Defaults: ttl=5min, failureWindow=60s, failureThreshold=5, lockoutDuration=5min.
func NewService(repo *Repo, media MediaPrivacy) *Service {
	return &Service{
		repo:             repo,
		media:            media,
		now:              func() time.Time { return time.Now().UTC() },
		rand:             cryptorand.Reader,
		ttl:              5 * time.Minute,
		failureWindow:    60 * time.Second,
		failureThreshold: 5,
		lockoutDuration:  5 * time.Minute,
	}
}

// SetClockForTest replaces the clock. Only call from tests.
func (s *Service) SetClockForTest(now func() time.Time) { s.now = now }

// SetLockoutForTest replaces lockout parameters. Only call from tests.
func (s *Service) SetLockoutForTest(window, duration time.Duration, threshold int) {
	s.failureWindow = window
	s.lockoutDuration = duration
	s.failureThreshold = threshold
}

// SetRandForTest replaces the random source. Only call from tests.
func (s *Service) SetRandForTest(r io.Reader) { s.rand = r }

// Setup stores a new passcode hash for principal. Returns ErrAlreadyExists if
// a credential already exists.
//
// The cheap GetCredential fast-path runs before the expensive HashPasscode so
// a duplicate-Setup hammer cannot force Argon2id work. A concurrent Setup that
// races past the fast-path is caught by InsertCredential's unique-conflict
// mapping, which is the authoritative guard.
func (s *Service) Setup(ctx context.Context, principal owners.Principal, passcode string) error {
	if err := ValidatePasscode(passcode); err != nil {
		return fmt.Errorf("setup hidden: %w", err)
	}
	// Fast-path: short-circuit before the expensive Argon2id hash when a
	// credential already exists. A non-NotFound error (transient DB failure)
	// is returned to the caller rather than swallowed, so Setup never
	// silently proceeds when the existence check is uncertain.
	if _, err := s.repo.GetCredential(ctx, principal); err == nil {
		slog.InfoContext(ctx, "auth.hidden.setup",
			"principal", principal.String(), "outcome", "already_exists")
		return fmt.Errorf("setup hidden: %w", errs.ErrAlreadyExists)
	} else if !errors.Is(err, errs.ErrNotFound) {
		return fmt.Errorf("setup hidden: check existing: %w", err)
	}
	hash, err := HashPasscode(passcode)
	if err != nil {
		return fmt.Errorf("setup hidden: %w", err)
	}
	if err := s.repo.InsertCredential(ctx, principal, hash, s.now()); err != nil {
		if errors.Is(err, errs.ErrAlreadyExists) {
			slog.InfoContext(ctx, "auth.hidden.setup",
				"principal", principal.String(), "outcome", "already_exists")
		}
		return fmt.Errorf("setup hidden: %w", err)
	}
	slog.InfoContext(ctx, "auth.hidden.setup", "principal", principal.String(), "outcome", "ok")
	return nil
}

// Change verifies oldPasscode and, on success, stores newPasscode and revokes
// all active sessions for principal.
func (s *Service) Change(
	ctx context.Context,
	principal owners.Principal,
	oldPasscode, newPasscode string,
) error {
	if err := ValidatePasscode(oldPasscode); err != nil {
		return fmt.Errorf("change hidden passcode: %w", err)
	}
	if err := ValidatePasscode(newPasscode); err != nil {
		return fmt.Errorf("change hidden passcode: %w", err)
	}
	if err := s.consumePasscode(ctx, principal, oldPasscode); err != nil {
		slog.InfoContext(ctx, "auth.hidden.change",
			"principal", principal.String(), "outcome", outcomeFor(err))
		return fmt.Errorf("change hidden passcode: %w", err)
	}
	hash, err := HashPasscode(newPasscode)
	if err != nil {
		return fmt.Errorf("change hidden passcode: hash: %w", err)
	}
	if err := s.repo.UpsertCredential(ctx, principal, hash, s.now()); err != nil {
		return fmt.Errorf("change hidden passcode: upsert: %w", err)
	}
	if err := s.repo.RevokeAllSessionsForPrincipal(ctx, principal, s.now()); err != nil {
		return fmt.Errorf("change hidden passcode: revoke sessions: %w", err)
	}
	slog.InfoContext(ctx, "auth.hidden.change", "principal", principal.String(), "outcome", "ok")
	return nil
}

// Disable verifies passcode, clears all hidden media flags for principal,
// revokes all active sessions, then deletes the credential. Operations are
// ordered so that a mid-sequence failure leaves the credential intact and the
// caller can re-run Disable to retry from the beginning.
//
// All three operations span repos so they cannot share a SQL transaction;
// ClearAllHidden and RevokeAllSessions are idempotent and safe to re-run.
func (s *Service) Disable(ctx context.Context, principal owners.Principal, passcode string) error {
	if err := ValidatePasscode(passcode); err != nil {
		return fmt.Errorf("disable hidden: %w", err)
	}
	if err := s.consumePasscode(ctx, principal, passcode); err != nil {
		slog.InfoContext(ctx, "auth.hidden.disable",
			"principal", principal.String(), "outcome", outcomeFor(err))
		return fmt.Errorf("disable hidden: %w", err)
	}
	if err := s.media.ClearAllHiddenForOwner(ctx, principal); err != nil {
		return fmt.Errorf("disable hidden: clear hidden flags: %w", err)
	}
	if err := s.repo.RevokeAllSessionsForPrincipal(ctx, principal, s.now()); err != nil {
		return fmt.Errorf("disable hidden: revoke sessions: %w", err)
	}
	if err := s.repo.DeleteCredential(ctx, principal); err != nil {
		return fmt.Errorf("disable hidden: delete credential: %w", err)
	}
	slog.InfoContext(ctx, "auth.hidden.disable", "principal", principal.String(), "outcome", "ok")
	return nil
}

// Unlock verifies passcode and, on success, issues a new session token.
// Returns the raw token string (base64url, 43 chars) and the absolute expiry.
func (s *Service) Unlock(
	ctx context.Context,
	principal owners.Principal,
	passcode string,
) (rawToken string, expiresAt time.Time, err error) {
	if vErr := ValidatePasscode(passcode); vErr != nil {
		return "", time.Time{}, fmt.Errorf("unlock hidden: %w", vErr)
	}
	if cErr := s.consumePasscode(ctx, principal, passcode); cErr != nil {
		slog.InfoContext(ctx, "auth.hidden.unlock",
			"principal", principal.String(), "outcome", outcomeFor(cErr))
		return "", time.Time{}, fmt.Errorf("unlock hidden: %w", cErr)
	}
	now := s.now()
	expires := now.Add(s.ttl)
	raw, sha, err := NewToken(s.rand)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("unlock hidden: generate token: %w", err)
	}
	sess := Session{
		TokenSHA256: sha,
		Principal:   principal,
		IssuedAt:    now,
		ExpiresAt:   expires,
	}
	if err := s.repo.InsertSession(ctx, sess); err != nil {
		return "", time.Time{}, fmt.Errorf("unlock hidden: insert session: %w", err)
	}
	slog.InfoContext(ctx, "auth.hidden.unlock", "principal", principal.String(), "outcome", "ok")
	return raw, expires, nil
}

// Lock revokes the session identified by rawToken. It is idempotent: an
// unknown or already-revoked token returns nil.
func (s *Service) Lock(ctx context.Context, rawToken string) error {
	sha, err := TokenSHA256(rawToken)
	if err != nil {
		// Malformed token — treat as already-locked, no error.
		slog.InfoContext(ctx, "auth.hidden.lock", "outcome", "noop_malformed")
		return nil
	}
	if err := s.repo.RevokeSession(ctx, sha, s.now()); err != nil {
		slog.ErrorContext(ctx, "auth.hidden.lock", "outcome", "error", "err", err)
		return fmt.Errorf("lock hidden: %w", err)
	}
	slog.InfoContext(ctx, "auth.hidden.lock", "outcome", "ok")
	return nil
}

// AdminReset deletes the credential and revokes all sessions for principal.
// Hidden flags on media are NOT touched; the operator re-runs setup to
// attach a new passcode.
func (s *Service) AdminReset(ctx context.Context, principal owners.Principal) error {
	if err := s.repo.DeleteCredential(ctx, principal); err != nil {
		return fmt.Errorf("admin reset hidden: delete credential: %w", err)
	}
	if err := s.repo.RevokeAllSessionsForPrincipal(ctx, principal, s.now()); err != nil {
		return fmt.Errorf("admin reset hidden: revoke sessions: %w", err)
	}
	slog.InfoContext(ctx, "auth.hidden.admin_reset", "principal", principal.String(), "outcome", "ok")
	return nil
}

// GetCredential returns the credential for principal, or errs.ErrNotFound if
// no credential has been set up. The HTTP /state handler uses this to
// determine whether hidden is configured for the caller.
func (s *Service) GetCredential(ctx context.Context, principal owners.Principal) (*Credential, error) {
	return s.repo.GetCredential(ctx, principal)
}

// Sweep revokes expired sessions and purges failure rows older than the
// lockout window. Active lockout rows are not touched.
func (s *Service) Sweep(ctx context.Context) error {
	now := s.now()
	if err := s.repo.SweepExpiredSessions(ctx, now); err != nil {
		return fmt.Errorf("sweep hidden: expired sessions: %w", err)
	}
	before := now.Add(-s.failureWindow)
	if err := s.repo.PurgeOldFailures(ctx, before); err != nil {
		return fmt.Errorf("sweep hidden: old failures: %w", err)
	}
	return nil
}

// consumePasscode is the shared verify-and-track-failure helper used by
// Change, Disable, and Unlock.
//
//  1. Check for an active lockout; return ErrLockedOut if present.
//  2. Look up the credential; return ErrHiddenNotConfigured if missing.
//  3. Verify the passcode; on failure insert a failure row and maybe upsert
//     a lockout.
//  4. On success delete all failure rows and any lockout row for principal.
func (s *Service) consumePasscode(ctx context.Context, principal owners.Principal, passcode string) error {
	now := s.now()

	// Step 1: active lockout check (must happen before verification).
	// Fail closed on any non-NotFound DB error so a transient failure
	// reading auth_hidden_lockout cannot let attackers bypass an active
	// lockout window by retrying past the read failure.
	lo, lockErr := s.repo.GetLockout(ctx, principal)
	if lockErr != nil && !errors.Is(lockErr, errs.ErrNotFound) {
		return fmt.Errorf("check lockout: %w", lockErr)
	}
	if lockErr == nil && lo.LockedUntil.After(now) {
		return errs.ErrLockedOut
	}

	// Step 2: fetch credential.
	cred, err := s.repo.GetCredential(ctx, principal)
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			return fmt.Errorf("%w: no credential for principal %s", errs.ErrHiddenNotConfigured, principal)
		}
		return err
	}

	// Step 3: verify.
	ok, err := VerifyPasscode(cred.PasscodeHash, passcode)
	if err != nil {
		return fmt.Errorf("verify passcode: %w", err)
	}
	if !ok {
		if fErr := s.recordFailure(ctx, principal, now); fErr != nil {
			return fErr
		}
		return errs.ErrPermissionDenied
	}

	// Step 4: success — clear failure log and any lockout.
	if err := s.clearFailureState(ctx, principal); err != nil {
		return err
	}
	return nil
}

// recordFailure inserts a failure row and upserts a lockout if the threshold
// is reached and no active lockout already exists.
func (s *Service) recordFailure(ctx context.Context, principal owners.Principal, now time.Time) error {
	if err := s.repo.InsertFailure(ctx, principal, now); err != nil {
		return fmt.Errorf("record failure: %w", err)
	}
	since := now.Add(-s.failureWindow)
	count, err := s.repo.CountRecentFailures(ctx, principal, since)
	if err != nil {
		return fmt.Errorf("record failure: count: %w", err)
	}
	if count < s.failureThreshold {
		return nil
	}
	// Check whether there's already an active lockout before upserting.
	if lo, loErr := s.repo.GetLockout(ctx, principal); loErr == nil && lo.LockedUntil.After(now) {
		return nil // active lockout already in place
	}
	if err := s.repo.UpsertLockout(ctx, principal, now.Add(s.lockoutDuration), now); err != nil {
		return fmt.Errorf("record failure: upsert lockout: %w", err)
	}
	return nil
}

// clearFailureState deletes all failure rows and any lockout row for principal.
func (s *Service) clearFailureState(ctx context.Context, principal owners.Principal) error {
	if err := s.repo.DeleteAllFailuresForPrincipal(ctx, principal); err != nil {
		return fmt.Errorf("clear failure state: delete failures: %w", err)
	}
	if err := s.repo.DeleteLockout(ctx, principal); err != nil {
		return fmt.Errorf("clear failure state: delete lockout: %w", err)
	}
	return nil
}

// ActiveLockoutUntil returns the locked_until time and true if there is a
// currently active lockout for principal. Returns (zero, false, nil) when no
// active lockout exists. Returns a non-nil error only on DB failure.
// Used by HTTP handlers to supply a Retry-After header on 429 responses.
func (s *Service) ActiveLockoutUntil(
	ctx context.Context, principal owners.Principal,
) (time.Time, bool, error) {
	now := s.now()
	lo, err := s.repo.GetLockout(ctx, principal)
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, fmt.Errorf("active lockout until: %w", err)
	}
	if lo.LockedUntil.After(now) {
		return lo.LockedUntil, true, nil
	}
	return time.Time{}, false, nil
}

// LookupSession is the read-only session lookup used by the unlock-cookie
// middleware. It returns errs.ErrNotFound for any miss (unknown token,
// expired, revoked) so the middleware can swallow uniformly.
func (s *Service) LookupSession(ctx context.Context, sha []byte, now time.Time) (Session, error) {
	sess, err := s.repo.LookupActiveSession(ctx, sha, now)
	if err != nil {
		return Session{}, fmt.Errorf("lookup session: %w", err)
	}
	return *sess, nil
}

// outcomeFor returns a log-friendly outcome string for known sentinel errors.
func outcomeFor(err error) string {
	switch {
	case errors.Is(err, errs.ErrLockedOut):
		return "locked"
	case errors.Is(err, errs.ErrPermissionDenied):
		return "denied"
	case errors.Is(err, errs.ErrHiddenNotConfigured):
		return "not_configured"
	default:
		return "error"
	}
}
