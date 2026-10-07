package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"zoom-checker/internal/auth"
	"zoom-checker/internal/model"
)

// slClock is a settable clock for session lifetime tests.
type slClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *slClock) now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *slClock) set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

func newSessionLifetimeEnv(t *testing.T, clock *slClock) *azEnv {
	return newAZEnv(t, func(s *azSetup) {
		s.cfg.SessionTTL = 24 * time.Hour
		s.cfg.ExternalSessionTTL = 8 * time.Hour
		s.deps.Now = clock.now
	})
}

// storedSession creates a session row directly (as an older version or another
// login source would have left it) and returns its cookie.
func storedSession(t *testing.T, e *azEnv, u *model.User, source model.AuthSource, created, expires time.Time) *http.Cookie {
	t.Helper()
	token, hash := auth.NewToken()
	if err := e.st.CreateSession(context.Background(), &model.Session{
		IDHash: hash, UserID: u.ID, CreatedAt: created, ExpiresAt: expires, LastSeenAt: created, AuthSource: source,
	}); err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: SessionCookie, Value: token}
}

func TestSessionLifetimeIsAbsolute(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		source  model.AuthSource
		created time.Time
		expires time.Time // stored expires_at (older versions slid it)
		at      time.Time
		wantOK  bool
	}{
		{"local, within SESSION_TTL", model.AuthLocal, t0, t0.Add(24 * time.Hour), t0.Add(23 * time.Hour), true},
		{"local, after SESSION_TTL", model.AuthLocal, t0, t0.Add(24 * time.Hour), t0.Add(24*time.Hour + time.Second), false},
		{"local, slid expiry from an older version does not count", model.AuthLocal, t0, t0.Add(30 * 24 * time.Hour), t0.Add(25 * time.Hour), false},
		{"ldap, within EXTERNAL_SESSION_TTL", model.AuthLDAP, t0, t0.Add(8 * time.Hour), t0.Add(7 * time.Hour), true},
		{"ldap, after EXTERNAL_SESSION_TTL", model.AuthLDAP, t0, t0.Add(24 * time.Hour), t0.Add(8*time.Hour + time.Second), false},
		{"oidc, after EXTERNAL_SESSION_TTL", model.AuthOIDC, t0, t0.Add(24 * time.Hour), t0.Add(9 * time.Hour), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clock := &slClock{t: tc.at}
			e := newSessionLifetimeEnv(t, clock)
			u := e.createUser("ivanova", "", model.RoleTeacher, func(u *model.User) { u.AuthSource = tc.source })
			c := storedSession(t, e, u, tc.source, tc.created, tc.expires)
			rec := e.do("GET", "/api/auth/me", nil, c)
			if got := rec.Code == http.StatusOK; got != tc.wantOK {
				t.Fatalf("GET /api/auth/me = %d, want ok=%v", rec.Code, tc.wantOK)
			}
		})
	}
}

// Activity is recorded, but no request (and no open live screen) moves the
// end of the session.
func TestSessionActivityDoesNotExtend(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	clock := &slClock{t: t0}
	e := newSessionLifetimeEnv(t, clock)
	e.createUser("petrov", "teacher-pass-1", model.RoleTeacher, nil)
	rec, c := e.login("petrov", "teacher-pass-1")
	if c == nil {
		t.Fatalf("login = %d %s", rec.Code, rec.Body.String())
	}
	if c.MaxAge != int((24 * time.Hour).Seconds()) {
		t.Errorf("cookie Max-Age = %d, want SESSION_TTL", c.MaxAge)
	}
	idHash := auth.HashToken(c.Value)
	for _, h := range []int{1, 6, 12, 23} {
		clock.set(t0.Add(time.Duration(h) * time.Hour))
		azWantStatus(t, e.do("GET", "/api/auth/me", nil, c), http.StatusOK)
		sess, _, err := e.st.GetSessionWithUser(context.Background(), idHash)
		if err != nil {
			t.Fatal(err)
		}
		if !sess.ExpiresAt.Equal(t0.Add(24 * time.Hour)) {
			t.Fatalf("after a request at +%dh expires_at = %v, want the login time + SESSION_TTL", h, sess.ExpiresAt)
		}
		if !sess.LastSeenAt.Equal(clock.now()) {
			t.Errorf("last_seen_at = %v, want %v", sess.LastSeenAt, clock.now())
		}
	}

	// The SSE re-check only validates: last_seen_at stays where it was.
	clock.set(t0.Add(23*time.Hour + 30*time.Minute))
	r := httptest.NewRequest("GET", "/api/lessons/1/stream", nil)
	r.AddCookie(c)
	if e.srv.lessonSSESessionGone(r) {
		t.Fatal("session reported gone before its end")
	}
	sess, _, err := e.st.GetSessionWithUser(context.Background(), idHash)
	if err != nil {
		t.Fatal(err)
	}
	if !sess.LastSeenAt.Equal(t0.Add(23 * time.Hour)) {
		t.Errorf("SSE check touched the session: last_seen_at = %v", sess.LastSeenAt)
	}
	clock.set(t0.Add(24*time.Hour + time.Second))
	if !e.srv.lessonSSESessionGone(r) {
		t.Error("an open stream outlived the login")
	}
	azWantStatus(t, e.do("GET", "/api/auth/me", nil, c), http.StatusUnauthorized)
}

// A directory login gets the shorter lifetime in its cookie and its row.
func TestExternalSessionCookieLifetime(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	e := newSessionLifetimeEnv(t, &slClock{t: t0})
	u := e.createUser("kc-teacher", "", model.RoleTeacher, func(u *model.User) { u.AuthSource = model.AuthOIDC })
	rec := httptest.NewRecorder()
	if err := e.srv.startSession(rec, httptest.NewRequest("GET", "/auth/oidc/callback", nil), u, model.AuthOIDC, ""); err != nil {
		t.Fatal(err)
	}
	c := azCookie(rec, SessionCookie)
	if c == nil || c.MaxAge != int((8*time.Hour).Seconds()) {
		t.Fatalf("cookie = %+v, want Max-Age of EXTERNAL_SESSION_TTL", c)
	}
	sess, _, err := e.st.GetSessionWithUser(context.Background(), auth.HashToken(c.Value))
	if err != nil {
		t.Fatal(err)
	}
	if !sess.ExpiresAt.Equal(t0.Add(8 * time.Hour)) {
		t.Errorf("expires_at = %v, want login + EXTERNAL_SESSION_TTL", sess.ExpiresAt)
	}
}
