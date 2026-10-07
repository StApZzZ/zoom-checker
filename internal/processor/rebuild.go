package processor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"zoom-checker/internal/attendance"
	"zoom-checker/internal/model"
	"zoom-checker/internal/names"
	"zoom-checker/internal/store"
	"zoom-checker/internal/zoom"
)

// Projection rebuild (docs/architecture.md §5.6).

// RebuildLesson recomputes and stores the projection of a lesson, then publishes
// updates. ErrNotFound when the lesson does not exist.
func (p *Processor) RebuildLesson(ctx context.Context, lessonID int64) error {
	return p.inTx(ctx, func(tx *store.Store, now time.Time, fx *effects) error {
		fx.lessons[lessonID] = true
		return nil
	})
}

// rebuildTx recomputes lesson lessonID from raw events inside tx (the caller holds
// p.mu and owns the transaction) and writes participants, attendance, intervals and
// the lesson's computed columns.
func (p *Processor) rebuildTx(ctx context.Context, tx *store.Store, lessonID int64, now time.Time, fx *effects) error {
	fail := func(what string, err error) error {
		return fmt.Errorf("processor: rebuild lesson %d: %s: %w", lessonID, what, err)
	}
	lesson, err := tx.GetLesson(ctx, lessonID)
	if err != nil {
		return fail("load lesson", err)
	}
	settings, err := tx.GetSettings(ctx)
	if err != nil {
		return fail("load settings", err)
	}
	sessions, err := tx.ListZoomMeetingsByLesson(ctx, lessonID)
	if err != nil {
		return fail("load sessions", err)
	}
	uuids := make([]string, len(sessions))
	for i, s := range sessions {
		uuids[i] = s.MeetingUUID
	}
	raws, err := tx.ListParticipantEvents(ctx, uuids)
	if err != nil {
		return fail("load events", err)
	}
	students, err := tx.ListStudents(ctx, lesson.GroupID, true)
	if err != nil {
		return fail("load students", err)
	}
	existing, err := tx.ListAttendance(ctx, lessonID)
	if err != nil {
		return fail("load attendance", err)
	}
	mappings, err := tx.ListMappings(ctx, lessonID)
	if err != nil {
		return fail("load mappings", err)
	}
	oldParticipants, err := tx.ListParticipants(ctx, lessonID)
	if err != nil {
		return fail("load participants", err)
	}

	in := attendance.Input{
		Lesson: attendance.LessonInfo{
			ID:               lesson.ID,
			PlannedStart:     lesson.PlannedStartAt,
			PlannedEnd:       lesson.PlannedEndAt,
			ManualFinishedAt: lesson.ManualFinishedAt,
		},
		Sessions:          make([]attendance.Session, len(sessions)),
		Events:            participantEvents(raws),
		Mappings:          make(map[string]attendance.Mapping, len(mappings)),
		IgnoredNames:      make(map[string]bool, len(settings.IgnoredNames)),
		IgnoreHost:        settings.IgnoreHost,
		PresentMinPercent: settings.PresentMinPercent,
		Now:               now,
	}
	for i, s := range sessions {
		in.Sessions[i] = attendance.Session{
			MeetingUUID:  s.MeetingUUID,
			HostID:       s.HostID,
			StartedAt:    s.StartedAt,
			EndedAt:      s.EndedAt,
			FirstEventAt: s.FirstEventAt,
		}
	}
	for _, n := range settings.IgnoredNames {
		if n = names.Normalize(n); n != "" {
			in.IgnoredNames[n] = true
		}
	}

	byID := make(map[int64]*model.Student, len(students))
	for i := range students {
		byID[students[i].ID] = &students[i]
	}
	mapped := make(map[int64]bool)
	for _, m := range mappings {
		mp := attendance.Mapping{Action: m.Action}
		if m.StudentID != nil {
			mp.StudentID = *m.StudentID
			// Only students of the lesson's group can be targets; others stay
			// unidentified (the engine does that for ids outside the roster).
			if m.Action == model.MappingStudent && byID[*m.StudentID] != nil {
				mapped[*m.StudentID] = true
			}
		}
		in.Mappings[m.NormalizedName] = mp
	}

	plan := planRoster(lesson, sessions, students, existing, mapped, now)
	if plan.frozen {
		plan.keepMatches(oldParticipants)
	}
	in.Roster = plan.roster

	out := attendance.Compute(in)

	// ---- write ----
	ps := make([]model.Participant, len(out.Participants))
	for i, r := range out.Participants {
		ps[i] = model.Participant{
			NormalizedName:      r.NormalizedName,
			DisplayName:         r.DisplayName,
			Resolution:          r.Resolution,
			StudentID:           r.StudentID,
			IgnoreReason:        r.IgnoreReason,
			CandidateStudentIDs: r.CandidateStudentIDs,
			CurrentStatus:       r.CurrentStatus,
			FirstJoinAt:         r.FirstJoinAt,
			LastLeaveAt:         r.LastLeaveAt,
			TotalSeconds:        r.TotalSeconds,
			Devices:             r.Devices,
			ComputedAt:          now,
		}
	}
	if plan.frozen && len(sessions) == 0 {
		// A held lesson lost its sessions for a moment (a wrong Zoom link pasted
		// and then fixed, an unlink and relink): its matches must survive
		// until the sessions come back (keepMatches).
		ps = append(ps, carriedMatches(oldParticipants, now)...)
	}
	participantIDs, err := tx.UpsertParticipants(ctx, lessonID, ps)
	if err != nil {
		return fail("write participants", err)
	}

	rows, drop := plan.attendanceRows(out.Students, now)
	attendanceIDs, err := tx.UpsertAttendanceComputed(ctx, lessonID, rows)
	if err != nil {
		return fail("write attendance", err)
	}
	if len(drop) > 0 {
		if _, err := tx.DeleteAttendance(ctx, lessonID, drop); err != nil {
			return fail("roster sync", err)
		}
	}

	ivs := make([]model.Interval, len(out.Intervals))
	for i, r := range out.Intervals {
		iv := model.Interval{
			MeetingUUID:         r.MeetingUUID,
			ZoomParticipantUUID: r.ParticipantUUID,
			ZoomUserName:        r.UserName,
			JoinAt:              r.JoinAt,
			LeaveAt:             r.LeaveAt,
			DurationSeconds:     r.DurationSeconds,
			LeaveReason:         intervalLeaveReason(r),
		}
		if id, ok := participantIDs[r.NormalizedName]; ok {
			iv.ZoomParticipantID = &id
		}
		if r.StudentID != nil {
			if id, ok := attendanceIDs[*r.StudentID]; ok {
				iv.AttendanceID = &id
			}
		}
		ivs[i] = iv
	}
	if err := tx.ReplaceIntervals(ctx, lessonID, ivs); err != nil {
		return fail("write intervals", err)
	}
	if err := tx.SetLessonComputed(ctx, lessonID, out.ActualStart, out.ActualEnd, out.Status); err != nil {
		return fail("write lesson", err)
	}

	fx.lessons[lessonID] = true
	fx.touched = true
	logChanges(fx, lesson, out, oldParticipants, existing)
	return nil
}

// participantEvents parses the raw participant webhooks of a lesson's sessions
// into engine events. Repeated deliveries that are not marked duplicate yet (still
// pending) are skipped by dedupe key; unparseable rows are skipped.
func participantEvents(raws []model.WebhookEvent) []attendance.ParticipantEvent {
	out := make([]attendance.ParticipantEvent, 0, len(raws))
	seen := make(map[string]bool, len(raws))
	for i := range raws {
		raw := &raws[i]
		if raw.DedupeKey != "" {
			if seen[raw.DedupeKey] {
				continue
			}
			seen[raw.DedupeKey] = true
		}
		if pe, ok := participantEvent(raw); ok {
			out = append(out, pe)
		}
	}
	return out
}

// participantEvent converts one raw participant webhook.
func participantEvent(raw *model.WebhookEvent) (attendance.ParticipantEvent, bool) {
	ev, err := parseEvent([]byte(raw.Payload))
	if err != nil || ev.Participant == nil || !zoom.IsParticipantEvent(ev.Type) {
		return attendance.ParticipantEvent{}, false
	}
	p := ev.Participant
	kind := attendance.Leave
	if zoom.IsJoinEvent(ev.Type) {
		kind = attendance.Join
	}
	breakout := zoom.IsBreakoutEvent(ev.Type)
	key := p.Key()
	if breakout && strings.TrimSpace(p.ParticipantUUID) == "" {
		// Inside a breakout room user_id is room-local; parent_user_id is the
		// main-meeting user_id, which keeps join and leave of the room together.
		if parent := strings.TrimSpace(p.ParentUserID); parent != "" {
			key = "uid:" + parent
		}
	}
	// participant_user_id is the Zoom account id, the same kind of id as the
	// meeting's host_id (host rule); the deprecated "id" is the older equivalent.
	participantID := strings.TrimSpace(p.ParticipantUserID)
	if participantID == "" {
		participantID = strings.TrimSpace(p.ID)
	}
	meetingUUID := ev.MeetingUUID
	if meetingUUID == "" {
		meetingUUID = raw.MeetingUUID
	}
	return attendance.ParticipantEvent{
		EventID:         raw.ID,
		Kind:            kind,
		MeetingUUID:     meetingUUID,
		At:              ev.OccurredAt(),
		EventTS:         ev.EventTS,
		Key:             key,
		ParticipantUUID: p.ParticipantUUID,
		ParticipantID:   participantID,
		UserID:          strings.TrimSpace(p.UserID),
		UserName:        p.UserName,
		LeaveReason:     p.LeaveReasonText(),
		JoinTime:        p.JoinTime,
		Breakout:        breakout,
	}, true
}

// intervalLeaveReason is the stored leave_reason: Zoom's text or the engine's code
// (meeting_ended, lesson_finished, renamed), plus a note when the device changed
// its display name during the interval.
func intervalLeaveReason(r attendance.IntervalResult) string {
	if r.RenamedFrom == "" {
		return r.LeaveReason
	}
	note := attendance.ReasonRenamed + " from: " + r.RenamedFrom
	if r.LeaveReason == "" {
		return note
	}
	return r.LeaveReason + "; " + note
}

// rosterPlan decides which students take part in a rebuild and which attendance
// rows are written or removed (roster sync, §5.6).
type rosterPlan struct {
	roster   []attendance.RosterStudent
	byID     map[int64]*model.Student
	existing map[int64]*model.Attendance
	mapped   map[int64]bool
	frozen   bool
}

// planRoster builds the roster of a rebuild:
//   - while the lesson is not finished: active students of the group, students
//     that already have a row in this lesson (archived ones keep their history) and
//     students targeted by this lesson's manual mappings;
//   - once finished (and rows exist), the roster is frozen: only students with a
//     row plus mapping targets. Students who joined the group later do not appear
//     in a past lesson, and names are not auto-matched to them. A held lesson
//     stays frozen while it has no linked session (heldWithoutSessions), so a
//     session unlinked for a moment cannot drop archived students' rows or add
//     students who joined later.
//
// Automatic name matching covers active students and archived students that
// already have a row in this lesson: they were in the group when the lesson ran,
// so a later rebuild (settings change, late Zoom retry, "recompute") must not
// turn their attended time into "not seen". A name that an active roster student
// also uses belongs to the active student (no conflict with a former member).
// Archived students without a row (mapping targets only) are never auto-matched.
func planRoster(lesson *model.Lesson, sessions []model.ZoomMeeting, students []model.Student,
	existing []model.Attendance, mapped map[int64]bool, now time.Time) *rosterPlan {
	plan := &rosterPlan{
		byID:     make(map[int64]*model.Student, len(students)),
		existing: make(map[int64]*model.Attendance, len(existing)),
		mapped:   mapped,
	}
	for i := range students {
		plan.byID[students[i].ID] = &students[i]
	}
	for i := range existing {
		plan.existing[existing[i].StudentID] = &existing[i]
	}
	plan.frozen = len(existing) > 0 && (lessonFinished(lesson, sessions) || heldWithoutSessions(lesson, sessions, now))

	included := make([]*model.Student, 0, len(students))
	activeNames := make(map[string]bool)
	for i := range students {
		st := &students[i]
		active := st.Status == model.StatusActive
		if plan.existing[st.ID] == nil && !mapped[st.ID] && (!active || plan.frozen) {
			continue
		}
		included = append(included, st)
		if active {
			for _, n := range studentNames(st) {
				activeNames[n] = true
			}
		}
	}
	for _, st := range included {
		rs := attendance.RosterStudent{StudentID: st.ID, FullName: st.FullName}
		switch {
		case st.Status == model.StatusActive:
			rs.Active = true
			rs.NormalizedName = st.NormalizedName
			for _, a := range st.Aliases {
				rs.NormalizedAliases = append(rs.NormalizedAliases, a.NormalizedAlias)
			}
		case plan.existing[st.ID] != nil:
			rs.Active = true
			if !activeNames[st.NormalizedName] {
				rs.NormalizedName = st.NormalizedName
			}
			for _, a := range st.Aliases {
				if !activeNames[a.NormalizedAlias] {
					rs.NormalizedAliases = append(rs.NormalizedAliases, a.NormalizedAlias)
				}
			}
		}
		plan.roster = append(plan.roster, rs)
	}
	return plan
}

// keepMatches keeps the automatic matches of a finished lesson (TZ §17: its
// attendance is fixed): a Zoom name the previous projection matched to a roster
// student stays with that student when the current names no longer produce the
// match — the student was renamed, an alias was removed. It is passed to the
// engine as an extra matching name of the student, so the result is still
// "matched" and the teacher's mappings keep priority. A name that matches a
// roster student now goes to that student; newly added aliases apply as usual.
// Once a rebuild does not match the name (a teacher's mapping overrode it, or
// the lesson was reopened and current names applied), nothing is kept.
func (plan *rosterPlan) keepMatches(prev []model.Participant) {
	idx := make(map[int64]int, len(plan.roster))
	taken := make(map[string]bool)
	for i, rs := range plan.roster {
		idx[rs.StudentID] = i
		if !rs.Active {
			continue // archived mapping targets match nothing automatically
		}
		taken[rs.NormalizedName] = true
		for _, a := range rs.NormalizedAliases {
			taken[a] = true
		}
	}
	for _, p := range prev {
		if p.Resolution != model.ResolutionMatched || p.StudentID == nil || p.NormalizedName == "" || taken[p.NormalizedName] {
			continue
		}
		i, ok := idx[*p.StudentID]
		if !ok || !plan.roster[i].Active {
			continue
		}
		plan.roster[i].NormalizedAliases = append(plan.roster[i].NormalizedAliases, p.NormalizedName)
		taken[p.NormalizedName] = true
	}
}

// studentNames returns the normalized full name and aliases of a student.
func studentNames(st *model.Student) []string {
	out := make([]string, 0, 1+len(st.Aliases))
	if st.NormalizedName != "" {
		out = append(out, st.NormalizedName)
	}
	for _, a := range st.Aliases {
		if a.NormalizedAlias != "" {
			out = append(out, a.NormalizedAlias)
		}
	}
	return out
}

// attendanceRows turns the engine's per-student results into rows to upsert and
// student ids whose rows are removed: archived students that were never seen and
// carry no manual status or comment (only while the roster is not frozen).
func (plan *rosterPlan) attendanceRows(results []attendance.StudentResult, now time.Time) (rows []model.Attendance, drop []int64) {
	rows = make([]model.Attendance, 0, len(results))
	for _, r := range results {
		st := plan.byID[r.StudentID]
		ex := plan.existing[r.StudentID]
		var keep bool
		switch {
		case plan.frozen:
			keep = ex != nil || (r.Seen && plan.mapped[r.StudentID])
		case st != nil && st.Status == model.StatusActive:
			keep = true
		case r.Seen:
			keep = true
		case ex != nil && (ex.ManualStatus != nil || ex.Comment != ""):
			keep = true
		case ex != nil:
			drop = append(drop, r.StudentID)
		}
		if !keep {
			continue
		}
		rows = append(rows, model.Attendance{
			StudentID:            r.StudentID,
			FirstJoinAt:          r.FirstJoinAt,
			LastLeaveAt:          r.LastLeaveAt,
			TotalDurationSeconds: r.TotalSeconds,
			LateSeconds:          r.LateSeconds,
			EarlyLeaveSeconds:    r.EarlyLeaveSeconds,
			CurrentStatus:        r.CurrentStatus,
			AutoStatus:           r.AutoStatus,
			ComputedAt:           now,
		})
	}
	return rows, drop
}

// heldWithoutSessions reports a lesson without linked sessions that was held
// or whose planned time is over: the stored status is still "finished" on the
// first rebuild after its sessions were unlinked, and a past lesson stays past.
func heldWithoutSessions(l *model.Lesson, sessions []model.ZoomMeeting, now time.Time) bool {
	return len(sessions) == 0 && (l.Status == model.LessonFinished || !now.Before(l.PlannedEndAt))
}

// carriedMatches are the automatic matches of prev as participants without
// time: a frozen lesson without sessions has no Zoom data, but the next
// rebuild with its sessions back needs them (keepMatches).
func carriedMatches(prev []model.Participant, now time.Time) []model.Participant {
	var out []model.Participant
	for _, p := range prev {
		if p.Resolution != model.ResolutionMatched || p.StudentID == nil || p.NormalizedName == "" {
			continue
		}
		out = append(out, model.Participant{
			NormalizedName: p.NormalizedName,
			DisplayName:    p.DisplayName,
			Resolution:     model.ResolutionMatched,
			StudentID:      p.StudentID,
			CurrentStatus:  model.CurrentLeft,
			ComputedAt:     now,
		})
	}
	return out
}

// lessonFinished mirrors the engine's rule 5: finished by the teacher, or every
// linked session has ended.
func lessonFinished(l *model.Lesson, sessions []model.ZoomMeeting) bool {
	if l.ManualFinishedAt != nil {
		return true
	}
	if len(sessions) == 0 {
		return false
	}
	for _, s := range sessions {
		if s.EndedAt == nil {
			return false
		}
	}
	return true
}

// logChanges queues info logs for what a rebuild changed: lesson status, name
// resolutions and students' live status (docs §10).
func logChanges(fx *effects, lesson *model.Lesson, out attendance.Output, oldPs []model.Participant, oldRows []model.Attendance) {
	if lesson.Status != out.Status {
		fx.info("lesson status changed", "lesson_id", lesson.ID, "from", string(lesson.Status), "to", string(out.Status))
	}
	old := make(map[string]*model.Participant, len(oldPs))
	for i := range oldPs {
		old[oldPs[i].NormalizedName] = &oldPs[i]
	}
	for _, r := range out.Participants {
		o := old[r.NormalizedName]
		if o != nil && o.Resolution == r.Resolution && eqID(o.StudentID, r.StudentID) && o.IgnoreReason == r.IgnoreReason {
			continue
		}
		attrs := []any{"lesson_id", lesson.ID, "participant", r.DisplayName, "resolution", string(r.Resolution)}
		if r.StudentID != nil {
			attrs = append(attrs, "student_id", *r.StudentID)
		}
		if r.IgnoreReason != model.IgnoreNone {
			attrs = append(attrs, "ignore_reason", string(r.IgnoreReason))
		}
		if o != nil {
			attrs = append(attrs, "previous_resolution", string(o.Resolution))
		}
		fx.info("participant resolution", attrs...)
	}
	prev := make(map[int64]model.CurrentStatus, len(oldRows))
	for _, a := range oldRows {
		prev[a.StudentID] = a.CurrentStatus
	}
	for _, s := range out.Students {
		was, ok := prev[s.StudentID]
		if (ok && was == s.CurrentStatus) || (!ok && s.CurrentStatus == model.CurrentNotSeen) {
			continue
		}
		fx.info("student status changed", "lesson_id", lesson.ID, "student_id", s.StudentID,
			"status", string(s.CurrentStatus), "total_seconds", s.TotalSeconds)
	}
}

func eqID(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
