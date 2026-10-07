package processor

import (
	"testing"

	"zoom-checker/internal/model"
)

// Roster sync and history (docs §5.6, TZ §17): what a rebuild keeps when the
// group changes after students attended a lesson.

func (e *env) archiveStudent(id int64) {
	e.t.Helper()
	st, err := e.st.GetStudent(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	st.Status = model.StatusArchived
	if err := e.st.UpdateStudent(e.ctx, st); err != nil {
		e.t.Fatal(err)
	}
}

// TestArchivedStudentKeepsHistory: a student who attended a lesson and was
// archived afterwards (left the group) keeps that lesson's attendance when the
// lesson is rebuilt later (settings change, late Zoom retry, "Пересчитать",
// a manual mapping) — docs §5.6 "so archived students keep history".
func TestArchivedStudentKeepsHistory(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	ivanov := c.sid("Иванов Иван")
	d := dev("Иванов Иван", "a")
	c.send(c.m.started(hm(9, 0)), c.m.joined(d, hm(9, 1)), c.m.left(d, hm(10, 1)), c.m.ended(hm(10, 30)))
	before := c.row(c.lesson, ivanov)
	if before.TotalDurationSeconds != 60*60 || before.FinalStatus != model.FinalPresent {
		t.Fatalf("before archiving: %+v", before)
	}

	c.clock.Set(hm(12, 0))
	c.archiveStudent(ivanov)
	c.settings(func(s *model.Settings) { s.PresentMinPercent = 50 }) // what PUT /api/settings does next: rebuild
	c.rebuild(c.lesson)

	after := c.row(c.lesson, ivanov)
	if after.TotalDurationSeconds != 60*60 || after.CurrentStatus != model.CurrentLeft || after.FinalStatus != model.FinalPresent {
		t.Fatalf("archived student lost the lesson history: %+v", after)
	}
	p := c.participant(c.lesson, "Иванов Иван")
	if p.Resolution != model.ResolutionMatched || p.StudentID == nil || *p.StudentID != ivanov {
		t.Fatalf("participant %+v, want still matched to the archived student", p)
	}
}

// TestArchivedDuringLesson: a student archived while the lesson runs keeps the
// time already spent (the row has intervals, §5.6 roster sync), while an
// archived student who never joined loses the row.
func TestArchivedDuringLesson(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	ivanov, petrov := c.sid("Иванов Иван"), c.sid("Петров Пётр")
	c.send(c.m.started(hm(9, 0)), c.m.joined(dev("Иванов Иван", "a"), hm(9, 1)))
	c.clock.Set(hm(9, 31))
	c.archiveStudent(ivanov)
	c.archiveStudent(petrov)
	c.rebuild(c.lesson)

	rows := c.attendance(c.lesson)
	if r, ok := rows[ivanov]; !ok || r.CurrentStatus != model.CurrentOnline || r.TotalDurationSeconds != 30*60 {
		t.Fatalf("archived student who is in Zoom: ok=%v %+v", ok, r)
	}
	if _, ok := rows[petrov]; ok {
		t.Fatal("archived student who never joined kept a row")
	}
}

// TestArchivedNameReusedByActiveStudent: when an archived student and a current
// student of the group share a name, the current student gets the Zoom name
// (no conflict), and the archived one keeps the names nobody else uses.
func TestArchivedNameReusedByActiveStudent(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	old := c.sid("Иванов Иван")
	if err := c.st.AddAlias(c.ctx, &model.Alias{StudentID: old, GroupID: c.g.id, Alias: "Ваня И"}); err != nil {
		t.Fatal(err)
	}
	c.send(c.m.started(hm(9, 0)), c.m.joined(dev("Ваня И", "v"), hm(9, 2)))
	c.clock.Set(hm(9, 10))
	c.archiveStudent(old)
	newcomer := c.student(c.g.id, "Иванов Иван") // a different person with the same name
	c.send(c.m.joined(dev("Иванов Иван", "a"), hm(9, 20)))

	if p := c.participant(c.lesson, "Иванов Иван"); p.Resolution != model.ResolutionMatched || p.StudentID == nil || *p.StudentID != newcomer {
		t.Fatalf("shared name: %+v, want matched to the current student %d", p, newcomer)
	}
	if p := c.participant(c.lesson, "Ваня И"); p.Resolution != model.ResolutionMatched || p.StudentID == nil || *p.StudentID != old {
		t.Fatalf("alias of the archived student: %+v, want matched to %d", p, old)
	}
	if r := c.row(c.lesson, old); r.CurrentStatus != model.CurrentOnline {
		t.Fatalf("archived student: %+v", r)
	}
}

// TestFinishedLessonKeepsMatchesAfterRosterEdits: TZ §17 fixes the attendance of
// a finished lesson. Editing the group afterwards (a student's name corrected, an
// alias removed) must not erase time that was matched automatically when a later
// rebuild runs (settings change, "Пересчитать"); new names still add matches. In
// a lesson that is not finished the current names apply.
func TestFinishedLessonKeepsMatchesAfterRosterEdits(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	ivanov, petrov, sidorova := c.sid("Иванов Иван"), c.sid("Петров Пётр"), c.sid("Сидорова Анна")
	if err := c.st.AddAlias(c.ctx, &model.Alias{StudentID: petrov, GroupID: c.g.id, Alias: "Петя"}); err != nil {
		t.Fatal(err)
	}
	c.send(c.m.started(hm(9, 0)),
		c.m.joined(dev("Иванов Иван", "a"), hm(9, 1)), c.m.left(dev("Иванов Иван", "a"), hm(10, 1)),
		c.m.joined(dev("Петя", "p"), hm(9, 2)), c.m.left(dev("Петя", "p"), hm(9, 32)),
		c.m.joined(dev("Аня", "s"), hm(9, 3)), c.m.left(dev("Аня", "s"), hm(9, 13)),
		c.m.ended(hm(10, 30)))
	if c.row(c.lesson, ivanov).TotalDurationSeconds != 60*60 || c.row(c.lesson, petrov).TotalDurationSeconds != 30*60 {
		t.Fatal("setup: matches missing")
	}

	c.clock.Set(hm(12, 0))
	st, err := c.st.GetStudent(c.ctx, ivanov)
	if err != nil {
		t.Fatal(err)
	}
	st.FullName = "Иванов Иван Иванович" // the teacher adds the patronymic
	if err := c.st.UpdateStudent(c.ctx, st); err != nil {
		t.Fatal(err)
	}
	p, err := c.st.GetStudent(c.ctx, petrov)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.st.DeleteAlias(c.ctx, petrov, p.Aliases[0].ID); err != nil {
		t.Fatal(err)
	}
	// A new alias still applies to the finished lesson.
	if err := c.st.AddAlias(c.ctx, &model.Alias{StudentID: sidorova, GroupID: c.g.id, Alias: "Аня"}); err != nil {
		t.Fatal(err)
	}
	c.settings(func(s *model.Settings) { s.PresentMinPercent = 10 })
	c.rebuild(c.lesson)

	for _, tc := range []struct {
		name    string
		student int64
		zoom    string
		total   int64
	}{
		{"renamed student", ivanov, "Иванов Иван", 60 * 60},
		{"removed alias", petrov, "Петя", 30 * 60},
		{"new alias", sidorova, "Аня", 10 * 60},
	} {
		if r := c.row(c.lesson, tc.student); r.TotalDurationSeconds != tc.total || r.FinalStatus != model.FinalPresent {
			t.Errorf("%s: %+v, want %d s present", tc.name, r, tc.total)
		}
		if p := c.participant(c.lesson, tc.zoom); p.Resolution != model.ResolutionMatched || p.StudentID == nil || *p.StudentID != tc.student {
			t.Errorf("%s: participant %+v", tc.name, p)
		}
	}

	// The teacher's own decision in that lesson still wins over the kept match.
	if err := c.st.UpsertMapping(c.ctx, &model.Mapping{LessonID: c.lesson, NormalizedName: "петя", Action: model.MappingNone}); err != nil {
		t.Fatal(err)
	}
	c.rebuild(c.lesson)
	if r := c.row(c.lesson, petrov); r.TotalDurationSeconds != 0 {
		t.Fatalf("manual 'not this student' ignored: %+v", r)
	}
	if err := c.st.DeleteMapping(c.ctx, c.lesson, "петя"); err != nil {
		t.Fatal(err)
	}
	c.rebuild(c.lesson)
	if r := c.row(c.lesson, petrov); r.TotalDurationSeconds != 0 {
		t.Fatalf("a match the teacher undid came back: %+v", r)
	}

	// Reopened (not finished any more): the current names apply.
	if err := c.st.SetLessonManualFinished(c.ctx, c.lesson, nil); err != nil {
		t.Fatal(err)
	}
	c.send(newMeeting(testMeeting, "uuid-restart", hm(11, 0)).started(hm(11, 0)))
	if l := c.getLesson(c.lesson); l.Status != model.LessonActive {
		t.Fatalf("lesson %s, want active after the restart", l.Status)
	}
	if r := c.row(c.lesson, ivanov); r.TotalDurationSeconds != 0 {
		t.Fatalf("running lesson kept an outdated name: %+v", r)
	}
}

// TestIgnoreListDoesNotHideGroupAlias: a teacher "always hides" «Александр» in
// one group; in another group a student uses that name as an alias. The global
// ignore list must not hide the student there (§5.4 rule 2: group names and
// aliases are checked before the ignore list), while the first group still
// hides the name.
func TestIgnoreListDoesNotHideGroupAlias(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.group("10А", "Иванов Иван")
	b := e.group("11Б", "Александров Александр")
	alex := b.students["Александров Александр"]
	if err := e.st.AddAlias(e.ctx, &model.Alias{StudentID: alex, GroupID: b.id, Alias: "Александр"}); err != nil {
		t.Fatal(err)
	}
	e.settings(func(s *model.Settings) { s.IgnoredNames = []string{"Александр"} })
	const meetingA, meetingB = "85746065432", "91122233344"
	lessonA := e.lesson(a.id, meetingA, hm(9, 0), hm(10, 30))
	lessonB := e.lesson(b.id, meetingB, hm(9, 0), hm(10, 30))

	ma := newMeeting(meetingA, "uuid-a==", hm(9, 0))
	mb := newMeeting(meetingB, "uuid-b==", hm(9, 0))
	e.send(ma.started(hm(9, 0)), mb.started(hm(9, 0)),
		ma.joined(dev("Александр", "a1"), hm(9, 1)), mb.joined(dev("александр ", "b1"), hm(9, 2)))

	if p := e.participant(lessonA, "Александр"); p.Resolution != model.ResolutionIgnored || p.IgnoreReason != model.IgnoreList {
		t.Errorf("group without that name: %+v, want ignored by the list", p)
	}
	p := e.participant(lessonB, "александр ")
	if p.Resolution != model.ResolutionMatched || p.StudentID == nil || *p.StudentID != alex {
		t.Fatalf("group where it is an alias: %+v, want matched to %d", p, alex)
	}
	if r := e.row(lessonB, alex); r.CurrentStatus != model.CurrentOnline {
		t.Errorf("student row %+v, want online", r)
	}
}

// TestHeldLessonSurvivesTemporaryUnlink: a finished lesson's session is
// unlinked for a moment — the teacher pastes a wrong Zoom link and fixes it, or
// unlinks and relinks it on the Zoom sessions page. The lesson's roster must
// stay as it was: the archived student keeps the row, a student added later
// gets none, and kept matches survive.
func TestHeldLessonSurvivesTemporaryUnlink(t *testing.T) {
	t.Parallel()
	for _, how := range []string{"meeting id edited", "session unlinked by hand"} {
		t.Run(how, func(t *testing.T) {
			t.Parallel()
			c := newClass(t, hm(9, 0), hm(10, 30))
			ivanov, petrov := c.sid("Иванов Иван"), c.sid("Петров Пётр")
			c.send(c.m.started(hm(9, 0)),
				c.m.joined(dev("Иванов Иван", "a"), hm(9, 0)), c.m.joined(dev("Петров Пётр", "p"), hm(9, 0)),
				c.m.left(dev("Иванов Иван", "a"), hm(10, 30)), c.m.left(dev("Петров Пётр", "p"), hm(10, 30)),
				c.m.ended(hm(10, 30)))

			c.clock.Set(hm(12, 0))
			c.archiveStudent(petrov)
			// Иванов is renamed: only the kept match ties «Иванов Иван» to him now.
			st, err := c.st.GetStudent(c.ctx, ivanov)
			if err != nil {
				t.Fatal(err)
			}
			st.FullName = "Иванов Иван Петрович"
			if err := c.st.UpdateStudent(c.ctx, st); err != nil {
				t.Fatal(err)
			}
			sidorov := c.student(c.g.id, "Сидоров Сидор")
			c.rebuild(c.lesson)

			switch how {
			case "meeting id edited":
				l := c.getLesson(c.lesson)
				l.ZoomMeetingID = "11122233344"
				if err := c.st.UpdateLesson(c.ctx, l); err != nil {
					t.Fatal(err)
				}
				if err := c.p.RelinkLesson(c.ctx, c.lesson); err != nil {
					t.Fatal(err)
				}
				if s := c.session(mainUUID); s.LessonID != nil {
					t.Fatalf("setup: session still linked: %+v", s)
				}
				l.ZoomMeetingID = testMeeting
				if err := c.st.UpdateLesson(c.ctx, l); err != nil {
					t.Fatal(err)
				}
				if err := c.p.RelinkLesson(c.ctx, c.lesson); err != nil {
					t.Fatal(err)
				}
			default:
				s := c.session(mainUUID)
				if err := c.p.LinkSession(c.ctx, s.ID, nil); err != nil {
					t.Fatal(err)
				}
				// While it is unlinked the past lesson keeps its roster too.
				if _, ok := c.attendance(c.lesson)[petrov]; !ok {
					t.Fatal("the archived student's row was dropped while the session was unlinked")
				}
				if _, ok := c.attendance(c.lesson)[sidorov]; ok {
					t.Fatal("a student added later got a row while the session was unlinked")
				}
				lessonID := c.lesson
				if err := c.p.LinkSession(c.ctx, s.ID, &lessonID); err != nil {
					t.Fatal(err)
				}
			}

			if s := c.session(mainUUID); s.LessonID == nil || *s.LessonID != c.lesson {
				t.Fatalf("session not linked back: %+v", s)
			}
			rows := c.attendance(c.lesson)
			if r, ok := rows[petrov]; !ok || r.TotalDurationSeconds != 90*60 || r.FinalStatus != model.FinalPresent {
				t.Fatalf("archived student: ok=%v %+v, want 90 min present", ok, r)
			}
			if r := rows[ivanov]; r.TotalDurationSeconds != 90*60 {
				t.Fatalf("renamed student lost the kept match: %+v", r)
			}
			if _, ok := rows[sidorov]; ok {
				t.Fatal("a student added after the lesson got a row in it")
			}
			for name, id := range map[string]int64{"Петров Пётр": petrov, "Иванов Иван": ivanov} {
				if p := c.participant(c.lesson, name); p.Resolution != model.ResolutionMatched || p.StudentID == nil || *p.StudentID != id {
					t.Fatalf("%s: %+v, want matched to %d", name, p, id)
				}
			}
		})
	}
}
