package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/auth/hidden"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/httpapi"
	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/testutil"
)

// hiddenAPIFixture wires a real hidden.Service + httpapi.New for
// HTTP-level tests. The identity stub always resolves to fx.owner.
type hiddenAPIFixture struct {
	srv         *httptest.Server
	svc         *hidden.Service
	repo        *hidden.Repo
	owner       owners.Principal
	cookieCfg   hidden.CookieConfig
	devInsecure bool
}

type fakeHiddenMediaPrivacy struct{}

func (f *fakeHiddenMediaPrivacy) ClearAllHiddenForOwner(
	_ context.Context, _ owners.Principal,
) error {
	return nil
}

// trackingMediaPrivacy records whether ClearAllHiddenForOwner was called.
type trackingMediaPrivacy struct {
	called bool
}

func (f *trackingMediaPrivacy) ClearAllHiddenForOwner(
	_ context.Context, _ owners.Principal,
) error {
	f.called = true
	return nil
}

func newHiddenAPIFixture(t *testing.T) hiddenAPIFixture {
	t.Helper()
	return newHiddenAPIFixtureMode(t, true)
}

// newHiddenAPIFixtureMode builds a hiddenAPIFixture with a configurable
// devInsecure flag. Pass devInsecure=false to get a TLS server with prod-mode
// cookies (__Host-fotobank-hidden, Secure=true).
func newHiddenAPIFixtureMode(t *testing.T, devInsecure bool) hiddenAPIFixture {
	t.Helper()
	d := testutil.OpenTestDB(t)
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk", time.Now().UTC(),
	)
	require.NoError(t, err)

	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	svc := hidden.NewService(repo, &fakeHiddenMediaPrivacy{})
	cookieCfg := hidden.CookieConfigFor(devInsecure)
	idp := identity.NewStub(p, "Test User")
	h, err := httpapi.New(httpapi.Deps{
		IdentityProvider:         idp,
		HiddenAuth:               svc,
		DevInsecureHiddenCookies: devInsecure,
	})
	require.NoError(t, err)

	var srv *httptest.Server
	if devInsecure {
		// Plain HTTP is fine for dev-insecure cookies.
		srv = httptest.NewServer(h)
	} else {
		// Prod mode requires TLS so the __Host- prefix and Secure flag are valid.
		srv = httptest.NewTLSServer(h)
	}
	t.Cleanup(srv.Close)
	return hiddenAPIFixture{
		srv:         srv,
		svc:         svc,
		repo:        repo,
		owner:       p,
		cookieCfg:   cookieCfg,
		devInsecure: devInsecure,
	}
}

func postJSON(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	require.NoError(t, err)
	return resp
}

func postJSONWithClient(t *testing.T, client *http.Client, url, body string) *http.Response {
	t.Helper()
	return postJSONWithClientAndCookie(t, client, url, body, nil)
}

func postJSONWithCookie(t *testing.T, url, body string, cookie *http.Cookie) *http.Response {
	t.Helper()
	return postJSONWithClientAndCookie(t, http.DefaultClient, url, body, cookie)
}

func postJSONWithClientAndCookie(
	t *testing.T,
	client *http.Client,
	url, body string,
	cookie *http.Cookie,
) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

func getWithCookie(t *testing.T, url string, cookie *http.Cookie) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// stateURL returns the /state endpoint URL.
func stateURL(fx hiddenAPIFixture) string {
	return fx.srv.URL + "/api/v1/auth/hidden/state"
}

func TestHiddenStateNotConfigured(t *testing.T) {
	r := require.New(t)
	fx := newHiddenAPIFixture(t)

	resp, err := http.Get(stateURL(fx))
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	var got map[string]any
	r.NoError(json.NewDecoder(resp.Body).Decode(&got))
	r.Equal(false, got["configured"])
	r.Equal(false, got["unlocked"])
	r.Nil(got["expires_at"])
}

func TestHiddenSetupReturns204ThenStateConfigured(t *testing.T) {
	r := require.New(t)
	fx := newHiddenAPIFixture(t)

	resp := postJSON(t, fx.srv.URL+"/api/v1/auth/hidden/setup",
		`{"passcode":"correct-horse"}`)
	defer resp.Body.Close()
	r.Equal(http.StatusNoContent, resp.StatusCode)

	// /state should now show configured=true.
	stateResp, err := http.Get(stateURL(fx))
	r.NoError(err)
	defer stateResp.Body.Close()
	var got map[string]any
	r.NoError(json.NewDecoder(stateResp.Body).Decode(&got))
	r.Equal(true, got["configured"])
	r.Equal(false, got["unlocked"])
}

func TestHiddenSetupDuplicateReturns409(t *testing.T) {
	r := require.New(t)
	fx := newHiddenAPIFixture(t)

	resp1 := postJSON(t, fx.srv.URL+"/api/v1/auth/hidden/setup", `{"passcode":"aaa"}`)
	defer resp1.Body.Close()
	r.Equal(http.StatusNoContent, resp1.StatusCode)

	resp2 := postJSON(t, fx.srv.URL+"/api/v1/auth/hidden/setup", `{"passcode":"aaa"}`)
	defer resp2.Body.Close()
	r.Equal(http.StatusConflict, resp2.StatusCode)
}

func TestHiddenSetupEmptyPasscodeReturns400(t *testing.T) {
	r := require.New(t)
	fx := newHiddenAPIFixture(t)

	// Empty passcode — huma minLength:1 enforcement.
	resp := postJSON(t, fx.srv.URL+"/api/v1/auth/hidden/setup", `{"passcode":""}`)
	defer resp.Body.Close()
	r.Equal(http.StatusUnprocessableEntity, resp.StatusCode,
		"empty passcode should be rejected by schema validation (422)")
}

func TestHiddenSetupTooLongPasscodeReturns400(t *testing.T) {
	r := require.New(t)
	fx := newHiddenAPIFixture(t)

	// 1025 bytes — exceeds maxLength:1024.
	longPass := strings.Repeat("a", 1025)
	body, _ := json.Marshal(map[string]string{"passcode": longPass})
	resp, err := http.Post(fx.srv.URL+"/api/v1/auth/hidden/setup",
		"application/json", bytes.NewReader(body))
	r.NoError(err)
	defer resp.Body.Close()
	// huma schema maxLength enforcement returns 422 Unprocessable Entity.
	r.Equal(http.StatusUnprocessableEntity, resp.StatusCode)
}

func TestHiddenUnlockWrongPasscodeReturns403NoCookie(t *testing.T) {
	r := require.New(t)
	fx := newHiddenAPIFixture(t)

	require.NoError(t, fx.svc.Setup(context.Background(), fx.owner, "right"))

	resp := postJSON(t, fx.srv.URL+"/api/v1/auth/hidden/unlock", `{"passcode":"wrong"}`)
	defer resp.Body.Close()
	r.Equal(http.StatusForbidden, resp.StatusCode)

	// No Set-Cookie header.
	for _, c := range resp.Cookies() {
		r.NotEqual(fx.cookieCfg.Name, c.Name, "wrong passcode must not set a cookie")
	}
}

func TestHiddenUnlockCorrectPasscodeReturns204SetsCookie(t *testing.T) {
	r := require.New(t)
	fx := newHiddenAPIFixture(t)

	require.NoError(t, fx.svc.Setup(context.Background(), fx.owner, "correct-horse"))

	resp := postJSON(t, fx.srv.URL+"/api/v1/auth/hidden/unlock",
		`{"passcode":"correct-horse"}`)
	defer resp.Body.Close()
	r.Equal(http.StatusNoContent, resp.StatusCode)

	// Must set the unlock cookie.
	var unlockCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == fx.cookieCfg.Name {
			unlockCookie = c
		}
	}
	r.NotNil(unlockCookie, "unlock cookie must be set on 204")
	r.NotEmpty(unlockCookie.Value)
	r.True(unlockCookie.HttpOnly)
	r.Equal("/", unlockCookie.Path)
	r.Equal(http.SameSiteStrictMode, unlockCookie.SameSite)
}

func TestHiddenStateAfterUnlockShowsUnlockedWithExpiry(t *testing.T) {
	r := require.New(t)
	fx := newHiddenAPIFixture(t)
	require.NoError(t, fx.svc.Setup(context.Background(), fx.owner, "correct-horse"))

	unlockResp := postJSON(t, fx.srv.URL+"/api/v1/auth/hidden/unlock",
		`{"passcode":"correct-horse"}`)
	defer unlockResp.Body.Close()
	r.Equal(http.StatusNoContent, unlockResp.StatusCode)

	// Find the cookie.
	var unlockCookie *http.Cookie
	for _, c := range unlockResp.Cookies() {
		if c.Name == fx.cookieCfg.Name {
			unlockCookie = c
		}
	}
	r.NotNil(unlockCookie)

	// GET /state with cookie.
	stateResp := getWithCookie(t, stateURL(fx), unlockCookie)
	defer stateResp.Body.Close()
	r.Equal(http.StatusOK, stateResp.StatusCode)

	var got map[string]any
	r.NoError(json.NewDecoder(stateResp.Body).Decode(&got))
	r.Equal(true, got["configured"])
	r.Equal(true, got["unlocked"])
	r.NotNil(got["expires_at"], "expires_at must be present when unlocked")
}

func TestHiddenChangeWithCorrectOldReturns204(t *testing.T) {
	r := require.New(t)
	fx := newHiddenAPIFixture(t)
	require.NoError(t, fx.svc.Setup(context.Background(), fx.owner, "old-pass"))

	body, _ := json.Marshal(map[string]string{
		"old_passcode": "old-pass",
		"new_passcode": "new-pass",
	})
	resp, err := http.Post(fx.srv.URL+"/api/v1/auth/hidden/change",
		"application/json", bytes.NewReader(body))
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusNoContent, resp.StatusCode)

	// Old passcode should now be rejected.
	wrongResp := postJSON(t, fx.srv.URL+"/api/v1/auth/hidden/unlock",
		`{"passcode":"old-pass"}`)
	defer wrongResp.Body.Close()
	r.Equal(http.StatusForbidden, wrongResp.StatusCode)
}

func TestHiddenDisableClearsCredential(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk", time.Now().UTC(),
	)
	require.NoError(t, err)

	tracker := &trackingMediaPrivacy{}
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	svc := hidden.NewService(repo, tracker)
	idp := identity.NewStub(p, "Test User")
	h, err := httpapi.New(httpapi.Deps{
		IdentityProvider:         idp,
		HiddenAuth:               svc,
		DevInsecureHiddenCookies: true,
	})
	r.NoError(err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	require.NoError(t, svc.Setup(context.Background(), p, "my-pass"))

	resp := postJSON(t, srv.URL+"/api/v1/auth/hidden/disable", `{"passcode":"my-pass"}`)
	defer resp.Body.Close()
	r.Equal(http.StatusNoContent, resp.StatusCode)

	// ClearAllHiddenForOwner should have been called.
	r.True(tracker.called, "Disable must call ClearAllHiddenForOwner")

	// /state should now show not-configured.
	stateResp, err := http.Get(srv.URL + "/api/v1/auth/hidden/state")
	r.NoError(err)
	defer stateResp.Body.Close()
	var got map[string]any
	r.NoError(json.NewDecoder(stateResp.Body).Decode(&got))
	r.Equal(false, got["configured"])
}

func TestHiddenLockAlwaysReturns204ClearsCookie(t *testing.T) {
	r := require.New(t)
	fx := newHiddenAPIFixture(t)

	// POST /lock with no setup or cookie — must still return 204.
	resp, err := http.Post(fx.srv.URL+"/api/v1/auth/hidden/lock",
		"application/json", nil)
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusNoContent, resp.StatusCode)

	// The Set-Cookie header should clear the cookie (MaxAge <= 0).
	var clearCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == fx.cookieCfg.Name {
			clearCookie = c
		}
	}
	r.NotNil(clearCookie, "lock must emit a cookie-clearing Set-Cookie header")
	r.True(clearCookie.MaxAge < 0 || clearCookie.Value == "",
		"clearing cookie should have MaxAge<0 or empty value")
}

func TestHiddenLockRevokesCookieSession(t *testing.T) {
	r := require.New(t)
	fx := newHiddenAPIFixture(t)
	require.NoError(t, fx.svc.Setup(context.Background(), fx.owner, "pass"))

	// Unlock.
	unlockResp := postJSON(t, fx.srv.URL+"/api/v1/auth/hidden/unlock", `{"passcode":"pass"}`)
	defer unlockResp.Body.Close()
	r.Equal(http.StatusNoContent, unlockResp.StatusCode)
	var unlockCookie *http.Cookie
	for _, c := range unlockResp.Cookies() {
		if c.Name == fx.cookieCfg.Name {
			unlockCookie = c
		}
	}
	r.NotNil(unlockCookie)

	// Lock with the cookie.
	lockResp := postJSONWithCookie(t, fx.srv.URL+"/api/v1/auth/hidden/lock", "", unlockCookie)
	defer lockResp.Body.Close()
	r.Equal(http.StatusNoContent, lockResp.StatusCode)

	// /state with old cookie should show unlocked=false.
	stateResp := getWithCookie(t, stateURL(fx), unlockCookie)
	defer stateResp.Body.Close()
	var got map[string]any
	r.NoError(json.NewDecoder(stateResp.Body).Decode(&got))
	r.Equal(false, got["unlocked"])
}

func TestHiddenLockoutAfterFiveWrongAttempts(t *testing.T) {
	r := require.New(t)
	fx := newHiddenAPIFixture(t)
	require.NoError(t, fx.svc.Setup(context.Background(), fx.owner, "right"))

	// Use a short lockout window so we control timing without sleeping.
	fx.svc.SetLockoutForTest(time.Hour, 5*time.Minute, 5)

	for i := range 5 {
		resp := postJSON(t, fx.srv.URL+"/api/v1/auth/hidden/unlock",
			fmt.Sprintf(`{"passcode":"wrong%d"}`, i))
		resp.Body.Close()
	}

	// 6th attempt should get 429.
	resp := postJSON(t, fx.srv.URL+"/api/v1/auth/hidden/unlock",
		`{"passcode":"wrongagain"}`)
	defer resp.Body.Close()
	r.Equal(http.StatusTooManyRequests, resp.StatusCode)

	retryAfter := resp.Header.Get("Retry-After")
	r.NotEmpty(retryAfter, "429 must include Retry-After header")
}

// TestHiddenTranslateNewSentinels ensures errors_test coverage is
// complemented by verifying that the handler layer emits the right codes.
func TestHiddenTranslateErrLockedOut(t *testing.T) {
	r := require.New(t)
	got := httpapi.Translate(errs.ErrLockedOut)
	r.NotNil(got)
	r.Equal(429, got.GetStatus())
}

func TestHiddenTranslateErrHiddenNotConfigured(t *testing.T) {
	r := require.New(t)
	got := httpapi.Translate(errs.ErrHiddenNotConfigured)
	r.NotNil(got)
	r.Equal(409, got.GetStatus())
}

// TestHiddenLockIdempotentOnRevokedCookie verifies that /lock returns 204 even
// when the supplied cookie's session was already revoked by a prior /lock call.
func TestHiddenLockIdempotentOnRevokedCookie(t *testing.T) {
	r := require.New(t)
	fx := newHiddenAPIFixture(t)
	require.NoError(t, fx.svc.Setup(context.Background(), fx.owner, "pass"))

	// Unlock to obtain a valid cookie.
	unlockResp := postJSON(t, fx.srv.URL+"/api/v1/auth/hidden/unlock", `{"passcode":"pass"}`)
	defer unlockResp.Body.Close()
	r.Equal(http.StatusNoContent, unlockResp.StatusCode)
	var unlockCookie *http.Cookie
	for _, c := range unlockResp.Cookies() {
		if c.Name == fx.cookieCfg.Name {
			unlockCookie = c
		}
	}
	r.NotNil(unlockCookie)

	// First /lock with the cookie — revokes the session.
	lock1 := postJSONWithCookie(t, fx.srv.URL+"/api/v1/auth/hidden/lock", "", unlockCookie)
	defer lock1.Body.Close()
	r.Equal(http.StatusNoContent, lock1.StatusCode)

	// Second /lock with the now-revoked cookie — must still return 204.
	lock2 := postJSONWithCookie(t, fx.srv.URL+"/api/v1/auth/hidden/lock", "", unlockCookie)
	defer lock2.Body.Close()
	r.Equal(http.StatusNoContent, lock2.StatusCode)
}

// TestHiddenStateDBErrorReturns5xx verifies that /state propagates a real DB
// error (non-ErrNotFound) rather than silently reporting configured=false.
func TestHiddenStateDBErrorReturns5xx(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)

	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	svc := hidden.NewService(repo, &fakeHiddenMediaPrivacy{})
	idp := identity.NewStub(p, "Test User")
	h, err := httpapi.New(httpapi.Deps{
		IdentityProvider:         idp,
		HiddenAuth:               svc,
		DevInsecureHiddenCookies: true,
	})
	r.NoError(err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// Close the read pool so GetCredential fails with a real DB error, not ErrNotFound.
	r.NoError(d.ReadDB().Close())

	resp, getErr := http.Get(srv.URL + "/api/v1/auth/hidden/state")
	r.NoError(getErr)
	defer resp.Body.Close()
	// Must not return 200 with configured=false — must bubble as a 5xx.
	r.NotEqual(http.StatusOK, resp.StatusCode,
		"/state must not return 200 when the DB fails with a real error")
}

// TestHiddenLockReturns204WhenRevokeSessionFails verifies that the /lock
// endpoint preserves its idempotency contract (always 204) even when the
// underlying DB revocation fails.
func TestHiddenLockReturns204WhenRevokeSessionFails(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)

	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	svc := hidden.NewService(repo, &fakeHiddenMediaPrivacy{})
	cookieCfg := hidden.CookieConfigFor(true)
	idp := identity.NewStub(p, "Test User")
	h, err := httpapi.New(httpapi.Deps{
		IdentityProvider:         idp,
		HiddenAuth:               svc,
		DevInsecureHiddenCookies: true,
	})
	r.NoError(err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	r.NoError(svc.Setup(context.Background(), p, "pass"))

	// Unlock to get a valid cookie.
	unlockResp := postJSON(t, srv.URL+"/api/v1/auth/hidden/unlock", `{"passcode":"pass"}`)
	defer unlockResp.Body.Close()
	r.Equal(http.StatusNoContent, unlockResp.StatusCode)
	var unlockCookie *http.Cookie
	for _, c := range unlockResp.Cookies() {
		if c.Name == cookieCfg.Name {
			unlockCookie = c
		}
	}
	r.NotNil(unlockCookie)

	// Close the write pool so RevokeSession fails with a real DB error.
	r.NoError(d.WriteDB().Close())

	// /lock must still return 204 (idempotent, best-effort revocation).
	lockResp := postJSONWithCookie(t, srv.URL+"/api/v1/auth/hidden/lock", "", unlockCookie)
	defer lockResp.Body.Close()
	r.Equal(http.StatusNoContent, lockResp.StatusCode)
}

// TestHiddenProdCookieNameAndSecureFlag verifies that in production mode the
// handler sets the __Host-fotobank-hidden cookie with Secure=true.
func TestHiddenProdCookieNameAndSecureFlag(t *testing.T) {
	r := require.New(t)
	fx := newHiddenAPIFixtureMode(t, false) // prod mode: TLS server
	require.NoError(t, fx.svc.Setup(context.Background(), fx.owner, "prod-pass"))

	// Use the TLS server's own client so the self-signed cert is trusted.
	tlsClient := fx.srv.Client()
	resp := postJSONWithClient(t, tlsClient,
		fx.srv.URL+"/api/v1/auth/hidden/unlock",
		`{"passcode":"prod-pass"}`)
	defer resp.Body.Close()
	r.Equal(http.StatusNoContent, resp.StatusCode)

	var prodCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == fx.cookieCfg.Name {
			prodCookie = c
		}
	}
	r.NotNil(prodCookie, "prod unlock cookie must be present in response")
	r.Equal("__Host-fotobank-hidden", prodCookie.Name)
	r.True(prodCookie.Secure, "prod cookie must have Secure=true")
}
