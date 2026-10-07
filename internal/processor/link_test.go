package processor

import (
	"strings"
	"testing"
	"time"

	"zoom-checker/internal/model"
	"zoom-checker/internal/realtime"
	"zoom-checker/internal/store"
)

// Linking side effects (docs §5.5): automatic reopen audit, realtime pokes,
// sessions of a deleted lesson.

// TestAutomaticReopenAudit: the audit entry of an automatic reopen reads like a
// teacher's reopen, with times in the configured time zone.
func TestAutomaticReopenAudit(t *testing.T) {
	t.Parallel()
	msk, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	for _, tc := range []struct {
		name     string
		loc      *time.Location
		previous string
		reason   string
	}{
		{"configured time zone", msk, "Завершено вручную: 06.10.2026 12:31 (UTC+3)", "(06.10.2026 12:35 (UTC+3))"},
		{"default UTC", nil, "Завершено вручную: 06.10.2026 09:31 (UTC)", "(06.10.2026 09:35 (UTC))"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			e.p = New(e.st, e.broker, nil, WithClock(e.clock.Now), WithLocation(tc.loc))
			g := e.group("10А", "Иванов Иван")
			lessonID := e.lesson(g.id, testMeeting, hm(9, 0), hm(10, 30))
			m1 := newMeeting(testMeeting, "uuid-first", hm(9, 0))
			e.send(m1.started(hm(9, 0)), m1.ended(hm(9, 30)))
			if err := e.st.SetLessonManualFinished(e.ctx, lessonID, ptrTime(hm(9, 31))); err != nil {
				t.Fatal(err)
			}
			m2 := newMeeting(testMeeting, "uuid-second", hm(9, 35))
			e.send(m2.started(hm(9, 35)))

			audit, err := e.st.ListAudit(e.ctx, store.AuditFilter{LessonID: &lessonID})
			if err != nil {
				t.Fatal(err)
			}
			if len(audit) != 1 {
				t.Fatalf("audit %+v", audit)
			}
			a := audit[0]
			if a.Action != model.AuditLessonReopen || a.UserName != "Система" || a.UserID != nil ||
				a.PreviousValue != tc.previous || a.NewValue != "Занятие возобновлено автоматически" ||
				!strings.Contains(a.Reason, tc.reason) || a.EntityType != "lesson" || a.GroupID == nil || *a.GroupID != g.id {
				t.Fatalf("audit entry %+v", a)
			}
		})
	}
}

// TestUnlinkedMeetingDoesNotPokeDashboards: events of a Zoom meeting that is no
// lesson (account-wide webhooks) change no lesson list, so nothing is published;
// events of a lesson's meeting publish the lesson and the lists.
func TestUnlinkedMeetingDoesNotPokeDashboards(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	lists, cancelLists := c.broker.Subscribe(realtime.TopicLessons)
	defer cancelLists()
	lesson, cancelLesson := c.broker.Subscribe(realtime.LessonTopic(c.lesson))
	defer cancelLesson()

	other := newMeeting("11122233344", "uuid-other", hm(9, 0))
	c.send(other.started(hm(9, 0)), other.joined(dev("Кто-то", "x"), hm(9, 1)), other.left(dev("Кто-то", "x"), hm(9, 2)))
	if s := c.session("uuid-other"); s.LessonID != nil {
		t.Fatalf("unrelated session linked: %+v", s)
	}
	select {
	case <-lists:
		t.Fatal("events of an unrelated meeting poked the lesson lists")
	default:
	}

	c.send(c.m.joined(dev("Иванов Иван", "a"), hm(9, 3)))
	for name, ch := range map[string]<-chan struct{}{"lessons": lists, "lesson": lesson} {
		select {
		case <-ch:
		default:
			t.Fatalf("topic %s not published for the lesson's meeting", name)
		}
	}
}

// TestDeletedLessonSessionsMoveToRemainingLesson: a lesson created twice by
// mistake; the session went to the lower id, which the teacher deletes after the
// meeting ended. store.DeleteLesson detaches the session (auto mode) and no Zoom
// event will come any more; RelinkLesson on the remaining lesson with the same
// meeting id (what the delete handler should do) attaches it there.
func TestDeletedLessonSessionsMoveToRemainingLesson(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	g := e.group("10А", "Иванов Иван")
	first := e.lesson(g.id, testMeeting, hm(9, 0), hm(10, 30))
	twin := e.lesson(g.id, testMeeting, hm(9, 0), hm(10, 30))
	m := newMeeting(testMeeting, mainUUID, hm(9, 0))
	d := dev("Иванов Иван", "a")
	e.send(m.started(hm(9, 0)), m.joined(d, hm(9, 1)), m.left(d, hm(10, 1)), m.ended(hm(10, 30)))
	if s := e.session(mainUUID); s.LessonID == nil || *s.LessonID != first {
		t.Fatalf("session %+v, want lesson %d", s, first)
	}

	if err := e.st.DeleteLesson(e.ctx, first); err != nil {
		t.Fatal(err)
	}
	settings, err := e.st.GetSettings(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	from, to := NearbyRange(settings, hm(9, 0), hm(10, 30))
	near, err := e.st.FindLessonsByMeeting(e.ctx, testMeeting, from, to)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range near {
		if err := e.p.RelinkLesson(e.ctx, l.ID); err != nil {
			t.Fatal(err)
		}
	}
	if s := e.session(mainUUID); s.LessonID == nil || *s.LessonID != twin {
		t.Fatalf("session %+v, want the remaining lesson %d", s, twin)
	}
	if r := e.row(twin, g.students["Иванов Иван"]); r.TotalDurationSeconds != 60*60 {
		t.Fatalf("remaining lesson: %+v", r)
	}
	if l := e.getLesson(twin); l.Status != model.LessonFinished {
		t.Fatalf("remaining lesson %s, want finished", l.Status)
	}
}
