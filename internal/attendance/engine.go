package attendance

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"zoom-checker/internal/model"
)

// Compute builds the lesson projection from in (docs/architecture.md §5.4).
// It is deterministic: Students follow Roster order, Participants are sorted by first
// join then display name, Intervals by join time then device key, and the order of
// in.Events does not matter. in is not modified.
func Compute(in Input) Output {
	out := Output{AsOf: in.Now}
	out.ActualStart, out.ActualEnd, out.Status = lessonState(in.Lesson, in.Sessions)

	sessions := make(map[string]*Session, len(in.Sessions))
	order := make([]string, 0, len(in.Sessions))
	for i := range in.Sessions {
		s := &in.Sessions[i]
		if _, dup := sessions[s.MeetingUUID]; dup {
			continue // the first row for a UUID wins
		}
		sessions[s.MeetingUUID] = s
		order = append(order, s.MeetingUUID)
	}

	// Group events by session. Events of an unknown UUID are kept (as a session with
	// no host and no end of its own) rather than silently dropped.
	byMeeting := make(map[string][]*ParticipantEvent, len(order))
	var unknown []string
	for i := range in.Events {
		ev := &in.Events[i]
		if ev.Kind != Join && ev.Kind != Leave {
			continue
		}
		if ev.At.IsZero() {
			// No join/leave time and no event_ts (zoom.Event.OccurredAt() returns zero):
			// the event cannot be placed in time, and keeping it would open an interval
			// in year 1 and inflate totals to ~292 years.
			continue
		}
		if _, ok := sessions[ev.MeetingUUID]; !ok {
			if _, seen := byMeeting[ev.MeetingUUID]; !seen {
				unknown = append(unknown, ev.MeetingUUID)
			}
		}
		byMeeting[ev.MeetingUUID] = append(byMeeting[ev.MeetingUUID], ev)
	}
	slices.Sort(unknown)
	order = append(order, unknown...)

	b := newBuilder(len(in.Events))
	// zoomEnd is the latest moment a session closes. Once the lesson is finished every
	// session has an end (its EndedAt or the lesson end), so it is then the end of Zoom.
	var zoomEnd *time.Time
	for _, uuid := range order {
		s := sessions[uuid]
		end, reason := sessionEnd(s, out.ActualEnd)
		if end != nil && (zoomEnd == nil || end.After(*zoomEnd)) {
			zoomEnd = end
		}
		evs := byMeeting[uuid]
		if len(evs) == 0 {
			continue
		}
		hostID := ""
		if s != nil {
			hostID = s.HostID
		}
		b.runSession(uuid, hostID, evs, end, reason)
	}

	hosts := make(map[string]bool)
	for i := range b.ivs {
		if b.ivs[i].host {
			hosts[b.ivs[i].name] = true
			if b.ivs[i].joinName != "" {
				hosts[b.ivs[i].joinName] = true
			}
		}
	}
	r := newResolver(&in, hosts)

	resolved := make(map[string]resolution)
	resolve := func(name string) resolution {
		res, ok := resolved[name]
		if !ok {
			res = r.resolve(name)
			resolved[name] = res
		}
		return res
	}
	keepIdentifiedJoinNames(b.ivs, resolve)

	nameAggs := make(map[string]*aggregate)
	studentAggs := make(map[int64]*aggregate)
	for i := range b.ivs {
		iv := &b.ivs[i]
		res := resolve(iv.name)
		aggFor(nameAggs, iv.name).add(iv, in.Now)
		if res.hasStudent {
			aggFor(studentAggs, res.student).add(iv, in.Now)
		}
	}

	// Early leave is measured against the end of the Zoom meeting (TZ §20): the lesson
	// end, except when every session had already ended before the teacher pressed
	// "finish" — then a student who stayed until Zoom closed did not leave early.
	earlyRef := out.ActualEnd
	if earlyRef != nil && zoomEnd != nil && zoomEnd.Before(*earlyRef) {
		earlyRef = zoomEnd
	}

	out.Intervals = buildIntervals(b.ivs, resolved)
	out.Participants = buildParticipants(nameAggs, resolved, b.hints)
	out.Students = buildStudents(&in, &out, studentAggs, earlyRef)
	return out
}

// keepIdentifiedJoinNames undoes the rename at leave (intervals.go leave) of
// an interval whose join name is a roster student while its leave name is not
// that student (unidentified, ignored, a conflict or another student): a
// student identified once stays seen (TZ §18), and the result must not
// contradict what the teacher saw live. The leave name then gets no time.
func keepIdentifiedJoinNames(ivs []interval, resolve func(string) resolution) {
	for i := range ivs {
		iv := &ivs[i]
		if iv.joinName == "" || iv.joinName == iv.name {
			continue
		}
		join := resolve(iv.joinName)
		if !join.hasStudent {
			continue
		}
		if leave := resolve(iv.name); leave.hasStudent && leave.student == join.student {
			continue
		}
		iv.name, iv.rawName, iv.renamedFrom, iv.joinName = iv.joinName, iv.renamedFrom, "", ""
	}
}

// lessonState derives actual start/end and status (rule 5).
func lessonState(l LessonInfo, sessions []Session) (start, end *time.Time, status model.LessonStatus) {
	allEnded := len(sessions) > 0
	var maxEnd time.Time
	for _, s := range sessions {
		ref := s.FirstEventAt
		if s.StartedAt != nil {
			ref = *s.StartedAt
		}
		if !ref.IsZero() && (start == nil || ref.Before(*start)) {
			start = ptr(ref)
		}
		if s.EndedAt == nil {
			allEnded = false
		} else if s.EndedAt.After(maxEnd) {
			maxEnd = *s.EndedAt
		}
	}
	switch {
	case l.ManualFinishedAt != nil:
		end = ptr(*l.ManualFinishedAt)
	case allEnded:
		end = ptr(maxEnd)
	}
	switch {
	case end != nil:
		status = model.LessonFinished
	case len(sessions) > 0:
		status = model.LessonActive
	default:
		status = model.LessonPlanned
	}
	return start, end, status
}

// sessionEnd is when the open intervals of a session close: the earlier of its own
// EndedAt and the lesson end (the teacher's "finish", or the end of the other
// sessions for events of an unknown session). A manual finish therefore stays final
// even when meeting.ended arrives later: joins after it are ignored and leaves are
// clipped to it. nil while the session is running and the lesson is not finished.
func sessionEnd(s *Session, lessonEnd *time.Time) (*time.Time, string) {
	if s != nil && s.EndedAt != nil && (lessonEnd == nil || !s.EndedAt.After(*lessonEnd)) {
		return s.EndedAt, ReasonMeetingEnded
	}
	if lessonEnd != nil {
		return lessonEnd, ReasonLessonFinished
	}
	return nil, ""
}

// aggregate accumulates the intervals of one name or one student.
type aggregate struct {
	segs     []segment
	keys     map[string]bool
	online   map[string]bool
	first    time.Time
	last     time.Time
	hasFirst bool
	hasLast  bool
}

func aggFor[K comparable](m map[K]*aggregate, k K) *aggregate {
	a := m[k]
	if a == nil {
		a = &aggregate{keys: make(map[string]bool), online: make(map[string]bool)}
		m[k] = a
	}
	return a
}

func (a *aggregate) add(iv *interval, now time.Time) {
	a.keys[iv.key] = true
	if !a.hasFirst || iv.join.Before(a.first) {
		a.first, a.hasFirst = iv.join, true
	}
	end := now
	if iv.closed {
		end = iv.leave
		if !a.hasLast || iv.leave.After(a.last) {
			a.last, a.hasLast = iv.leave, true
		}
	} else {
		a.online[iv.key] = true
	}
	a.segs = appendSegment(a.segs, iv.join, end)
}

func (a *aggregate) currentStatus() model.CurrentStatus {
	if len(a.online) > 0 {
		return model.CurrentOnline
	}
	return model.CurrentLeft
}

func (a *aggregate) lastLeave() *time.Time {
	if !a.hasLast {
		return nil
	}
	return ptr(a.last)
}

func buildIntervals(ivs []interval, resolved map[string]resolution) []IntervalResult {
	out := make([]IntervalResult, 0, len(ivs))
	// Backing storage for the pointer fields: one allocation each instead of one per
	// interval. Capacity is exact, so appends never move earlier elements.
	leaves := make([]time.Time, 0, len(ivs))
	durations := make([]int64, 0, len(ivs))
	studentIDs := make([]int64, 0, len(ivs))
	for i := range ivs {
		iv := &ivs[i]
		res := resolved[iv.name]
		ir := IntervalResult{
			NormalizedName:  iv.name,
			ParticipantKey:  iv.key,
			ParticipantUUID: iv.puuid,
			UserName:        iv.rawName,
			RenamedFrom:     iv.renamedFrom,
			MeetingUUID:     iv.meetingUUID,
			JoinAt:          iv.join,
			LeaveReason:     iv.reason,
			Resolution:      res.res,
			Breakout:        iv.breakout,
		}
		if iv.closed {
			leaves = append(leaves, iv.leave)
			ir.LeaveAt = &leaves[len(leaves)-1]
			durations = append(durations, seconds(iv.leave.Sub(iv.join)))
			ir.DurationSeconds = &durations[len(durations)-1]
		}
		if res.hasStudent {
			studentIDs = append(studentIDs, res.student)
			ir.StudentID = &studentIDs[len(studentIDs)-1]
		}
		out = append(out, ir)
	}
	slices.SortFunc(out, func(a, b IntervalResult) int {
		return cmp.Or(
			a.JoinAt.Compare(b.JoinAt),
			strings.Compare(a.ParticipantKey, b.ParticipantKey),
			compareBool(a.Breakout, b.Breakout),
			strings.Compare(a.MeetingUUID, b.MeetingUUID),
			compareLeave(a.LeaveAt, b.LeaveAt),
			strings.Compare(a.NormalizedName, b.NormalizedName),
		)
	})
	return out
}

// compareLeave orders closed intervals by leave time and open ones last.
func compareLeave(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	}
	return a.Compare(*b)
}

func buildParticipants(aggs map[string]*aggregate, resolved map[string]resolution, hints map[string]nameHint) []ParticipantResult {
	out := make([]ParticipantResult, 0, len(aggs))
	for name, a := range aggs {
		res := resolved[name]
		p := ParticipantResult{
			NormalizedName:      name,
			DisplayName:         hints[name].raw,
			Resolution:          res.res,
			IgnoreReason:        res.reason,
			CandidateStudentIDs: res.candidates,
			CurrentStatus:       a.currentStatus(),
			FirstJoinAt:         ptr(a.first),
			LastLeaveAt:         a.lastLeave(),
			TotalSeconds:        unionSegments(a.segs),
			Devices:             len(a.keys),
			OnlineDevices:       len(a.online),
		}
		if res.hasStudent {
			p.StudentID = ptr(res.student)
		}
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b ParticipantResult) int {
		return cmp.Or(
			a.FirstJoinAt.Compare(*b.FirstJoinAt),
			strings.Compare(a.DisplayName, b.DisplayName),
			strings.Compare(a.NormalizedName, b.NormalizedName),
		)
	})
	return out
}

// buildStudents makes one row per roster student. earlyRef is the reference time for
// early leave (non-nil when the lesson is finished).
func buildStudents(in *Input, out *Output, aggs map[int64]*aggregate, earlyRef *time.Time) []StudentResult {
	zoomNames := make(map[int64][]string)
	for _, p := range out.Participants { // already in display order
		if p.StudentID != nil && !slices.Contains(zoomNames[*p.StudentID], p.DisplayName) {
			zoomNames[*p.StudentID] = append(zoomNames[*p.StudentID], p.DisplayName)
		}
	}
	finished := out.Status == model.LessonFinished
	winStart, winEnd := percentWindow(in.Lesson, out, finished)
	lessonSeconds := seconds(winEnd.Sub(winStart))
	minPercent := int64(min(max(in.PresentMinPercent, 0), 100))

	students := make([]StudentResult, 0, len(in.Roster))
	done := make(map[int64]bool, len(in.Roster))
	for _, rs := range in.Roster {
		if done[rs.StudentID] {
			continue
		}
		done[rs.StudentID] = true
		sr := StudentResult{
			StudentID:     rs.StudentID,
			CurrentStatus: model.CurrentNotSeen,
			AutoStatus:    model.FinalAbsent,
			ZoomNames:     zoomNames[rs.StudentID],
		}
		if sr.ZoomNames == nil {
			sr.ZoomNames = []string{}
		}
		a := aggs[rs.StudentID]
		if a == nil {
			students = append(students, sr)
			continue
		}
		sr.Seen = true
		sr.CurrentStatus = a.currentStatus()
		sr.OnlineDevices = len(a.online)
		sr.FirstJoinAt = ptr(a.first)
		sr.LastLeaveAt = a.lastLeave()
		sr.TotalSeconds = unionSegments(a.segs)
		sr.LateSeconds = ptr(max(0, seconds(a.first.Sub(in.Lesson.PlannedStart))))
		if finished {
			early := int64(0)
			if len(a.online) == 0 && a.hasLast {
				early = max(0, seconds(earlyRef.Sub(a.last)))
			}
			sr.EarlyLeaveSeconds = ptr(early)
		}
		if minPercent == 0 || lessonSeconds <= 0 ||
			clippedUnion(a.segs, winStart, winEnd)*100 >= minPercent*lessonSeconds {
			sr.AutoStatus = model.FinalPresent
		}
		students = append(students, sr)
	}
	return students
}

// percentWindow is the lesson time the minimum-percent rule measures against
// (rule 3): the planned time while the lesson runs; once it is finished, the part
// of the planned time it actually ran (a lesson that ran shorter is still
// honoured, while the host's Zoom time before and after the lesson is not), or
// the actual time when it ran entirely outside the plan (rescheduled without
// editing the lesson). A student's time counts only inside this window.
func percentWindow(l LessonInfo, out *Output, finished bool) (start, end time.Time) {
	start, end = l.PlannedStart, l.PlannedEnd
	if !finished || out.ActualStart == nil || out.ActualEnd == nil {
		return start, end
	}
	s, e := *out.ActualStart, *out.ActualEnd
	if s.Before(start) {
		s = start
	}
	if e.After(end) {
		e = end
	}
	if e.After(s) {
		return s, e
	}
	return *out.ActualStart, *out.ActualEnd
}

func seconds(d time.Duration) int64 { return int64(d / time.Second) }

func ptr[T any](v T) *T { return &v }
