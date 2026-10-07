package httpapi

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"zoom-checker/internal/auth"
	"zoom-checker/internal/model"
)

// loginFrom posts the login form from remote ("ip:port") with optional cookies.
func (e *azEnv) loginFrom(remote, username, password string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	e.t.Helper()
	r := azRequest(e.t, "POST", "/api/auth/login", map[string]string{"username": username, "password": password})
	r.RemoteAddr = remote
	for _, c := range cookies {
		if c != nil {
			r.AddCookie(c)
		}
	}
	return e.serve(r)
}

func TestLoginLimitsIPv6Network(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		addrs func(i int) string // address of attempt i
		user  func(i int) string
		limit int
		want  int // status of attempt limit+1
	}{
		{"one /64 shares the per-account key", func(i int) string { return fmt.Sprintf("[2001:db8:1:2::%x]:443", i+1) },
			func(int) string { return "petrov" }, 10, http.StatusTooManyRequests},
		{"one /64 shares the per-address budget", func(i int) string { return fmt.Sprintf("[2001:db8:1:2:%x::1]:443", i+1) },
			func(i int) string { return fmt.Sprintf("user%d", i) }, 60, http.StatusTooManyRequests},
		{"different /64s are independent", func(i int) string { return fmt.Sprintf("[2001:db8:1:%x::1]:443", i+1) },
			func(i int) string { return fmt.Sprintf("user%d", i) }, 60, http.StatusUnprocessableEntity},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newAZEnv(t)
			for i := range tc.limit {
				azWantError(t, e.loginFrom(tc.addrs(i), tc.user(i), "wrong"), http.StatusUnprocessableEntity, CodeInvalidCredentials)
			}
			rec := e.loginFrom(tc.addrs(tc.limit), tc.user(tc.limit), "wrong")
			if rec.Code != tc.want {
				t.Fatalf("attempt %d: status %d, want %d: %s", tc.limit+1, rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// An IPv4-mapped address from X-Forwarded-For counts as the IPv4 address.
func TestLoginLimitsMappedIPv4(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t, func(s *azSetup) {
		_, lan, _ := net.ParseCIDR("10.0.0.0/8")
		s.cfg.TrustedProxies = []*net.IPNet{lan}
	})
	for i := range 10 {
		r := azRequest(t, "POST", "/api/auth/login", map[string]string{"username": "petrov", "password": fmt.Sprint("wrong", i)})
		r.RemoteAddr = "10.0.0.1:1"
		if i%2 == 0 {
			r.Header.Set("X-Forwarded-For", "::ffff:198.51.100.7")
		} else {
			r.Header.Set("X-Forwarded-For", "198.51.100.7")
		}
		azWantError(t, e.serve(r), http.StatusUnprocessableEntity, CodeInvalidCredentials)
	}
	azWantError(t, e.loginFrom("198.51.100.7:1", "petrov", "wrong"), http.StatusTooManyRequests, CodeRateLimited)
}

// Guessing one account from many networks is capped per account.
func TestLoginLimitsPerAccountAcrossNetworks(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	e.srv.accountLimiter = auth.NewLoginLimiter(5, 15*time.Minute)
	e.createUser("petrov", "right password", model.RoleTeacher, nil)
	for i := range 5 {
		azWantError(t, e.loginFrom(fmt.Sprintf("[2001:db8:%x::1]:1", i+1), "petrov", "wrong"),
			http.StatusUnprocessableEntity, CodeInvalidCredentials)
	}
	rec := e.loginFrom("[2001:db8:99::1]:1", "petrov", "right password")
	azWantError(t, rec, http.StatusTooManyRequests, CodeRateLimited)
	// Other accounts are not affected.
	azWantError(t, e.loginFrom("[2001:db8:99::1]:1", "ivanov", "wrong"), http.StatusUnprocessableEntity, CodeInvalidCredentials)
}

// A browser that logged into the account before is not locked out by other
// people's failures from the same address, or by attempts on its account
// from elsewhere; its own guesses are still limited.
func TestLoginKnownDeviceCookie(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	e.srv.ipLimiter = auth.NewLoginLimiter(3, 15*time.Minute)
	e.srv.accountLimiter = auth.NewLoginLimiter(4, 15*time.Minute)
	e.createUser("petrov", "right password", model.RoleTeacher, nil)
	e.createUser("ivanov", "ivanov password", model.RoleTeacher, nil)
	const school = "203.0.113.5:1000"

	rec := e.loginFrom(school, "petrov", "right password")
	azWantStatus(t, rec, http.StatusOK)
	device := azCookie(rec, knownDeviceCookie)
	if device == nil || !device.HttpOnly || device.Path != knownDevicePath || device.SameSite != http.SameSiteStrictMode {
		t.Fatalf("known-device cookie = %+v", device)
	}
	if strings.Contains(device.Value, "petrov") {
		t.Fatal("the cookie reveals the account name")
	}

	// A student sprays logins from the school address: the address is blocked...
	for i := range 3 {
		azWantError(t, e.loginFrom(school, fmt.Sprintf("student%d", i), "x"), http.StatusUnprocessableEntity, CodeInvalidCredentials)
	}
	rec = e.loginFrom(school, "petrov", "right password")
	msg := azWantError(t, rec, http.StatusTooManyRequests, CodeRateLimited)
	if !strings.Contains(msg, "С этого адреса") {
		t.Errorf("per-address message = %q", msg)
	}
	// ...but not for the teacher's own browser.
	rec = e.loginFrom(school, "petrov", "right password", device)
	azWantStatus(t, rec, http.StatusOK)

	// Someone hammers petrov's account from elsewhere until it is blocked.
	for i := range 4 {
		azWantError(t, e.loginFrom(fmt.Sprintf("198.51.100.%d:1", i+1), "petrov", "wrong"), http.StatusUnprocessableEntity, CodeInvalidCredentials)
	}
	azWantError(t, e.loginFrom("198.51.100.99:1", "petrov", "right password"), http.StatusTooManyRequests, CodeRateLimited)
	azWantStatus(t, e.loginFrom("192.0.2.1:1", "petrov", "right password", device), http.StatusOK)

	// The cookie does not help another account.
	rec = e.loginFrom(school, "ivanov", "ivanov password", device)
	azWantError(t, rec, http.StatusTooManyRequests, CodeRateLimited)

	// Guesses with the cookie are limited per device (10 per window).
	for i := range 10 {
		azWantError(t, e.loginFrom(school, "petrov", fmt.Sprint("wrong", i), device), http.StatusUnprocessableEntity, CodeInvalidCredentials)
	}
	azWantError(t, e.loginFrom(school, "petrov", "right password", device), http.StatusTooManyRequests, CodeRateLimited)

	// A forged or tampered cookie is ignored.
	forged := &http.Cookie{Name: knownDeviceCookie, Value: device.Value[:len(device.Value)-2] + "AA"}
	azWantError(t, e.loginFrom("192.0.2.77:1", "petrov", "right password", forged), http.StatusTooManyRequests, CodeRateLimited)
}

func TestKnownDeviceCookieRemembersSeveralAccounts(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	var device *http.Cookie
	for i := range knownDeviceAccounts + 1 {
		name := fmt.Sprintf("teacher%d", i)
		e.createUser(name, name+" password", model.RoleTeacher, nil)
		rec := e.loginFrom("203.0.113.5:1", name, name+" password", device)
		azWantStatus(t, rec, http.StatusOK)
		device = azCookie(rec, knownDeviceCookie)
	}
	r := azRequest(t, "POST", "/api/auth/login", nil)
	r.AddCookie(device)
	now := time.Now()
	for i := range knownDeviceAccounts + 1 {
		got := e.srv.knownDevice(r, fmt.Sprintf("teacher%d", i), now) != ""
		if want := i > 0; got != want { // the oldest login was dropped
			t.Errorf("teacher%d known = %v, want %v", i, got, want)
		}
	}
	if e.srv.knownDevice(r, "teacher5", now.Add(knownDeviceMaxAge+time.Hour)) != "" {
		t.Error("an expired cookie still counts")
	}
}

func TestRateLimitMessageNamesKeycloak(t *testing.T) {
	t.Parallel()
	e, _ := newAZOIDCEnv(t)
	if msg := e.srv.rateLimitMessage(true); !strings.Contains(msg, "«Войти через Keycloak»") || fmt.Sprintf(msg, 3) == "" {
		t.Errorf("message = %q", msg)
	}
	if msg := e.srv.rateLimitMessage(false); msg != "Слишком много попыток входа. Попробуйте через %d мин." {
		t.Errorf("per-account message = %q", msg)
	}
}
