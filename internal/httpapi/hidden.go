package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/fotobank/internal/auth/hidden"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
)

// --- request / response shapes ---

type hiddenPasscodeInput struct {
	Body struct {
		Passcode string `json:"passcode" minLength:"1" maxLength:"1024" doc:"Passcode (1–1024 bytes UTF-8)."`
	}
}

type hiddenChangeInput struct {
	Body struct {
		OldPasscode string `json:"old_passcode" minLength:"1" maxLength:"1024"`
		NewPasscode string `json:"new_passcode" minLength:"1" maxLength:"1024"`
	}
}

// hidden204Output is used by endpoints that return 204 No Content with no body.
type hidden204Output struct {
	Status int
}

// hiddenUnlockOutput is the 204 response for POST /unlock that also carries
// the Set-Cookie header issued to the browser.
type hiddenUnlockOutput struct {
	Status    int
	SetCookie http.Cookie `header:"Set-Cookie"`
}

// hiddenLockOutput is the 204 response for POST /lock carrying a
// cookie-clearing Set-Cookie header.
type hiddenLockOutput struct {
	Status    int
	SetCookie http.Cookie `header:"Set-Cookie"`
}

type hiddenStateOutput struct {
	Body struct {
		Configured bool       `json:"configured"`
		Unlocked   bool       `json:"unlocked"`
		ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	}
}

// --- registration ---

// registerHiddenAuth registers all /api/v1/auth/hidden/* routes.
// svc may be nil; all handlers return 503 in that case so the OpenAPI
// spec dumper can call buildAPI with empty Deps and still emit the schema.
func registerHiddenAuth(api huma.API, svc *hidden.Service, cookieCfg hidden.CookieConfig) {
	registerHiddenSetup(api, svc)
	registerHiddenChange(api, svc)
	registerHiddenDisable(api, svc)
	registerHiddenUnlock(api, svc, cookieCfg)
	registerHiddenLock(api, svc, cookieCfg)
	registerHiddenState(api, svc)
}

func registerHiddenSetup(api huma.API, svc *hidden.Service) {
	huma.Register(api, huma.Operation{
		OperationID:   "hidden-setup",
		Method:        http.MethodPost,
		Path:          "/api/v1/auth/hidden/setup",
		Summary:       "Configure hidden privacy with an initial passcode",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *hiddenPasscodeInput) (*hidden204Output, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("hidden auth not configured")
		}
		caller, err := callerFromCtx(ctx)
		if err != nil {
			return nil, Translate(err)
		}
		if err := svc.Setup(ctx, caller, in.Body.Passcode); err != nil {
			return nil, Translate(err)
		}
		return &hidden204Output{Status: http.StatusNoContent}, nil
	})
}

func registerHiddenChange(api huma.API, svc *hidden.Service) {
	huma.Register(api, huma.Operation{
		OperationID:   "hidden-change",
		Method:        http.MethodPost,
		Path:          "/api/v1/auth/hidden/change",
		Summary:       "Change the hidden-privacy passcode",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *hiddenChangeInput) (*hidden204Output, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("hidden auth not configured")
		}
		caller, err := callerFromCtx(ctx)
		if err != nil {
			return nil, Translate(err)
		}
		if err := svc.Change(ctx, caller, in.Body.OldPasscode, in.Body.NewPasscode); err != nil {
			return nil, translateHiddenPasscodeError(ctx, svc, caller, err)
		}
		return &hidden204Output{Status: http.StatusNoContent}, nil
	})
}

func registerHiddenDisable(api huma.API, svc *hidden.Service) {
	huma.Register(api, huma.Operation{
		OperationID:   "hidden-disable",
		Method:        http.MethodPost,
		Path:          "/api/v1/auth/hidden/disable",
		Summary:       "Disable hidden privacy (clears passcode and all hidden flags)",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *hiddenPasscodeInput) (*hidden204Output, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("hidden auth not configured")
		}
		caller, err := callerFromCtx(ctx)
		if err != nil {
			return nil, Translate(err)
		}
		if err := svc.Disable(ctx, caller, in.Body.Passcode); err != nil {
			return nil, translateHiddenPasscodeError(ctx, svc, caller, err)
		}
		return &hidden204Output{Status: http.StatusNoContent}, nil
	})
}

func registerHiddenUnlock(api huma.API, svc *hidden.Service, cookieCfg hidden.CookieConfig) {
	huma.Register(api, huma.Operation{
		OperationID:   "hidden-unlock",
		Method:        http.MethodPost,
		Path:          "/api/v1/auth/hidden/unlock",
		Summary:       "Verify passcode and issue a hidden-unlock session cookie",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *hiddenPasscodeInput) (*hiddenUnlockOutput, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("hidden auth not configured")
		}
		caller, err := callerFromCtx(ctx)
		if err != nil {
			return nil, Translate(err)
		}
		rawToken, expiresAt, err := svc.Unlock(ctx, caller, in.Body.Passcode)
		if err != nil {
			return nil, translateHiddenPasscodeError(ctx, svc, caller, err)
		}
		now := time.Now().UTC()
		cookie := cookieCfg.IssueCookie(rawToken, now, expiresAt)
		return &hiddenUnlockOutput{Status: http.StatusNoContent, SetCookie: *cookie}, nil
	})
}

func registerHiddenLock(api huma.API, svc *hidden.Service, cookieCfg hidden.CookieConfig) {
	huma.Register(api, huma.Operation{
		OperationID:   "hidden-lock",
		Method:        http.MethodPost,
		Path:          "/api/v1/auth/hidden/lock",
		Summary:       "Revoke the active hidden-unlock session (idempotent, always 204)",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, _ *struct{}) (*hiddenLockOutput, error) {
		clearCookie := cookieCfg.ClearCookie()
		if svc != nil {
			// Best-effort: revoke the token server-side if the cookie was present.
			// WithHiddenUnlock stored the raw value in context regardless of session
			// validity so we can attempt revocation even for stale tokens.
			if rawToken, ok := RawHiddenTokenFromContext(ctx); ok {
				_ = svc.Lock(ctx, rawToken)
			}
		}
		return &hiddenLockOutput{Status: http.StatusNoContent, SetCookie: *clearCookie}, nil
	})
}

func registerHiddenState(api huma.API, svc *hidden.Service) {
	huma.Register(api, huma.Operation{
		OperationID: "hidden-state",
		Method:      http.MethodGet,
		Path:        "/api/v1/auth/hidden/state",
		Summary:     "Return current hidden-privacy configuration and unlock state",
	}, func(ctx context.Context, _ *struct{}) (*hiddenStateOutput, error) {
		if svc == nil {
			return &hiddenStateOutput{}, nil
		}
		caller, err := callerFromCtx(ctx)
		if err != nil {
			return nil, Translate(err)
		}
		configured := false
		if _, credErr := svc.GetCredential(ctx, caller); credErr == nil {
			configured = true
		} else if !errors.Is(credErr, errs.ErrNotFound) {
			return nil, Translate(credErr)
		}

		out := &hiddenStateOutput{}
		out.Body.Configured = configured

		claim, hasClaim := hidden.UnlockClaimFromContext(ctx)
		if hasClaim && claim.Principal == caller {
			out.Body.Unlocked = true
			out.Body.ExpiresAt = &claim.ExpiresAt
		}
		return out, nil
	})
}

// --- error translation ---

// translateHiddenPasscodeError maps service errors on passcode-bearing endpoints.
// On ErrLockedOut it queries the service for the active lockout time and
// returns a lockedOutError that carries a Retry-After header.
func translateHiddenPasscodeError(
	ctx context.Context,
	svc *hidden.Service,
	caller owners.Principal,
	err error,
) huma.StatusError {
	if !errors.Is(err, errs.ErrLockedOut) {
		return Translate(err)
	}
	retryAfterSecs := 300 // default fallback: 5 min
	if until, active, lErr := svc.ActiveLockoutUntil(ctx, caller); lErr == nil && active {
		secs := int(time.Until(until).Seconds())
		if secs > 0 {
			retryAfterSecs = secs
		}
	}
	return &lockedOutError{retryAfterSecs: retryAfterSecs}
}

// lockedOutError is a huma.StatusError that also implements huma.HeadersError
// so the Retry-After header is emitted on the 429 response before the body.
type lockedOutError struct {
	retryAfterSecs int
}

func (e *lockedOutError) Error() string  { return "passcode rejected" }
func (e *lockedOutError) GetStatus() int { return http.StatusTooManyRequests }

// GetHeaders implements huma.HeadersError so huma sets the header before
// writing the status/body.
func (e *lockedOutError) GetHeaders() http.Header {
	h := make(http.Header)
	h.Set("Retry-After", strconv.Itoa(e.retryAfterSecs))
	return h
}
