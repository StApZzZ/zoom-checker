package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"

	"zoom-checker/internal/auth"
	"zoom-checker/internal/model"
	"zoom-checker/internal/version"
)

func TestAuthConfig(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	rec := e.do("GET", "/api/auth/config", nil, nil)
	azWantStatus(t, rec, http.StatusOK)
	got := azDecode[map[string]any](t, rec)
	want := map[string]any{"local_enabled": true, "ldap_enabled": false, "oidc_enabled": false,
		"oidc_label": "", "version": version.Version, "public_url": "http://app.test", "timezone": "UTC"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("config = %v, want %v", got, want)
	}

	e2 := newAZEnv(t, func(s *azSetup) {
		s.cfg.LocalAuthEnabled = false
		s.cfg.OIDC.ButtonLabel = "Войти через SSO"
		s.deps.OIDC = azTestOIDC(t, "http://127.0.0.1:1", nil)
	})
	got2 := azDecode[authConfigDTO](t, e2.do("GET", "/api/auth/config", nil, nil))
	if got2.LocalEnabled || !got2.OIDCEnabled || got2.OIDCLabel != "Войти через SSO" {
		t.Fatalf("config with OIDC = %+v", got2)
	}

	// timezone is the IANA name of TIMEZONE: the web compares it with the browser's zone.
	ekb, err := time.LoadLocation("Asia/Yekaterinburg")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	e3 := newAZEnv(t, func(s *azSetup) { s.cfg.Timezone = ekb })
	if got3 := azDecode[authConfigDTO](t, e3.do("GET", "/api/auth/config", nil, nil)); got3.Timezone != "Asia/Yekaterinburg" {
		t.Fatalf("timezone = %q", got3.Timezone)
	}
}

func TestLoginLocal(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	u := e.createUser("ivanova", "correct horse 1", model.RoleTeacher, nil)

	rec, cookie := e.login("  IVANOVA ", "correct horse 1")
	azWantStatus(t, rec, http.StatusOK)
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Fatalf("session cookie = %+v", cookie)
	}
	got := azDecode[loginResponse](t, rec).User
	if got.ID != u.ID || got.Username != "ivanova" || got.AuthSource != "local" || got.LastLoginAt == nil || got.MustChangePassword {
		t.Fatalf("user = %+v", got)
	}
	if !strings.Contains(rec.Body.String(), `"last_login_at":"`) || strings.Contains(rec.Body.String(), "password_hash") {
		t.Fatalf("body = %s", rec.Body.String())
	}

	me := e.do("GET", "/api/auth/me", nil, cookie)
	azWantStatus(t, me, http.StatusOK)
	if m := azDecode[loginResponse](t, me).User; m.ID != u.ID {
		t.Fatalf("me = %+v", m)
	}
	azWantError(t, e.do("GET", "/api/auth/me", nil, nil), http.StatusUnauthorized, CodeUnauthorized)
}

func TestLoginFailures(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	e.createUser("petrov", "right password", model.RoleTeacher, nil)
	e.createUser("sidorov", "right password", model.RoleTeacher, func(u *model.User) { u.Disabled = true })

	cases := []struct{ name, user, pw string }{
		{"wrong password", "petrov", "wrong password"},
		{"unknown user", "nobody", "right password"},
		{"disabled user, right password", "sidorov", "right password"},
	}
	for _, c := range cases {
		rec, cookie := e.login(c.user, c.pw)
		msg := azWantError(t, rec, http.StatusUnprocessableEntity, CodeInvalidCredentials)
		if msg != "Неверный логин или пароль" || cookie != nil {
			t.Errorf("%s: message %q, cookie %v", c.name, msg, cookie)
		}
	}

	azWantError(t, e.do("POST", "/api/auth/login", map[string]string{"username": " ", "password": "x"}, nil),
		http.StatusUnprocessableEntity, CodeValidation)
	azWantError(t, e.do("POST", "/api/auth/login", "{not json", nil), http.StatusBadRequest, CodeBadRequest)
	// Huge logins are refused before they become rate-limiter keys.
	azWantError(t, e.do("POST", "/api/auth/login", map[string]string{"username": strings.Repeat("x", 300), "password": "x"}, nil),
		http.StatusUnprocessableEntity, CodeValidation)
	if n := e.srv.loginLimiter.Len(); n != 3 {
		t.Fatalf("limiter keys = %d, want 3 (one per checked login)", n)
	}

	// Local accounts are skipped entirely when local login is switched off.
	off := newAZEnv(t, func(s *azSetup) { s.cfg.LocalAuthEnabled = false })
	off.createUser("petrov", "right password", model.RoleTeacher, nil)
	rec, _ := off.login("petrov", "right password")
	azWantError(t, rec, http.StatusUnprocessableEntity, CodeInvalidCredentials)
}

func TestLoginRateLimit(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	e.createUser("petrov", "right password", model.RoleTeacher, nil)

	for i := range 10 {
		rec, _ := e.login("petrov", fmt.Sprintf("wrong %d", i))
		azWantError(t, rec, http.StatusUnprocessableEntity, CodeInvalidCredentials)
	}
	// The 11th attempt is refused before the password is even checked.
	rec, cookie := e.login("PETROV", "right password")
	msg := azWantError(t, rec, http.StatusTooManyRequests, CodeRateLimited)
	if msg != "Слишком много попыток входа. Попробуйте через 15 мин." || cookie != nil {
		t.Fatalf("message %q cookie %v", msg, cookie)
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" || ra == "0" {
		t.Fatalf("Retry-After = %q", ra)
	}

	// Another address is not affected by this IP+user key.
	r := azRequest(t, "POST", "/api/auth/login", map[string]string{"username": "petrov", "password": "right password"})
	r.RemoteAddr = "198.51.100.7:4444"
	azWantStatus(t, e.serve(r), http.StatusOK)
}

func TestLoginRateLimitPerIP(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	// 60 attempts per IP in the window, whatever the usernames.
	for i := range 60 {
		rec, _ := e.login(fmt.Sprintf("user%d", i), "whatever")
		azWantError(t, rec, http.StatusUnprocessableEntity, CodeInvalidCredentials)
	}
	rec, _ := e.login("fresh-user", "whatever")
	azWantError(t, rec, http.StatusTooManyRequests, CodeRateLimited)
}

func TestLoginRehashesOldHash(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	key := argon2.IDKey([]byte("old password"), salt, 1, 8*1024, 1, 32)
	old := fmt.Sprintf("$argon2id$v=19$m=8192,t=1,p=1$%s$%s",
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
	if !auth.NeedsRehash(old) {
		t.Fatal("test hash should need a rehash")
	}
	u := e.createUser("legacy", "", model.RoleTeacher, func(u *model.User) { u.PasswordHash = old })

	rec, _ := e.login("legacy", "old password")
	azWantStatus(t, rec, http.StatusOK)
	got, err := e.st.GetUser(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PasswordHash == old || auth.NeedsRehash(got.PasswordHash) {
		t.Fatalf("hash not upgraded: %s", got.PasswordHash)
	}
	if ok, _ := auth.VerifyPassword(got.PasswordHash, "old password"); !ok {
		t.Fatal("upgraded hash does not verify")
	}
}

func TestMustChangePasswordGate(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	_, admin := e.adminCookie()

	rec := e.do("POST", "/api/users", map[string]any{"username": "Teacher.One", "display_name": "Учитель",
		"password": "temporary-1", "role": "teacher"}, admin)
	azWantStatus(t, rec, http.StatusCreated)
	if u := azDecode[UserDTO](t, rec); u.Username != "teacher.one" || !u.MustChangePassword || u.AuthSource != "local" {
		t.Fatalf("created %+v", u)
	}

	rec, cookie := e.login("teacher.one", "temporary-1")
	azWantStatus(t, rec, http.StatusOK)
	if !azDecode[loginResponse](t, rec).User.MustChangePassword {
		t.Fatal("must_change_password not reported at login")
	}
	_, otherBrowser := e.login("teacher.one", "temporary-1")

	// Everything except me / logout / password is blocked until the change.
	azWantError(t, e.do("GET", "/api/zoom/status", nil, cookie), http.StatusForbidden, CodePasswordChangeRequired)
	azWantStatus(t, e.do("GET", "/api/auth/me", nil, cookie), http.StatusOK)

	change := func(cur, next string) *http.Response {
		return e.do("POST", "/api/auth/password", map[string]string{"current_password": cur, "new_password": next}, cookie).Result()
	}
	msg := azWantError(t, e.do("POST", "/api/auth/password",
		map[string]string{"current_password": "nope", "new_password": "brand new pass"}, cookie),
		http.StatusUnprocessableEntity, CodeValidation)
	if msg != "Текущий пароль указан неверно" {
		t.Fatalf("wrong current: %q", msg)
	}
	msg = azWantError(t, e.do("POST", "/api/auth/password",
		map[string]string{"current_password": "temporary-1", "new_password": "short"}, cookie),
		http.StatusUnprocessableEntity, CodeValidation)
	if msg != auth.ErrPasswordTooShort.Error() {
		t.Fatalf("weak password: %q", msg)
	}
	if res := change("temporary-1", "temporary-1"); res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("same password accepted: %d", res.StatusCode)
	}
	rec = e.do("POST", "/api/auth/password", map[string]string{"current_password": "temporary-1", "new_password": "brand new pass"}, cookie)
	azWantStatus(t, rec, http.StatusOK)
	if azDecode[loginResponse](t, rec).User.MustChangePassword {
		t.Fatal("must_change_password still set")
	}

	azWantStatus(t, e.do("GET", "/api/zoom/status", nil, cookie), http.StatusOK)
	// The other browser was signed out, this one stays.
	azWantError(t, e.do("GET", "/api/auth/me", nil, otherBrowser), http.StatusUnauthorized, CodeUnauthorized)
	if rec, _ := e.login("teacher.one", "brand new pass"); rec.Code != http.StatusOK {
		t.Fatalf("login with new password: %d", rec.Code)
	}
	if n := len(e.azAudit(model.AuditUserUpdate)); n != 1 {
		t.Fatalf("audit entries for password change: %d", n)
	}
}

func TestChangePasswordExternalAccount(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	u := e.createUser("ldapuser", "", model.RoleTeacher, func(u *model.User) {
		u.AuthSource, u.ExternalID = model.AuthLDAP, "uuid-1"
	})
	c := e.sessionFor(u, model.AuthLDAP, "")
	msg := azWantError(t, e.do("POST", "/api/auth/password",
		map[string]string{"current_password": "x", "new_password": "long enough pw"}, c),
		http.StatusUnprocessableEntity, CodeValidation)
	if msg != "Пароль меняется в корпоративной системе" {
		t.Fatalf("message %q", msg)
	}
}

func TestChangePasswordRemovesInitialAdminFile(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	file := filepath.Join(e.cfg.DataDir, "initial-admin-password.txt")
	if err := os.WriteFile(file, []byte("Generated-Pass-7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := e.createUser("teacher", "teacher pass 1", model.RoleTeacher, nil)
	oc := e.sessionFor(other, model.AuthLocal, "")
	azWantStatus(t, e.do("POST", "/api/auth/password",
		map[string]string{"current_password": "teacher pass 1", "new_password": "teacher pass 2"}, oc), http.StatusOK)
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("another user's password change must keep the file: %v", err)
	}

	admin := e.createUser("admin", "Generated-Pass-7", model.RoleAdmin, func(u *model.User) { u.MustChangePassword = true })
	ac := e.sessionFor(admin, model.AuthLocal, "")
	azWantStatus(t, e.do("POST", "/api/auth/password",
		map[string]string{"current_password": "Generated-Pass-7", "new_password": "my own admin pass"}, ac), http.StatusOK)
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("initial password file still there: %v", err)
	}
}

func TestLogoutLocal(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	e.createUser("ivanova", "correct horse 1", model.RoleTeacher, nil)
	_, cookie := e.login("ivanova", "correct horse 1")
	rec := e.do("POST", "/api/auth/logout", map[string]any{}, cookie)
	azWantStatus(t, rec, http.StatusOK)
	if got := azDecode[logoutResponse](t, rec); got.RedirectURL != "/login" {
		t.Fatalf("redirect = %q", got.RedirectURL)
	}
	cleared := false
	for _, c := range rec.Result().Cookies() {
		cleared = cleared || (c.Name == SessionCookie && c.MaxAge < 0)
	}
	if !cleared {
		t.Fatal("session cookie not cleared")
	}
	azWantError(t, e.do("GET", "/api/auth/me", nil, cookie), http.StatusUnauthorized, CodeUnauthorized)
}

func TestCrossSiteProtection(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t, func(s *azSetup) { s.cfg.ZoomWebhookSecret = "secret_token" })
	e.createUser("ivanova", "correct horse 1", model.RoleTeacher, nil)

	r := azRequest(t, "POST", "/api/auth/login", map[string]string{"username": "ivanova", "password": "correct horse 1"})
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	azWantError(t, e.serve(r), http.StatusForbidden, CodeForbidden)

	r = azRequest(t, "POST", "/api/auth/login", map[string]string{"username": "ivanova", "password": "correct horse 1"})
	r.Header.Set("Origin", "https://evil.example")
	azWantError(t, e.serve(r), http.StatusForbidden, CodeForbidden)

	r = azRequest(t, "POST", "/api/auth/login", map[string]string{"username": "ivanova", "password": "correct horse 1"})
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	azWantStatus(t, e.serve(r), http.StatusOK)

	// Zoom's server-to-server call is not subject to the browser CSRF check.
	r = azSignedWebhook(t, "secret_token", azURLValidationBody, azNow())
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Origin", "https://zoom.us")
	azWantStatus(t, e.serve(r), http.StatusOK)
}

func TestLoginNeverLogsPassword(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	e.createUser("ivanova", "correct horse 1", model.RoleTeacher, nil)
	e.login("ivanova", "Sup3rSecretGuess")
	e.login("ivanova", "correct horse 1")
	if logs := e.logs.String(); strings.Contains(logs, "Sup3rSecretGuess") || strings.Contains(logs, "correct horse 1") {
		t.Fatalf("password in logs:\n%s", logs)
	}
}
