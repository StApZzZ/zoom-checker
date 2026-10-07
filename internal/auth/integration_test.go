package auth

// Integration tests against real identity providers. They are skipped unless
// the TEST_* variables are set; start the stand with
//
//	deploy/dev/auth-stand/stand.sh up && eval "$(deploy/dev/auth-stand/stand.sh env)"
//
// The users, groups and passwords below are seeded by that stand.

import (
	"bytes"
	"context"
	"errors"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"zoom-checker/internal/config"
	"zoom-checker/internal/model"
)

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// syncBuffer collects log output from concurrent goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newTestLDAP builds an LDAP authenticator through config.Load (so the
// preset defaults are the real ones) from env overrides.
func newTestLDAP(t *testing.T, env map[string]string) (*LDAP, *syncBuffer) {
	t.Helper()
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	logs := &syncBuffer{}
	l, err := NewLDAP(cfg.LDAP, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatalf("NewLDAP: %v", err)
	}
	return l, logs
}

func with(base map[string]string, kv ...string) map[string]string {
	m := make(map[string]string, len(base)+len(kv)/2)
	for k, v := range base {
		m[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

func requireEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("%s not set; run deploy/dev/auth-stand/stand.sh up and eval \"$(deploy/dev/auth-stand/stand.sh env)\"", name)
	}
	return v
}

func authOK(t *testing.T, l *LDAP, user, pw string, role model.Role) *Identity {
	t.Helper()
	id, err := l.Authenticate(context.Background(), user, pw)
	if err != nil {
		t.Fatalf("Authenticate(%q): %v", user, err)
	}
	if id.Role != role || id.Source != model.AuthLDAP {
		t.Fatalf("Authenticate(%q) = %+v, want role %s", user, id, role)
	}
	return id
}

func authErr(t *testing.T, l *LDAP, user, pw string, want error) error {
	t.Helper()
	id, err := l.Authenticate(context.Background(), user, pw)
	if id != nil || !errors.Is(err, want) {
		t.Fatalf("Authenticate(%q) = %+v, %v; want %v", user, id, err, want)
	}
	return err
}

// sameCost checks that a login rejected by the user search (unknown) makes as
// many connections as a wrong password for an existing user, so response
// times do not reveal which accounts exist.
func sameCost(t *testing.T, l *LDAP, unknown, existing string) {
	t.Helper()
	dials := func(user string) int64 {
		before := l.dials.Load()
		authErr(t, l, user, "Wrong-Passw0rd!", ErrInvalidCredentials)
		return l.dials.Load() - before
	}
	if u, e := dials(unknown), dials(existing); u != e {
		t.Fatalf("%q costs %d connections, %q costs %d: timing reveals existing accounts", unknown, u, existing, e)
	}
}

func checkNoSecrets(t *testing.T, steps []DiagStep, secrets ...string) {
	t.Helper()
	for _, s := range steps {
		for _, sec := range secrets {
			if sec != "" && strings.Contains(s.Name+s.Detail, sec) {
				t.Fatalf("diagnostic step %q leaks a secret: %s", s.Name, s.Detail)
			}
		}
	}
}

func stepNames(steps []DiagStep) string {
	var b strings.Builder
	for _, s := range steps {
		b.WriteString(s.Name)
		if s.OK {
			b.WriteString(" ✓; ")
		} else {
			b.WriteString(" ✗ " + s.Detail + "; ")
		}
	}
	return b.String()
}

func TestIntegrationOpenLDAP(t *testing.T) {
	base := map[string]string{
		"LDAP_URL":            requireEnv(t, "TEST_LDAP_OPENLDAP_URL"),
		"LDAP_PRESET":         "openldap",
		"LDAP_BIND_DN":        os.Getenv("TEST_LDAP_OPENLDAP_BIND_DN"),
		"LDAP_BIND_PASSWORD":  os.Getenv("TEST_LDAP_OPENLDAP_BIND_PASSWORD"),
		"LDAP_BASE_DN":        os.Getenv("TEST_LDAP_OPENLDAP_BASE_DN"),
		"LDAP_ADMIN_GROUPS":   "zoom-checker-admins",                                      // CN rule
		"LDAP_TEACHER_GROUPS": "cn=zoom-checker-teachers, ou=Groups, dc=example, dc=test", // DN rule
		"LDAP_TIMEOUT":        "5s",
	}
	l, logs := newTestLDAP(t, base)
	const teacherPW = "Teacher1-Passw0rd!"

	t.Run("teacher", func(t *testing.T) {
		id := authOK(t, l, "teacher1", teacherPW, model.RoleTeacher)
		if id.Username != "teacher1" || id.DisplayName != "Мария Иванова" || id.Email != "teacher1@example.test" || !uuidRe.MatchString(id.ExternalID) {
			t.Fatalf("identity: %+v", id)
		}
		groups := strings.ToLower(strings.Join(id.Groups, ";"))
		if !strings.Contains(groups, "cn=zoom-checker-teachers,ou=groups") || !strings.Contains(groups, "cn=zc-all-staff,ou=groups") {
			t.Fatalf("groups (member search + memberOf): %q", id.Groups)
		}
	})
	t.Run("admin", func(t *testing.T) {
		authOK(t, l, "admin1", "Admin1-Passw0rd!", model.RoleAdmin)
	})
	t.Run("login variants", func(t *testing.T) {
		for _, u := range []string{"TEACHER1", "  teacher1 ", `EXAMPLE\teacher1`} {
			if id := authOK(t, l, u, teacherPW, model.RoleTeacher); id.Username != "teacher1" {
				t.Fatalf("%q -> username %q", u, id.Username)
			}
		}
	})
	t.Run("student denied", func(t *testing.T) {
		err := authErr(t, l, "student1", "Student1-Passw0rd!", ErrAccessDenied)
		var denied *AccessDeniedError
		if !errors.As(err, &denied) || len(denied.Required) != 2 || denied.Username != "student1" {
			t.Fatalf("AccessDeniedError: %+v", denied)
		}
	})
	t.Run("wrong password", func(t *testing.T) {
		authErr(t, l, "teacher1", "Teacher1-Passw0rd?", ErrInvalidCredentials)
	})
	t.Run("unknown user", func(t *testing.T) {
		authErr(t, l, "nobody", teacherPW, ErrInvalidCredentials)
		// admin1, not teacher1: the stand locks an account after 3 consecutive
		// failures (ppolicy), and admin1 logs in successfully again below.
		sameCost(t, l, "nobody", "admin1")
	})
	t.Run("filter injection", func(t *testing.T) {
		for _, u := range []string{"*", "teacher*", "*)(uid=*", "teacher1)(|(uid=*", "*)(objectClass=*"} {
			authErr(t, l, u, teacherPW, ErrInvalidCredentials)
		}
	})
	t.Run("empty password never reaches the server", func(t *testing.T) {
		before := l.dials.Load()
		authErr(t, l, "teacher1", "", ErrInvalidCredentials)
		authErr(t, l, "teacher1", "   ", ErrInvalidCredentials)
		if l.dials.Load() != before {
			t.Fatal("dialed for an empty password")
		}
	})
	t.Run("star teacher groups", func(t *testing.T) {
		l2, _ := newTestLDAP(t, with(base, "LDAP_TEACHER_GROUPS", "*"))
		authOK(t, l2, "student1", "Student1-Passw0rd!", model.RoleTeacher)
		authOK(t, l2, "admin1", "Admin1-Passw0rd!", model.RoleAdmin)
	})
	t.Run("wrong service password", func(t *testing.T) {
		l2, _ := newTestLDAP(t, with(base, "LDAP_BIND_PASSWORD", "wrong"))
		authErr(t, l2, "teacher1", teacherPW, ErrUnavailable)
		steps := l2.Diagnose(context.Background(), "teacher1", "")
		if len(steps) != 2 || steps[1].OK || !strings.Contains(steps[1].Detail, "LDAP_BIND_PASSWORD") {
			t.Fatalf("Diagnose: %s", stepNames(steps))
		}
	})
	t.Run("wrong base dn", func(t *testing.T) {
		l2, _ := newTestLDAP(t, with(base, "LDAP_BASE_DN", "ou=nope,dc=example,dc=test"))
		authErr(t, l2, "teacher1", teacherPW, ErrUnavailable)
		steps := l2.Diagnose(context.Background(), "teacher1", "")
		if last := steps[len(steps)-1]; last.OK || !strings.Contains(last.Detail, "LDAP_BASE_DN") {
			t.Fatalf("Diagnose: %s", stepNames(steps))
		}
	})
	t.Run("diagnose", func(t *testing.T) {
		steps := l.Diagnose(context.Background(), "teacher1", teacherPW)
		if len(steps) != 6 || slices.ContainsFunc(steps, func(s DiagStep) bool { return !s.OK }) ||
			!strings.Contains(steps[5].Detail, "Преподаватель") || !strings.Contains(steps[4].Detail, "zoom-checker-teachers") {
			t.Fatalf("Diagnose: %s", stepNames(steps))
		}
		if !strings.Contains(steps[0].Detail, "без шифрования") {
			t.Fatalf("plain ldap:// must be flagged: %s", steps[0].Detail)
		}
		checkNoSecrets(t, steps, teacherPW, base["LDAP_BIND_PASSWORD"])

		steps = l.Diagnose(context.Background(), "teacher1", "")
		if len(steps) != 5 || steps[2].Name != "Поиск пользователя" || slices.ContainsFunc(steps, func(s DiagStep) bool { return s.Name == "Проверка пароля" }) {
			t.Fatalf("without password there is no bind step: %s", stepNames(steps))
		}
		steps = l.Diagnose(context.Background(), "student1", "")
		if last := steps[len(steps)-1]; last.OK || !strings.Contains(last.Detail, "zoom-checker-admins") {
			t.Fatalf("student role step: %s", stepNames(steps))
		}
		steps = l.Diagnose(context.Background(), "teacher1", "bad")
		if last := steps[len(steps)-1]; last.Name != "Проверка пароля" || last.OK {
			t.Fatalf("wrong password: %s", stepNames(steps))
		}
		steps = l.Diagnose(context.Background(), "nobody", "")
		if last := steps[len(steps)-1]; last.OK || !strings.Contains(last.Detail, "не найден") {
			t.Fatalf("unknown user: %s", stepNames(steps))
		}
	})
	if strings.Contains(logs.String(), teacherPW) {
		t.Fatal("password found in logs")
	}
}

func adBase(t *testing.T) map[string]string {
	return map[string]string{
		"LDAP_URL":           requireEnv(t, "TEST_LDAP_AD_URL"),
		"LDAP_PRESET":        "ad",
		"LDAP_BIND_DN":       os.Getenv("TEST_LDAP_AD_BIND_DN"),
		"LDAP_BIND_PASSWORD": os.Getenv("TEST_LDAP_AD_BIND_PASSWORD"),
		"LDAP_BASE_DN":       os.Getenv("TEST_LDAP_AD_BASE_DN"),
		"LDAP_DOMAIN":        os.Getenv("TEST_LDAP_AD_DOMAIN"),
		"LDAP_CA_FILE":       os.Getenv("TEST_LDAP_AD_CA_FILE"),
		// Samba's certificate names only dc1.corp.example.test, the tests
		// connect to localhost.
		"LDAP_INSECURE_SKIP_VERIFY": "true",
		"LDAP_ADMIN_GROUPS":         "ZoomChecker-Admins",
		"LDAP_TEACHER_GROUPS":       "CN=ZoomChecker-Teachers,OU=ZoomGroups,DC=corp,DC=example,DC=test",
		"LDAP_TIMEOUT":              "5s",
	}
}

func TestIntegrationActiveDirectory(t *testing.T) {
	base := adBase(t)
	l, logs := newTestLDAP(t, base)
	const ivanovaPW = "Ivanova-Passw0rd!"

	t.Run("teacher through nested group", func(t *testing.T) {
		id := authOK(t, l, "t.ivanova", ivanovaPW, model.RoleTeacher)
		if id.Username != "t.ivanova" || id.DisplayName != "Татьяна Иванова" || id.Email != "t.ivanova@corp.example.test" || !uuidRe.MatchString(id.ExternalID) {
			t.Fatalf("identity: %+v", id)
		}
		groups := strings.ToLower(strings.Join(id.Groups, ";"))
		if !strings.Contains(groups, "cn=helpdesk,") || !strings.Contains(groups, "cn=zoomchecker-teachers,") {
			t.Fatalf("transitive groups: %q", id.Groups)
		}
		again := authOK(t, l, "t.ivanova", ivanovaPW, model.RoleTeacher)
		if again.ExternalID != id.ExternalID {
			t.Fatal("objectGUID must be stable")
		}
	})
	t.Run("login variants", func(t *testing.T) {
		for _, u := range []string{"t.ivanova@corp.example.test", `CORP\t.ivanova`, "T.IVANOVA", "T.Ivanova@CORP.EXAMPLE.TEST"} {
			if id := authOK(t, l, u, ivanovaPW, model.RoleTeacher); id.Username != "t.ivanova" {
				t.Fatalf("%q -> %q", u, id.Username)
			}
		}
	})
	t.Run("admin", func(t *testing.T) {
		authOK(t, l, "a.petrov", "Petrov-Passw0rd!", model.RoleAdmin)
	})
	t.Run("no group", func(t *testing.T) {
		authErr(t, l, "s.sidorov", "Sidorov-Passw0rd!", ErrAccessDenied)
	})
	t.Run("wrong password", func(t *testing.T) {
		authErr(t, l, "t.ivanova", "Ivanova-Passw0rd?", ErrInvalidCredentials)
		if !strings.Contains(logs.String(), "AD 52e") {
			t.Fatalf("AD sub-code must be logged: %s", logs.String())
		}
	})
	t.Run("unknown user", func(t *testing.T) {
		authErr(t, l, "nobody", ivanovaPW, ErrInvalidCredentials)
		sameCost(t, l, "nobody", "t.ivanova")
		sameCost(t, l, "carol", "t.ivanova") // disabled: filtered out by the preset
	})
	t.Run("disabled user", func(t *testing.T) {
		// The preset filter skips disabled accounts.
		authErr(t, l, "carol", "Carol-Passw0rd!", ErrInvalidCredentials)
		// Without that clause the bind itself is refused with AD code 533.
		l2, logs2 := newTestLDAP(t, with(base, "LDAP_USER_FILTER", "(&(objectClass=user)(sAMAccountName={username}))"))
		authErr(t, l2, "carol", "Carol-Passw0rd!", ErrInvalidCredentials)
		if !strings.Contains(logs2.String(), "account disabled (AD 533)") {
			t.Fatalf("log must explain the disabled account: %s", logs2.String())
		}
		steps := l2.Diagnose(context.Background(), "carol", "Carol-Passw0rd!")
		if last := steps[len(steps)-1]; last.OK || !strings.Contains(last.Detail, "учётная запись отключена") {
			t.Fatalf("Diagnose: %s", stepNames(steps))
		}
	})
	t.Run("filter injection", func(t *testing.T) {
		for _, u := range []string{"*", "t.ivan*", "t.ivanova)(|(sAMAccountName=*", "*@corp.example.test", "*)(objectClass=*"} {
			authErr(t, l, u, ivanovaPW, ErrInvalidCredentials)
		}
	})
	t.Run("empty password never reaches the server", func(t *testing.T) {
		before := l.dials.Load()
		authErr(t, l, "t.ivanova", "", ErrInvalidCredentials)
		if l.dials.Load() != before {
			t.Fatal("dialed for an empty password")
		}
	})
	t.Run("nesting disabled", func(t *testing.T) {
		l2, _ := newTestLDAP(t, with(base, "LDAP_NESTED_GROUPS", "false"))
		authErr(t, l2, "t.ivanova", ivanovaPW, ErrAccessDenied)
		authOK(t, l2, "a.petrov", "Petrov-Passw0rd!", model.RoleAdmin)
	})
	t.Run("direct bind without service account", func(t *testing.T) {
		l2, _ := newTestLDAP(t, with(base, "LDAP_BIND_DN", "", "LDAP_BIND_PASSWORD", ""))
		authOK(t, l2, "t.ivanova", ivanovaPW, model.RoleTeacher)
		authOK(t, l2, "a.petrov@corp.example.test", "Petrov-Passw0rd!", model.RoleAdmin)
		authErr(t, l2, "t.ivanova", "wrong", ErrInvalidCredentials)
		authErr(t, l2, "carol", "Carol-Passw0rd!", ErrInvalidCredentials)
		steps := l2.Diagnose(context.Background(), "t.ivanova", ivanovaPW)
		if slices.ContainsFunc(steps, func(s DiagStep) bool { return !s.OK }) || len(steps) != 6 {
			t.Fatalf("Diagnose: %s", stepNames(steps))
		}
		// A filter that finds somebody else than the account that bound
		// (WhoAmI says CORP\t.ivanova, the entry is a.petrov) is refused.
		l3, logs3 := newTestLDAP(t, with(base, "LDAP_BIND_DN", "", "LDAP_BIND_PASSWORD", "",
			"LDAP_USER_FILTER", "(&(sAMAccountName=a.petrov)(|(sAMAccountName={username})(objectClass=user)))"))
		authErr(t, l3, "t.ivanova", ivanovaPW, ErrInvalidCredentials)
		if !strings.Contains(logs3.String(), "not the account that signed in") {
			t.Fatalf("log: %s", logs3.String())
		}
	})
	t.Run("starttls", func(t *testing.T) {
		u := os.Getenv("TEST_LDAP_AD_STARTTLS_URL")
		if u == "" {
			t.Skip("TEST_LDAP_AD_STARTTLS_URL not set")
		}
		l2, _ := newTestLDAP(t, with(base, "LDAP_URL", u, "LDAP_START_TLS", "true"))
		authOK(t, l2, "t.ivanova", ivanovaPW, model.RoleTeacher)
		// Samba refuses simple binds over plain LDAP (strong auth required).
		l3, _ := newTestLDAP(t, with(base, "LDAP_URL", u))
		authErr(t, l3, "t.ivanova", ivanovaPW, ErrUnavailable)
		steps := l3.Diagnose(context.Background(), "", "")
		if last := steps[len(steps)-1]; last.OK || !strings.Contains(last.Detail, "зашифрованное соединение") {
			t.Fatalf("Diagnose: %s", stepNames(steps))
		}
	})
	t.Run("certificate is verified", func(t *testing.T) {
		// Without LDAP_INSECURE_SKIP_VERIFY the certificate (issued to
		// dc1.corp.example.test) does not match "localhost" and the login is
		// refused as unavailable, never attempted. (Go checks the host name
		// before the chain; TestDiagnoseTLS covers the CA file itself.)
		l2, _ := newTestLDAP(t, with(base, "LDAP_INSECURE_SKIP_VERIFY", "false"))
		authErr(t, l2, "t.ivanova", ivanovaPW, ErrUnavailable)
		steps := l2.Diagnose(context.Background(), "", "")
		if len(steps) != 1 || !strings.Contains(steps[0].Detail, "не совпадает") {
			t.Fatalf("Diagnose: %s", stepNames(steps))
		}
	})
	t.Run("certificate chain verified with the CA file", func(t *testing.T) {
		// Connect to localhost but verify as the name in Samba's certificate
		// (as if LDAP_URL named dc1.corp.example.test): proves that
		// LDAP_CA_FILE is what makes the real chain verify.
		caFile := os.Getenv("TEST_LDAP_AD_CA_FILE")
		if caFile == "" {
			t.Skip("TEST_LDAP_AD_CA_FILE not set")
		}
		l2, _ := newTestLDAP(t, with(base, "LDAP_INSECURE_SKIP_VERIFY", "false"))
		l2.tlsConfig.ServerName = "dc1.corp.example.test"
		authOK(t, l2, "t.ivanova", ivanovaPW, model.RoleTeacher)

		l3, _ := newTestLDAP(t, with(base, "LDAP_INSECURE_SKIP_VERIFY", "false", "LDAP_CA_FILE", ""))
		l3.tlsConfig.ServerName = "dc1.corp.example.test"
		authErr(t, l3, "t.ivanova", ivanovaPW, ErrUnavailable)
		steps := l3.Diagnose(context.Background(), "", "")
		if len(steps) != 1 || !strings.Contains(steps[0].Detail, "неизвестным центром сертификации") {
			t.Fatalf("Diagnose without CA: %s", stepNames(steps))
		}
	})
	t.Run("NUL in password never reaches the server", func(t *testing.T) {
		before := l.dials.Load()
		authErr(t, l, "t.ivanova", "\x00", ErrInvalidCredentials)
		authErr(t, l, "t.ivanova", ivanovaPW+"\x00", ErrInvalidCredentials)
		if l.dials.Load() != before {
			t.Fatal("dialed for a password with NUL")
		}
	})
	t.Run("diagnose", func(t *testing.T) {
		steps := l.Diagnose(context.Background(), "t.ivanova", ivanovaPW)
		if len(steps) != 6 || slices.ContainsFunc(steps, func(s DiagStep) bool { return !s.OK }) ||
			!strings.Contains(steps[4].Detail, "ZoomChecker-Teachers") || !strings.Contains(steps[0].Detail, "проверка сертификата отключена") {
			t.Fatalf("Diagnose: %s", stepNames(steps))
		}
		checkNoSecrets(t, steps, ivanovaPW, base["LDAP_BIND_PASSWORD"])
	})
	if strings.Contains(logs.String(), ivanovaPW) {
		t.Fatal("password found in logs")
	}
}

// looseJar keeps cookies by name for a single test host. Keycloak marks its
// session cookies Secure (SameSite=None), which net/http/cookiejar refuses to
// send over plain http://localhost, unlike browsers.
type looseJar struct {
	mu sync.Mutex
	m  map[string]*http.Cookie
}

func (j *looseJar) SetCookies(_ *url.URL, cs []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range cs {
		if c.MaxAge < 0 || (!c.Expires.IsZero() && c.Expires.Before(time.Now())) {
			delete(j.m, c.Name)
			continue
		}
		j.m[c.Name] = &http.Cookie{Name: c.Name, Value: c.Value}
	}
}

func (j *looseJar) Cookies(*url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]*http.Cookie, 0, len(j.m))
	for _, c := range j.m {
		out = append(out, c)
	}
	return out
}

var loginFormAction = regexp.MustCompile(`action="([^"]*login-actions/authenticate[^"]*)"`)

type kcSession struct {
	client   *http.Client
	callback *http.Request // the callback request the browser would send to the app
}

// kcBrowserLogin drives the Keycloak login page without a browser: it starts
// the flow, posts the login form and captures the redirect to our callback.
func kcBrowserLogin(t *testing.T, o *OIDC, user, pw string) *kcSession {
	t.Helper()
	rec := httptest.NewRecorder()
	authURL, err := o.StartLogin(rec, httptest.NewRequest("GET", "http://localhost:8080/auth/oidc/login", nil), "/lessons/42")
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	client := &http.Client{
		Jar:     &looseJar{m: map[string]*http.Cookie{}},
		Timeout: 20 * time.Second,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if strings.HasPrefix(req.URL.String(), o.RedirectURL()) || strings.HasPrefix(req.URL.String(), "http://localhost:8080/") {
				return http.ErrUseLastResponse // the app is not running; stop at its URL
			}
			return nil
		},
	}
	resp, err := client.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := loginFormAction.FindSubmatch(page)
	if resp.StatusCode != http.StatusOK || m == nil {
		t.Fatalf("login page: status %d, no form: %.500s", resp.StatusCode, page)
	}
	resp, err = client.PostForm(html.UnescapeString(string(m[1])), url.Values{"username": {user}, "password": {pw}, "credentialId": {""}})
	if err != nil {
		t.Fatal(err)
	}
	page, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(loc, o.RedirectURL()+"?") {
		t.Fatalf("login form post: status %d location %q: %.500s", resp.StatusCode, loc, page)
	}
	cb := httptest.NewRequest("GET", loc, nil)
	for _, c := range rec.Result().Cookies() {
		cb.AddCookie(c)
	}
	return &kcSession{client: client, callback: cb}
}

func newKeycloakOIDC(t *testing.T, mod func(*config.OIDCConfig)) *OIDC {
	t.Helper()
	cfg := config.OIDCConfig{
		IssuerURL:    requireEnv(t, "TEST_OIDC_ISSUER"),
		ClientID:     cmpOr(os.Getenv("TEST_OIDC_CLIENT_ID"), "zoom-checker"),
		ClientSecret: os.Getenv("TEST_OIDC_CLIENT_SECRET"),
		Scopes:       []string{"openid", "profile", "email"},
		AdminRoles:   []string{"zoom-checker-admin"},
		TeacherRoles: []string{"zoom-checker-teacher"},
	}
	if mod != nil {
		mod(&cfg)
	}
	pub, _ := url.Parse("http://localhost:8080") // registered redirect URI; nothing needs to listen there
	o, err := NewOIDC(cfg, pub, DeriveKey([]byte("integration"), "oidc-flow-cookie"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestIntegrationKeycloak(t *testing.T) {
	o := newKeycloakOIDC(t, nil)

	t.Run("admin from access token realm roles", func(t *testing.T) {
		s := kcBrowserLogin(t, o, "kc-admin", "KcAdmin-Passw0rd!")
		id, raw, next, err := o.HandleCallback(httptest.NewRecorder(), s.callback)
		if err != nil {
			t.Fatalf("HandleCallback: %v", err)
		}
		if id.Role != model.RoleAdmin || id.Username != "kc-admin" || id.DisplayName != "Анна Админова" ||
			id.Email != "kc-admin@example.test" || !uuidRe.MatchString(id.ExternalID) || next != "/lessons/42" {
			t.Fatalf("identity: %+v next=%q", id, next)
		}
		if !slices.Contains(id.Groups, "zoom-checker-admin") {
			t.Fatalf("roles collected: %q", id.Groups)
		}

		// RP-initiated logout ends the Keycloak session and returns to /login.
		lu := o.LogoutURL(raw)
		if q := mustParse(t, lu).Query(); q.Get("id_token_hint") != raw || q.Get("post_logout_redirect_uri") != "http://localhost:8080/login" || q.Get("client_id") != "zoom-checker" {
			t.Fatalf("LogoutURL = %s", lu)
		}
		resp, err := s.client.Get(lu)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "http://localhost:8080/login" {
			t.Fatalf("logout: status %d location %q", resp.StatusCode, resp.Header.Get("Location"))
		}

		// Replaying the same callback fails: the code is single-use.
		if _, _, _, err := o.HandleCallback(httptest.NewRecorder(), s.callback); !errors.Is(err, ErrOIDCFlow) {
			t.Fatalf("replay: %v", err)
		}
	})
	t.Run("teacher through group role", func(t *testing.T) {
		s := kcBrowserLogin(t, o, "kc-teacher", "KcTeacher-Passw0rd!")
		id, _, _, err := o.HandleCallback(httptest.NewRecorder(), s.callback)
		if err != nil || id.Role != model.RoleTeacher {
			t.Fatalf("got %+v, %v", id, err)
		}
		if !slices.Contains(id.Groups, "/staff/zoom-teachers") {
			t.Fatalf("groups claim (full path) missing: %q", id.Groups)
		}
	})
	t.Run("teacher by group name rule", func(t *testing.T) {
		o2 := newKeycloakOIDC(t, func(c *config.OIDCConfig) { c.TeacherRoles = []string{"zoom-teachers"} })
		s := kcBrowserLogin(t, o2, "kc-teacher", "KcTeacher-Passw0rd!")
		if id, _, _, err := o2.HandleCallback(httptest.NewRecorder(), s.callback); err != nil || id.Role != model.RoleTeacher {
			t.Fatalf("got %+v, %v", id, err)
		}
	})
	t.Run("student denied", func(t *testing.T) {
		s := kcBrowserLogin(t, o, "kc-student", "KcStudent-Passw0rd!")
		id, raw, _, err := o.HandleCallback(httptest.NewRecorder(), s.callback)
		var denied *AccessDeniedError
		if id != nil || !errors.As(err, &denied) || denied.Username != "kc-student" || raw == "" {
			t.Fatalf("got %+v, %v", id, err)
		}
		if !strings.Contains(o.LogoutURL(raw), "id_token_hint=") {
			t.Fatal("denied user must be able to log out of Keycloak")
		}
	})
	t.Run("state mismatch", func(t *testing.T) {
		s := kcBrowserLogin(t, o, "kc-teacher", "KcTeacher-Passw0rd!")
		q := s.callback.URL.Query()
		q.Set("state", "forged")
		s.callback.URL.RawQuery = q.Encode()
		if _, _, _, err := o.HandleCallback(httptest.NewRecorder(), s.callback); !errors.Is(err, ErrOIDCFlow) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("wrong client secret", func(t *testing.T) {
		o2 := newKeycloakOIDC(t, func(c *config.OIDCConfig) { c.ClientSecret = "wrong" })
		s := kcBrowserLogin(t, o2, "kc-teacher", "KcTeacher-Passw0rd!")
		if _, _, _, err := o2.HandleCallback(httptest.NewRecorder(), s.callback); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("diagnose", func(t *testing.T) {
		steps := o.Diagnose(context.Background())
		if len(steps) != 5 || slices.ContainsFunc(steps, func(s DiagStep) bool { return !s.OK }) {
			t.Fatalf("Diagnose: %s", stepNames(steps))
		}
		checkNoSecrets(t, steps, os.Getenv("TEST_OIDC_CLIENT_SECRET"))
		bad := newKeycloakOIDC(t, func(c *config.OIDCConfig) { c.ClientSecret = "wrong" }).Diagnose(context.Background())
		if last := bad[len(bad)-1]; last.OK || !strings.Contains(last.Detail, "OIDC_CLIENT_SECRET") {
			t.Fatalf("wrong secret: %s", stepNames(bad))
		}
		badRedirect, err := NewOIDC(o.cfg, mustParse(t, "http://wrong.example:9999"), make([]byte, 32), nil)
		if err != nil {
			t.Fatal(err)
		}
		steps = badRedirect.Diagnose(context.Background())
		if steps[3].OK || !strings.Contains(steps[3].Detail, "http://wrong.example:9999/auth/oidc/callback") {
			t.Fatalf("unregistered redirect URI: %s", stepNames(steps))
		}
		wrongIssuer := newKeycloakOIDC(t, func(c *config.OIDCConfig) { c.IssuerURL = strings.Replace(c.IssuerURL, "localhost", "127.0.0.1", 1) })
		steps = wrongIssuer.Diagnose(context.Background())
		if len(steps) != 2 || steps[1].OK {
			t.Fatalf("issuer mismatch via another host name: %s", stepNames(steps))
		}
	})
}
