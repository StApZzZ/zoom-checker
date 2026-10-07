package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"zoom-checker/internal/auth"
	"zoom-checker/internal/model"
	"zoom-checker/internal/store"
)

// SessionCookie is the name of the login cookie.
const SessionCookie = "zc_session"

// CodePasswordChangeRequired is returned while a user must change the password.
const CodePasswordChangeRequired = "password_change_required"

// currentUser returns the authenticated user of the request (nil if none).
func currentUser(r *http.Request) *model.User {
	u, _ := r.Context().Value(ctxUser).(*model.User)
	return u
}

// currentSession returns the session of the request (nil if none).
func currentSession(r *http.Request) *model.Session {
	sess, _ := r.Context().Value(ctxSession).(*model.Session)
	return sess
}

// userIDPtr returns the current user's id for created_by / manual_by columns.
func userIDPtr(r *http.Request) *int64 {
	if u := currentUser(r); u != nil {
		id := u.ID
		return &id
	}
	return nil
}

// withSession authenticates the request from the session cookie.
func (s *Server) withSession(adminOnly, allowPasswordChange bool, h handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, user, err := s.loadSession(r)
		if err != nil {
			if !errors.Is(err, errNoSession) {
				s.Log.Error("load session", "err", err)
			}
			s.clearSessionCookie(w)
			writeError(w, r, s.Log, errUnauthorized())
			return
		}
		if user.MustChangePassword && !allowPasswordChange {
			writeError(w, r, s.Log, &APIError{Status: http.StatusForbidden, Code: CodePasswordChangeRequired,
				Message: "Сначала смените временный пароль"})
			return
		}
		if adminOnly && user.Role != model.RoleAdmin {
			writeError(w, r, s.Log, errForbidden("Это действие доступно только администратору"))
			return
		}
		ctx := context.WithValue(r.Context(), ctxUser, user)
		ctx = context.WithValue(ctx, ctxSession, sess)
		if err := h(w, r.WithContext(ctx)); err != nil {
			writeError(w, r, s.Log, err)
		}
	}
}

var errNoSession = errors.New("no valid session")

// sessionLifetime is how long a login of the given source lasts, counted from
// the login itself (no sliding expiry: the cookie's Max-Age is fixed at login
// too). LDAP and Keycloak logins get the shorter EXTERNAL_SESSION_TTL: their
// role and disabled state are only synced at login, so a user removed from
// the directory groups or disabled there must not keep access for a week.
func (s *Server) sessionLifetime(source model.AuthSource) time.Duration {
	ttl := s.Config.SessionTTL
	if source != model.AuthLocal && s.Config.ExternalSessionTTL > 0 && s.Config.ExternalSessionTTL < ttl {
		ttl = s.Config.ExternalSessionTTL
	}
	return ttl
}

// validateSession checks the cookie's session without changing anything: the
// session must exist, be younger than its absolute lifetime and belong to an
// enabled user. SSE streams re-check their login with it, so a long-lived
// stream never extends the session.
func (s *Server) validateSession(r *http.Request) (*model.Session, *model.User, error) {
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" || len(c.Value) > 200 {
		return nil, nil, errNoSession
	}
	idHash := auth.HashToken(c.Value)
	sess, user, err := s.Store.GetSessionWithUser(r.Context(), idHash)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, errNoSession
	}
	if err != nil {
		return nil, nil, err
	}
	now := s.now()
	// expires_at may come from an older version that slid it on every
	// request: the absolute end counted from the login wins.
	end := sess.CreatedAt.Add(s.sessionLifetime(sess.AuthSource))
	if !sess.ExpiresAt.After(now) || !end.After(now) || user.Disabled {
		_ = s.Store.DeleteSession(r.Context(), idHash)
		return nil, nil, errNoSession
	}
	return sess, user, nil
}

// loadSession validates the session and records the user's activity
// (last_seen_at, at most once a minute). The expiry never moves.
func (s *Server) loadSession(r *http.Request) (*model.Session, *model.User, error) {
	sess, user, err := s.validateSession(r)
	if err != nil {
		return nil, nil, err
	}
	now := s.now()
	if now.Sub(sess.LastSeenAt) > time.Minute {
		exp := sess.CreatedAt.Add(s.sessionLifetime(sess.AuthSource))
		if sess.ExpiresAt.Before(exp) {
			exp = sess.ExpiresAt
		}
		if err := s.Store.TouchSession(r.Context(), sess.IDHash, now, exp); err == nil {
			sess.LastSeenAt, sess.ExpiresAt = now, exp
		}
	}
	return sess, user, nil
}

// startSession creates a new session for user, sets the cookie and records the login.
// A fresh random token is always issued (no session fixation).
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, user *model.User, source model.AuthSource, oidcIDToken string) error {
	// Drop a previous session of this browser, if any.
	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
		_ = s.Store.DeleteSession(r.Context(), auth.HashToken(c.Value))
	}
	token, idHash := auth.NewToken()
	now := s.now()
	ttl := s.sessionLifetime(source)
	ua := r.UserAgent()
	if len(ua) > 300 {
		ua = ua[:300]
	}
	sess := &model.Session{
		IDHash:      idHash,
		UserID:      user.ID,
		CreatedAt:   now,
		ExpiresAt:   now.Add(ttl),
		LastSeenAt:  now,
		IP:          clientIP(r),
		UserAgent:   ua,
		AuthSource:  source,
		OIDCIDToken: oidcIDToken,
	}
	if err := s.Store.CreateSession(r.Context(), sess); err != nil {
		return err
	}
	_ = s.Store.TouchUserLogin(r.Context(), user.ID, now)
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.Config.PublicHTTPS(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	})
	return nil
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.Config.PublicHTTPS(), SameSite: http.SameSiteLaxMode,
	})
}

// audit appends an audit entry on behalf of the current user. Failures are
// logged, never returned: the change itself already happened.
func (s *Server) audit(r *http.Request, e model.AuditEntry) {
	if u := currentUser(r); u != nil {
		id := u.ID
		e.UserID = &id
		e.UserName = u.DisplayName
		if e.UserName == "" {
			e.UserName = u.Username
		}
	}
	if e.At.IsZero() {
		e.At = s.now()
	}
	if err := s.Store.InsertAudit(r.Context(), &e); err != nil {
		s.Log.Error("audit insert failed", "action", e.Action, "err", err)
	}
}

// storeErr maps store sentinel errors to API errors. notFound is the Russian
// message for ErrNotFound ("" = generic).
func storeErr(err error, notFound string) error {
	var nt *store.NameTakenError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return errNotFound(notFound)
	case errors.As(err, &nt):
		msg := "В группе уже есть студент с таким именем. Невозможно однозначно идентифицировать студентов по Zoom display name."
		if nt.IsAlias {
			msg = "Это имя уже используется как дополнительное имя другого студента группы."
		}
		return &APIError{Status: http.StatusConflict, Code: CodeConflict, Message: msg,
			Details: map[string]any{"name": nt.Name, "student_id": nt.StudentID, "is_alias": nt.IsAlias}, Err: err}
	case errors.Is(err, store.ErrConflict):
		return &APIError{Status: http.StatusConflict, Code: CodeConflict, Message: "Такая запись уже существует", Err: err}
	case errors.Is(err, store.ErrInvalid):
		return &APIError{Status: http.StatusUnprocessableEntity, Code: CodeValidation, Message: "Некорректные данные", Err: err}
	}
	return errInternal(err)
}
