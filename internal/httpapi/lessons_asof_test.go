package httpapi

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"zoom-checker/internal/model"
)

// A system clock that steps back (NTP or VM time sync) must not make a new lesson
// view look older than the one a client already has: clients keep the newest view
// by as_of and would drop every snapshot until the clock caught up (seen in the
// browser e2e suite on WSL2: the live screen stayed on "Запланировано" after
// meeting.started).
func TestLessonViewAsOfNeverGoesBack(t *testing.T) {
	t.Parallel()
	e := newLTEnv(t)
	g := e.group("Часы")
	e.student(g.ID, "Иванов Иван", "")
	l := e.lesson(g.ID, "Занятие", ltAt("18:00"))
	asOf := func() time.Time {
		t.Helper()
		var v struct {
			AsOf time.Time `json:"as_of"`
		}
		e.ok(e.do(http.MethodGet, fmt.Sprintf("/api/lessons/%d/attendance", l.ID), nil), http.StatusOK, &v)
		return v.AsOf
	}

	e.clock.Set(ltAt("18:31").Add(500 * time.Millisecond))
	first := asOf()
	if want := ltAt("18:31").Add(500 * time.Millisecond); !first.Equal(want) {
		t.Fatalf("as_of = %v, want the clock %v", first, want)
	}

	e.clock.Set(ltAt("18:31").Add(-1500 * time.Millisecond)) // stepped back by 2 s
	if got := asOf(); got.Before(first) {
		t.Fatalf("as_of went back with the clock: %v after %v", got, first)
	}

	e.clock.Set(ltAt("18:32"))
	if got := asOf(); !got.Equal(ltAt("18:32")) {
		t.Fatalf("as_of = %v, want the clock again once it passed the last view (%v)", got, ltAt("18:32"))
	}
}

// A large backward step is a real clock correction: as_of follows the new time,
// so the durations of students still online are not inflated by the step.
func TestLessonViewAsOfFollowsLargeClockCorrection(t *testing.T) {
	t.Parallel()
	e := newLTEnv(t)
	g := e.group("Часы")
	iv := e.student(g.ID, "Иванов Иван", "").ID
	l := e.lesson(g.ID, "Занятие", ltAt("18:00"))
	e.project(l.ID, ltProjection{
		Participants: []model.Participant{{NormalizedName: "иванов иван", DisplayName: "Иванов Иван",
			Resolution: model.ResolutionMatched, StudentID: &iv, CurrentStatus: model.CurrentOnline,
			FirstJoinAt: ltPtr(ltAt("18:00")), Devices: 1}},
		Rows: []model.Attendance{{StudentID: iv, CurrentStatus: model.CurrentOnline, AutoStatus: model.FinalPresent,
			FirstJoinAt: ltPtr(ltAt("18:00"))}},
		Intervals: []ltIv{{Name: "Иванов Иван", UUID: "pu-1", Join: ltAt("18:00"), Student: iv}},
	})
	view := func() (time.Time, int64) {
		t.Helper()
		var v struct {
			AsOf     time.Time `json:"as_of"`
			Students []struct {
				TotalSeconds int64 `json:"total_seconds"`
			} `json:"students"`
		}
		e.ok(e.do(http.MethodGet, fmt.Sprintf("/api/lessons/%d/attendance", l.ID), nil), http.StatusOK, &v)
		if len(v.Students) != 1 {
			t.Fatalf("students = %+v", v.Students)
		}
		return v.AsOf, v.Students[0].TotalSeconds
	}

	tests := []struct {
		name      string
		clock     time.Time
		wantAsOf  time.Time
		wantTotal int64
	}{
		{"clock an hour ahead", ltAt("19:05"), ltAt("19:05"), 65 * 60},
		{"stepped back by 1 min: kept", ltAt("19:04"), ltAt("19:05"), 65 * 60},
		{"corrected by an hour: follows", ltAt("18:05"), ltAt("18:05"), 5 * 60},
		{"runs on from there", ltAt("18:06"), ltAt("18:06"), 6 * 60},
	}
	for _, tc := range tests { // sequential: each step depends on the previous one
		e.clock.Set(tc.clock)
		asOf, total := view()
		if !asOf.Equal(tc.wantAsOf) || total != tc.wantTotal {
			t.Fatalf("%s: as_of %v total %d s, want %v and %d s", tc.name, asOf, total, tc.wantAsOf, tc.wantTotal)
		}
	}
}
