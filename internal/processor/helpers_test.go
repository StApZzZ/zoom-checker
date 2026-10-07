package processor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"zoom-checker/internal/db"
	"zoom-checker/internal/model"
	"zoom-checker/internal/names"
	"zoom-checker/internal/realtime"
	"zoom-checker/internal/store"
	"zoom-checker/internal/zoom"
)

// Lesson day used by the tests (same date as internal/zoom/testdata); times are UTC.
var day = time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)

func hms(h, m, s int) time.Time {
	return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(s)*time.Second)
}

func hm(h, m int) time.Time { return hms(h, m, 0) }

const (
	testSecret  = "test-secret-token"
	testMeeting = "85746065432"
	hostAccount = "z8yCxAbCDEfg23HiJKl4mN"
)

// clock is a settable clock shared by the store and the processor.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// syncBuffer collects log output from several goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type env struct {
	t      testing.TB
	ctx    context.Context
	db     *db.DB
	st     *store.Store
	p      *Processor
	broker *realtime.Broker
	clock  *clock
	logs   *syncBuffer
}

func newEnv(t testing.TB) *env {
	t.Helper()
	ctx := context.Background()
	d := openTestDB(t)
	clk := &clock{t: hm(8, 0)}
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	st := store.New(d, store.WithClock(clk.Now))
	b := realtime.NewBroker()
	e := &env{t: t, ctx: ctx, db: d, st: st, broker: b, clock: clk, logs: logs,
		p: New(st, b, log, WithClock(clk.Now))}
	t.Cleanup(func() {
		if t.Failed() && os.Getenv("PROCESSOR_TEST_LOGS") != "" {
			t.Log(logs.String())
		}
	})
	return e
}

// ---- fixtures ----

type groupFixture struct {
	id       int64
	students map[string]int64 // full name -> id
}

func (e *env) group(name string, students ...string) groupFixture {
	e.t.Helper()
	g := &model.Group{Name: name}
	if err := e.st.CreateGroup(e.ctx, g); err != nil {
		e.t.Fatal(err)
	}
	gf := groupFixture{id: g.ID, students: map[string]int64{}}
	for _, n := range students {
		gf.students[n] = e.student(g.ID, n)
	}
	return gf
}

func (e *env) student(groupID int64, name string) int64 {
	e.t.Helper()
	s := &model.Student{GroupID: groupID, FullName: name}
	if err := e.st.CreateStudent(e.ctx, s); err != nil {
		e.t.Fatal(err)
	}
	return s.ID
}

// lesson creates a lesson the way the API does: insert, then RelinkLesson.
func (e *env) lesson(groupID int64, meetingID string, start, end time.Time) int64 {
	e.t.Helper()
	l := &model.Lesson{GroupID: groupID, Title: "Занятие " + start.Format("02.01 15:04"), ZoomMeetingID: meetingID,
		PlannedStartAt: start, PlannedEndAt: end}
	if err := e.st.CreateLesson(e.ctx, l); err != nil {
		e.t.Fatal(err)
	}
	if err := e.p.RelinkLesson(e.ctx, l.ID); err != nil {
		e.t.Fatal(err)
	}
	return l.ID
}

func (e *env) settings(mut func(*model.Settings)) {
	e.t.Helper()
	s, err := e.st.GetSettings(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	mut(&s)
	if err := e.st.PutSettings(e.ctx, s, nil); err != nil {
		e.t.Fatal(err)
	}
}

// ---- webhook delivery ----

// deliver runs body through the ingest path of the webhook endpoint: Zoom
// signature (signed and verified at the current clock), zoom.Parse,
// BuildRawEvent, InsertWebhookEvent. It does not process the event.
func (e *env) deliver(body []byte) *model.WebhookEvent {
	e.t.Helper()
	// Webhooks arrive a second after they happen; the clock only moves forward, so a
	// test can deliver an old event late by setting the clock first.
	if ev, err := zoom.Parse(body); err == nil {
		if at := ev.OccurredAt(); !at.IsZero() && e.clock.Now().Before(at.Add(time.Second)) {
			e.clock.Set(at.Add(time.Second))
		}
	}
	now := e.clock.Now()
	tsHeader, sigHeader := zoom.Sign(testSecret, now, body)
	h := http.Header{}
	h.Set(zoom.HeaderTimestamp, tsHeader)
	h.Set(zoom.HeaderSignature, sigHeader)
	if err := zoom.Verify(testSecret, h, body, now, 0); err != nil {
		e.t.Fatalf("verify: %v", err)
	}
	ev, perr := zoom.Parse(body)
	raw := BuildRawEvent(body, ev, perr, now)
	t0 := time.Now()
	if _, err := e.st.InsertWebhookEvent(e.ctx, raw); err != nil {
		e.t.Fatalf("after %v: %v", time.Since(t0), err)
	}
	return raw
}

// process runs the worker until nothing is due.
func (e *env) process() int {
	e.t.Helper()
	total := 0
	for {
		n, err := e.p.ProcessPending(e.ctx)
		if err != nil {
			e.t.Fatal(err)
		}
		total += n
		if n == 0 {
			return total
		}
	}
}

// send delivers bodies and processes them, one by one.
func (e *env) send(bodies ...[]byte) {
	e.t.Helper()
	for _, b := range bodies {
		e.deliver(b)
		e.process()
	}
}

func (e *env) rebuild(lessonID int64) {
	e.t.Helper()
	if err := e.p.RebuildLesson(e.ctx, lessonID); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) event(id int64) *model.WebhookEvent {
	e.t.Helper()
	ev, err := e.st.GetWebhookEvent(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return ev
}

func (e *env) session(uuid string) *model.ZoomMeeting {
	e.t.Helper()
	m, err := e.st.GetZoomMeetingByUUID(e.ctx, uuid)
	if err != nil {
		e.t.Fatalf("session %s: %v", uuid, err)
	}
	return m
}

func (e *env) getLesson(id int64) *model.Lesson {
	e.t.Helper()
	l, err := e.st.GetLesson(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return l
}

func (e *env) attendance(lessonID int64) map[int64]model.Attendance {
	e.t.Helper()
	rows, err := e.st.ListAttendance(e.ctx, lessonID)
	if err != nil {
		e.t.Fatal(err)
	}
	out := make(map[int64]model.Attendance, len(rows))
	for _, a := range rows {
		out[a.StudentID] = a
	}
	return out
}

func (e *env) row(lessonID, studentID int64) model.Attendance {
	e.t.Helper()
	a, err := e.st.GetAttendance(e.ctx, lessonID, studentID)
	if err != nil {
		e.t.Fatalf("attendance of student %d in lesson %d: %v", studentID, lessonID, err)
	}
	return *a
}

func (e *env) participants(lessonID int64) map[string]model.Participant {
	e.t.Helper()
	ps, err := e.st.ListParticipants(e.ctx, lessonID)
	if err != nil {
		e.t.Fatal(err)
	}
	out := make(map[string]model.Participant, len(ps))
	for _, p := range ps {
		out[p.NormalizedName] = p
	}
	return out
}

func (e *env) participant(lessonID int64, displayName string) model.Participant {
	e.t.Helper()
	p, ok := e.participants(lessonID)[names.Normalize(displayName)]
	if !ok {
		e.t.Fatalf("no participant %q in lesson %d", displayName, lessonID)
	}
	return p
}

func (e *env) intervals(lessonID int64) []model.Interval {
	e.t.Helper()
	ivs, err := e.st.ListIntervals(e.ctx, lessonID)
	if err != nil {
		e.t.Fatal(err)
	}
	return ivs
}

// ---- Zoom payloads ----

// device is one Zoom participant connection.
type device struct {
	name      string
	puuid     string // participant_uuid (per join)
	userID    string // meeting-local user_id
	accountID string // participant_user_id (signed-in users)
}

// dev builds a guest device whose ids derive from key.
func dev(name, key string) device {
	return device{name: name, puuid: "PU-" + key, userID: "UID-" + key}
}

// zmeeting builds realistic webhook bodies (shape of internal/zoom/testdata) for one
// Zoom session.
type zmeeting struct {
	id, uuid, host, topic string
	start                 time.Time // object.start_time of participant events
	etsOffset             int64     // event_ts = time + offset (ms)
}

func newMeeting(id, uuid string, start time.Time) *zmeeting {
	return &zmeeting{id: id, uuid: uuid, host: hostAccount, topic: "Математика — 10А", start: start, etsOffset: 250}
}

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func (m *zmeeting) object() map[string]any {
	return map[string]any{
		"id": m.id, "uuid": m.uuid, "host_id": m.host, "topic": m.topic, "type": 8,
		"start_time": rfc(m.start), "timezone": "Europe/Moscow", "duration": 90,
	}
}

func (m *zmeeting) envelope(typ string, t time.Time, obj map[string]any) []byte {
	body, err := json.Marshal(map[string]any{
		"event":    typ,
		"event_ts": t.UnixMilli() + m.etsOffset,
		"payload":  map[string]any{"account_id": "Xq3vKc9mT0yLw2BnR5aZpQ", "object": obj},
	})
	if err != nil {
		panic(err)
	}
	return body
}

func (m *zmeeting) started(t time.Time) []byte {
	obj := m.object()
	obj["start_time"] = rfc(t)
	return m.envelope(zoom.EventMeetingStarted, t, obj)
}

func (m *zmeeting) ended(t time.Time) []byte {
	obj := m.object()
	obj["end_time"] = rfc(t)
	return m.envelope(zoom.EventMeetingEnded, t, obj)
}

func participantObj(d device) map[string]any {
	return map[string]any{
		"user_id": d.userID, "user_name": d.name, "id": "", "participant_uuid": d.puuid, "email": "",
		"registrant_id": "", "participant_user_id": d.accountID, "public_ip": "203.0.113.17",
	}
}

func (m *zmeeting) joined(d device, t time.Time) []byte {
	obj := m.object()
	p := participantObj(d)
	p["join_time"] = rfc(t)
	obj["participant"] = p
	return m.envelope(zoom.EventParticipantJoined, t, obj)
}

func (m *zmeeting) left(d device, t time.Time) []byte {
	return m.leftWith(d, t, "left the meeting")
}

func (m *zmeeting) leftWith(d device, t time.Time, reason string) []byte {
	obj := m.object()
	p := participantObj(d)
	p["leave_time"] = rfc(t)
	p["leave_reason"] = "left the meeting. Reason : " + reason
	p["private_ip"] = "192.168.1.23"
	obj["participant"] = p
	return m.envelope(zoom.EventParticipantLeft, t, obj)
}

// breakout builds a breakout-room join/leave: object.uuid is the main meeting,
// user_id is room-local, parent_user_id is the main-meeting user_id.
func (m *zmeeting) breakout(join bool, d device, room string, t time.Time) []byte {
	obj := m.object()
	obj["breakout_room_uuid"] = room
	p := participantObj(d)
	p["user_id"] = d.userID + "-room"
	p["parent_user_id"] = d.userID
	typ := zoom.EventBreakoutJoined
	if join {
		p["join_time"] = rfc(t)
	} else {
		typ = zoom.EventBreakoutLeft
		p["leave_time"] = rfc(t)
		p["leave_reason"] = "left the meeting. Reason : Breakout room closed"
	}
	obj["participant"] = p
	return m.envelope(typ, t, obj)
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "zoom", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// snapshot renders a lesson projection in a stable textual form, to compare two
// runs (ids and computed_at stamps excluded).
func (e *env) snapshot(lessonID int64) string {
	e.t.Helper()
	var buf bytes.Buffer
	l := e.getLesson(lessonID)
	fmt.Fprintf(&buf, "lesson status=%s start=%v end=%v\n", l.Status, tstr(l.ActualStartAt), tstr(l.ActualEndAt))
	att := e.attendance(lessonID)
	ids := make([]int64, 0, len(att))
	for id := range att {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		a := att[id]
		fmt.Fprintf(&buf, "student %d %s total=%d first=%v last=%v late=%v early=%v auto=%s final=%s\n", id, a.CurrentStatus,
			a.TotalDurationSeconds, tstr(a.FirstJoinAt), tstr(a.LastLeaveAt), ip(a.LateSeconds), ip(a.EarlyLeaveSeconds),
			a.AutoStatus, a.FinalStatus)
	}
	ps, _ := e.st.ListParticipants(e.ctx, lessonID)
	for _, p := range ps {
		fmt.Fprintf(&buf, "participant %q %s student=%v reason=%s %s total=%d devices=%d first=%v last=%v\n", p.DisplayName,
			p.Resolution, ip(p.StudentID), p.IgnoreReason, p.CurrentStatus, p.TotalSeconds, p.Devices, tstr(p.FirstJoinAt),
			tstr(p.LastLeaveAt))
	}
	for _, iv := range e.intervals(lessonID) {
		fmt.Fprintf(&buf, "interval %q %s..%v dur=%v reason=%q uuid=%s\n", iv.ZoomUserName, rfc(iv.JoinAt), tstr(iv.LeaveAt),
			ip(iv.DurationSeconds), iv.LeaveReason, iv.MeetingUUID)
	}
	return buf.String()
}

func tstr(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func ip(v *int64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprint(*v)
}

func ptrTime(t time.Time) *time.Time { return &t }
