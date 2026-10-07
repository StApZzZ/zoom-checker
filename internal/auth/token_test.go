package auth

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func TestNewToken(t *testing.T) {
	t.Parallel()
	tok, hash := NewToken()
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil || len(raw) != 32 {
		t.Fatalf("token %q: decoded %d bytes, err %v", tok, len(raw), err)
	}
	if hash != HashToken(tok) {
		t.Fatal("hash must equal HashToken(token)")
	}
	if b, err := hex.DecodeString(hash); err != nil || len(b) != 32 {
		t.Fatalf("hash %q is not hex sha256", hash)
	}
	tok2, hash2 := NewToken()
	if tok2 == tok || hash2 == hash {
		t.Fatal("tokens must be random")
	}
}

func TestHashTokenVector(t *testing.T) {
	t.Parallel()
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" // sha256("abc")
	if got := HashToken("abc"); got != want {
		t.Fatalf("HashToken(abc) = %s", got)
	}
}

func TestDeriveKey(t *testing.T) {
	t.Parallel()
	secret := []byte("server-secret")
	a := DeriveKey(secret, "oidc-flow-cookie")
	if len(a) != 32 || !bytes.Equal(a, DeriveKey(secret, "oidc-flow-cookie")) {
		t.Fatal("DeriveKey must return 32 deterministic bytes")
	}
	if bytes.Equal(a, DeriveKey(secret, "other")) || bytes.Equal(a, DeriveKey([]byte("x"), "oidc-flow-cookie")) {
		t.Fatal("DeriveKey must depend on purpose and secret")
	}
}

func TestSanitizeNext(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":                               "/",
		"/":                              "/",
		"/lessons/5":                     "/lessons/5",
		"/lessons/5?tab=log#top":         "/lessons/5?tab=log#top",
		"/groups/%2F%2Fevil.com":         "/groups/%2F%2Fevil.com",
		"//evil.com":                     "/",
		"///evil.com":                    "/",
		"/\\evil.com":                    "/",
		"/a\\b":                          "/",
		"\\\\evil.com":                   "/",
		"https://evil.com":               "/",
		"http:/evil.com":                 "/",
		"javascript:alert(1)":            "/",
		"evil.com":                       "/",
		"lessons/5":                      "/",
		"/\t/evil.com":                   "/",
		"/\n/evil.com":                   "/",
		"/\x00":                          "/",
		" /lessons":                      "/",
		"/%zz":                           "/",
		"/" + string(make([]byte, 3000)): "/",
	}
	for in, want := range cases {
		if got := SanitizeNext(in); got != want {
			t.Errorf("SanitizeNext(%q) = %q, want %q", in, got, want)
		}
	}
}
