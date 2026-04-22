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
// error falls through to 500 Internal Server Error.
func Translate(err error) huma.StatusError {
	switch {
	case errors.Is(err, errs.ErrNotFound):
		return huma.Error404NotFound(err.Error())
	case errors.Is(err, errs.ErrAlreadyExists), errors.Is(err, errs.ErrConcurrentImport):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, errs.ErrInvalidArgument):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, errs.ErrPermissionDenied),
		errors.Is(err, errs.ErrOwnerMismatch),
		errors.Is(err, errs.ErrDirectAccessBlocked):
		return huma.Error403Forbidden(err.Error())
	case errors.Is(err, errs.ErrIdentityMissing):
		return huma.Error401Unauthorized(err.Error())
	case errors.Is(err, errs.ErrBrokerUnavailable):
		return huma.Error503ServiceUnavailable(err.Error())
	default:
		return huma.Error500InternalServerError(err.Error())
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
