package httpapi

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"zoom-checker/internal/auth"
	"zoom-checker/internal/model"
)

// When every password hashing slot is taken, logins and password operations
// answer 503 with Retry-After instead of queueing without limit, unknown and
// known logins alike (no enumeration), and the refused attempts do not use up
// anybody's login budget.
//
// Not parallel: it occupies the process-wide hashing slots.
func TestPasswordHashingBusy(t *testing.T) {
	e := newAZEnv(t)
	e.srv.ipLimiter = auth.NewLoginLimiter(2, 15*time.Minute)
	e.srv.loginLimiter = auth.NewLoginLimiter(2, 15*time.Minute)
	e.createUser("petrov", "right password", model.RoleTeacher, nil)
	teacher := e.createUser("sidorov", "sidorov password", model.RoleTeacher, nil)
	teacherCookie := e.sessionFor(teacher, model.AuthLocal, "")
	_, adminCookie := e.adminCookie()
	_ = dummyPasswordHash() // computed before the slots are taken

	release := auth.HoldPasswordSlots(20 * time.Millisecond)
	defer release()

	wantBusy := func(what string, rec interface {
		Result() *http.Response
	}) {
		t.Helper()
		res := rec.Result()
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("%s: status %d, want 503", what, res.StatusCode)
		}
		if got := res.Header.Get("Retry-After"); got != "3" {
			t.Errorf("%s: Retry-After = %q, want 3", what, got)
		}
	}
	for range 3 {
		rec, cookie := e.login("petrov", "right password")
		wantBusy("known login", rec)
		azWantError(t, rec, http.StatusServiceUnavailable, CodeUnavailable)
		if cookie != nil {
			t.Fatal("session started without a password check")
		}
		rec, _ = e.login("nobody", "whatever")
		wantBusy("unknown login", rec)
	}
	rec := e.do("POST", "/api/auth/password", map[string]string{
		"current_password": "sidorov password", "new_password": "a new password"}, teacherCookie)
	wantBusy("password change", rec)
	rec = e.do("POST", "/api/users", map[string]any{
		"username": "kuznetsov", "display_name": "Кузнецов", "password": "temporary pw", "role": "teacher"}, adminCookie)
	wantBusy("admin create user", rec)
	rec = e.do("PATCH", fmt.Sprintf("/api/users/%d", teacher.ID), map[string]any{"password": "temporary pw"}, adminCookie)
	wantBusy("admin password reset", rec)

	release()
	// The refused attempts were given back: the budget of 2 is untouched.
	if rec, cookie := e.login("petrov", "right password"); rec.Code != http.StatusOK || cookie == nil {
		t.Fatalf("login after the burst: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do("POST", "/api/auth/password", map[string]string{
		"current_password": "sidorov password", "new_password": "a new password"}, teacherCookie)
	azWantStatus(t, rec, http.StatusOK)
}
