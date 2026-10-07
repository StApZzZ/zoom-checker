package processor

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"zoom-checker/internal/attendance"
	"zoom-checker/internal/export"
	"zoom-checker/internal/model"
	"zoom-checker/internal/realtime"
)

// TZ §36 acceptance scenarios, end to end: signed raw webhook bytes -> ingest
// (BuildRawEvent + InsertWebhookEvent) -> worker -> stored projections.

const mainUUID = "/kV8nE2xQ+O0hR3dWm1x9A=="

type class struct {
	*env
	g      groupFixture
	lesson int64
	m      *zmeeting
}

func (c *class) sid(name string) int64 {
	c.t.Helper()
	id, ok := c.g.students[name]
	if !ok {
		c.t.Fatalf("no student %q", name)
	}
	return id
}

func newClass(t *testing.T, start, end time.Time) *class {
	t.Helper()
	e := newEnv(t)
	g := e.group("10А", "Иванов Иван", "Петров Пётр", "Сидорова Анна", "Александров Александр")
	lessonID := e.lesson(g.id, testMeeting, start, end)
	return &class{env: e, g: g, lesson: lessonID, m: newMeeting(testMeeting, mainUUID, start)}
}

func TestScenario1_OnlineWithinFiveSeconds(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	ctx, cancel := context.WithCancel(c.ctx)
	done := make(chan error, 1)
	go func() { done <- c.p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	updates, unsubscribe := c.broker.Subscribe(realtime.LessonTopic(c.lesson))
	defer unsubscribe()
	list, unsubscribeList := c.broker.Subscribe(realtime.TopicLessons)
	defer unsubscribeList()

	received := time.Now()
	c.deliver(c.m.joined(dev("Иванов Иван", "ivanov"), hms(9, 1, 15)))
	c.p.Notify() // what the webhook handler does after storing the row
	select {
	case <-updates:
	case <-time.After(5 * time.Second):
		t.Fatal("the lesson screen was not notified within 5 s")
	}
	if took := time.Since(received); took > 5*time.Second {
		t.Fatalf("update took %v", took)
	}
	select {
	case <-list:
	case <-time.After(time.Second):
		t.Fatal("lessons list topic not published")
	}

	row := c.row(c.lesson, c.sid("Иванов Иван"))
	if row.CurrentStatus != model.CurrentOnline || row.FinalStatus != model.FinalPresent {
		t.Fatalf("Иванов Иван: %s/%s, want online/present", row.CurrentStatus, row.FinalStatus)
	}
	if l := c.getLesson(c.lesson); l.Status != model.LessonActive {
		t.Fatalf("lesson status %s, want active", l.Status)
	}
}

func TestScenario2_NormalizedNameMatches(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	c.send(c.m.joined(dev("  ИВАНОВ   ИВАН ", "a"), hm(9, 2)))

	p := c.participant(c.lesson, "Иванов Иван")
	if p.Resolution != model.ResolutionMatched || p.StudentID == nil || *p.StudentID != c.sid("Иванов Иван") {
		t.Fatalf("participant %+v, want matched to Иванов Иван", p)
	}
	if p.DisplayName != "  ИВАНОВ   ИВАН " {
		t.Fatalf("display name %q, want the raw Zoom name", p.DisplayName)
	}
	if row := c.row(c.lesson, c.sid("Иванов Иван")); row.CurrentStatus != model.CurrentOnline {
		t.Fatalf("status %s, want online", row.CurrentStatus)
	}
}

func TestScenario3_UnknownNameIsUnidentified(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	c.send(c.m.joined(dev("Ваня", "a"), hm(9, 3)))

	p := c.participant(c.lesson, "Ваня")
	if p.Resolution != model.ResolutionUnidentified || p.StudentID != nil {
		t.Fatalf("participant %+v, want unidentified", p)
	}
	for name, id := range c.g.students {
		row := c.row(c.lesson, id)
		if row.CurrentStatus != model.CurrentNotSeen || row.AutoStatus != model.FinalAbsent {
			t.Fatalf("%s: %s/%s, want not_seen/absent", name, row.CurrentStatus, row.AutoStatus)
		}
	}
	counts, err := c.st.LessonCounts(c.ctx, []int64{c.lesson})
	if err != nil {
		t.Fatal(err)
	}
	if got := counts[c.lesson]; got.Unidentified != 1 || got.Online != 0 || got.InGroup != 4 {
		t.Fatalf("counts %+v", got)
	}
}

func TestScenario4_ReconnectsAddUp(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(18, 0), hm(19, 30))
	first, second := dev("Петров Пётр", "p1"), dev("Петров Пётр", "p2") // participant_uuid changes on rejoin
	c.send(
		c.m.joined(first, hm(18, 10)),
		c.m.left(first, hm(18, 40)),
		c.m.joined(second, hm(18, 50)),
		c.m.left(second, hm(19, 30)),
	)
	row := c.row(c.lesson, c.sid("Петров Пётр"))
	if row.TotalDurationSeconds != 70*60 {
		t.Fatalf("total %d s, want 70 min", row.TotalDurationSeconds)
	}
	if row.CurrentStatus != model.CurrentLeft || !row.FirstJoinAt.Equal(hm(18, 10)) || !row.LastLeaveAt.Equal(hm(19, 30)) {
		t.Fatalf("row %+v", row)
	}
	if late := row.LateSeconds; late == nil || *late != 600 {
		t.Fatalf("late %v, want 600", ip(late))
	}
	if n := len(c.intervals(c.lesson)); n != 2 {
		t.Fatalf("%d intervals, want 2", n)
	}
}

func TestScenario5_TwoDevicesCountedOnce(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(18, 0), hm(19, 30))
	pc, phone := dev("Сидорова Анна", "pc"), dev("Сидорова Анна", "phone")
	c.send(
		c.m.joined(pc, hm(18, 0)),
		c.m.joined(phone, hm(18, 30)),
		c.m.left(pc, hm(19, 0)),
		c.m.left(phone, hm(19, 30)),
	)
	row := c.row(c.lesson, c.sid("Сидорова Анна"))
	if row.TotalDurationSeconds != 90*60 {
		t.Fatalf("total %d s, want 90 min (not 120)", row.TotalDurationSeconds)
	}
	p := c.participant(c.lesson, "Сидорова Анна")
	if p.Devices != 2 || p.TotalSeconds != 90*60 {
		t.Fatalf("participant devices=%d total=%d", p.Devices, p.TotalSeconds)
	}
}

func TestScenario6_RepeatedWebhookIsIgnored(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	d := dev("Иванов Иван", "a")

	join := c.m.joined(d, hm(9, 1))
	first, again := c.deliver(join), c.deliver(join) // processed in the same batch
	c.process()

	leave := c.m.left(d, hm(9, 30))
	leaveEv := c.deliver(leave)
	c.process()
	c.m.etsOffset = 7777 // Zoom redelivery with a new event_ts
	redelivered := c.deliver(c.m.left(d, hm(9, 30)))
	c.clock.Advance(time.Hour) // a retry long after the original
	late := c.deliver(leave)
	c.process()

	for _, tc := range []struct {
		ev   *model.WebhookEvent
		want model.EventStatus
	}{
		{first, model.EventProcessed}, {again, model.EventDuplicate},
		{leaveEv, model.EventProcessed}, {redelivered, model.EventDuplicate}, {late, model.EventDuplicate},
	} {
		if got := c.event(tc.ev.ID).ProcessingStatus; got != tc.want {
			t.Errorf("event %d: status %s, want %s", tc.ev.ID, got, tc.want)
		}
	}
	ivs := c.intervals(c.lesson)
	if len(ivs) != 1 || !ivs[0].JoinAt.Equal(hm(9, 1)) || ivs[0].LeaveAt == nil || !ivs[0].LeaveAt.Equal(hm(9, 30)) {
		t.Fatalf("intervals %+v, want one 09:01-09:30", ivs)
	}
	if row := c.row(c.lesson, c.sid("Иванов Иван")); row.TotalDurationSeconds != 29*60 {
		t.Fatalf("total %d, want 29 min", row.TotalDurationSeconds)
	}
}

func TestScenario7_MeetingEndClosesOpenIntervals(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	end := hms(10, 31, 47)
	c.send(
		c.m.started(hms(9, 0, 3)),
		c.m.joined(dev("Иванов Иван", "a"), hm(9, 1)),
		c.m.joined(dev("Петров Пётр", "b"), hm(9, 2)),
		c.m.joined(dev("Сидорова Анна", "c"), hm(9, 3)),
		c.m.left(dev("Петров Пётр", "b"), hm(9, 50)),
	)
	if l := c.getLesson(c.lesson); l.Status != model.LessonActive || l.ActualStartAt == nil || !l.ActualStartAt.Equal(hms(9, 0, 3)) {
		t.Fatalf("lesson before end: %s start=%v", l.Status, tstr(l.ActualStartAt))
	}
	c.send(c.m.ended(end))

	for _, iv := range c.intervals(c.lesson) {
		if iv.LeaveAt == nil {
			t.Fatalf("interval of %q still open", iv.ZoomUserName)
		}
		wantReason := attendance.ReasonMeetingEnded
		wantEnd := end
		if iv.ZoomUserName == "Петров Пётр" {
			wantReason, wantEnd = "left the meeting", hm(9, 50)
		}
		if !iv.LeaveAt.Equal(wantEnd) || iv.LeaveReason != wantReason {
			t.Errorf("%q: leave %v reason %q, want %v %q", iv.ZoomUserName, tstr(iv.LeaveAt), iv.LeaveReason, wantEnd, wantReason)
		}
	}
	for _, name := range []string{"Иванов Иван", "Сидорова Анна"} {
		row := c.row(c.lesson, c.sid(name))
		if row.CurrentStatus != model.CurrentLeft || !row.LastLeaveAt.Equal(end) {
			t.Errorf("%s: %s last=%v", name, row.CurrentStatus, tstr(row.LastLeaveAt))
		}
	}
	l := c.getLesson(c.lesson)
	if l.Status != model.LessonFinished || l.ActualEndAt == nil || !l.ActualEndAt.Equal(end) {
		t.Fatalf("lesson %s end=%v, want finished at %v", l.Status, tstr(l.ActualEndAt), end)
	}
	if s := c.session(mainUUID); s.EndedAt == nil || !s.EndedAt.Equal(end) || s.StartedAt == nil || !s.StartedAt.Equal(hms(9, 0, 3)) {
		t.Fatalf("session %+v", s)
	}
}

func TestScenario8_ManualMappingMovesAccumulatedTime(t *testing.T) {
	t.Parallel()
	for _, afterFinish := range []bool{false, true} {
		name := "during lesson"
		if afterFinish {
			name = "after lesson finished"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := newClass(t, hm(9, 0), hm(10, 30))
			alex := dev("Александр", "alex")
			c.send(c.m.joined(alex, hm(9, 5)), c.m.left(alex, hm(9, 45)))
			if afterFinish {
				c.send(c.m.ended(hm(10, 30)))
			}
			target := c.sid("Александров Александр")
			if row := c.row(c.lesson, target); row.CurrentStatus != model.CurrentNotSeen {
				t.Fatalf("before mapping: %s", row.CurrentStatus)
			}
			if p := c.participant(c.lesson, "Александр"); p.Resolution != model.ResolutionUnidentified {
				t.Fatalf("before mapping: %s", p.Resolution)
			}

			// What POST /api/lessons/{id}/participants/{pid}/map does.
			if err := c.st.UpsertMapping(c.ctx, &model.Mapping{LessonID: c.lesson, NormalizedName: "александр",
				Action: model.MappingStudent, StudentID: &target}); err != nil {
				t.Fatal(err)
			}
			c.rebuild(c.lesson)

			row := c.row(c.lesson, target)
			if row.TotalDurationSeconds != 40*60 || row.FinalStatus != model.FinalPresent || row.CurrentStatus != model.CurrentLeft {
				t.Fatalf("after mapping: %+v", row)
			}
			p := c.participant(c.lesson, "Александр")
			if p.Resolution != model.ResolutionManual || p.StudentID == nil || *p.StudentID != target {
				t.Fatalf("participant %+v", p)
			}
			ivs, err := c.st.ListStudentIntervals(c.ctx, c.lesson, target)
			if err != nil {
				t.Fatal(err)
			}
			if len(ivs) != 1 || *ivs[0].DurationSeconds != 40*60 {
				t.Fatalf("student intervals %+v", ivs)
			}
			if got := len(c.attendance(c.lesson)); got != 4 {
				t.Fatalf("%d attendance rows, want 4", got)
			}
		})
	}
}

func TestScenario9_ExportHasEveryStudent(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	c.send(
		c.m.started(hm(9, 0)),
		c.m.joined(dev("Иванов Иван", "a"), hm(9, 1)),
		c.m.ended(hm(10, 30)),
	)
	l := c.getLesson(c.lesson)
	if l.Status != model.LessonFinished {
		t.Fatalf("lesson %s, want finished", l.Status)
	}
	rows := c.attendance(c.lesson)
	if len(rows) != len(c.g.students) {
		t.Fatalf("%d rows, want every student (%d)", len(rows), len(c.g.students))
	}
	for name, id := range c.g.students {
		r, ok := rows[id]
		if !ok {
			t.Fatalf("no row for %s", name)
		}
		want := model.FinalAbsent
		if name == "Иванов Иван" {
			want = model.FinalPresent
		}
		if r.FinalStatus != want {
			t.Errorf("%s: %s, want %s", name, r.FinalStatus, want)
		}
	}

	// Export the way the lesson export endpoint does: every attendance row.
	sheet := export.LessonSheet{GroupName: "10А", LessonTitle: l.Title, PlannedStart: l.PlannedStartAt, ZoomMeetingID: l.ZoomMeetingID}
	students, err := c.st.ListStudents(c.ctx, c.g.id, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range students {
		r := rows[st.ID]
		sheet.Rows = append(sheet.Rows, export.LessonRow{FullName: st.FullName, FirstJoin: r.FirstJoinAt, LastLeave: r.LastLeaveAt,
			TotalSeconds: r.TotalDurationSeconds, FinalStatus: r.FinalStatus})
	}
	var buf bytes.Buffer
	if err := export.WriteLessonCSV(&buf, sheet); err != nil {
		t.Fatal(err)
	}
	for name := range c.g.students {
		if !strings.Contains(buf.String(), name) {
			t.Errorf("export misses %s", name)
		}
	}
}
