package httpapi

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"zoom-checker/internal/attendance"
	"zoom-checker/internal/model"
	"zoom-checker/internal/names"
)

// lessonViewData is everything stored about a lesson that the live screen needs.
// It is loaded by loadLessonViewData and turned into a LessonViewDTO by the pure
// assembleLessonView, so the time math is testable without a database.
type lessonViewData struct {
	Lesson       model.Lesson
	GroupName    string
	Sessions     []model.ZoomMeeting
	Rows         []model.Attendance  // roster rows of the projection
	Students     []model.Student     // every student of the group, archived ones included
	Participants []model.Participant // every Zoom display name of the lesson
	Intervals    []model.Interval
	Mappings     []model.Mapping
}

// buildLessonView is THE way to build the live screen payload: the attendance
// endpoint, SSE snapshots, mutation responses and exports all use it, so they can
// never disagree. Totals are recomputed as of now from the stored intervals (open
// intervals end at now), which keeps online rows ticking although the projection
// itself was computed at the last event. Store errors pass through unchanged
// (store.ErrNotFound when the lesson does not exist).
func (s *Server) buildLessonView(ctx context.Context, lessonID int64, now time.Time) (*LessonViewDTO, error) {
	d, err := s.loadLessonViewData(ctx, lessonID)
	if err != nil {
		return nil, err
	}
	v := assembleLessonView(d, now)
	return &v, nil
}

func (s *Server) loadLessonViewData(ctx context.Context, lessonID int64) (*lessonViewData, error) {
	l, err := s.Store.GetLesson(ctx, lessonID)
	if err != nil {
		return nil, err
	}
	d := &lessonViewData{Lesson: *l}
	g, err := s.Store.GetGroup(ctx, l.GroupID)
	if err != nil {
		return nil, fmt.Errorf("lesson view: group: %w", err)
	}
	d.GroupName = g.Name
	if d.Sessions, err = s.Store.ListZoomMeetingsByLesson(ctx, lessonID); err != nil {
		return nil, fmt.Errorf("lesson view: sessions: %w", err)
	}
	if d.Rows, err = s.Store.ListAttendance(ctx, lessonID); err != nil {
		return nil, fmt.Errorf("lesson view: attendance: %w", err)
	}
	if d.Students, err = s.Store.ListStudents(ctx, l.GroupID, true); err != nil {
		return nil, fmt.Errorf("lesson view: students: %w", err)
	}
	if d.Participants, err = s.Store.ListParticipants(ctx, lessonID); err != nil {
		return nil, fmt.Errorf("lesson view: participants: %w", err)
	}
	if d.Intervals, err = s.Store.ListIntervals(ctx, lessonID); err != nil {
		return nil, fmt.Errorf("lesson view: intervals: %w", err)
	}
	if d.Mappings, err = s.Store.ListMappings(ctx, lessonID); err != nil {
		return nil, fmt.Errorf("lesson view: mappings: %w", err)
	}
	return d, nil
}

// assembleLessonView turns stored data into the live screen payload as of now.
//
//   - Student rows come from the attendance projection. Active students of the
//     group that have no row yet (added after the last rebuild, or the lesson was
//     never rebuilt) are shown as "not seen / absent" — exactly what the next
//     rebuild produces — unless the lesson is finished (its roster is frozen then)
//     or a stale planned lesson (lessonStalePlanned: roster changes no longer
//     rebuild it), when it has rows.
//   - total_seconds = union of the row's intervals as of now (two devices or a
//     breakout room are counted once); a row without stored intervals keeps the
//     projected total.
//   - devices_online = open intervals of the student, one per device connection.
//   - Participants: needing a decision (unidentified/conflict) first, then by
//     first join.
//   - Counts are derived from the same rows, so tiles and table always agree.
func assembleLessonView(d *lessonViewData, now time.Time) LessonViewDTO {
	studentByID := make(map[int64]*model.Student, len(d.Students))
	for i := range d.Students {
		studentByID[d.Students[i].ID] = &d.Students[i]
	}
	participantByID := make(map[int64]*model.Participant, len(d.Participants))
	for i := range d.Participants {
		participantByID[d.Participants[i].ID] = &d.Participants[i]
	}
	rowByID := make(map[int64]*model.Attendance, len(d.Rows))
	rowByStudent := make(map[int64]*model.Attendance, len(d.Rows))
	for i := range d.Rows {
		rowByID[d.Rows[i].ID] = &d.Rows[i]
		rowByStudent[d.Rows[i].StudentID] = &d.Rows[i]
	}
	mappingByName := make(map[string]*model.Mapping, len(d.Mappings))
	for i := range d.Mappings {
		mappingByName[d.Mappings[i].NormalizedName] = &d.Mappings[i]
	}

	// Group the stored intervals by student and by participant.
	studentIvs := map[int64][]*model.Interval{}
	participantIvs := map[int64][]*model.Interval{}
	for i := range d.Intervals {
		iv := &d.Intervals[i]
		if iv.ZoomParticipantID != nil {
			participantIvs[*iv.ZoomParticipantID] = append(participantIvs[*iv.ZoomParticipantID], iv)
		}
		var row *model.Attendance
		if iv.AttendanceID != nil {
			row = rowByID[*iv.AttendanceID]
		} else if iv.ZoomParticipantID != nil {
			// Defensive: an interval not linked to a row yet still belongs to the
			// student its name is resolved to.
			if p := participantByID[*iv.ZoomParticipantID]; p != nil && p.StudentID != nil && lvCountsForStudent(p.Resolution) {
				row = rowByStudent[*p.StudentID]
			}
		}
		if row != nil {
			studentIvs[row.StudentID] = append(studentIvs[row.StudentID], iv)
		}
	}

	// Participants in first-join order (used for zoom_names), then the display order.
	parts := make([]*model.Participant, 0, len(d.Participants))
	for i := range d.Participants {
		parts = append(parts, &d.Participants[i])
	}
	slices.SortStableFunc(parts, func(a, b *model.Participant) int {
		if c := lvCmpTime(a.FirstJoinAt, b.FirstJoinAt); c != 0 {
			return c
		}
		return cmp.Or(cmp.Compare(a.NormalizedName, b.NormalizedName), cmp.Compare(a.ID, b.ID))
	})
	zoomNames := map[int64][]string{}
	for _, p := range parts {
		if p.StudentID != nil && lvCountsForStudent(p.Resolution) {
			zoomNames[*p.StudentID] = append(zoomNames[*p.StudentID], p.DisplayName)
		}
	}

	// Student rows.
	rows := make([]StudentRowDTO, 0, len(d.Rows)+len(d.Students))
	for i := range d.Rows {
		a := &d.Rows[i]
		st := studentByID[a.StudentID]
		ivs := studentIvs[a.StudentID]
		total := a.TotalDurationSeconds
		if len(ivs) > 0 {
			total = lvUnion(ivs, now)
		}
		rows = append(rows, StudentRowDTO{
			StudentID:         a.StudentID,
			FullName:          lvStudentName(st, a.StudentID),
			ExternalID:        lvStudentExternalID(st),
			CurrentStatus:     a.CurrentStatus,
			TotalSeconds:      total,
			FirstJoinAt:       utcPtr(a.FirstJoinAt),
			LastLeaveAt:       utcPtr(a.LastLeaveAt),
			LateSeconds:       a.LateSeconds,
			EarlyLeaveSeconds: a.EarlyLeaveSeconds,
			AutoStatus:        a.AutoStatus,
			ManualStatus:      a.ManualStatus,
			FinalStatus:       a.FinalStatus,
			Comment:           a.Comment,
			ZoomNames:         lvStrings(zoomNames[a.StudentID]),
			DevicesOnline:     lvOpenDevices(ivs),
			Archived:          st != nil && st.Status == model.StatusArchived,
		})
	}
	if len(d.Rows) == 0 || (d.Lesson.Status != model.LessonFinished && !lessonStalePlanned(&d.Lesson, now)) {
		for i := range d.Students {
			st := &d.Students[i]
			if st.Status != model.StatusActive || rowByStudent[st.ID] != nil {
				continue
			}
			rows = append(rows, StudentRowDTO{
				StudentID: st.ID, FullName: st.FullName, ExternalID: st.ExternalID,
				CurrentStatus: model.CurrentNotSeen, AutoStatus: model.FinalAbsent, FinalStatus: model.FinalAbsent,
				ZoomNames: []string{},
			})
		}
	}
	slices.SortStableFunc(rows, func(a, b StudentRowDTO) int {
		return cmp.Or(cmp.Compare(names.Normalize(a.FullName), names.Normalize(b.FullName)), cmp.Compare(a.StudentID, b.StudentID))
	})

	// Participants.
	outParts := make([]ParticipantDTO, 0, len(parts))
	for _, p := range parts {
		ivs := participantIvs[p.ID]
		total := p.TotalSeconds
		if len(ivs) > 0 {
			total = lvUnion(ivs, now)
		}
		var studentName *string
		if p.StudentID != nil {
			n := lvStudentName(studentByID[*p.StudentID], *p.StudentID)
			studentName = &n
		}
		cands := make([]int64, 0, len(p.CandidateStudentIDs))
		cands = append(cands, p.CandidateStudentIDs...)
		m := mappingByName[p.NormalizedName]
		outParts = append(outParts, ParticipantDTO{
			ID: p.ID, DisplayName: p.DisplayName, NormalizedName: p.NormalizedName,
			Resolution: p.Resolution, IgnoreReason: p.IgnoreReason, StudentID: p.StudentID,
			StudentName: studentName, CandidateStudentIDs: cands, CurrentStatus: p.CurrentStatus,
			FirstJoinAt: utcPtr(p.FirstJoinAt), LastLeaveAt: utcPtr(p.LastLeaveAt),
			TotalSeconds: total, Devices: p.Devices,
			// "none" (do not auto-match) is a teacher decision too and can be undone.
			Manual: p.Resolution == model.ResolutionManual || p.IgnoreReason == model.IgnoreManual ||
				(m != nil && m.Action == model.MappingNone),
		})
	}
	// Stable sort keeps the first-join order inside each block.
	slices.SortStableFunc(outParts, func(a, b ParticipantDTO) int {
		return cmp.Compare(lvDecisionRank(a.Resolution), lvDecisionRank(b.Resolution))
	})

	return LessonViewDTO{
		Lesson:       toLessonDTO(&d.Lesson, d.GroupName, d.Sessions),
		Counts:       countLessonView(rows, outParts),
		Students:     rows,
		Participants: outParts,
		AsOf:         now.UTC(),
	}
}

// countLessonView computes the stat tiles from the rows shown on the screen.
func countLessonView(rows []StudentRowDTO, parts []ParticipantDTO) CountsDTO {
	c := CountsDTO{InGroup: len(rows)}
	for _, r := range rows {
		switch r.CurrentStatus {
		case model.CurrentOnline:
			c.Online++
			c.Seen++
		case model.CurrentLeft:
			c.Seen++
		default:
			c.NotSeen++
		}
		if r.FinalStatus == model.FinalPresent {
			c.Present++
		} else {
			c.Absent++
		}
	}
	for _, p := range parts {
		if lvNeedsDecision(p.Resolution) {
			c.Unidentified++
		}
	}
	return c
}

// lvNeedsDecision reports whether a participant waits for the teacher
// ("Не идентифицированы" block).
func lvNeedsDecision(r model.Resolution) bool {
	return r == model.ResolutionUnidentified || r == model.ResolutionConflict
}

func lvDecisionRank(r model.Resolution) int {
	if lvNeedsDecision(r) {
		return 0
	}
	return 1
}

// lvCountsForStudent reports whether a participant's time counts for its StudentID.
func lvCountsForStudent(r model.Resolution) bool {
	return r == model.ResolutionMatched || r == model.ResolutionManual
}

// lvUnion is the union length of intervals with open ones ending at now.
func lvUnion(ivs []*model.Interval, now time.Time) int64 {
	spans := make([]attendance.Span, 0, len(ivs))
	for _, iv := range ivs {
		spans = append(spans, attendance.Span{Start: iv.JoinAt, End: iv.LeaveAt})
	}
	return attendance.UnionSeconds(spans, now)
}

// lvOpenDevices counts device connections with an open interval. A device that is
// in the main room and a breakout room at once has two open intervals with the
// same participant UUID and is counted once.
func lvOpenDevices(ivs []*model.Interval) int {
	seen := map[string]bool{}
	for i, iv := range ivs {
		if iv.LeaveAt != nil {
			continue
		}
		key := iv.MeetingUUID + "\x00" + iv.ZoomParticipantUUID
		if iv.ZoomParticipantUUID == "" {
			key = fmt.Sprintf("interval:%d:%d", iv.ID, i)
		}
		seen[key] = true
	}
	return len(seen)
}

func lvStudentName(st *model.Student, id int64) string {
	if st == nil {
		return fmt.Sprintf("Студент №%d", id)
	}
	return st.FullName
}

func lvStudentExternalID(st *model.Student) string {
	if st == nil {
		return ""
	}
	return st.ExternalID
}

func lvStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// lvCmpTime orders times ascending with nil last.
func lvCmpTime(a, b *time.Time) int {
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
