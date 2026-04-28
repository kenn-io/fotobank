// Package errs holds cross-cutting sentinel errors that HTTP handlers
// translate to status codes and CLI commands translate to exit codes.
package errs

import "errors"

var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
	// ErrPairedSidecarsExist is the future-contract delete sentinel
	// per F2.2 spec §8.7: a primary cannot be deleted while sidecars
	// reference it via paired_with_id. F2.2 ships the sentinel and the
	// HTTP translator mapping (409 Conflict) but no service path
	// throws it yet — Delete is deferred per the broader roadmap.
	ErrPairedSidecarsExist   = errors.New("paired sidecars exist")
	ErrInvalidArgument       = errors.New("invalid argument")
	ErrPermissionDenied      = errors.New("permission denied")
	ErrOwnerMismatch         = errors.New("owner mismatch")
	ErrConcurrentImport      = errors.New("another import is in progress")
	ErrBrokerUnavailable     = errors.New("broker unavailable")
	ErrIdentityMissing       = errors.New("identity unavailable")
	ErrDirectAccessBlocked   = errors.New("direct access blocked")
	ErrMigrationPrecondition = errors.New("migration precondition failed")
	ErrBadConfiguration      = errors.New("bad configuration")
)
