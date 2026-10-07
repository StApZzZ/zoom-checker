package httpapi

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"zoom-checker/internal/auth"
	"zoom-checker/internal/model"
	"zoom-checker/internal/store"
	"zoom-checker/internal/version"
)

// Error codes specific to the login endpoints.
const (
	CodeInvalidCredentials = "invalid_credentials"
	CodeAccessDenied       = "access_denied"
	CodeAccountDisabled    = "account_disabled"
)

const (
	msgInvalidCredentials = "Неверный логин или пароль"
	msgAccountDisabled    = "Ваша учётная запись отключена. Обратитесь к администратору."
	msgExternalPassword   = "Пароль меняется в корпоративной системе"

	// initialAdminPasswordFile is written by the first-start bootstrap (docs §3).
	initialAdminPasswordFile = "initial-admin-password.txt"
	defaultOIDCLabel         = "Войти через Keycloak"
	// maxLoginLength bounds the login field in bytes (directories allow 256).
	maxLoginLength = 256
)

func (s *Server) registerAuth(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/config", s.public(s.handleAuthConfig))
	mux.HandleFunc("POST /api/auth/login", s.public(s.handleLogin))
	mux.HandleFunc("POST /api/auth/logout", s.authedAllowPasswordChange(s.handleLogout))
	mux.HandleFunc("GET /api/auth/me", s.authedAllowPasswordChange(s.handleMe))
	mux.HandleFunc("POST /api/auth/password", s.authedAllowPasswordChange(s.handleChangePassword))
	// Browser navigations (not XHR): answered with redirects.
	mux.HandleFunc("GET /auth/oidc/login", s.handleOIDCLogin)
	mux.HandleFunc("GET /auth/oidc/callback", s.handleOIDCCallback)
	// Compute the timing-equalization hash now, not during the first unknown login.
	go dummyPasswordHash()
}

// authConfigDTO is GET /api/auth/config (types.ts AuthConfig).
type authConfigDTO struct {
	LocalEnabled bool   `json:"local_enabled"`
	LDAPEnabled  bool   `json:"ldap_enabled"`
	OIDCEnabled  bool   `json:"oidc_enabled"`
	OIDCLabel    string `json:"oidc_label"`
	Version      string `json:"version"`
	// PublicURL is the service's main address (PUBLIC_URL): the login page
	// names it after error=oidc_host.
	PublicURL string `json:"public_url"`
	// Timezone is the IANA name of TIMEZONE: the web app notes when the
	// browser's zone differs from it (server messages use TIMEZONE).
	Timezone string `json:"timezone"`
}

func (s *Server) handleAuthConfig(w http.ResponseWriter, _ *http.Request) error {
	d := authConfigDTO{
		LocalEnabled: s.Config.LocalAuthEnabled,
		LDAPEnabled:  s.LDAP != nil,
		OIDCEnabled:  s.OIDC != nil,
		Version:      version.Version,
		PublicURL:    strings.TrimRight(s.Config.PublicURL.String(), "/"),
		Timezone:     s.lessonLoc().String(),
	}
	if d.OIDCEnabled {
		d.OIDCLabel = strings.TrimSpace(s.Config.OIDC.ButtonLabel)
		if d.OIDCLabel == "" {
			d.OIDCLabel = defaultOIDCLabel
		}
	}
	writeJSON(w, http.StatusOK, d)
	return nil
}

// loginResponse is {"user": User} (login and me).
type loginResponse struct {
	User UserDTO `json:"user"`
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// dummyPasswordHash is verified against when no account can check the password,
// so unknown logins cost as much time as wrong passwords (no user enumeration).
// It is computed with the current parameters so the timing really matches.
var dummyPasswordHash = sync.OnceValue(func() string {
	h, _ := auth.HashPassword(auth.GeneratePassword())
	return h
})

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) error {
	var req loginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	rawLogin := strings.TrimSpace(req.Username)
	username := strings.ToLower(rawLogin)
	if username == "" || req.Password == "" {
		return errValidation("Введите логин и пароль", nil)
	}
	// Checked before the limiters: their keys contain the login.
	if len(rawLogin) > maxLoginLength {
		return errValidation("Слишком длинный логин", nil)
	}

	// Every attempt is counted before the password is checked (login_limits.go).
	now := s.now()
	ip := clientIP(r)
	gate := s.newLoginGate(r, username, now)
	if ok, wait, perIP := gate.allow(); !ok {
		s.Log.Warn("login rate limited", "username", username, "ip", ip, "per_address", perIP,
			"known_device", gate.device != "")
		return rateLimitedError(w, wait, s.rateLimitMessage(perIP))
	}

	user, source, change, err := s.authenticate(r, username, rawLogin, req.Password)
	var denied *auth.AccessDeniedError
	switch {
	case err == nil:
	case errors.Is(err, auth.ErrInvalidCredentials):
		gate.failure()
		s.Log.Info("login failed", "username", username, "ip", ip)
		return &APIError{Status: http.StatusUnprocessableEntity, Code: CodeInvalidCredentials, Message: msgInvalidCredentials}
	case errors.As(err, &denied):
		s.Log.Info("login denied: no matching role", "username", username, "source", source, "ip", ip)
		return &APIError{Status: http.StatusForbidden, Code: CodeAccessDenied, Message: auth.UserMessage(err), Err: err}
	case errors.Is(err, errExternalUserDisabled):
		s.Log.Info("login denied: account disabled", "username", username, "source", source, "ip", ip)
		return &APIError{Status: http.StatusForbidden, Code: CodeAccountDisabled, Message: msgAccountDisabled}
	case errors.Is(err, auth.ErrUnavailable):
		s.Log.Warn("login failed: directory unavailable", "username", username, "err", err)
		return &APIError{Status: http.StatusServiceUnavailable, Code: CodeUnavailable, Message: auth.UserMessage(err), Err: err}
	case errors.Is(err, auth.ErrBusy):
		// Every password hashing slot stayed taken (a burst of logins): the
		// password was never checked, so the attempt does not count.
		gate.release()
		s.Log.Warn("login refused: password hashing is busy", "username", username, "ip", ip)
		return busyError(w)
	default:
		return errInternal(err)
	}

	gate.success()
	s.auditExternalUser(r, user, change)
	if err := s.startSession(w, r, user, source, ""); err != nil {
		return errInternal(err)
	}
	s.rememberDevice(w, r, s.loginLimitAccount(username), now)
	t := s.now()
	user.LastLoginAt = &t
	s.Log.Info("login", "username", user.Username, "user_id", user.ID, "source", source, "ip", ip)
	writeJSON(w, http.StatusOK, loginResponse{User: toUserDTO(user)})
	return nil
}

// authenticate checks a login form submission (docs §8): a local account with
// that username wins; otherwise LDAP when enabled; otherwise the generic
// failure after a dummy hash check. Errors: auth.ErrInvalidCredentials,
// *auth.AccessDeniedError, auth.ErrUnavailable, errExternalUserDisabled or an
// internal error. change describes what an LDAP login changed in the account
// record (for the audit log).
func (s *Server) authenticate(r *http.Request, username, rawLogin, password string) (*model.User, model.AuthSource, externalUserChange, error) {
	ctx := r.Context()
	local, err := s.Store.GetUserByUsername(ctx, model.AuthLocal, username)
	switch {
	case err == nil && s.Config.LocalAuthEnabled:
		ok, verr := auth.VerifyPasswordContext(ctx, local.PasswordHash, password)
		if errors.Is(verr, auth.ErrBusy) {
			return nil, model.AuthLocal, externalUserChange{}, verr
		}
		if verr != nil {
			// Broken or empty hash (e.g. imported account): never matches. It is
			// rejected without hashing, so spend the same time as a real check:
			// such accounts must not be told apart by the response time.
			s.Log.Warn("local password hash unusable", "user_id", local.ID, "err", verr)
			if err := dummyPasswordCheck(ctx, password); err != nil {
				return nil, model.AuthLocal, externalUserChange{}, err
			}
		}
		if !ok || local.Disabled {
			return nil, model.AuthLocal, externalUserChange{}, auth.ErrInvalidCredentials
		}
		if auth.NeedsRehash(local.PasswordHash) {
			// Best effort: a busy hashing pool only postpones the upgrade.
			if h, herr := auth.HashPasswordContext(ctx, password); herr == nil {
				local.PasswordHash = h
				if uerr := s.Store.UpdateUser(ctx, local); uerr != nil {
					s.Log.Warn("password rehash not saved", "user_id", local.ID, "err", uerr)
				}
			}
		}
		return local, model.AuthLocal, externalUserChange{}, nil
	case err != nil && !errors.Is(err, store.ErrNotFound):
		return nil, model.AuthLocal, externalUserChange{}, err
	}

	if s.LDAP != nil {
		id, err := s.LDAP.Authenticate(ctx, rawLogin, password)
		if err != nil {
			return nil, model.AuthLDAP, externalUserChange{}, err
		}
		u, change, err := s.upsertExternalUser(ctx, id)
		return u, model.AuthLDAP, change, err
	}

	if err := dummyPasswordCheck(ctx, password); err != nil {
		return nil, model.AuthLocal, externalUserChange{}, err
	}
	return nil, model.AuthLocal, externalUserChange{}, auth.ErrInvalidCredentials
}

// dummyPasswordCheck spends the time of a real password check (no user
// enumeration by timing). It goes through the same bounded hashing pool as
// real checks, so an unknown login under load is answered exactly like a
// known one: auth.ErrBusy when no slot is free, nil otherwise.
func dummyPasswordCheck(ctx context.Context, password string) error {
	if _, err := auth.VerifyPasswordContext(ctx, dummyPasswordHash(), password); errors.Is(err, auth.ErrBusy) {
		return err
	}
	return nil
}

// busyRetryAfter is the Retry-After of a 503 answered because every password
// hashing slot was taken.
const busyRetryAfter = 3 * time.Second

// busyError renders auth.ErrBusy: 503 unavailable with a Retry-After header.
func busyError(w http.ResponseWriter) error {
	w.Header().Set("Retry-After", strconv.Itoa(int(busyRetryAfter.Seconds())))
	return &APIError{Status: http.StatusServiceUnavailable, Code: CodeUnavailable, Message: auth.ErrBusy.Error(),
		Details: map[string]any{"retry_after_seconds": int(busyRetryAfter.Seconds())}}
}

// loginLimitAccount is the account part of the per-account login limiter key
// (username already trimmed and lower-cased). Spellings the directory treats
// as the same account — "CORP\ivanov", "ivanov@<LDAP_DOMAIN>" and "ivanov" —
// share one key, so the per-account limit cannot be multiplied by varying the
// domain part (internal/auth strips any "DOMAIN\" prefix before the search).
func (s *Server) loginLimitAccount(username string) string {
	k := username
	if i := strings.LastIndexByte(k, '\\'); i >= 0 {
		k = strings.TrimSpace(k[i+1:])
	}
	if d := strings.ToLower(strings.TrimSpace(s.Config.LDAP.Domain)); d != "" {
		k = strings.TrimSuffix(k, "@"+d)
	}
	return k
}

// rateLimitedError renders a 429 with a Retry-After header; format gets the
// number of whole minutes to wait (at least 1).
func rateLimitedError(w http.ResponseWriter, wait time.Duration, format string) error {
	secs := max(1, int(math.Ceil(wait.Seconds())))
	mins := max(1, int(math.Ceil(wait.Minutes())))
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	return &APIError{Status: http.StatusTooManyRequests, Code: CodeRateLimited,
		Message: fmt.Sprintf(format, mins), Details: map[string]any{"retry_after_seconds": secs}}
}

// logoutResponse is POST /api/auth/logout.
type logoutResponse struct {
	RedirectURL string `json:"redirect_url"`
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) error {
	sess := currentSession(r)
	// Delete first: building the IdP logout URL may wait for discovery.
	err := s.Store.DeleteSession(r.Context(), sess.IDHash)
	s.clearSessionCookie(w)
	if err != nil {
		return errInternal(err)
	}
	redirect := "/login"
	if sess.AuthSource == model.AuthOIDC && sess.OIDCIDToken != "" && s.OIDC != nil {
		// Also end the Keycloak session, otherwise the next login is silent.
		if u := s.OIDC.LogoutURL(sess.OIDCIDToken); u != "" {
			redirect = u
		}
	}
	if u := currentUser(r); u != nil {
		s.Log.Info("logout", "username", u.Username, "user_id", u.ID)
	}
	writeJSON(w, http.StatusOK, logoutResponse{RedirectURL: redirect})
	return nil
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, http.StatusOK, loginResponse{User: toUserDTO(currentUser(r))})
	return nil
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) error {
	var req changePasswordRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	me := currentUser(r)
	if me.AuthSource != model.AuthLocal {
		return errValidation(msgExternalPassword, nil)
	}
	// A stolen session must not become a password-guessing oracle.
	now := s.now()
	key := "password-change|" + strconv.FormatInt(me.ID, 10)
	if !s.loginLimiter.Allow(key, now) {
		return rateLimitedError(w, s.loginLimiter.RetryAfter(key, now), "Слишком много попыток. Попробуйте через %d мин.")
	}
	ctx := r.Context()
	u, err := s.Store.GetUser(ctx, me.ID)
	if err != nil {
		return storeErr(err, "Пользователь не найден")
	}
	ok, verr := auth.VerifyPasswordContext(ctx, u.PasswordHash, req.CurrentPassword)
	if errors.Is(verr, auth.ErrBusy) {
		s.loginLimiter.Release(key, now) // nothing was checked
		return busyError(w)
	}
	if !ok {
		s.loginLimiter.Failure(key, now)
		return errValidation("Текущий пароль указан неверно", map[string]any{"field": "current_password"})
	}
	// Retrying with a too weak new password must not lock the user out.
	s.loginLimiter.Success(key)
	if err := auth.ValidateNewPassword(req.NewPassword); err != nil {
		return errValidation(err.Error(), map[string]any{"field": "new_password"})
	}
	if req.NewPassword == req.CurrentPassword {
		return errValidation("Новый пароль должен отличаться от текущего", map[string]any{"field": "new_password"})
	}
	hash, err := auth.HashPasswordContext(ctx, req.NewPassword)
	if errors.Is(err, auth.ErrBusy) {
		return busyError(w)
	}
	if err != nil {
		return errInternal(err)
	}
	u.PasswordHash = hash
	u.MustChangePassword = false
	if err := s.Store.UpdateUser(ctx, u); err != nil {
		return storeErr(err, "Пользователь не найден")
	}
	// Other browsers logged in with the old password are signed out.
	if _, err := s.Store.DeleteUserSessions(ctx, u.ID, currentSession(r).IDHash); err != nil {
		s.Log.Error("delete other sessions after password change", "user_id", u.ID, "err", err)
	}
	s.removeInitialAdminPassword(u, func(held string) bool { return held == req.CurrentPassword })
	s.audit(r, model.AuditEntry{Action: model.AuditUserUpdate, EntityType: "user",
		EntityID: strconv.FormatInt(u.ID, 10), NewValue: "пароль изменён пользователем"})
	s.Log.Info("password changed", "username", u.Username, "user_id", u.ID)
	writeJSON(w, http.StatusOK, loginResponse{User: toUserDTO(u)})
	return nil
}

// removeInitialAdminPassword deletes the bootstrap password file after the
// password of local account u changed (by the user or an admin reset), once
// the password the file holds is no longer valid: u is the bootstrap admin
// (ADMIN_USERNAME), or wasOldPassword reports that the file holds the password
// that was just replaced. Other users' changes keep the file, so a teacher's
// password change cannot delete the admin's only copy before it was read.
func (s *Server) removeInitialAdminPassword(u *model.User, wasOldPassword func(held string) bool) {
	if s.Config.DataDir == "" || u.AuthSource != model.AuthLocal {
		return
	}
	path := filepath.Join(s.Config.DataDir, initialAdminPasswordFile)
	content, err := os.ReadFile(path)
	if err != nil {
		return // usually: does not exist
	}
	isBootstrapAdmin := strings.EqualFold(u.Username, strings.TrimSpace(s.Config.AdminUsername))
	if !isBootstrapAdmin && !wasOldPassword(strings.TrimSpace(string(content))) {
		return
	}
	if err := os.Remove(path); err != nil {
		s.Log.Warn("could not remove the initial admin password file", "path", path, "err", err)
		return
	}
	s.Log.Info("initial admin password file removed", "path", path)
}

// handleOIDCLogin starts the Keycloak login (browser navigation).
// ?prompt=login|select_account is "Войти под другой учётной записью" on the
// login page after an access denial: otherwise Keycloak's session sends the
// same account straight back. When the denied account's ID token is still at
// hand (auth.OIDC.RememberDenied), its Keycloak session is ended first and the
// browser comes back to /login?state=switch_account: prompt=login alone only
// makes Keycloak ask that same account for its password again.
func (s *Server) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	if s.OIDC == nil {
		writeError(w, r, s.Log, errNotFound("Вход через Keycloak не настроен"))
		return
	}
	q := r.URL.Query()
	if prompt := q.Get("prompt"); prompt != "" && auth.ValidPrompt(prompt) {
		if target := s.OIDC.SwitchAccountURL(w, r); target != "" {
			s.Log.Info("oidc: ending the identity provider session of the denied account before another login")
			// The provider's logout returns to the bare /login: keep where
			// the user was going for the login that follows.
			s.OIDC.RememberNext(w, q.Get("next"))
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, target, http.StatusFound)
			return
		}
	}
	next := q.Get("next")
	if kept := s.OIDC.TakeNext(w, r); kept != "" && auth.SanitizeNext(next) == "/" {
		next = kept
	}
	target, err := s.OIDC.StartLogin(w, r, next, auth.WithPrompt(q.Get("prompt")))
	if err != nil {
		s.Log.Warn("oidc login could not start", "err", err)
		redirectLoginError(w, r, "oidc_failed")
		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// handleOIDCCallback completes the Keycloak login and starts a session.
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if s.OIDC == nil {
		writeError(w, r, s.Log, errNotFound("Вход через Keycloak не настроен"))
		return
	}
	id, rawIDToken, next, err := s.OIDC.HandleCallback(w, r)
	var denied *auth.AccessDeniedError
	switch {
	case errors.As(err, &denied):
		s.Log.Info("oidc login denied: no matching role", "username", denied.Username)
		// Kept for "Войти под другой учётной записью" (handleOIDCLogin).
		s.OIDC.RememberDenied(w, rawIDToken)
		redirectLogin(w, r, withNext(accessDeniedQuery(denied.Required), next))
		return
	case errors.Is(err, auth.ErrOIDCNoFlowCookie):
		// Usually the login was started on another address than PUBLIC_URL:
		// the login page names the main address (GET /api/auth/config).
		s.Log.Warn("oidc login failed: login cookie missing; the login was probably started on another address than PUBLIC_URL "+
			"(or the cookie expired, was blocked or belongs to another browser)", "public_url", s.Config.PublicURL.String())
		redirectLoginError(w, r, "oidc_host")
		return
	case err != nil:
		s.Log.Warn("oidc login failed", "err", err)
		redirectLogin(w, r, withNext(url.Values{"error": {"oidc_failed"}}, next))
		return
	}
	user, change, err := s.upsertExternalUser(r.Context(), id)
	switch {
	case errors.Is(err, errExternalUserDisabled):
		s.Log.Info("oidc login denied: account disabled", "username", id.Username)
		// Keycloak still holds this account's session: without its ID token
		// "Войти под другой учётной записью" could not end it, and every login
		// on this browser would come back as the disabled account.
		s.OIDC.RememberDenied(w, rawIDToken)
		redirectLogin(w, r, withNext(url.Values{"error": {"disabled"}}, next))
		return
	case err != nil:
		s.Log.Error("oidc login: user record", "username", id.Username, "err", err)
		redirectLoginError(w, r, "oidc_failed")
		return
	}
	s.auditExternalUser(r, user, change)
	s.OIDC.ForgetDenied(w, r)
	if err := s.startSession(w, r, user, model.AuthOIDC, rawIDToken); err != nil {
		s.Log.Error("oidc login: start session", "user_id", user.ID, "err", err)
		redirectLoginError(w, r, "oidc_failed")
		return
	}
	s.Log.Info("login", "username", user.Username, "user_id", user.ID, "source", model.AuthOIDC, "ip", clientIP(r))
	http.Redirect(w, r, auth.SanitizeNext(next), http.StatusFound)
}

func redirectLoginError(w http.ResponseWriter, r *http.Request, code string) {
	redirectLogin(w, r, url.Values{"error": {code}})
}

// withNext adds the local path the user was going to (when not "/") to a
// login page query, so a retry after the error still ends up there.
func withNext(q url.Values, next string) url.Values {
	if next = auth.SanitizeNext(next); next != "/" {
		q.Set("next", next)
	}
	return q
}

// redirectLogin sends the browser to the login page with query q.
func redirectLogin(w http.ResponseWriter, r *http.Request, q url.Values) {
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/login?"+q.Encode(), http.StatusFound)
}

// Limits of the "required" list in an access-denied redirect: the configured
// roles are short, but the URL must stay well below proxy limits.
const (
	maxRequiredRoles   = 20
	maxRequiredRoleLen = 1000
)

// accessDeniedQuery is the login page query after a denied OIDC login:
// error=access_denied and required=<comma-separated roles/groups that grant
// access>, so the page can name them (docs §8). required is left out when
// nothing is configured to grant access.
func accessDeniedQuery(required []string) url.Values {
	q := url.Values{"error": {"access_denied"}}
	var list []string
	size := 0
	for _, role := range required {
		role = strings.TrimSpace(role)
		if role == "" || strings.Contains(role, ",") {
			continue
		}
		if len(list) == maxRequiredRoles || size+len(role)+1 > maxRequiredRoleLen {
			break
		}
		list = append(list, role)
		size += len(role) + 1
	}
	if len(list) > 0 {
		q.Set("required", strings.Join(list, ","))
	}
	return q
}
