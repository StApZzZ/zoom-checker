// Package auth verifies who a user is: local password hashing, LDAP / Active
// Directory binds, OpenID Connect (Keycloak) logins, role mapping and login
// rate limiting. It never touches the database: LDAP and OIDC produce an
// Identity, and HTTP session handling and user records live elsewhere.
package auth

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/go-ldap/ldap/v3"

	"zoom-checker/internal/model"
)

// Identity is a user authenticated by an external provider (LDAP or OIDC).
type Identity struct {
	Source model.AuthSource
	// ExternalID is the stable provider-side ID: AD objectGUID, entryUUID,
	// ipaUniqueID (falling back to the normalized DN) or the OIDC "sub".
	ExternalID  string
	Username    string // lower-case login name
	DisplayName string
	Email       string
	Groups      []string // raw roles/groups found, for diagnostics
	Role        model.Role
}

var (
	// ErrInvalidCredentials is the generic login failure. It never tells
	// whether the user exists, is disabled or typed a wrong password.
	ErrInvalidCredentials = errors.New("auth: invalid username or password")
	// ErrAccessDenied means the user authenticated but has none of the
	// configured roles/groups. Returned errors are *AccessDeniedError.
	ErrAccessDenied = errors.New("auth: access denied")
	// ErrUnavailable means the IdP / directory could not be reached or is
	// misconfigured (network error, TLS error, bad service account, ...).
	ErrUnavailable = errors.New("auth: identity provider unavailable")
	// ErrOIDCFlow means the browser round trip to the OpenID provider could
	// not be completed: missing, expired or tampered flow cookie, state or
	// nonce mismatch, an error returned by the provider, an authorization code
	// that was rejected or a token that failed validation. The user should
	// simply start the login again.
	ErrOIDCFlow = errors.New("auth: oidc login could not be completed")
	// ErrOIDCNoFlowCookie is the ErrOIDCFlow of a callback that arrived
	// without the login's flow cookie. The usual cause: the login was started
	// on another address than PUBLIC_URL (an IP, another DNS name), so the
	// cookie belongs to that host while the provider returns to PUBLIC_URL.
	ErrOIDCNoFlowCookie = fmt.Errorf("%w: login cookie missing", ErrOIDCFlow)
)

// AccessDeniedError is returned when a user authenticated successfully but has
// none of the required roles/groups. errors.Is(err, ErrAccessDenied) is true.
type AccessDeniedError struct {
	Username string
	// Required lists the configured roles/groups that would grant access
	// (admin and teacher rules). Empty when nothing is configured.
	Required []string
	// Found lists the roles/groups the user actually has (diagnostics).
	Found []string
}

func (e *AccessDeniedError) Error() string {
	if len(e.Required) == 0 {
		return fmt.Sprintf("auth: access denied for %q: no roles/groups are configured to grant access", e.Username)
	}
	return fmt.Sprintf("auth: access denied for %q: none of the required roles/groups %q", e.Username, e.Required)
}

// Is makes errors.Is(err, ErrAccessDenied) work.
func (e *AccessDeniedError) Is(target error) bool { return target == ErrAccessDenied }

// UserMessage turns an error from this package into a short Russian message
// that is safe to show on the login page.
func UserMessage(err error) string {
	var denied *AccessDeniedError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &denied):
		if len(denied.Required) == 0 {
			return "Вход этим способом пока никому не разрешён. Обратитесь к администратору."
		}
		return "У вас нет доступа к Zoom Checker. Попросите администратора добавить вас в одну из групп: " +
			strings.Join(denied.Required, ", ") + "."
	case errors.Is(err, ErrAccessDenied):
		return "У вас нет доступа к Zoom Checker. Обратитесь к администратору."
	case errors.Is(err, ErrInvalidCredentials):
		return "Неверный логин или пароль."
	case errors.Is(err, ErrUnavailable):
		return "Сервис входа сейчас недоступен. Попробуйте ещё раз через несколько минут или обратитесь к администратору."
	case errors.Is(err, ErrOIDCFlow):
		return "Не удалось завершить вход. Попробуйте ещё раз."
	default:
		return "Не удалось войти. Попробуйте ещё раз."
	}
}

// unavailable wraps err so that errors.Is(result, ErrUnavailable) holds.
func unavailable(what string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrUnavailable, what, err)
}

// MapRole maps the roles/groups a user has to an application role.
//
// Admin wins if any admin rule matches; otherwise teacher if any teacher rule
// matches or teacherRules contains "*"; otherwise access is denied (ok=false).
// "*" is deliberately NOT a wildcard for admin rules.
//
// Matching is case-insensitive:
//   - a value containing "=" that does not start with "/" is an LDAP group DN.
//     It matches a DN rule (one containing "=") equal to it after
//     normalization (case, spaces around ',' '=' '+', escaping), or a plain
//     rule equal to its first RDN value (the CN). Path semantics never apply
//     to DNs, so a group named "x/Admins" or "/Admins" does not pass for "Admins";
//   - any other value is an OIDC role or group: a plain rule "x" matches "x",
//     "/x" and a group path whose last segment is "x" (e.g. "/staff/x"); a rule
//     with a slash ("staff/x" or "/staff/x") matches that full path only.
//     DN rules never match these values.
//
// LDAP callers pass group DNs only, never bare CNs (see groupValues).
func MapRole(values []string, adminRules, teacherRules []string) (model.Role, bool) {
	if anyRuleMatches(values, adminRules) {
		return model.RoleAdmin, true
	}
	if slices.ContainsFunc(teacherRules, func(r string) bool { return strings.TrimSpace(r) == "*" }) ||
		anyRuleMatches(values, teacherRules) {
		return model.RoleTeacher, true
	}
	return "", false
}

func anyRuleMatches(values, rules []string) bool {
	for _, r := range rules {
		for _, v := range values {
			if ruleMatches(r, v) {
				return true
			}
		}
	}
	return false
}

func ruleMatches(rule, value string) bool {
	rule, value = strings.TrimSpace(rule), strings.TrimSpace(value)
	if rule == "" || value == "" || rule == "*" {
		return false
	}
	dnRule := strings.Contains(rule, "=")
	r := strings.ToLower(strings.TrimPrefix(rule, "/"))
	if !dnRule && r == "" {
		return false // "/" alone would match every path ending in "/"
	}
	if strings.Contains(value, "=") && !strings.HasPrefix(value, "/") {
		if dnRule {
			return normalizeDN(rule) == normalizeDN(value)
		}
		cn, ok := firstRDNValue(value)
		return ok && strings.ToLower(strings.TrimSpace(cn)) == r
	}
	if dnRule {
		return false
	}
	v := strings.ToLower(value)
	if strings.TrimPrefix(v, "/") == r {
		return true
	}
	if !strings.Contains(r, "/") {
		if i := strings.LastIndexByte(v, '/'); i >= 0 && v[i+1:] == r {
			return true
		}
	}
	return false
}

var dnSeparatorSpaces = regexp.MustCompile(`\s*([,=+])\s*`)

// normalizeDN returns a canonical lower-case form of a DN so that
// "CN=Teachers, OU=Groups,DC=corp" and "cn=teachers,ou=groups,dc=corp" compare
// equal. Unparseable input falls back to a plain textual normalization.
func normalizeDN(s string) string {
	s = strings.TrimSpace(s)
	dn, err := ldap.ParseDN(s)
	if err != nil || len(dn.RDNs) == 0 {
		return strings.ToLower(dnSeparatorSpaces.ReplaceAllString(s, "$1"))
	}
	var b strings.Builder
	for i, rdn := range dn.RDNs {
		if i > 0 {
			b.WriteByte(',')
		}
		for j, a := range rdn.Attributes {
			if j > 0 {
				b.WriteByte('+')
			}
			b.WriteString(strings.ToLower(strings.TrimSpace(a.Type)))
			b.WriteByte('=')
			b.WriteString(strings.ToLower(ldap.EscapeDN(a.Value)))
		}
	}
	return b.String()
}

// firstRDNValue returns the value of the first RDN of a DN ("Teachers" for
// "CN=Teachers,OU=Groups,DC=corp"), preferring a cn attribute in multi-valued RDNs.
func firstRDNValue(s string) (string, bool) {
	dn, err := ldap.ParseDN(strings.TrimSpace(s))
	if err != nil || len(dn.RDNs) == 0 || len(dn.RDNs[0].Attributes) == 0 {
		return "", false
	}
	attrs := dn.RDNs[0].Attributes
	for _, a := range attrs {
		if strings.EqualFold(a.Type, "cn") {
			return a.Value, true
		}
	}
	return attrs[0].Value, true
}

// dedupe removes empty strings and case-insensitive duplicates, keeping order.
func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		k := strings.ToLower(s)
		if s == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}
