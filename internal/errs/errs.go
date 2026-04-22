// Package errs holds cross-cutting sentinel errors that HTTP handlers
// translate to status codes and CLI commands translate to exit codes.
package errs

import "errors"

var (
	ErrNotFound              = errors.New("not found")
	ErrAlreadyExists         = errors.New("already exists")
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
