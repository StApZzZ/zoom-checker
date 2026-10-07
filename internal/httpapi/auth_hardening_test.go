package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"zoom-checker/internal/auth"
	"zoom-checker/internal/config"
	"zoom-checker/internal/model"
)

// Spellings of one directory account share the per-account limiter key, so
// "CORP\petrov", "x\petrov" and "petrov@<domain>" cannot each get 10 tries.
func TestLoginRateLimitDomainSpellings(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t, func(s *azSetup) { s.cfg.LDAP.Domain = "Corp.Local" })
	for i := range 10 {
		rec, _ := e.login("petrov", fmt.Sprintf("wrong %d", i))
		azWantError(t, rec, http.StatusUnprocessableEntity, CodeInvalidCredentials)
	}
	for _, login := range []string{`CORP\petrov`, `other\Petrov`, "petrov@corp.local", " PETROV@CORP.LOCAL "} {
		rec, _ := e.login(login, "whatever")
		azWantError(t, rec, http.StatusTooManyRequests, CodeRateLimited)
	}
	// Another account and another domain are separate keys.
	rec, _ := e.login("ivanov", "whatever")
	azWantError(t, rec, http.StatusUnprocessableEntity, CodeInvalidCredentials)
	rec, _ = e.login("petrov@other.example", "whatever")
	azWantError(t, rec, http.StatusUnprocessableEntity, CodeInvalidCredentials)
}

func TestLoginLimitAccount(t *testing.T) {
	t.Parallel()
	withDomain := &Server{Deps: Deps{Config: &config.Config{LDAP: config.LDAPConfig{Domain: " Corp.Local "}}}}
	noDomain := &Server{Deps: Deps{Config: &config.Config{}}}
	cases := []struct {
		srv      *Server
		in, want string
	}{
		{withDomain, "ivanov", "ivanov"},
		{withDomain, `corp\ivanov`, "ivanov"},
		{withDomain, `a\b\ivanov`, "ivanov"},
		{withDomain, `corp\ ivanov`, "ivanov"},
		{withDomain, "ivanov@corp.local", "ivanov"},
		{withDomain, "ivanov@corp.local.evil", "ivanov@corp.local.evil"},
		{noDomain, "ivanov@corp.local", "ivanov@corp.local"},
		{noDomain, `corp\ivanov`, "ivanov"},
	}
	for _, c := range cases {
		if got := c.srv.loginLimitAccount(c.in); got != c.want {
			t.Errorf("loginLimitAccount(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A local account whose stored hash is unusable never logs in and answers
// like any wrong password.
func TestLoginUnusableHash(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	e.createUser("imported", "", model.RoleTeacher, func(u *model.User) { u.PasswordHash = "not-a-phc-string" })
	rec, cookie := e.login("imported", "anything at all")
	if msg := azWantError(t, rec, http.StatusUnprocessableEntity, CodeInvalidCredentials); msg != msgInvalidCredentials || cookie != nil {
		t.Fatalf("message %q cookie %v", msg, cookie)
	}
	if !strings.Contains(e.logs.String(), "local password hash unusable") {
		t.Fatal("unusable hash not reported in the log")
	}
}

// Logins through the directory leave a trace in the audit log: the account
// creation on the first login and later role changes synced from groups.
func TestLDAPLoginAudit(t *testing.T) {
	t.Parallel()
	dir := newAZFakeLDAP(t, azLDAPUser{UID: "alice", Password: "alice-pw", UUID: "uuid-alice", CN: "Алиса Петрова",
		Mail: "alice@example.test", MemberOf: []string{azTeachersDN}})
	e := newAZLDAPEnv(t, dir)

	rec, _ := e.login("alice", "alice-pw")
	azWantStatus(t, rec, http.StatusOK)
	u := azDecode[loginResponse](t, rec).User
	created := e.azAudit(model.AuditUserCreate)
	if len(created) != 1 || created[0].EntityID != fmt.Sprint(u.ID) || created[0].UserID == nil || *created[0].UserID != u.ID ||
		created[0].UserName != "Алиса Петрова" || !strings.Contains(created[0].NewValue, "преподаватель") ||
		!strings.Contains(created[0].Reason, "LDAP") {
		t.Fatalf("create audit = %+v", created)
	}

	// Same data again: nothing to audit.
	rec, _ = e.login("alice", "alice-pw")
	azWantStatus(t, rec, http.StatusOK)
	if n := len(e.azAudit(model.AuditUserUpdate)); n != 0 {
		t.Fatalf("%d update entries for an unchanged login", n)
	}

	// Promoted in the directory: the role change is recorded.
	dir.set(azLDAPUser{UID: "alice", Password: "alice-pw", UUID: "uuid-alice", CN: "Алиса Петрова",
		Mail: "alice@example.test", MemberOf: []string{azAdminsDN}})
	rec, _ = e.login("alice", "alice-pw")
	azWantStatus(t, rec, http.StatusOK)
	upd := e.azAudit(model.AuditUserUpdate)
	if len(upd) != 1 || upd[0].PreviousValue != "роль: преподаватель" || upd[0].NewValue != "роль: администратор" {
		t.Fatalf("update audit = %+v", upd)
	}
}

func TestOIDCLoginAudit(t *testing.T) {
	t.Parallel()
	e, idp := newAZOIDCEnv(t)
	rec := e.oidcLogin(idp, "/", map[string]any{"roles": []string{"zc-teacher"}}, nil)
	if rec.Code != http.StatusFound || azCookie(rec, SessionCookie) == nil {
		t.Fatalf("callback: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	created := e.azAudit(model.AuditUserCreate)
	if len(created) != 1 || !strings.Contains(created[0].Reason, "Keycloak") || created[0].UserID == nil {
		t.Fatalf("create audit = %+v", created)
	}
}

// Two admins demoting each other at the same moment must leave one admin.
func TestLastAdminParallelDemotion(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	a := e.createUser("admin-a", "", model.RoleAdmin, nil)
	b := e.createUser("admin-b", "", model.RoleAdmin, nil)
	ca, cb := e.sessionFor(a, model.AuthLocal, ""), e.sessionFor(b, model.AuthLocal, "")

	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i, req := range []struct {
		target int64
		cookie *http.Cookie
	}{{b.ID, ca}, {a.ID, cb}} {
		wg.Go(func() {
			codes[i] = e.do("PATCH", fmt.Sprintf("/api/users/%d", req.target), map[string]any{"role": "teacher"}, req.cookie).Code
		})
	}
	wg.Wait()
	users, err := e.st.ListUsers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	admins := 0
	for _, u := range users {
		if u.Role == model.RoleAdmin && !u.Disabled {
			admins++
		}
	}
	if admins != 1 {
		t.Fatalf("%d admins left (responses %v)", admins, codes)
	}
}

// Successful logins do not use up the per-IP budget (a school behind one
// address), and they do not reset the failures counted for that address.
func TestLoginRateLimitPerIPIgnoresSuccesses(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	e.srv.ipLimiter = auth.NewLoginLimiter(3, 15*time.Minute)
	e.createUser("petrov", "right password", model.RoleTeacher, nil)

	rec, _ := e.login("ivanov", "wrong")
	azWantError(t, rec, http.StatusUnprocessableEntity, CodeInvalidCredentials)
	for i := range 5 {
		if rec, cookie := e.login("petrov", "right password"); rec.Code != http.StatusOK || cookie == nil {
			t.Fatalf("login %d: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	// The failure before the successes still counts: two more, then 429.
	for _, u := range []string{"sidorov", "kuznetsov"} {
		rec, _ := e.login(u, "wrong")
		azWantError(t, rec, http.StatusUnprocessableEntity, CodeInvalidCredentials)
	}
	rec, _ = e.login("petrov", "right password")
	azWantError(t, rec, http.StatusTooManyRequests, CodeRateLimited)
}
