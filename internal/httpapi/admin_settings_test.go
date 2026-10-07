package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"zoom-checker/internal/auth"
	"zoom-checker/internal/config"
	"zoom-checker/internal/model"
)

func TestSettingsGetPut(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	_, admin := e.adminCookie()
	ctx := context.Background()

	rec := e.do("GET", "/api/settings", nil, admin)
	azWantStatus(t, rec, http.StatusOK)
	def := model.DefaultSettings()
	got := azDecode[model.Settings](t, rec)
	if got.PresentMinPercent != def.PresentMinPercent || got.LinkWindowHours != def.LinkWindowHours || got.IgnoredNames == nil {
		t.Fatalf("defaults = %+v", got)
	}
	if !strings.Contains(rec.Body.String(), `"ignored_names":[`) {
		t.Fatalf("ignored_names must be an array: %s", rec.Body.String())
	}

	// Lessons: one recent, one future, one long ago, and a recent one that was
	// never held (still planned: no participants the new rules could apply to).
	g := &model.Group{Name: "10А"}
	if err := e.st.CreateGroup(ctx, g); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	mk := func(start time.Time, status model.LessonStatus) int64 {
		l := &model.Lesson{GroupID: g.ID, Title: "Урок", ZoomMeetingID: "85746065432",
			PlannedStartAt: start, PlannedEndAt: start.Add(90 * time.Minute), Status: status}
		if err := e.st.CreateLesson(ctx, l); err != nil {
			t.Fatal(err)
		}
		return l.ID
	}
	recent, future := mk(now.Add(-10*24*time.Hour), model.LessonFinished), mk(now.Add(48*time.Hour), model.LessonPlanned)
	old := mk(now.Add(-100*24*time.Hour), model.LessonFinished)
	notHeld := mk(now.Add(-5*24*time.Hour), model.LessonPlanned)

	body := map[string]any{
		"ignored_names":       []string{"  Преподаватель ", "преподаватель", "", "   ", "iPhone", "IPHONE"},
		"present_min_percent": 50,
		"link_window_hours":   8,
		"ignore_host":         true,
	}
	rec = e.do("PUT", "/api/settings", body, admin)
	azWantStatus(t, rec, http.StatusOK)
	saved := azDecode[model.Settings](t, rec)
	want := model.Settings{IgnoredNames: []string{"Преподаватель", "iPhone"}, PresentMinPercent: 50, LinkWindowHours: 8, IgnoreHost: true}
	if fmt.Sprint(saved) != fmt.Sprint(want) {
		t.Fatalf("saved = %+v, want %+v", saved, want)
	}
	if stored, _ := e.st.GetSettings(ctx); fmt.Sprint(stored) != fmt.Sprint(want) {
		t.Fatalf("stored = %+v", stored)
	}
	azEventually(t, "background rebuild", func() bool {
		ids := e.proc.rebuiltIDs()
		return slices.Contains(ids, recent) && slices.Contains(ids, future)
	})
	azEventually(t, "rebuild log line", func() bool { return strings.Contains(e.logs.String(), "lessons rebuilt") })
	if slices.Contains(e.proc.rebuiltIDs(), old) {
		t.Fatal("a lesson older than 60 days was rebuilt")
	}
	if slices.Contains(e.proc.rebuiltIDs(), notHeld) {
		t.Fatal("a past lesson that was never held was rebuilt")
	}
	if a := e.azAudit(model.AuditSettingsUpdate); len(a) != 1 || !strings.Contains(a[0].NewValue, "iPhone") {
		t.Fatalf("audit = %+v", a)
	}

	// Partial update keeps the other fields.
	rec = e.do("PUT", "/api/settings", map[string]any{"ignore_host": false}, admin)
	azWantStatus(t, rec, http.StatusOK)
	if s := azDecode[model.Settings](t, rec); s.IgnoreHost || s.PresentMinPercent != 50 || len(s.IgnoredNames) != 2 {
		t.Fatalf("partial update = %+v", s)
	}
}

func TestSettingsValidation(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	_, admin := e.adminCookie()
	many := make([]string, 501)
	for i := range many {
		many[i] = fmt.Sprintf("имя %d", i)
	}
	cases := []struct {
		name string
		body map[string]any
	}{
		{"percent below 0", map[string]any{"present_min_percent": -1}},
		{"percent above 100", map[string]any{"present_min_percent": 101}},
		{"window 0", map[string]any{"link_window_hours": 0}},
		{"window 73", map[string]any{"link_window_hours": 73}},
		{"too many names", map[string]any{"ignored_names": many}},
		{"name too long", map[string]any{"ignored_names": []string{strings.Repeat("я", 201)}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			azWantError(t, e.do("PUT", "/api/settings", c.body, admin), http.StatusUnprocessableEntity, CodeValidation)
		})
	}
	azWantError(t, e.do("PUT", "/api/settings", map[string]any{"present_min_percent": "x"}, admin), http.StatusBadRequest, CodeBadRequest)

	// Boundaries are accepted; 500 distinct names after de-duplication are fine.
	rec := e.do("PUT", "/api/settings", map[string]any{"present_min_percent": 100, "link_window_hours": 72,
		"ignored_names": append(many[:500:500], "ИМЯ 1", strings.Repeat("я", 200))}, admin)
	azWantError(t, rec, http.StatusUnprocessableEntity, CodeValidation) // 501 distinct
	rec = e.do("PUT", "/api/settings", map[string]any{"present_min_percent": 0, "link_window_hours": 1,
		"ignored_names": append(many[:499:499], "ИМЯ 1", strings.Repeat("я", 200))}, admin)
	azWantStatus(t, rec, http.StatusOK)
	if s := azDecode[model.Settings](t, rec); len(s.IgnoredNames) != 500 || s.LinkWindowHours != 1 {
		t.Fatalf("saved %d names, window %d", len(s.IgnoredNames), s.LinkWindowHours)
	}
	if len(e.proc.rebuiltIDs()) != 0 {
		t.Fatal("no lessons, nothing to rebuild")
	}
}

func TestSystemInfo(t *testing.T) {
	t.Parallel()
	lcfg := azLDAPConfig("ldaps://dc1.corp.test:636")
	lcfg.BindPassword = "ldap-bind-secret"
	l, err := auth.NewLDAP(lcfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := newAZEnv(t, func(s *azSetup) {
		s.cfg.ZoomWebhookSecret = "zoom-secret-token-value"
		s.cfg.LDAP = lcfg
		s.deps.LDAP = l
		s.cfg.OIDC = config.OIDCConfig{IssuerURL: "https://sso.example.test/realms/edu", ClientID: azOIDCClientID, ClientSecret: azOIDCClientSecret}
		s.deps.OIDC = azTestOIDC(t, "https://sso.example.test/realms/edu", nil)
		s.cfg.DatabaseURL = "postgres://zc:db-password-value@db:5432/zc"
		s.cfg.PublicURL, _ = url.Parse("https://user:url-password@app.test")
	})
	_, admin := e.adminCookie()
	rec := e.do("GET", "/api/system", nil, admin)
	azWantStatus(t, rec, http.StatusOK)
	body := rec.Body.String()
	for _, secret := range []string{"zoom-secret-token-value", "ldap-bind-secret", azOIDCClientSecret, "db-password-value", "url-password"} {
		if strings.Contains(body, secret) {
			t.Fatalf("secret %q leaked: %s", secret, body)
		}
	}
	info := azDecode[systemInfoDTO](t, rec)
	if info.DBDriver != "postgres" || info.TLSMode != "off" || info.Timezone != "UTC" || info.DataDir != e.cfg.DataDir ||
		info.Version == "" || info.UptimeSeconds < 0 || !strings.HasPrefix(info.PublicURL, "https://user@app.test") {
		t.Fatalf("info = %+v", info)
	}
	if !info.Auth.Local.Enabled || !info.Auth.LDAP.Enabled || !info.Auth.OIDC.Enabled ||
		!strings.Contains(info.Auth.LDAP.Detail, "ldaps://dc1.corp.test:636") ||
		!strings.Contains(info.Auth.OIDC.Detail, "https://sso.example.test/realms/edu") ||
		!strings.Contains(info.Auth.OIDC.Detail, "/auth/oidc/callback") {
		t.Fatalf("auth = %+v", info.Auth)
	}
	for _, k := range []string{`"version"`, `"commit"`, `"db_driver"`, `"tls_mode"`, `"public_url"`, `"data_dir"`,
		`"timezone"`, `"uptime_seconds"`, `"auth":{"local":{"enabled":true,"detail":`} {
		if !strings.Contains(body, k) {
			t.Fatalf("missing %s in %s", k, body)
		}
	}

	plain := newAZEnv(t)
	_, a2 := plain.adminCookie()
	info = azDecode[systemInfoDTO](t, plain.do("GET", "/api/system", nil, a2))
	if info.Auth.LDAP.Enabled || info.Auth.OIDC.Enabled || info.Auth.LDAP.Detail == "" || info.DBDriver != "sqlite" {
		t.Fatalf("plain info = %+v", info)
	}
}
