package attendance

import (
	"reflect"
	"slices"
	"testing"

	"zoom-checker/internal/model"
)

// The teacher finished the lesson at its planned end while the host kept Zoom
// open; meeting.ended arrives later. The results must not change from what
// the finish fixed: no extra minutes, no latecomer counted as present.
func TestManualFinishSurvivesLateMeetingEnded(t *testing.T) {
	t.Parallel()
	inp := baseInput(numbered(
		join("a", "Иванов Иван", hm(18, 0)),
		join("b", "Петров Пётр", hm(19, 45)),
		leave("b", "Петров Пётр", hm(19, 50)),
		leave("a", "Иванов Иван", hm(20, 0)),
	)...)
	inp.Lesson.ManualFinishedAt = tp(hm(19, 30))
	inp.Now = hm(20, 5)

	before := Compute(inp) // meeting.ended not received yet
	inp.Sessions = []Session{{MeetingUUID: sess1, HostID: host, StartedAt: tp(hm(18, 0)), EndedAt: tp(hm(20, 0))}}
	after := Compute(inp)

	for name, out := range map[string]Output{"before meeting.ended": before, "after meeting.ended": after} {
		wantTime(t, name+": actual end", out.ActualEnd, tp(hm(19, 30)))
		s := student(t, out, ivanov)
		wantStudent(t, s, model.CurrentLeft, minutes(90), model.FinalPresent)
		wantTime(t, name+": ivanov last leave", s.LastLeaveAt, tp(hm(19, 30)))
		wantStudent(t, student(t, out, petrov), model.CurrentNotSeen, 0, model.FinalAbsent)
	}
	if !reflect.DeepEqual(before.Students, after.Students) {
		t.Errorf("students changed when meeting.ended arrived:\nbefore %+v\nafter  %+v", before.Students, after.Students)
	}

	// Finished after Zoom had already closed: Zoom's end is used as before.
	inp.Lesson.ManualFinishedAt = tp(hm(20, 30))
	out := Compute(inp)
	iv := intervalsOf(out, "иванов иван")[0]
	wantTime(t, "ivanov leave (finish after Zoom ended)", iv.LeaveAt, tp(hm(20, 0)))
}

// A rename seen only at leave must not take the time away from a student who
// was identified under the join name (TZ §18).
func TestRenameAtLeaveKeepsIdentifiedStudent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		events  []ParticipantEvent
		ignored []string
		// expectations
		wantIvanov int64
		wantPetrov int64
		wantNames  []string // normalized participants
	}{
		{
			name: "student renamed to an unknown name",
			events: []ParticipantEvent{
				join("pc", "Иванов Иван", hm(18, 0)),
				leave("pc", "Vanya (back in 5)", hm(18, 50)),
			},
			wantIvanov: minutes(50),
			wantNames:  []string{"иванов иван"},
		},
		{
			name: "same visit, leave sorted before its join",
			events: []ParticipantEvent{
				leave("pc", "Vanya (back in 5)", hm(18, 50), ts(1000), joinedAt(hm(18, 0))),
				join("pc", "Иванов Иван", hm(18, 50), ts(5000)),
			},
			wantIvanov: minutes(50),
			wantNames:  []string{"иванов иван"},
		},
		{
			name: "student renamed to an ignored name",
			events: []ParticipantEvent{
				join("pc", "Иванов Иван", hm(18, 0)),
				leave("pc", "Наблюдатель", hm(18, 50)),
			},
			ignored:    []string{"наблюдатель"},
			wantIvanov: minutes(50),
			wantNames:  []string{"иванов иван"},
		},
		{
			name: "student renamed to another student",
			events: []ParticipantEvent{
				join("pc", "Иванов Иван", hm(18, 0)),
				leave("pc", "Петров Пётр", hm(18, 50)),
			},
			wantIvanov: minutes(50),
			wantNames:  []string{"иванов иван"},
		},
		{
			name: "unidentified name renamed to a student still moves",
			events: []ParticipantEvent{
				join("pc", "Ваня", hm(18, 0)),
				leave("pc", "Петров Пётр", hm(18, 50)),
			},
			wantPetrov: minutes(50),
			wantNames:  []string{"петров петр"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inp := baseInput(numbered(tc.events...)...)
			if len(tc.ignored) > 0 {
				inp.IgnoredNames = map[string]bool{}
				for _, n := range tc.ignored {
					inp.IgnoredNames[n] = true
				}
			}
			out := Compute(inp)
			if got := student(t, out, ivanov).TotalSeconds; got != tc.wantIvanov {
				t.Errorf("ivanov total = %d, want %d", got, tc.wantIvanov)
			}
			if got := student(t, out, petrov).TotalSeconds; got != tc.wantPetrov {
				t.Errorf("petrov total = %d, want %d", got, tc.wantPetrov)
			}
			if tc.wantIvanov > 0 {
				wantStudent(t, student(t, out, ivanov), model.CurrentLeft, tc.wantIvanov, model.FinalPresent)
			}
			if got := participantNames(out); !slices.Equal(got, tc.wantNames) {
				t.Errorf("participants = %v, want %v", got, tc.wantNames)
			}
			// Input order never matters.
			rev := slices.Clone(inp.Events)
			slices.Reverse(rev)
			inp.Events = rev
			if again := Compute(inp); !reflect.DeepEqual(again, out) {
				t.Errorf("result depends on event order:\n%+v\n%+v", out.Students, again.Students)
			}
		})
	}
}

// The minimum-percent rule measures the scheduled lesson time, not the whole
// Zoom meeting: the host opening Zoom early or staying late must not turn a
// student who attended the whole lesson into "absent".
func TestPresentPercentUsesLessonWindow(t *testing.T) {
	t.Parallel()
	attended := func(from, to [2]int) []ParticipantEvent {
		return numbered(join("a", "Иванов Иван", hm(from[0], from[1])), leave("a", "Иванов Иван", hm(to[0], to[1])))
	}
	tests := []struct {
		name     string
		events   []ParticipantEvent
		started  [2]int
		ended    *[2]int // nil = still running
		finish   *[2]int // manual finish
		now      [2]int
		percent  int
		wantAuto model.FinalStatus
	}{
		{"host opened Zoom an hour early, finished", attended([2]int{18, 0}, [2]int{19, 30}),
			[2]int{17, 0}, &[2]int{19, 30}, nil, [2]int{19, 31}, 70, model.FinalPresent},
		{"host opened Zoom an hour early, running", attended([2]int{18, 0}, [2]int{19, 30}),
			[2]int{17, 0}, nil, nil, [2]int{19, 30}, 70, model.FinalPresent},
		{"host stayed until 21:30", attended([2]int{18, 0}, [2]int{19, 30}),
			[2]int{18, 0}, &[2]int{21, 30}, nil, [2]int{21, 31}, 50, model.FinalPresent},
		{"only in the host's early part", attended([2]int{17, 0}, [2]int{18, 0}),
			[2]int{17, 0}, &[2]int{19, 30}, nil, [2]int{19, 31}, 50, model.FinalAbsent},
		{"only in the early part, running", attended([2]int{17, 0}, [2]int{18, 0}),
			[2]int{17, 0}, nil, nil, [2]int{18, 30}, 50, model.FinalAbsent},
		{"meeting.ended lost, finished by hand at 23:00", attended([2]int{18, 0}, [2]int{19, 30}),
			[2]int{18, 0}, nil, &[2]int{23, 0}, [2]int{23, 1}, 50, model.FinalPresent},
		{"lesson ran shorter than planned", attended([2]int{18, 30}, [2]int{19, 0}),
			[2]int{18, 5}, &[2]int{19, 5}, nil, [2]int{19, 6}, 50, model.FinalPresent},
		{"held entirely outside the plan", attended([2]int{20, 0}, [2]int{21, 0}),
			[2]int{20, 0}, &[2]int{21, 30}, nil, [2]int{21, 31}, 50, model.FinalPresent},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inp := baseInput(tc.events...)
			inp.Sessions[0].StartedAt = tp(hm(tc.started[0], tc.started[1]))
			inp.Sessions[0].FirstEventAt = hm(tc.started[0], tc.started[1])
			if tc.ended != nil {
				inp.Sessions[0].EndedAt = tp(hm(tc.ended[0], tc.ended[1]))
			}
			if tc.finish != nil {
				inp.Lesson.ManualFinishedAt = tp(hm(tc.finish[0], tc.finish[1]))
			}
			inp.Now = hm(tc.now[0], tc.now[1])
			inp.PresentMinPercent = tc.percent
			s := student(t, Compute(inp), ivanov)
			if s.AutoStatus != tc.wantAuto {
				t.Errorf("auto status = %q, want %q (total %d s)", s.AutoStatus, tc.wantAuto, s.TotalSeconds)
			}
		})
	}
}
