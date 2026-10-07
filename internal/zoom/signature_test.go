package zoom

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// refHMAC is an independent RFC 2104 HMAC-SHA256 (no crypto/hmac), so the
// known-answer tests do not just compare the production code with itself.
func refHMAC(key, msg []byte) []byte {
	const block = 64
	if len(key) > block {
		sum := sha256.Sum256(key)
		key = sum[:]
	}
	k := make([]byte, block)
	copy(k, key)
	inner := make([]byte, 0, block+len(msg))
	outer := make([]byte, 0, block+sha256.Size)
	for _, b := range k {
		inner = append(inner, b^0x36)
		outer = append(outer, b^0x5c)
	}
	innerSum := sha256.Sum256(append(inner, msg...))
	outerSum := sha256.Sum256(append(outer, innerSum[:]...))
	return outerSum[:]
}

func refSignature(secret, ts string, body []byte) string {
	msg := append([]byte("v0:"+ts+":"), body...)
	return "v0=" + hex.EncodeToString(refHMAC([]byte(secret), msg))
}

// asciiJSON escapes every non-ASCII rune of a JSON text as a JSON \u escape
// (surrogate pairs above the BMP), as some senders do.
func asciiJSON(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < utf8.RuneSelf:
			b.WriteRune(r)
		case r > 0xFFFF:
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(&b, "\\u%04x\\u%04x", hi, lo)
		default:
			fmt.Fprintf(&b, "\\u%04x", r)
		}
	}
	return b.String()
}

func signedHeader(ts, sig string) http.Header {
	h := http.Header{}
	if ts != "" {
		h.Set(HeaderTimestamp, ts)
	}
	if sig != "" {
		h.Set(HeaderSignature, sig)
	}
	return h
}

func readFixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRefHMACMatchesRFC4231(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		key, data []byte
		want      string
	}{
		{"case 2", []byte("Jefe"), []byte("what do ya want for nothing?"),
			"5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843"},
		{"case 6 (key longer than block)", bytes.Repeat([]byte{0xaa}, 131),
			[]byte("Test Using Larger Than Block-Size Key - Hash Key First"),
			"60e431591ee0b67f0d8a26aacbf5b77f8e0bc6213728c5140546040f0ee37f54"},
	}
	for _, tt := range tests {
		if got := hex.EncodeToString(refHMAC(tt.key, tt.data)); got != tt.want {
			t.Errorf("%s: refHMAC = %s, want %s", tt.name, got, tt.want)
		}
	}
}

// Hard-coded vector, computed independently with Python hmac and openssl:
//
//	printf '%s' 'v0:1739923528:{"event":"endpoint.url_validation"}' | openssl dgst -sha256 -hmac secret_token
const (
	knownSecret = "secret_token"
	knownTS     = "1739923528"
	knownBody   = `{"event":"endpoint.url_validation"}`
	knownSig    = "v0=49fcf7b91015963ac5d59f505698fabd2bd5a57baf24d083bef0407eefa4dd49"
)

func TestSignKnownVector(t *testing.T) {
	t.Parallel()
	ts, sig := Sign(knownSecret, time.Unix(1739923528, 0), []byte(knownBody))
	if ts != knownTS || sig != knownSig {
		t.Fatalf("Sign = (%q, %q), want (%q, %q)", ts, sig, knownTS, knownSig)
	}
	// Sub-second precision is dropped: Zoom sends whole seconds.
	if ts2, _ := Sign(knownSecret, time.Unix(1739923528, 999_999_999), nil); ts2 != knownTS {
		t.Fatalf("timestamp header = %q, want %q", ts2, knownTS)
	}
}

func TestVerifyKnownVector(t *testing.T) {
	t.Parallel()
	now := time.Unix(1739923528, 0)
	if err := Verify(knownSecret, signedHeader(knownTS, knownSig), []byte(knownBody), now, 2*time.Hour); err != nil {
		t.Fatalf("Verify known vector: %v", err)
	}
}

func TestSignMatchesReference(t *testing.T) {
	t.Parallel()
	longSecret := strings.Repeat("k", 200) // longer than the SHA-256 block size
	bodies := [][]byte{
		nil,
		[]byte(`{}`),
		[]byte(knownBody),
		[]byte(`{"payload":{"object":{"participant":{"user_name":"Фёдорова Алёна 🦊"}}},"event":"meeting.participant_joined"}`),
		{0x00, 0xff, 0xfe, '\n', '"'},
	}
	for _, secret := range []string{"s", knownSecret, "Zx9_-+/=секрет", longSecret} {
		for i, body := range bodies {
			for _, unix := range []int64{0, 1739923528, 1791277203} {
				ts, sig := Sign(secret, time.Unix(unix, 0), body)
				if want := strconv.FormatInt(unix, 10); ts != want {
					t.Fatalf("ts = %q, want %q", ts, want)
				}
				if want := refSignature(secret, ts, body); sig != want {
					t.Errorf("secret %d chars, body %d, ts %d: Sign = %s, reference = %s", len(secret), i, unix, sig, want)
				}
			}
		}
	}
}

func TestVerify(t *testing.T) {
	t.Parallel()
	now := time.Unix(1791277275, 0)
	body := readFixture(t, "participant_joined.json")
	ts, sig := Sign(knownSecret, now, body)
	tampered := bytes.Clone(body)
	tampered[len(tampered)/2] ^= 0x01
	nextTS := strconv.FormatInt(now.Unix()+1, 10)

	tests := []struct {
		name    string
		secret  string
		header  http.Header
		body    []byte
		wantErr error
	}{
		{"valid", knownSecret, signedHeader(ts, sig), body, nil},
		{"valid, upper-case hex", knownSecret, signedHeader(ts, "v0="+strings.ToUpper(strings.TrimPrefix(sig, "v0="))), body, nil},
		{"valid, padded header values", knownSecret, signedHeader(" "+ts+" ", " "+sig+" "), body, nil},
		{"tampered body", knownSecret, signedHeader(ts, sig), tampered, ErrBadSignature},
		{"body with extra trailing newline", knownSecret, signedHeader(ts, sig), append(bytes.Clone(body), '\n'), ErrBadSignature},
		{"empty body", knownSecret, signedHeader(ts, sig), nil, ErrBadSignature},
		{"tampered timestamp", knownSecret, signedHeader(nextTS, sig), body, ErrBadSignature},
		{"wrong secret", "other_secret", signedHeader(ts, sig), body, ErrBadSignature},
		{"empty secret", "", signedHeader(ts, sig), body, ErrBadSignature},
		{"no version prefix", knownSecret, signedHeader(ts, strings.TrimPrefix(sig, "v0=")), body, ErrBadSignature},
		{"unknown version", knownSecret, signedHeader(ts, "v1="+strings.TrimPrefix(sig, "v0=")), body, ErrBadSignature},
		{"not hex", knownSecret, signedHeader(ts, "v0=zz"+sig[5:]), body, ErrBadSignature},
		{"truncated mac", knownSecret, signedHeader(ts, sig[:len(sig)-2]), body, ErrBadSignature},
		{"empty mac", knownSecret, signedHeader(ts, "v0="), body, ErrBadSignature},
		{"missing both headers", knownSecret, http.Header{}, body, ErrMissingHeaders},
		{"missing signature", knownSecret, signedHeader(ts, ""), body, ErrMissingHeaders},
		{"missing timestamp", knownSecret, signedHeader("", sig), body, ErrMissingHeaders},
		{"blank values", knownSecret, signedHeader("  ", "  "), body, ErrMissingHeaders},
		{"timestamp not a number", knownSecret, signedHeader("abc", sig), body, ErrMissingHeaders},
		{"timestamp with fraction", knownSecret, signedHeader(ts+".5", sig), body, ErrMissingHeaders},
		{"negative timestamp", knownSecret, signedHeader("-"+ts, sig), body, ErrMissingHeaders},
		{"timestamp in milliseconds", knownSecret, signedHeader(ts+"000", sig), body, ErrMissingHeaders},
		{"timestamp overflowing int64", knownSecret, signedHeader("99999999999999999999", sig), body, ErrMissingHeaders},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := Verify(tt.secret, tt.header, tt.body, now, 2*time.Hour)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Verify = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Verify = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestVerifyHeaderCaseInsensitive(t *testing.T) {
	t.Parallel()
	now := time.Unix(1791277275, 0)
	ts, sig := Sign(knownSecret, now, []byte(knownBody))
	// Go canonicalizes header names; Zoom sends them lower-case.
	h := http.Header{}
	h.Set("X-ZM-SIGNATURE", sig)
	h.Set("x-zm-request-timestamp", ts)
	if err := Verify(knownSecret, h, []byte(knownBody), now, time.Hour); err != nil {
		t.Fatalf("Verify = %v", err)
	}

	// A hand-built map skips canonicalization; the lookup still finds the keys.
	raw := http.Header{"x-zm-signature": {sig}, "X-ZM-REQUEST-TIMESTAMP": {ts}}
	if err := Verify(knownSecret, raw, []byte(knownBody), now, time.Hour); err != nil {
		t.Fatalf("non-canonical keys: Verify = %v", err)
	}
	raw = http.Header{"x-zm-signature": {""}, "X-Zm-Request-Timestamp": {ts}}
	if err := Verify(knownSecret, raw, []byte(knownBody), now, time.Hour); !errors.Is(err, ErrMissingHeaders) {
		t.Fatalf("empty non-canonical value: Verify = %v, want ErrMissingHeaders", err)
	}
}

func TestVerifyTimestampWindow(t *testing.T) {
	t.Parallel()
	now := time.Unix(1791277275, 0)
	body := []byte(knownBody)
	tests := []struct {
		name    string
		offset  time.Duration // request timestamp relative to now
		maxAge  time.Duration
		wantErr error
	}{
		{"now", 0, 2 * time.Hour, nil},
		{"exactly maxAge old", -2 * time.Hour, 2 * time.Hour, nil},
		{"one second past maxAge", -2*time.Hour - time.Second, 2 * time.Hour, ErrStale},
		{"Zoom's last retry (+85 min)", -85 * time.Minute, 2 * time.Hour, nil},
		{"day old", -24 * time.Hour, 2 * time.Hour, ErrStale},
		{"our clock 6 min behind", 6 * time.Minute, 2 * time.Hour, nil},
		{"our clock 3 h behind", 3 * time.Hour, 2 * time.Hour, nil},
		{"exactly MaxFutureSkew ahead", MaxFutureSkew, 2 * time.Hour, nil},
		{"one second past MaxFutureSkew", MaxFutureSkew + time.Second, 2 * time.Hour, ErrStale},
		{"far future", 365 * 24 * time.Hour, 2 * time.Hour, ErrStale},
		{"custom maxAge, inside", -10 * time.Minute, 10 * time.Minute, nil},
		{"custom maxAge, outside", -10*time.Minute - time.Second, 10 * time.Minute, ErrStale},
		{"zero maxAge uses default, inside", -DefaultMaxAge, 0, nil},
		{"zero maxAge uses default, outside", -DefaultMaxAge - time.Second, 0, ErrStale},
		{"negative maxAge uses default", -DefaultMaxAge - time.Second, -time.Minute, ErrStale},
		{"unix epoch", -time.Duration(now.Unix()) * time.Second, 2 * time.Hour, ErrStale},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ts, sig := Sign(knownSecret, now.Add(tt.offset), body)
			err := Verify(knownSecret, signedHeader(ts, sig), body, now, tt.maxAge)
			if tt.wantErr == nil && err != nil {
				t.Fatalf("Verify = %v, want nil", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("Verify = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestVerifyErrorsDoNotLeakSecrets(t *testing.T) {
	t.Parallel()
	const secret = "Sup3r-Secret-Token-Value"
	now := time.Unix(1791277275, 0)
	body := []byte(knownBody)
	ts, sig := Sign(secret, now, body)
	expectedHex := strings.TrimPrefix(sig, "v0=")
	staleTS, staleSig := Sign(secret, now.Add(-3*time.Hour), body)

	cases := map[string]error{
		"bad signature": Verify(secret, signedHeader(ts, "v0="+strings.Repeat("0", 64)), body, now, time.Hour),
		"bad format":    Verify(secret, signedHeader(ts, "v0=xyz"), body, now, time.Hour),
		"stale":         Verify(secret, signedHeader(staleTS, staleSig), body, now, time.Hour),
		"missing":       Verify(secret, http.Header{}, body, now, time.Hour),
		"bad timestamp": Verify(secret, signedHeader("x", sig), body, now, time.Hour),
	}
	for name, err := range cases {
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}
		msg := err.Error()
		for _, s := range []string{secret, expectedHex, strings.TrimPrefix(staleSig, "v0=")} {
			if strings.Contains(msg, s) {
				t.Errorf("%s: error %q leaks a secret value", name, msg)
			}
		}
	}
}

// The signature covers the raw bytes. Re-serializing (Zoom's own sample does
// JSON.stringify) changes escaping and key order and breaks non-ASCII names.
func TestVerifyRawBodyNonASCII(t *testing.T) {
	t.Parallel()
	now := time.Unix(1791277510, 0)

	for _, name := range []string{"participant_joined_emoji.json", "participant_left.json"} {
		raw := readFixture(t, name)
		ts, sig := Sign(knownSecret, now, raw)
		if err := Verify(knownSecret, signedHeader(ts, sig), raw, now, time.Hour); err != nil {
			t.Fatalf("%s: raw body: %v", name, err)
		}

		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		reserialized, err := json.Marshal(m) // sorted keys, compact
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(reserialized, raw) {
			t.Fatalf("%s: re-serialization unexpectedly kept the bytes", name)
		}
		if err := Verify(knownSecret, signedHeader(ts, sig), reserialized, now, time.Hour); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("%s: re-serialized body: Verify = %v, want ErrBadSignature", name, err)
		}
	}

	// JSON escapes (\uXXXX) and literal UTF-8 decode to the same name but are different bytes.
	literal := []byte(`{"event":"meeting.participant_joined","payload":{"object":{"uuid":"u","participant":{"user_name":"Иван 🦊","participant_uuid":"p"}}}}`)
	escaped := []byte(asciiJSON(string(literal)))
	for _, b := range escaped {
		if b >= utf8.RuneSelf {
			t.Fatalf("asciiJSON left a non-ASCII byte: %s", escaped)
		}
	}
	ts, sig := Sign(knownSecret, now, escaped)
	if err := Verify(knownSecret, signedHeader(ts, sig), escaped, now, time.Hour); err != nil {
		t.Fatalf("escaped body: %v", err)
	}
	if err := Verify(knownSecret, signedHeader(ts, sig), literal, now, time.Hour); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("literal body with escaped body's signature: Verify = %v, want ErrBadSignature", err)
	}
	e, err := Parse(escaped)
	if err != nil {
		t.Fatal(err)
	}
	if e.Participant.UserName != "Иван 🦊" {
		t.Fatalf("UserName = %q", e.Participant.UserName)
	}
}

func TestURLValidation(t *testing.T) {
	t.Parallel()
	// Computed with: printf '%s' qgg8vlvZRS6UYooatFL8Aw | openssl dgst -sha256 -hmac secret_token
	got := URLValidation(knownSecret, "qgg8vlvZRS6UYooatFL8Aw")
	want := URLValidationResponse{
		PlainToken:     "qgg8vlvZRS6UYooatFL8Aw",
		EncryptedToken: "0abd212d4653d125d6cbf403b3f529f1804ffabd9578aabfd1af0e4b2b1701f2",
	}
	if got != want {
		t.Fatalf("URLValidation = %+v, want %+v", got, want)
	}

	for _, tok := range []string{"", "a", "Zx-_9QwErTy", strings.Repeat("t", 100)} {
		r := URLValidation("another secret", tok)
		if wantEnc := hex.EncodeToString(refHMAC([]byte("another secret"), []byte(tok))); r.EncryptedToken != wantEnc || r.PlainToken != tok {
			t.Errorf("token %q: got %+v, want encrypted %s", tok, r, wantEnc)
		}
	}

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if wantJSON := `{"plainToken":"qgg8vlvZRS6UYooatFL8Aw","encryptedToken":"0abd212d4653d125d6cbf403b3f529f1804ffabd9578aabfd1af0e4b2b1701f2"}`; string(b) != wantJSON {
		t.Fatalf("JSON = %s, want %s", b, wantJSON)
	}
}

// The url_validation fixture is signed like any other request and parses to its plainToken.
func TestURLValidationFixtureFlow(t *testing.T) {
	t.Parallel()
	raw := readFixture(t, "url_validation.json")
	now := time.Unix(1654503850, 0)
	ts, sig := Sign(knownSecret, now, raw)
	if err := Verify(knownSecret, signedHeader(ts, sig), raw, now, time.Hour); err != nil {
		t.Fatal(err)
	}
	e, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if e.Type != EventURLValidation || IsSupported(e.Type) {
		t.Fatalf("Type = %q, supported = %v", e.Type, IsSupported(e.Type))
	}
	if r := URLValidation(knownSecret, e.PlainToken); r.EncryptedToken != "0abd212d4653d125d6cbf403b3f529f1804ffabd9578aabfd1af0e4b2b1701f2" {
		t.Fatalf("EncryptedToken = %s", r.EncryptedToken)
	}
}

func TestRequestTime(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		header string
		want   int64
		ok     bool
	}{
		{"1791277275", 1791277275, true},
		{" 1791277275 ", 1791277275, true},
		{"", 0, false},
		{"17912772.75", 0, false},
		{"9999999999999", 0, false},
	} {
		h := http.Header{}
		if tc.header != "" {
			h.Set(HeaderTimestamp, tc.header)
		}
		got, ok := RequestTime(h)
		if ok != tc.ok || (ok && got.Unix() != tc.want) {
			t.Errorf("RequestTime(%q) = %v, %v; want %d, %v", tc.header, got, ok, tc.want, tc.ok)
		}
	}
}
