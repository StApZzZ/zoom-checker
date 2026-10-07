package processor

import (
	"fmt"
	"testing"
	"time"
)

// BenchmarkRebuildLesson measures one projection rebuild of a 30-student lesson
// with 600 participant events (20 visits per student).
func BenchmarkRebuildLesson(b *testing.B) {
	e := newEnv(b)
	names := make([]string, 30)
	for i := range names {
		names[i] = fmt.Sprintf("Студент Номер%02d", i)
	}
	g := e.group("10А", names...)
	lessonID := e.lesson(g.id, testMeeting, hm(9, 0), hm(10, 30))
	m := newMeeting(testMeeting, mainUUID, hm(9, 0))
	for v := range 10 {
		for i, n := range names {
			d := dev(n, fmt.Sprintf("%d-%d", i, v))
			e.deliver(m.joined(d, hm(9, 0).Add(time.Duration(v*8)*time.Minute)))
			e.deliver(m.left(d, hm(9, 0).Add(time.Duration(v*8+6)*time.Minute)))
		}
	}
	e.process()
	b.ResetTimer()
	for b.Loop() {
		if err := e.p.RebuildLesson(e.ctx, lessonID); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkProcessLargeLesson measures draining a backlog of 2,000 participant
// events of one lesson (100 students, 10 visits each): the worker rebuilds the
// lesson after every batch it commits, so the cost of a burst depends on how
// many rebuilds the backlog causes.
func BenchmarkProcessLargeLesson(b *testing.B) {
	for b.Loop() {
		b.StopTimer()
		e := newEnv(b)
		names := make([]string, 100)
		for i := range names {
			names[i] = fmt.Sprintf("Студент Номер%03d", i)
		}
		g := e.group("10А", names...)
		e.lesson(g.id, testMeeting, hm(9, 0), hm(10, 30))
		m := newMeeting(testMeeting, mainUUID, hm(9, 0))
		e.deliver(m.started(hm(9, 0)))
		for v := range 10 {
			for i, n := range names {
				d := dev(n, fmt.Sprintf("%d-%d", i, v))
				e.deliver(m.joined(d, hm(9, 0).Add(time.Duration(v*8)*time.Minute)))
				e.deliver(m.left(d, hm(9, 0).Add(time.Duration(v*8+6)*time.Minute)))
			}
		}
		b.StartTimer()
		if n := e.process(); n != 2001 {
			b.Fatalf("processed %d events", n)
		}
	}
}
