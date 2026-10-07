package attendance

import (
	"maps"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"zoom-checker/internal/model"
)

// zoom.Event.OccurredAt() returns the zero time when a payload has neither a
// join/leave time nor event_ts; such events must not open intervals in year 1.
func TestZeroTimesAreIgnored(t *testing.T) {
	t.Parallel()
	zeroAt := func(e *ParticipantEvent) { e.At = time.Time{} }
	inp := baseInput(numbered(
		join("z", "Иванов Иван", hm(18, 0), zeroAt),
		leave("z2", "Иванов Иван", hm(18, 0), zeroAt),
		leave("q", "Петров Пётр", hm(18, 30), func(e *ParticipantEvent) { e.JoinTime = &time.Time{} }),
	)...)
	out := Compute(inp)

	wantStudent(t, student(t, out, ivanov), model.CurrentNotSeen, 0, model.FinalAbsent)
	if hasParticipant(out, "иванов иван") {
		t.Error("an event without any time must not create a participant")
	}
	// The zero join_time is treated as absent: a zero-length sighting at the leave.
	s := student(t, out, petrov)
	wantStudent(t, s, model.CurrentLeft, 0, model.FinalPresent)
	wantTime(t, "petrov first join", s.FirstJoinAt, tp(hm(18, 30)))
	wantInt(t, "petrov late", s.LateSeconds, i64(minutes(30)))
	if len(out.Intervals) != 1 {
		t.Fatalf("intervals = %+v, want one", out.Intervals)
	}
}

// TZ §20: early leave is measured against the end of the Zoom meeting. A teacher who
// presses "finish" after Zoom already ended must not make everyone "leave early".
func TestEarlyLeaveWhenFinishedAfterZoomEnded(t *testing.T) {
	t.Parallel()
	evs := numbered(
		join("a", "Иванов Иван", hm(18, 0)), // stays until Zoom ends
		join("b", "Петров Пётр", hm(18, 0)), leave("b", "Петров Пётр", hm(18, 50)),
	)

	t.Run("single session", func(t *testing.T) {
		t.Parallel()
		inp := baseInput(evs...)
		inp.Sessions[0].EndedAt = tp(hm(19, 0))
		inp.Lesson.ManualFinishedAt = tp(hm(19, 20))
		out := Compute(inp)
		wantTime(t, "actual end", out.ActualEnd, tp(hm(19, 20))) // rule 5 is unchanged
		wantInt(t, "ivanov", student(t, out, ivanov).EarlyLeaveSeconds, i64(0))
		wantInt(t, "petrov", student(t, out, petrov).EarlyLeaveSeconds, i64(minutes(10)))
	})

	t.Run("restarted meeting: the last session's end counts", func(t *testing.T) {
		t.Parallel()
		inp := baseInput(evs...)
		inp.Sessions[0].EndedAt = tp(hm(18, 55))
		inp.Sessions = append(inp.Sessions, Session{MeetingUUID: sess2, StartedAt: tp(hm(18, 56)), EndedAt: tp(hm(19, 5))})
		inp.Lesson.ManualFinishedAt = tp(hm(19, 20))
		out := Compute(inp)
		wantInt(t, "ivanov", student(t, out, ivanov).EarlyLeaveSeconds, i64(minutes(10)))
		wantInt(t, "petrov", student(t, out, petrov).EarlyLeaveSeconds, i64(minutes(15)))
	})

	t.Run("finish while Zoom runs: the finish time counts", func(t *testing.T) {
		t.Parallel()
		inp := baseInput(evs...)
		inp.Lesson.ManualFinishedAt = tp(hm(19, 20))
		out := Compute(inp)
		wantInt(t, "ivanov", student(t, out, ivanov).EarlyLeaveSeconds, i64(0))
		wantInt(t, "petrov", student(t, out, petrov).EarlyLeaveSeconds, i64(minutes(30)))
	})
}

func TestBreakoutOpenAtMeetingEnd(t *testing.T) {
	t.Parallel()
	inp := baseInput(numbered(
		join("a", "Иванов Иван", hm(18, 0)),
		leave("a", "Иванов Иван", hm(18, 20), ts(1)),
		join("a", "Иванов Иван", hm(18, 20), ts(2), bo()), // still in the room when Zoom ends
	)...)
	inp.Sessions[0].EndedAt = tp(hm(19, 0))
	inp.Now = hm(20, 0)
	out := Compute(inp)

	s := student(t, out, ivanov)
	wantStudent(t, s, model.CurrentLeft, minutes(60), model.FinalPresent)
	wantInt(t, "early leave", s.EarlyLeaveSeconds, i64(0))
	for _, iv := range out.Intervals {
		if iv.Breakout && (iv.LeaveAt == nil || !iv.LeaveAt.Equal(hm(19, 0)) || iv.LeaveReason != ReasonMeetingEnded) {
			t.Errorf("breakout interval %+v, want closed at 19:00 as %q", iv, ReasonMeetingEnded)
		}
	}
}

func TestSessionOrderDoesNotMatter(t *testing.T) {
	t.Parallel()
	want := Compute(richInput())
	inp := richInput()
	slices.Reverse(inp.Sessions)
	if got := Compute(inp); !reflect.DeepEqual(got, want) {
		t.Fatalf("reversing Sessions changed the output\n got: %+v\nwant: %+v", got, want)
	}
}

// cloneInput deep-copies everything Compute could possibly write to.
func cloneInput(in Input) Input {
	c := in
	if in.Lesson.ManualFinishedAt != nil {
		c.Lesson.ManualFinishedAt = tp(*in.Lesson.ManualFinishedAt)
	}
	c.Sessions = slices.Clone(in.Sessions)
	for i, s := range c.Sessions {
		if s.StartedAt != nil {
			c.Sessions[i].StartedAt = tp(*s.StartedAt)
		}
		if s.EndedAt != nil {
			c.Sessions[i].EndedAt = tp(*s.EndedAt)
		}
	}
	c.Events = slices.Clone(in.Events)
	for i, e := range c.Events {
		if e.JoinTime != nil {
			c.Events[i].JoinTime = tp(*e.JoinTime)
		}
	}
	c.Roster = slices.Clone(in.Roster)
	for i, r := range c.Roster {
		c.Roster[i].NormalizedAliases = slices.Clone(r.NormalizedAliases)
	}
	c.Mappings = maps.Clone(in.Mappings)
	c.IgnoredNames = maps.Clone(in.IgnoredNames)
	return c
}

func TestComputeDoesNotModifyInput(t *testing.T) {
	t.Parallel()
	inp := richInput()
	inp.Lesson.ManualFinishedAt = tp(hm(19, 20))
	inp.Roster[0].NormalizedAliases = []string{"ваня", "Иванов  И."}
	inp.Mappings["  ВАНЯ "] = Mapping{Action: model.MappingIgnore}
	before := cloneInput(inp)
	Compute(inp)
	if !reflect.DeepEqual(inp, before) {
		t.Fatal("Compute modified its input")
	}
}

// The output owns its pointers: changing them affects neither the input nor other
// rows (the processor may keep and adjust results).
func TestOutputDoesNotAliasInputOrItself(t *testing.T) {
	t.Parallel()
	inp := richInput()
	inp.Lesson.ManualFinishedAt = tp(hm(19, 20))
	before := cloneInput(inp)
	out := Compute(inp)
	want := Compute(cloneInput(inp))

	far := hm(23, 59)
	bump := func(p *time.Time) {
		if p != nil {
			*p = far
		}
	}
	bumpInt := func(p *int64) {
		if p != nil {
			*p = -1
		}
	}
	bump(out.ActualStart)
	bump(out.ActualEnd)
	for i := range out.Intervals {
		bump(out.Intervals[i].LeaveAt)
		bumpInt(out.Intervals[i].DurationSeconds)
		bumpInt(out.Intervals[i].StudentID)
	}
	for i := range out.Participants {
		bump(out.Participants[i].FirstJoinAt)
		bump(out.Participants[i].LastLeaveAt)
		bumpInt(out.Participants[i].StudentID)
		for j := range out.Participants[i].CandidateStudentIDs {
			out.Participants[i].CandidateStudentIDs[j] = -1
		}
	}
	for i := range out.Students {
		bump(out.Students[i].FirstJoinAt)
		bump(out.Students[i].LastLeaveAt)
		bumpInt(out.Students[i].LateSeconds)
		bumpInt(out.Students[i].EarlyLeaveSeconds)
		for j := range out.Students[i].ZoomNames {
			out.Students[i].ZoomNames[j] = "x"
		}
	}
	if !reflect.DeepEqual(inp, before) {
		t.Fatal("output pointers alias the input")
	}
	if got := Compute(inp); !reflect.DeepEqual(got, want) {
		t.Fatal("a later Compute sees changes made to an earlier output")
	}
}

// The processor may rebuild several lessons at once; Compute shares no state.
// Meaningful under -race.
func TestComputeConcurrentUse(t *testing.T) {
	t.Parallel()
	inp := richInput()
	want := Compute(inp)
	var wg sync.WaitGroup
	errs := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			if got := Compute(inp); !reflect.DeepEqual(got, want) {
				errs <- "concurrent Compute returned a different result"
			}
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

func TestHostDetectedByUserID(t *testing.T) {
	t.Parallel()
	inp := baseInput(numbered(
		join("h", "Учитель", hm(18, 0), uid(host)),
		join("g", "Гость", hm(18, 1), uid("someone-else")),
	)...)
	out := Compute(inp)
	if p := participant(t, out, "учитель"); p.Resolution != model.ResolutionIgnored || p.IgnoreReason != model.IgnoreHost {
		t.Errorf("host by user_id: %q/%q, want ignored/host", p.Resolution, p.IgnoreReason)
	}
	if p := participant(t, out, "гость"); p.Resolution != model.ResolutionUnidentified {
		t.Errorf("guest: %q, want unidentified", p.Resolution)
	}

	t.Run("session without a host id ignores nobody", func(t *testing.T) {
		t.Parallel()
		inp := baseInput(join("h", "Учитель", hm(18, 0), uid(""), pid("")))
		inp.Sessions[0].HostID = ""
		if p := participant(t, Compute(inp), "учитель"); p.Resolution != model.ResolutionUnidentified {
			t.Errorf("resolution = %q, want unidentified", p.Resolution)
		}
	})
}

func TestLeaveAfterManualFinishIsClipped(t *testing.T) {
	t.Parallel()
	inp := baseInput(numbered(
		join("a", "Иванов Иван", hm(18, 0)),
		leave("a", "Иванов Иван", hm(19, 25), reason("left the meeting")),
	)...)
	inp.Lesson.ManualFinishedAt = tp(hm(19, 20))
	inp.Now = hm(20, 0)
	out := Compute(inp)

	ivs := intervalsOf(out, "иванов иван")
	if len(ivs) != 1 {
		t.Fatalf("intervals = %+v", ivs)
	}
	wantTime(t, "leave", ivs[0].LeaveAt, tp(hm(19, 20)))
	if ivs[0].LeaveReason != "left the meeting" {
		t.Errorf("leave reason = %q, want Zoom's own", ivs[0].LeaveReason)
	}
	wantStudent(t, student(t, out, ivanov), model.CurrentLeft, minutes(80), model.FinalPresent)
}

func TestParticipantTimesAndDevices(t *testing.T) {
	t.Parallel()
	inp := baseInput(numbered(
		join("pc", "Ваня", hm(18, 5)), leave("pc", "Ваня", hm(18, 25)),
		join("phone", "ваня", hm(18, 10)), leave("phone", "ваня", hm(18, 15)),
		join("pc2", "Ваня", hm(18, 30)), // back on the PC with a new participant_uuid
	)...)
	inp.Now = hm(18, 40)
	out := Compute(inp)

	p := participant(t, out, "ваня")
	wantTime(t, "first join", p.FirstJoinAt, tp(hm(18, 5)))
	wantTime(t, "last leave", p.LastLeaveAt, tp(hm(18, 25)))
	if p.CurrentStatus != model.CurrentOnline || p.Devices != 3 || p.OnlineDevices != 1 {
		t.Errorf("status=%q devices=%d online=%d, want online/3/1", p.CurrentStatus, p.Devices, p.OnlineDevices)
	}
	if p.TotalSeconds != minutes(30) { // 18:05–18:25 ∪ 18:10–18:15 ∪ 18:30–18:40
		t.Errorf("total = %d, want %d", p.TotalSeconds, minutes(30))
	}
	if p.DisplayName != "Ваня" {
		t.Errorf("display name = %q, want the latest join's raw name", p.DisplayName)
	}
}

func TestRenameChain(t *testing.T) {
	t.Parallel()
	// Join as "A", re-join the same device as "B" (closes A), leave as the student.
	out := Compute(baseInput(numbered(
		join("pc", "A", hm(18, 0)),
		join("pc", "B", hm(18, 10)),
		leave("pc", "Иванов Иван", hm(18, 20)),
	)...))

	a := intervalsOf(out, "a")
	if len(a) != 1 || a[0].LeaveReason != ReasonRenamed || !a[0].LeaveAt.Equal(hm(18, 10)) {
		t.Fatalf("A intervals = %+v", a)
	}
	iv := intervalsOf(out, "иванов иван")
	if len(iv) != 1 || iv[0].RenamedFrom != "B" || !iv[0].JoinAt.Equal(hm(18, 10)) {
		t.Fatalf("student intervals = %+v, want one from 18:10 renamed from B", iv)
	}
	if hasParticipant(out, "b") {
		t.Error(`"b" must not stay a participant`)
	}
	s := student(t, out, ivanov)
	wantStudent(t, s, model.CurrentLeft, minutes(10), model.FinalPresent)
	wantInt(t, "late", s.LateSeconds, i64(minutes(10)))
}

// A caller-supplied Key and one derived from padded identifiers name the same lane,
// exactly like zoom.Participant.Key() (which trims).
func TestDerivedKeyMatchesZoomKey(t *testing.T) {
	t.Parallel()
	out := Compute(baseInput(numbered(
		join("U1", "Иванов Иван", hm(18, 0)), // Key "puuid:U1"
		leave("", "Иванов Иван", hm(18, 30), func(e *ParticipantEvent) { e.Key, e.ParticipantUUID = "", " U1 " }),
	)...))
	if len(out.Intervals) != 1 || out.Intervals[0].LeaveAt == nil {
		t.Fatalf("intervals = %+v, want one closed interval", out.Intervals)
	}
	wantStudent(t, student(t, out, ivanov), model.CurrentLeft, minutes(30), model.FinalPresent)
}
