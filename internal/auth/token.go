package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"strings"
)

// NewToken returns a new random session token (32 bytes, base64url without
// padding) for the cookie, and the hex SHA-256 of it for the database.
// Only the hash is ever stored, so a database leak does not leak sessions.
func NewToken() (token string, hashHex string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // never fails since Go 1.24
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token)
}

// HashToken returns the hex SHA-256 of a session token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// DeriveKey derives a 32-byte key for one purpose (e.g. "oidc-flow-cookie")
// from a long random server secret, so one stored secret can feed several
// independent keys.
func DeriveKey(secret []byte, purpose string) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("zoom-checker/" + purpose))
	return m.Sum(nil)
}

// randomString returns n random bytes encoded as base64url without padding.
func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// SanitizeNext returns next if it is a safe local path to redirect to after a
// login, otherwise "/". A safe path starts with "/" but not "//" or "/\"
// (both would be scheme-relative URLs in browsers) and contains no control
// characters or backslashes (browsers drop tabs/newlines and treat "\" as "/").
func SanitizeNext(next string) string {
	if next == "" || len(next) > 2048 || next[0] != '/' {
		return "/"
	}
	if len(next) > 1 && (next[1] == '/' || next[1] == '\\') {
		return "/"
	}
	for _, r := range next {
		if r < 0x20 || r == 0x7f || r == '\\' {
			return "/"
		}
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || !strings.HasPrefix(u.Path, "/") {
		return "/"
	}
	return next
}
