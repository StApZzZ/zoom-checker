package server

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/certmagic"
	"go.uber.org/zap"

	"zoom-checker/internal/auth"
	"zoom-checker/internal/config"
	"zoom-checker/internal/model"
	"zoom-checker/internal/store"
)

func TestRedirectToHTTPS(t *testing.T) {
	t.Parallel()
	hosts := []string{"att.example.com", "203.0.113.10"}
	cases := []struct {
		name, host, target, port, want string
	}{
		{"same host and path", "att.example.com", "/lessons/5?x=1", "443", "https://att.example.com/lessons/5?x=1"},
		{"ip host", "203.0.113.10", "/", "443", "https://203.0.113.10/"},
		{"host with port", "att.example.com:80", "/a", "443", "https://att.example.com/a"},
		{"foreign host falls back", "evil.example.com", "/a", "443", "https://att.example.com/a"},
		{"custom https port", "att.example.com", "/a", "8443", "https://att.example.com:8443/a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := redirectToHTTPS(hosts, "att.example.com", c.port)
			r := httptest.NewRequest("GET", c.target, nil)
			r.Host = c.host
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusPermanentRedirect {
				t.Fatalf("status %d, want 308", w.Code)
			}
			if got := w.Header().Get("Location"); got != c.want {
				t.Fatalf("Location %q, want %q", got, c.want)
			}
		})
	}
	// /healthz is answered on port 80 itself (install.sh probes it).
	h := redirectToHTTPS(hosts, "att.example.com", "443")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 || w.Body.String() != "ok" {
		t.Fatalf("/healthz on :80 = %d %q", w.Code, w.Body.String())
	}
	// POST keeps its method through a 308.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/webhooks/zoom", nil))
	if w.Code != http.StatusPermanentRedirect {
		t.Fatalf("POST redirect status %d", w.Code)
	}
}

func TestManagedHostsAndProfile(t *testing.T) {
	t.Parallel()
	mk := func(public string, extra ...string) *config.Config {
		u, _ := url.Parse(public)
		return &config.Config{PublicURL: u, TLSExtraHosts: extra}
	}
	hosts := managedHosts(mk("https://Att.Example.com", "www.att.example.com", "att.example.com", "198.51.100.7:443", "[2001:db8::1]"))
	want := []string{"att.example.com", "www.att.example.com", "198.51.100.7", "2001:db8::1"}
	if !slices.Equal(hosts, want) {
		t.Fatalf("managedHosts = %v, want %v", hosts, want)
	}
	cases := []struct {
		configured string
		hosts      []string
		want       string
	}{
		{"", []string{"att.example.com"}, ""},
		{"", []string{"203.0.113.10"}, "shortlived"},
		{"", []string{"att.example.com", "2001:db8::1"}, "shortlived"},
		{"tlsserver", []string{"203.0.113.10"}, "tlsserver"},
	}
	for _, c := range cases {
		if got := acmeProfile(c.configured, c.hosts); got != c.want {
			t.Errorf("acmeProfile(%q, %v) = %q, want %q", c.configured, c.hosts, got, c.want)
		}
	}
	if acmeCA("production") != certmagic.LetsEncryptProductionCA || acmeCA("") != certmagic.LetsEncryptProductionCA {
		t.Error("production CA")
	}
	if acmeCA("Staging") != certmagic.LetsEncryptStagingCA {
		t.Error("staging CA")
	}
	if acmeCA("https://ca.example/dir") != "https://ca.example/dir" {
		t.Error("custom CA URL")
	}
}

func TestACMEManagerSetup(t *testing.T) {
	t.Parallel()
	u, _ := url.Parse("https://203.0.113.10")
	cfg := &config.Config{PublicURL: u, DataDir: t.TempDir(), TLSACMECA: "staging", TLSEmail: "ops@example.com"}
	m := newACMEManager(cfg, slog.New(slog.DiscardHandler))
	defer m.stop()
	if m.profile != "shortlived" || m.ca != certmagic.LetsEncryptStagingCA {
		t.Fatalf("profile %q ca %q", m.profile, m.ca)
	}
	if m.magic.DefaultServerName != "203.0.113.10" {
		t.Errorf("DefaultServerName = %q", m.magic.DefaultServerName)
	}
	if m.issuer.Profile != "shortlived" || !m.issuer.Agreed || m.issuer.Email != "ops@example.com" {
		t.Errorf("issuer: profile %q agreed %v email %q", m.issuer.Profile, m.issuer.Agreed, m.issuer.Email)
	}
	tc := m.tlsConfig()
	for _, p := range []string{"h2", "http/1.1", "acme-tls/1"} {
		if !slices.Contains(tc.NextProtos, p) {
			t.Errorf("NextProtos %v lacks %s", tc.NextProtos, p)
		}
	}
	if tc.NextProtos[0] != "h2" {
		t.Errorf("h2 must be preferred: %v", tc.NextProtos)
	}
	// The port-80 chain: challenge handler wrapping the redirect + /healthz mux.
	h := m.issuer.HTTPChallengeHandler(redirectToHTTPS(m.hosts, "203.0.113.10", "443"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 {
		t.Fatalf("/healthz through the challenge handler = %d", w.Code)
	}
	w = httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/x", nil)
	r.Host = "203.0.113.10"
	h.ServeHTTP(w, r)
	if w.Code != http.StatusPermanentRedirect || w.Header().Get("Location") != "https://203.0.113.10/x" {
		t.Fatalf("redirect through the challenge handler = %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestNextDailyRun(t *testing.T) {
	t.Parallel()
	msk, _ := time.LoadLocation("Europe/Moscow")
	berlin, _ := time.LoadLocation("Europe/Berlin")
	cases := []struct {
		now  time.Time
		loc  *time.Location
		want time.Time
	}{
		{time.Date(2026, 10, 6, 1, 0, 0, 0, msk), msk, time.Date(2026, 10, 6, 3, 30, 0, 0, msk)},
		{time.Date(2026, 10, 6, 3, 30, 0, 0, msk), msk, time.Date(2026, 10, 7, 3, 30, 0, 0, msk)},
		{time.Date(2026, 10, 6, 23, 59, 0, 0, msk), msk, time.Date(2026, 10, 7, 3, 30, 0, 0, msk)},
		{time.Date(2026, 12, 31, 12, 0, 0, 0, msk), msk, time.Date(2027, 1, 1, 3, 30, 0, 0, msk)},
		// DST change night in Berlin (clocks jump 02:00 → 03:00): still 03:30 local.
		{time.Date(2026, 3, 28, 22, 0, 0, 0, berlin), berlin, time.Date(2026, 3, 29, 3, 30, 0, 0, berlin)},
		{time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), nil, time.Date(2026, 10, 6, 3, 30, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		got := nextDailyRun(c.now, c.loc)
		if !got.Equal(c.want) {
			t.Errorf("nextDailyRun(%v) = %v, want %v", c.now, got, c.want)
		}
		if !got.After(c.now) {
			t.Errorf("nextDailyRun(%v) not in the future", c.now)
		}
	}
}

func TestUntilDailyRunUsesJobClock(t *testing.T) {
	t.Parallel()
	msk, _ := time.LoadLocation("Europe/Moscow")
	cfg := &config.Config{Timezone: msk}
	// A clock far in the past must still give a positive wait (no busy loop).
	now := time.Date(2001, 1, 1, 3, 0, 0, 0, msk)
	j := &jobs{cfg: cfg, now: func() time.Time { return now }}
	if got := j.untilDailyRun(); got != 30*time.Minute {
		t.Fatalf("untilDailyRun = %s, want 30m", got)
	}
}

func TestBackupRotation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, n := range []string{
		"zoom-checker-2026-10-01.db", "zoom-checker-2026-10-03.db", "zoom-checker-2026-10-02.db",
		"zoom-checker-2026-09-30.db", "notes.txt", ".tmp-zoom-checker-2026-10-04.db",
	} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	names, err := listBackups(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"zoom-checker-2026-10-03.db", "zoom-checker-2026-10-02.db", "zoom-checker-2026-10-01.db", "zoom-checker-2026-09-30.db"}
	if !slices.Equal(names, want) {
		t.Fatalf("listBackups = %v", names)
	}
	removed, err := pruneBackups(dir, 2)
	if err != nil || removed != 2 {
		t.Fatalf("prune: %d %v", removed, err)
	}
	names, _ = listBackups(dir)
	if !slices.Equal(names, want[:2]) {
		t.Fatalf("after prune: %v", names)
	}
	for _, keep := range []string{"notes.txt", ".tmp-zoom-checker-2026-10-04.db"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("%s removed: %v", keep, err)
		}
	}
	if n, _ := listBackups(filepath.Join(dir, "missing")); n != nil {
		t.Error("missing dir should list nothing")
	}
}

func TestJobsBackupAndRetention(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t, map[string]string{"BACKUP_KEEP": "2", "EVENTS_RETENTION_DAYS": "30"})
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		t.Fatal(err)
	}
	st := openStore(t, cfg)
	ctx := context.Background()
	if err := st.DB().Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 6, 0, 30, 0, 0, time.UTC) // 03:30 in Moscow
	j := &jobs{cfg: cfg, db: st.DB(), st: st, log: slog.New(slog.DiscardHandler), now: func() time.Time { return now }}

	path, err := j.backup(ctx)
	if err != nil || filepath.Base(path) != "zoom-checker-2026-10-06.db" {
		t.Fatalf("backup = %q %v", path, err)
	}
	if again, err := j.backup(ctx); err != nil || again != "" {
		t.Fatalf("second backup the same day = %q %v", again, err)
	}
	for _, d := range []time.Time{now.AddDate(0, 0, 1), now.AddDate(0, 0, 2)} {
		now = d
		if _, err := j.backup(ctx); err != nil {
			t.Fatal(err)
		}
	}
	names, _ := listBackups(j.backupDir())
	if !slices.Equal(names, []string{"zoom-checker-2026-10-08.db", "zoom-checker-2026-10-07.db"}) {
		t.Fatalf("kept backups = %v", names)
	}

	// Retention removes raw events older than the limit only; the configured
	// 30 days are raised to MinEventsRetentionDays, so a 40-day-old event (its
	// session may still be linked by hand) survives.
	for _, age := range []int{MinEventsRetentionDays + 10, 40, 1} {
		if _, err := st.InsertWebhookEvent(ctx, eventAt(now.AddDate(0, 0, -age))); err != nil {
			t.Fatal(err)
		}
	}
	// The events of a session linked to a lesson are that lesson's source of
	// truth: they stay whatever their age.
	old := now.AddDate(0, 0, -2*MinEventsRetentionDays)
	g := &model.Group{Name: "10А"}
	if err := st.CreateGroup(ctx, g); err != nil {
		t.Fatal(err)
	}
	l := &model.Lesson{GroupID: g.ID, Title: "Урок", ZoomMeetingID: "85746065432", PlannedStartAt: old, PlannedEndAt: old.Add(time.Hour)}
	if err := st.CreateLesson(ctx, l); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertZoomMeeting(ctx, &model.ZoomMeeting{MeetingUUID: "linked==", MeetingID: "85746065432",
		FirstEventAt: old, LessonID: &l.ID}); err != nil {
		t.Fatal(err)
	}
	linked := eventAt(old)
	linked.EventType, linked.MeetingUUID = "meeting.participant_joined", "linked=="
	linkedID, err := st.InsertWebhookEvent(ctx, linked)
	if err != nil {
		t.Fatal(err)
	}
	j.applyRetention(ctx)
	evs, err := st.ListWebhookEvents(ctx, storeEventFilter())
	if err != nil || len(evs) != 3 {
		t.Fatalf("events after retention: %d %v", len(evs), err)
	}
	for _, e := range evs {
		if e.ID != linkedID && now.Sub(e.ReceivedAt) > MinEventsRetentionDays*24*time.Hour {
			t.Errorf("event received %s survived retention", e.ReceivedAt)
		}
	}
	if !slices.ContainsFunc(evs, func(e model.WebhookEvent) bool { return e.ID == linkedID }) {
		t.Error("an event of a session linked to a lesson was purged")
	}
}

// The processor writes user-facing times (automatic reopen audit) in TIMEZONE.
func TestNewProcessorUsesConfiguredTimezone(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t, map[string]string{"TIMEZONE": "Asia/Yekaterinburg"})
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		t.Fatal(err)
	}
	st := openStore(t, cfg)
	p := newProcessor(cfg, st, nil, slog.New(slog.DiscardHandler), time.Now)
	if p.Location() != cfg.Timezone || p.Location().String() != "Asia/Yekaterinburg" {
		t.Fatalf("processor location = %v, want %v", p.Location(), cfg.Timezone)
	}
}

func TestStaleInitialPasswordFile(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t, map[string]string{"BACKUP_KEEP": "0"})
	st := openStore(t, cfg)
	ctx := context.Background()
	if err := st.DB().Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	if err := bootstrapAdmin(ctx, st, cfg, log); err != nil {
		t.Fatal(err)
	}
	pwFile := InitialPasswordPath(cfg.DataDir)
	if _, err := os.Stat(pwFile); err != nil {
		t.Fatalf("password file not written: %v", err)
	}

	// ADMIN_USERNAME edited after the first start: the file still holds the
	// only copy of a valid first-login password and must stay.
	renamed := *cfg
	renamed.AdminUsername = "someone-else"
	if err := bootstrapAdmin(ctx, st, &renamed, log); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pwFile); err != nil {
		t.Fatalf("valid password file removed after ADMIN_USERNAME changed: %v", err)
	}
	if n, _ := st.CountUsers(ctx); n != 1 {
		t.Fatalf("users = %d, want the one bootstrap admin", n)
	}

	// Once the password was changed the file is stale and goes away.
	u, err := st.GetUserByUsername(ctx, model.AuthLocal, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if u.PasswordHash, err = auth.HashPassword("Changed-Pass-2026"); err != nil {
		t.Fatal(err)
	}
	u.MustChangePassword = false
	if err := st.UpdateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := bootstrapAdmin(ctx, st, &renamed, log); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pwFile); !os.IsNotExist(err) {
		t.Fatalf("stale password file kept: %v", err)
	}
}

func TestEffectiveEventsRetentionDays(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want int }{
		{0, 0},
		{-5, 0},
		{1, MinEventsRetentionDays},
		{30, MinEventsRetentionDays},
		{MinEventsRetentionDays, MinEventsRetentionDays},
		{365, 365},
	}
	for _, c := range cases {
		if got := EffectiveEventsRetentionDays(c.in); got != c.want {
			t.Errorf("EffectiveEventsRetentionDays(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestSlogCoreBridge(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	zl := zap.New(&slogCore{log: log}).Named("certmagic").With(zap.String("identifier", "203.0.113.10"))
	zl.Error("could not get certificate", zap.Int("attempt", 2))
	zl.Info("routine")
	out := buf.String()
	for _, want := range []string{"level=ERROR", "could not get certificate", "identifier=203.0.113.10", "attempt=2", "logger=certmagic", "level=DEBUG msg=routine"} {
		if !strings.Contains(out, want) {
			t.Errorf("log output lacks %q:\n%s", want, out)
		}
	}
	quiet := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if (&slogCore{log: quiet}).Enabled(zap.InfoLevel) {
		t.Error("zap info should be debug-level in slog")
	}
}

func TestNewLogger(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	l := NewLogger("warn", "json", &buf)
	l.Info("hidden")
	l.Warn("shown", "k", "v")
	if strings.Contains(buf.String(), "hidden") || !strings.Contains(buf.String(), `"msg":"shown"`) {
		t.Fatalf("json/warn logger output: %s", buf.String())
	}
	if ParseLevel("nonsense") != slog.LevelInfo || ParseLevel("DEBUG") != slog.LevelDebug {
		t.Fatal("ParseLevel")
	}
}

func eventAt(t time.Time) *model.WebhookEvent {
	return &model.WebhookEvent{
		EventType:        "meeting.started",
		DedupeKey:        t.String(),
		Payload:          "{}",
		ReceivedAt:       t,
		ProcessingStatus: model.EventProcessed,
	}
}

func storeEventFilter() store.EventFilter { return store.EventFilter{Limit: 100} }
