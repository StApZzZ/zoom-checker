package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
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

// Test helpers of the group routes (prefix gt to stay clear of the helpers of
// other route groups in this package).

// gtNow is the fixed server clock of the group tests (12:00 in Moscow).
var gtNow = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)

// gtMoscow is a fixed UTC+3 zone (no tzdata needed).
var gtMoscow = time.FixedZone("MSK", 3*60*60)

// gtProcessor records lesson rebuilds.
type gtProcessor struct {
	mu      sync.Mutex
	rebuilt []int64
}

func (p *gtProcessor) Notify() {}
func (p *gtProcessor) RebuildLesson(_ context.Context, id int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rebuilt = append(p.rebuilt, id)
	return nil
}
func (p *gtProcessor) RelinkLesson(context.Context, int64) error        { return nil }
func (p *gtProcessor) LinkSession(context.Context, int64, *int64) error { return nil }
func (p *gtProcessor) reset()                                           { p.mu.Lock(); p.rebuilt = nil; p.mu.Unlock() }
func (p *gtProcessor) ids() []int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := slices.Clone(p.rebuilt)
	slices.Sort(out)
	return slices.Compact(out)
}

type gtEnv struct {
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	proc   *gtProcessor
	broker *realtime.Broker
	srv    *httptest.Server
	cookie *http.Cookie
	user   *model.User
}

func newGTEnv(t *testing.T) *gtEnv {
	t.Helper()
	return newGTEnvDB(t, "sqlite://"+filepath.ToSlash(filepath.Join(t.TempDir(), "zc.db")))
}

// newGTEnvDB is newGTEnv over the database at dsn (migrated here).
func newGTEnvDB(t *testing.T, dsn string) *gtEnv {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(d)
	pub, _ := url.Parse("http://localhost:8080")
	cfg := &config.Config{PublicURL: pub, Timezone: gtMoscow, SessionTTL: 24 * time.Hour, LocalAuthEnabled: true}
	e := &gtEnv{t: t, ctx: ctx, st: st, proc: &gtProcessor{}, broker: realtime.NewBroker()}
	srv := New(Deps{
		Config: cfg, Store: st, Processor: e.proc, Broker: e.broker,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return gtNow },
	})
	closeServerAtCleanup(t, srv)
	e.srv = httptest.NewServer(srv.Handler())
	t.Cleanup(e.srv.Close)

	e.user = &model.User{Username: "teacher", DisplayName: "Мария Петровна", Role: model.RoleTeacher, AuthSource: model.AuthLocal}
	if err := st.CreateUser(ctx, e.user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	token, hash := auth.NewToken()
	if err := st.CreateSession(ctx, &model.Session{IDHash: hash, UserID: e.user.ID, CreatedAt: gtNow,
		ExpiresAt: gtNow.Add(24 * time.Hour), LastSeenAt: gtNow, AuthSource: model.AuthLocal}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	e.cookie = &http.Cookie{Name: SessionCookie, Value: token}
	return e
}

// gtResp is a finished HTTP exchange.
type gtResp struct {
	status int
	header http.Header
	body   []byte
}

func (r gtResp) decode(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
}

// apiErr decodes the error envelope.
func (r gtResp) apiErr(t *testing.T) gtErrBody {
	t.Helper()
	var e struct {
		Error gtErrBody `json:"error"`
	}
	r.decode(t, &e)
	return e.Error
}

type gtErrBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

func (e *gtEnv) raw(method, path, contentType string, body io.Reader, withCookie bool) gtResp {
	e.t.Helper()
	req, err := http.NewRequestWithContext(e.ctx, method, e.srv.URL+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if withCookie {
		req.AddCookie(e.cookie)
	}
	res, err := e.srv.Client().Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	return gtResp{status: res.StatusCode, header: res.Header, body: b}
}

// do sends body as JSON (nil = no body) with the session cookie.
func (e *gtEnv) do(method, path string, body any) gtResp {
	e.t.Helper()
	if body == nil {
		return e.raw(method, path, "", nil, true)
	}
	b, err := json.Marshal(body)
	if err != nil {
		e.t.Fatal(err)
	}
	return e.raw(method, path, "application/json", bytes.NewReader(b), true)
}

// upload posts a multipart form with one "file" part.
func (e *gtEnv) upload(path, filename string, data []byte) gtResp {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		e.t.Fatal(err)
	}
	_, _ = fw.Write(data)
	if err := mw.Close(); err != nil {
		e.t.Fatal(err)
	}
	return e.raw(http.MethodPost, path, mw.FormDataContentType(), &buf, true)
}

func (e *gtEnv) want(r gtResp, status int) {
	e.t.Helper()
	if r.status != status {
		e.t.Fatalf("status %d, want %d; body %s", r.status, status, r.body)
	}
}

// group creates a group through the API.
func (e *gtEnv) group(name string) GroupSummaryDTO {
	e.t.Helper()
	r := e.do(http.MethodPost, "/api/groups", map[string]any{"name": name})
	e.want(r, http.StatusCreated)
	var g GroupSummaryDTO
	r.decode(e.t, &g)
	return g
}

// student adds a student through the API.
func (e *gtEnv) student(groupID int64, name, ext string) StudentDTO {
	e.t.Helper()
	r := e.do(http.MethodPost, "/api/groups/"+grpID(groupID)+"/students", map[string]any{"full_name": name, "external_id": ext})
	e.want(r, http.StatusCreated)
	var st StudentDTO
	r.decode(e.t, &st)
	return st
}

// detail loads a group with its students.
func (e *gtEnv) detail(groupID int64) GroupDetailDTO {
	e.t.Helper()
	r := e.do(http.MethodGet, "/api/groups/"+grpID(groupID), nil)
	e.want(r, http.StatusOK)
	var g GroupDetailDTO
	r.decode(e.t, &g)
	return g
}

// lesson stores a lesson directly.
func (e *gtEnv) lesson(groupID int64, title string, start time.Time, status model.LessonStatus) *model.Lesson {
	e.t.Helper()
	l := &model.Lesson{GroupID: groupID, Title: title, ZoomMeetingID: "1234567890",
		PlannedStartAt: start, PlannedEndAt: start.Add(90 * time.Minute), Status: status}
	if err := e.st.CreateLesson(e.ctx, l); err != nil {
		e.t.Fatalf("create lesson: %v", err)
	}
	return l
}

// audit lists the audit entries of a group, oldest first.
func (e *gtEnv) audit(groupID int64) []model.AuditEntry {
	e.t.Helper()
	list, err := e.st.ListAudit(e.ctx, store.AuditFilter{GroupID: &groupID})
	if err != nil {
		e.t.Fatal(err)
	}
	slices.Reverse(list)
	return list
}

func gtActions(entries []model.AuditEntry) []string {
	out := make([]string, len(entries))
	for i, a := range entries {
		out[i] = a.Action
	}
	return out
}
