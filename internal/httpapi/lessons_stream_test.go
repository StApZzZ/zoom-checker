package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"zoom-checker/internal/export"
	"zoom-checker/internal/model"
	"zoom-checker/internal/realtime"
)

// ---- export ----

func TestLessonsExportContainsWholeRoster(t *testing.T) {
	t.Parallel()
	e, l, st, _ := ltLiveFixture(t)
	// A student who joined the group after the last rebuild has no row yet but
	// must be in the file too (TZ scenario 9).
	e.student(l.GroupID, "Яковлев Яков", "1004")
	if _, err := e.st.SetAttendanceComment(e.ctx, l.ID, st["petrov"].ID, "=HYPERLINK(\"x\")", nil, ltAt("18:30")); err != nil {
		t.Fatal(err)
	}

	rec := e.do("GET", fmt.Sprintf("/api/lessons/%d/export?format=csv", l.ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("csv export = %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("content type = %q", ct)
	}
	cd := rec.Header().Get("Content-Disposition")
	wantName := "Посещаемость DevOps-1 Linux 4 06.10.2026.csv"
	if !strings.HasPrefix(cd, `attachment; filename="Poseshchaemost DevOps-1 Linux 4 06.10.2026.csv"; filename*=UTF-8''`) ||
		!strings.HasSuffix(cd, lessonRFC5987(wantName)) {
		t.Errorf("content disposition = %q", cd)
	}
	csv := rec.Body.String()
	for _, name := range []string{"Иванов Иван", "Петров Пётр", "Сидоров Алексей", "Яковлев Яков"} {
		if !strings.Contains(csv, name) {
			t.Errorf("csv lacks %s:\n%s", name, csv)
		}
	}
	if !strings.Contains(csv, "'=HYPERLINK") {
		t.Error("formula in comment not escaped")
	}
	lines := strings.Split(strings.TrimSpace(csv), "\r\n")
	if len(lines) != 5 { // header + 4 students
		t.Errorf("csv lines = %d:\n%s", len(lines), csv)
	}
	// Sidorov never connected: absent, 0 minutes.
	for _, line := range lines {
		if strings.HasPrefix(line, "Сидоров Алексей;1003;DevOps-1;Linux 4;06.10.2026;;;0;") &&
			!strings.Contains(line, export.StatusText(model.FinalAbsent)) {
			t.Errorf("sidorov line = %q", line)
		}
	}

	rec = e.do("GET", fmt.Sprintf("/api/lessons/%d/export", l.ID), nil) // xlsx by default
	if rec.Code != http.StatusOK {
		t.Fatalf("xlsx export = %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Errorf("content type = %q", ct)
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), ".xlsx") {
		t.Errorf("content disposition = %q", rec.Header().Get("Content-Disposition"))
	}
	f, err := excelize.OpenReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := f.GetRows(export.SheetAttendance)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows[1:] {
		got = append(got, r[0])
	}
	if fmt.Sprint(got) != "[Иванов Иван Петров Пётр Сидоров Алексей Яковлев Яков]" {
		t.Errorf("xlsx students = %v", got)
	}
	un, err := f.GetRows(export.SheetUnidentified)
	if err != nil {
		t.Fatal(err)
	}
	if len(un) != 2 || un[1][0] != "Ваня" {
		t.Errorf("unidentified sheet = %v", un)
	}

	e.fail(e.do("GET", fmt.Sprintf("/api/lessons/%d/export?format=pdf", l.ID), nil), http.StatusUnprocessableEntity)
	e.fail(e.do("GET", "/api/lessons/9999/export?format=csv", nil), http.StatusNotFound)
}

// TestLessonsExportTimeZone: tz (the browser's zone) sets the times and the
// clock column titles of the file; a missing or unknown tz falls back to
// TIMEZONE (Europe/Moscow in this fixture).
func TestLessonsExportTimeZone(t *testing.T) {
	t.Parallel()
	e, l, _, _ := ltLiveFixture(t)
	exportCSV := func(query string) (head []string, ivanov []string) {
		t.Helper()
		rec := e.do("GET", fmt.Sprintf("/api/lessons/%d/export?format=csv%s", l.ID, query), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("export%s = %d %s", query, rec.Code, rec.Body.String())
		}
		for i, line := range strings.Split(strings.TrimPrefix(rec.Body.String(), "\uFEFF"), "\r\n") {
			cells := strings.Split(line, ";")
			if i == 0 {
				head = cells
			} else if cells[0] == "Иванов Иван" {
				ivanov = cells
			}
		}
		if ivanov == nil {
			t.Fatalf("no Иванов row in export%s", query)
		}
		return head, ivanov
	}
	head, msk := exportCSV("")
	if head[5] != "Первый вход (UTC+3)" || head[6] != "Последний выход (UTC+3)" {
		t.Errorf("default header = %q", head)
	}
	for _, bad := range []string{"&tz=Mars%2FBase", "&tz=Local", "&tz=..%2F..%2Fetc%2Fpasswd", "&tz="} {
		if h, row := exportCSV(bad); h[5] != head[5] || row[5] != msk[5] {
			t.Errorf("tz%s: header %q, first join %q; want TIMEZONE", bad, h[5], row[5])
		}
	}
	head, ekb := exportCSV("&tz=Asia%2FYekaterinburg")
	if head[5] != "Первый вход (UTC+5)" || head[6] != "Последний выход (UTC+5)" {
		t.Errorf("tz header = %q", head)
	}
	m, err1 := time.Parse("15:04", msk[5])
	k, err2 := time.Parse("15:04", ekb[5])
	if err1 != nil || err2 != nil || k.Sub(m) != 2*time.Hour {
		t.Errorf("first join MSK %q, Yekaterinburg %q: want 2 hours apart", msk[5], ekb[5])
	}
}

// ---- SSE ----

type ltSSEEvent struct {
	Name string
	Data string
}

// ltOpenStream connects to an SSE endpoint and returns its events; the
// response is closed with the test.
func ltOpenStream(t *testing.T, e *ltEnv, ts *httptest.Server, path string) (*http.Response, <-chan ltSSEEvent) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: e.token})
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	ch := make(chan ltSSEEvent, 16)
	if res.StatusCode != http.StatusOK {
		close(ch)
		return res, ch
	}
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
		var ev ltSSEEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if ev.Name != "" || ev.Data != "" {
					ch <- ev
				}
				ev = ltSSEEvent{}
			case strings.HasPrefix(line, ":"):
				ch <- ltSSEEvent{Name: "comment", Data: strings.TrimSpace(line[1:])}
			case strings.HasPrefix(line, "event: "):
				ev.Name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ev.Data += strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return res, ch
}

// ltNextEvent waits for the next event with the given name (skipping others).
func ltNextEvent(t *testing.T, ch <-chan ltSSEEvent, name string, d time.Duration) (ltSSEEvent, bool) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return ltSSEEvent{}, false
			}
			if ev.Name == name {
				return ev, true
			}
		case <-deadline:
			return ltSSEEvent{}, false
		}
	}
}

// ltWaitClosed drains ch until it is closed (true) or d passes (false).
func ltWaitClosed(ch <-chan ltSSEEvent, d time.Duration) bool {
	deadline := time.After(d)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func TestLessonsStreamSnapshots(t *testing.T) {
	t.Parallel()
	e, l, st, _ := ltLiveFixture(t)
	ts := httptest.NewServer(e.srv.Handler())
	t.Cleanup(ts.Close)

	res, events := ltOpenStream(t, e, ts, fmt.Sprintf("/api/lessons/%d/stream", l.ID))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", res.StatusCode)
	}
	h := res.Header
	if !strings.HasPrefix(h.Get("Content-Type"), "text/event-stream") || h.Get("Cache-Control") != "no-cache" ||
		h.Get("X-Accel-Buffering") != "no" {
		t.Errorf("headers = %v", h)
	}
	ev, ok := ltNextEvent(t, events, "snapshot", 2*time.Second)
	if !ok {
		t.Fatal("no first snapshot")
	}
	var v LessonViewDTO
	if err := json.Unmarshal([]byte(ev.Data), &v); err != nil {
		t.Fatalf("snapshot: %v (%s)", err, ev.Data)
	}
	if v.Lesson.ID != l.ID || v.Counts.Present != 2 || len(v.Students) != 3 {
		t.Errorf("first snapshot = %+v", v.Counts)
	}

	// A change followed by a publish arrives as a fresh snapshot within a second.
	present := model.FinalPresent
	if _, err := e.st.SetManualStatus(e.ctx, l.ID, st["sidorov"].ID, &present, nil, ltAt("18:31")); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(ltAt("18:31"))
	start := time.Now()
	e.broker.Publish(realtime.LessonTopic(l.ID))
	ev, ok = ltNextEvent(t, events, "snapshot", time.Second)
	if !ok {
		t.Fatal("no snapshot within 1 s after publish")
	}
	if d := time.Since(start); d < lessonSSE.SnapshotDebounce/2 {
		t.Errorf("snapshot after %v: debounce not applied", d)
	}
	if err := json.Unmarshal([]byte(ev.Data), &v); err != nil {
		t.Fatal(err)
	}
	if v.Counts.Present != 3 || !v.AsOf.Equal(ltAt("18:31")) || v.Students[0].TotalSeconds != 31*60 {
		t.Errorf("second snapshot = %+v as_of %v total %d", v.Counts, v.AsOf, v.Students[0].TotalSeconds)
	}

	// A burst of pokes is coalesced into one snapshot.
	for range 5 {
		e.broker.Publish(realtime.LessonTopic(l.ID))
	}
	if _, ok := ltNextEvent(t, events, "snapshot", time.Second); !ok {
		t.Fatal("no snapshot after burst")
	}
	if ev, ok := ltNextEvent(t, events, "snapshot", 3*lessonSSE.SnapshotDebounce); ok {
		t.Errorf("burst produced an extra snapshot: %.60s", ev.Data)
	}

	// Deleting the lesson ends the stream.
	if err := e.st.DeleteLesson(e.ctx, l.ID); err != nil {
		t.Fatal(err)
	}
	e.broker.Publish(realtime.LessonTopic(l.ID))
	if !ltWaitClosed(events, 2*time.Second) {
		t.Fatal("stream not closed after the lesson was deleted")
	}
	// The subscription is released when the stream ends.
	waitUntil := time.Now().Add(time.Second)
	for e.broker.Subscribers(realtime.LessonTopic(l.ID)) != 0 && time.Now().Before(waitUntil) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := e.broker.Subscribers(realtime.LessonTopic(l.ID)); n != 0 {
		t.Errorf("subscribers after close = %d", n)
	}
}

func TestLessonsStreamMissingLesson(t *testing.T) {
	t.Parallel()
	e := newLTEnv(t)
	ts := httptest.NewServer(e.srv.Handler())
	t.Cleanup(ts.Close)
	res, _ := ltOpenStream(t, e, ts, "/api/lessons/9999/stream")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), `"not_found"`) {
		t.Errorf("body = %s", body)
	}
	if n := e.broker.Subscribers(realtime.LessonTopic(9999)); n != 0 {
		t.Errorf("subscription leaked: %d", n)
	}
}

func TestLessonsStreamClientDisconnect(t *testing.T) {
	t.Parallel()
	e, l, _, _ := ltLiveFixture(t)
	ts := httptest.NewServer(e.srv.Handler())
	t.Cleanup(ts.Close)
	res, events := ltOpenStream(t, e, ts, fmt.Sprintf("/api/lessons/%d/stream", l.ID))
	if _, ok := ltNextEvent(t, events, "snapshot", 2*time.Second); !ok {
		t.Fatal("no snapshot")
	}
	topic := realtime.LessonTopic(l.ID)
	if n := e.broker.Subscribers(topic); n != 1 {
		t.Fatalf("subscribers = %d", n)
	}
	_ = res.Body.Close()
	// The handler notices on its next write (a poke) or via the request context.
	waitUntil := time.Now().Add(2 * time.Second)
	for e.broker.Subscribers(topic) != 0 && time.Now().Before(waitUntil) {
		e.broker.Publish(topic)
		time.Sleep(50 * time.Millisecond)
	}
	if n := e.broker.Subscribers(topic); n != 0 {
		t.Errorf("handler still subscribed after disconnect: %d", n)
	}
}

func TestLessonsGlobalStream(t *testing.T) {
	t.Parallel()
	e := newLTEnv(t)
	ts := httptest.NewServer(e.srv.Handler())
	t.Cleanup(ts.Close)
	res, events := ltOpenStream(t, e, ts, "/api/stream")
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status = %d, headers %v", res.StatusCode, res.Header)
	}
	// Wait until the handler has subscribed (the response headers are sent after that).
	if n := e.broker.Subscribers(realtime.TopicLessons); n != 1 {
		t.Fatalf("subscribers = %d", n)
	}
	e.broker.Publish(realtime.TopicLessons)
	ev, ok := ltNextEvent(t, events, "lessons", 2*time.Second)
	if !ok {
		t.Fatal("no lessons event")
	}
	if ev.Data != "{}" {
		t.Errorf("data = %q", ev.Data)
	}

	// Creating a lesson through the API notifies dashboards.
	g := e.group("DevOps-1")
	e.ok(e.do("POST", "/api/lessons", map[string]any{
		"group_id": g.ID, "title": "Linux 1", "zoom": "857 4606 5432",
		"planned_start_at": "2026-10-07T15:00:00Z", "planned_end_at": "2026-10-07T16:30:00Z",
	}), http.StatusCreated, nil)
	if _, ok := ltNextEvent(t, events, "lessons", 2*time.Second); !ok {
		t.Fatal("no lessons event after create")
	}
}

// TestLessonsStreamTimers covers the timer-driven parts of both streams: ping
// comments, the snapshot safety net without pokes, and the end of the stream
// after logout. It shortens the package-level timings, so it deliberately does
// NOT call t.Parallel: Go starts parallel tests of a package only after every
// sequential test has finished, so no other stream sees the short timings.
func TestLessonsStreamTimers(t *testing.T) {
	saved := lessonSSE
	// Registered first, so it runs last: after ts.Close has waited for the handlers.
	t.Cleanup(func() { lessonSSE = saved })
	lessonSSE = lessonSSETimings{
		SnapshotDebounce: 10 * time.Millisecond,
		ListDebounce:     10 * time.Millisecond,
		Ping:             50 * time.Millisecond,
		Refresh:          200 * time.Millisecond,
		SessionCheck:     100 * time.Millisecond,
		Retry:            time.Second,
	}
	e, l, _, _ := ltLiveFixture(t)
	ts := httptest.NewServer(e.srv.Handler())
	t.Cleanup(ts.Close)

	_, lesson := ltOpenStream(t, e, ts, fmt.Sprintf("/api/lessons/%d/stream", l.ID))
	_, global := ltOpenStream(t, e, ts, "/api/stream")
	if _, ok := ltNextEvent(t, lesson, "snapshot", 2*time.Second); !ok {
		t.Fatal("no first snapshot")
	}
	e.clock.Set(ltAt("18:32"))

	// Keep-alive comments on both streams.
	for name, ch := range map[string]<-chan ltSSEEvent{"lesson": lesson, "global": global} {
		if ev, ok := ltNextEvent(t, ch, "comment", time.Second); !ok || ev.Data != "ping" {
			t.Errorf("%s stream: no ping (got %+v, %v)", name, ev, ok)
		}
	}

	// Nothing is published, yet the lesson stream re-sends a fresh snapshot.
	// (A refresh may already have fired before the clock moved: wait for the
	// one computed as of 18:32.)
	fresh := false
	for deadline := time.Now().Add(2 * time.Second); !fresh && time.Now().Before(deadline); {
		ev, ok := ltNextEvent(t, lesson, "snapshot", time.Until(deadline))
		if !ok {
			break
		}
		var v LessonViewDTO
		if err := json.Unmarshal([]byte(ev.Data), &v); err != nil {
			t.Fatal(err)
		}
		if v.AsOf.Equal(ltAt("18:32")) {
			fresh = true
			// Иванов's laptop is still connected: his total kept growing.
			if got := v.Students[0].TotalSeconds; got != 32*60 {
				t.Errorf("refreshed total = %d, want %d", got, 32*60)
			}
		}
	}
	if !fresh {
		t.Fatal("no periodic snapshot without pokes")
	}

	// Logout (session rows deleted) ends both streams at the next check.
	if _, err := e.st.DeleteUserSessions(e.ctx, e.user.ID, ""); err != nil {
		t.Fatal(err)
	}
	if !ltWaitClosed(lesson, 2*time.Second) {
		t.Error("lesson stream still open after logout")
	}
	if !ltWaitClosed(global, 2*time.Second) {
		t.Error("global stream still open after logout")
	}
}

func TestLessonsStreamSessionCheck(t *testing.T) {
	t.Parallel()
	e := newLTEnv(t)
	req := httptest.NewRequest("GET", "/api/stream", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: e.token})
	if e.srv.lessonSSESessionGone(req) {
		t.Fatal("valid session reported as gone")
	}
	// Logout (session row deleted) ends open streams at the next check.
	if _, err := e.st.DeleteUserSessions(e.ctx, e.user.ID, ""); err != nil {
		t.Fatal(err)
	}
	if !e.srv.lessonSSESessionGone(req) {
		t.Error("deleted session not detected")
	}
}

// HEAD on a stream answers with the headers and returns at once (the GET
// pattern also matches HEAD; a streaming loop would hold the connection).
func TestLessonsStreamHead(t *testing.T) {
	t.Parallel()
	e, l, _, _ := ltLiveFixture(t)
	cases := []struct {
		path string
		want int
	}{
		{fmt.Sprintf("/api/lessons/%d/stream", l.ID), http.StatusOK},
		{"/api/stream", http.StatusOK},
		{"/api/lessons/9999/stream", http.StatusNotFound},
	}
	for _, tc := range cases {
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- e.do("HEAD", tc.path, nil) }()
		select {
		case rec := <-done:
			if rec.Code != tc.want {
				t.Errorf("HEAD %s = %d, want %d", tc.path, rec.Code, tc.want)
			}
			if tc.want == http.StatusOK && !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
				t.Errorf("HEAD %s content type = %q", tc.path, rec.Header().Get("Content-Type"))
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("HEAD %s did not return", tc.path)
		}
	}
	if n := e.broker.Subscribers(realtime.LessonTopic(l.ID)); n != 0 {
		t.Errorf("HEAD subscribed: %d", n)
	}
}
