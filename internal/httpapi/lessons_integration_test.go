package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"zoom-checker/internal/model"
	"zoom-checker/internal/processor"
	"zoom-checker/internal/zoom"
)

// End-to-end through the real processor: the view builder must agree with what
// RebuildLesson stores (interval ↔ attendance links, totals, statuses).

type ltZoom struct {
	e    *ltEnv
	p    *processor.Processor
	uuid string
}

func newLTRealEnv(t *testing.T) (*ltEnv, *ltZoom) {
	t.Helper()
	e := newLTEnv(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := processor.New(e.st, e.broker, log, processor.WithClock(e.clock.Now))
	e.srv = New(Deps{Config: e.srv.Config, Store: e.st, Processor: p, Broker: e.broker, Log: log, Now: e.clock.Now})
	closeServerAtCleanup(t, e.srv)
	return e, &ltZoom{e: e, p: p, uuid: "sess-1=="}
}

func (z *ltZoom) body(typ string, at time.Time, participant map[string]any, extra map[string]any) []byte {
	obj := map[string]any{"id": ltMeetingID, "uuid": z.uuid, "host_id": "host-1", "topic": "Linux",
		"type": 8, "start_time": ltAt("18:00").Format(time.RFC3339), "timezone": "Europe/Moscow", "duration": 90}
	for k, v := range extra {
		obj[k] = v
	}
	if participant != nil {
		obj["participant"] = participant
	}
	b, err := json.Marshal(map[string]any{"event": typ, "event_ts": at.UnixMilli() + 250,
		"payload": map[string]any{"account_id": "acc", "object": obj}})
	if err != nil {
		panic(err)
	}
	return b
}

func (z *ltZoom) person(name, key string, timeKey string, at time.Time) map[string]any {
	p := map[string]any{"user_id": "UID-" + key, "user_name": name, "id": "", "participant_uuid": "PU-" + key,
		"email": "", "participant_user_id": "", timeKey: at.UTC().Format(time.RFC3339)}
	if timeKey == "leave_time" {
		p["leave_reason"] = "left the meeting. Reason : left the meeting"
	}
	return p
}

func (z *ltZoom) joined(name, key string, at time.Time) []byte {
	return z.body("meeting.participant_joined", at, z.person(name, key, "join_time", at), nil)
}

func (z *ltZoom) left(name, key string, at time.Time) []byte {
	return z.body("meeting.participant_left", at, z.person(name, key, "leave_time", at), nil)
}

func (z *ltZoom) started(at time.Time) []byte {
	return z.body("meeting.started", at, nil, map[string]any{"start_time": at.UTC().Format(time.RFC3339)})
}

func (z *ltZoom) ended(at time.Time) []byte {
	return z.body("meeting.ended", at, nil, map[string]any{"end_time": at.UTC().Format(time.RFC3339)})
}

// send stores each body like the webhook endpoint does and runs the worker.
func (z *ltZoom) send(bodies ...[]byte) {
	z.e.t.Helper()
	for _, b := range bodies {
		ev, perr := zoom.Parse(b)
		if perr != nil {
			z.e.t.Fatalf("parse: %v", perr)
		}
		if at := ev.OccurredAt(); z.e.clock.Now().Before(at.Add(time.Second)) {
			z.e.clock.Set(at.Add(time.Second))
		}
		raw := processor.BuildRawEvent(b, ev, nil, z.e.clock.Now())
		if _, err := z.e.st.InsertWebhookEvent(z.e.ctx, raw); err != nil {
			z.e.t.Fatal(err)
		}
		for {
			n, err := z.p.ProcessPending(z.e.ctx)
			if err != nil {
				z.e.t.Fatal(err)
			}
			if n == 0 {
				break
			}
		}
	}
}

func TestLessonsEndToEndWithProcessor(t *testing.T) {
	t.Parallel()
	e, z := newLTRealEnv(t)
	g := e.group("DevOps-1")
	ids := map[string]int64{}
	for _, n := range []string{"Иванов Иван", "Петров Пётр", "Александров Александр", "Сидоров Алексей"} {
		ids[n] = e.student(g.ID, n, "").ID
	}
	e.clock.Set(ltAt("17:30"))
	var created []LessonDTO
	e.ok(e.do("POST", "/api/lessons", map[string]any{
		"group_id": g.ID, "title": "Linux 4", "zoom": "https://us02web.zoom.us/j/85746065432?pwd=x",
		"planned_start_at": ltAt("18:00").Format(time.RFC3339), "planned_end_at": ltAt("19:30").Format(time.RFC3339),
	}), http.StatusCreated, &created)
	lid := created[0].ID
	viewPath := fmt.Sprintf("/api/lessons/%d/attendance", lid)

	// Before anyone joins: the whole roster, nobody seen.
	var v LessonViewDTO
	e.ok(e.do("GET", viewPath, nil), http.StatusOK, &v)
	if v.Counts != (CountsDTO{InGroup: 4, NotSeen: 4, Absent: 4}) {
		t.Fatalf("initial counts = %+v", v.Counts)
	}

	z.send(
		z.started(ltAt("18:00")),
		z.joined("Иванов Иван", "phone", ltAt("18:01")),
		z.joined("ИВАНОВ  ИВАН", "laptop", ltAt("18:05")),
		z.joined("Петров Пётр", "petrov", ltAt("18:02")),
		z.joined("Саша", "sasha", ltAt("18:03")),
		z.left("Петров Пётр", "petrov", ltAt("18:12")),
		z.left("Иванов Иван", "phone", ltAt("18:20")),
		z.left("Саша", "sasha", ltAt("18:33")),
	)
	e.clock.Set(ltAt("18:40"))
	e.ok(e.do("GET", viewPath, nil), http.StatusOK, &v)
	if v.Lesson.Status != model.LessonActive || len(v.Lesson.Sessions) != 1 {
		t.Fatalf("lesson = %+v", v.Lesson)
	}
	want := CountsDTO{InGroup: 4, Online: 1, Seen: 2, NotSeen: 2, Unidentified: 1, Present: 2, Absent: 2}
	if v.Counts != want {
		t.Errorf("counts = %+v, want %+v", v.Counts, want)
	}
	row := func(v LessonViewDTO, name string) StudentRowDTO {
		t.Helper()
		return ltRow(t, v, ids[name])
	}
	iv := row(v, "Иванов Иван")
	// 18:01–18:20 (phone) ∪ 18:05–now (laptop) = 39 minutes, one device online.
	if iv.CurrentStatus != model.CurrentOnline || iv.TotalSeconds != 39*60 || iv.DevicesOnline != 1 ||
		len(iv.ZoomNames) != 1 || iv.LateSeconds == nil || *iv.LateSeconds != 60 {
		t.Errorf("ivanov = %+v", iv)
	}
	e.clock.Set(ltAt("18:45"))
	e.ok(e.do("GET", viewPath, nil), http.StatusOK, &v)
	if got := row(v, "Иванов Иван").TotalSeconds; got != 44*60 {
		t.Errorf("ivanov at 18:45 = %d, want %d", got, 44*60)
	}
	if got := row(v, "Петров Пётр"); got.TotalSeconds != 10*60 || got.CurrentStatus != model.CurrentLeft {
		t.Errorf("petrov = %+v", got)
	}
	sasha := v.Participants[0]
	if sasha.DisplayName != "Саша" || sasha.Resolution != model.ResolutionUnidentified || sasha.TotalSeconds != 30*60 {
		t.Fatalf("first participant = %+v", sasha)
	}

	// Scenario 8: map "Саша" to Александров; the accumulated time moves over.
	e.ok(e.do("POST", fmt.Sprintf("/api/lessons/%d/participants/%d/map", lid, sasha.ID),
		map[string]any{"student_id": ids["Александров Александр"], "remember_alias": true}), http.StatusOK, &v)
	al := row(v, "Александров Александр")
	if al.CurrentStatus != model.CurrentLeft || al.TotalSeconds != 30*60 || al.FinalStatus != model.FinalPresent ||
		len(al.ZoomNames) != 1 || al.ZoomNames[0] != "Саша" {
		t.Errorf("alexandrov after map = %+v", al)
	}
	if v.Counts.Unidentified != 0 || v.Counts.Present != 3 {
		t.Errorf("counts after map = %+v", v.Counts)
	}
	if p := ltPart(t, v, "Саша"); p.Resolution != model.ResolutionManual || !p.Manual || p.StudentName == nil {
		t.Errorf("participant after map = %+v", p)
	}

	// Live screen: a join arrives as a snapshot within a second.
	ts := httptest.NewServer(e.srv.Handler())
	t.Cleanup(ts.Close)
	_, events := ltOpenStream(t, e, ts, fmt.Sprintf("/api/lessons/%d/stream", lid))
	if _, ok := ltNextEvent(t, events, "snapshot", 2*time.Second); !ok {
		t.Fatal("no first snapshot")
	}
	z.send(z.joined("Сидоров Алексей", "sid", ltAt("18:46")))
	ev, ok := ltNextEvent(t, events, "snapshot", time.Second)
	if !ok {
		t.Fatal("no snapshot within 1 s after the webhook was processed")
	}
	if err := json.Unmarshal([]byte(ev.Data), &v); err != nil {
		t.Fatal(err)
	}
	if got := row(v, "Сидоров Алексей"); got.CurrentStatus != model.CurrentOnline {
		t.Errorf("sidorov in snapshot = %+v", got)
	}

	// Finish at 19:00: open intervals close then, totals stop growing.
	e.clock.Set(ltAt("19:05"))
	e.ok(e.do("POST", fmt.Sprintf("/api/lessons/%d/finish", lid), map[string]any{"at": ltAt("19:00").Format(time.RFC3339)}),
		http.StatusOK, &v)
	if v.Lesson.Status != model.LessonFinished || v.Counts.Online != 0 {
		t.Fatalf("after finish: status %s counts %+v", v.Lesson.Status, v.Counts)
	}
	e.clock.Set(ltAt("19:30"))
	e.ok(e.do("GET", viewPath, nil), http.StatusOK, &v)
	if got := row(v, "Иванов Иван"); got.TotalSeconds != 59*60 || got.DevicesOnline != 0 {
		t.Errorf("ivanov after finish = %+v", got)
	}

	// Manual override and the export with every student.
	e.ok(e.do("PATCH", fmt.Sprintf("/api/lessons/%d/attendance/%d", lid, ids["Петров Пётр"]),
		map[string]any{"status": "absent", "reason": "почти не был"}), http.StatusOK, &v)
	if got := row(v, "Петров Пётр"); got.FinalStatus != model.FinalAbsent || got.ManualStatus == nil {
		t.Errorf("petrov manual = %+v", got)
	}
	rec := e.do("GET", fmt.Sprintf("/api/lessons/%d/export?format=csv", lid), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d", rec.Code)
	}
	for n := range ids {
		if !strings.Contains(rec.Body.String(), n) {
			t.Errorf("export lacks %s", n)
		}
	}

	// Reopen: the manual finish is cleared, Иванов's laptop is online again.
	e.ok(e.do("POST", fmt.Sprintf("/api/lessons/%d/reopen", lid), nil), http.StatusOK, &v)
	if v.Lesson.Status != model.LessonActive || row(v, "Иванов Иван").CurrentStatus != model.CurrentOnline {
		t.Errorf("after reopen: %s %+v", v.Lesson.Status, row(v, "Иванов Иван"))
	}

	// The meeting ends in Zoom: the lesson is finished by Zoom and cannot be
	// reopened by hand (the live screen gets an explanation, not a fake success).
	z.send(z.ended(ltAt("19:35")))
	e.ok(e.do("GET", viewPath, nil), http.StatusOK, &v)
	if v.Lesson.Status != model.LessonFinished || v.Lesson.ActualEndAt == nil || !v.Lesson.ActualEndAt.Equal(ltAt("19:35")) {
		t.Fatalf("after meeting.ended: %+v", v.Lesson)
	}
	res := e.fail(e.do("POST", fmt.Sprintf("/api/lessons/%d/reopen", lid), nil), http.StatusConflict)
	if !strings.Contains(res.Error.Message, "откроется само") {
		t.Errorf("reopen after meeting.ended = %+v", res.Error)
	}
}
