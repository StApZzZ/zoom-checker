package httpapi

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"zoom-checker/internal/model"
)

// A lesson created twice by mistake: the Zoom session went to the lower id,
// and the teacher deletes that copy after the meeting ended. No Zoom event
// will come any more, so the delete itself must hand the session (and its
// attendance) to the remaining lesson.
func TestLessonDeleteMovesSessionsToRemainingLesson(t *testing.T) {
	t.Parallel()
	e, z := newLTRealEnv(t)
	g := e.group("DevOps-2")
	ivan := e.student(g.ID, "Иванов Иван", "").ID
	e.clock.Set(ltAt("17:00"))
	first := e.lesson(g.ID, "Linux 1", ltAt("18:00"))
	twin := e.lesson(g.ID, "Linux 1", ltAt("18:00"))
	nextWeek := e.lesson(g.ID, "Linux 2", ltAt("18:00").Add(7*24*time.Hour))

	z.send(z.started(ltAt("18:00")), z.joined("Иванов Иван", "a", ltAt("18:01")),
		z.left("Иванов Иван", "a", ltAt("19:01")), z.ended(ltAt("19:30")))
	if ms, err := e.st.ListZoomMeetingsByLesson(e.ctx, first.ID); err != nil || len(ms) != 1 {
		t.Fatalf("session of the first copy: %v %v", ms, err)
	}

	e.clock.Set(ltAt("20:00"))
	e.ok(e.do("DELETE", fmt.Sprintf("/api/lessons/%d", first.ID), nil), http.StatusNoContent, nil)

	var v LessonViewDTO
	e.ok(e.do("GET", fmt.Sprintf("/api/lessons/%d/attendance", twin.ID), nil), http.StatusOK, &v)
	if v.Lesson.Status != model.LessonFinished || len(v.Lesson.Sessions) != 1 || v.Lesson.Sessions[0].LinkMode != model.LinkAuto {
		t.Fatalf("remaining lesson = %+v", v.Lesson)
	}
	if r := ltRow(t, v, ivan); r.TotalSeconds != 60*60 || r.FinalStatus != model.FinalPresent {
		t.Fatalf("attendance moved with the session: %+v", r)
	}
	if ms, err := e.st.ListZoomMeetingsByLesson(e.ctx, nextWeek.ID); err != nil || len(ms) != 0 {
		t.Fatalf("next week's lesson must not take the session: %v %v", ms, err)
	}

	// Deleting the last copy leaves the session unlinked (shown on the Zoom page).
	e.ok(e.do("DELETE", fmt.Sprintf("/api/lessons/%d", twin.ID), nil), http.StatusNoContent, nil)
	var sessions []ZoomSessionDTO
	e.ok(e.do("GET", "/api/zoom/sessions?unlinked=1", nil), http.StatusOK, &sessions)
	if len(sessions) != 1 || sessions[0].LessonID != nil || sessions[0].Lesson != nil {
		t.Fatalf("unlinked sessions = %+v", sessions)
	}
}

// A session the teacher linked by hand to a lesson of another day goes back
// to the lesson it fits when that lesson is deleted: its own time is probed,
// not only the deleted lesson's neighbourhood.
func TestLessonDeleteRelinksHandLinkedSession(t *testing.T) {
	t.Parallel()
	e, z := newLTRealEnv(t)
	g := e.group("DevOps-5")
	ivan := e.student(g.ID, "Иванов Иван", "").ID
	e.clock.Set(ltAt("17:00"))
	monday := e.lesson(g.ID, "Linux 1", ltAt("18:00"))
	thursday := e.lesson(g.ID, "Linux 2", ltAt("18:00").Add(3*24*time.Hour))

	z.send(z.started(ltAt("18:00")), z.joined("Иванов Иван", "a", ltAt("18:01")),
		z.left("Иванов Иван", "a", ltAt("19:01")), z.ended(ltAt("19:30")))
	ms, err := e.st.ListZoomMeetingsByLesson(e.ctx, monday.ID)
	if err != nil || len(ms) != 1 {
		t.Fatalf("session of monday: %v %v", ms, err)
	}
	e.ok(e.do("POST", fmt.Sprintf("/api/zoom/sessions/%d/link", ms[0].ID), map[string]any{"lesson_id": thursday.ID}),
		http.StatusOK, nil)
	if moved, err := e.st.ListZoomMeetingsByLesson(e.ctx, thursday.ID); err != nil || len(moved) != 1 || moved[0].LinkMode != model.LinkManual {
		t.Fatalf("session linked by hand: %v %v", moved, err)
	}

	e.ok(e.do("DELETE", fmt.Sprintf("/api/lessons/%d", thursday.ID), nil), http.StatusNoContent, nil)
	var v LessonViewDTO
	e.ok(e.do("GET", fmt.Sprintf("/api/lessons/%d/attendance", monday.ID), nil), http.StatusOK, &v)
	if len(v.Lesson.Sessions) != 1 || v.Lesson.Sessions[0].ID != ms[0].ID || v.Lesson.Sessions[0].LinkMode != model.LinkAuto {
		t.Fatalf("monday after the delete = %+v", v.Lesson)
	}
	if r := ltRow(t, v, ivan); r.TotalSeconds != 60*60 || r.FinalStatus != model.FinalPresent {
		t.Fatalf("attendance came back with the session: %+v", r)
	}
}
