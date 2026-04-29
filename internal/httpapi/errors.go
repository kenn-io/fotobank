package httpapi

import (
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/fotobank/internal/errs"
)

// Translate converts a domain error into a huma.StatusError with the
// HTTP status code that best describes the failure. Sentinel errors
// from internal/errs map to their documented status codes; any other
// error falls through to 500 Internal Server Error. The response body
// never echoes err.Error() — a wrapped sentinel could carry internal
// details (e.g. fmt.Errorf("db path /srv/...: %w", ErrNotFound)) that
// errors.Is still matches, so we only return the sentinel's own
// message (for known sentinels) or http.StatusText (for the default
// path). Callers must log the original err server-side. Returns nil
// when err is nil.
func Translate(err error) huma.StatusError {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, errs.ErrNotFound):
		return huma.Error404NotFound(errs.ErrNotFound.Error())
	case errors.Is(err, errs.ErrAlreadyExists):
		return huma.Error409Conflict(errs.ErrAlreadyExists.Error())
	case errors.Is(err, errs.ErrConcurrentImport):
		return huma.Error409Conflict(errs.ErrConcurrentImport.Error())
	case errors.Is(err, errs.ErrPairedSidecarsExist):
		return huma.Error409Conflict(errs.ErrPairedSidecarsExist.Error())
	case errors.Is(err, errs.ErrInvalidArgument):
		return huma.Error400BadRequest(errs.ErrInvalidArgument.Error())
	case errors.Is(err, errs.ErrPermissionDenied):
		return huma.Error403Forbidden(errs.ErrPermissionDenied.Error())
	case errors.Is(err, errs.ErrOwnerMismatch):
		return huma.Error403Forbidden(errs.ErrOwnerMismatch.Error())
	case errors.Is(err, errs.ErrDirectAccessBlocked):
		return huma.Error403Forbidden(errs.ErrDirectAccessBlocked.Error())
	case errors.Is(err, errs.ErrIdentityMissing):
		return huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
	case errors.Is(err, errs.ErrBrokerUnavailable):
		return huma.Error503ServiceUnavailable(errs.ErrBrokerUnavailable.Error())
	case errors.Is(err, errs.ErrLockedOut):
		// 429: body is deliberately generic (same wording as wrong-passcode)
		// so lockout vs. rejection is not trivially distinguishable.
		// The Retry-After header is set by the handler that detects lockout.
		return huma.Error429TooManyRequests("passcode rejected")
	case errors.Is(err, errs.ErrHiddenNotConfigured):
		return huma.Error409Conflict("hidden not configured")
	default:
		return huma.Error500InternalServerError(http.StatusText(http.StatusInternalServerError))
	}
}

// StatusFrom extracts the numeric HTTP status code from a huma.StatusError,
// returning 200 OK when err is nil.
func StatusFrom(err huma.StatusError) int {
	if err == nil {
		return http.StatusOK
	}
	return err.GetStatus()
}
