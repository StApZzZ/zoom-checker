// Package config loads service configuration from environment variables
// (optionally pre-populated from a dotenv-style file). See docs/architecture.md §3.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TLS modes.
const (
	TLSOff  = "off"
	TLSAuto = "auto"
	TLSFile = "file"
)

// LDAP presets.
const (
	PresetAD       = "ad"
	PresetOpenLDAP = "openldap"
	PresetFreeIPA  = "freeipa"
)

// Config is the full service configuration.
type Config struct {
	PublicURL   *url.URL
	ListenAddr  string
	TLSMode     string
	TLSEmail    string
	TLSCertFile string
	TLSKeyFile  string
	// TLSACMECA is "production", "staging" or a directory URL.
	TLSACMECA string
	// TLSACMEProfile is the ACME profile; empty = automatic ("shortlived" for IP addresses,
	// which Let's Encrypt requires, CA default otherwise).
	TLSACMEProfile string
	// TLSExtraHosts are additional names/IPs to obtain certificates for.
	TLSExtraHosts []string

	DataDir     string
	DatabaseURL string
	Timezone    *time.Location

	ZoomWebhookSecret string
	// ZoomWebhookMaxAge rejects signed requests whose x-zm-request-timestamp is older.
	// Zoom retries failed deliveries after 5, 25 and 85 minutes and a 4xx answer stops
	// retries for good, so the window must cover the retry schedule; replays inside it
	// are harmless because processing is idempotent (dedupe).
	ZoomWebhookMaxAge time.Duration

	AdminUsername string
	AdminPassword string

	// SessionTTL is the absolute lifetime of a login (no sliding expiry).
	SessionTTL time.Duration
	// ExternalSessionTTL caps the lifetime of LDAP/OIDC logins: their role and
	// disabled state are only synced at login, so a user removed from the
	// directory groups or disabled there must log in again within this time.
	ExternalSessionTTL time.Duration
	TrustedProxies     []*net.IPNet

	LogLevel  string
	LogFormat string

	BackupKeep          int
	EventsRetentionDays int

	LocalAuthEnabled bool
	OIDC             OIDCConfig
	LDAP             LDAPConfig
}

// OIDCConfig configures login through an OpenID Connect provider (Keycloak).
type OIDCConfig struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	Scopes       []string
	ButtonLabel  string
	AdminRoles   []string
	TeacherRoles []string // "*" = any authenticated user; empty = nobody
	RolesClaim   string   // optional dot path
	CAFile       string
}

// Enabled reports whether OIDC login is configured.
func (c OIDCConfig) Enabled() bool { return c.IssuerURL != "" }

// LDAPConfig configures login through LDAP / Active Directory.
type LDAPConfig struct {
	URL                string
	StartTLS           bool
	CAFile             string
	InsecureSkipVerify bool
	Preset             string
	BindDN             string
	BindPassword       string
	Domain             string
	BaseDN             string
	UserFilter         string // contains {username}
	DisplayNameAttr    string
	EmailAttr          string
	IDAttr             string
	UsernameAttr       string
	AdminGroups        []string // DNs or CNs
	TeacherGroups      []string // DNs or CNs; "*" = any user who can bind; empty = nobody
	NestedGroups       bool
	Timeout            time.Duration
}

// Enabled reports whether LDAP login is configured.
func (c LDAPConfig) Enabled() bool { return c.URL != "" }

// Getenv is the lookup function used by Load (os.Getenv in production).
type Getenv func(string) string

// LoadEnvFile reads KEY=VALUE lines (dotenv style: '#' comments, optional
// "export " prefix, optional single/double quotes) and sets variables that are
// not already present in the process environment. When a key appears more than
// once, the last line wins, as with systemd, Docker and the shell: settings
// appended to the end of the file (echo KEY=VALUE >> file) take effect. Nothing
// is set when the file has a malformed line.
func LoadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 0
	vals := map[string]string{}
	var order []string
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("%s:%d: ожидается строка вида КЛЮЧ=значение", path, lineNo)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return fmt.Errorf("%s:%d: ожидается строка вида КЛЮЧ=значение", path, lineNo)
		}
		val = strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' && val[len(val)-1] == '"' || val[0] == '\'' && val[len(val)-1] == '\'') {
			val = unquoteEnv(val[0], val[1:len(val)-1])
		} else if q, ok := quotedBeforeComment(val); ok {
			val = unquoteEnv(val[0], q) // KEY="value"  # comment
		} else if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i]) // inline comment
		}
		if _, seen := vals[key]; !seen {
			order = append(order, key)
		}
		vals[key] = val
	}
	if err := sc.Err(); err != nil {
		return err
	}
	for _, key := range order {
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, vals[key]); err != nil {
				return err
			}
		}
	}
	return nil
}

// unquoteEnv returns the inside of a quoted env value: escapes (\n, \", \\)
// apply between double quotes, single quotes are literal.
func unquoteEnv(quote byte, inner string) string {
	if quote == '"' {
		return strings.NewReplacer(`\n`, "\n", `\"`, `"`, `\\`, `\`).Replace(inner)
	}
	return inner
}

// quotedBeforeComment recognizes a quoted value followed by an inline comment
// (KEY="secret value"  # from the Zoom app) and returns what is between the
// quotes. Without this the quotes would stay in the value: a quoted Zoom
// secret token would then fail every webhook signature while check-config
// still reports the settings as valid.
func quotedBeforeComment(val string) (string, bool) {
	if len(val) < 2 || (val[0] != '"' && val[0] != '\'') {
		return "", false
	}
	quote := val[0]
	for i := 1; i < len(val); i++ {
		switch {
		case quote == '"' && val[i] == '\\':
			i++ // escaped character
		case val[i] == quote:
			// Only whitespace and then "#" may follow the closing quote.
			rest := val[i+1:]
			if rest != "" && (rest[0] == ' ' || rest[0] == '\t') && strings.HasPrefix(strings.TrimLeft(rest, " \t"), "#") {
				return val[1:i], true
			}
			return "", false
		}
	}
	return "", false
}

// Load builds the configuration from env and validates it.
func Load(getenv Getenv) (*Config, error) {
	var errs []error
	e := envReader{getenv: getenv, errs: &errs}

	c := &Config{}
	pub := e.str("PUBLIC_URL", "http://localhost:8080")
	u, err := url.Parse(strings.TrimRight(pub, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, fmt.Errorf("PUBLIC_URL: укажите полный адрес, начиная с https:// (или http://), например https://attendance.example.com; сейчас %q", pub))
		u, _ = url.Parse("http://localhost:8080")
	}
	c.PublicURL = u
	c.ListenAddr = e.str("LISTEN_ADDR", ":8080")
	c.TLSMode = strings.ToLower(e.str("TLS_MODE", TLSOff))
	c.TLSEmail = e.str("TLS_EMAIL", "")
	c.TLSCertFile = e.str("TLS_CERT_FILE", "")
	c.TLSKeyFile = e.str("TLS_KEY_FILE", "")
	c.TLSACMECA = e.str("TLS_ACME_CA", "production")
	c.TLSACMEProfile = e.str("TLS_ACME_PROFILE", "")
	c.TLSExtraHosts = e.list("TLS_EXTRA_HOSTS", ",")
	switch c.TLSMode {
	case TLSOff:
	case TLSAuto:
		if u.Scheme != "https" {
			errs = append(errs, errors.New("TLS_MODE=auto: PUBLIC_URL должен начинаться с https://"))
		}
	case TLSFile:
		if c.TLSCertFile == "" || c.TLSKeyFile == "" {
			errs = append(errs, errors.New("TLS_MODE=file: укажите пути к сертификату и ключу в TLS_CERT_FILE и TLS_KEY_FILE"))
		}
	default:
		errs = append(errs, fmt.Errorf("TLS_MODE: допустимы значения off, auto или file; сейчас %q", c.TLSMode))
	}

	c.DataDir = e.str("DATA_DIR", "./data")
	c.DatabaseURL = e.str("DATABASE_URL", "sqlite://"+filepath.ToSlash(filepath.Join(c.DataDir, "zoom-checker.db")))
	tzName := e.str("TIMEZONE", "Europe/Moscow")
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		errs = append(errs, fmt.Errorf("TIMEZONE: неизвестный часовой пояс %q (пример: Europe/Moscow)", tzName))
		loc = time.UTC
	}
	c.Timezone = loc

	c.ZoomWebhookSecret = e.str("ZOOM_WEBHOOK_SECRET_TOKEN", "")
	c.ZoomWebhookMaxAge = e.dur("ZOOM_WEBHOOK_MAX_AGE", 2*time.Hour)

	c.AdminUsername = strings.ToLower(e.str("ADMIN_USERNAME", "admin"))
	c.AdminPassword = e.str("ADMIN_PASSWORD", "")
	c.SessionTTL = e.dur("SESSION_TTL", 168*time.Hour)
	c.ExternalSessionTTL = e.dur("EXTERNAL_SESSION_TTL", 24*time.Hour)
	for _, s := range e.list("TRUSTED_PROXIES", ",") {
		if !strings.Contains(s, "/") {
			if strings.Contains(s, ":") {
				s += "/128"
			} else {
				s += "/32"
			}
		}
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			errs = append(errs, fmt.Errorf("TRUSTED_PROXIES: %q — не IP-адрес и не подсеть (пример: 10.0.0.5 или 10.0.0.0/24)", s))
			continue
		}
		c.TrustedProxies = append(c.TrustedProxies, n)
	}
	c.LogLevel = strings.ToLower(e.str("LOG_LEVEL", "info"))
	c.LogFormat = strings.ToLower(e.str("LOG_FORMAT", "text"))
	c.BackupKeep = e.int("BACKUP_KEEP", 14)
	c.EventsRetentionDays = e.int("EVENTS_RETENTION_DAYS", 365)
	c.LocalAuthEnabled = e.bool("AUTH_LOCAL_ENABLED", true)

	c.OIDC = OIDCConfig{
		IssuerURL:    strings.TrimRight(e.str("OIDC_ISSUER_URL", ""), "/"),
		ClientID:     e.str("OIDC_CLIENT_ID", ""),
		ClientSecret: e.str("OIDC_CLIENT_SECRET", ""),
		Scopes:       strings.Fields(e.str("OIDC_SCOPES", "openid profile email")),
		ButtonLabel:  e.str("OIDC_BUTTON_LABEL", "Войти через Keycloak"),
		AdminRoles:   e.list("OIDC_ADMIN_ROLES", ","),
		TeacherRoles: e.list("OIDC_TEACHER_ROLES", ","),
		RolesClaim:   e.str("OIDC_ROLES_CLAIM", ""),
		CAFile:       e.str("OIDC_CA_FILE", ""),
	}
	if c.OIDC.Enabled() && c.OIDC.ClientID == "" {
		errs = append(errs, errors.New("OIDC_CLIENT_ID: укажите Client ID — он обязателен, когда задан OIDC_ISSUER_URL"))
	}

	preset := strings.ToLower(e.str("LDAP_PRESET", PresetAD))
	def, ok := ldapPresets[preset]
	if !ok {
		errs = append(errs, fmt.Errorf("LDAP_PRESET: допустимы значения ad, openldap или freeipa; сейчас %q", preset))
		def = ldapPresets[PresetAD]
	}
	c.LDAP = LDAPConfig{
		URL:                e.str("LDAP_URL", ""),
		StartTLS:           e.bool("LDAP_START_TLS", false),
		CAFile:             e.str("LDAP_CA_FILE", ""),
		InsecureSkipVerify: e.bool("LDAP_INSECURE_SKIP_VERIFY", false),
		Preset:             preset,
		BindDN:             e.str("LDAP_BIND_DN", ""),
		BindPassword:       e.str("LDAP_BIND_PASSWORD", ""),
		Domain:             e.str("LDAP_DOMAIN", ""),
		BaseDN:             e.str("LDAP_BASE_DN", ""),
		UserFilter:         e.str("LDAP_USER_FILTER", def.UserFilter),
		DisplayNameAttr:    e.str("LDAP_DISPLAY_NAME_ATTR", def.DisplayNameAttr),
		EmailAttr:          e.str("LDAP_EMAIL_ATTR", def.EmailAttr),
		IDAttr:             e.str("LDAP_ID_ATTR", def.IDAttr),
		UsernameAttr:       e.str("LDAP_USERNAME_ATTR", def.UsernameAttr),
		AdminGroups:        e.list("LDAP_ADMIN_GROUPS", ";"),
		TeacherGroups:      e.list("LDAP_TEACHER_GROUPS", ";"),
		NestedGroups:       e.bool("LDAP_NESTED_GROUPS", def.NestedGroups),
		Timeout:            e.dur("LDAP_TIMEOUT", 10*time.Second),
	}
	if c.LDAP.Enabled() {
		lu, err := url.Parse(c.LDAP.URL)
		if err != nil || (lu.Scheme != "ldap" && lu.Scheme != "ldaps") || lu.Host == "" {
			errs = append(errs, fmt.Errorf("LDAP_URL: адрес должен выглядеть как ldaps://сервер:636 или ldap://сервер:389; сейчас %q", c.LDAP.URL))
		}
		if c.LDAP.BaseDN == "" {
			errs = append(errs, errors.New("LDAP_BASE_DN: укажите, где искать пользователей (например DC=corp,DC=example,DC=com) — это обязательно, когда задан LDAP_URL"))
		}
		if !strings.Contains(c.LDAP.UserFilter, "{username}") {
			errs = append(errs, errors.New("LDAP_USER_FILTER: фильтр должен содержать {username}"))
		}
		if c.LDAP.BindDN == "" && !(c.LDAP.Preset == PresetAD && c.LDAP.Domain != "") {
			errs = append(errs, errors.New("LDAP_BIND_DN: укажите служебную учётную запись для поиска пользователей (или для AD задайте LDAP_PRESET=ad и LDAP_DOMAIN)"))
		}
	}
	if !c.LocalAuthEnabled && !c.OIDC.Enabled() && !c.LDAP.Enabled() {
		errs = append(errs, errors.New("не включён ни один способ входа: оставьте AUTH_LOCAL_ENABLED=true или настройте вход через Keycloak (OIDC_*) или LDAP (LDAP_*)"))
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("LOG_LEVEL: допустимы значения debug, info, warn или error; сейчас %q", c.LogLevel))
	}
	switch c.LogFormat {
	case "text", "json":
	default:
		errs = append(errs, fmt.Errorf("LOG_FORMAT: допустимы значения text или json; сейчас %q", c.LogFormat))
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return c, nil
}

type ldapPreset struct {
	UserFilter, DisplayNameAttr, EmailAttr, IDAttr, UsernameAttr string
	NestedGroups                                                 bool
}

var ldapPresets = map[string]ldapPreset{
	PresetAD: {
		// Person accounts matched by sAMAccountName or UPN, disabled accounts excluded.
		UserFilter:      "(&(objectCategory=person)(objectClass=user)(|(sAMAccountName={username})(userPrincipalName={username}))(!(userAccountControl:1.2.840.113556.1.4.803:=2)))",
		DisplayNameAttr: "displayName",
		EmailAttr:       "mail",
		IDAttr:          "objectGUID",
		UsernameAttr:    "sAMAccountName",
		NestedGroups:    true,
	},
	PresetOpenLDAP: {
		UserFilter:      "(&(objectClass=inetOrgPerson)(uid={username}))",
		DisplayNameAttr: "cn",
		EmailAttr:       "mail",
		IDAttr:          "entryUUID",
		UsernameAttr:    "uid",
	},
	PresetFreeIPA: {
		UserFilter:      "(&(objectClass=person)(uid={username})(!(nsAccountLock=TRUE)))",
		DisplayNameAttr: "displayName",
		EmailAttr:       "mail",
		IDAttr:          "ipaUniqueID",
		UsernameAttr:    "uid",
	},
}

// PublicHTTPS reports whether the external URL is https (secure cookies, HSTS).
func (c *Config) PublicHTTPS() bool { return c.PublicURL.Scheme == "https" }

// WebhookURL is the URL to paste into the Zoom app.
func (c *Config) WebhookURL() string { return c.PublicURL.String() + "/api/webhooks/zoom" }

// DBDriver returns "sqlite" or "postgres" based on DATABASE_URL.
func (c *Config) DBDriver() string {
	if strings.HasPrefix(c.DatabaseURL, "postgres://") || strings.HasPrefix(c.DatabaseURL, "postgresql://") {
		return "postgres"
	}
	return "sqlite"
}

// Redacted returns a human-readable summary safe to print or log.
func (c *Config) Redacted() map[string]string {
	set := func(s string) string {
		if s == "" {
			return "(не задан)"
		}
		return "(задан)"
	}
	dbURL := RedactDatabaseURL(c.DatabaseURL)
	return map[string]string{
		"PUBLIC_URL":                c.PublicURL.String(),
		"LISTEN_ADDR":               c.ListenAddr,
		"TLS_MODE":                  c.TLSMode,
		"DATA_DIR":                  c.DataDir,
		"DATABASE_URL":              dbURL,
		"TIMEZONE":                  c.Timezone.String(),
		"ZOOM_WEBHOOK_SECRET_TOKEN": set(c.ZoomWebhookSecret),
		"WEBHOOK_URL":               c.WebhookURL(),
		"AUTH_LOCAL_ENABLED":        strconv.FormatBool(c.LocalAuthEnabled),
		"OIDC":                      enabledStr(c.OIDC.Enabled(), c.OIDC.IssuerURL),
		"OIDC_CLIENT_SECRET":        set(c.OIDC.ClientSecret),
		"LDAP":                      enabledStr(c.LDAP.Enabled(), c.LDAP.URL),
		"LDAP_BIND_PASSWORD":        set(c.LDAP.BindPassword),
	}
}

// passwordParam matches password-like query parameters (password=,
// sslpassword=) that libpq-style URLs may carry instead of userinfo.
var passwordParam = regexp.MustCompile(`(?i)(password=)[^&#]*`)

// RedactDatabaseURL hides the passwords of a connection URL for printing and
// error messages: the userinfo password and any password= / sslpassword=
// query value. It works on the string, not on url.Parse, so passwords with
// unencoded '@', '/', '?' or '%' (which url.Parse rejects) are hidden too, and
// a user without a password stays as it is.
func RedactDatabaseURL(s string) string {
	s = passwordParam.ReplaceAllString(s, "${1}xxxxx")
	scheme, rest, ok := strings.Cut(s, "://")
	if !ok {
		return s
	}
	// The last '@' of the whole string: a password may contain unencoded '@', '/'
	// or '?'. If the query also contains '@' this over-redacts, which is harmless.
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return s
	}
	userinfo := rest[:at]
	if user, _, hasPass := strings.Cut(userinfo, ":"); hasPass {
		userinfo = user + ":xxxxx"
	}
	return scheme + "://" + userinfo + rest[at:]
}

func enabledStr(on bool, detail string) string {
	if !on {
		return "выключен"
	}
	return "включён: " + detail
}

type envReader struct {
	getenv Getenv
	errs   *[]error
}

func (e envReader) str(key, def string) string {
	if v := strings.TrimSpace(e.getenv(key)); v != "" {
		return v
	}
	return def
}

func (e envReader) bool(key string, def bool) bool {
	v := strings.TrimSpace(e.getenv(key))
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on", "да":
		return true
	case "0", "false", "no", "off", "нет":
		return false
	}
	*e.errs = append(*e.errs, fmt.Errorf("%s: ожидается true или false; сейчас %q", key, v))
	return def
}

func (e envReader) int(key string, def int) int {
	v := strings.TrimSpace(e.getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		*e.errs = append(*e.errs, fmt.Errorf("%s: ожидается целое число 0 или больше; сейчас %q", key, v))
		return def
	}
	return n
}

func (e envReader) dur(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(e.getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		*e.errs = append(*e.errs, fmt.Errorf("%s: ожидается длительность вида 30s, 5m или 168h (s — секунды, m — минуты, h — часы); сейчас %q", key, v))
		return def
	}
	return d
}

func (e envReader) list(key, sep string) []string {
	var out []string
	for _, p := range strings.Split(e.getenv(key), sep) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
