package httpapi

// Test environment shared by the auth / admin / zoom route tests: a real
// SQLite store in t.TempDir(), a fake Processor and in-process requests.
// Helpers carry an "az" prefix so they cannot clash with other test files of
// this package.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"zoom-checker/internal/auth"
	"zoom-checker/internal/config"
	"zoom-checker/internal/db"
	"zoom-checker/internal/model"
	"zoom-checker/internal/realtime"
	"zoom-checker/internal/store"
)

// azProcessor is a fake Processor that records calls. LinkSession really
// updates the link so handlers can read it back.
type azProcessor struct {
	mu       sync.Mutex
	st       *store.Store
	notifies int
	rebuilt  []int64
	links    []azLinkCall
	linkErr  error
}

type azLinkCall struct {
	SessionID int64
	LessonID  *int64
}

func (p *azProcessor) Notify() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.notifies++
}

func (p *azProcessor) RebuildLesson(_ context.Context, lessonID int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rebuilt = append(p.rebuilt, lessonID)
	return nil
}

func (p *azProcessor) RelinkLesson(context.Context, int64) error { return nil }

func (p *azProcessor) LinkSession(ctx context.Context, sessionID int64, lessonID *int64) error {
	p.mu.Lock()
	p.links = append(p.links, azLinkCall{sessionID, lessonID})
	err := p.linkErr
	p.mu.Unlock()
	if err != nil {
		return err
	}
	mode := model.LinkManual
	return p.st.SetZoomMeetingLink(ctx, sessionID, lessonID, mode)
}

func (p *azProcessor) notifyCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.notifies
}

func (p *azProcessor) rebuiltIDs() []int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int64(nil), p.rebuilt...)
}

// azLogBuffer collects log output (also from background goroutines).
type azLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *azLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *azLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type azEnv struct {
	t    *testing.T
	srv  *Server
	st   *store.Store
	cfg  *config.Config
	proc *azProcessor
	logs *azLogBuffer
}

// azSetup is what a test may change before the server is built.
type azSetup struct {
	cfg  *config.Config
	deps *Deps
}

func newAZEnv(t *testing.T, mods ...func(*azSetup)) *azEnv {
	t.Helper()
	dir := t.TempDir()
	dbURL := "sqlite://" + filepath.ToSlash(filepath.Join(dir, "zc.db"))
	d, err := db.Open(context.Background(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := store.New(d)
	pub, _ := url.Parse("http://app.test")
	cfg := &config.Config{
		PublicURL:         pub,
		ListenAddr:        ":8080",
		TLSMode:           config.TLSOff,
		DataDir:           dir,
		DatabaseURL:       dbURL,
		Timezone:          time.UTC,
		ZoomWebhookMaxAge: 2 * time.Hour,
		AdminUsername:     "admin",
		SessionTTL:        24 * time.Hour,
		LocalAuthEnabled:  true,
		OIDC:              config.OIDCConfig{ButtonLabel: "Войти через Keycloak"},
	}
	proc := &azProcessor{st: st}
	logs := &azLogBuffer{}
	deps := Deps{
		Config:    cfg,
		Store:     st,
		Processor: proc,
		Broker:    realtime.NewBroker(),
		Log:       slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	setup := &azSetup{cfg: cfg, deps: &deps}
	for _, m := range mods {
		m(setup)
	}
	srv := New(deps)
	closeServerAtCleanup(t, srv)
	return &azEnv{t: t, srv: srv, st: st, cfg: cfg, proc: proc, logs: logs}
}

// azRequest builds an in-process request. body may be nil, []byte/string
// (sent as is) or a value encoded as JSON.
func azRequest(t *testing.T, method, target string, body any) *http.Request {
	t.Helper()
	var rd io.Reader
	isJSON := false
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
		isJSON = true
	}
	r := httptest.NewRequest(method, "http://app.test"+target, rd)
	if isJSON {
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}

func (e *azEnv) serve(r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, r)
	return rec
}

// do sends a request with an optional session cookie.
func (e *azEnv) do(method, target string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	e.t.Helper()
	r := azRequest(e.t, method, target, body)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	return e.serve(r)
}

// createUser stores a user directly (password "" = no password hash).
func (e *azEnv) createUser(username, password string, role model.Role, mod func(*model.User)) *model.User {
	e.t.Helper()
	u := &model.User{Username: username, DisplayName: "Пользователь " + username, Role: role, AuthSource: model.AuthLocal}
	if password != "" {
		h, err := auth.HashPassword(password)
		if err != nil {
			e.t.Fatal(err)
		}
		u.PasswordHash = h
	}
	if mod != nil {
		mod(u)
	}
	if err := e.st.CreateUser(context.Background(), u); err != nil {
		e.t.Fatal(err)
	}
	return u
}

// sessionFor creates a session directly in the store and returns its cookie.
func (e *azEnv) sessionFor(u *model.User, source model.AuthSource, idToken string) *http.Cookie {
	e.t.Helper()
	token, hash := auth.NewToken()
	now := time.Now().UTC()
	if err := e.st.CreateSession(context.Background(), &model.Session{
		IDHash: hash, UserID: u.ID, CreatedAt: now, ExpiresAt: now.Add(time.Hour), LastSeenAt: now,
		AuthSource: source, OIDCIDToken: idToken,
	}); err != nil {
		e.t.Fatal(err)
	}
	return &http.Cookie{Name: SessionCookie, Value: token}
}

// login posts the login form and returns the response and the session cookie (nil on failure).
func (e *azEnv) login(username, password string) (*httptest.ResponseRecorder, *http.Cookie) {
	e.t.Helper()
	rec := e.do("POST", "/api/auth/login", map[string]string{"username": username, "password": password}, nil)
	return rec, azCookie(rec, SessionCookie)
}

// adminCookie creates an enabled admin "root" with a session.
func (e *azEnv) adminCookie() (*model.User, *http.Cookie) {
	e.t.Helper()
	u := e.createUser("root", "", model.RoleAdmin, nil)
	return u, e.sessionFor(u, model.AuthLocal, "")
}

func azCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name && c.Value != "" && c.MaxAge >= 0 {
			return c
		}
	}
	return nil
}

// azDecode decodes the JSON response body into T, failing the test on error.
func azDecode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %T from %d %q: %v", v, rec.Code, rec.Body.String(), err)
	}
	return v
}

type azErrorBody struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

// azWantError asserts status and error code and returns the message.
func azWantError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) string {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, status, rec.Body.String())
	}
	b := azDecode[azErrorBody](t, rec)
	if b.Error.Code != code {
		t.Fatalf("error code = %q, want %q; body %s", b.Error.Code, code, rec.Body.String())
	}
	if b.Error.Message == "" {
		t.Fatalf("error message empty: %s", rec.Body.String())
	}
	return b.Error.Message
}

func azWantStatus(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, status, rec.Body.String())
	}
}

// azAudit returns the audit entries with the given action.
func (e *azEnv) azAudit(action string) []model.AuditEntry {
	e.t.Helper()
	all, err := e.st.ListAudit(context.Background(), store.AuditFilter{})
	if err != nil {
		e.t.Fatal(err)
	}
	var out []model.AuditEntry
	for _, a := range all {
		if a.Action == action {
			out = append(out, a)
		}
	}
	return out
}

// azEventually polls cond until it holds or the deadline passes.
func azEventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
