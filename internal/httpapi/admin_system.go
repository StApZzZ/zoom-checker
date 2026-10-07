package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"zoom-checker/internal/auth"
	"zoom-checker/internal/model"
	"zoom-checker/internal/version"
)

// authProviderStatusDTO is types.ts AuthProviderStatus.
type authProviderStatusDTO struct {
	Enabled bool   `json:"enabled"`
	Detail  string `json:"detail"`
}

// systemInfoDTO is GET /api/system (types.ts SystemInfo). Never carries secrets.
type systemInfoDTO struct {
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	DBDriver      string `json:"db_driver"`
	TLSMode       string `json:"tls_mode"`
	PublicURL     string `json:"public_url"`
	DataDir       string `json:"data_dir"`
	Timezone      string `json:"timezone"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	Auth          struct {
		Local authProviderStatusDTO `json:"local"`
		LDAP  authProviderStatusDTO `json:"ldap"`
		OIDC  authProviderStatusDTO `json:"oidc"`
	} `json:"auth"`
}

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) error {
	c := s.Config
	d := systemInfoDTO{
		Version:       version.Version,
		Commit:        version.Commit,
		DBDriver:      c.DBDriver(),
		TLSMode:       c.TLSMode,
		PublicURL:     sysRedactURL(c.PublicURL),
		DataDir:       c.DataDir,
		UptimeSeconds: max(0, int64(s.now().Sub(s.started).Seconds())),
	}
	if c.Timezone != nil {
		d.Timezone = c.Timezone.String()
	}

	d.Auth.Local.Enabled = c.LocalAuthEnabled
	if c.LocalAuthEnabled {
		d.Auth.Local.Detail = "Вход по логину и паролю, созданным в Zoom Checker"
		if users, err := s.Store.ListUsers(r.Context()); err == nil {
			n := 0
			for i := range users {
				if users[i].AuthSource == model.AuthLocal {
					n++
				}
			}
			d.Auth.Local.Detail = fmt.Sprintf("Включён, локальных учётных записей: %d", n)
		}
	} else {
		d.Auth.Local.Detail = "Выключен (AUTH_LOCAL_ENABLED=false)"
	}

	d.Auth.LDAP.Enabled = s.LDAP != nil
	switch {
	case s.LDAP != nil:
		l := c.LDAP
		parts := []string{sysRedactRawURL(l.URL)}
		if l.StartTLS {
			parts = append(parts, "StartTLS")
		}
		if l.Preset != "" {
			parts = append(parts, "пресет "+l.Preset)
		}
		if l.BaseDN != "" {
			parts = append(parts, "база "+l.BaseDN)
		}
		if l.BindDN != "" {
			parts = append(parts, "сервисная учётная запись "+l.BindDN)
		} else {
			parts = append(parts, "прямой вход пользователя (без сервисной учётной записи)")
		}
		if l.InsecureSkipVerify {
			parts = append(parts, "ВНИМАНИЕ: сертификат сервера не проверяется")
		}
		d.Auth.LDAP.Detail = strings.Join(parts, " · ")
	case c.LDAP.Enabled():
		d.Auth.LDAP.Detail = "Настроен, но не запущен из-за ошибки в настройках — подробности в журнале сервера"
	default:
		d.Auth.LDAP.Detail = "Не настроен (LDAP_URL не задан)"
	}

	d.Auth.OIDC.Enabled = s.OIDC != nil
	switch {
	case s.OIDC != nil:
		d.Auth.OIDC.Detail = sysRedactRawURL(c.OIDC.IssuerURL) + " · клиент " + c.OIDC.ClientID +
			" · адрес возврата " + s.OIDC.RedirectURL()
	case c.OIDC.Enabled():
		d.Auth.OIDC.Detail = "Настроен, но не запущен из-за ошибки в настройках — подробности в журнале сервера"
	default:
		d.Auth.OIDC.Detail = "Не настроен (OIDC_ISSUER_URL не задан)"
	}
	writeJSON(w, http.StatusOK, d)
	return nil
}

// sysRedactURL drops credentials embedded in a URL.
func sysRedactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	c := *u
	if c.User != nil {
		c.User = url.User(c.User.Username())
	}
	return c.String()
}

func sysRedactRawURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(некорректный адрес)"
	}
	return sysRedactURL(u)
}

type testLDAPRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// testLDAPResponse is types.ts TestLdapResult.
type testLDAPResponse struct {
	OK    bool            `json:"ok"`
	Steps []auth.DiagStep `json:"steps"`
}

func (s *Server) handleTestLDAP(w http.ResponseWriter, r *http.Request) error {
	if s.LDAP == nil {
		if s.Config.LDAP.Enabled() {
			return errValidation("LDAP настроен с ошибкой и не запущен: исправьте параметры LDAP в настройках сервера "+
				"(подробности в журнале сервера) и перезапустите его", nil)
		}
		return errValidation("LDAP не настроен: задайте LDAP_URL и остальные параметры LDAP в настройках сервера и перезапустите его", nil)
	}
	var req testLDAPRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(w, r, &req); err != nil {
			return err
		}
	}
	username := strings.TrimSpace(req.Username)
	if len(username) > maxLoginLength {
		return errValidation("Слишком длинный логин", map[string]any{"field": "username"})
	}
	// A run with a password checks it against the directory and names the
	// reason of a refusal: without a limit it would be an unlimited password
	// guessing tool against any domain account. The budget belongs to the
	// tested account (several admins cannot multiply it) and to the admin (one
	// admin cannot spray many accounts); a correct password does not reset it.
	if req.Password != "" {
		now := s.now()
		acct := "ldaptest|" + s.loginLimitAccount(strings.ToLower(username))
		admin := "ldaptest-admin|" + strconv.FormatInt(currentUser(r).ID, 10)
		acctOK := s.loginLimiter.Allow(acct, now)
		adminOK := s.ipLimiter.Allow(admin, now)
		if !acctOK || !adminOK {
			if acctOK {
				s.loginLimiter.Release(acct, now)
			}
			if adminOK {
				s.ipLimiter.Release(admin, now)
			}
			wait := max(s.loginLimiter.RetryAfter(acct, now), s.ipLimiter.RetryAfter(admin, now))
			s.Log.Warn("ldap test with password rate limited", "user_id", currentUser(r).ID, "test_username", username)
			return rateLimitedError(w, wait, "Слишком много проверок пароля. Попробуйте через %d мин.")
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), ldapTestTimeout)
	defer cancel()
	steps := s.LDAP.Diagnose(ctx, username, req.Password)
	if steps == nil {
		steps = []auth.DiagStep{}
	}
	ok := len(steps) > 0
	for _, st := range steps {
		ok = ok && st.OK
	}
	s.Log.Info("ldap test run by admin", "user_id", currentUser(r).ID, "test_username", username,
		"with_password", req.Password != "", "ok", ok)
	// Visible to every admin in the audit log (never the password).
	what := "Проверка подключения к LDAP"
	if username != "" {
		what = "Проверка учётной записи LDAP «" + username + "»"
		if req.Password != "" {
			what += " с паролем"
		}
	}
	result := "успешно"
	if !ok {
		result = "с ошибкой"
	}
	s.audit(r, model.AuditEntry{Action: model.AuditLDAPTest, EntityType: "ldap", EntityID: username,
		NewValue: what + ": " + result})
	writeJSON(w, http.StatusOK, testLDAPResponse{OK: ok, Steps: steps})
	return nil
}
