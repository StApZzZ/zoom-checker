package processor

import (
	"testing"

	"zoom-checker/internal/model"
)

// Auto-link rules (docs/architecture.md §5.5): a personal meeting ID reused for
// other meetings must not touch finished or future lessons, and a restart stays
// with the lesson it continues.

// A teacher reuses one personal meeting ID: a staff meeting 2.5 h after the
// lesson ended must not reopen it, stretch its end or add strangers.
func TestPersonalMeetingReuseDoesNotReopenLesson(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(10, 0), hm(11, 30))
	m := c.m
	c.send(m.started(hm(10, 0)))
	for _, n := range []string{"Иванов Иван", "Петров Пётр", "Сидорова Анна"} {
		c.send(m.joined(dev(n, n), hm(10, 0)))
	}
	for _, n := range []string{"Иванов Иван", "Петров Пётр", "Сидорова Анна"} {
		c.send(m.left(dev(n, n), hm(11, 29)))
	}
	c.send(m.ended(hm(11, 30)))
	before := c.getLesson(c.lesson)
	if before.Status != model.LessonFinished {
		t.Fatalf("lesson %s, want finished", before.Status)
	}

	staff := newMeeting(testMeeting, "uuid-staff", hm(14, 0))
	z := dev("Завуч Мария", "z")
	c.send(staff.started(hm(14, 0)), staff.joined(z, hm(14, 0)), staff.left(z, hm(14, 30)), staff.ended(hm(14, 30)))

	if s := c.session("uuid-staff"); s.LessonID != nil {
		t.Fatalf("the staff meeting was linked to lesson %d", *s.LessonID)
	}
	after := c.getLesson(c.lesson)
	if after.Status != model.LessonFinished || !after.ActualEndAt.Equal(*before.ActualEndAt) {
		t.Fatalf("lesson changed: %s end %v, want finished at %v", after.Status, tstr(after.ActualEndAt), tstr(before.ActualEndAt))
	}
	if r := c.row(c.lesson, c.sid("Иванов Иван")); r.EarlyLeaveSeconds == nil || *r.EarlyLeaveSeconds != 60 {
		t.Fatalf("early leave = %v, want 60 s", ip(r.EarlyLeaveSeconds))
	}
	if _, ok := c.participants(c.lesson)["завуч мария"]; ok {
		t.Fatal("the staff meeting's participant appeared in the lesson")
	}
}

// A meeting hours before a lesson on the same ID is not that lesson, and a
// short meeting that ended before the lesson began does not finish it.
func TestMeetingBeforeLessonDoesNotFinishIt(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(18, 0), hm(19, 30))
	early := newMeeting(testMeeting, "uuid-14", hm(14, 0))
	d := dev("Иванов Иван", "a")
	c.send(early.started(hm(14, 0)), early.joined(d, hm(14, 0)), early.left(d, hm(14, 20)), early.ended(hm(14, 20)))
	if s := c.session("uuid-14"); s.LessonID != nil {
		t.Fatalf("a meeting 4 h before the lesson was linked to %d", *s.LessonID)
	}

	// 17:10–17:30: inside the 1 h before the start, so it links at first ...
	short := newMeeting(testMeeting, "uuid-short", hm(17, 10))
	c.send(short.started(hm(17, 10)), short.joined(d, hm(17, 10)))
	if s := c.session("uuid-short"); s.LessonID == nil || *s.LessonID != c.lesson {
		t.Fatalf("the meeting opened 50 min early was not linked: %+v", s)
	}
	if l := c.getLesson(c.lesson); l.Status != model.LessonActive {
		t.Fatalf("lesson %s, want active while the early meeting runs", l.Status)
	}
	// ... but it ended 30 min before the lesson: another meeting, unlinked.
	c.send(short.left(d, hm(17, 30)), short.ended(hm(17, 30)))
	if s := c.session("uuid-short"); s.LessonID != nil || s.LinkMode != model.LinkAuto {
		t.Fatalf("session %+v, want unlinked (auto)", s)
	}
	if l := c.getLesson(c.lesson); l.Status != model.LessonPlanned {
		t.Fatalf("lesson %s, want planned", l.Status)
	}
	if r := c.row(c.lesson, c.sid("Иванов Иван")); r.CurrentStatus != model.CurrentNotSeen {
		t.Fatalf("Иванов %+v, want not seen", r)
	}
	// Editing the lesson does not claim it back.
	if err := c.p.RelinkLesson(c.ctx, c.lesson); err != nil {
		t.Fatal(err)
	}
	if s := c.session("uuid-short"); s.LessonID != nil {
		t.Fatalf("relink claimed the early meeting: %+v", s)
	}

	// A meeting opened early that runs into the lesson stays.
	real := newMeeting(testMeeting, "uuid-real", hm(17, 50))
	c.send(real.started(hm(17, 50)), real.joined(d, hm(17, 55)), real.ended(hm(19, 30)))
	if s := c.session("uuid-real"); s.LessonID == nil || *s.LessonID != c.lesson {
		t.Fatalf("the real meeting is %+v", s)
	}
}

// Two lessons on one meeting ID: an accidental end and restart in the second
// half of the first lesson stays with it, not with the next lesson.
func TestRestartStaysWithRunningLesson(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	g := e.group("10А", "Иванов Иван", "Петров Пётр")
	a := e.lesson(g.id, testMeeting, hm(10, 0), hm(11, 30))
	b := e.lesson(g.id, testMeeting, hm(12, 0), hm(13, 30))
	iv, pe := dev("Иванов Иван", "i"), dev("Петров Пётр", "p")

	u1 := newMeeting(testMeeting, "uuid-1", hm(10, 0))
	e.send(u1.started(hm(10, 0)), u1.joined(iv, hm(10, 0)), u1.joined(pe, hm(10, 0)), u1.ended(hm(11, 5)))
	u2 := newMeeting(testMeeting, "uuid-2", hm(11, 6))
	e.send(u2.started(hm(11, 6)), u2.joined(iv, hm(11, 6)), u2.joined(pe, hm(11, 6)),
		u2.left(iv, hm(11, 30)), u2.left(pe, hm(11, 30)), u2.ended(hm(11, 31)))
	u3 := newMeeting(testMeeting, "uuid-3", hm(12, 0))
	e.send(u3.started(hm(12, 0)), u3.joined(iv, hm(12, 0)), u3.left(iv, hm(13, 30)), u3.ended(hm(13, 30)))

	for uuid, want := range map[string]int64{"uuid-1": a, "uuid-2": a, "uuid-3": b} {
		if s := e.session(uuid); s.LessonID == nil || *s.LessonID != want {
			t.Errorf("%s linked to %v, want %d", uuid, ip(s.LessonID), want)
		}
	}
	if r := e.row(a, g.students["Иванов Иван"]); r.TotalDurationSeconds != 89*60 {
		t.Errorf("lesson A: Иванов %d s, want 89 min", r.TotalDurationSeconds)
	}
	if r := e.row(b, g.students["Петров Пётр"]); r.CurrentStatus != model.CurrentNotSeen || r.FinalStatus != model.FinalAbsent {
		t.Errorf("lesson B: Петров %+v, want not seen / absent", r)
	}
}

// A 10-minute break between two lessons on one meeting ID: a meeting opened
// in the break belongs to the next lesson, while a restart right after the
// first lesson's overrunning meeting ended continues that lesson.
func TestBreakBetweenLessons(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		firstEnd  [2]int // when the first lesson's meeting ended
		nextStart [2]int // when the next session started
		wantFirst bool
	}{
		{"meeting opened in the break", [2]int{11, 30}, [2]int{11, 33}, false},
		{"first meeting ended early, opened again in the break", [2]int{11, 25}, [2]int{11, 33}, false},
		{"restart right after an overrun", [2]int{11, 35}, [2]int{11, 36}, true},
		{"overrun, the next lesson already started", [2]int{11, 35}, [2]int{11, 41}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			g := e.group("10А", "Иванов Иван")
			a := e.lesson(g.id, testMeeting, hm(10, 0), hm(11, 30))
			b := e.lesson(g.id, testMeeting, hm(11, 40), hm(13, 10))
			u1 := newMeeting(testMeeting, "uuid-1", hm(10, 0))
			e.send(u1.started(hm(10, 0)), u1.ended(hm(tc.firstEnd[0], tc.firstEnd[1])))
			start := hm(tc.nextStart[0], tc.nextStart[1])
			u2 := newMeeting(testMeeting, "uuid-2", start)
			e.send(u2.started(start))
			want := b
			if tc.wantFirst {
				want = a
			}
			if s := e.session("uuid-2"); s.LessonID == nil || *s.LessonID != want {
				t.Fatalf("linked to %v, want %d (first %d, next %d)", ip(s.LessonID), want, a, b)
			}
		})
	}
}
