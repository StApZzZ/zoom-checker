package store

import (
	"testing"
	"time"

	"zoom-checker/internal/model"
)

func TestUsers(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		n, err := e.s.CountUsers(e.ctx)
		e.must(err)
		if n != 0 {
			t.Fatalf("CountUsers = %d", n)
		}
		admin := &model.User{Username: "admin", DisplayName: "Администратор", Role: model.RoleAdmin,
			AuthSource: model.AuthLocal, PasswordHash: "$argon2id$x", MustChangePassword: true}
		e.must(e.s.CreateUser(e.ctx, admin))
		if admin.ID == 0 || !admin.CreatedAt.Equal(t0.Truncate(time.Millisecond)) {
			t.Fatalf("created user = %+v", admin)
		}
		got, err := e.s.GetUser(e.ctx, admin.ID)
		e.must(err)
		if got.Username != "admin" || got.Role != model.RoleAdmin || got.AuthSource != model.AuthLocal ||
			!got.MustChangePassword || got.Disabled || got.PasswordHash != "$argon2id$x" || got.LastLoginAt != nil ||
			got.DisplayName != "Администратор" {
			t.Errorf("GetUser = %+v", got)
		}

		// Same username with another source is fine; same source conflicts.
		ldapUser := &model.User{Username: "admin", Role: model.RoleTeacher, AuthSource: model.AuthLDAP, ExternalID: "guid-1"}
		e.must(e.s.CreateUser(e.ctx, ldapUser))
		wantErr(t, e.s.CreateUser(e.ctx, &model.User{Username: "admin", Role: model.RoleTeacher, AuthSource: model.AuthLocal}), ErrConflict)
		// External id unique per source, empty external ids never collide.
		wantErr(t, e.s.CreateUser(e.ctx, &model.User{Username: "other", Role: model.RoleTeacher, AuthSource: model.AuthLDAP, ExternalID: "guid-1"}), ErrConflict)
		e.must(e.s.CreateUser(e.ctx, &model.User{Username: "t1", Role: model.RoleTeacher, AuthSource: model.AuthLocal}))
		e.must(e.s.CreateUser(e.ctx, &model.User{Username: "t2", Role: model.RoleTeacher, AuthSource: model.AuthLocal}))

		byName, err := e.s.GetUserByUsername(e.ctx, model.AuthLDAP, "admin")
		e.must(err)
		if byName.ID != ldapUser.ID {
			t.Errorf("GetUserByUsername returned %d", byName.ID)
		}
		byExt, err := e.s.GetUserByExternalID(e.ctx, model.AuthLDAP, "guid-1")
		e.must(err)
		if byExt.ID != ldapUser.ID {
			t.Errorf("GetUserByExternalID returned %d", byExt.ID)
		}
		_, err = e.s.GetUserByExternalID(e.ctx, model.AuthLocal, "")
		wantErr(t, err, ErrNotFound)
		_, err = e.s.GetUserByUsername(e.ctx, model.AuthOIDC, "admin")
		wantErr(t, err, ErrNotFound)
		_, err = e.s.GetUser(e.ctx, 9999)
		wantErr(t, err, ErrNotFound)

		list, err := e.s.ListUsers(e.ctx)
		e.must(err)
		if len(list) != 4 || list[0].Username != "admin" || list[0].AuthSource != model.AuthLDAP || list[3].Username != "t2" {
			t.Errorf("ListUsers order = %+v", list)
		}
		if n, _ := e.s.CountUsers(e.ctx); n != 4 {
			t.Errorf("CountUsers = %d", n)
		}

		// Update round trip including booleans.
		e.clock.Advance(time.Minute)
		got.Role = model.RoleTeacher
		got.DisplayName = "Иван"
		got.Email = "i@example.org"
		got.Disabled = true
		got.MustChangePassword = false
		got.PasswordHash = "new"
		e.must(e.s.UpdateUser(e.ctx, got))
		again, err := e.s.GetUser(e.ctx, admin.ID)
		e.must(err)
		if again.Role != model.RoleTeacher || !again.Disabled || again.MustChangePassword || again.Email != "i@example.org" ||
			again.PasswordHash != "new" || again.DisplayName != "Иван" || !again.UpdatedAt.Equal(e.clock.Now().Truncate(time.Millisecond)) ||
			!again.CreatedAt.Equal(admin.CreatedAt) {
			t.Errorf("after update = %+v", again)
		}
		got.Username = "t1"
		wantErr(t, e.s.UpdateUser(e.ctx, got), ErrConflict)
		wantErr(t, e.s.UpdateUser(e.ctx, &model.User{ID: 9999, Username: "x", Role: model.RoleAdmin, AuthSource: model.AuthLocal}), ErrNotFound)

		login := t0.Add(time.Hour)
		e.must(e.s.TouchUserLogin(e.ctx, admin.ID, login))
		again, _ = e.s.GetUser(e.ctx, admin.ID)
		wantTime(t, "last login", again.LastLoginAt, &login)
		wantErr(t, e.s.TouchUserLogin(e.ctx, 9999, login), ErrNotFound)

		// Delete cascades sessions.
		e.must(e.s.CreateSession(e.ctx, &model.Session{IDHash: "h1", UserID: admin.ID, ExpiresAt: t0.Add(time.Hour), AuthSource: model.AuthLocal}))
		e.must(e.s.DeleteUser(e.ctx, admin.ID))
		_, _, err = e.s.GetSessionWithUser(e.ctx, "h1")
		wantErr(t, err, ErrNotFound)
		wantErr(t, e.s.DeleteUser(e.ctx, admin.ID), ErrNotFound)
	})
}

func TestSessions(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		u := &model.User{Username: "kc", DisplayName: "KC", Role: model.RoleTeacher, AuthSource: model.AuthOIDC, ExternalID: "sub-1"}
		e.must(e.s.CreateUser(e.ctx, u))
		exp := t0.Add(7 * 24 * time.Hour)
		s1 := &model.Session{IDHash: "aa", UserID: u.ID, ExpiresAt: exp, IP: "10.0.0.1", UserAgent: "UA",
			AuthSource: model.AuthOIDC, OIDCIDToken: "id.token.jwt"}
		e.must(e.s.CreateSession(e.ctx, s1))
		if !s1.CreatedAt.Equal(t0) || !s1.LastSeenAt.Equal(t0) {
			t.Errorf("defaults not applied: %+v", s1)
		}
		wantErr(t, e.s.CreateSession(e.ctx, &model.Session{IDHash: "aa", UserID: u.ID, ExpiresAt: exp, AuthSource: model.AuthOIDC}), ErrConflict)
		e.must(e.s.CreateSession(e.ctx, &model.Session{IDHash: "bb", UserID: u.ID, ExpiresAt: t0.Add(time.Minute), AuthSource: model.AuthOIDC}))
		e.must(e.s.CreateSession(e.ctx, &model.Session{IDHash: "cc", UserID: u.ID, ExpiresAt: exp, AuthSource: model.AuthOIDC}))

		sess, user, err := e.s.GetSessionWithUser(e.ctx, "aa")
		e.must(err)
		if sess.IDHash != "aa" || sess.UserID != u.ID || sess.IP != "10.0.0.1" || sess.UserAgent != "UA" ||
			sess.OIDCIDToken != "id.token.jwt" || sess.AuthSource != model.AuthOIDC || !sess.ExpiresAt.Equal(exp.Truncate(time.Millisecond)) {
			t.Errorf("session = %+v", sess)
		}
		if user.ID != u.ID || user.Username != "kc" || user.ExternalID != "sub-1" {
			t.Errorf("user = %+v", user)
		}

		seen, newExp := t0.Add(2*time.Minute), t0.Add(8*24*time.Hour)
		e.must(e.s.TouchSession(e.ctx, "aa", seen, newExp))
		sess, _, _ = e.s.GetSessionWithUser(e.ctx, "aa")
		if !sess.LastSeenAt.Equal(seen) || !sess.ExpiresAt.Equal(newExp) {
			t.Errorf("touch: %+v", sess)
		}
		wantErr(t, e.s.TouchSession(e.ctx, "zz", seen, newExp), ErrNotFound)

		n, err := e.s.DeleteExpiredSessions(e.ctx, t0.Add(time.Minute)) // "bb" expires exactly then
		e.must(err)
		if n != 1 {
			t.Errorf("DeleteExpiredSessions = %d", n)
		}
		n, err = e.s.DeleteUserSessions(e.ctx, u.ID, "aa")
		e.must(err)
		if n != 1 {
			t.Errorf("DeleteUserSessions(keep aa) = %d", n)
		}
		if _, _, err := e.s.GetSessionWithUser(e.ctx, "aa"); err != nil {
			t.Errorf("kept session gone: %v", err)
		}
		e.must(e.s.DeleteSession(e.ctx, "aa"))
		e.must(e.s.DeleteSession(e.ctx, "aa")) // idempotent
		_, _, err = e.s.GetSessionWithUser(e.ctx, "aa")
		wantErr(t, err, ErrNotFound)
		e.must(e.s.CreateSession(e.ctx, &model.Session{IDHash: "dd", UserID: u.ID, ExpiresAt: exp, AuthSource: model.AuthOIDC}))
		n, err = e.s.DeleteUserSessions(e.ctx, u.ID, "")
		e.must(err)
		if n != 1 {
			t.Errorf("DeleteUserSessions(all) = %d", n)
		}
	})
}
