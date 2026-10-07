package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"zoom-checker/internal/model"
	"zoom-checker/internal/realtime"
	"zoom-checker/internal/store"
)

func TestLessonsCreateValidation(t *testing.T) {
	t.Parallel()
	e := newLTEnv(t)
	g := e.group("DevOps-1")
	arch := e.group("Старая группа")
	arch.Status = model.StatusArchived
	if err := e.st.UpdateGroup(e.ctx, arch); err != nil {
		t.Fatal(err)
	}
	valid := func() map[string]any {
		return map[string]any{
			"group_id": g.ID, "title": "Linux 4", "zoom": "857 4606 5432",
			"planned_start_at": "2026-10-06T15:00:00Z", "planned_end_at": "2026-10-06T16:30:00Z",
		}
	}
	cases := []struct {
		name   string
		mutate func(m map[string]any)
		field  string
		msg    string
	}{
		{"no group", func(m map[string]any) { m["group_id"] = 0 }, "group_id", "Выберите группу"},
		{"unknown group", func(m map[string]any) { m["group_id"] = 9999 }, "group_id", "Группа не найдена"},
		{"archived group", func(m map[string]any) { m["group_id"] = arch.ID }, "group_id", "в архиве"},
		{"empty title", func(m map[string]any) { m["title"] = "   " }, "title", "название"},
		{"long title", func(m map[string]any) { m["title"] = strings.Repeat("я", 201) }, "title", "200"},
		{"personal link", func(m map[string]any) { m["zoom"] = "https://zoom.us/my/ivanov" }, "zoom", "zoom.us/my"},
		{"no zoom", func(m map[string]any) { m["zoom"] = "" }, "zoom", "Укажите ссылку"},
		{"bad time", func(m map[string]any) { m["planned_start_at"] = "вчера" }, "planned_start_at", "Некорректные"},
		{"end before start", func(m map[string]any) { m["planned_end_at"] = "2026-10-06T14:00:00Z" }, "planned_end_at", "позже начала"},
		{"too long", func(m map[string]any) { m["planned_end_at"] = "2026-10-07T03:01:00Z" }, "planned_end_at", "12 часов"},
		{"repeat 53", func(m map[string]any) { m["repeat_weekly"] = 53 }, "repeat_weekly", "от 1 до 52"},
		{"repeat -1", func(m map[string]any) { m["repeat_weekly"] = -1 }, "repeat_weekly", "от 1 до 52"},
	}
	for _, tc := range cases {
		body := valid()
		tc.mutate(body)
		res := e.fail(e.do("POST", "/api/lessons", body), http.StatusUnprocessableEntity)
		if res.Error.Code != CodeValidation || !strings.Contains(res.Error.Message, tc.msg) || res.Error.Details["field"] != tc.field {
			t.Errorf("%s: got %+v", tc.name, res.Error)
		}
	}
	lessons, err := e.st.ListLessons(e.ctx, store.LessonFilter{})
	if err != nil || len(lessons) != 0 {
		t.Fatalf("invalid requests created lessons: %v %v", lessons, err)
	}
	// Malformed JSON is a 400.
	e.fail(e.do("POST", "/api/lessons", "{"), http.StatusBadRequest)
}

func TestLessonsCreateWeeklySeries(t *testing.T) {
	t.Parallel()
	e := newLTEnv(t)
	g := e.group("DevOps-1")
	pub, cancel := e.broker.Subscribe(realtime.TopicLessons)
	defer cancel()
	var out []LessonDTO
	e.ok(e.do("POST", "/api/lessons", map[string]any{
		"group_id": g.ID, "title": "  Linux 4 ",
		"zoom":             "Приглашение: https://us02web.zoom.us/j/85746065432?pwd=abc\nИдентификатор конференции: 857 4606 5432",
		"planned_start_at": "2026-10-06T18:00:00+03:00", "planned_end_at": "2026-10-06T19:30:00+03:00",
		"repeat_weekly": 3,
	}), http.StatusCreated, &out)
	if len(out) != 3 {
		t.Fatalf("created %d lessons", len(out))
	}
	for i, l := range out {
		wantTitle := fmt.Sprintf("Linux %d", 4+i)
		wantStart := time.Date(2026, 10, 6+7*i, 15, 0, 0, 0, time.UTC)
		if l.Title != wantTitle || !l.PlannedStartAt.Equal(wantStart) || !l.PlannedEndAt.Equal(wantStart.Add(90*time.Minute)) {
			t.Errorf("lesson %d = %q %v–%v", i, l.Title, l.PlannedStartAt, l.PlannedEndAt)
		}
		if l.ZoomMeetingID != ltMeetingID || l.ZoomMeetingIDFormatted != "857 4606 5432" ||
			l.ZoomJoinURL != "https://us02web.zoom.us/j/85746065432?pwd=abc" || l.Group.Name != "DevOps-1" ||
			l.Status != model.LessonPlanned || l.Sessions == nil {
			t.Errorf("lesson %d = %+v", i, l)
		}
		if acts := e.auditActions(l.ID); len(acts) != 1 || acts[0] != model.AuditLessonCreate {
			t.Errorf("lesson %d audit = %v", i, acts)
		}
	}
	if got := e.proc.Relinks(); len(got) != 3 || got[0] != out[0].ID || got[2] != out[2].ID {
		t.Errorf("relinks = %v", got)
	}
	if !ltWaitPoke(pub, time.Second) {
		t.Error("lessons topic not published")
	}
	a := e.audit(out[0].ID)[0]
	if a.UserName != "Мария Ивановна" || a.EntityType != "lesson" || !strings.Contains(a.NewValue, "Linux 4") ||
		a.GroupID == nil || *a.GroupID != g.ID {
		t.Errorf("audit = %+v", a)
	}
	// Default repeat = 1, created_by recorded.
	e.ok(e.do("POST", "/api/lessons", map[string]any{
		"group_id": g.ID, "title": "Разовое", "zoom": "857 4606 5432",
		"planned_start_at": "2026-11-01T10:00:00Z", "planned_end_at": "2026-11-01T11:00:00Z",
	}), http.StatusCreated, &out)
	if len(out) != 1 || out[0].ZoomJoinURL != "" {
		t.Errorf("single lesson = %+v", out)
	}
	l, err := e.st.GetLesson(e.ctx, out[0].ID)
	if err != nil || l.CreatedBy == nil || *l.CreatedBy != e.user.ID {
		t.Errorf("created_by = %v (%v)", l, err)
	}
}

func TestLessonsListFilters(t *testing.T) {
	t.Parallel()
	e := newLTEnv(t)
	g1 := e.group("DevOps-1")
	g2 := e.group("Python-2")
	e.student(g1.ID, "Иванов Иван", "")
	e.student(g1.ID, "Петров Пётр", "")
	l1 := e.lesson(g1.ID, "Понедельник", ltAt("18:00").AddDate(0, 0, -1))
	l2 := e.lesson(g1.ID, "Вторник", ltAt("18:00"))
	l3 := e.lesson(g2.ID, "Среда", ltAt("18:00").AddDate(0, 0, 1))
	if err := e.st.SetLessonComputed(e.ctx, l3.ID, nil, nil, model.LessonFinished); err != nil {
		t.Fatal(err)
	}
	ids := func(q string) []int64 {
		t.Helper()
		var out []LessonSummaryDTO
		e.ok(e.do("GET", "/api/lessons"+q, nil), http.StatusOK, &out)
		var got []int64
		for _, l := range out {
			got = append(got, l.ID)
		}
		return got
	}
	eq := func(name string, got []int64, want ...int64) {
		t.Helper()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}
	eq("all", ids(""), l1.ID, l2.ID, l3.ID)
	eq("day", ids("?from=2026-10-06&to=2026-10-06"), l2.ID)
	eq("from day", ids("?from=2026-10-06"), l2.ID, l3.ID)
	eq("rfc3339 to is exclusive", ids("?to=2026-10-06T15:00:00Z"), l1.ID)
	eq("group", ids(fmt.Sprintf("?group_id=%d", g2.ID)), l3.ID)
	eq("status", ids("?status=finished"), l3.ID)
	e.fail(e.do("GET", "/api/lessons?status=bad", nil), http.StatusUnprocessableEntity)
	e.fail(e.do("GET", "/api/lessons?from=06.10.2026", nil), http.StatusUnprocessableEntity)
	e.fail(e.do("GET", "/api/lessons?group_id=x", nil), http.StatusBadRequest)

	var out []LessonSummaryDTO
	e.ok(e.do("GET", "/api/lessons?from=2026-10-06&to=2026-10-06", nil), http.StatusOK, &out)
	c := out[0].Counts
	if out[0].Group.Name != "DevOps-1" || c.InGroup != 2 || c.NotSeen != 2 || c.Absent != 2 {
		t.Errorf("summary = %+v", out[0])
	}
}

func TestLessonsGetUpdateDelete(t *testing.T) {
	t.Parallel()
	e := newLTEnv(t)
	g := e.group("DevOps-1")
	l := e.lesson(g.ID, "Linux 4", ltAt("18:00"))
	path := fmt.Sprintf("/api/lessons/%d", l.ID)

	sess, err := e.st.UpsertZoomMeeting(e.ctx, &model.ZoomMeeting{MeetingUUID: "u1", MeetingID: ltMeetingID,
		LessonID: &l.ID, FirstEventAt: ltAt("17:58")})
	if err != nil {
		t.Fatal(err)
	}
	var got LessonDTO
	e.ok(e.do("GET", path, nil), http.StatusOK, &got)
	if got.ID != l.ID || len(got.Sessions) != 1 || got.Sessions[0].ID != sess.ID || got.Group.Name != "DevOps-1" {
		t.Errorf("get = %+v", got)
	}
	e.fail(e.do("GET", "/api/lessons/9999", nil), http.StatusNotFound)
	e.fail(e.do("GET", "/api/lessons/abc", nil), http.StatusNotFound)

	// Title only: audited, no relink.
	e.ok(e.do("PATCH", path, map[string]any{"title": "Linux 5"}), http.StatusOK, &got)
	if got.Title != "Linux 5" || len(e.proc.Relinks()) != 0 {
		t.Errorf("patch title = %+v relinks %v", got, e.proc.Relinks())
	}
	a := e.audit(l.ID)[0]
	if a.Action != model.AuditLessonUpdate || a.PreviousValue != "Название: «Linux 4»" || a.NewValue != "Название: «Linux 5»" {
		t.Errorf("update audit = %+v", a)
	}
	// Same values: no audit.
	e.ok(e.do("PATCH", path, map[string]any{"title": "Linux 5"}), http.StatusOK, &got)
	if n := len(e.audit(l.ID)); n != 1 {
		t.Errorf("no-op patch wrote audit (%d entries)", n)
	}
	// Meeting id and time: relinked.
	e.ok(e.do("PATCH", path, map[string]any{
		"zoom": "https://zoom.us/j/123456789", "planned_start_at": "2026-10-06T15:10:00Z",
	}), http.StatusOK, &got)
	if got.ZoomMeetingID != "123456789" || got.ZoomJoinURL != "https://zoom.us/j/123456789" ||
		!got.PlannedStartAt.Equal(ltAt("18:10")) || len(e.proc.Relinks()) != 1 {
		t.Errorf("patch zoom = %+v relinks %v", got, e.proc.Relinks())
	}
	a = e.audit(l.ID)[0]
	if !strings.Contains(a.PreviousValue, "Zoom: 857 4606 5432") || !strings.Contains(a.NewValue, "Zoom: 123 456 789") ||
		!strings.Contains(a.PreviousValue, "Ссылка: нет") || !strings.Contains(a.NewValue, "Ссылка: https://zoom.us/j/123456789") ||
		!strings.Contains(a.NewValue, "Начало: 06.10.2026 18:10 (UTC+3)") {
		t.Errorf("patch audit = %+v", a)
	}
	// Pasting only the number of the same meeting keeps the link.
	e.ok(e.do("PATCH", path, map[string]any{"zoom": "123 456 789"}), http.StatusOK, &got)
	if got.ZoomJoinURL != "https://zoom.us/j/123456789" {
		t.Errorf("join url lost: %q", got.ZoomJoinURL)
	}
	// Validation on merged values.
	res := e.fail(e.do("PATCH", path, map[string]any{"planned_end_at": "2026-10-06T15:00:00Z"}), http.StatusUnprocessableEntity)
	if res.Error.Details["field"] != "planned_end_at" {
		t.Errorf("patch validation = %+v", res.Error)
	}
	e.fail(e.do("PATCH", path, map[string]any{"title": ""}), http.StatusUnprocessableEntity)
	e.fail(e.do("PATCH", "/api/lessons/9999", map[string]any{"title": "x"}), http.StatusNotFound)

	// Delete: 204, audit kept, live screens poked.
	poke, cancel := e.broker.Subscribe(realtime.LessonTopic(l.ID))
	defer cancel()
	rec := e.do("DELETE", path, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	if !ltWaitPoke(poke, time.Second) {
		t.Error("delete did not publish")
	}
	e.fail(e.do("GET", path, nil), http.StatusNotFound)
	e.fail(e.do("DELETE", path, nil), http.StatusNotFound)
	if acts := e.auditActions(l.ID); acts[0] != model.AuditLessonDelete {
		t.Errorf("audit after delete = %v", acts)
	}
	if s, err := e.st.GetZoomMeeting(e.ctx, sess.ID); err != nil || s.LessonID != nil {
		t.Errorf("session after delete = %+v %v", s, err)
	}
}

func TestLessonsFinishReopenRecompute(t *testing.T) {
	t.Parallel()
	e := newLTEnv(t)
	g := e.group("DevOps-1")
	e.student(g.ID, "Иванов Иван", "")
	l := e.lesson(g.ID, "Linux 4", ltAt("18:00"))
	path := fmt.Sprintf("/api/lessons/%d", l.ID)
	e.clock.Set(ltAt("19:00"))

	// Future (beyond the 1 minute slack) and before the start are rejected.
	res := e.fail(e.do("POST", path+"/finish", map[string]any{"at": ltAt("19:02").Format(time.RFC3339)}), http.StatusUnprocessableEntity)
	if !strings.Contains(res.Error.Message, "будущем") {
		t.Errorf("future finish: %+v", res.Error)
	}
	res = e.fail(e.do("POST", path+"/finish", map[string]any{"at": ltAt("17:59").Format(time.RFC3339)}), http.StatusUnprocessableEntity)
	if !strings.Contains(res.Error.Message, "раньше начала") {
		t.Errorf("early finish: %+v", res.Error)
	}
	if len(e.proc.Rebuilds()) != 0 {
		t.Fatal("rejected finish rebuilt")
	}
	// Actual start (Zoom began early) is the lower bound when known.
	if err := e.st.SetLessonComputed(e.ctx, l.ID, ltPtr(ltAt("17:50")), nil, model.LessonActive); err != nil {
		t.Fatal(err)
	}
	var v LessonViewDTO
	e.ok(e.do("POST", path+"/finish", map[string]any{"at": ltAt("17:55").Format(time.RFC3339)}), http.StatusOK, &v)
	if v.Lesson.ManualFinishedAt == nil || !v.Lesson.ManualFinishedAt.Equal(ltAt("17:55")) {
		t.Errorf("manual_finished_at = %v", v.Lesson.ManualFinishedAt)
	}
	// Default "now"; an empty body is fine too.
	rec := e.do("POST", path+"/finish", nil)
	e.ok(rec, http.StatusOK, &v)
	if !v.Lesson.ManualFinishedAt.Equal(ltAt("19:00")) || !v.AsOf.Equal(ltAt("19:00")) {
		t.Errorf("finish now = %v as_of %v", v.Lesson.ManualFinishedAt, v.AsOf)
	}
	if got := e.proc.Rebuilds(); len(got) != 2 || got[0] != l.ID {
		t.Errorf("rebuilds = %v", got)
	}
	a := e.audit(l.ID)[0]
	if a.Action != model.AuditLessonFinish || a.NewValue != "Завершено вручную: 06.10.2026 19:00 (UTC+3)" ||
		a.PreviousValue != "Завершено вручную: 06.10.2026 17:55 (UTC+3)" {
		t.Errorf("finish audit = %+v", a)
	}

	e.ok(e.do("POST", path+"/reopen", map[string]any{}), http.StatusOK, &v)
	if v.Lesson.ManualFinishedAt != nil {
		t.Errorf("reopen kept manual_finished_at")
	}
	if a := e.audit(l.ID)[0]; a.Action != model.AuditLessonReopen {
		t.Errorf("reopen audit = %+v", a)
	}
	// Reopen of a lesson that is not manually finished changes nothing but rebuilds.
	e.ok(e.do("POST", path+"/reopen", map[string]any{}), http.StatusOK, &v)
	if n := len(e.audit(l.ID)); n != 3 {
		t.Errorf("audit entries = %d, want 3", n)
	}

	e.ok(e.do("POST", path+"/recompute", nil), http.StatusOK, &v)
	if got := e.proc.Rebuilds(); len(got) != 5 {
		t.Errorf("rebuilds = %v", got)
	}
	if v.Counts.InGroup != 1 || len(v.Students) != 1 {
		t.Errorf("view = %+v", v.Counts)
	}
	e.proc.mu.Lock()
	e.proc.rebuildErr = errors.New("boom")
	e.proc.mu.Unlock()
	e.fail(e.do("POST", path+"/recompute", nil), http.StatusInternalServerError)
	// A failed rebuild after finish: the finish time is saved and audited, and
	// open live screens are still poked so they show it.
	poke, cancel := e.broker.Subscribe(realtime.LessonTopic(l.ID))
	defer cancel()
	e.fail(e.do("POST", path+"/finish", nil), http.StatusInternalServerError)
	if got, err := e.st.GetLesson(e.ctx, l.ID); err != nil || got.ManualFinishedAt == nil {
		t.Errorf("finish not saved after failed rebuild: %+v %v", got, err)
	}
	if !ltWaitPoke(poke, time.Second) {
		t.Error("failed rebuild after finish did not publish")
	}
	if a := e.audit(l.ID)[0]; a.Action != model.AuditLessonFinish {
		t.Errorf("finish audit after failed rebuild = %+v", a)
	}
	e.fail(e.do("POST", path+"/reopen", nil), http.StatusInternalServerError)
	if !ltWaitPoke(poke, time.Second) {
		t.Error("failed rebuild after reopen did not publish")
	}
	e.proc.mu.Lock()
	e.proc.rebuildErr = nil
	e.proc.mu.Unlock()
	e.fail(e.do("POST", "/api/lessons/9999/recompute", nil), http.StatusNotFound)
	e.fail(e.do("POST", "/api/lessons/9999/finish", nil), http.StatusNotFound)

	// A lesson that has not started yet cannot be finished "now".
	future := e.lesson(g.ID, "Завтра", ltAt("18:00").AddDate(0, 0, 1))
	res = e.fail(e.do("POST", fmt.Sprintf("/api/lessons/%d/finish", future.ID), nil), http.StatusUnprocessableEntity)
	if !strings.Contains(res.Error.Message, "ещё не началось") {
		t.Errorf("not started: %+v", res.Error)
	}
}

// A lesson that ended with its Zoom meeting cannot be reopened by hand: a 409
// that explains why instead of a silent "reopened" that changes nothing. With a
// manual finish on top, reopen clears it (the Zoom end time applies again).
func TestLessonsReopenAfterZoomEnded(t *testing.T) {
	t.Parallel()
	e := newLTEnv(t)
	g := e.group("DevOps-1")
	l := e.lesson(g.ID, "Linux 4", ltAt("18:00"))
	path := fmt.Sprintf("/api/lessons/%d/reopen", l.ID)
	e.clock.Set(ltAt("20:00"))
	for i, end := range []time.Time{ltAt("19:10"), ltAt("19:31")} {
		if _, err := e.st.UpsertZoomMeeting(e.ctx, &model.ZoomMeeting{MeetingUUID: fmt.Sprintf("s%d", i), MeetingID: ltMeetingID,
			LessonID: &l.ID, StartedAt: ltPtr(ltAt("18:00")), EndedAt: &end, FirstEventAt: ltAt("18:00")}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.st.SetLessonComputed(e.ctx, l.ID, ltPtr(ltAt("18:00")), ltPtr(ltAt("19:31")), model.LessonFinished); err != nil {
		t.Fatal(err)
	}

	res := e.fail(e.do("POST", path, nil), http.StatusConflict)
	if res.Error.Code != CodeConflict || !strings.Contains(res.Error.Message, "конференцией в Zoom (06.10.2026 19:31 (UTC+3))") ||
		res.Error.Details["zoom_ended_at"] != "2026-10-06T16:31:00Z" {
		t.Errorf("reopen of a Zoom-ended lesson = %+v", res.Error)
	}
	if len(e.audit(l.ID)) != 0 || len(e.proc.Rebuilds()) != 0 {
		t.Error("rejected reopen audited or rebuilt")
	}

	// Finished by the teacher too: reopen undoes the manual time.
	if err := e.st.SetLessonManualFinished(e.ctx, l.ID, ltPtr(ltAt("19:00"))); err != nil {
		t.Fatal(err)
	}
	var v LessonViewDTO
	e.ok(e.do("POST", path, nil), http.StatusOK, &v)
	if v.Lesson.ManualFinishedAt != nil {
		t.Error("manual finish kept")
	}
	a := e.audit(l.ID)[0]
	if a.Action != model.AuditLessonReopen || a.PreviousValue != "Завершено вручную: 06.10.2026 19:00 (UTC+3)" ||
		a.NewValue != "Ручное завершение отменено; занятие остаётся завершённым: конференция в Zoom закончилась 06.10.2026 19:31 (UTC+3)" {
		t.Errorf("reopen audit = %+v", a)
	}

	// A session still running: the lesson is not finished by Zoom.
	if _, err := e.st.UpsertZoomMeeting(e.ctx, &model.ZoomMeeting{MeetingUUID: "s-live", MeetingID: ltMeetingID,
		LessonID: &l.ID, StartedAt: ltPtr(ltAt("19:45")), FirstEventAt: ltAt("19:45")}); err != nil {
		t.Fatal(err)
	}
	e.ok(e.do("POST", path, nil), http.StatusOK, nil)
	if n := len(e.audit(l.ID)); n != 1 {
		t.Errorf("no-op reopen audited (%d entries)", n)
	}
}

func TestLessonZoomEnded(t *testing.T) {
	t.Parallel()
	t1, t2 := ltAt("19:00"), ltAt("19:30")
	cases := []struct {
		name string
		in   []model.ZoomMeeting
		want *time.Time
	}{
		{"no sessions", nil, nil},
		{"one running", []model.ZoomMeeting{{EndedAt: &t1}, {}}, nil},
		{"latest end", []model.ZoomMeeting{{EndedAt: &t2}, {EndedAt: &t1}}, &t2},
	}
	for _, tc := range cases {
		got := lessonZoomEnded(tc.in)
		if (got == nil) != (tc.want == nil) || (got != nil && !got.Equal(*tc.want)) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// ltLiveFixture: lesson 18:00 with Иванов online (two devices), Петров left,
// Сидоров never connected, participant "Ваня" unidentified. Now = 18:30.
func ltLiveFixture(t *testing.T) (e *ltEnv, l *model.Lesson, st map[string]*model.Student, parts map[string]int64) {
	e = newLTEnv(t)
	g := e.group("DevOps-1")
	st = map[string]*model.Student{
		"ivanov":  e.student(g.ID, "Иванов Иван", "1001"),
		"petrov":  e.student(g.ID, "Петров Пётр", "1002"),
		"sidorov": e.student(g.ID, "Сидоров Алексей", "1003"),
	}
	l = e.lesson(g.ID, "Linux 4", ltAt("18:00"))
	e.clock.Set(ltAt("18:30"))
	iv, pt, sd := st["ivanov"].ID, st["petrov"].ID, st["sidorov"].ID
	pIDs, _ := e.project(l.ID, ltProjection{
		Participants: []model.Participant{
			{NormalizedName: "иванов иван", DisplayName: "Иванов Иван", Resolution: model.ResolutionMatched, StudentID: &iv,
				CurrentStatus: model.CurrentOnline, FirstJoinAt: ltPtr(ltAt("18:00")), Devices: 2},
			{NormalizedName: "петров петр", DisplayName: "Петров Пётр", Resolution: model.ResolutionMatched, StudentID: &pt,
				CurrentStatus: model.CurrentLeft, FirstJoinAt: ltPtr(ltAt("18:05")), LastLeaveAt: ltPtr(ltAt("18:15")), Devices: 1},
			{NormalizedName: "ваня", DisplayName: "Ваня", Resolution: model.ResolutionUnidentified,
				CurrentStatus: model.CurrentOnline, FirstJoinAt: ltPtr(ltAt("18:10")), Devices: 1},
		},
		Rows: []model.Attendance{
			{StudentID: iv, CurrentStatus: model.CurrentOnline, AutoStatus: model.FinalPresent, FirstJoinAt: ltPtr(ltAt("18:00")),
				TotalDurationSeconds: 60, LateSeconds: ltPtr(int64(0))},
			{StudentID: pt, CurrentStatus: model.CurrentLeft, AutoStatus: model.FinalPresent, FirstJoinAt: ltPtr(ltAt("18:05")),
				LastLeaveAt: ltPtr(ltAt("18:15")), TotalDurationSeconds: 600, LateSeconds: ltPtr(int64(300))},
			{StudentID: sd, CurrentStatus: model.CurrentNotSeen, AutoStatus: model.FinalAbsent},
		},
		Intervals: []ltIv{
			{Name: "Иванов Иван", UUID: "phone", Join: ltAt("18:00"), Leave: ltPtr(ltAt("18:20")), Student: iv},
			{Name: "Иванов Иван", UUID: "laptop", Join: ltAt("18:10"), Student: iv},
			{Name: "Петров Пётр", UUID: "p1", Join: ltAt("18:05"), Leave: ltPtr(ltAt("18:15")), Student: pt, Reason: "left the meeting"},
			{Name: "Ваня", UUID: "v1", Join: ltAt("18:10")},
		},
	})
	return e, l, st, pIDs
}

func TestLessonsAttendanceView(t *testing.T) {
	t.Parallel()
	e, l, st, _ := ltLiveFixture(t)
	path := fmt.Sprintf("/api/lessons/%d/attendance", l.ID)
	var v LessonViewDTO
	e.ok(e.do("GET", path, nil), http.StatusOK, &v)
	want := CountsDTO{InGroup: 3, Online: 1, Seen: 2, NotSeen: 1, Unidentified: 1, Present: 2, Absent: 1}
	if v.Counts != want {
		t.Errorf("counts = %+v, want %+v", v.Counts, want)
	}
	if len(v.Students) != 3 || v.Students[0].FullName != "Иванов Иван" || v.Students[2].FullName != "Сидоров Алексей" {
		t.Fatalf("students = %+v", v.Students)
	}
	iv := v.Students[0]
	if iv.TotalSeconds != 30*60 || iv.DevicesOnline != 1 || iv.ExternalID != "1001" || len(iv.ZoomNames) != 1 {
		t.Errorf("ivanov = %+v", iv)
	}
	// The open interval ticks: 5 minutes later the total grows by 5 minutes.
	e.clock.Set(ltAt("18:35"))
	e.ok(e.do("GET", path, nil), http.StatusOK, &v)
	if v.Students[0].TotalSeconds != 35*60 || v.Students[1].TotalSeconds != 600 {
		t.Errorf("totals at 18:35 = %d, %d", v.Students[0].TotalSeconds, v.Students[1].TotalSeconds)
	}
	if v.Participants[0].DisplayName != "Ваня" || v.Participants[0].TotalSeconds != 25*60 {
		t.Errorf("participants = %+v", v.Participants)
	}
	_ = st

	var un []ParticipantDTO
	e.ok(e.do("GET", fmt.Sprintf("/api/lessons/%d/unidentified", l.ID), nil), http.StatusOK, &un)
	if len(un) != 1 || un[0].DisplayName != "Ваня" {
		t.Errorf("unidentified = %+v", un)
	}
	var all []ParticipantDTO
	e.ok(e.do("GET", fmt.Sprintf("/api/lessons/%d/participants", l.ID), nil), http.StatusOK, &all)
	if len(all) != 3 {
		t.Errorf("participants = %+v", all)
	}
	e.fail(e.do("GET", "/api/lessons/9999/attendance", nil), http.StatusNotFound)
	e.fail(e.do("GET", "/api/lessons/9999/unidentified", nil), http.StatusNotFound)
}

func TestLessonsMapParticipant(t *testing.T) {
	t.Parallel()
	e, l, st, parts := ltLiveFixture(t)
	vanya := parts["ваня"]
	mapPath := fmt.Sprintf("/api/lessons/%d/participants/%d/map", l.ID, vanya)
	poke, cancel := e.broker.Subscribe(realtime.LessonTopic(l.ID))
	defer cancel()

	// Map to Иванов and remember the name.
	var v LessonViewDTO
	e.ok(e.do("POST", mapPath, map[string]any{"student_id": st["ivanov"].ID, "remember_alias": true, "reason": "это он"}),
		http.StatusOK, &v)
	m, err := e.st.GetMapping(e.ctx, l.ID, "ваня")
	if err != nil || m.Action != model.MappingStudent || m.StudentID == nil || *m.StudentID != st["ivanov"].ID ||
		m.CreatedBy == nil || *m.CreatedBy != e.user.ID {
		t.Fatalf("mapping = %+v %v", m, err)
	}
	stu, _ := e.st.GetStudent(e.ctx, st["ivanov"].ID)
	if len(stu.Aliases) != 1 || stu.Aliases[0].Alias != "Ваня" {
		t.Errorf("aliases = %+v", stu.Aliases)
	}
	if got := e.proc.Rebuilds(); len(got) != 1 || got[0] != l.ID {
		t.Errorf("rebuilds = %v", got)
	}
	if !ltWaitPoke(poke, time.Second) {
		t.Error("map did not publish")
	}
	audit := e.audit(l.ID)
	if len(audit) != 2 || audit[0].Action != model.AuditParticipantMap || audit[1].Action != model.AuditAliasAdd {
		t.Fatalf("audit = %+v", audit)
	}
	a := audit[0]
	ref := fmt.Sprintf("Иванов Иван (id %d)", st["ivanov"].ID)
	if a.EntityType != "participant" || a.EntityID != "ваня" || a.Reason != "это он" ||
		a.PreviousValue != "«Ваня»: автоматически: не идентифицирован" ||
		a.NewValue != "«Ваня»: студент "+ref+"; имя запомнено для следующих занятий" {
		t.Errorf("map audit = %+v", a)
	}
	if v.Lesson.ID != l.ID || v.Counts.InGroup != 3 {
		t.Errorf("view = %+v", v.Counts)
	}

	// Remembering the same name for another student conflicts: nothing is saved.
	res := e.fail(e.do("POST", mapPath, map[string]any{"student_id": st["petrov"].ID, "remember_alias": true}), http.StatusConflict)
	if !strings.Contains(res.Error.Message, "Иванов Иван") || !strings.Contains(res.Error.Message, "не сохранено") {
		t.Errorf("conflict message = %q", res.Error.Message)
	}
	if m, _ := e.st.GetMapping(e.ctx, l.ID, "ваня"); *m.StudentID != st["ivanov"].ID {
		t.Error("mapping changed despite the conflict")
	}
	if n := len(e.audit(l.ID)); n != 2 {
		t.Errorf("conflict wrote audit (%d)", n)
	}
	// Remembering again for the same student is fine (already remembered).
	e.ok(e.do("POST", mapPath, map[string]any{"student_id": st["ivanov"].ID, "remember_alias": true}), http.StatusOK, nil)

	// Validation.
	e.fail(e.do("POST", mapPath, map[string]any{}), http.StatusUnprocessableEntity)
	e.fail(e.do("POST", mapPath, map[string]any{"action": "student"}), http.StatusUnprocessableEntity)
	e.fail(e.do("POST", mapPath, map[string]any{"action": "drop"}), http.StatusUnprocessableEntity)
	e.fail(e.do("POST", mapPath, map[string]any{"student_id": 9999}), http.StatusUnprocessableEntity)
	other := e.group("Другая")
	stranger := e.student(other.ID, "Чужой Студент", "")
	res = e.fail(e.do("POST", mapPath, map[string]any{"student_id": stranger.ID}), http.StatusUnprocessableEntity)
	if !strings.Contains(res.Error.Message, "не из группы") {
		t.Errorf("stranger: %+v", res.Error)
	}
	otherLesson := e.lesson(other.ID, "Другое", ltAt("18:00"))
	e.fail(e.do("POST", fmt.Sprintf("/api/lessons/%d/participants/%d/map", otherLesson.ID, vanya),
		map[string]any{"action": "ignore"}), http.StatusNotFound)
	e.fail(e.do("POST", fmt.Sprintf("/api/lessons/%d/participants/9999/map", l.ID), map[string]any{"action": "ignore"}),
		http.StatusNotFound)

	// Ignore always: added to settings once (deduplicated by normalized name).
	e.ok(e.do("POST", mapPath, map[string]any{"action": "ignore", "ignore_always": true}), http.StatusOK, nil)
	e.ok(e.do("POST", mapPath, map[string]any{"action": "ignore", "ignore_always": true}), http.StatusOK, nil)
	set, err := e.st.GetSettings(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, name := range set.IgnoredNames {
		if name == "Ваня" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("ignored names = %v", set.IgnoredNames)
	}
	if m, _ := e.st.GetMapping(e.ctx, l.ID, "ваня"); m.Action != model.MappingIgnore || m.StudentID != nil {
		t.Errorf("ignore mapping = %+v", m)
	}
	// The repeated identical decision (and the repeated "remember" above) wrote
	// no audit entries: map(ignore) + settings.update, map(student) + alias.add.
	acts := e.auditActions(l.ID)
	if fmt.Sprint(acts) != fmt.Sprint([]string{model.AuditParticipantMap, model.AuditSettingsUpdate,
		model.AuditParticipantMap, model.AuditAliasAdd}) {
		t.Errorf("audit actions = %v", acts)
	}
	if a := e.audit(l.ID)[1]; a.EntityType != "settings" || a.EntityID != settingsEntityID {
		t.Errorf("ignore list audit = %+v", a)
	}

	// Unmap removes the decision; a second unmap is a no-op.
	rebuilds := len(e.proc.Rebuilds())
	e.ok(e.do("DELETE", mapPath, nil), http.StatusOK, &v)
	if _, err := e.st.GetMapping(e.ctx, l.ID, "ваня"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("mapping after unmap: %v", err)
	}
	if a := e.audit(l.ID)[0]; a.Action != model.AuditParticipantUnmap || a.PreviousValue != "«Ваня»: не студент (скрыт)" {
		t.Errorf("unmap audit = %+v", a)
	}
	if len(e.proc.Rebuilds()) != rebuilds+1 {
		t.Error("unmap did not rebuild")
	}
	count := len(e.audit(l.ID))
	e.ok(e.do("DELETE", mapPath, nil), http.StatusOK, nil)
	if len(e.audit(l.ID)) != count {
		t.Error("second unmap wrote audit")
	}
}

// A failed rebuild after a saved decision is reported (a stale view would
// claim the time moved), the decision and its audit entry stay, live screens
// are poked, and repeating the request retries the rebuild without a second
// audit entry.
func TestLessonsMapRebuildFailure(t *testing.T) {
	t.Parallel()
	e, l, st, parts := ltLiveFixture(t)
	mapPath := fmt.Sprintf("/api/lessons/%d/participants/%d/map", l.ID, parts["ваня"])
	poke, cancel := e.broker.Subscribe(realtime.LessonTopic(l.ID))
	defer cancel()
	e.proc.mu.Lock()
	e.proc.rebuildErr = errors.New("database is locked")
	e.proc.mu.Unlock()

	res := e.fail(e.do("POST", mapPath, map[string]any{"student_id": st["ivanov"].ID}), http.StatusInternalServerError)
	if res.Error.Code != CodeInternal || strings.Contains(res.Error.Message, "locked") {
		t.Errorf("error = %+v", res.Error)
	}
	if m, err := e.st.GetMapping(e.ctx, l.ID, "ваня"); err != nil || m.StudentID == nil || *m.StudentID != st["ivanov"].ID {
		t.Errorf("mapping not saved: %+v %v", m, err)
	}
	if acts := e.auditActions(l.ID); len(acts) != 1 || acts[0] != model.AuditParticipantMap {
		t.Errorf("audit = %v", acts)
	}
	if !ltWaitPoke(poke, time.Second) {
		t.Error("failed rebuild did not publish")
	}

	e.proc.mu.Lock()
	e.proc.rebuildErr = nil
	e.proc.mu.Unlock()
	before := len(e.proc.Rebuilds())
	e.ok(e.do("POST", mapPath, map[string]any{"student_id": st["ivanov"].ID}), http.StatusOK, nil)
	if len(e.proc.Rebuilds()) != before+1 {
		t.Error("repeating the decision did not rebuild")
	}
	if n := len(e.audit(l.ID)); n != 1 {
		t.Errorf("repeated decision audited again (%d entries)", n)
	}

	// Unmap: same contract.
	e.proc.mu.Lock()
	e.proc.rebuildErr = errors.New("boom")
	e.proc.mu.Unlock()
	e.fail(e.do("DELETE", mapPath, nil), http.StatusInternalServerError)
	if _, err := e.st.GetMapping(e.ctx, l.ID, "ваня"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unmap not saved: %v", err)
	}
	e.proc.mu.Lock()
	e.proc.rebuildErr = nil
	e.proc.mu.Unlock()
	before = len(e.proc.Rebuilds())
	e.ok(e.do("DELETE", mapPath, nil), http.StatusOK, nil)
	if len(e.proc.Rebuilds()) != before+1 {
		t.Error("repeating the unmap did not rebuild")
	}
	if acts := e.auditActions(l.ID); len(acts) != 2 || acts[0] != model.AuditParticipantUnmap {
		t.Errorf("audit after unmap = %v", acts)
	}
}

// "Always hide" respects the limits of PUT /api/settings, so the admin page
// can still save the list afterwards; when it cannot be added nothing is saved.
func TestLessonsMapIgnoreAlwaysLimits(t *testing.T) {
	t.Parallel()
	e, l, _, parts := ltLiveFixture(t)
	mapPath := fmt.Sprintf("/api/lessons/%d/participants/%d/map", l.ID, parts["ваня"])
	set, err := e.st.GetSettings(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	set.IgnoredNames = nil
	for i := range maxIgnoredNames {
		set.IgnoredNames = append(set.IgnoredNames, fmt.Sprintf("Гость %d", i))
	}
	if err := e.st.PutSettings(e.ctx, set, nil); err != nil {
		t.Fatal(err)
	}
	res := e.fail(e.do("POST", mapPath, map[string]any{"action": "ignore", "ignore_always": true}), http.StatusUnprocessableEntity)
	if res.Error.Details["field"] != "ignore_always" || !strings.Contains(res.Error.Message, "администратора") {
		t.Errorf("full list = %+v", res.Error)
	}
	if _, err := e.st.GetMapping(e.ctx, l.ID, "ваня"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("mapping saved although the name could not be added: %v", err)
	}
	if got, _ := e.st.GetSettings(e.ctx); len(got.IgnoredNames) != maxIgnoredNames {
		t.Errorf("ignored names = %d", len(got.IgnoredNames))
	}
	if len(e.audit(l.ID)) != 0 || len(e.proc.Rebuilds()) != 0 {
		t.Error("rejected request audited or rebuilt")
	}
	// A name already on the full list is fine (nothing to add).
	set.IgnoredNames[0] = "ВАНЯ"
	if err := e.st.PutSettings(e.ctx, set, nil); err != nil {
		t.Fatal(err)
	}
	e.ok(e.do("POST", mapPath, map[string]any{"action": "ignore", "ignore_always": true}), http.StatusOK, nil)
	if acts := e.auditActions(l.ID); len(acts) != 1 || acts[0] != model.AuditParticipantMap {
		t.Errorf("audit = %v", acts)
	}

	// A display name longer than the list allows is rejected up front.
	long := strings.Repeat("Я", maxIgnoredNameLength+1)
	ids, err := e.st.UpsertParticipants(e.ctx, l.ID, []model.Participant{{NormalizedName: strings.ToLower(long),
		DisplayName: long, Resolution: model.ResolutionUnidentified, CurrentStatus: model.CurrentLeft}})
	if err != nil {
		t.Fatal(err)
	}
	res = e.fail(e.do("POST", fmt.Sprintf("/api/lessons/%d/participants/%d/map", l.ID, ids[strings.ToLower(long)]),
		map[string]any{"action": "ignore", "ignore_always": true}), http.StatusUnprocessableEntity)
	if res.Error.Details["field"] != "ignore_always" {
		t.Errorf("long name = %+v", res.Error)
	}
}

func TestLessonsMapRememberAliasTakenByOtherStudent(t *testing.T) {
	t.Parallel()
	e, l, st, parts := ltLiveFixture(t)
	// "Ваня" is already Петров's alias.
	if err := e.st.AddAlias(e.ctx, &model.Alias{StudentID: st["petrov"].ID, Alias: "Ваня"}); err != nil {
		t.Fatal(err)
	}
	res := e.fail(e.do("POST", fmt.Sprintf("/api/lessons/%d/participants/%d/map", l.ID, parts["ваня"]),
		map[string]any{"student_id": st["ivanov"].ID, "remember_alias": true}), http.StatusConflict)
	if res.Error.Code != CodeConflict || !strings.Contains(res.Error.Message, "Петров Пётр") ||
		res.Error.Details["is_alias"] != true {
		t.Errorf("conflict = %+v", res.Error)
	}
	if _, err := e.st.GetMapping(e.ctx, l.ID, "ваня"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("mapping saved despite conflict: %v", err)
	}
	if len(e.proc.Rebuilds()) != 0 || len(e.audit(l.ID)) != 0 {
		t.Error("conflict rebuilt or audited")
	}
}

func TestLessonsUpdateAttendance(t *testing.T) {
	t.Parallel()
	e, l, st, _ := ltLiveFixture(t)
	sid := st["sidorov"].ID
	path := fmt.Sprintf("/api/lessons/%d/attendance/%d", l.ID, sid)
	poke, cancel := e.broker.Subscribe(realtime.LessonTopic(l.ID))
	defer cancel()

	row := func() *model.Attendance {
		t.Helper()
		a, err := e.st.GetAttendance(e.ctx, l.ID, sid)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	var v LessonViewDTO
	e.ok(e.do("PATCH", path, map[string]any{"status": "present", "reason": "был в аудитории"}), http.StatusOK, &v)
	if a := row(); a.ManualStatus == nil || *a.ManualStatus != model.FinalPresent || a.FinalStatus != model.FinalPresent ||
		a.ManualBy == nil || *a.ManualBy != e.user.ID {
		t.Errorf("row = %+v", a)
	}
	if v.Counts.Present != 3 || v.Counts.Absent != 0 {
		t.Errorf("counts = %+v", v.Counts)
	}
	if !ltWaitPoke(poke, time.Second) {
		t.Error("status change did not publish")
	}
	a := e.audit(l.ID)[0]
	if a.Action != model.AuditAttendanceStatus || a.EntityType != "attendance" || a.EntityID != fmt.Sprint(sid) ||
		a.PreviousValue != "Отсутствовал (автоматически)" || a.NewValue != "Присутствовал (вручную)" || a.Reason != "был в аудитории" {
		t.Errorf("status audit = %+v", a)
	}

	// Missing "status" key leaves the status alone.
	e.ok(e.do("PATCH", path, map[string]any{"comment": "  сидел с Петровым  "}), http.StatusOK, nil)
	if a := row(); a.ManualStatus == nil || a.Comment != "сидел с Петровым" {
		t.Errorf("comment patch = %+v", a)
	}
	if a := e.audit(l.ID)[0]; a.Action != model.AuditAttendanceComment || a.NewValue != "сидел с Петровым" || a.PreviousValue != "" {
		t.Errorf("comment audit = %+v", a)
	}
	// status: null goes back to automatic.
	e.ok(e.do("PATCH", path, `{"status": null}`), http.StatusOK, &v)
	if a := row(); a.ManualStatus != nil || a.FinalStatus != model.FinalAbsent || a.Comment != "сидел с Петровым" {
		t.Errorf("status null = %+v", a)
	}
	if a := e.audit(l.ID)[0]; a.PreviousValue != "Присутствовал (вручную)" || a.NewValue != "Отсутствовал (автоматически)" {
		t.Errorf("reset audit = %+v", a)
	}
	// Unchanged values write nothing.
	n := len(e.audit(l.ID))
	e.ok(e.do("PATCH", path, `{"status": null, "comment": "сидел с Петровым"}`), http.StatusOK, nil)
	if len(e.audit(l.ID)) != n {
		t.Error("no-op patch wrote audit")
	}
	// Status and comment together: both saved, one audit entry each.
	e.ok(e.do("PATCH", path, map[string]any{"status": "absent", "comment": "", "reason": "ушёл"}), http.StatusOK, nil)
	if a := row(); a.ManualStatus == nil || *a.ManualStatus != model.FinalAbsent || a.Comment != "" {
		t.Errorf("combined patch = %+v", a)
	}
	if acts := e.auditActions(l.ID); len(acts) != n+2 || acts[0] != model.AuditAttendanceComment ||
		acts[1] != model.AuditAttendanceStatus {
		t.Errorf("combined audit = %v", acts)
	}

	// Validation.
	e.fail(e.do("PATCH", path, map[string]any{"status": "late"}), http.StatusUnprocessableEntity)
	e.fail(e.do("PATCH", path, map[string]any{"status": 1}), http.StatusUnprocessableEntity)
	e.fail(e.do("PATCH", path, map[string]any{"comment": strings.Repeat("я", 2001)}), http.StatusUnprocessableEntity)
	e.fail(e.do("PATCH", path, map[string]any{"reason": "x"}), http.StatusUnprocessableEntity)
	e.fail(e.do("PATCH", path, map[string]any{"status": "present", "reason": strings.Repeat("я", 501)}), http.StatusUnprocessableEntity)
	e.fail(e.do("PATCH", fmt.Sprintf("/api/lessons/%d/attendance/9999", l.ID), map[string]any{"status": "present"}), http.StatusNotFound)
	other := e.group("Другая")
	stranger := e.student(other.ID, "Чужой Студент", "")
	e.fail(e.do("PATCH", fmt.Sprintf("/api/lessons/%d/attendance/%d", l.ID, stranger.ID), map[string]any{"status": "present"}),
		http.StatusNotFound)
	e.fail(e.do("PATCH", fmt.Sprintf("/api/lessons/9999/attendance/%d", sid), map[string]any{"status": "present"}), http.StatusNotFound)
}

func TestLessonsUpdateAttendanceStudentWithoutRow(t *testing.T) {
	t.Parallel()
	e, l, _, _ := ltLiveFixture(t)
	g, _ := e.st.GetGroup(e.ctx, l.GroupID)
	late := e.student(g.ID, "Новиков Новый", "")

	// The view shows the newcomer (not rebuilt yet); the PATCH first asks for a
	// rebuild and, when that does not add the row, creates a default one.
	var v LessonViewDTO
	e.ok(e.do("GET", fmt.Sprintf("/api/lessons/%d/attendance", l.ID), nil), http.StatusOK, &v)
	if v.Counts.InGroup != 4 {
		t.Fatalf("in_group = %d", v.Counts.InGroup)
	}
	e.ok(e.do("PATCH", fmt.Sprintf("/api/lessons/%d/attendance/%d", l.ID, late.ID), map[string]any{"status": "present"}),
		http.StatusOK, &v)
	a, err := e.st.GetAttendance(e.ctx, l.ID, late.ID)
	if err != nil || a.FinalStatus != model.FinalPresent || a.AutoStatus != model.FinalAbsent {
		t.Fatalf("row = %+v %v", a, err)
	}
	if len(e.proc.Rebuilds()) != 1 || v.Counts.InGroup != 4 || v.Counts.Present != 3 {
		t.Errorf("rebuilds %v counts %+v", e.proc.Rebuilds(), v.Counts)
	}

	// When the rebuild produces the row, it is used.
	late2 := e.student(g.ID, "Позднов Пётр", "")
	e.proc.mu.Lock()
	e.proc.onRebuild = func(ctx context.Context, id int64) error {
		_, err := e.st.UpsertAttendanceComputed(ctx, id, []model.Attendance{{StudentID: late2.ID,
			CurrentStatus: model.CurrentLeft, AutoStatus: model.FinalPresent, TotalDurationSeconds: 5}})
		return err
	}
	e.proc.mu.Unlock()
	e.ok(e.do("PATCH", fmt.Sprintf("/api/lessons/%d/attendance/%d", l.ID, late2.ID), map[string]any{"comment": "ок"}),
		http.StatusOK, nil)
	if a, err := e.st.GetAttendance(e.ctx, l.ID, late2.ID); err != nil || a.CurrentStatus != model.CurrentLeft || a.Comment != "ок" {
		t.Errorf("rebuilt row = %+v %v", a, err)
	}
}

func TestLessonsStudentHistoryAndAudit(t *testing.T) {
	t.Parallel()
	e, l, st, parts := ltLiveFixture(t)
	iv, pt := st["ivanov"].ID, st["petrov"].ID
	e.ok(e.do("POST", fmt.Sprintf("/api/lessons/%d/participants/%d/map", l.ID, parts["ваня"]),
		map[string]any{"student_id": iv}), http.StatusOK, nil)
	e.ok(e.do("PATCH", fmt.Sprintf("/api/lessons/%d/attendance/%d", l.ID, iv), map[string]any{"comment": "камера выключена"}),
		http.StatusOK, nil)
	e.ok(e.do("PATCH", fmt.Sprintf("/api/lessons/%d/attendance/%d", l.ID, pt), map[string]any{"status": "absent"}),
		http.StatusOK, nil)

	var h lessonStudentHistoryDTO
	e.ok(e.do("GET", fmt.Sprintf("/api/lessons/%d/students/%d/history", l.ID, iv), nil), http.StatusOK, &h)
	if len(h.Intervals) != 2 || h.Intervals[0].ZoomParticipantUUID != "phone" || h.Intervals[1].LeaveAt != nil {
		t.Errorf("intervals = %+v", h.Intervals)
	}
	if len(h.Audit) != 2 || h.Audit[0].Action != model.AuditAttendanceComment || h.Audit[1].Action != model.AuditParticipantMap {
		t.Errorf("ivanov audit = %+v", h.Audit)
	}
	e.ok(e.do("GET", fmt.Sprintf("/api/lessons/%d/students/%d/history", l.ID, pt), nil), http.StatusOK, &h)
	if len(h.Audit) != 1 || h.Audit[0].Action != model.AuditAttendanceStatus || len(h.Intervals) != 1 ||
		h.Intervals[0].LeaveReason != "left the meeting" {
		t.Errorf("petrov history = %+v", h)
	}
	e.ok(e.do("GET", fmt.Sprintf("/api/lessons/%d/students/%d/history", l.ID, st["sidorov"].ID), nil), http.StatusOK, &h)
	if h.Intervals == nil || h.Audit == nil || len(h.Intervals) != 0 || len(h.Audit) != 0 {
		t.Errorf("sidorov history = %+v", h)
	}
	e.fail(e.do("GET", fmt.Sprintf("/api/lessons/%d/students/9999/history", l.ID), nil), http.StatusNotFound)
	e.fail(e.do("GET", fmt.Sprintf("/api/lessons/9999/students/%d/history", iv), nil), http.StatusNotFound)

	var all []AuditDTO
	e.ok(e.do("GET", fmt.Sprintf("/api/lessons/%d/audit", l.ID), nil), http.StatusOK, &all)
	if len(all) != 3 || all[0].Action != model.AuditAttendanceStatus || all[2].Action != model.AuditParticipantMap ||
		all[0].UserName != "Мария Ивановна" {
		t.Errorf("lesson audit = %+v", all)
	}
	e.fail(e.do("GET", "/api/lessons/9999/audit", nil), http.StatusNotFound)
}

func TestLessonsRequireLogin(t *testing.T) {
	t.Parallel()
	e := newLTEnv(t)
	for _, p := range []string{"/api/lessons", "/api/lessons/1", "/api/lessons/1/attendance", "/api/lessons/1/stream", "/api/stream"} {
		req, _ := http.NewRequest("GET", p, nil)
		rec := httptest.NewRecorder()
		e.srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without session = %d", p, rec.Code)
		}
	}
}
