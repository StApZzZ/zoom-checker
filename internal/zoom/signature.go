package zoom

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Header names Zoom uses for the request signature.
const (
	HeaderSignature = "x-zm-signature"
	HeaderTimestamp = "x-zm-request-timestamp"
)

const (
	// DefaultMaxAge is used when Verify gets a non-positive maxAge. It has to cover
	// Zoom's retry schedule (+5, +25, +85 min): a 4xx answer stops retries for good.
	DefaultMaxAge = 2 * time.Hour
	// MaxFutureSkew is how far ahead of our clock a request timestamp may be. It
	// is generous on purpose: a validly signed request from the "future" only
	// means our clock is behind, replays are harmless (events are deduplicated),
	// and a 4xx answer makes Zoom drop the event for good.
	MaxFutureSkew = 24 * time.Hour

	signatureVersion = "v0"
	// maxTimestampDigits keeps absurd values away from time arithmetic overflow;
	// unix seconds stay at 10-11 digits for millennia.
	maxTimestampDigits = 12
)

// Verify checks a webhook request the way Zoom signs it:
//
//	x-zm-signature = "v0=" + hex(HMAC-SHA256(secret, "v0:" + x-zm-request-timestamp + ":" + body))
//
// body must be the raw request bytes; re-serialized JSON does not verify.
// The signature is checked first (constant time), then the timestamp (unix seconds)
// must lie in [now-maxAge, now+MaxFutureSkew]; maxAge <= 0 means DefaultMaxAge.
// It applies to every request, endpoint.url_validation included.
//
// Errors wrap ErrMissingHeaders, ErrBadSignature or ErrStale. They never contain
// the secret or the expected signature, so they are safe to log.
func Verify(secret string, h http.Header, body []byte, now time.Time, maxAge time.Duration) error {
	sigHeader := strings.TrimSpace(headerValue(h, HeaderSignature))
	tsHeader := strings.TrimSpace(headerValue(h, HeaderTimestamp))
	if sigHeader == "" || tsHeader == "" {
		return ErrMissingHeaders
	}
	ts, err := parseUnixSeconds(tsHeader)
	if err != nil {
		return fmt.Errorf("%w: %s is not a unix time in seconds", ErrMissingHeaders, HeaderTimestamp)
	}
	if secret == "" {
		// An empty key would let anyone forge a valid HMAC.
		return fmt.Errorf("%w: secret token is not configured", ErrBadSignature)
	}

	version, given, ok := strings.Cut(sigHeader, "=")
	if !ok || version != signatureVersion {
		return fmt.Errorf("%w: unsupported signature format", ErrBadSignature)
	}
	givenMAC, err := hex.DecodeString(given)
	if err != nil || len(givenMAC) != sha256.Size {
		return fmt.Errorf("%w: signature is not a hex SHA-256 HMAC", ErrBadSignature)
	}
	if !hmac.Equal(givenMAC, signatureMAC(secret, tsHeader, body)) {
		return ErrBadSignature
	}

	if maxAge <= 0 {
		maxAge = DefaultMaxAge
	}
	at := time.Unix(ts, 0)
	switch {
	case at.Before(now.Add(-maxAge)):
		return fmt.Errorf("%w: timestamp is %s old (limit %s)", ErrStale, now.Sub(at).Truncate(time.Second), maxAge)
	case at.After(now.Add(MaxFutureSkew)):
		return fmt.Errorf("%w: timestamp is %s in the future (limit %s)", ErrStale, at.Sub(now).Truncate(time.Second), MaxFutureSkew)
	}
	return nil
}

// RequestTime returns the x-zm-request-timestamp of a request (ok=false when it
// is missing or malformed). Compared with the local clock it shows how far the
// server's clock is off Zoom's.
func RequestTime(h http.Header) (time.Time, bool) {
	ts, err := parseUnixSeconds(strings.TrimSpace(headerValue(h, HeaderTimestamp)))
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(ts, 0), true
}

// Sign returns the x-zm-request-timestamp and x-zm-signature header values Zoom
// would send for body at ts. Used by tests and the "simulate" CLI.
func Sign(secret string, ts time.Time, body []byte) (timestampHeader, signatureHeader string) {
	timestampHeader = strconv.FormatInt(ts.Unix(), 10)
	mac := signatureMAC(secret, timestampHeader, body)
	return timestampHeader, signatureVersion + "=" + hex.EncodeToString(mac)
}

// URLValidationResponse is the JSON answer to endpoint.url_validation.
type URLValidationResponse struct {
	PlainToken     string `json:"plainToken"`
	EncryptedToken string `json:"encryptedToken"`
}

// URLValidation builds the answer to Zoom's endpoint.url_validation challenge:
// encryptedToken = hex(HMAC-SHA256(secret, plainToken)).
func URLValidation(secret, plainToken string) URLValidationResponse {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(plainToken))
	return URLValidationResponse{PlainToken: plainToken, EncryptedToken: hex.EncodeToString(m.Sum(nil))}
}

// signatureMAC computes HMAC-SHA256(secret, "v0:"+ts+":"+body) without copying body.
func signatureMAC(secret, ts string, body []byte) []byte {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(signatureVersion + ":" + ts + ":"))
	m.Write(body)
	return m.Sum(nil)
}

// headerValue is h.Get(name) that also finds keys stored without canonicalization
// (a hand-built http.Header{"x-zm-signature": ...}); net/http canonicalizes
// incoming requests, so the fallback scan only runs for such maps.
func headerValue(h http.Header, name string) string {
	if v := h.Get(name); v != "" {
		return v
	}
	for k, vs := range h {
		if len(vs) > 0 && vs[0] != "" && strings.EqualFold(k, name) {
			return vs[0]
		}
	}
	return ""
}

func parseUnixSeconds(s string) (int64, error) {
	if len(s) > maxTimestampDigits {
		return 0, strconv.ErrRange
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, strconv.ErrSyntax
		}
	}
	return strconv.ParseInt(s, 10, 64)
}
