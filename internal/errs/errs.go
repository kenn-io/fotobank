// Package errs holds cross-cutting sentinel errors that HTTP handlers
// translate to status codes and CLI commands translate to exit codes.
package errs

import "errors"

var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
	// ErrPairedSidecarsExist prevents deleting a primary while sidecars
	// reference it through paired_with_id. HTTP maps it to 409 Conflict.
	ErrPairedSidecarsExist     = errors.New("paired sidecars exist")
	ErrInvalidArgument         = errors.New("invalid argument")
	ErrPermissionDenied        = errors.New("permission denied")
	ErrOwnerMismatch           = errors.New("owner mismatch")
	ErrConcurrentImport        = errors.New("another import is in progress")
	ErrBrokerUnavailable       = errors.New("broker unavailable")
	ErrIdentityMissing         = errors.New("identity unavailable")
	ErrDirectAccessBlocked     = errors.New("direct access blocked")
	ErrMigrationPrecondition   = errors.New("migration precondition failed")
	ErrBadConfiguration        = errors.New("bad configuration")
	ErrContentConflict         = errors.New("content conflict")
	ErrContentIdentityMismatch = errors.New("content identity mismatch")
	ErrContentUnavailable      = errors.New("content unavailable")
	// ErrLockedOut is returned by the hidden-auth service when the principal
	// has exceeded the failure threshold. HTTP maps it to 429 with Retry-After.
	ErrLockedOut = errors.New("locked out")
	// ErrHiddenNotConfigured is returned when a hidden-auth operation is
	// requested but no credential row exists for the principal.
	// HTTP maps it to 409 on POST /media:hidden.
	ErrHiddenNotConfigured = errors.New("hidden not configured")
	// ErrAcknowledgementRequired is returned by AI write operations when
	// the caller's principal has not yet acknowledged hidden-photo
	// processing. HTTP maps it to 409 Conflict so the UI can prompt for
	// the gate before retrying.
	ErrAcknowledgementRequired = errors.New("acknowledgement required")
)
