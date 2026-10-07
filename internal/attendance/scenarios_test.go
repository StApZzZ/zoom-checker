package attendance

import (
	"testing"

	"zoom-checker/internal/model"
)

// TZ §36, scenario 1: a student joining with the exact roster name is online.
func TestScenario1_ExactNameIsOnline(t *testing.T) {
	t.Parallel()
	inp := baseInput(join("pc", "Иванов Иван", hm(18, 2)))
	inp.Now = hm(18, 5)

	out := Compute(inp)

	s := student(t, out, ivanov)
	wantStudent(t, s, model.CurrentOnline, minutes(3), model.FinalPresent)
	if !s.Seen || s.OnlineDevices != 1 {
		t.Errorf("seen=%v online devices=%d, want true/1", s.Seen, s.OnlineDevices)
	}
	p := participant(t, out, "иванов иван")
	if p.Resolution != model.ResolutionMatched || p.StudentID == nil || *p.StudentID != ivanov {
		t.Errorf("participant resolution=%q student=%v, want matched/%d", p.Resolution, p.StudentID, ivanov)
	}
	if p.CurrentStatus != model.CurrentOnline {
		t.Errorf("participant status = %q, want online", p.CurrentStatus)
	}
	if out.Status != model.LessonActive {
		t.Errorf("lesson status = %q, want active", out.Status)
	}
}

// TZ §36, scenario 2 (+ §7): spacing, case, ё, NBSP and invisible characters do not
// prevent the match.
func TestScenario2_NormalizedNameMatches(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"  ИВАНОВ   ИВАН ",
		"иванов иван",
		"Иванов Иван",
		"Ива​нов Иван",
		"\tИванов\n Иван",
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			out := Compute(baseInput(join("pc", raw, hm(18, 0))))
			p := participant(t, out, "иванов иван")
			if p.Resolution != model.ResolutionMatched || *p.StudentID != ivanov {
				t.Fatalf("resolution=%q student=%v, want matched/%d", p.Resolution, p.StudentID, ivanov)
			}
			if p.DisplayName != raw {
				t.Errorf("display name = %q, want the raw Zoom name %q", p.DisplayName, raw)
			}
			wantStudent(t, student(t, out, ivanov), model.CurrentOnline, minutes(60), model.FinalPresent)
		})
	}

	t.Run("ё in roster and in Zoom", func(t *testing.T) {
		t.Parallel()
		inp := baseInput(join("pc", "ПЕТРОВ ПЁТР", hm(18, 0)))
		out := Compute(inp)
		if p := participant(t, out, "петров петр"); p.Resolution != model.ResolutionMatched || *p.StudentID != petrov {
			t.Fatalf("resolution=%q student=%v", p.Resolution, p.StudentID)
		}
	})
}

// TZ §36, scenario 3: an unknown name is listed as unidentified and nobody is marked present.
func TestScenario3_UnknownNameIsUnidentified(t *testing.T) {
	t.Parallel()
	out := Compute(baseInput(join("phone", "Ваня", hm(18, 3))))

	p := participant(t, out, "ваня")
	if p.Resolution != model.ResolutionUnidentified || p.StudentID != nil {
		t.Fatalf("resolution=%q student=%v, want unidentified/nil", p.Resolution, p.StudentID)
	}
	if p.CurrentStatus != model.CurrentOnline || p.TotalSeconds != minutes(57) {
		t.Errorf("status=%q total=%d, want online/%d", p.CurrentStatus, p.TotalSeconds, minutes(57))
	}
	for _, s := range out.Students {
		wantStudent(t, s, model.CurrentNotSeen, 0, model.FinalAbsent)
		if s.Seen || s.FirstJoinAt != nil || s.LateSeconds != nil {
			t.Errorf("student %d must stay unseen: %+v", s.StudentID, s)
		}
	}
	if iv := out.Intervals[0]; iv.StudentID != nil || iv.Resolution != model.ResolutionUnidentified {
		t.Errorf("interval student=%v resolution=%q", iv.StudentID, iv.Resolution)
	}
}

// TZ §36, scenario 4: 18:10–18:40 + 18:50–19:30 = 70 minutes.
func TestScenario4_RejoinSumsIntervals(t *testing.T) {
	t.Parallel()
	cases := map[string][]ParticipantEvent{
		// Real Zoom: participant_uuid changes on every rejoin.
		"new key per join": {
			join("j1", "Иванов Иван", hm(18, 10)), leave("j1", "Иванов Иван", hm(18, 40)),
			join("j2", "Иванов Иван", hm(18, 50)), leave("j2", "Иванов Иван", hm(19, 30)),
		},
		"same key": {
			join("pc", "Иванов Иван", hm(18, 10)), leave("pc", "Иванов Иван", hm(18, 40)),
			join("pc", "Иванов Иван", hm(18, 50)), leave("pc", "Иванов Иван", hm(19, 30)),
		},
	}
	for name, evs := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			inp := baseInput(numbered(evs...)...)
			inp.Now = hm(20, 0)
			out := Compute(inp)

			s := student(t, out, ivanov)
			wantStudent(t, s, model.CurrentLeft, 4200, model.FinalPresent)
			wantTime(t, "first join", s.FirstJoinAt, tp(hm(18, 10)))
			wantTime(t, "last leave", s.LastLeaveAt, tp(hm(19, 30)))
			if n := len(out.Participants); n != 1 {
				t.Fatalf("participants = %d, want 1 (a rejoin is not a new student)", n)
			}
			ivs := intervalsOf(out, "иванов иван")
			if len(ivs) != 2 {
				t.Fatalf("intervals = %d, want 2", len(ivs))
			}
			if *ivs[0].DurationSeconds != 1800 || *ivs[1].DurationSeconds != 2400 {
				t.Errorf("durations = %d, %d; want 1800, 2400", *ivs[0].DurationSeconds, *ivs[1].DurationSeconds)
			}
		})
	}
}

// TZ §36, scenario 5 (+ §32): PC 18:00–19:00 and phone 18:30–19:30 = 90 minutes, not 120.
func TestScenario5_TwoDevicesOverlapCountedOnce(t *testing.T) {
	t.Parallel()
	inp := baseInput(numbered(
		join("pc", "Иванов Иван", hm(18, 0)),
		join("phone", "иванов иван", hm(18, 30)),
		leave("pc", "Иванов Иван", hm(19, 0)),
		leave("phone", "иванов иван", hm(19, 30)),
	)...)
	inp.Now = hm(20, 0)
	out := Compute(inp)

	wantStudent(t, student(t, out, ivanov), model.CurrentLeft, minutes(90), model.FinalPresent)
	p := participant(t, out, "иванов иван")
	if p.TotalSeconds != minutes(90) || p.Devices != 2 {
		t.Errorf("participant total=%d devices=%d, want %d/2", p.TotalSeconds, p.Devices, minutes(90))
	}
	if len(out.Participants) != 1 {
		t.Errorf("two devices with one name must be one participant, got %v", participantNames(out))
	}

	// While both devices are online.
	inp.Now = hm(18, 45)
	inp.Events = numbered(join("pc", "Иванов Иван", hm(18, 0)), join("phone", "иванов иван", hm(18, 30)))
	out = Compute(inp)
	s := student(t, out, ivanov)
	if s.OnlineDevices != 2 || s.TotalSeconds != minutes(45) {
		t.Errorf("online devices=%d total=%d, want 2/%d", s.OnlineDevices, s.TotalSeconds, minutes(45))
	}
}

// TZ §36, scenario 6: Zoom re-sent the same webhook; no duplicate presence appears.
func TestScenario6_RepeatedWebhookNoDuplicate(t *testing.T) {
	t.Parallel()
	j := join("pc", "Иванов Иван", hm(18, 0))
	l := leave("pc", "Иванов Иван", hm(18, 40))
	inp := baseInput(numbered(j, j, l, l, j)...) // the late copy of the join arrives after the leave
	inp.Now = hm(19, 0)
	out := Compute(inp)

	if n := len(out.Intervals); n != 1 {
		t.Fatalf("intervals = %d, want 1: %+v", n, out.Intervals)
	}
	wantStudent(t, student(t, out, ivanov), model.CurrentLeft, minutes(40), model.FinalPresent)
}

// TZ §36, scenario 7 (+ §17): meeting.ended closes every open interval at the end time.
func TestScenario7_MeetingEndClosesOpenIntervals(t *testing.T) {
	t.Parallel()
	inp := baseInput(numbered(
		join("a", "Иванов Иван", hm(18, 0)),
		join("b", "Петров Пётр", hm(18, 5)),
		join("c", "Ваня", hm(18, 10)),
		join("d", "Сидоров Алексей", hm(18, 15)),
		leave("d", "Сидоров Алексей", hm(19, 0)),
	)...)
	inp.Sessions[0].EndedAt = tp(hm(19, 30))
	inp.Now = hm(21, 0) // long after the end: totals must not grow

	out := Compute(inp)

	if out.Status != model.LessonFinished {
		t.Fatalf("lesson status = %q, want finished", out.Status)
	}
	wantTime(t, "actual end", out.ActualEnd, tp(hm(19, 30)))
	wantStudent(t, student(t, out, ivanov), model.CurrentLeft, minutes(90), model.FinalPresent)
	wantStudent(t, student(t, out, petrov), model.CurrentLeft, minutes(85), model.FinalPresent)
	wantStudent(t, student(t, out, sidorov), model.CurrentLeft, minutes(45), model.FinalPresent)
	wantStudent(t, student(t, out, alexandrov), model.CurrentNotSeen, 0, model.FinalAbsent)
	for _, iv := range out.Intervals {
		if iv.LeaveAt == nil {
			t.Fatalf("interval of %q still open after meeting.ended", iv.NormalizedName)
		}
		if iv.NormalizedName != "сидоров алексей" {
			wantTime(t, iv.NormalizedName+" leave", iv.LeaveAt, tp(hm(19, 30)))
			if iv.LeaveReason != ReasonMeetingEnded {
				t.Errorf("%s leave reason = %q, want %q", iv.NormalizedName, iv.LeaveReason, ReasonMeetingEnded)
			}
		}
	}
	if p := participant(t, out, "ваня"); p.CurrentStatus != model.CurrentLeft || p.OnlineDevices != 0 {
		t.Errorf("unidentified participant status=%q online=%d after end", p.CurrentStatus, p.OnlineDevices)
	}

	t.Run("trailing leaves after the end are clipped", func(t *testing.T) {
		t.Parallel()
		inp := inp
		inp.Events = append(numbered(inp.Events...),
			leave("a", "Иванов Иван", hms(19, 30, 4), reason("meeting ended by host"), eventID(10)))
		out := Compute(inp)
		ivs := intervalsOf(out, "иванов иван")
		if len(ivs) != 1 {
			t.Fatalf("intervals = %d, want 1", len(ivs))
		}
		wantTime(t, "leave", ivs[0].LeaveAt, tp(hm(19, 30)))
		if ivs[0].LeaveReason != "meeting ended by host" {
			t.Errorf("leave reason = %q, want Zoom's own reason", ivs[0].LeaveReason)
		}
		wantStudent(t, student(t, out, ivanov), model.CurrentLeft, minutes(90), model.FinalPresent)
	})
}

// TZ §36, scenario 8 (+ §12): a manual mapping moves the whole accumulated time of an
// unidentified name to the student.
func TestScenario8_ManualMappingTransfersTime(t *testing.T) {
	t.Parallel()
	inp := baseInput(numbered(
		join("phone", "Александр", hm(18, 5)),
		leave("phone", "Александр", hm(18, 45)),
		join("phone2", "Александр", hm(18, 50)),
	)...)
	inp.Now = hm(19, 10)

	before := Compute(inp)
	p := participant(t, before, "александр")
	if p.Resolution != model.ResolutionUnidentified || p.TotalSeconds != minutes(60) {
		t.Fatalf("before mapping: resolution=%q total=%d", p.Resolution, p.TotalSeconds)
	}
	wantStudent(t, student(t, before, alexandrov), model.CurrentNotSeen, 0, model.FinalAbsent)

	inp.Mappings = map[string]Mapping{"александр": {Action: model.MappingStudent, StudentID: alexandrov}}
	after := Compute(inp)

	s := student(t, after, alexandrov)
	wantStudent(t, s, model.CurrentOnline, minutes(60), model.FinalPresent)
	wantTime(t, "first join", s.FirstJoinAt, tp(hm(18, 5)))
	wantInt(t, "late", s.LateSeconds, i64(minutes(5)))
	if len(s.ZoomNames) != 1 || s.ZoomNames[0] != "Александр" {
		t.Errorf("zoom names = %v, want [Александр]", s.ZoomNames)
	}
	p = participant(t, after, "александр")
	if p.Resolution != model.ResolutionManual || *p.StudentID != alexandrov {
		t.Errorf("after mapping: resolution=%q student=%v", p.Resolution, p.StudentID)
	}
	for _, iv := range after.Intervals {
		if iv.StudentID == nil || *iv.StudentID != alexandrov || iv.Resolution != model.ResolutionManual {
			t.Errorf("interval %+v not transferred to the student", iv)
		}
	}

	t.Run("union with the student's own correctly named device", func(t *testing.T) {
		t.Parallel()
		inp := inp
		inp.Events = append(numbered(inp.Events...),
			join("pc", "Александров Александр", hm(18, 30), eventID(20)),
			leave("pc", "Александров Александр", hm(19, 0), eventID(21)))
		out := Compute(inp)
		// 18:05–18:45 ∪ 18:30–19:00 ∪ 18:50–19:10 = 18:05–19:10.
		wantStudent(t, student(t, out, alexandrov), model.CurrentOnline, minutes(65), model.FinalPresent)
		if got := student(t, out, alexandrov).ZoomNames; len(got) != 2 {
			t.Errorf("zoom names = %v, want both names", got)
		}
	})
}

// TZ §36, scenario 9: the result contains every student of the group, including those
// who never connected (the export is built from it).
func TestScenario9_AllRosterStudentsInResult(t *testing.T) {
	t.Parallel()
	inp := baseInput(numbered(
		join("a", "Петров Пётр", hm(18, 0)),
		leave("a", "Петров Пётр", hm(19, 0)),
	)...)
	inp.Sessions[0].EndedAt = tp(hm(19, 30))
	out := Compute(inp)

	if len(out.Students) != len(inp.Roster) {
		t.Fatalf("students = %d, want %d", len(out.Students), len(inp.Roster))
	}
	for i, s := range out.Students {
		if s.StudentID != inp.Roster[i].StudentID {
			t.Errorf("students[%d] = %d, want roster order (%d)", i, s.StudentID, inp.Roster[i].StudentID)
		}
		if s.StudentID == petrov {
			continue
		}
		wantStudent(t, s, model.CurrentNotSeen, 0, model.FinalAbsent)
		if s.ZoomNames == nil || len(s.ZoomNames) != 0 {
			t.Errorf("student %d zoom names = %#v, want empty non-nil", s.StudentID, s.ZoomNames)
		}
		if s.FirstJoinAt != nil || s.LastLeaveAt != nil || s.LateSeconds != nil || s.EarlyLeaveSeconds != nil {
			t.Errorf("unseen student %d has times: %+v", s.StudentID, s)
		}
	}
	wantStudent(t, student(t, out, petrov), model.CurrentLeft, minutes(60), model.FinalPresent)
}
