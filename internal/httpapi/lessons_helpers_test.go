package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

// Test helpers of the lesson routes. Every identifier starts with "lt" so they
// cannot collide with helpers of other route groups in this package.

const ltMeetingID = "85746065432"

// ltT0 is the default "now" of lesson tests: 06.10.2026 18:30 Moscow time.
var ltT0 = time.Date(2026, 10, 6, 15, 30, 0, 0, time.UTC)

type ltClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *ltClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *ltClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// ltFakeProcessor records calls; hooks and errors can be injected.
type ltFakeProcessor struct {
	mu         sync.Mutex
	rebuilds   []int64
	relinks    []int64
	links      []int64
	notifies   int
	rebuildErr error
	onRebuild  func(ctx context.Context, lessonID int64) error
}

func (p *ltFakeProcessor) Notify() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.notifies++
}

func (p *ltFakeProcessor) RebuildLesson(ctx context.Context, lessonID int64) error {
	p.mu.Lock()
	p.rebuilds = append(p.rebuilds, lessonID)
	hook, err := p.onRebuild, p.rebuildErr
	p.mu.Unlock()
	if err != nil {
		return err
	}
	if hook != nil {
		return hook(ctx, lessonID)
	}
	return nil
}

func (p *ltFakeProcessor) RelinkLesson(ctx context.Context, lessonID int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.relinks = append(p.relinks, lessonID)
	return nil
}

func (p *ltFakeProcessor) LinkSession(ctx context.Context, sessionID int64, lessonID *int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.links = append(p.links, sessionID)
	return nil
}

func (p *ltFakeProcessor) Rebuilds() []int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int64(nil), p.rebuilds...)
}

func (p *ltFakeProcessor) Relinks() []int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int64(nil), p.relinks...)
}

type ltEnv struct {
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	srv    *Server
	proc   *ltFakeProcessor
	broker *realtime.Broker
	clock  *ltClock
	user   *model.User
	token  string
}

func newLTEnv(t *testing.T) *ltEnv {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, "sqlite://"+filepath.ToSlash(filepath.Join(t.TempDir(), "zc.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	clock := &ltClock{now: ltT0}
	st := store.New(d, store.WithClock(clock.Now))
	env := map[string]string{"DATA_DIR": t.TempDir(), "TIMEZONE": "Europe/Moscow"}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	broker := realtime.NewBroker()
	proc := &ltFakeProcessor{}
	srv := New(Deps{
		Config: cfg, Store: st, Processor: proc, Broker: broker,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: clock.Now,
	})
	closeServerAtCleanup(t, srv)
	u := &model.User{Username: "teacher", DisplayName: "Мария Ивановна", Role: model.RoleTeacher, AuthSource: model.AuthLocal}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	token, hash := auth.NewToken()
	if err := st.CreateSession(ctx, &model.Session{
		IDHash: hash, UserID: u.ID, CreatedAt: ltT0, LastSeenAt: ltT0, ExpiresAt: ltT0.Add(10 * 365 * 24 * time.Hour),
		AuthSource: model.AuthLocal,
	}); err != nil {
		t.Fatal(err)
	}
	return &ltEnv{t: t, ctx: ctx, st: st, srv: srv, proc: proc, broker: broker, clock: clock, user: u, token: token}
}

// do sends an authenticated request; body is JSON-encoded unless it is a string.
func (e *ltEnv) do(method, path string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = bytes.NewBufferString(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: e.token})
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	return rec
}

// ok asserts the status code and decodes the JSON body into out (if non-nil).
func (e *ltEnv) ok(rec *httptest.ResponseRecorder, status int, out any) {
	e.t.Helper()
	if rec.Code != status {
		e.t.Fatalf("status = %d, want %d; body: %s", rec.Code, status, rec.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			e.t.Fatalf("decode %T: %v; body: %s", out, err, rec.Body.String())
		}
	}
}

// ltAPIErr is the error envelope.
type ltAPIErr struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

func (e *ltEnv) fail(rec *httptest.ResponseRecorder, status int) ltAPIErr {
	e.t.Helper()
	var out ltAPIErr
	e.ok(rec, status, &out)
	if out.Error.Message == "" {
		e.t.Fatalf("error without message: %s", rec.Body.String())
	}
	return out
}

func (e *ltEnv) group(name string) *model.Group {
	e.t.Helper()
	g := &model.Group{Name: name}
	if err := e.st.CreateGroup(e.ctx, g); err != nil {
		e.t.Fatal(err)
	}
	return g
}

func (e *ltEnv) student(groupID int64, name, extID string) *model.Student {
	e.t.Helper()
	s := &model.Student{GroupID: groupID, FullName: name, ExternalID: extID}
	if err := e.st.CreateStudent(e.ctx, s); err != nil {
		e.t.Fatal(err)
	}
	return s
}

// lesson creates a lesson planned at start for 90 minutes.
func (e *ltEnv) lesson(groupID int64, title string, start time.Time) *model.Lesson {
	e.t.Helper()
	l := &model.Lesson{GroupID: groupID, Title: title, ZoomMeetingID: ltMeetingID,
		PlannedStartAt: start, PlannedEndAt: start.Add(90 * time.Minute)}
	if err := e.st.CreateLesson(e.ctx, l); err != nil {
		e.t.Fatal(err)
	}
	return l
}

// ltIv describes one stored interval of a fixture projection.
type ltIv struct {
	Name     string // participant display name (normalized key = names.Normalize by the caller)
	UUID     string
	Join     time.Time
	Leave    *time.Time
	Student  int64 // 0 = not a student
	Reason   string
	Meeting  string
	Duration *int64
}

// ltProjection writes a projection directly through the store, as a rebuild would.
type ltProjection struct {
	Participants []model.Participant
	Rows         []model.Attendance
	Intervals    []ltIv
}

func (e *ltEnv) project(lessonID int64, p ltProjection) (partIDs map[string]int64, rowIDs map[int64]int64) {
	e.t.Helper()
	partIDs, err := e.st.UpsertParticipants(e.ctx, lessonID, p.Participants)
	if err != nil {
		e.t.Fatal(err)
	}
	rowIDs, err = e.st.UpsertAttendanceComputed(e.ctx, lessonID, p.Rows)
	if err != nil {
		e.t.Fatal(err)
	}
	byName := map[string]int64{}
	for _, pp := range p.Participants {
		byName[pp.DisplayName] = partIDs[pp.NormalizedName]
	}
	ivs := make([]model.Interval, 0, len(p.Intervals))
	for _, iv := range p.Intervals {
		pid := byName[iv.Name]
		mi := model.Interval{
			ZoomParticipantID: &pid, MeetingUUID: "uuid-1", ZoomParticipantUUID: iv.UUID,
			ZoomUserName: iv.Name, JoinAt: iv.Join, LeaveAt: iv.Leave, LeaveReason: iv.Reason,
		}
		if iv.Meeting != "" {
			mi.MeetingUUID = iv.Meeting
		}
		if iv.Leave != nil {
			d := int64(iv.Leave.Sub(iv.Join) / time.Second)
			mi.DurationSeconds = &d
		}
		if iv.Student != 0 {
			rid := rowIDs[iv.Student]
			mi.AttendanceID = &rid
		}
		ivs = append(ivs, mi)
	}
	if err := e.st.ReplaceIntervals(e.ctx, lessonID, ivs); err != nil {
		e.t.Fatal(err)
	}
	return partIDs, rowIDs
}

func (e *ltEnv) audit(lessonID int64) []model.AuditEntry {
	e.t.Helper()
	out, err := e.st.ListAudit(e.ctx, store.AuditFilter{LessonID: &lessonID})
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e *ltEnv) auditActions(lessonID int64) []string {
	var out []string
	for _, a := range e.audit(lessonID) {
		out = append(out, a.Action)
	}
	return out
}

func ltPtr[T any](v T) *T { return &v }

func ltAt(hhmm string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", "2026-10-06 "+hhmm, ltMoscow())
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func ltMoscow() *time.Location {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		panic(err)
	}
	return loc
}

// ltWaitPoke reports whether ch receives within d.
func ltWaitPoke(ch <-chan struct{}, d time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}
