package processor

import (
	"strings"
	"testing"
	"time"

	"zoom-checker/internal/model"
	"zoom-checker/internal/store"
)

// With the minimum-percent rule a student who simply stays in Zoom becomes
// present once enough time has passed, although no Zoom event arrives.
func TestUpkeepRefreshesPercentStatus(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	c.settings(func(s *model.Settings) { s.PresentMinPercent = 50 })
	ivanov := c.sid("Иванов Иван")
	c.send(c.m.started(hm(9, 0)), c.m.joined(dev("Иванов Иван", "a"), hm(9, 0)))
	if r := c.row(c.lesson, ivanov); r.CurrentStatus != model.CurrentOnline || r.FinalStatus != model.FinalAbsent {
		t.Fatalf("at 9:00: %+v", r)
	}
	c.clock.Set(hm(9, 30)) // 30 of 90 min: not yet
	if err := c.p.upkeep(c.ctx); err != nil {
		t.Fatal(err)
	}
	if r := c.row(c.lesson, ivanov); r.FinalStatus != model.FinalAbsent {
		t.Fatalf("at 9:30: %+v", r)
	}
	c.clock.Set(hm(9, 50)) // 50 of 90 min
	if err := c.p.upkeep(c.ctx); err != nil {
		t.Fatal(err)
	}
	if r := c.row(c.lesson, ivanov); r.AutoStatus != model.FinalPresent || r.FinalStatus != model.FinalPresent {
		t.Fatalf("at 9:50 the stored status is still %+v", r)
	}
}

// Zoom's meeting.ended never arrived (and neither did a leave): the lesson is
// finished automatically once its open session has been silent for long
// enough, so the time of those still "in Zoom" stops growing.
func TestUpkeepFinishesOverdueLesson(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	ivanov, petrov := c.sid("Иванов Иван"), c.sid("Петров Пётр")
	c.send(c.m.started(hm(9, 0)), c.m.joined(dev("Иванов Иван", "a"), hm(9, 1)),
		c.m.joined(dev("Петров Пётр", "p"), hm(9, 1)), c.m.left(dev("Петров Пётр", "p"), hm(11, 0)))

	c.clock.Set(hm(16, 59)) // less than 6 h after the last Zoom event (11:00)
	if err := c.p.upkeep(c.ctx); err != nil {
		t.Fatal(err)
	}
	if l := c.getLesson(c.lesson); l.Status != model.LessonActive || l.ManualFinishedAt != nil {
		t.Fatalf("finished too early: %+v", l)
	}

	c.clock.Set(hm(17, 1))
	if err := c.p.upkeep(c.ctx); err != nil {
		t.Fatal(err)
	}
	l := c.getLesson(c.lesson)
	if l.Status != model.LessonFinished || l.ManualFinishedAt == nil || !l.ManualFinishedAt.Equal(hm(11, 0)) {
		t.Fatalf("lesson %s finished at %v, want finished at the last Zoom event 11:00", l.Status, tstr(l.ManualFinishedAt))
	}
	if r := c.row(c.lesson, ivanov); r.CurrentStatus != model.CurrentLeft || r.TotalDurationSeconds != 119*60 {
		t.Fatalf("Иванов: %+v, want left after 119 min", r)
	}
	if r := c.row(c.lesson, petrov); r.TotalDurationSeconds != 119*60 {
		t.Fatalf("Петров: %+v", r)
	}
	audit, err := c.st.ListAudit(c.ctx, store.AuditFilter{LessonID: &c.lesson})
	if err != nil {
		t.Fatal(err)
	}
	if len(audit) != 1 || audit[0].Action != model.AuditLessonFinish || audit[0].UserName != "Система" ||
		!strings.Contains(audit[0].NewValue, "автоматически") {
		t.Fatalf("audit = %+v", audit)
	}

	// A later meeting.ended changes nothing; a lesson that ended normally is
	// never touched.
	c.send(c.m.ended(hm(17, 30)))
	if r := c.row(c.lesson, ivanov); r.TotalDurationSeconds != 119*60 {
		t.Fatalf("after the late meeting.ended: %+v", r)
	}
	ended := newClass(t, hm(9, 0), hm(10, 30))
	ended.send(ended.m.started(hm(9, 0)), ended.m.ended(hm(10, 30)))
	ended.clock.Set(hm(9, 0).Add(24 * time.Hour))
	if err := ended.p.upkeep(ended.ctx); err != nil {
		t.Fatal(err)
	}
	if l := ended.getLesson(ended.lesson); l.ManualFinishedAt != nil {
		t.Fatalf("a normally finished lesson was finished again: %+v", l)
	}
}
