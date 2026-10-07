package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-ldap/ldap/v3"

	"zoom-checker/internal/config"
	"zoom-checker/internal/model"
)

const (
	// ruleInChain is LDAP_MATCHING_RULE_IN_CHAIN (transitive membership, AD only;
	// OpenLDAP silently returns nothing for it).
	ruleInChain      = "1.2.840.113556.1.4.1941"
	groupPageSize    = 500
	maxLoginLength   = 256
	maxPasswordBytes = 1024
)

var errBadLogin = errors.New("auth: malformed username")

// errTooMany means the user filter matched more than one entry.
var errTooMany = errors.New("auth: ldap user filter matched several entries")

// LDAP authenticates users against an LDAP directory or Active Directory.
// It is safe for concurrent use; every call opens its own connections.
type LDAP struct {
	cfg       config.LDAPConfig
	log       *slog.Logger
	tlsConfig *tls.Config
	dials     atomic.Int64 // number of connection attempts (tests assert "no dial")
}

// NewLDAP validates cfg and loads the optional CA file. It does not connect.
func NewLDAP(cfg config.LDAPConfig, log *slog.Logger) (*LDAP, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "ldap" && u.Scheme != "ldaps") || u.Host == "" {
		return nil, fmt.Errorf("auth: LDAP_URL must look like ldaps://host:636 or ldap://host:389, got %q", cfg.URL)
	}
	if cfg.StartTLS && u.Scheme == "ldaps" {
		return nil, errors.New("auth: LDAP_START_TLS works only with ldap:// URLs (ldaps:// is already encrypted)")
	}
	if strings.TrimSpace(cfg.BaseDN) == "" {
		return nil, errors.New("auth: LDAP_BASE_DN is required")
	}
	if !strings.Contains(cfg.UserFilter, "{username}") {
		return nil, errors.New("auth: LDAP_USER_FILTER must contain {username}")
	}
	if cfg.BindDN == "" && (cfg.Preset != config.PresetAD || cfg.Domain == "") {
		return nil, errors.New("auth: LDAP_BIND_DN is required (or LDAP_PRESET=ad with LDAP_DOMAIN for direct bind)")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.UsernameAttr == "" {
		cfg.UsernameAttr = "uid"
		if cfg.Preset == config.PresetAD {
			cfg.UsernameAttr = "sAMAccountName"
		}
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}
	if cfg.CAFile != "" {
		if tc.RootCAs, err = loadCAPool(cfg.CAFile); err != nil {
			return nil, fmt.Errorf("auth: LDAP_CA_FILE: %w", err)
		}
	}
	if cfg.InsecureSkipVerify {
		tc.InsecureSkipVerify = true
		log.Warn("LDAP_INSECURE_SKIP_VERIFY=true: the LDAP server certificate is NOT verified; use this for testing only")
	}
	if u.Scheme == "ldap" && !cfg.StartTLS {
		log.Warn("LDAP connection is not encrypted, passwords travel in clear text; use ldaps:// or LDAP_START_TLS=true", "url", cfg.URL)
	}
	return &LDAP{cfg: cfg, log: log, tlsConfig: tc}, nil
}

// loadCAPool returns the system roots plus the PEM certificates from path.
func loadCAPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no PEM certificates found in %s", path)
	}
	return pool, nil
}

// Authenticate checks username/password against the directory and returns
// the user's identity with the mapped role.
//
// Errors: ErrInvalidCredentials (unknown user, wrong password, disabled or
// locked account, ambiguous match: never distinguishable by the caller),
// *AccessDeniedError (valid user without a configured group) and
// ErrUnavailable (network/TLS problems, broken service account or base DN).
func (l *LDAP) Authenticate(ctx context.Context, username, password string) (*Identity, error) {
	login, err := normalizeLogin(username)
	if err != nil {
		l.log.Info("ldap login rejected", "reason", "malformed username")
		return nil, ErrInvalidCredentials
	}
	// Must happen before any bind: Active Directory treats a simple bind with
	// an empty password as an anonymous bind and reports success.
	if unusablePassword(password) {
		l.log.Info("ldap login rejected", "username", login, "reason", "empty, oversized or NUL-containing password")
		return nil, ErrInvalidCredentials
	}
	ctx, cancel := context.WithTimeout(ctx, l.budget())
	defer cancel()

	conn, release, err := l.connect(ctx)
	if err != nil {
		l.log.Warn("ldap connect failed", "url", l.cfg.URL, "err", err)
		return nil, l.ctxErr(ctx, unavailable("ldap connect", err))
	}
	defer release()

	var entry *ldap.Entry
	if l.cfg.BindDN != "" {
		if err := conn.Bind(l.cfg.BindDN, l.cfg.BindPassword); err != nil {
			l.log.Error("ldap service account bind failed: check LDAP_BIND_DN and LDAP_BIND_PASSWORD", "bind_dn", l.cfg.BindDN, "err", err)
			return nil, l.ctxErr(ctx, unavailable("ldap service bind", err))
		}
		if entry, err = l.findUser(conn, login); err != nil {
			if errors.Is(err, ErrInvalidCredentials) {
				l.equalizeTiming(ctx)
			}
			return nil, l.ctxErr(ctx, err)
		}
		// Verify the password on a fresh connection so the service
		// connection keeps its own identity for the group lookup.
		if err := l.checkPassword(ctx, entry.DN, password, login); err != nil {
			return nil, l.ctxErr(ctx, err)
		}
	} else {
		// No service account (AD): bind directly as user@domain, then read
		// our own entry with the user's rights.
		if err := conn.Bind(l.directBindName(login), password); err != nil {
			return nil, l.ctxErr(ctx, l.bindError(err, login))
		}
		if entry, err = l.findUser(conn, login); err != nil {
			return nil, l.ctxErr(ctx, err)
		}
		if !boundAs(conn, entry) {
			l.log.Warn("ldap login rejected: the entry found is not the account that signed in; check LDAP_USER_FILTER", "username", login)
			return nil, ErrInvalidCredentials
		}
	}

	groups, err := l.groupsOf(conn, entry)
	if err != nil {
		return nil, l.ctxErr(ctx, err)
	}
	id := l.identity(entry, login, groups)
	role, ok := MapRole(groupValues(groups), l.cfg.AdminGroups, l.cfg.TeacherGroups)
	if !ok {
		l.log.Info("ldap login denied: user is in none of the configured groups",
			"username", id.Username, "groups", groupNames(groups))
		return nil, &AccessDeniedError{Username: id.Username, Required: l.requiredGroups(), Found: id.Groups}
	}
	id.Role = role
	return id, nil
}

// budget bounds a whole Authenticate/Diagnose call (several round trips).
func (l *LDAP) budget() time.Duration { return 3 * l.cfg.Timeout }

// ctxErr reports an expired/cancelled context instead of the "connection
// closed" error it causes (connections are closed when ctx is done).
func (l *LDAP) ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil && !errors.Is(err, ErrInvalidCredentials) && !errors.Is(err, ErrAccessDenied) {
		return unavailable("ldap", ctx.Err())
	}
	return err
}

// connect dials the server (with StartTLS when configured). The connection is
// closed when ctx is done; call release when finished.
func (l *LDAP) connect(ctx context.Context) (*ldap.Conn, func(), error) {
	l.dials.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	d := &net.Dialer{Timeout: l.cfg.Timeout}
	if dl, ok := ctx.Deadline(); ok {
		d.Deadline = dl
	}
	conn, err := ldap.DialURL(l.cfg.URL, ldap.DialWithDialer(d), ldap.DialWithTLSConfig(l.tlsConfig))
	if err != nil {
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	release := func() {
		stop()
		_ = conn.Close()
	}
	conn.SetTimeout(l.cfg.Timeout)
	if l.cfg.StartTLS {
		if err := conn.StartTLS(l.tlsConfig); err != nil {
			release()
			return nil, nil, fmt.Errorf("starttls: %w", err)
		}
	}
	return conn, release, nil
}

// unusablePassword reports passwords that must never reach a bind: blank ones
// (an unauthenticated bind succeeds on Windows AD), oversized ones, and ones
// with a NUL byte (C-string based servers and proxies may cut the password
// there, turning it into an empty one).
func unusablePassword(pw string) bool {
	return strings.TrimSpace(pw) == "" || len(pw) > maxPasswordBytes || strings.IndexByte(pw, 0) >= 0
}

// normalizeLogin trims the input, rejects control characters and strips a
// "DOMAIN\" prefix.
func normalizeLogin(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if !utf8.ValidString(s) {
		return "", errBadLogin
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", errBadLogin
		}
	}
	if i := strings.LastIndexByte(s, '\\'); i >= 0 {
		s = strings.TrimSpace(s[i+1:])
	}
	if s == "" || len(s) > maxLoginLength {
		return "", errBadLogin
	}
	return s, nil
}

// userFilter substitutes the escaped login into LDAP_USER_FILTER.
func (l *LDAP) userFilter(login string) string {
	return strings.ReplaceAll(l.cfg.UserFilter, "{username}", ldap.EscapeFilter(login))
}

func (l *LDAP) directBindName(login string) string {
	if strings.Contains(login, "@") {
		return login
	}
	return login + "@" + l.cfg.Domain
}

func (l *LDAP) timeLimit() int { return max(1, int(l.cfg.Timeout/time.Second)) }

func (l *LDAP) userAttrs() []string {
	attrs := []string{l.cfg.UsernameAttr, l.cfg.DisplayNameAttr, l.cfg.EmailAttr, l.cfg.IDAttr, "memberOf", "uid", "cn"}
	if l.cfg.Preset == config.PresetAD {
		attrs = append(attrs, "sAMAccountName", "userPrincipalName")
	}
	return dedupe(attrs)
}

// boundAs reports whether entry is the account the connection is bound as,
// using the WhoAmI extended operation ("u:DOMAIN\user" on AD, "dn:..." on
// others). After a direct user@domain bind this guards against a filter that
// finds a different account. Servers without WhoAmI are trusted.
func boundAs(conn *ldap.Conn, entry *ldap.Entry) bool {
	res, err := conn.WhoAmI(nil)
	if err != nil || res == nil {
		return true
	}
	authz := strings.TrimSpace(res.AuthzID)
	switch {
	case strings.HasPrefix(authz, "dn:"):
		return normalizeDN(authz[3:]) == normalizeDN(entry.DN)
	case strings.HasPrefix(authz, "u:"):
		name := authz[2:]
		if i := strings.LastIndexByte(name, '\\'); i >= 0 {
			name = name[i+1:]
		}
		return strings.EqualFold(name, entry.GetEqualFoldAttributeValue("sAMAccountName")) ||
			strings.EqualFold(name, entry.GetEqualFoldAttributeValue("userPrincipalName"))
	}
	return true
}

// lookupUser searches LDAP_USER_FILTER for login. For AD, an input like
// "user@<LDAP_DOMAIN>" that matches no userPrincipalName is retried as the
// sAMAccountName "user" (the UPN attribute is optional in AD).
func (l *LDAP) lookupUser(conn *ldap.Conn, login string) ([]*ldap.Entry, error) {
	entries, err := l.searchUser(conn, login)
	if err == nil && len(entries) == 0 && l.cfg.Preset == config.PresetAD && l.cfg.Domain != "" {
		if local, domain, ok := strings.Cut(login, "@"); ok && local != "" && strings.EqualFold(domain, l.cfg.Domain) {
			entries, err = l.searchUser(conn, local)
		}
	}
	return entries, err
}

func (l *LDAP) searchUser(conn *ldap.Conn, value string) ([]*ldap.Entry, error) {
	req := ldap.NewSearchRequest(l.cfg.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		2, l.timeLimit(), false, l.userFilter(value), l.userAttrs(), nil)
	// Samba/AD ignore a client size limit; enforce it client-side too.
	req.EnforceSizeLimit = true
	sr, err := conn.Search(req)
	if err != nil {
		if errors.Is(err, ldap.ErrSizeLimitExceeded) || ldap.IsErrorWithCode(err, ldap.LDAPResultSizeLimitExceeded) {
			return nil, errTooMany
		}
		return nil, err
	}
	if len(sr.Entries) > 1 {
		return nil, errTooMany
	}
	return sr.Entries, nil
}

// findUser returns exactly one entry or a classified error.
func (l *LDAP) findUser(conn *ldap.Conn, login string) (*ldap.Entry, error) {
	entries, err := l.lookupUser(conn, login)
	switch {
	case errors.Is(err, errTooMany):
		l.log.Warn("ldap login rejected: LDAP_USER_FILTER matched several entries", "username", login)
		return nil, ErrInvalidCredentials
	case ldap.IsErrorWithCode(err, ldap.LDAPResultNoSuchObject):
		l.log.Error("ldap user search failed: LDAP_BASE_DN does not exist", "base_dn", l.cfg.BaseDN, "err", err)
		return nil, unavailable("ldap user search", err)
	case err != nil:
		l.log.Error("ldap user search failed", "err", err)
		return nil, unavailable("ldap user search", err)
	case len(entries) == 0:
		l.log.Info("ldap login rejected", "username", login, "reason", "user not found")
		return nil, ErrInvalidCredentials
	}
	return entries[0], nil
}

// checkPassword binds as dn on a fresh connection.
func (l *LDAP) checkPassword(ctx context.Context, dn, password, login string) error {
	uc, release, err := l.connect(ctx)
	if err != nil {
		l.log.Warn("ldap connect failed", "url", l.cfg.URL, "err", err)
		return unavailable("ldap connect", err)
	}
	defer release()
	if err := uc.Bind(dn, password); err != nil {
		return l.bindError(err, login)
	}
	return nil
}

// noSuchUserRDN names the entry equalizeTiming binds as; it is not expected
// to exist (and a random password is used even if it does).
const noSuchUserRDN = "cn=zoom-checker-no-such-user"

// equalizeTiming makes a rejected search cost what a wrong password costs: a
// fresh connection and a failing bind. Without it an unknown (or, with the AD
// preset, disabled) user answers in half the time of an existing one, which
// lets anyone enumerate accounts. The result is ignored.
func (l *LDAP) equalizeTiming(ctx context.Context) {
	c, release, err := l.connect(ctx)
	if err != nil {
		return
	}
	defer release()
	_ = c.Bind(noSuchUserRDN+","+l.cfg.BaseDN, randomString(18))
}

// bindError classifies a failed user bind: wrong password, disabled or locked
// account etc. become ErrInvalidCredentials (the reason is logged, never
// returned); network and server-side outages become ErrUnavailable.
func (l *LDAP) bindError(err error, login string) error {
	var le *ldap.Error
	if !errors.As(err, &le) {
		l.log.Warn("ldap bind failed", "username", login, "err", err)
		return unavailable("ldap bind", err)
	}
	switch le.ResultCode {
	case ldap.LDAPResultStrongAuthRequired, ldap.LDAPResultConfidentialityRequired:
		l.log.Error("ldap server requires an encrypted connection: use ldaps:// or LDAP_START_TLS=true", "err", err)
		return unavailable("ldap bind", err)
	case ldap.ErrorNetwork, ldap.LDAPResultBusy, ldap.LDAPResultUnavailable, ldap.LDAPResultServerDown, ldap.LDAPResultTimeout:
		l.log.Warn("ldap bind failed", "username", login, "err", err)
		return unavailable("ldap bind", err)
	}
	l.log.Info("ldap login rejected", "username", login, "reason", bindReason(le).en)
	return ErrInvalidCredentials
}

type reason struct{ en, ru string }

// AD reports why a bind failed as "data <hex>" inside result 49.
var (
	adDataCode = regexp.MustCompile(`(?i)\bdata ([0-9a-f]{3,8})\b`)
	adReasons  = map[string]reason{
		"525": {"user not found", "пользователь не найден"},
		"52e": {"invalid credentials", "неверный пароль"},
		"530": {"logon not permitted at this time", "вход в это время запрещён"},
		"531": {"logon not permitted from this workstation", "вход с этого компьютера запрещён"},
		"532": {"password expired", "срок действия пароля истёк"},
		"533": {"account disabled", "учётная запись отключена"},
		"701": {"account expired", "срок действия учётной записи истёк"},
		"773": {"user must reset password", "пользователь должен сменить пароль"},
		"775": {"account locked out", "учётная запись заблокирована"},
	}
)

func bindReason(le *ldap.Error) reason {
	if le.ResultCode == ldap.LDAPResultInvalidCredentials {
		if m := adDataCode.FindStringSubmatch(le.Err.Error()); m != nil {
			code := strings.ToLower(m[1])
			if r, ok := adReasons[code]; ok {
				return reason{r.en + " (AD " + code + ")", r.ru + " (код AD " + code + ")"}
			}
			return reason{"invalid credentials (AD " + code + ")", "вход отклонён (код AD " + code + ")"}
		}
		return reason{"invalid credentials", "неверный пароль"}
	}
	name := ldap.LDAPResultCodeMap[le.ResultCode]
	return reason{fmt.Sprintf("LDAP result %d %s", le.ResultCode, name), fmt.Sprintf("сервер отказал во входе (код LDAP %d %s)", le.ResultCode, name)}
}

// ldapGroup is one group the user belongs to.
type ldapGroup struct{ DN, CN string }

// groupsOf collects the user's groups: memberOf plus, depending on the
// preset, a transitive search (AD) or a member/uniqueMember/memberUid search
// (OpenLDAP, whose memberOf overlay is often missing or partial).
func (l *LDAP) groupsOf(conn *ldap.Conn, e *ldap.Entry) ([]ldapGroup, error) {
	var groups []ldapGroup
	for _, dn := range e.GetEqualFoldAttributeValues("memberOf") {
		groups = append(groups, ldapGroup{DN: dn})
	}
	var filter string
	switch {
	case l.cfg.Preset == config.PresetAD && l.cfg.NestedGroups:
		filter = fmt.Sprintf("(&(objectClass=group)(member:%s:=%s))", ruleInChain, ldap.EscapeFilter(e.DN))
	case l.cfg.Preset == config.PresetOpenLDAP:
		dn := ldap.EscapeFilter(e.DN)
		filter = "(&(objectClass=groupOfNames)(member=" + dn + "))(&(objectClass=groupOfUniqueNames)(uniqueMember=" + dn + "))"
		if uid := e.GetEqualFoldAttributeValue("uid"); uid != "" {
			filter += "(&(objectClass=posixGroup)(memberUid=" + ldap.EscapeFilter(uid) + "))"
		}
		filter = "(|" + filter + ")"
	}
	if filter != "" {
		req := ldap.NewSearchRequest(groupSearchBase(l.cfg.BaseDN), ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
			0, l.timeLimit(), false, filter, []string{"cn"}, nil)
		sr, err := conn.SearchWithPaging(req, groupPageSize)
		switch {
		case err != nil && isNetworkError(err):
			l.log.Warn("ldap group search failed", "err", err)
			return nil, unavailable("ldap group search", err)
		case err != nil:
			// Keep memberOf (and any partial result) rather than locking everyone out.
			l.log.Warn("ldap group search failed; using memberOf only", "err", err)
		}
		if sr != nil {
			for _, g := range sr.Entries {
				groups = append(groups, ldapGroup{DN: g.DN, CN: g.GetEqualFoldAttributeValue("cn")})
			}
		}
	}
	// Dedupe by DN and fill in CNs.
	seen := map[string]int{}
	out := make([]ldapGroup, 0, len(groups))
	for _, g := range groups {
		if strings.TrimSpace(g.DN) == "" {
			continue
		}
		k := normalizeDN(g.DN)
		if i, ok := seen[k]; ok {
			if out[i].CN == "" {
				out[i].CN = g.CN
			}
			continue
		}
		if g.CN == "" {
			g.CN, _ = firstRDNValue(g.DN)
		}
		seen[k] = len(out)
		out = append(out, g)
	}
	return out, nil
}

// groupSearchBase widens the base DN to its domain part (the trailing DC=
// components), so groups kept outside a narrow LDAP_BASE_DN such as
// OU=Teachers,DC=corp,DC=local are still found. Without DC components the
// base DN is used as is.
func groupSearchBase(baseDN string) string {
	dn, err := ldap.ParseDN(baseDN)
	if err != nil {
		return baseDN
	}
	i := len(dn.RDNs)
	for i > 0 && len(dn.RDNs[i-1].Attributes) == 1 && strings.EqualFold(dn.RDNs[i-1].Attributes[0].Type, "dc") {
		i--
	}
	if i == len(dn.RDNs) || i == 0 {
		return baseDN
	}
	parts := make([]string, 0, len(dn.RDNs)-i)
	for _, r := range dn.RDNs[i:] {
		parts = append(parts, r.Attributes[0].Type+"="+ldap.EscapeDN(r.Attributes[0].Value))
	}
	return strings.Join(parts, ",")
}

func isNetworkError(err error) bool {
	return ldap.IsErrorAnyOf(err, ldap.ErrorNetwork, ldap.LDAPResultBusy, ldap.LDAPResultUnavailable,
		ldap.LDAPResultServerDown, ldap.LDAPResultTimeout) && !errors.Is(err, ldap.ErrSizeLimitExceeded)
}

// groupValues returns the values MapRole matches against: the group DNs only.
// A CN rule is compared with the DN's first RDN; passing bare CNs would let
// OIDC-style path matching apply to them (a group "x/Admins" passing for
// "Admins"), and a multi-valued cn attribute could differ from the RDN.
func groupValues(groups []ldapGroup) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		out = append(out, g.DN)
	}
	return out
}

// groupDNs lists the groups by full DN (the name when the DN is unknown).
func groupDNs(groups []ldapGroup) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		out = append(out, cmpOr(g.DN, g.CN))
	}
	return out
}

func groupNames(groups []ldapGroup) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		out = append(out, cmpOr(g.CN, g.DN))
	}
	return out
}

func (l *LDAP) requiredGroups() []string {
	return dedupe(slices.Concat(l.cfg.TeacherGroups, l.cfg.AdminGroups))
}

func (l *LDAP) identity(e *ldap.Entry, login string, groups []ldapGroup) *Identity {
	username := strings.ToLower(strings.TrimSpace(e.GetEqualFoldAttributeValue(l.cfg.UsernameAttr)))
	if username == "" {
		username = strings.ToLower(login)
	}
	display := strings.TrimSpace(cmpOr(e.GetEqualFoldAttributeValue(l.cfg.DisplayNameAttr), e.GetEqualFoldAttributeValue("cn"), username))
	dns := make([]string, 0, len(groups))
	for _, g := range groups {
		dns = append(dns, g.DN)
	}
	return &Identity{
		Source:      model.AuthLDAP,
		ExternalID:  l.externalID(e),
		Username:    username,
		DisplayName: display,
		Email:       strings.TrimSpace(e.GetEqualFoldAttributeValue(l.cfg.EmailAttr)),
		Groups:      dns,
	}
}

// externalID returns the stable directory ID of the entry (objectGUID,
// entryUUID, ipaUniqueID, ...), falling back to the normalized DN.
func (l *LDAP) externalID(e *ldap.Entry) string {
	if attr := l.cfg.IDAttr; attr != "" {
		raw := e.GetEqualFoldRawAttributeValue(attr)
		switch {
		case len(raw) == 16 && strings.EqualFold(attr, "objectGUID"):
			return formatGUID(raw)
		case len(raw) > 0 && utf8.Valid(raw) && !slices.ContainsFunc([]rune(string(raw)), unicode.IsControl):
			return strings.TrimSpace(string(raw))
		case len(raw) > 0:
			return hex.EncodeToString(raw)
		}
	}
	return normalizeDN(e.DN)
}

// formatGUID renders a 16-byte AD objectGUID in the canonical form; the
// first three groups are stored little-endian.
func formatGUID(b []byte) string {
	if len(b) != 16 {
		return hex.EncodeToString(b)
	}
	return fmt.Sprintf("%08x-%04x-%04x-%x-%x",
		binary.LittleEndian.Uint32(b[0:4]), binary.LittleEndian.Uint16(b[4:6]), binary.LittleEndian.Uint16(b[6:8]),
		b[8:10], b[10:16])
}

func cmpOr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// Diagnose checks the LDAP setup step by step: connection, service account,
// user search, password (only when one is given), groups and resulting role.
// It stops at the first failing step. Details are Russian and contain no
// passwords; it is meant for the check-ldap CLI and the admin UI.
func (l *LDAP) Diagnose(ctx context.Context, username, password string) []DiagStep {
	var d diagSteps
	ctx, cancel := context.WithTimeout(ctx, l.budget())
	defer cancel()

	conn, release, err := l.connect(ctx)
	if err != nil {
		d.add("Подключение к серверу", false, describeNetError(err, l.cfg.Timeout))
		return d
	}
	defer release()
	d.add("Подключение к серверу", true, fmt.Sprintf("Соединение с %s установлено (%s).", l.cfg.URL, l.transportNote()))

	direct := l.cfg.BindDN == ""
	if direct {
		d.add("Служебная учётная запись", true, fmt.Sprintf("Не задана: пользователь входит напрямую как имя@%s.", l.cfg.Domain))
	} else if err := conn.Bind(l.cfg.BindDN, l.cfg.BindPassword); err != nil {
		d.add("Вход служебной учётной записи", false, l.describeServiceBind(err))
		return d
	} else {
		d.add("Вход служебной учётной записи", true, fmt.Sprintf("Вход выполнен как %s.", l.cfg.BindDN))
	}

	if strings.TrimSpace(username) == "" {
		return d
	}
	login, err := normalizeLogin(username)
	if err != nil {
		d.add("Поиск пользователя", false, "Имя пользователя пустое или содержит недопустимые символы.")
		return d
	}
	if password != "" && unusablePassword(password) {
		d.add("Проверка пароля", false, "Такой пароль не допускается: он состоит из одних пробелов, слишком длинный или содержит нулевой символ.")
		return d
	}
	if direct {
		if password == "" {
			d.add("Поиск пользователя", false, "Без служебной учётной записи (LDAP_BIND_DN) найти пользователя можно только с его паролем.")
			return d
		}
		ok, detail := l.describeUserBind(conn.Bind(l.directBindName(login), password))
		if !d.add("Проверка пароля", ok, detail) {
			return d
		}
	}

	entries, err := l.lookupUser(conn, login)
	switch {
	case errors.Is(err, errTooMany):
		d.add("Поиск пользователя", false, "Найдено несколько записей с таким именем. Уточните фильтр LDAP_USER_FILTER.")
		return d
	case err != nil:
		d.add("Поиск пользователя", false, l.describeSearchError(err))
		return d
	case len(entries) == 0:
		d.add("Поиск пользователя", false, fmt.Sprintf("Пользователь %q не найден (или отключён). База поиска: %s, фильтр: %s.",
			login, l.cfg.BaseDN, l.userFilter(login)))
		return d
	}
	entry := entries[0]
	d.add("Поиск пользователя", true, "Найден: "+entry.DN)

	if !direct && password != "" {
		uc, urelease, err := l.connect(ctx)
		if err != nil {
			d.add("Проверка пароля", false, describeNetError(err, l.cfg.Timeout))
			return d
		}
		ok, detail := l.describeUserBind(uc.Bind(entry.DN, password))
		urelease()
		if !d.add("Проверка пароля", ok, detail) {
			return d
		}
	}

	groups, err := l.groupsOf(conn, entry)
	if err != nil {
		d.add("Группы", false, "Не удалось получить группы: "+describeNetError(err, l.cfg.Timeout))
		return d
	}
	// Full DNs, not just names: LDAP_ADMIN_GROUPS should hold a DN (a bare
	// name matches a group of that name anywhere in the directory), and this
	// is where an administrator copies it from.
	names := groupDNs(groups)
	if len(names) == 0 {
		d.add("Группы", true, "Пользователь не состоит ни в одной группе.")
	} else {
		shown := names
		if len(shown) > 30 {
			shown = append(slices.Clone(shown[:30]), "…")
		}
		d.add("Группы", true, fmt.Sprintf("Найдено групп: %d — %s.", len(names), strings.Join(shown, "; ")))
	}

	role, ok := MapRole(groupValues(groups), l.cfg.AdminGroups, l.cfg.TeacherGroups)
	d.add("Роль в Zoom Checker", ok, roleDetail(role, ok, l.requiredGroups(), "LDAP_ADMIN_GROUPS", "LDAP_TEACHER_GROUPS"))
	return d
}

func (l *LDAP) transportNote() string {
	if !l.cfg.StartTLS && !strings.HasPrefix(strings.ToLower(l.cfg.URL), "ldaps://") {
		return "без шифрования: пароли передаются открытым текстом, используйте ldaps:// или LDAP_START_TLS=true"
	}
	s := "шифрование TLS"
	if l.cfg.StartTLS {
		s = "шифрование StartTLS"
	}
	if l.cfg.InsecureSkipVerify {
		s += ", проверка сертификата отключена (LDAP_INSECURE_SKIP_VERIFY)"
	}
	return s
}

func (l *LDAP) describeServiceBind(err error) string {
	var le *ldap.Error
	switch {
	case ldap.IsErrorWithCode(err, ldap.ErrorEmptyPassword):
		return "Не задан пароль служебной учётной записи (LDAP_BIND_PASSWORD)."
	case ldap.IsErrorAnyOf(err, ldap.LDAPResultStrongAuthRequired, ldap.LDAPResultConfidentialityRequired):
		return "Сервер требует зашифрованное соединение: используйте ldaps:// или LDAP_START_TLS=true."
	case ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) && errors.As(err, &le):
		return "Неверный LDAP_BIND_DN или LDAP_BIND_PASSWORD (" + bindReason(le).ru + ")."
	case isNetworkError(err):
		return describeNetError(err, l.cfg.Timeout)
	}
	return "Сервер отказал во входе: " + err.Error()
}

func (l *LDAP) describeUserBind(err error) (bool, string) {
	var le *ldap.Error
	switch {
	case err == nil:
		return true, "Пароль верный."
	case ldap.IsErrorAnyOf(err, ldap.LDAPResultStrongAuthRequired, ldap.LDAPResultConfidentialityRequired):
		return false, "Сервер требует зашифрованное соединение: используйте ldaps:// или LDAP_START_TLS=true."
	case isNetworkError(err):
		return false, describeNetError(err, l.cfg.Timeout)
	case errors.As(err, &le):
		return false, "Вход отклонён: " + bindReason(le).ru + "."
	}
	return false, "Вход отклонён: " + err.Error()
}

func (l *LDAP) describeSearchError(err error) string {
	switch {
	case ldap.IsErrorWithCode(err, ldap.LDAPResultNoSuchObject):
		return fmt.Sprintf("База поиска %q не найдена на сервере. Проверьте LDAP_BASE_DN.", l.cfg.BaseDN)
	case ldap.IsErrorAnyOf(err, ldap.LDAPResultInsufficientAccessRights, ldap.LDAPResultOperationsError):
		return "Недостаточно прав для поиска. Проверьте служебную учётную запись (LDAP_BIND_DN)."
	case ldap.IsErrorWithCode(err, ldap.LDAPResultFilterError) || ldap.IsErrorWithCode(err, ldap.ErrorFilterCompile):
		return "Ошибка в фильтре LDAP_USER_FILTER: " + err.Error()
	case isNetworkError(err):
		return describeNetError(err, l.cfg.Timeout)
	}
	return "Поиск не выполнен: " + err.Error()
}

// roleDetail explains the role mapping result in Russian.
func roleDetail(role model.Role, ok bool, required []string, adminVar, teacherVar string) string {
	switch {
	case ok && role == model.RoleAdmin:
		return "Администратор (совпадение с " + adminVar + ")."
	case ok:
		return "Преподаватель (совпадение с " + teacherVar + " или " + teacherVar + "=*)."
	case len(required) == 0:
		return "Нет доступа: " + teacherVar + " и " + adminVar + " не заданы, поэтому вход этим способом никому не разрешён."
	}
	return "Нет доступа: нужна одна из групп/ролей: " + strings.Join(required, ", ") + "."
}
