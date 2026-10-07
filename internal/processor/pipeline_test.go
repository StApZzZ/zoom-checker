package processor

import (
	"errors"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"zoom-checker/internal/attendance"
	"zoom-checker/internal/model"
	"zoom-checker/internal/store"
)

// TestTestdataEndToEnd feeds the real-shaped payloads of internal/zoom/testdata
// byte-for-byte through ingest and the worker.
func TestTestdataEndToEnd(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	g := e.group("10А", "Иванов Иван", "Фёдорова Алёна", "Петров Пётр")
	lessonID := e.lesson(g.id, testMeeting, hm(9, 0), hm(10, 30))

	steps := []struct {
		file string
		want model.EventStatus
	}{
		{"url_validation.json", model.EventIgnored},
		{"participant_admitted.json", model.EventIgnored}, // unsupported type
		{"meeting_started.json", model.EventProcessed},
		{"participant_joined.json", model.EventProcessed},             // Иванов  Иван 09:01:15
		{"participant_joined_emoji.json", model.EventProcessed},       // 🦊 Фёдорова Алёна 09:05:10.123
		{"breakout_joined.json", model.EventProcessed},                // Иванов in a breakout room 09:20
		{"breakout_left.json", model.EventProcessed},                  // back 09:35
		{"participant_left.json", model.EventProcessed},               // Иванов leaves 09:44:55
		{"participant_left_null_id.json", model.EventIgnored},         // no meeting id, unknown uuid
		{"participant_left_number_id.json", model.EventProcessed},     // another meeting, no lesson
		{"participant_joined_doc_example.json", model.EventProcessed}, // 2021 doc example, no lesson
		{"meeting_ended.json", model.EventProcessed},                  // 10:31:47
	}
	for _, s := range steps {
		raw := e.deliver(readTestdata(t, s.file))
		e.process()
		got := e.event(raw.ID)
		if got.ProcessingStatus != s.want {
			t.Fatalf("%s: status %s (%q), want %s", s.file, got.ProcessingStatus, got.Error, s.want)
		}
		if got.ProcessingStatus == model.EventIgnored && got.Error == "" {
			t.Errorf("%s: ignored without an explanation", s.file)
		}
	}

	ivanov := e.row(lessonID, g.students["Иванов Иван"])
	// 09:01:15-09:44:55 in the main room; the breakout visit lies inside it.
	if ivanov.TotalDurationSeconds != 43*60+40 || ivanov.CurrentStatus != model.CurrentLeft {
		t.Fatalf("Иванов: %+v", ivanov)
	}
	// The emoji makes the name differ from the roster: unidentified, and closed by meeting.ended.
	fox := e.participant(lessonID, "🦊 Фёдорова Алёна")
	if fox.Resolution != model.ResolutionUnidentified || fox.LastLeaveAt == nil || !fox.LastLeaveAt.Equal(hms(10, 31, 47)) {
		t.Fatalf("fox participant: %+v", fox)
	}
	l := e.getLesson(lessonID)
	if l.Status != model.LessonFinished || !l.ActualStartAt.Equal(hms(9, 0, 3)) || !l.ActualEndAt.Equal(hms(10, 31, 47)) {
		t.Fatalf("lesson %s %v..%v", l.Status, tstr(l.ActualStartAt), tstr(l.ActualEndAt))
	}
	if s := e.session("lQ7mYt2+RkeT3zKd0aWq8w=="); s.LessonID != nil || s.MeetingID != "251361636" {
		t.Fatalf("other meeting session %+v", s)
	}
	nullID := e.events(t, "participant_left_null_id")
	if !strings.Contains(nullID.Error, "uuid") {
		t.Fatalf("null id note %q", nullID.Error)
	}
	// Breakout intervals are stored and attributed to the student.
	var breakout int
	for _, iv := range e.intervals(lessonID) {
		if iv.JoinAt.Equal(hm(9, 20)) && iv.LeaveAt != nil && iv.LeaveAt.Equal(hm(9, 35)) {
			breakout++
			if iv.AttendanceID == nil || iv.ZoomParticipantID == nil {
				t.Fatalf("breakout interval not attributed: %+v", iv)
			}
		}
	}
	if breakout != 1 {
		t.Fatalf("%d breakout intervals, want 1", breakout)
	}
}

// events finds the raw row whose payload came from the named testdata file (by
// its distinctive participant name / uuid).
func (e *env) events(t *testing.T, file string) *model.WebhookEvent {
	t.Helper()
	evs, err := e.st.ListWebhookEvents(e.ctx, store.EventFilter{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if file == "participant_left_null_id" && ev.MeetingUUID == "qLa3t1fZTxe0n8Yw6Vb+5g==" {
			return &ev
		}
	}
	t.Fatalf("event of %s not found", file)
	return nil
}

func TestOutOfOrderArrivalGivesSameProjection(t *testing.T) {
	t.Parallel()
	m := newMeeting(testMeeting, mainUUID, hm(9, 0))
	a, a2, b := dev("Иванов Иван", "a"), dev("Иванов Иван", "a2"), dev("Петров Пётр", "b")
	bodies := [][]byte{
		m.started(hm(9, 0)),
		m.joined(a, hm(9, 1)),
		m.joined(b, hm(9, 2)),
		m.left(a, hm(9, 30)),
		m.joined(a2, hm(9, 40)),
		m.left(b, hm(10, 0)),
		m.ended(hm(10, 30)),
	}
	run := func(order []int, batch bool) string {
		e := newEnv(t)
		g := e.group("10А", "Иванов Иван", "Петров Пётр", "Сидорова Анна")
		lessonID := e.lesson(g.id, testMeeting, hm(9, 0), hm(10, 30))
		e.clock.Set(hm(11, 0))
		for _, i := range order {
			e.deliver(bodies[i])
			if !batch {
				e.process()
			}
		}
		e.process()
		return e.snapshot(lessonID)
	}
	inOrder := []int{0, 1, 2, 3, 4, 5, 6}
	want := run(inOrder, false)
	if !strings.Contains(want, "total=4740") { // Иванов: 29 min + 50 min (closed by meeting.ended)
		t.Fatalf("unexpected in-order projection:\n%s", want)
	}
	rng := rand.New(rand.NewPCG(1, 2))
	orders := [][]int{{6, 5, 4, 3, 2, 1, 0}, {3, 1, 6, 0, 5, 2, 4}}
	for range 3 {
		o := slicesClone(inOrder)
		rng.Shuffle(len(o), func(i, j int) { o[i], o[j] = o[j], o[i] })
		orders = append(orders, o)
	}
	for _, o := range orders {
		for _, batch := range []bool{false, true} {
			if got := run(o, batch); got != want {
				t.Fatalf("order %v (batch=%v):\n%s\nwant:\n%s", o, batch, got, want)
			}
		}
	}
}

func slicesClone(s []int) []int { return append([]int(nil), s...) }

func TestRecurringMeetingLinksEachDateToItsLesson(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	g := e.group("10А", "Иванов Иван")
	ivanov := g.students["Иванов Иван"]
	week := 7 * 24 * time.Hour
	first := e.lesson(g.id, testMeeting, hm(9, 0), hm(10, 30))
	second := e.lesson(g.id, testMeeting, hm(9, 0).Add(week), hm(10, 30).Add(week))

	m1 := newMeeting(testMeeting, "uuid-week-1", hm(9, 0))
	e.send(m1.started(hm(9, 0)), m1.joined(dev("Иванов Иван", "a"), hm(9, 5)), m1.left(dev("Иванов Иван", "a"), hm(10, 0)), m1.ended(hm(10, 30)))
	m2 := newMeeting(testMeeting, "uuid-week-2", hm(9, 0).Add(week))
	e.send(m2.started(hm(9, 0).Add(week)), m2.joined(dev("Иванов Иван", "b"), hm(9, 10).Add(week)))

	if l := e.session("uuid-week-1").LessonID; l == nil || *l != first {
		t.Fatalf("week 1 session linked to %v, want %d", ip(l), first)
	}
	if l := e.session("uuid-week-2").LessonID; l == nil || *l != second {
		t.Fatalf("week 2 session linked to %v, want %d", ip(l), second)
	}
	if r := e.row(first, ivanov); r.TotalDurationSeconds != 55*60 || r.CurrentStatus != model.CurrentLeft {
		t.Fatalf("week 1: %+v", r)
	}
	if r := e.row(second, ivanov); r.CurrentStatus != model.CurrentOnline || !r.FirstJoinAt.Equal(hm(9, 10).Add(week)) {
		t.Fatalf("week 2: %+v", r)
	}
	if e.getLesson(first).Status != model.LessonFinished || e.getLesson(second).Status != model.LessonActive {
		t.Fatal("lesson statuses")
	}
}

func TestMeetingRestartWithinOneLesson(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	g := e.group("10А", "Иванов Иван")
	ivanov := g.students["Иванов Иван"]
	lessonID := e.lesson(g.id, testMeeting, hm(9, 0), hm(10, 30))

	m1 := newMeeting(testMeeting, "uuid-first", hm(9, 0))
	e.send(m1.started(hm(9, 0)), m1.joined(dev("Иванов Иван", "a"), hm(9, 0)), m1.ended(hm(9, 30)))
	if l := e.getLesson(lessonID); l.Status != model.LessonFinished {
		t.Fatalf("after the first session ended: %s", l.Status)
	}
	// The teacher pressed "finish" (what POST /finish does), then restarted Zoom.
	if err := e.st.SetLessonManualFinished(e.ctx, lessonID, ptrTime(hm(9, 31))); err != nil {
		t.Fatal(err)
	}
	e.rebuild(lessonID)

	m2 := newMeeting(testMeeting, "uuid-second", hm(9, 35))
	e.send(m2.started(hm(9, 35)), m2.joined(dev("Иванов Иван", "b"), hm(9, 36)))
	l := e.getLesson(lessonID)
	if l.ManualFinishedAt != nil || l.Status != model.LessonActive {
		t.Fatalf("restarted lesson: status %s manual finish %v", l.Status, tstr(l.ManualFinishedAt))
	}
	audit, err := e.st.ListAudit(e.ctx, store.AuditFilter{LessonID: &lessonID})
	if err != nil {
		t.Fatal(err)
	}
	if len(audit) != 1 || audit[0].Action != model.AuditLessonReopen {
		t.Fatalf("audit %+v, want one automatic reopen", audit)
	}
	e.send(m2.left(dev("Иванов Иван", "b"), hm(10, 0)), m2.ended(hm(10, 30)))

	for _, uuid := range []string{"uuid-first", "uuid-second"} {
		if s := e.session(uuid); s.LessonID == nil || *s.LessonID != lessonID {
			t.Fatalf("session %s not linked to the lesson", uuid)
		}
	}
	r := e.row(lessonID, ivanov)
	if r.TotalDurationSeconds != (30+24)*60 {
		t.Fatalf("total %d, want 54 min (union of both sessions)", r.TotalDurationSeconds)
	}
	l = e.getLesson(lessonID)
	if l.Status != model.LessonFinished || !l.ActualStartAt.Equal(hm(9, 0)) || !l.ActualEndAt.Equal(hm(10, 30)) {
		t.Fatalf("lesson %s %v..%v", l.Status, tstr(l.ActualStartAt), tstr(l.ActualEndAt))
	}
}

func TestLessonCreatedAfterMeetingStarted(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	g := e.group("10А", "Иванов Иван", "Петров Пётр")
	m := newMeeting(testMeeting, mainUUID, hm(9, 0))
	e.send(m.started(hm(9, 0)), m.joined(dev("Иванов Иван", "a"), hm(9, 1)), m.joined(dev("Ваня", "v"), hm(9, 2)))
	s := e.session(mainUUID)
	if s.LessonID != nil || s.LinkMode != model.LinkAuto {
		t.Fatalf("session %+v, want unlinked", s)
	}

	lessonID := e.lesson(g.id, testMeeting, hm(9, 0), hm(10, 30)) // RelinkLesson attaches stored events
	if s := e.session(mainUUID); s.LessonID == nil || *s.LessonID != lessonID || s.LinkMode != model.LinkAuto {
		t.Fatalf("session after lesson creation %+v", s)
	}
	if r := e.row(lessonID, g.students["Иванов Иван"]); r.CurrentStatus != model.CurrentOnline || !r.FirstJoinAt.Equal(hm(9, 1)) {
		t.Fatalf("Иванов: %+v", r)
	}
	if r := e.row(lessonID, g.students["Петров Пётр"]); r.CurrentStatus != model.CurrentNotSeen {
		t.Fatalf("Петров: %+v", r)
	}
	if p := e.participant(lessonID, "Ваня"); p.Resolution != model.ResolutionUnidentified {
		t.Fatalf("Ваня: %+v", p)
	}
	// New events keep flowing into the lesson.
	e.send(m.left(dev("Иванов Иван", "a"), hm(9, 31)))
	if r := e.row(lessonID, g.students["Иванов Иван"]); r.TotalDurationSeconds != 30*60 {
		t.Fatalf("Иванов total %d", r.TotalDurationSeconds)
	}
}

func TestManualUnlinkIsSticky(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	g := e.group("10А", "Иванов Иван")
	ivanov := g.students["Иванов Иван"]
	lessonID := e.lesson(g.id, testMeeting, hm(9, 0), hm(10, 30))
	other := e.lesson(g.id, "99988877766", hm(9, 0), hm(10, 30))
	m := newMeeting(testMeeting, mainUUID, hm(9, 0))
	e.send(m.started(hm(9, 0)), m.joined(dev("Иванов Иван", "a"), hm(9, 1)))
	s := e.session(mainUUID)

	if err := e.p.LinkSession(e.ctx, s.ID, nil); err != nil {
		t.Fatal(err)
	}
	if s := e.session(mainUUID); s.LessonID != nil || s.LinkMode != model.LinkManual {
		t.Fatalf("after unlink %+v", s)
	}
	if r := e.row(lessonID, ivanov); r.CurrentStatus != model.CurrentNotSeen || len(e.intervals(lessonID)) != 0 {
		t.Fatalf("lesson still shows the unlinked session: %+v", r)
	}
	if l := e.getLesson(lessonID); l.Status != model.LessonPlanned {
		t.Fatalf("lesson status %s, want planned", l.Status)
	}

	e.send(m.left(dev("Иванов Иван", "a"), hm(9, 30)), m.joined(dev("Иванов Иван", "b"), hm(9, 40)))
	if err := e.p.RelinkLesson(e.ctx, lessonID); err != nil {
		t.Fatal(err)
	}
	if s := e.session(mainUUID); s.LessonID != nil {
		t.Fatalf("manually unlinked session was linked again to %d", *s.LessonID)
	}

	// A manual link to another lesson moves the data there and is sticky too.
	if err := e.p.LinkSession(e.ctx, s.ID, &other); err != nil {
		t.Fatal(err)
	}
	if err := e.p.RelinkLesson(e.ctx, other); err != nil {
		t.Fatal(err)
	}
	if s := e.session(mainUUID); s.LessonID == nil || *s.LessonID != other || s.LinkMode != model.LinkManual {
		t.Fatalf("after manual link %+v", s)
	}
	if r := e.row(other, ivanov); r.CurrentStatus != model.CurrentOnline || r.TotalDurationSeconds == 0 {
		t.Fatalf("other lesson: %+v", r)
	}

	if err := e.p.LinkSession(e.ctx, 424242, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown session: %v", err)
	}
	missing := int64(424242)
	if err := e.p.LinkSession(e.ctx, s.ID, &missing); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown lesson: %v", err)
	}
}

func TestLinkWindowBoundaries(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	g := e.group("10А", "Иванов Иван")
	lessonID := e.lesson(g.id, testMeeting, hm(10, 0), hm(11, 30))

	// link_window_hours is how early before the planned start a session may
	// start; after the planned end there is a fixed 30 minutes.
	cases := []struct {
		name   string
		window int
		start  time.Time
		linked bool
	}{
		{"1h before start", 1, hm(9, 0), true},
		{"just outside before", 1, hms(8, 59, 59), false},
		{"6h window: 6h before", 6, hm(4, 0), true},
		{"6h window: outside", 6, hms(3, 59, 59), false},
		{"inside planned time", 1, hm(10, 45), true},
		{"30 min after end", 1, hm(12, 0), true},
		{"just outside after", 1, hms(12, 0, 1), false},
		{"a wide window does not reach past the end", 6, hm(13, 0), false},
		{"0h window: at start", 0, hm(10, 0), true},
		{"0h window: before", 0, hms(9, 59, 59), false},
	}
	for i, tc := range cases {
		e.settings(func(s *model.Settings) { s.LinkWindowHours = tc.window })
		uuid := "uuid-window-" + string(rune('a'+i))
		m := newMeeting(testMeeting, uuid, tc.start)
		e.send(m.started(tc.start))
		s := e.session(uuid)
		if got := s.LessonID != nil && *s.LessonID == lessonID; got != tc.linked {
			t.Errorf("%s: linked=%v, want %v", tc.name, got, tc.linked)
		}
	}

	// A different meeting id never links, whatever the time.
	e.settings(func(s *model.Settings) { s.LinkWindowHours = 6 })
	m := newMeeting("11122233344", "uuid-other-id", hm(10, 0))
	e.send(m.started(hm(10, 0)))
	if s := e.session("uuid-other-id"); s.LessonID != nil {
		t.Fatal("session of another meeting id was linked")
	}
}

func TestClosestLessonWins(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	g := e.group("10А", "Иванов Иван")
	morning := e.lesson(g.id, testMeeting, hm(9, 0), hm(10, 30))
	noon := e.lesson(g.id, testMeeting, hm(11, 0), hm(12, 30))
	twin := e.lesson(g.id, testMeeting, hm(11, 0), hm(12, 30)) // same time: the lower id wins

	m := newMeeting(testMeeting, "uuid-noon", hm(10, 50))
	e.send(m.started(hm(10, 50)))
	if s := e.session("uuid-noon"); s.LessonID == nil || *s.LessonID != noon {
		t.Fatalf("linked to %v, want %d (morning %d, twin %d)", ip(s.LessonID), noon, morning, twin)
	}
}

func TestRelinkAfterLessonEdits(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	g := e.group("10А", "Иванов Иван")
	ivanov := g.students["Иванов Иван"]
	morning := e.lesson(g.id, testMeeting, hm(9, 0), hm(10, 30))

	// A 14:00 session on the same meeting id is not the morning lesson's ...
	m := newMeeting(testMeeting, "uuid-afternoon", hm(14, 0))
	e.send(m.started(hm(14, 0)), m.joined(dev("Иванов Иван", "a"), hm(14, 1)))
	if s := e.session("uuid-afternoon"); s.LessonID != nil {
		t.Fatalf("the 14:00 session went to the morning lesson %d", *s.LessonID)
	}
	// ... it waits for the lesson it really belongs to.
	afternoon := e.lesson(g.id, testMeeting, hm(14, 0), hm(15, 30))
	if s := e.session("uuid-afternoon"); s.LessonID == nil || *s.LessonID != afternoon {
		t.Fatalf("session linked to %v, want the afternoon lesson %d", ip(s.LessonID), afternoon)
	}
	if r := e.row(morning, ivanov); r.CurrentStatus != model.CurrentNotSeen {
		t.Fatalf("morning lesson counts the session: %+v", r)
	}
	if r := e.row(afternoon, ivanov); r.CurrentStatus != model.CurrentOnline {
		t.Fatalf("afternoon: %+v", r)
	}

	// Changing the meeting id of the lesson unlinks its auto-linked session.
	l := e.getLesson(afternoon)
	l.ZoomMeetingID = "11122233344"
	if err := e.st.UpdateLesson(e.ctx, l); err != nil {
		t.Fatal(err)
	}
	if err := e.p.RelinkLesson(e.ctx, afternoon); err != nil {
		t.Fatal(err)
	}
	if s := e.session("uuid-afternoon"); s.LessonID != nil || s.LinkMode != model.LinkAuto {
		t.Fatalf("after the id change the session is %+v, want unlinked (auto)", s)
	}
	if r := e.row(afternoon, ivanov); r.CurrentStatus != model.CurrentNotSeen {
		t.Fatalf("afternoon still shows the session: %+v", r)
	}
	// Changing it back links it again.
	l.ZoomMeetingID = testMeeting
	if err := e.st.UpdateLesson(e.ctx, l); err != nil {
		t.Fatal(err)
	}
	if err := e.p.RelinkLesson(e.ctx, afternoon); err != nil {
		t.Fatal(err)
	}
	if s := e.session("uuid-afternoon"); s.LessonID == nil || *s.LessonID != afternoon {
		t.Fatalf("after the id was restored the session is linked to %v", ip(s.LessonID))
	}

	// A better fitting lesson created late claims the session: it started
	// inside that lesson's planned time, not just before the afternoon one.
	early := newMeeting(testMeeting, "uuid-early", hm(13, 40))
	e.send(early.started(hm(13, 40)))
	if s := e.session("uuid-early"); s.LessonID == nil || *s.LessonID != afternoon {
		t.Fatalf("13:40 session linked to %v, want the upcoming afternoon lesson", ip(s.LessonID))
	}
	consult := e.lesson(g.id, testMeeting, hm(13, 30), hm(14, 0))
	if s := e.session("uuid-early"); s.LessonID == nil || *s.LessonID != consult {
		t.Fatalf("13:40 session linked to %v, want the lesson running at 13:40 (%d)", ip(s.LessonID), consult)
	}

	// Moving the afternoon lesson out of reach unlinks its session completely.
	l = e.getLesson(afternoon)
	l.PlannedStartAt, l.PlannedEndAt = hm(20, 0), hm(21, 0)
	if err := e.st.UpdateLesson(e.ctx, l); err != nil {
		t.Fatal(err)
	}
	if err := e.p.RelinkLesson(e.ctx, afternoon); err != nil {
		t.Fatal(err)
	}
	if s := e.session("uuid-afternoon"); s.LessonID == nil || *s.LessonID != consult {
		// 14:00 is the end of the 13:30–14:00 lesson: it is the only candidate left.
		t.Fatalf("session %+v, want it in the 13:30 lesson", s)
	}

	if err := e.p.RelinkLesson(e.ctx, 999999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown lesson: %v", err)
	}
	if err := e.p.RebuildLesson(e.ctx, 999999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown lesson: %v", err)
	}
}

func TestBreakoutRoomsKeepStudentPresent(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	d := dev("Иванов Иван", "a")
	c.send(c.m.joined(d, hm(9, 1)))
	// Moving to a breakout room: Zoom reports a main-room leave and a breakout join
	// in the same second (event_ts orders them).
	c.m.etsOffset = 100
	c.send(c.m.leftWith(d, hm(9, 20), "join breakout room"))
	c.m.etsOffset = 300
	c.send(c.m.breakout(true, d, "room-1", hm(9, 20)))

	c.clock.Set(hm(9, 25))
	c.rebuild(c.lesson)
	row := c.row(c.lesson, c.sid("Иванов Иван"))
	if row.CurrentStatus != model.CurrentOnline || row.TotalDurationSeconds != 24*60 {
		t.Fatalf("in the breakout room: %s total=%d, want online 24 min", row.CurrentStatus, row.TotalDurationSeconds)
	}

	c.m.etsOffset = 100
	c.send(c.m.breakout(false, d, "room-1", hm(9, 35)))
	c.m.etsOffset = 300
	c.send(c.m.joined(d, hm(9, 35)), c.m.left(d, hm(10, 0)))
	row = c.row(c.lesson, c.sid("Иванов Иван"))
	if row.CurrentStatus != model.CurrentLeft || row.TotalDurationSeconds != 59*60 {
		t.Fatalf("after the lesson: %s total=%d, want 59 min", row.CurrentStatus, row.TotalDurationSeconds)
	}
	if p := c.participant(c.lesson, "Иванов Иван"); p.Resolution != model.ResolutionMatched {
		t.Fatalf("participant %+v", p)
	}
}

func TestBreakoutKeyFallsBackToParentUserID(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	// No participant_uuid (older payloads): the breakout events are tied together by
	// parent_user_id although their room-local user_id differs.
	d := device{name: "Петров Пётр", userID: "16778240"}
	c.send(c.m.breakout(true, d, "room-1", hm(9, 20)))
	raw := c.deliver(c.m.breakout(false, d, "room-1", hm(9, 50)))
	c.process()
	if got := c.event(raw.ID).ProcessingStatus; got != model.EventProcessed {
		t.Fatalf("status %s", got)
	}
	ivs := c.intervals(c.lesson)
	if len(ivs) != 1 || ivs[0].DurationSeconds == nil || *ivs[0].DurationSeconds != 30*60 {
		t.Fatalf("intervals %+v, want one 30 min breakout interval", ivs)
	}
}

func TestHostIsIgnored(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	teacher := device{name: "Мария Ивановна", puuid: "PU-host", userID: "16777216", accountID: hostAccount}
	c.send(c.m.started(hm(9, 0)), c.m.joined(teacher, hm(9, 0)), c.m.joined(dev("Иванов Иван", "a"), hm(9, 1)))

	p := c.participant(c.lesson, "Мария Ивановна")
	if p.Resolution != model.ResolutionIgnored || p.IgnoreReason != model.IgnoreHost {
		t.Fatalf("host participant %+v, want ignored(host)", p)
	}
	counts, err := c.st.LessonCounts(c.ctx, []int64{c.lesson})
	if err != nil {
		t.Fatal(err)
	}
	if got := counts[c.lesson]; got.Unidentified != 0 || got.Online != 1 {
		t.Fatalf("counts %+v", got)
	}

	c.settings(func(s *model.Settings) { s.IgnoreHost = false })
	c.rebuild(c.lesson)
	if p := c.participant(c.lesson, "Мария Ивановна"); p.Resolution != model.ResolutionUnidentified {
		t.Fatalf("with ignore_host off: %+v", p)
	}

	c.settings(func(s *model.Settings) { s.IgnoredNames = []string{"  мария  ИВАНОВНА"} })
	c.rebuild(c.lesson)
	if p := c.participant(c.lesson, "Мария Ивановна"); p.Resolution != model.ResolutionIgnored || p.IgnoreReason != model.IgnoreList {
		t.Fatalf("ignore list: %+v", p)
	}
}

func TestRenameAtLeaveIsRecorded(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	c.send(c.m.joined(dev("Ваня", "a"), hm(9, 1)), c.m.left(dev("Иванов Иван", "a"), hm(9, 31)))
	ivs := c.intervals(c.lesson)
	if len(ivs) != 1 {
		t.Fatalf("intervals %+v", ivs)
	}
	if ivs[0].ZoomUserName != "Иванов Иван" || ivs[0].LeaveReason != "left the meeting; "+attendance.ReasonRenamed+" from: Ваня" {
		t.Fatalf("interval %+v", ivs[0])
	}
	if r := c.row(c.lesson, c.sid("Иванов Иван")); r.TotalDurationSeconds != 30*60 {
		t.Fatalf("total %d", r.TotalDurationSeconds)
	}
}

func TestParticipantEventWithoutMeetingID(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	noID := &zmeeting{uuid: mainUUID, host: hostAccount, start: hm(9, 0), etsOffset: 1}
	// Unknown session: cannot be linked.
	first := c.deliver(noID.joined(dev("Иванов Иван", "a"), hm(9, 1)))
	c.process()
	if ev := c.event(first.ID); ev.ProcessingStatus != model.EventIgnored || ev.Error == "" {
		t.Fatalf("event %+v, want ignored with a note", ev)
	}
	// Once the session is known (it has a meeting id), such events count.
	c.send(c.m.started(hm(9, 0)))
	second := c.deliver(noID.left(dev("Иванов Иван", "a"), hm(9, 30)))
	c.process()
	if ev := c.event(second.ID); ev.ProcessingStatus != model.EventProcessed {
		t.Fatalf("event %+v, want processed", ev)
	}
	if s := c.session(mainUUID); s.MeetingID != testMeeting {
		t.Fatalf("meeting id overwritten with empty: %+v", s)
	}
	// The earlier ignored join is raw data of the session now and closes the visit.
	if r := c.row(c.lesson, c.sid("Иванов Иван")); r.TotalDurationSeconds != 29*60 {
		t.Fatalf("total %d", r.TotalDurationSeconds)
	}
}

func TestMeetingEndedFirstFillsStart(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	c.send(c.m.ended(hms(10, 31, 47)))
	s := c.session(mainUUID)
	if s.StartedAt == nil || !s.StartedAt.Equal(hm(9, 0)) || s.EndedAt == nil || !s.EndedAt.Equal(hms(10, 31, 47)) {
		t.Fatalf("session %+v", s)
	}
	if s.LessonID == nil || *s.LessonID != c.lesson {
		t.Fatal("not linked")
	}
	// meeting.started arriving late keeps its own (authoritative) start.
	c.send(c.m.started(hms(9, 0, 3)))
	if s := c.session(mainUUID); !s.StartedAt.Equal(hms(9, 0, 3)) {
		t.Fatalf("started_at %v", tstr(s.StartedAt))
	}
	// A participant payload never sets started_at.
	m2 := newMeeting(testMeeting, "uuid-2", hm(9, 0))
	c.send(m2.joined(dev("Иванов Иван", "a"), hm(9, 40)))
	if s := c.session("uuid-2"); s.StartedAt != nil || !s.FirstEventAt.Equal(hm(9, 40)) {
		t.Fatalf("session %+v", s)
	}
}

func TestURLValidationStoredAsIgnored(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	raw := e.deliver(readTestdata(t, "url_validation.json"))
	if n := e.process(); n != 0 {
		t.Fatalf("worker handled %d events, want 0", n)
	}
	ev := e.event(raw.ID)
	if ev.EventType != "endpoint.url_validation" || ev.ProcessingStatus != model.EventIgnored || ev.Error == "" {
		t.Fatalf("event %+v", ev)
	}
	if ev.Payload != string(readTestdata(t, "url_validation.json")) {
		t.Fatal("payload not stored byte-for-byte")
	}
}

func TestRosterSync(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	petrov, sidorova := c.sid("Петров Пётр"), c.sid("Сидорова Анна")
	c.send(c.m.started(hm(9, 0)), c.m.joined(dev("Иванов Иван", "a"), hm(9, 1)))

	archive := func(id int64) {
		st, err := c.st.GetStudent(c.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		st.Status = model.StatusArchived
		if err := c.st.UpdateStudent(c.ctx, st); err != nil {
			t.Fatal(err)
		}
	}
	// Archived without intervals and without manual data: the row goes away.
	archive(petrov)
	// Archived with a teacher comment: the row stays.
	if _, err := c.st.SetAttendanceComment(c.ctx, c.lesson, sidorova, "болеет", nil, c.clock.Now()); err != nil {
		t.Fatal(err)
	}
	archive(sidorova)
	// A student added during the lesson gets a row.
	newcomer := c.student(c.g.id, "Новиков Николай")
	c.rebuild(c.lesson)
	rows := c.attendance(c.lesson)
	if _, ok := rows[petrov]; ok {
		t.Error("archived student without data kept a row")
	}
	if _, ok := rows[sidorova]; !ok {
		t.Error("archived student with a comment lost the row")
	}
	if _, ok := rows[newcomer]; !ok {
		t.Error("new active student has no row")
	}

	// After the lesson finished the roster is frozen ...
	c.send(c.m.ended(hm(10, 30)))
	late := c.student(c.g.id, "Орлова Ольга")
	c.rebuild(c.lesson)
	if _, ok := c.attendance(c.lesson)[late]; ok {
		t.Error("a student added after the lesson appeared in it")
	}
	// ... except for students who get time through a manual mapping.
	c.clock.Set(hm(11, 0))
	if err := c.st.UpsertMapping(c.ctx, &model.Mapping{LessonID: c.lesson, NormalizedName: "иванов иван",
		Action: model.MappingStudent, StudentID: &late}); err != nil {
		t.Fatal(err)
	}
	c.rebuild(c.lesson)
	r, ok := c.attendance(c.lesson)[late]
	if !ok || r.TotalDurationSeconds != 89*60 {
		t.Fatalf("mapped student after finish: ok=%v %+v", ok, r)
	}
	if r := c.row(c.lesson, c.sid("Иванов Иван")); r.CurrentStatus != model.CurrentNotSeen {
		t.Fatalf("time stayed with the old student: %+v", r)
	}
}

func TestLogsCarryContextButNoPayload(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	c.send(c.m.started(hm(9, 0)), c.m.joined(dev("Иванов Иван", "a"), hm(9, 1)), c.m.joined(dev("Ваня", "v"), hm(9, 2)))
	logs := c.logs.String()
	for _, want := range []string{"event_type=meeting.participant_joined", "participant=\"Иванов Иван\"", "lesson_id=",
		`meeting_uuid="` + mainUUID + `"`, "resolution=matched", "resolution=unidentified", "status=online"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs miss %q", want)
		}
	}
	for _, leak := range []string{"public_ip", "203.0.113.17", "account_id", "Xq3vKc9mT0yLw2BnR5aZpQ", "payload"} {
		if strings.Contains(logs, leak) {
			t.Errorf("logs contain payload data %q", leak)
		}
	}
}
