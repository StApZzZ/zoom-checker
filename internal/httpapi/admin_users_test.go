package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"zoom-checker/internal/auth"
	"zoom-checker/internal/model"
)

func TestUsersAdminOnly(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	teacher := e.createUser("teacher", "", model.RoleTeacher, nil)
	tc := e.sessionFor(teacher, model.AuthLocal, "")
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/users"}, {"POST", "/api/users"}, {"PATCH", "/api/users/1"}, {"DELETE", "/api/users/1"},
		{"GET", "/api/settings"}, {"PUT", "/api/settings"}, {"GET", "/api/system"}, {"POST", "/api/system/test-ldap"},
		{"GET", "/api/zoom/events"}, {"GET", "/api/zoom/events/1"}, {"POST", "/api/zoom/events/1/reprocess"},
		{"POST", "/api/zoom/events/reprocess-failed"},
	} {
		var body any
		if c.method != "GET" && c.method != "DELETE" {
			body = map[string]any{}
		}
		azWantError(t, e.do(c.method, c.path, body, tc), http.StatusForbidden, CodeForbidden)
		azWantError(t, e.do(c.method, c.path, body, nil), http.StatusUnauthorized, CodeUnauthorized)
	}
}

func TestUsersCreateValidation(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	_, admin := e.adminCookie()
	e.createUser("ldap.user", "", model.RoleTeacher, func(u *model.User) { u.AuthSource, u.ExternalID = model.AuthLDAP, "g1" })
	ok := map[string]any{"username": "new.user", "display_name": "Новый", "password": "good password", "role": "teacher"}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for kk, vv := range ok {
			m[kk] = vv
		}
		m[k] = v
		return m
	}
	cases := []struct {
		name   string
		body   map[string]any
		status int
		code   string
	}{
		{"short username", with("username", "a"), 422, CodeValidation},
		{"bad characters", with("username", "иван"), 422, CodeValidation},
		{"spaces inside", with("username", "a b"), 422, CodeValidation},
		{"too long", with("username", strings.Repeat("a", 65)), 422, CodeValidation},
		{"weak password", with("password", "1234567"), 422, CodeValidation},
		{"bad role", with("role", "student"), 422, CodeValidation},
		{"long display name", with("display_name", strings.Repeat("я", 201)), 422, CodeValidation},
		{"shadows ldap account", with("username", "LDAP.User"), 409, CodeConflict},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			azWantError(t, e.do("POST", "/api/users", c.body, admin), c.status, c.code)
		})
	}

	rec := e.do("POST", "/api/users", with("username", " New.User@corp "), admin)
	azWantStatus(t, rec, http.StatusCreated)
	u := azDecode[UserDTO](t, rec)
	if u.Username != "new.user@corp" || u.Role != model.RoleTeacher || !u.MustChangePassword || u.Disabled || u.LastLoginAt != nil {
		t.Fatalf("created %+v", u)
	}
	azWantError(t, e.do("POST", "/api/users", with("username", "new.user@corp"), admin), http.StatusConflict, CodeConflict)
	if len(e.azAudit(model.AuditUserCreate)) != 1 {
		t.Fatal("no audit entry for user.create")
	}
	if strings.Contains(e.logs.String(), "good password") {
		t.Fatal("password logged")
	}

	list := azDecode[[]UserDTO](t, e.do("GET", "/api/users", nil, admin))
	if len(list) != 3 {
		t.Fatalf("users = %+v", list)
	}
}

func TestUsersUpdate(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	_, admin := e.adminCookie()
	ctx := context.Background()
	u := e.createUser("teacher", "first password", model.RoleTeacher, nil)
	path := fmt.Sprintf("/api/users/%d", u.ID)

	_, tc := e.login("teacher", "first password")
	rec := e.do("PATCH", path, map[string]any{"display_name": "  Мария  ", "role": "admin"}, admin)
	azWantStatus(t, rec, http.StatusOK)
	if got := azDecode[UserDTO](t, rec); got.DisplayName != "Мария" || got.Role != model.RoleAdmin {
		t.Fatalf("updated %+v", got)
	}
	// A role change keeps sessions; the role is read on every request.
	azWantStatus(t, e.do("GET", "/api/users", nil, tc), http.StatusOK)

	// Password reset: temporary password, sessions dropped.
	rec = e.do("PATCH", path, map[string]any{"password": "temporary pw 2"}, admin)
	azWantStatus(t, rec, http.StatusOK)
	if !azDecode[UserDTO](t, rec).MustChangePassword {
		t.Fatal("must_change_password not set by reset")
	}
	azWantError(t, e.do("GET", "/api/auth/me", nil, tc), http.StatusUnauthorized, CodeUnauthorized)
	stored, _ := e.st.GetUser(ctx, u.ID)
	if ok, _ := auth.VerifyPassword(stored.PasswordHash, "temporary pw 2"); !ok {
		t.Fatal("new password not stored")
	}

	// Disable: sessions dropped, login refused.
	_, tc = e.login("teacher", "temporary pw 2")
	azWantStatus(t, e.do("PATCH", path, map[string]any{"disabled": true}, admin), http.StatusOK)
	azWantError(t, e.do("GET", "/api/auth/me", nil, tc), http.StatusUnauthorized, CodeUnauthorized)
	rec, _ = e.login("teacher", "temporary pw 2")
	azWantError(t, rec, http.StatusUnprocessableEntity, CodeInvalidCredentials)
	if n, _ := e.st.DeleteUserSessions(ctx, u.ID, ""); n != 0 {
		t.Fatalf("%d sessions left", n)
	}

	// Validation and not found.
	azWantError(t, e.do("PATCH", path, map[string]any{"role": "boss"}, admin), 422, CodeValidation)
	azWantError(t, e.do("PATCH", path, map[string]any{"password": "short"}, admin), 422, CodeValidation)
	azWantError(t, e.do("PATCH", "/api/users/99999", map[string]any{"disabled": false}, admin), 404, CodeNotFound)

	// Directory accounts: role/disabled yes, password no.
	ext := e.createUser("ldapuser", "", model.RoleTeacher, func(x *model.User) { x.AuthSource, x.ExternalID = model.AuthLDAP, "g" })
	extPath := fmt.Sprintf("/api/users/%d", ext.ID)
	azWantError(t, e.do("PATCH", extPath, map[string]any{"password": "long enough 1"}, admin), 422, CodeValidation)
	azWantStatus(t, e.do("PATCH", extPath, map[string]any{"disabled": true}, admin), http.StatusOK)

	audits := e.azAudit(model.AuditUserUpdate)
	if len(audits) < 4 {
		t.Fatalf("audit entries: %+v", audits)
	}
	for _, a := range audits {
		if strings.Contains(a.NewValue, "temporary pw 2") || strings.Contains(a.PreviousValue, "$argon2") {
			t.Fatalf("secret in audit: %+v", a)
		}
	}
}

func TestLastAdminProtection(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	me, admin := e.adminCookie()
	other := e.createUser("second", "", model.RoleAdmin, func(u *model.User) { u.Disabled = true })
	mePath := fmt.Sprintf("/api/users/%d", me.ID)
	otherPath := fmt.Sprintf("/api/users/%d", other.ID)

	// The only enabled admin cannot step down (a disabled admin does not count).
	msg := azWantError(t, e.do("PATCH", mePath, map[string]any{"role": "teacher"}, admin), 422, CodeValidation)
	if msg != "Нельзя убрать последнего администратора" {
		t.Fatalf("message %q", msg)
	}
	azWantError(t, e.do("PATCH", mePath, map[string]any{"disabled": true}, admin), 422, CodeValidation)
	azWantError(t, e.do("DELETE", mePath, nil, admin), 422, CodeValidation)
	if u, _ := e.st.GetUser(context.Background(), me.ID); u.Role != model.RoleAdmin {
		t.Fatal("role changed despite the error")
	}

	// With a second enabled admin, stepping down works.
	azWantStatus(t, e.do("PATCH", otherPath, map[string]any{"disabled": false}, admin), http.StatusOK)
	azWantStatus(t, e.do("PATCH", mePath, map[string]any{"role": "teacher"}, admin), http.StatusOK)
	azWantError(t, e.do("GET", "/api/users", nil, admin), http.StatusForbidden, CodeForbidden)

	// The remaining admin is protected from the store side too.
	oc := e.sessionFor(other, model.AuthLocal, "")
	azWantError(t, e.do("PATCH", otherPath, map[string]any{"role": "teacher"}, oc), 422, CodeValidation)
	if err := ensureAnotherAdmin(context.Background(), e.st, other.ID); err == nil {
		t.Fatal("ensureAnotherAdmin passed with a single admin")
	}

	// Deleting another (non-last) admin is fine.
	third := e.createUser("third", "", model.RoleAdmin, nil)
	rec := e.do("DELETE", fmt.Sprintf("/api/users/%d", third.ID), nil, oc)
	azWantStatus(t, rec, http.StatusNoContent)
	azWantError(t, e.do("DELETE", fmt.Sprintf("/api/users/%d", third.ID), nil, oc), 404, CodeNotFound)
	if len(e.azAudit(model.AuditUserDelete)) != 1 {
		t.Fatal("no audit entry for user.delete")
	}
}

// An admin resetting the bootstrap admin's password (PATCH /api/users/{id})
// makes the generated password in DATA_DIR/initial-admin-password.txt invalid:
// the file goes. Resetting anybody else keeps it, unless the file holds that
// user's replaced password.
func TestUsersResetRemovesInitialAdminFile(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	file := filepath.Join(e.cfg.DataDir, "initial-admin-password.txt")
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(content+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	exists := func() bool {
		_, err := os.Stat(file)
		return err == nil
	}
	_, root := e.adminCookie()
	boot := e.createUser("admin", "Generated-Pass-7", model.RoleAdmin, func(u *model.User) { u.MustChangePassword = true })
	teacher := e.createUser("teacher", "teacher pass 1", model.RoleTeacher, nil)
	reset := func(u *model.User, pw string) {
		t.Helper()
		azWantStatus(t, e.do("PATCH", fmt.Sprintf("/api/users/%d", u.ID), map[string]any{"password": pw}, root), http.StatusOK)
	}

	write("Generated-Pass-7")
	reset(teacher, "teacher pass 2")
	if !exists() {
		t.Fatal("resetting another user's password must keep the file")
	}
	azWantStatus(t, e.do("PATCH", fmt.Sprintf("/api/users/%d", boot.ID), map[string]any{"display_name": "Главный"}, root), http.StatusOK)
	if !exists() {
		t.Fatal("a change without a new password must keep the file")
	}
	reset(boot, "brand new admin pw")
	if exists() {
		t.Fatal("resetting the bootstrap admin's password must remove the file")
	}

	// The file holds the teacher's replaced password (e.g. ADMIN_USERNAME was
	// changed after the first start): that password is gone, so is the file.
	write("teacher pass 2")
	reset(teacher, "teacher pass 3")
	if exists() {
		t.Fatal("file with the replaced password must be removed")
	}
}
