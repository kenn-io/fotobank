package hidden_test

import (
	"bytes"
	"crypto/rand"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/auth/hidden"
	"go.kenn.io/fotobank/internal/errs"
)

func TestValidatePasscodeRejectsEmpty(t *testing.T) {
	require.ErrorIs(t, hidden.ValidatePasscode(""), errs.ErrInvalidArgument)
}

func TestValidatePasscodeAcceptsMinByte(t *testing.T) {
	require.NoError(t, hidden.ValidatePasscode("x"))
}

func TestValidatePasscodeAccepts1024Bytes(t *testing.T) {
	passcode := string(bytes.Repeat([]byte("a"), 1024))
	require.NoError(t, hidden.ValidatePasscode(passcode))
}

func TestValidatePasscodeRejects1025Bytes(t *testing.T) {
	passcode := string(bytes.Repeat([]byte("a"), 1025))
	require.ErrorIs(t, hidden.ValidatePasscode(passcode), errs.ErrInvalidArgument)
}

// Multi-byte UTF-8 rune (3 bytes: €). Ensures byte length is used, not rune count.
func TestValidatePasscodeUsesByteLength(t *testing.T) {
	// 341 × 3 bytes = 1023 bytes — ok
	rune341 := string(bytes.Repeat([]byte("€"), 341))
	require.NoError(t, hidden.ValidatePasscode(rune341))

	// 342 × 3 bytes = 1026 bytes — rejected
	rune342 := string(bytes.Repeat([]byte("€"), 342))
	require.ErrorIs(t, hidden.ValidatePasscode(rune342), errs.ErrInvalidArgument)
}

func TestHashPasscodeProducesEncodedString(t *testing.T) {
	h, err := hidden.HashPasscode("hello")
	require.NoError(t, err)
	require.Contains(t, h, "argon2id$")
}

func TestVerifyPasscodeCorrect(t *testing.T) {
	r := require.New(t)
	h, err := hidden.HashPasscode("secret")
	r.NoError(err)
	ok, err := hidden.VerifyPasscode(h, "secret")
	r.NoError(err)
	r.True(ok)
}

func TestVerifyPasscodeWrong(t *testing.T) {
	r := require.New(t)
	h, err := hidden.HashPasscode("secret")
	r.NoError(err)
	ok, err := hidden.VerifyPasscode(h, "not-secret")
	r.NoError(err)
	r.False(ok)
}

func TestVerifyPasscodeRejectsBadFormat(t *testing.T) {
	_, err := hidden.VerifyPasscode("garbage-not-a-hash", "passcode")
	require.Error(t, err)
}

func TestNewTokenLength(t *testing.T) {
	r := require.New(t)
	raw, sha, err := hidden.NewToken(rand.Reader)
	r.NoError(err)
	r.Len(raw, 43, "32 bytes base64url = 43 chars")
	r.Len(sha, 32, "sha256 is 32 bytes")
}

func TestTokenSHA256Decodes(t *testing.T) {
	r := require.New(t)
	raw, expectedSHA, err := hidden.NewToken(rand.Reader)
	r.NoError(err)

	got, err := hidden.TokenSHA256(raw)
	r.NoError(err)
	r.Equal(expectedSHA, got)
}

func TestTokenSHA256RejectsBadBase64(t *testing.T) {
	_, err := hidden.TokenSHA256("not!valid!")
	require.Error(t, err)
}

// --- CookieConfig tests ---

func TestCookieConfigForProd(t *testing.T) {
	r := require.New(t)
	cfg := hidden.CookieConfigFor(false)
	r.Equal("__Host-fotobank-hidden", cfg.Name)
	r.True(cfg.Secure)
}

func TestCookieConfigForDev(t *testing.T) {
	r := require.New(t)
	cfg := hidden.CookieConfigFor(true)
	r.Equal("fotobank-hidden", cfg.Name)
	r.False(cfg.Secure)
}

func TestIssueCookieProdAttributes(t *testing.T) {
	r := require.New(t)
	cfg := hidden.CookieConfigFor(false)
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(5 * time.Minute)
	c := cfg.IssueCookie("rawtoken", now, expiresAt)

	r.Equal("__Host-fotobank-hidden", c.Name)
	r.Equal("rawtoken", c.Value)
	r.Equal("/", c.Path)
	r.True(c.Secure)
	r.True(c.HttpOnly)
	r.Equal(http.SameSiteStrictMode, c.SameSite)
	r.Equal(300, c.MaxAge)
}

func TestIssueCookieDevAttributes(t *testing.T) {
	r := require.New(t)
	cfg := hidden.CookieConfigFor(true)
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(5 * time.Minute)
	c := cfg.IssueCookie("rawtoken", now, expiresAt)

	r.Equal("fotobank-hidden", c.Name)
	r.False(c.Secure)
	r.True(c.HttpOnly)
	r.Equal(http.SameSiteStrictMode, c.SameSite)
	r.Equal(300, c.MaxAge)
}

func TestIssueCookieMaxAgeMinOne(t *testing.T) {
	// When expiresAt <= now, MaxAge must floor to 1 (not 0 or negative).
	r := require.New(t)
	cfg := hidden.CookieConfigFor(false)
	now := time.Now().UTC()
	c := cfg.IssueCookie("tok", now, now) // zero duration
	r.Equal(1, c.MaxAge)
}

func TestClearCookieAttributes(t *testing.T) {
	r := require.New(t)
	cfg := hidden.CookieConfigFor(false)
	c := cfg.ClearCookie()

	r.Equal("__Host-fotobank-hidden", c.Name)
	r.Empty(c.Value)
	r.Equal("/", c.Path)
	r.True(c.Secure)
	r.True(c.HttpOnly)
	r.Equal(http.SameSiteStrictMode, c.SameSite)
	r.Equal(-1, c.MaxAge)
}
