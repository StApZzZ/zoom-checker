package auth

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

func TestHashAndVerifyPassword(t *testing.T) {
	t.Parallel()
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected PHC prefix: %s", h)
	}
	if parts := strings.Split(h, "$"); len(parts) != 6 || len(parts[4]) != 22 || len(parts[5]) != 43 {
		t.Fatalf("unexpected PHC layout (16-byte salt, 32-byte key): %s", h)
	}
	ok, err := VerifyPassword(h, "correct horse battery")
	if err != nil || !ok {
		t.Fatalf("VerifyPassword(correct) = %v, %v", ok, err)
	}
	ok, err = VerifyPassword(h, "correct horse batterY")
	if err != nil || ok {
		t.Fatalf("VerifyPassword(wrong) = %v, %v", ok, err)
	}
	h2, _ := HashPassword("correct horse battery")
	if h2 == h {
		t.Fatal("two hashes of the same password must differ (random salt)")
	}
	if NeedsRehash(h) {
		t.Fatal("fresh hash must not need a rehash")
	}
}

func TestVerifyPasswordForeignParameters(t *testing.T) {
	t.Parallel()
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte("пароль-123"), salt, 3, 65536, 4, 32)
	h := fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=4$%s$%s", b64.EncodeToString(salt), b64.EncodeToString(key))
	ok, err := VerifyPassword(h, "пароль-123")
	if err != nil || !ok {
		t.Fatalf("VerifyPassword with other params = %v, %v", ok, err)
	}
	if !NeedsRehash(h) {
		t.Fatal("hash with other parameters must need a rehash")
	}
}

func TestVerifyPasswordMalformed(t *testing.T) {
	t.Parallel()
	good, _ := HashPassword("x")
	parts := strings.Split(good, "$")
	join := func(p ...string) string { return strings.Join(p, "$") }
	cases := map[string]string{
		"empty":          "",
		"plain":          "secret",
		"bcrypt":         "$2a$10$abcdefghijklmnopqrstuuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0",
		"argon2i":        join("", "argon2i", parts[2], parts[3], parts[4], parts[5]),
		"version":        join("", "argon2id", "v=16", parts[3], parts[4], parts[5]),
		"huge memory":    join("", "argon2id", parts[2], "m=4194304,t=2,p=1", parts[4], parts[5]),
		"zero time":      join("", "argon2id", parts[2], "m=19456,t=0,p=1", parts[4], parts[5]),
		"unknown param":  join("", "argon2id", parts[2], "m=19456,t=2,p=1,x=1", parts[4], parts[5]),
		"duplicate":      join("", "argon2id", parts[2], "m=19456,m=19456,t=2,p=1", parts[4], parts[5]),
		"bad salt":       join("", "argon2id", parts[2], parts[3], "!!!", parts[5]),
		"short key":      join("", "argon2id", parts[2], parts[3], parts[4], "AAAA"),
		"missing fields": join("", "argon2id", parts[2], parts[3]),
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ok, err := VerifyPassword(h, "x")
			if err == nil || ok {
				t.Fatalf("VerifyPassword(%q) = %v, %v; want error", h, ok, err)
			}
			if !NeedsRehash(h) {
				t.Fatalf("NeedsRehash(%q) = false", h)
			}
		})
	}
}

func TestGeneratePassword(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for range 200 {
		p := GeneratePassword()
		if len(p) != 16 {
			t.Fatalf("len(%q) = %d", p, len(p))
		}
		if strings.ContainsAny(p, "0O1lIo") {
			t.Fatalf("ambiguous character in %q", p)
		}
		for _, r := range p {
			if !strings.ContainsRune(passwordAlphabet, r) {
				t.Fatalf("unexpected character %q in %q", r, p)
			}
		}
		if err := ValidateNewPassword(p); err != nil {
			t.Fatalf("generated password %q fails policy: %v", p, err)
		}
		if seen[p] {
			t.Fatalf("duplicate password %q", p)
		}
		seen[p] = true
	}
}

func TestValidateNewPassword(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pw   string
		want error
	}{
		{"", ErrPasswordTooShort},
		{"1234567", ErrPasswordTooShort},
		{"пароль7", ErrPasswordTooShort}, // 7 runes, 14 bytes
		{"aaaaaaaa", ErrPasswordRepeated},
		{"        ", ErrPasswordRepeated},
		{"ёёёёёёёёё", ErrPasswordRepeated},
		{strings.Repeat("ab", 129), ErrPasswordTooLong},
		{"abcdefgh", nil},
		{"пароль12", nil},
		{"aaaaaaab", nil},
		{"12345678", nil},
	}
	for _, c := range cases {
		err := ValidateNewPassword(c.pw)
		if !errors.Is(err, c.want) || (c.want == nil) != (err == nil) {
			t.Errorf("ValidateNewPassword(%q) = %v, want %v", c.pw, err, c.want)
		}
	}
	if !strings.Contains(ErrPasswordTooShort.Error(), "не короче 8") {
		t.Errorf("message must be Russian and name the minimum: %q", ErrPasswordTooShort)
	}
}
