package attendance

import (
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"

	"zoom-checker/internal/model"
)

func TestDuplicateJoinSameKeySameNameKeepsOneInterval(t *testing.T) {
	t.Parallel()
	inp := baseInput(numbered(
		join("pc", "Иванов Иван", hm(18, 0)),
		join("pc", "ИВАНОВ  ИВАН", hm(18, 20)), // lost leave / repeated join: same device, same name
		leave("pc", "Иванов Иван", hm(18, 50)),
	)...)
	out := Compute(inp)

	ivs := intervalsOf(out, "иванов иван")
	if len(ivs) != 1 {
		t.Fatalf("intervals = %d, want 1", len(ivs))
	}
	if !ivs[0].JoinAt.Equal(hm(18, 0)) {
		t.Errorf("join = %v, want the earlier start 18:00", ivs[0].JoinAt)
	}
	wantStudent(t, student(t, out, ivanov), model.CurrentLeft, minutes(50), model.FinalPresent)
	if p := participant(t, out, "иванов иван"); p.DisplayName != "ИВАНОВ  ИВАН" {
		t.Errorf("display name = %q, want the most recent join's raw name", p.DisplayName)
	}
}

// richEvents exercises most rules at once: rejoin, two devices, rename, breakout,
// orphan leave, unidentified, host, a second session and same-second ties.
func richEvents() []ParticipantEvent {
	return numbered(
		join("host", "Преподаватель", hm(17, 58), pid(host)),
		join("a1", "Иванов Иван", hm(18, 0)),
		leave("a1", "Иванов Иван", hm(18, 20)),
		join("a1", "Иванов Иван", hm(18, 20), bo()),
		leave("a1", "Иванов Иван", hm(18, 40), bo()),
		join("a2", "Иванов Иван", hm(18, 40)),
		join("p1", "Петров Пётр", hm(18, 1)),
		join("p2", "петров петр", hm(18, 30)),
		leave("p1", "Петров Пётр", hm(18, 31)),
		join("v1", "Ваня", hm(18, 2)),
		leave("v1", "Сидоров Алексей", hm(18, 50)), // renamed during the meeting
		leave("x1", "Александр", hm(18, 7), joinedAt(hm(18, 3))),
		leave("x2", "Гость", hm(18, 8)),
		join("s2a", "Иванов Иван", hm(18, 45), in(sess2)),
		leave("s2a", "Иванов Иван", hm(18, 55), in(sess2)),
		join("same", "Иванов Иван", hm(18, 41), ts(2000)),
		leave("same", "Иванов Иван", hm(18, 41), ts(1000)),
	)
}

func richInput() Input {
	inp := baseInput(richEvents()...)
	inp.Sessions = append(inp.Sessions, Session{MeetingUUID: sess2, StartedAt: tp(hm(18, 44)), FirstEventAt: hm(18, 44), EndedAt: tp(hm(19, 0))})
	inp.Mappings = map[string]Mapping{"александр": {Action: model.MappingStudent, StudentID: alexandrov}}
	inp.IgnoredNames = map[string]bool{"гость": true}
	inp.Now = hm(19, 5)
	return inp
}

func TestOutOfOrderInputGivesSameResult(t *testing.T) {
	t.Parallel()
	inp := richInput()
	want := Compute(inp)

	rng := rand.New(rand.NewPCG(7, 42))
	for i := range 200 {
		shuffled := slices.Clone(inp.Events)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		inp2 := inp
		inp2.Events = shuffled
		if got := Compute(inp2); !reflect.DeepEqual(got, want) {
			t.Fatalf("shuffle %d: output differs from sorted input\n got: %+v\nwant: %+v", i, got, want)
		}
	}
	// Compute must not reorder the caller's slice.
	if !reflect.DeepEqual(inp.Events, richEvents()) {
		t.Error("Compute modified Input.Events")
	}
}

func TestRichLessonExpectations(t *testing.T) {
	t.Parallel()
	out := Compute(richInput())

	// Иванов: 18:00–18:20 main, 18:20–18:40 breakout, 18:40– main (open), 18:41 zero-length,
	// 18:45–18:55 in session 2 → continuous 18:00–19:05.
	wantStudent(t, student(t, out, ivanov), model.CurrentOnline, minutes(65), model.FinalPresent)
	// Петров: 18:01–18:31 ∪ 18:30–19:05.
	wantStudent(t, student(t, out, petrov), model.CurrentOnline, minutes(64), model.FinalPresent)
	// Сидоров: the "Ваня" device was renamed and left as Сидоров: 18:02–18:50.
	wantStudent(t, student(t, out, sidorov), model.CurrentLeft, minutes(48), model.FinalPresent)
	// Александров: manual mapping of an orphan leave with join_time 18:03–18:07.
	wantStudent(t, student(t, out, alexandrov), model.CurrentLeft, minutes(4), model.FinalPresent)

	if p := participant(t, out, "преподаватель"); p.Resolution != model.ResolutionIgnored || p.IgnoreReason != model.IgnoreHost {
		t.Errorf("host: %q/%q", p.Resolution, p.IgnoreReason)
	}
	if p := participant(t, out, "гость"); p.Resolution != model.ResolutionIgnored || p.IgnoreReason != model.IgnoreList {
		t.Errorf("guest: %q/%q", p.Resolution, p.IgnoreReason)
	}
	if hasParticipant(out, "ваня") {
		t.Error(`"ваня" must disappear: its only interval was renamed at leave`)
	}
}

func TestOrphanLeave(t *testing.T) {
	t.Parallel()

	t.Run("with join_time", func(t *testing.T) {
		t.Parallel()
		out := Compute(baseInput(leave("pc", "Иванов Иван", hm(18, 40), joinedAt(hm(18, 10)))))
		ivs := intervalsOf(out, "иванов иван")
		if len(ivs) != 1 || !ivs[0].JoinAt.Equal(hm(18, 10)) || !ivs[0].LeaveAt.Equal(hm(18, 40)) {
			t.Fatalf("intervals = %+v, want [18:10, 18:40]", ivs)
		}
		wantStudent(t, student(t, out, ivanov), model.CurrentLeft, minutes(30), model.FinalPresent)
	})

	t.Run("without join_time is a zero-length sighting", func(t *testing.T) {
		t.Parallel()
		inp := baseInput(leave("pc", "Иванов Иван", hm(18, 40)))
		out := Compute(inp)
		ivs := intervalsOf(out, "иванов иван")
		if len(ivs) != 1 || !ivs[0].JoinAt.Equal(hm(18, 40)) || *ivs[0].DurationSeconds != 0 {
			t.Fatalf("intervals = %+v, want zero-length at 18:40", ivs)
		}
		s := student(t, out, ivanov)
		wantStudent(t, s, model.CurrentLeft, 0, model.FinalPresent) // seen at least once (TZ §18)
		wantTime(t, "first join", s.FirstJoinAt, tp(hm(18, 40)))
		wantTime(t, "last leave", s.LastLeaveAt, tp(hm(18, 40)))

		inp.PresentMinPercent = 10
		wantStudent(t, student(t, Compute(inp), ivanov), model.CurrentLeft, 0, model.FinalAbsent)
	})

	t.Run("join_time after leave is ignored", func(t *testing.T) {
		t.Parallel()
		out := Compute(baseInput(leave("pc", "Иванов Иван", hm(18, 40), joinedAt(hm(18, 50)))))
		if ivs := intervalsOf(out, "иванов иван"); len(ivs) != 1 || *ivs[0].DurationSeconds != 0 {
			t.Fatalf("intervals = %+v, want one zero-length", ivs)
		}
	})

	t.Run("repeated leave adds nothing", func(t *testing.T) {
		t.Parallel()
		out := Compute(baseInput(numbered(
			join("pc", "Иванов Иван", hm(18, 0)),
			leave("pc", "Иванов Иван", hm(18, 40)),
			leave("pc", "Иванов Иван", hm(18, 40)),
			leave("pc", "Иванов Иван", hm(18, 40), joinedAt(hm(18, 0))),
		)...))
		if ivs := intervalsOf(out, "иванов иван"); len(ivs) != 1 {
			t.Fatalf("intervals = %d, want 1: %+v", len(ivs), ivs)
		}
	})

	t.Run("overlapping join_time is trimmed to the previous leave", func(t *testing.T) {
		t.Parallel()
		out := Compute(baseInput(numbered(
			join("pc", "Иванов Иван", hm(18, 0)),
			leave("pc", "Иванов Иван", hm(18, 20)),
			leave("pc", "Иванов Иван", hm(18, 50), joinedAt(hm(18, 10))), // rejoin lost, join_time is off
		)...))
		ivs := intervalsOf(out, "иванов иван")
		if len(ivs) != 2 || !ivs[1].JoinAt.Equal(hm(18, 20)) {
			t.Fatalf("intervals = %+v, want second one to start at 18:20", ivs)
		}
		wantStudent(t, student(t, out, ivanov), model.CurrentLeft, minutes(50), model.FinalPresent)
	})

	t.Run("leave sorted before its join in the same second", func(t *testing.T) {
		t.Parallel()
		// A redelivered join carries a newer event_ts than the leave of the same short visit.
		out := Compute(baseInput(numbered(
			leave("pc", "Иванов Иван", hm(18, 10), ts(1000)),
			join("pc", "Иванов Иван", hm(18, 10), ts(5000)),
		)...))
		ivs := intervalsOf(out, "иванов иван")
		if len(ivs) != 1 || ivs[0].LeaveAt == nil {
			t.Fatalf("intervals = %+v, want one closed interval (not online forever)", ivs)
		}
		wantStudent(t, student(t, out, ivanov), model.CurrentLeft, 0, model.FinalPresent)
	})
}

func TestJoinAfterSessionEndIsIgnored(t *testing.T) {
	t.Parallel()
	inp := baseInput(numbered(
		join("a", "Иванов Иван", hm(18, 0)),
		join("b", "Петров Пётр", hm(19, 31)),
		leave("b", "Петров Пётр", hm(19, 32)),
	)...)
	inp.Sessions[0].EndedAt = tp(hm(19, 30))
	out := Compute(inp)

	wantStudent(t, student(t, out, ivanov), model.CurrentLeft, minutes(90), model.FinalPresent)
	// The leave of a visit that started after the end goes with its join.
	wantStudent(t, student(t, out, petrov), model.CurrentNotSeen, 0, model.FinalAbsent)
	if hasParticipant(out, "петров петр") || len(out.Intervals) != 1 {
		t.Errorf("late visit left traces: %+v", out.Intervals)
	}

	for name, evs := range map[string][]ParticipantEvent{
		"join only":            {join("b", "Петров Пётр", hm(19, 31))},
		"leave with late join": {leave("b", "Петров Пётр", hm(19, 40), joinedAt(hm(19, 31)))},
	} {
		inp.Events = evs
		if out := Compute(inp); len(out.Intervals) != 0 {
			t.Errorf("%s: created %+v", name, out.Intervals)
		}
	}

	// A leave without any join, a few seconds after meeting.ended, proves presence at the end.
	inp.Events = []ParticipantEvent{leave("b", "Петров Пётр", hms(19, 30, 2))}
	out = Compute(inp)
	s := student(t, out, petrov)
	wantStudent(t, s, model.CurrentLeft, 0, model.FinalPresent)
	wantTime(t, "petrov last leave", s.LastLeaveAt, tp(hm(19, 30)))
}

func TestManualFinishClosesOpenIntervals(t *testing.T) {
	t.Parallel()
	inp := baseInput(numbered(
		join("a", "Иванов Иван", hm(18, 0)),
		join("b", "Петров Пётр", hm(18, 0)),
		leave("b", "Петров Пётр", hm(19, 0)),
		join("c", "Сидоров Алексей", hm(19, 25)), // after the teacher finished
	)...)
	inp.Lesson.ManualFinishedAt = tp(hm(19, 20))
	inp.Now = hm(20, 0)
	out := Compute(inp)

	if out.Status != model.LessonFinished {
		t.Fatalf("status = %q, want finished", out.Status)
	}
	wantTime(t, "actual end", out.ActualEnd, tp(hm(19, 20)))
	iv := intervalsOf(out, "иванов иван")[0]
	wantTime(t, "ivanov leave", iv.LeaveAt, tp(hm(19, 20)))
	if iv.LeaveReason != ReasonLessonFinished {
		t.Errorf("leave reason = %q, want %q", iv.LeaveReason, ReasonLessonFinished)
	}
	wantStudent(t, student(t, out, ivanov), model.CurrentLeft, minutes(80), model.FinalPresent)
	wantInt(t, "ivanov early leave", student(t, out, ivanov).EarlyLeaveSeconds, i64(0))
	wantInt(t, "petrov early leave", student(t, out, petrov).EarlyLeaveSeconds, i64(minutes(20)))
	wantStudent(t, student(t, out, sidorov), model.CurrentNotSeen, 0, model.FinalAbsent)

	t.Run("the manual finish stays final when meeting.ended comes later", func(t *testing.T) {
		t.Parallel()
		inp := inp
		inp.Sessions = []Session{{MeetingUUID: sess1, StartedAt: tp(hm(18, 0)), EndedAt: tp(hm(19, 30))}}
		out := Compute(inp)
		wantTime(t, "actual end", out.ActualEnd, tp(hm(19, 20)))
		iv := intervalsOf(out, "иванов иван")[0]
		wantTime(t, "ivanov leave", iv.LeaveAt, tp(hm(19, 20)))
		if iv.LeaveReason != ReasonLessonFinished {
			t.Errorf("leave reason = %q, want %q", iv.LeaveReason, ReasonLessonFinished)
		}
		wantStudent(t, student(t, out, ivanov), model.CurrentLeft, minutes(80), model.FinalPresent)
		wantInt(t, "ivanov early leave", student(t, out, ivanov).EarlyLeaveSeconds, i64(0))
		// Joined after the finish: not part of the lesson.
		wantStudent(t, student(t, out, sidorov), model.CurrentNotSeen, 0, model.FinalAbsent)
	})
}

func TestRename(t *testing.T) {
	t.Parallel()

	t.Run("join under a new name while open", func(t *testing.T) {
		t.Parallel()
		out := Compute(baseInput(numbered(
			join("pc", "Ваня", hm(18, 0)),
			join("pc", "Иванов Иван", hm(18, 10)),
		)...))
		old := intervalsOf(out, "ваня")
		if len(old) != 1 || !old[0].LeaveAt.Equal(hm(18, 10)) || old[0].LeaveReason != ReasonRenamed {
			t.Fatalf("old-name intervals = %+v, want closed at 18:10 as renamed", old)
		}
		wantStudent(t, student(t, out, ivanov), model.CurrentOnline, minutes(50), model.FinalPresent)
		if p := participant(t, out, "ваня"); p.CurrentStatus != model.CurrentLeft || p.TotalSeconds != minutes(10) {
			t.Errorf("old name: status=%q total=%d", p.CurrentStatus, p.TotalSeconds)
		}
	})

	t.Run("leave reports the new name", func(t *testing.T) {
		t.Parallel()
		out := Compute(baseInput(numbered(
			join("pc", "Ваня", hm(18, 0)),
			leave("pc", "Иванов Иван", hm(18, 50)),
		)...))
		ivs := intervalsOf(out, "иванов иван")
		if len(ivs) != 1 || ivs[0].RenamedFrom != "Ваня" || ivs[0].UserName != "Иванов Иван" {
			t.Fatalf("intervals = %+v, want one renamed from Ваня", ivs)
		}
		wantStudent(t, student(t, out, ivanov), model.CurrentLeft, minutes(50), model.FinalPresent)
		if hasParticipant(out, "ваня") {
			t.Error(`"ваня" must not be a participant any more`)
		}
		if p := participant(t, out, "иванов иван"); p.DisplayName != "Иванов Иван" {
			t.Errorf("display name = %q", p.DisplayName)
		}
	})

	t.Run("leave with an empty name keeps the join name", func(t *testing.T) {
		t.Parallel()
		out := Compute(baseInput(numbered(
			join("pc", "Иванов Иван", hm(18, 0)),
			leave("pc", "", hm(18, 50)),
		)...))
		if ivs := intervalsOf(out, "иванов иван"); len(ivs) != 1 || ivs[0].RenamedFrom != "" {
			t.Fatalf("intervals = %+v", out.Intervals)
		}
	})

	t.Run("still open: attributed to the join name", func(t *testing.T) {
		t.Parallel()
		out := Compute(baseInput(join("pc", "Ваня", hm(18, 0))))
		if p := participant(t, out, "ваня"); p.Resolution != model.ResolutionUnidentified || p.CurrentStatus != model.CurrentOnline {
			t.Errorf("%+v", p)
		}
	})
}

func TestTwoDevicesSameNameAreOneStudent(t *testing.T) {
	t.Parallel()
	inp := baseInput(numbered(
		join("pc", "Иванов Иван", hm(18, 0)),
		join("phone", "Иванов Иван", hm(18, 10)),
		leave("phone", "Иванов Иван", hm(18, 20)),
	)...)
	inp.Now = hm(18, 30)
	out := Compute(inp)

	if len(out.Participants) != 1 {
		t.Fatalf("participants = %v, want one", participantNames(out))
	}
	p := out.Participants[0]
	if p.Devices != 2 || p.OnlineDevices != 1 || p.TotalSeconds != minutes(30) {
		t.Errorf("devices=%d online=%d total=%d, want 2/1/%d", p.Devices, p.OnlineDevices, p.TotalSeconds, minutes(30))
	}
	s := student(t, out, ivanov)
	wantStudent(t, s, model.CurrentOnline, minutes(30), model.FinalPresent)
	if s.OnlineDevices != 1 {
		t.Errorf("online devices = %d", s.OnlineDevices)
	}
}

func TestBreakoutRoomsKeepStudentPresent(t *testing.T) {
	t.Parallel()
	cases := map[string][]ParticipantEvent{
		"main leave reported": {
			join("a", "Иванов Иван", hm(18, 0)),
			leave("a", "Иванов Иван", hm(18, 20), ts(100), reason("join breakout room")),
			join("a", "Иванов Иван", hm(18, 20), ts(200), bo()),
			leave("a", "Иванов Иван", hm(18, 50), ts(100), bo()),
			join("b", "Иванов Иван", hm(18, 50), ts(200)), // back in the main room with a new participant_uuid
			leave("b", "Иванов Иван", hm(19, 30)),
		},
		"main room stays open": {
			join("a", "Иванов Иван", hm(18, 0)),
			join("a", "Иванов Иван", hm(18, 20), bo()),
			leave("a", "Иванов Иван", hm(18, 50), bo()), // must not close the main-room interval
			leave("a", "Иванов Иван", hm(19, 30)),
		},
		"breakout join sorted before the main leave": {
			join("a", "Иванов Иван", hm(18, 0)),
			join("a", "Иванов Иван", hm(18, 20), ts(100), bo()),
			leave("a", "Иванов Иван", hm(18, 20), ts(200)),
			leave("a", "Иванов Иван", hm(18, 50), bo()),
			join("b", "Иванов Иван", hm(18, 50)),
			leave("b", "Иванов Иван", hm(19, 30)),
		},
	}
	for name, evs := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			inp := baseInput(numbered(evs...)...)
			inp.Now = hm(20, 0)
			out := Compute(inp)
			wantStudent(t, student(t, out, ivanov), model.CurrentLeft, minutes(90), model.FinalPresent)

			var breakout int
			for _, iv := range out.Intervals {
				if iv.Breakout {
					breakout++
					if iv.ParticipantKey != "puuid:a" || iv.MeetingUUID != sess1 {
						t.Errorf("breakout interval key=%q meeting=%q", iv.ParticipantKey, iv.MeetingUUID)
					}
				}
			}
			if breakout != 1 {
				t.Errorf("breakout intervals = %d, want 1", breakout)
			}
		})
	}

	t.Run("in a breakout room right now", func(t *testing.T) {
		t.Parallel()
		inp := baseInput(numbered(
			join("a", "Иванов Иван", hm(18, 0)),
			leave("a", "Иванов Иван", hm(18, 20)),
			join("a", "Иванов Иван", hm(18, 20), bo()),
		)...)
		inp.Now = hm(18, 30)
		out := Compute(inp)
		s := student(t, out, ivanov)
		wantStudent(t, s, model.CurrentOnline, minutes(30), model.FinalPresent)
		if p := participant(t, out, "иванов иван"); p.Devices != 1 {
			t.Errorf("devices = %d, want 1 (breakout keeps the same participant)", p.Devices)
		}
	})
}

func TestMultipleSessionsAreUnioned(t *testing.T) {
	t.Parallel()
	inp := baseInput(numbered(
		join("a", "Иванов Иван", hm(18, 0)),
		join("b", "Петров Пётр", hm(18, 10)),
		join("c", "Иванов Иван", hm(18, 36), in(sess2)),
	)...)
	// The meeting crashed at 18:30 and was restarted (new UUID) at 18:35.
	inp.Sessions[0].EndedAt = tp(hm(18, 30))
	inp.Sessions = append(inp.Sessions, Session{MeetingUUID: sess2, HostID: host, StartedAt: tp(hm(18, 35)), FirstEventAt: hm(18, 35)})
	inp.Now = hm(19, 0)

	out := Compute(inp)
	if out.Status != model.LessonActive || out.ActualEnd != nil {
		t.Fatalf("status=%q end=%v, want active while session 2 runs", out.Status, fmtTime(out.ActualEnd))
	}
	wantTime(t, "actual start", out.ActualStart, tp(hm(18, 0)))
	wantStudent(t, student(t, out, ivanov), model.CurrentOnline, minutes(30+24), model.FinalPresent)
	p := student(t, out, petrov)
	wantStudent(t, p, model.CurrentLeft, minutes(20), model.FinalPresent)
	wantTime(t, "petrov last leave", p.LastLeaveAt, tp(hm(18, 30)))

	inp.Sessions[1].EndedAt = tp(hm(19, 30))
	inp.Now = hm(20, 0)
	out = Compute(inp)
	if out.Status != model.LessonFinished {
		t.Fatalf("status = %q, want finished", out.Status)
	}
	wantTime(t, "actual end", out.ActualEnd, tp(hm(19, 30)))
	wantStudent(t, student(t, out, ivanov), model.CurrentLeft, minutes(30+54), model.FinalPresent)
	wantInt(t, "petrov early leave", student(t, out, petrov).EarlyLeaveSeconds, i64(minutes(60)))
}

func TestMappings(t *testing.T) {
	t.Parallel()
	ev := []ParticipantEvent{join("a", "Иванов Иван", hm(18, 0))}

	t.Run("none forces unidentified even on an exact match", func(t *testing.T) {
		t.Parallel()
		inp := baseInput(ev...)
		inp.Mappings = map[string]Mapping{"иванов иван": {Action: model.MappingNone}}
		out := Compute(inp)
		if p := participant(t, out, "иванов иван"); p.Resolution != model.ResolutionUnidentified || p.StudentID != nil {
			t.Fatalf("resolution=%q student=%v", p.Resolution, p.StudentID)
		}
		wantStudent(t, student(t, out, ivanov), model.CurrentNotSeen, 0, model.FinalAbsent)
	})

	t.Run("ignore", func(t *testing.T) {
		t.Parallel()
		inp := baseInput(ev...)
		inp.Mappings = map[string]Mapping{"иванов иван": {Action: model.MappingIgnore}}
		out := Compute(inp)
		if p := participant(t, out, "иванов иван"); p.Resolution != model.ResolutionIgnored || p.IgnoreReason != model.IgnoreManual {
			t.Fatalf("resolution=%q reason=%q", p.Resolution, p.IgnoreReason)
		}
		wantStudent(t, student(t, out, ivanov), model.CurrentNotSeen, 0, model.FinalAbsent)
	})

	t.Run("student overrides an automatic match", func(t *testing.T) {
		t.Parallel()
		inp := baseInput(ev...)
		inp.Mappings = map[string]Mapping{"иванов иван": {Action: model.MappingStudent, StudentID: petrov}}
		out := Compute(inp)
		if p := participant(t, out, "иванов иван"); p.Resolution != model.ResolutionManual || *p.StudentID != petrov {
			t.Fatalf("resolution=%q student=%v", p.Resolution, p.StudentID)
		}
		wantStudent(t, student(t, out, petrov), model.CurrentOnline, minutes(60), model.FinalPresent)
		wantStudent(t, student(t, out, ivanov), model.CurrentNotSeen, 0, model.FinalAbsent)
	})

	t.Run("target outside the roster is unidentified", func(t *testing.T) {
		t.Parallel()
		inp := baseInput(join("a", "Вася", hm(18, 0)))
		inp.Mappings = map[string]Mapping{"вася": {Action: model.MappingStudent, StudentID: 999}}
		out := Compute(inp)
		if p := participant(t, out, "вася"); p.Resolution != model.ResolutionUnidentified || p.StudentID != nil {
			t.Fatalf("resolution=%q student=%v", p.Resolution, p.StudentID)
		}
		if len(out.Students) != len(inp.Roster) {
			t.Errorf("students = %d, want only the roster", len(out.Students))
		}
	})

	t.Run("archived student: no automatic match, manual mapping allowed", func(t *testing.T) {
		t.Parallel()
		inp := baseInput(join("a", "Сидоров Алексей", hm(18, 0)), join("b", "Лёша", hm(18, 5)))
		inp.Roster[2].Active = false
		inp.Roster[2].NormalizedAliases = []string{"леша"}
		out := Compute(inp)
		if p := participant(t, out, "сидоров алексей"); p.Resolution != model.ResolutionUnidentified {
			t.Errorf("archived name resolution = %q, want unidentified", p.Resolution)
		}
		if p := participant(t, out, "леша"); p.Resolution != model.ResolutionUnidentified {
			t.Errorf("archived alias resolution = %q, want unidentified", p.Resolution)
		}
		wantStudent(t, student(t, out, sidorov), model.CurrentNotSeen, 0, model.FinalAbsent)

		inp.Mappings = map[string]Mapping{"сидоров алексей": {Action: model.MappingStudent, StudentID: sidorov}}
		out = Compute(inp)
		wantStudent(t, student(t, out, sidorov), model.CurrentOnline, minutes(60), model.FinalPresent)
	})

	t.Run("keys are normalized defensively", func(t *testing.T) {
		t.Parallel()
		inp := baseInput(join("a", "Ваня", hm(18, 0)))
		inp.Mappings = map[string]Mapping{"  ВАНЯ ": {Action: model.MappingStudent, StudentID: ivanov}}
		if p := participant(t, Compute(inp), "ваня"); p.Resolution != model.ResolutionManual {
			t.Errorf("resolution = %q, want manual", p.Resolution)
		}
	})

	t.Run("unknown action falls back to automatic rules", func(t *testing.T) {
		t.Parallel()
		inp := baseInput(ev...)
		inp.Mappings = map[string]Mapping{"иванов иван": {Action: "bogus"}}
		if p := participant(t, Compute(inp), "иванов иван"); p.Resolution != model.ResolutionMatched {
			t.Errorf("resolution = %q, want matched", p.Resolution)
		}
	})
}

func TestIgnoreListAndHostOnlyWithoutRosterMatch(t *testing.T) {
	t.Parallel()
	inp := baseInput(numbered(
		join("a", "Иванов Иван", hm(18, 0)),
		join("b", "iPhone", hm(18, 1)),
		join("h", "Преподаватель", hm(18, 0), pid(host)),
		join("h2", "Петров Пётр", hm(18, 0), uid(host)), // host's device named like a student
		join("c", "Гость", hm(18, 2)),
	)...)
	inp.IgnoredNames = map[string]bool{"иванов иван": true, "iphone": true, "  Гость ": true, "ваня": false}

	out := Compute(inp)
	for _, tc := range []struct {
		name   string
		res    model.Resolution
		reason model.IgnoreReason
	}{
		{"иванов иван", model.ResolutionMatched, model.IgnoreNone},
		{"петров петр", model.ResolutionMatched, model.IgnoreNone},
		{"iphone", model.ResolutionIgnored, model.IgnoreList},
		{"гость", model.ResolutionIgnored, model.IgnoreList},
		{"преподаватель", model.ResolutionIgnored, model.IgnoreHost},
	} {
		p := participant(t, out, tc.name)
		if p.Resolution != tc.res || p.IgnoreReason != tc.reason {
			t.Errorf("%s: %q/%q, want %q/%q", tc.name, p.Resolution, p.IgnoreReason, tc.res, tc.reason)
		}
	}
	wantStudent(t, student(t, out, ivanov), model.CurrentOnline, minutes(60), model.FinalPresent)

	inp.IgnoreHost = false
	if p := participant(t, Compute(inp), "преподаватель"); p.Resolution != model.ResolutionUnidentified {
		t.Errorf("host with IgnoreHost=false: %q, want unidentified", p.Resolution)
	}

	t.Run("host of another session is not the host", func(t *testing.T) {
		t.Parallel()
		inp := baseInput(join("h", "Преподаватель", hm(18, 0), pid("other-host")))
		if p := participant(t, Compute(inp), "преподаватель"); p.Resolution != model.ResolutionUnidentified {
			t.Errorf("resolution = %q, want unidentified", p.Resolution)
		}
	})

	t.Run("host renamed at leave stays ignored", func(t *testing.T) {
		t.Parallel()
		inp := baseInput(numbered(
			join("h", "Zoom user", hm(18, 0), pid(host)),
			leave("h", "Преподаватель", hm(19, 0)),
		)...)
		if p := participant(t, Compute(inp), "преподаватель"); p.IgnoreReason != model.IgnoreHost {
			t.Errorf("reason = %q, want host", p.IgnoreReason)
		}
	})
}

func TestConflict(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		roster     []RosterStudent
		wantRes    model.Resolution
		candidates []int64
	}{
		{
			name: "two students with one name",
			roster: []RosterStudent{
				{StudentID: 7, NormalizedName: "иванов иван", Active: true},
				{StudentID: 5, NormalizedName: "иванов иван", Active: true},
			},
			wantRes: model.ResolutionConflict, candidates: []int64{5, 7},
		},
		{
			name: "name of one student, alias of another",
			roster: []RosterStudent{
				{StudentID: 1, NormalizedName: "иванов иван", Active: true},
				{StudentID: 2, NormalizedName: "иванов иван сергеевич", NormalizedAliases: []string{"Иванов Иван"}, Active: true},
			},
			wantRes: model.ResolutionConflict, candidates: []int64{1, 2},
		},
		{
			name: "own name and own alias are one student",
			roster: []RosterStudent{
				{StudentID: 1, NormalizedName: "иванов иван", NormalizedAliases: []string{"иванов иван", "ваня"}, Active: true},
			},
			wantRes: model.ResolutionMatched,
		},
		{
			name: "archived namesake is not a candidate",
			roster: []RosterStudent{
				{StudentID: 1, NormalizedName: "иванов иван", Active: true},
				{StudentID: 2, NormalizedName: "иванов иван", Active: false},
			},
			wantRes: model.ResolutionMatched,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inp := baseInput(join("a", "Иванов Иван", hm(18, 0)))
			inp.Roster = tc.roster
			out := Compute(inp)
			p := participant(t, out, "иванов иван")
			if p.Resolution != tc.wantRes {
				t.Fatalf("resolution = %q, want %q", p.Resolution, tc.wantRes)
			}
			if tc.wantRes != model.ResolutionConflict {
				return
			}
			if !slices.Equal(p.CandidateStudentIDs, tc.candidates) || p.StudentID != nil {
				t.Errorf("candidates=%v student=%v, want %v/nil", p.CandidateStudentIDs, p.StudentID, tc.candidates)
			}
			for _, s := range out.Students {
				if s.Seen {
					t.Errorf("student %d credited despite the conflict", s.StudentID)
				}
			}
			if out.Intervals[0].Resolution != model.ResolutionConflict || out.Intervals[0].StudentID != nil {
				t.Errorf("interval %+v", out.Intervals[0])
			}
		})
	}

	t.Run("manual mapping resolves a conflict", func(t *testing.T) {
		t.Parallel()
		inp := baseInput(join("a", "Иванов Иван", hm(18, 0)))
		inp.Roster = cases[0].roster
		inp.Mappings = map[string]Mapping{"иванов иван": {Action: model.MappingStudent, StudentID: 5}}
		out := Compute(inp)
		if !student(t, out, 5).Seen || student(t, out, 7).Seen {
			t.Error("mapping must credit exactly student 5")
		}
	})

	t.Run("conflicting name on the ignore list stays a conflict", func(t *testing.T) {
		t.Parallel()
		inp := baseInput(join("a", "Иванов Иван", hm(18, 0)))
		inp.Roster = cases[0].roster
		inp.IgnoredNames = map[string]bool{"иванов иван": true}
		if p := participant(t, Compute(inp), "иванов иван"); p.Resolution != model.ResolutionConflict {
			t.Errorf("resolution = %q, want conflict", p.Resolution)
		}
	})
}

func TestLateSeconds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		first time.Time
		want  *int64
	}{
		{"late by 12 minutes (TZ §19)", hm(18, 12), i64(minutes(12))},
		{"exactly on time", hm(18, 0), i64(0)},
		{"early", hm(17, 50), i64(0)},
		{"seconds precision", hms(18, 0, 59), i64(59)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := Compute(baseInput(join("a", "Иванов Иван", tc.first), join("b", "Иванов Иван", hm(18, 30))))
			wantInt(t, "late", student(t, out, ivanov).LateSeconds, tc.want)
		})
	}
	out := Compute(baseInput())
	wantInt(t, "late of an unseen student", student(t, out, ivanov).LateSeconds, nil)
}

func TestEarlyLeave(t *testing.T) {
	t.Parallel()
	evs := numbered(
		join("a", "Иванов Иван", hm(18, 0)), leave("a", "Иванов Иван", hm(19, 10)),
		join("b", "Петров Пётр", hm(18, 0)), // stays until the end
		join("c", "Сидоров Алексей", hm(18, 0)), leave("c", "Сидоров Алексей", hm(18, 30)),
		join("c2", "Сидоров Алексей", hm(19, 0)), leave("c2", "Сидоров Алексей", hm(19, 29)),
	)

	running := Compute(baseInput(evs...))
	for _, s := range running.Students {
		if s.EarlyLeaveSeconds != nil {
			t.Errorf("student %d: early leave %v while the lesson is running", s.StudentID, fmtInt(s.EarlyLeaveSeconds))
		}
	}

	inp := baseInput(evs...)
	inp.Sessions[0].EndedAt = tp(hm(19, 30))
	out := Compute(inp)
	wantInt(t, "ivanov", student(t, out, ivanov).EarlyLeaveSeconds, i64(minutes(20)))
	wantInt(t, "petrov", student(t, out, petrov).EarlyLeaveSeconds, i64(0))
	wantInt(t, "sidorov", student(t, out, sidorov).EarlyLeaveSeconds, i64(minutes(1)))
	wantInt(t, "unseen", student(t, out, alexandrov).EarlyLeaveSeconds, nil)
}

func TestPresentMinPercent(t *testing.T) {
	t.Parallel()
	// Planned lesson 18:00–19:30 = 5400 s; 50% = 2700 s.
	for _, tc := range []struct {
		name    string
		percent int
		leave   time.Time
		want    model.FinalStatus
	}{
		{"rule off: any presence counts", 0, hms(18, 0, 9), model.FinalPresent},
		{"exactly the threshold", 50, hm(18, 45), model.FinalPresent},
		{"one second short", 50, hms(18, 44, 59), model.FinalAbsent},
		{"100 percent", 100, hm(19, 30), model.FinalPresent},
		{"over 100 is clamped", 250, hm(19, 30), model.FinalPresent},
		{"negative means off", -5, hms(18, 0, 1), model.FinalPresent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inp := baseInput(numbered(join("a", "Иванов Иван", hm(18, 0)), leave("a", "Иванов Иван", tc.leave))...)
			inp.PresentMinPercent = tc.percent
			inp.Now = hm(19, 30)
			if got := student(t, Compute(inp), ivanov).AutoStatus; got != tc.want {
				t.Errorf("auto status = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("finished lesson uses the actual duration", func(t *testing.T) {
		t.Parallel()
		// Actual 18:05–19:05 = 60 min; 50% = 30 min (planned 90 min would need 45).
		inp := baseInput(numbered(join("a", "Иванов Иван", hm(18, 30)), leave("a", "Иванов Иван", hm(19, 0)))...)
		inp.Sessions[0].StartedAt = tp(hm(18, 5))
		inp.Sessions[0].EndedAt = tp(hm(19, 5))
		inp.PresentMinPercent = 50
		if got := student(t, Compute(inp), ivanov).AutoStatus; got != model.FinalPresent {
			t.Errorf("auto status = %q, want present", got)
		}
		inp.Sessions[0].EndedAt = nil // running: planned duration applies
		if got := student(t, Compute(inp), ivanov).AutoStatus; got != model.FinalAbsent {
			t.Errorf("auto status = %q, want absent", got)
		}
	})

	t.Run("unseen is always absent", func(t *testing.T) {
		t.Parallel()
		inp := baseInput()
		if got := student(t, Compute(inp), ivanov).AutoStatus; got != model.FinalAbsent {
			t.Errorf("auto status = %q", got)
		}
	})
}

func TestLiveTotalsAsOfNow(t *testing.T) {
	t.Parallel()
	inp := baseInput(numbered(
		join("a", "Иванов Иван", hm(18, 0)), leave("a", "Иванов Иван", hm(18, 10)),
		join("b", "Иванов Иван", hm(18, 20)),
	)...)
	for _, tc := range []struct {
		now  time.Time
		want int64
	}{
		{hm(18, 30), minutes(20)},
		{hm(19, 0), minutes(50)},
		{hms(19, 0, 30), minutes(50) + 30},
		{hm(18, 15), minutes(10)}, // "now" before the open interval started counts nothing for it
	} {
		inp.Now = tc.now
		out := Compute(inp)
		if !out.AsOf.Equal(tc.now) {
			t.Errorf("as of = %v, want %v", out.AsOf, tc.now)
		}
		s := student(t, out, ivanov)
		if s.TotalSeconds != tc.want || s.CurrentStatus != model.CurrentOnline {
			t.Errorf("now %s: total=%d status=%q, want %d online", tc.now.Format("15:04:05"), s.TotalSeconds, s.CurrentStatus, tc.want)
		}
		if p := participant(t, out, "иванов иван"); p.TotalSeconds != tc.want {
			t.Errorf("now %s: participant total=%d, want %d", tc.now.Format("15:04:05"), p.TotalSeconds, tc.want)
		}
		wantTime(t, "last leave while online", s.LastLeaveAt, tp(hm(18, 10)))
	}
}

func TestLessonState(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		sessions   []Session
		manual     *time.Time
		wantStatus model.LessonStatus
		wantStart  *time.Time
		wantEnd    *time.Time
	}{
		{name: "no sessions", wantStatus: model.LessonPlanned},
		{
			name:       "running session",
			sessions:   []Session{{MeetingUUID: "a", StartedAt: tp(hm(18, 2)), FirstEventAt: hm(17, 55)}},
			wantStatus: model.LessonActive, wantStart: tp(hm(18, 2)),
		},
		{
			name:       "meeting.started not received: first event time",
			sessions:   []Session{{MeetingUUID: "a", FirstEventAt: hm(17, 55)}},
			wantStatus: model.LessonActive, wantStart: tp(hm(17, 55)),
		},
		{
			name: "one of two sessions ended",
			sessions: []Session{
				{MeetingUUID: "a", StartedAt: tp(hm(18, 0)), EndedAt: tp(hm(18, 30))},
				{MeetingUUID: "b", StartedAt: tp(hm(18, 35))},
			},
			wantStatus: model.LessonActive, wantStart: tp(hm(18, 0)),
		},
		{
			name: "all sessions ended",
			sessions: []Session{
				{MeetingUUID: "b", StartedAt: tp(hm(18, 35)), EndedAt: tp(hm(19, 31))},
				{MeetingUUID: "a", StartedAt: tp(hm(18, 0)), EndedAt: tp(hm(18, 30))},
			},
			wantStatus: model.LessonFinished, wantStart: tp(hm(18, 0)), wantEnd: tp(hm(19, 31)),
		},
		{
			name:       "teacher finished a running lesson",
			sessions:   []Session{{MeetingUUID: "a", StartedAt: tp(hm(18, 0))}},
			manual:     tp(hm(19, 25)),
			wantStatus: model.LessonFinished, wantStart: tp(hm(18, 0)), wantEnd: tp(hm(19, 25)),
		},
		{
			name:       "manual finish wins over meeting.ended",
			sessions:   []Session{{MeetingUUID: "a", StartedAt: tp(hm(18, 0)), EndedAt: tp(hm(19, 40))}},
			manual:     tp(hm(19, 25)),
			wantStatus: model.LessonFinished, wantStart: tp(hm(18, 0)), wantEnd: tp(hm(19, 25)),
		},
		{
			name:       "finished without any Zoom session",
			manual:     tp(hm(19, 30)),
			wantStatus: model.LessonFinished, wantEnd: tp(hm(19, 30)),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inp := baseInput()
			inp.Sessions = tc.sessions
			inp.Lesson.ManualFinishedAt = tc.manual
			out := Compute(inp)
			if out.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", out.Status, tc.wantStatus)
			}
			wantTime(t, "actual start", out.ActualStart, tc.wantStart)
			wantTime(t, "actual end", out.ActualEnd, tc.wantEnd)
		})
	}
}

func TestEmptyNormalizedName(t *testing.T) {
	t.Parallel()
	inp := baseInput(numbered(
		join("a", "   ", hm(18, 0), pid(host)),
		join("b", "​­", hm(18, 1)),
	)...)
	inp.Roster = append(inp.Roster, RosterStudent{StudentID: 99, NormalizedName: "", Active: true})
	inp.IgnoredNames = map[string]bool{"": true}
	out := Compute(inp)

	p := participant(t, out, "")
	if p.Resolution != model.ResolutionUnidentified || p.StudentID != nil || p.Devices != 2 {
		t.Fatalf("empty name: resolution=%q student=%v devices=%d", p.Resolution, p.StudentID, p.Devices)
	}
	if student(t, out, 99).Seen {
		t.Error("a student with an empty name must never be matched")
	}

	inp.Mappings = map[string]Mapping{"": {Action: model.MappingIgnore}}
	if p := participant(t, Compute(inp), ""); p.Resolution != model.ResolutionIgnored {
		t.Errorf("explicit teacher decision on an empty name: %q, want ignored", p.Resolution)
	}
}

func TestDisplayNameAndZoomNames(t *testing.T) {
	t.Parallel()
	inp := baseInput(numbered(
		join("a", "Иванов Иван", hm(18, 0)),
		join("b", "ИВАНОВ ИВАН", hm(18, 10)),
		join("c", "Ivanov", hm(18, 5)),
		leave("a", "иванов иван", hm(18, 20)), // leave names do not beat join names
	)...)
	inp.Mappings = map[string]Mapping{"ivanov": {Action: model.MappingStudent, StudentID: ivanov}}
	out := Compute(inp)

	if p := participant(t, out, "иванов иван"); p.DisplayName != "ИВАНОВ ИВАН" {
		t.Errorf("display name = %q, want the most recent join's raw name", p.DisplayName)
	}
	if got := student(t, out, ivanov).ZoomNames; !slices.Equal(got, []string{"ИВАНОВ ИВАН", "Ivanov"}) {
		t.Errorf("zoom names = %v, want participant order", got)
	}
}

func TestOutputOrdering(t *testing.T) {
	t.Parallel()
	inp := baseInput(numbered(
		join("z", "Бета", hm(18, 5)),
		join("y", "Альфа", hm(18, 5)),
		join("x", "Гамма", hm(18, 1)),
		join("w", "Гамма", hm(18, 1), bo()),
		join("v", "Дельта", hm(18, 1)),
	)...)
	out := Compute(inp)

	var gotP []string
	for _, p := range out.Participants {
		gotP = append(gotP, p.DisplayName)
	}
	if want := []string{"Гамма", "Дельта", "Альфа", "Бета"}; !slices.Equal(gotP, want) {
		t.Errorf("participants = %v, want %v (first join, then display name)", gotP, want)
	}
	var gotI []string
	for _, iv := range out.Intervals {
		gotI = append(gotI, iv.ParticipantKey)
	}
	if want := []string{"puuid:v", "puuid:w", "puuid:x", "puuid:y", "puuid:z"}; !slices.Equal(gotI, want) {
		t.Errorf("intervals = %v, want %v (join time, then key)", gotI, want)
	}
}

func TestKeyFallback(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		o    evOpt
		want string
	}{
		{"participant uuid", func(e *ParticipantEvent) { e.Key, e.ParticipantUUID = "", "U1" }, "puuid:U1"},
		{"user id", func(e *ParticipantEvent) {
			e.Key, e.ParticipantUUID, e.UserID, e.ParticipantID = "", "", "16778240", "p"
		}, "uid:16778240"},
		{"participant id", func(e *ParticipantEvent) { e.Key, e.ParticipantUUID, e.ParticipantID = "", "", "P9" }, "pid:P9"},
		{"name", func(e *ParticipantEvent) { e.Key, e.ParticipantUUID = "", "" }, "name:иванов иван"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := Compute(baseInput(numbered(
				join("", " Иванов  Иван", hm(18, 0), tc.o),
				leave("", "иванов иван", hm(18, 30), tc.o),
			)...))
			if len(out.Intervals) != 1 || out.Intervals[0].ParticipantKey != tc.want || out.Intervals[0].LeaveAt == nil {
				t.Fatalf("intervals = %+v, want one closed interval keyed %q", out.Intervals, tc.want)
			}
		})
	}
}

func TestEventsOfUnknownSession(t *testing.T) {
	t.Parallel()
	inp := baseInput(join("a", "Иванов Иван", hm(18, 0), in("stray")))
	out := Compute(inp)
	wantStudent(t, student(t, out, ivanov), model.CurrentOnline, minutes(60), model.FinalPresent)

	// Once the lesson is over, such intervals close with it.
	inp.Sessions[0].EndedAt = tp(hm(19, 30))
	inp.Now = hm(21, 0)
	out = Compute(inp)
	wantStudent(t, student(t, out, ivanov), model.CurrentLeft, minutes(90), model.FinalPresent)
	if r := out.Intervals[0].LeaveReason; r != ReasonLessonFinished {
		t.Errorf("leave reason = %q", r)
	}
}

func TestIgnoresUnknownKindsAndDuplicateRosterRows(t *testing.T) {
	t.Parallel()
	inp := baseInput(join("a", "Иванов Иван", hm(18, 0)), ParticipantEvent{Kind: 0, MeetingUUID: sess1, UserName: "Петров Пётр", At: hm(18, 0)})
	inp.Roster = append(inp.Roster, RosterStudent{StudentID: ivanov, NormalizedName: "другое имя", Active: true})
	out := Compute(inp)
	if len(out.Students) != 4 || len(out.Intervals) != 1 {
		t.Fatalf("students=%d intervals=%d, want 4/1", len(out.Students), len(out.Intervals))
	}
}

func TestEmptyInput(t *testing.T) {
	t.Parallel()
	out := Compute(Input{})
	if out.Status != model.LessonPlanned || out.Participants == nil || out.Students == nil || out.Intervals == nil {
		t.Fatalf("%+v", out)
	}
	if len(out.Participants)+len(out.Students)+len(out.Intervals) != 0 {
		t.Fatalf("%+v", out)
	}
}

func TestIntervalDurations(t *testing.T) {
	t.Parallel()
	inp := baseInput(numbered(
		join("a", "Иванов Иван", hms(18, 0, 0)), leave("a", "Иванов Иван", hms(18, 1, 30)),
		join("b", "Иванов Иван", hm(18, 5)),
	)...)
	out := Compute(inp)
	if d := out.Intervals[0].DurationSeconds; d == nil || *d != 90 {
		t.Errorf("closed duration = %v, want 90", fmtInt(d))
	}
	if out.Intervals[1].DurationSeconds != nil || out.Intervals[1].LeaveAt != nil {
		t.Errorf("open interval has leave/duration: %+v", out.Intervals[1])
	}
}

// The contract orders events by (At, EventTS, Join<Leave, EventID).
func TestEventOrder(t *testing.T) {
	t.Parallel()
	base := ParticipantEvent{Kind: Leave, At: hm(18, 0), EventTS: 500, EventID: 5}
	for _, tc := range []struct {
		name string
		a    ParticipantEvent
	}{
		{"earlier At wins over everything", apply(base, func(e *ParticipantEvent) { e.At, e.EventTS, e.EventID = hms(17, 59, 59), 9999, 99 })},
		{"same At: smaller event_ts", apply(base, func(e *ParticipantEvent) { e.EventTS, e.EventID = 499, 99 })},
		{"same At and event_ts: join before leave", apply(base, func(e *ParticipantEvent) { e.Kind, e.EventID = Join, 99 })},
		{"all equal: smaller event id", apply(base, func(e *ParticipantEvent) { e.EventID = 4 })},
	} {
		if compareEvents(&tc.a, &base) >= 0 || compareEvents(&base, &tc.a) <= 0 {
			t.Errorf("%s: %+v must sort before %+v", tc.name, tc.a, base)
		}
	}
	same := base
	if compareEvents(&same, &base) != 0 {
		t.Error("identical events must compare equal")
	}
}

// A device renamed itself and its join+leave under the new name share one second:
// the join (rename) must be applied first so the device ends up offline.
func TestSameSecondJoinBeforeLeave(t *testing.T) {
	t.Parallel()
	out := Compute(baseInput(numbered(
		join("pc", "Ваня", hm(18, 0)),
		leave("pc", "Иванов Иван", hm(18, 30)),
		join("pc", "Иванов Иван", hm(18, 30)),
	)...))
	for _, p := range out.Participants {
		if p.CurrentStatus != model.CurrentLeft {
			t.Errorf("%s is %q, want left", p.NormalizedName, p.CurrentStatus)
		}
	}
}

// Every field of every interval, so pointer fields cannot alias each other.
func TestIntervalResultsInDetail(t *testing.T) {
	t.Parallel()
	inp := baseInput(numbered(
		join("i1", "Иванов Иван", hm(18, 10)), leave("i1", "Иванов Иван", hm(18, 40), reason("left")),
		join("i2", "Иванов Иван", hm(18, 50)), leave("i2", "Иванов Иван", hm(19, 30)),
		join("v", "Ваня", hm(18, 5)),
		join("p", "Петров Пётр", hm(18, 0)), leave("p", "Петров Пётр", hm(18, 20)),
	)...)
	inp.Now = hm(19, 40)
	out := Compute(inp)

	type row struct {
		name      string
		join      time.Time
		leave     *time.Time
		dur       *int64
		studentID *int64
		res       model.Resolution
		reason    string
	}
	want := []row{
		{"петров петр", hm(18, 0), tp(hm(18, 20)), i64(1200), i64(petrov), model.ResolutionMatched, ""},
		{"ваня", hm(18, 5), nil, nil, nil, model.ResolutionUnidentified, ""},
		{"иванов иван", hm(18, 10), tp(hm(18, 40)), i64(1800), i64(ivanov), model.ResolutionMatched, "left"},
		{"иванов иван", hm(18, 50), tp(hm(19, 30)), i64(2400), i64(ivanov), model.ResolutionMatched, ""},
	}
	if len(out.Intervals) != len(want) {
		t.Fatalf("intervals = %d, want %d", len(out.Intervals), len(want))
	}
	for i, w := range want {
		got := out.Intervals[i]
		if got.NormalizedName != w.name || !got.JoinAt.Equal(w.join) || got.Resolution != w.res ||
			got.LeaveReason != w.reason || got.MeetingUUID != sess1 {
			t.Errorf("interval %d = %+v, want %+v", i, got, w)
		}
		wantTime(t, "leave", got.LeaveAt, w.leave)
		wantInt(t, "duration", got.DurationSeconds, w.dur)
		wantInt(t, "student", got.StudentID, w.studentID)
	}
}
