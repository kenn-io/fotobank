package hidden

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
)

const (
	prodCookieName = "__Host-fotobank-hidden"
	devCookieName  = "fotobank-hidden"
)

// CookieConfig captures the deployment-mode-dependent cookie attributes.
// Production: __Host-fotobank-hidden, Secure. Dev: fotobank-hidden, no Secure.
type CookieConfig struct {
	Name   string
	Secure bool
}

// CookieConfigFor returns the right config for the deployment mode.
// devInsecure=true means the operator opted into HTTP-loopback dev cookies via
// http.dev_insecure_cookies; the __Host- prefix and Secure flag are dropped.
func CookieConfigFor(devInsecure bool) CookieConfig {
	if devInsecure {
		return CookieConfig{Name: devCookieName, Secure: false}
	}
	return CookieConfig{Name: prodCookieName, Secure: true}
}

// IssueCookie builds a *http.Cookie carrying the raw token. Max-Age is
// derived from expiresAt - now (rounded down to whole seconds, min 1).
func (c CookieConfig) IssueCookie(rawToken string, now, expiresAt time.Time) *http.Cookie {
	secs := max(int(expiresAt.Sub(now)/time.Second), 1)
	return &http.Cookie{
		Name:     c.Name,
		Value:    rawToken,
		Path:     "/",
		Secure:   c.Secure,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   secs,
	}
}

// ClearCookie returns a Set-Cookie that immediately expires the unlock cookie.
// Max-Age=-1 deletes; Value="" zeroes the payload as defense in depth.
func (c CookieConfig) ClearCookie() *http.Cookie {
	return &http.Cookie{
		Name:     c.Name,
		Value:    "",
		Path:     "/",
		Secure:   c.Secure,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	}
}

// UnlockClaim is attached to the request context when the hidden-unlock cookie
// is present, valid, and references an active session matching the caller.
type UnlockClaim struct {
	Principal owners.Principal
	ExpiresAt time.Time
}

type unlockClaimKey struct{}

// WithUnlockClaim returns a derived context carrying claim.
func WithUnlockClaim(ctx context.Context, claim UnlockClaim) context.Context {
	return context.WithValue(ctx, unlockClaimKey{}, claim)
}

// UnlockClaimFromContext returns the UnlockClaim attached to ctx, if any.
// The second return value reports whether a claim was present.
func UnlockClaimFromContext(ctx context.Context) (UnlockClaim, bool) {
	c, ok := ctx.Value(unlockClaimKey{}).(UnlockClaim)
	return c, ok
}

// Argon2id parameters are intentionally fixed rather than operator-configurable.
const (
	argon2Memory     = 19456 // KiB
	argon2Time       = 2
	argon2Threads    = 1
	argon2HashLen    = 32
	argon2SaltLen    = 16
	passcodeMinBytes = 1
	passcodeMaxBytes = 1024
	tokenRawBytes    = 32
)

// ValidatePasscode enforces 1 <= len([]byte(passcode)) <= 1024.
// Returns a wrapped ErrInvalidArgument on violation.
func ValidatePasscode(passcode string) error {
	n := len([]byte(passcode))
	if n < passcodeMinBytes || n > passcodeMaxBytes {
		return fmt.Errorf("%w: passcode must be 1–1024 bytes, got %d", errs.ErrInvalidArgument, n)
	}
	return nil
}

// HashPasscode derives an Argon2id hash from passcode and returns the
// encoded string in the form:
//
//	argon2id$m=19456,t=2,p=1$<base64url-salt>$<base64url-hash>
//
// A fresh 16-byte salt is drawn from crypto/rand on every call.
func HashPasscode(passcode string) (string, error) {
	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("hash passcode: generate salt: %w", err)
	}
	hash := argon2.IDKey([]byte(passcode), salt, argon2Time, argon2Memory, argon2Threads, argon2HashLen)
	return encodeArgon2(salt, hash), nil
}

// VerifyPasscode decodes encoded and recomputes the hash for passcode,
// returning (true, nil) on match, (false, nil) on mismatch, and
// (false, error) on parse failure or unsupported version.
func VerifyPasscode(encoded, passcode string) (bool, error) {
	salt, expectedHash, err := decodeArgon2(encoded)
	if err != nil {
		return false, fmt.Errorf("verify passcode: %w", err)
	}
	candidate := argon2.IDKey([]byte(passcode), salt, argon2Time, argon2Memory, argon2Threads, argon2HashLen)
	if subtle.ConstantTimeCompare(candidate, expectedHash) != 1 {
		return false, nil
	}
	return true, nil
}

// NewToken generates a random session token.
//
// Returns:
//   - raw: base64url-encoded 32 random bytes (43 chars, no padding).
//   - sha: SHA-256 of the raw bytes (32 bytes); store this in the DB.
//   - err: non-nil only if r cannot provide 32 bytes.
func NewToken(r io.Reader) (raw string, sha []byte, err error) {
	buf := make([]byte, tokenRawBytes)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", nil, fmt.Errorf("new token: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256(buf)
	return raw, sum[:], nil
}

// TokenSHA256 decodes raw (a base64url token string) and returns its
// SHA-256. Returns an error if raw is not valid base64url.
func TokenSHA256(raw string) ([]byte, error) {
	buf, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("token sha256: decode: %w", err)
	}
	sum := sha256.Sum256(buf)
	return sum[:], nil
}

// encodeArgon2 serialises salt and hash into the canonical encoded format.
func encodeArgon2(salt, hash []byte) string {
	return fmt.Sprintf("argon2id$m=%d,t=%d,p=%d$%s$%s",
		argon2Memory, argon2Time, argon2Threads,
		base64.RawURLEncoding.EncodeToString(salt),
		base64.RawURLEncoding.EncodeToString(hash),
	)
}

// decodeArgon2 parses the encoded string produced by encodeArgon2.
// Returns an error if the string has an unexpected format or variant.
func decodeArgon2(encoded string) (salt, hash []byte, err error) {
	if !strings.HasPrefix(encoded, "argon2id$") {
		return nil, nil, fmt.Errorf("unsupported hash variant or format")
	}
	// Expected format: argon2id$m=...,t=...,p=...$<salt>$<hash>
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 {
		return nil, nil, fmt.Errorf("malformed hash: expected 4 dollar-separated parts")
	}
	if parts[0] != "argon2id" {
		return nil, nil, fmt.Errorf("unsupported hash variant: %s", parts[0])
	}
	if parts[1] != fmt.Sprintf("m=%d,t=%d,p=%d", argon2Memory, argon2Time, argon2Threads) {
		return nil, nil, fmt.Errorf("unsupported argon2id parameters: %s", parts[1])
	}
	salt, err = base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, nil, fmt.Errorf("decode salt: %w", err)
	}
	hash, err = base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return nil, nil, fmt.Errorf("decode hash: %w", err)
	}
	return salt, hash, nil
}
