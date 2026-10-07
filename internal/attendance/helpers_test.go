package attendance

import (
	"testing"
	"time"

	"zoom-checker/internal/model"
)

// Lesson day used by all tests; times are UTC.
var day = time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)

func hm(h, m int) time.Time { return hms(h, m, 0) }

func hms(h, m, s int) time.Time {
	return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(s)*time.Second)
}

func tp(t time.Time) *time.Time { return &t }

func minutes(n int64) int64 { return n * 60 }

const (
	sess1 = "sess-1"
	sess2 = "sess-2"
	host  = "host-user-id"

	ivanov     int64 = 1 // Иванов Иван
	petrov     int64 = 2 // Петров Пётр
	sidorov    int64 = 3 // Сидоров Алексей
	alexandrov int64 = 4 // Александров Александр
)

type evOpt func(*ParticipantEvent)

func in(uuid string) evOpt       { return func(e *ParticipantEvent) { e.MeetingUUID = uuid } }
func bo() evOpt                  { return func(e *ParticipantEvent) { e.Breakout = true } }
func ts(ms int64) evOpt          { return func(e *ParticipantEvent) { e.EventTS = ms } }
func reason(r string) evOpt      { return func(e *ParticipantEvent) { e.LeaveReason = r } }
func joinedAt(t time.Time) evOpt { return func(e *ParticipantEvent) { e.JoinTime = tp(t) } }
func pid(id string) evOpt        { return func(e *ParticipantEvent) { e.ParticipantID = id } }
func uid(id string) evOpt        { return func(e *ParticipantEvent) { e.UserID = id } }
func eventID(id int64) evOpt     { return func(e *ParticipantEvent) { e.EventID = id } }
func apply(e ParticipantEvent, o ...evOpt) ParticipantEvent {
	for _, f := range o {
		f(&e)
	}
	return e
}

// ev builds an event in sess1 whose key is derived from the given device key.
func ev(k EventKind, key, userName string, t time.Time, o ...evOpt) ParticipantEvent {
	e := ParticipantEvent{
		Kind:            k,
		MeetingUUID:     sess1,
		At:              t,
		Key:             "puuid:" + key,
		ParticipantUUID: key,
		UserName:        userName,
	}
	return apply(e, o...)
}

func join(key, userName string, t time.Time, o ...evOpt) ParticipantEvent {
	return ev(Join, key, userName, t, o...)
}

func leave(key, userName string, t time.Time, o ...evOpt) ParticipantEvent {
	return ev(Leave, key, userName, t, o...)
}

// numbered assigns increasing EventIDs in slice order (= arrival order).
func numbered(evs ...ParticipantEvent) []ParticipantEvent {
	for i := range evs {
		if evs[i].EventID == 0 {
			evs[i].EventID = int64(i + 1)
		}
	}
	return evs
}

func roster() []RosterStudent {
	return []RosterStudent{
		{StudentID: ivanov, FullName: "Иванов Иван", NormalizedName: "иванов иван", Active: true},
		{StudentID: petrov, FullName: "Петров Пётр", NormalizedName: "петров петр", Active: true},
		{StudentID: sidorov, FullName: "Сидоров Алексей", NormalizedName: "сидоров алексей", Active: true},
		{StudentID: alexandrov, FullName: "Александров Александр", NormalizedName: "александров александр", Active: true},
	}
}

// baseInput is a lesson planned 18:00–19:30 with one running session started at 18:00.
func baseInput(events ...ParticipantEvent) Input {
	return Input{
		Lesson: LessonInfo{ID: 10, PlannedStart: hm(18, 0), PlannedEnd: hm(19, 30)},
		Sessions: []Session{
			{MeetingUUID: sess1, HostID: host, StartedAt: tp(hm(18, 0)), FirstEventAt: hm(17, 58)},
		},
		Events:     events,
		Roster:     roster(),
		IgnoreHost: true,
		Now:        hm(19, 0),
	}
}

func student(t *testing.T, out Output, id int64) StudentResult {
	t.Helper()
	for _, s := range out.Students {
		if s.StudentID == id {
			return s
		}
	}
	t.Fatalf("student %d not in output", id)
	return StudentResult{}
}

func participant(t *testing.T, out Output, normalized string) ParticipantResult {
	t.Helper()
	for _, p := range out.Participants {
		if p.NormalizedName == normalized {
			return p
		}
	}
	t.Fatalf("participant %q not in output; have %v", normalized, participantNames(out))
	return ParticipantResult{}
}

func participantNames(out Output) []string {
	var ns []string
	for _, p := range out.Participants {
		ns = append(ns, p.NormalizedName)
	}
	return ns
}

func hasParticipant(out Output, normalized string) bool {
	for _, p := range out.Participants {
		if p.NormalizedName == normalized {
			return true
		}
	}
	return false
}

func intervalsOf(out Output, normalized string) []IntervalResult {
	var ivs []IntervalResult
	for _, iv := range out.Intervals {
		if iv.NormalizedName == normalized {
			ivs = append(ivs, iv)
		}
	}
	return ivs
}

func wantStudent(t *testing.T, s StudentResult, status model.CurrentStatus, total int64, auto model.FinalStatus) {
	t.Helper()
	if s.CurrentStatus != status {
		t.Errorf("student %d: current status = %q, want %q", s.StudentID, s.CurrentStatus, status)
	}
	if s.TotalSeconds != total {
		t.Errorf("student %d: total = %d s (%.1f min), want %d s (%.1f min)",
			s.StudentID, s.TotalSeconds, float64(s.TotalSeconds)/60, total, float64(total)/60)
	}
	if s.AutoStatus != auto {
		t.Errorf("student %d: auto status = %q, want %q", s.StudentID, s.AutoStatus, auto)
	}
}

func wantTime(t *testing.T, what string, got *time.Time, want *time.Time) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil || want == nil:
		t.Errorf("%s = %v, want %v", what, fmtTime(got), fmtTime(want))
	case !got.Equal(*want):
		t.Errorf("%s = %v, want %v", what, fmtTime(got), fmtTime(want))
	}
}

func wantInt(t *testing.T, what string, got *int64, want *int64) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil || want == nil:
		t.Errorf("%s = %v, want %v", what, fmtInt(got), fmtInt(want))
	case *got != *want:
		t.Errorf("%s = %d, want %d", what, *got, *want)
	}
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return "<nil>"
	}
	return t.Format("15:04:05")
}

func fmtInt(v *int64) string {
	if v == nil {
		return "<nil>"
	}
	return time.Duration(*v * int64(time.Second)).String()
}

func i64(v int64) *int64 { return &v }
