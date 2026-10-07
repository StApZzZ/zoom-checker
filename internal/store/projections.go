package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"zoom-checker/internal/model"
)

// Projection tables (zoom_participants, attendance, attendance_intervals) are
// rebuilt by the processor from raw events (docs/architecture.md §5.6). Only the
// manual attendance columns (manual_status, comment, manual_by, manual_at) are
// edited by people, and the rebuild methods never touch them.

// Rows per multi-row INSERT; keeps the parameter count far below the SQLite
// (32766) and PostgreSQL (65535) limits.
const upsertChunk = 100

// ---- zoom_participants ----

const participantCols = `id, lesson_id, normalized_name, display_name, resolution, student_id, ignore_reason,
	candidate_student_ids, current_status, first_join_at, last_leave_at, total_seconds, devices, computed_at, updated_at`

func scanParticipant(sc scanner) (model.Participant, error) {
	var p model.Participant
	var studentID, firstJoin, lastLeave sql.NullInt64
	var candidates string
	var computed, updated int64
	if err := sc.Scan(&p.ID, &p.LessonID, &p.NormalizedName, &p.DisplayName, &p.Resolution, &studentID, &p.IgnoreReason,
		&candidates, &p.CurrentStatus, &firstJoin, &lastLeave, &p.TotalSeconds, &p.Devices, &computed, &updated); err != nil {
		return p, err
	}
	var err error
	if p.CandidateStudentIDs, err = splitIDs(candidates); err != nil {
		return p, err
	}
	p.StudentID = fromNullInt(studentID)
	p.FirstJoinAt = fromNullMS(firstJoin)
	p.LastLeaveAt = fromNullMS(lastLeave)
	p.ComputedAt = fromMS(computed)
	p.UpdatedAt = fromMS(updated)
	return p, nil
}

// UpsertParticipants makes the participant rows of a lesson equal to ps: rows are
// upserted by (lesson_id, normalized_name) — so ids stay stable for names that
// remain — and rows whose name is not in ps are deleted (with their intervals).
// p.LessonID and p.ID are ignored; zero ComputedAt = store clock. Returns
// normalized name → id for all of ps. ErrInvalid when a normalized name repeats.
func (s *Store) UpsertParticipants(ctx context.Context, lessonID int64, ps []model.Participant) (map[string]int64, error) {
	seen := make(map[string]bool, len(ps))
	for _, p := range ps {
		if seen[p.NormalizedName] {
			return nil, fmt.Errorf("%w: participant name %q repeats", ErrInvalid, p.NormalizedName)
		}
		seen[p.NormalizedName] = true
	}
	ids := make(map[string]int64, len(ps))
	err := s.InTx(ctx, func(tx *Store) error {
		now := tx.nowMS()
		for _, ch := range chunks(len(ps), upsertChunk) {
			batch := ps[ch[0]:ch[1]]
			values := make([]string, len(batch))
			args := make([]any, 0, len(batch)*14)
			for i, p := range batch {
				values[i] = `(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
				computed := now
				if !p.ComputedAt.IsZero() {
					computed = toMS(p.ComputedAt)
				}
				args = append(args, lessonID, p.NormalizedName, p.DisplayName, string(p.Resolution), int64Arg(p.StudentID),
					string(p.IgnoreReason), joinIDs(p.CandidateStudentIDs), string(p.CurrentStatus),
					msArg(p.FirstJoinAt), msArg(p.LastLeaveAt), p.TotalSeconds, p.Devices, computed, now)
			}
			rows, err := tx.query(ctx, `INSERT INTO zoom_participants (lesson_id, normalized_name, display_name, resolution,
				student_id, ignore_reason, candidate_student_ids, current_status, first_join_at, last_leave_at, total_seconds,
				devices, computed_at, updated_at) VALUES `+strings.Join(values, ", ")+`
				ON CONFLICT (lesson_id, normalized_name) DO UPDATE SET
					display_name = excluded.display_name, resolution = excluded.resolution, student_id = excluded.student_id,
					ignore_reason = excluded.ignore_reason, candidate_student_ids = excluded.candidate_student_ids,
					current_status = excluded.current_status, first_join_at = excluded.first_join_at,
					last_leave_at = excluded.last_leave_at, total_seconds = excluded.total_seconds, devices = excluded.devices,
					computed_at = excluded.computed_at, updated_at = excluded.updated_at
				RETURNING id, normalized_name`, args...)
			if err != nil {
				return fmt.Errorf("store: upsert participants: %w", err)
			}
			if _, err := collect(rows, func(sc scanner) (struct{}, error) {
				var id int64
				var name string
				err := sc.Scan(&id, &name)
				ids[name] = id
				return struct{}{}, err
			}); err != nil {
				return fmt.Errorf("store: upsert participants: %w", err)
			}
		}
		// Delete names that disappeared (e.g. after a manual mapping change).
		rows, err := tx.query(ctx, `SELECT id, normalized_name FROM zoom_participants WHERE lesson_id = ?`, lessonID)
		if err != nil {
			return fmt.Errorf("store: upsert participants: %w", err)
		}
		var stale []int64
		if _, err := collect(rows, func(sc scanner) (struct{}, error) {
			var id int64
			var name string
			err := sc.Scan(&id, &name)
			if !seen[name] {
				stale = append(stale, id)
			}
			return struct{}{}, err
		}); err != nil {
			return fmt.Errorf("store: upsert participants: %w", err)
		}
		return tx.deleteByIDs(ctx, `zoom_participants`, stale)
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// deleteByIDs deletes rows of table by primary key in chunks.
func (s *Store) deleteByIDs(ctx context.Context, table string, ids []int64) error {
	for _, ch := range chunks(len(ids), inChunk) {
		part := ids[ch[0]:ch[1]]
		if _, err := s.exec(ctx, `DELETE FROM `+table+` WHERE id IN (`+placeholders(len(part))+`)`, int64sArgs(part)...); err != nil {
			return fmt.Errorf("store: delete from %s: %w", table, err)
		}
	}
	return nil
}

// ListParticipants returns all participant rows of a lesson (ignored ones too)
// ordered by normalized name.
func (s *Store) ListParticipants(ctx context.Context, lessonID int64) ([]model.Participant, error) {
	rows, err := s.query(ctx, `SELECT `+participantCols+` FROM zoom_participants WHERE lesson_id = ? ORDER BY normalized_name, id`, lessonID)
	if err != nil {
		return nil, fmt.Errorf("store: list participants: %w", err)
	}
	out, err := collect(rows, scanParticipant)
	if err != nil {
		return nil, fmt.Errorf("store: list participants: %w", err)
	}
	return out, nil
}

// GetParticipant returns one participant row or ErrNotFound.
func (s *Store) GetParticipant(ctx context.Context, id int64) (*model.Participant, error) {
	p, err := scanParticipant(s.queryRow(ctx, `SELECT `+participantCols+` FROM zoom_participants WHERE id = ?`, id))
	if err != nil {
		if err = notFound(err); err == ErrNotFound {
			return nil, err
		}
		return nil, fmt.Errorf("store: get participant: %w", err)
	}
	return &p, nil
}

// ---- attendance ----

const attendanceCols = `id, lesson_id, student_id, first_join_at, last_leave_at, total_duration_seconds, late_seconds,
	early_leave_seconds, current_status, auto_status, manual_status, final_status, comment, manual_by, manual_at,
	computed_at, created_at, updated_at`

func scanAttendance(sc scanner) (model.Attendance, error) {
	var a model.Attendance
	var firstJoin, lastLeave, late, early, manualBy, manualAt sql.NullInt64
	var manual sql.NullString
	var computed, created, updated int64
	if err := sc.Scan(&a.ID, &a.LessonID, &a.StudentID, &firstJoin, &lastLeave, &a.TotalDurationSeconds, &late, &early,
		&a.CurrentStatus, &a.AutoStatus, &manual, &a.FinalStatus, &a.Comment, &manualBy, &manualAt,
		&computed, &created, &updated); err != nil {
		return a, err
	}
	a.FirstJoinAt = fromNullMS(firstJoin)
	a.LastLeaveAt = fromNullMS(lastLeave)
	a.LateSeconds = fromNullInt(late)
	a.EarlyLeaveSeconds = fromNullInt(early)
	if manual.Valid {
		st := model.FinalStatus(manual.String)
		a.ManualStatus = &st
	}
	a.ManualBy = fromNullInt(manualBy)
	a.ManualAt = fromNullMS(manualAt)
	a.ComputedAt = fromMS(computed)
	a.CreatedAt = fromMS(created)
	a.UpdatedAt = fromMS(updated)
	return a, nil
}

// UpsertAttendanceComputed writes the computed columns of the given rows
// (first/last, totals, late/early, current_status, auto_status, computed_at) by
// (lesson_id, student_id), inserting missing rows. Manual columns (manual_status,
// comment, manual_by, manual_at) are preserved; final_status becomes
// COALESCE(manual_status, auto_status). Rows not in the list are left alone (use
// DeleteAttendance for roster sync). a.LessonID, a.ID and manual fields of the
// input are ignored; zero ComputedAt = store clock. Returns student id → row id.
// ErrInvalid when a student repeats.
func (s *Store) UpsertAttendanceComputed(ctx context.Context, lessonID int64, rows []model.Attendance) (map[int64]int64, error) {
	seen := make(map[int64]bool, len(rows))
	for _, a := range rows {
		if seen[a.StudentID] {
			return nil, fmt.Errorf("%w: attendance for student %d repeats", ErrInvalid, a.StudentID)
		}
		seen[a.StudentID] = true
	}
	ids := make(map[int64]int64, len(rows))
	err := s.InTx(ctx, func(tx *Store) error {
		now := tx.nowMS()
		for _, ch := range chunks(len(rows), upsertChunk) {
			batch := rows[ch[0]:ch[1]]
			values := make([]string, len(batch))
			args := make([]any, 0, len(batch)*13)
			for i, a := range batch {
				values[i] = `(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
				computed := now
				if !a.ComputedAt.IsZero() {
					computed = toMS(a.ComputedAt)
				}
				args = append(args, lessonID, a.StudentID, msArg(a.FirstJoinAt), msArg(a.LastLeaveAt), a.TotalDurationSeconds,
					int64Arg(a.LateSeconds), int64Arg(a.EarlyLeaveSeconds), string(a.CurrentStatus), string(a.AutoStatus),
					string(a.AutoStatus), computed, now, now)
			}
			res, err := tx.query(ctx, `INSERT INTO attendance (lesson_id, student_id, first_join_at, last_leave_at,
				total_duration_seconds, late_seconds, early_leave_seconds, current_status, auto_status, final_status,
				computed_at, created_at, updated_at) VALUES `+strings.Join(values, ", ")+`
				ON CONFLICT (lesson_id, student_id) DO UPDATE SET
					first_join_at = excluded.first_join_at, last_leave_at = excluded.last_leave_at,
					total_duration_seconds = excluded.total_duration_seconds, late_seconds = excluded.late_seconds,
					early_leave_seconds = excluded.early_leave_seconds, current_status = excluded.current_status,
					auto_status = excluded.auto_status,
					final_status = COALESCE(attendance.manual_status, excluded.auto_status),
					computed_at = excluded.computed_at, updated_at = excluded.updated_at
				RETURNING id, student_id`, args...)
			if err != nil {
				return fmt.Errorf("store: upsert attendance: %w", err)
			}
			if _, err := collect(res, func(sc scanner) (struct{}, error) {
				var id, studentID int64
				err := sc.Scan(&id, &studentID)
				ids[studentID] = id
				return struct{}{}, err
			}); err != nil {
				return fmt.Errorf("store: upsert attendance: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// DeleteAttendance deletes the attendance rows of the given students in a lesson
// (roster sync); their intervals stay with attendance_id = NULL until the next
// ReplaceIntervals. Returns the number of deleted rows.
func (s *Store) DeleteAttendance(ctx context.Context, lessonID int64, studentIDs []int64) (int64, error) {
	var total int64
	for _, ch := range chunks(len(studentIDs), inChunk) {
		part := studentIDs[ch[0]:ch[1]]
		n, err := s.execAffected(ctx, `DELETE FROM attendance WHERE lesson_id = ? AND student_id IN (`+placeholders(len(part))+`)`,
			append([]any{lessonID}, int64sArgs(part)...)...)
		if err != nil {
			return total, fmt.Errorf("store: delete attendance: %w", err)
		}
		total += n
	}
	return total, nil
}

// ListAttendance returns the attendance rows of a lesson ordered by the
// student's normalized name.
func (s *Store) ListAttendance(ctx context.Context, lessonID int64) ([]model.Attendance, error) {
	rows, err := s.query(ctx, `SELECT `+prefixCols("a", attendanceCols)+` FROM attendance a
		JOIN students st ON st.id = a.student_id WHERE a.lesson_id = ? ORDER BY st.normalized_name, a.id`, lessonID)
	if err != nil {
		return nil, fmt.Errorf("store: list attendance: %w", err)
	}
	out, err := collect(rows, scanAttendance)
	if err != nil {
		return nil, fmt.Errorf("store: list attendance: %w", err)
	}
	return out, nil
}

// GetAttendance returns the row of one student in a lesson or ErrNotFound.
func (s *Store) GetAttendance(ctx context.Context, lessonID, studentID int64) (*model.Attendance, error) {
	a, err := scanAttendance(s.queryRow(ctx, `SELECT `+attendanceCols+` FROM attendance WHERE lesson_id = ? AND student_id = ?`,
		lessonID, studentID))
	if err != nil {
		if err = notFound(err); err == ErrNotFound {
			return nil, err
		}
		return nil, fmt.Errorf("store: get attendance: %w", err)
	}
	return &a, nil
}

// SetManualStatus sets (or with nil clears, i.e. "back to automatic") the
// teacher's verdict for a student in a lesson and recomputes final_status =
// COALESCE(manual_status, auto_status). manual_by/manual_at record who changed it
// and when. Returns the updated row; ErrNotFound when the row does not exist.
func (s *Store) SetManualStatus(ctx context.Context, lessonID, studentID int64, status *model.FinalStatus, by *int64, at time.Time) (*model.Attendance, error) {
	if status != nil && *status != model.FinalPresent && *status != model.FinalAbsent {
		return nil, fmt.Errorf("%w: final status %q", ErrInvalid, *status)
	}
	a, err := scanAttendance(s.queryRow(ctx, `UPDATE attendance SET manual_status = ?, final_status = COALESCE(?, auto_status),
		manual_by = ?, manual_at = ?, updated_at = ? WHERE lesson_id = ? AND student_id = ? RETURNING `+attendanceCols,
		stringArg(status), stringArg(status), int64Arg(by), toMS(at), s.nowMS(), lessonID, studentID))
	if err != nil {
		if err = notFound(err); err == ErrNotFound {
			return nil, err
		}
		return nil, fmt.Errorf("store: set manual status: %w", err)
	}
	return &a, nil
}

// SetAttendanceComment sets the teacher comment of a student in a lesson ("" clears)
// and records manual_by/manual_at. Returns the updated row; ErrNotFound when missing.
func (s *Store) SetAttendanceComment(ctx context.Context, lessonID, studentID int64, comment string, by *int64, at time.Time) (*model.Attendance, error) {
	a, err := scanAttendance(s.queryRow(ctx, `UPDATE attendance SET comment = ?, manual_by = ?, manual_at = ?, updated_at = ?
		WHERE lesson_id = ? AND student_id = ? RETURNING `+attendanceCols,
		comment, int64Arg(by), toMS(at), s.nowMS(), lessonID, studentID))
	if err != nil {
		if err = notFound(err); err == ErrNotFound {
			return nil, err
		}
		return nil, fmt.Errorf("store: set attendance comment: %w", err)
	}
	return &a, nil
}

// ---- attendance_intervals ----

const intervalCols = `id, lesson_id, zoom_participant_id, attendance_id, meeting_uuid, zoom_participant_uuid, zoom_user_name,
	join_at, leave_at, duration_seconds, leave_reason`

func scanInterval(sc scanner) (model.Interval, error) {
	var iv model.Interval
	var participantID, attendanceID, leave, duration sql.NullInt64
	var join int64
	if err := sc.Scan(&iv.ID, &iv.LessonID, &participantID, &attendanceID, &iv.MeetingUUID, &iv.ZoomParticipantUUID,
		&iv.ZoomUserName, &join, &leave, &duration, &iv.LeaveReason); err != nil {
		return iv, err
	}
	iv.ZoomParticipantID = fromNullInt(participantID)
	iv.AttendanceID = fromNullInt(attendanceID)
	iv.JoinAt = fromMS(join)
	iv.LeaveAt = fromNullMS(leave)
	iv.DurationSeconds = fromNullInt(duration)
	return iv, nil
}

// ReplaceIntervals deletes all intervals of a lesson and inserts ivs (their
// LessonID and ID are ignored; ids are not reported back). ZoomParticipantID and
// AttendanceID must reference rows of the same lesson (from UpsertParticipants /
// UpsertAttendanceComputed) or be nil.
func (s *Store) ReplaceIntervals(ctx context.Context, lessonID int64, ivs []model.Interval) error {
	return s.InTx(ctx, func(tx *Store) error {
		if _, err := tx.exec(ctx, `DELETE FROM attendance_intervals WHERE lesson_id = ?`, lessonID); err != nil {
			return fmt.Errorf("store: replace intervals: %w", err)
		}
		for _, ch := range chunks(len(ivs), upsertChunk) {
			batch := ivs[ch[0]:ch[1]]
			values := make([]string, len(batch))
			args := make([]any, 0, len(batch)*10)
			for i, iv := range batch {
				values[i] = `(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
				args = append(args, lessonID, int64Arg(iv.ZoomParticipantID), int64Arg(iv.AttendanceID), iv.MeetingUUID,
					iv.ZoomParticipantUUID, iv.ZoomUserName, toMS(iv.JoinAt), msArg(iv.LeaveAt), int64Arg(iv.DurationSeconds),
					iv.LeaveReason)
			}
			if _, err := tx.exec(ctx, `INSERT INTO attendance_intervals (lesson_id, zoom_participant_id, attendance_id,
				meeting_uuid, zoom_participant_uuid, zoom_user_name, join_at, leave_at, duration_seconds, leave_reason)
				VALUES `+strings.Join(values, ", "), args...); err != nil {
				return fmt.Errorf("store: replace intervals: %w", err)
			}
		}
		return nil
	})
}

// ListIntervals returns all intervals of a lesson ordered by join time, id.
func (s *Store) ListIntervals(ctx context.Context, lessonID int64) ([]model.Interval, error) {
	return s.listIntervals(ctx, `SELECT `+intervalCols+` FROM attendance_intervals WHERE lesson_id = ? ORDER BY join_at, id`, lessonID)
}

// ListStudentIntervals returns the intervals attributed to one student's
// attendance row in a lesson, ordered by join time, id.
func (s *Store) ListStudentIntervals(ctx context.Context, lessonID, studentID int64) ([]model.Interval, error) {
	return s.listIntervals(ctx, `SELECT `+prefixCols("i", intervalCols)+` FROM attendance_intervals i
		JOIN attendance a ON a.id = i.attendance_id
		WHERE i.lesson_id = ? AND a.lesson_id = ? AND a.student_id = ? ORDER BY i.join_at, i.id`, lessonID, lessonID, studentID)
}

func (s *Store) listIntervals(ctx context.Context, q string, args ...any) ([]model.Interval, error) {
	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list intervals: %w", err)
	}
	out, err := collect(rows, scanInterval)
	if err != nil {
		return nil, fmt.Errorf("store: list intervals: %w", err)
	}
	return out, nil
}

// ---- participant_mappings ----

const mappingCols = `id, lesson_id, normalized_name, action, student_id, created_by, created_at`

func scanMapping(sc scanner) (model.Mapping, error) {
	var m model.Mapping
	var studentID, createdBy sql.NullInt64
	var created int64
	if err := sc.Scan(&m.ID, &m.LessonID, &m.NormalizedName, &m.Action, &studentID, &createdBy, &created); err != nil {
		return m, err
	}
	m.StudentID = fromNullInt(studentID)
	m.CreatedBy = fromNullInt(createdBy)
	m.CreatedAt = fromMS(created)
	return m, nil
}

// ListMappings returns the manual decisions of a lesson ordered by normalized name.
func (s *Store) ListMappings(ctx context.Context, lessonID int64) ([]model.Mapping, error) {
	rows, err := s.query(ctx, `SELECT `+mappingCols+` FROM participant_mappings WHERE lesson_id = ? ORDER BY normalized_name, id`, lessonID)
	if err != nil {
		return nil, fmt.Errorf("store: list mappings: %w", err)
	}
	out, err := collect(rows, scanMapping)
	if err != nil {
		return nil, fmt.Errorf("store: list mappings: %w", err)
	}
	return out, nil
}

// GetMapping returns the decision for a normalized name in a lesson or ErrNotFound.
func (s *Store) GetMapping(ctx context.Context, lessonID int64, normalizedName string) (*model.Mapping, error) {
	m, err := scanMapping(s.queryRow(ctx, `SELECT `+mappingCols+` FROM participant_mappings WHERE lesson_id = ? AND normalized_name = ?`,
		lessonID, normalizedName))
	if err != nil {
		if err = notFound(err); err == ErrNotFound {
			return nil, err
		}
		return nil, fmt.Errorf("store: get mapping: %w", err)
	}
	return &m, nil
}

// UpsertMapping creates or replaces the decision for (m.LessonID,
// m.NormalizedName) — the name must already be normalized (it is the participant
// row's NormalizedName). Action "student" requires StudentID; for "ignore"/"none"
// StudentID is stored as NULL. Sets m.ID and m.CreatedAt (store clock).
// ErrInvalid for a bad action, ErrNotFound when the lesson does not exist.
func (s *Store) UpsertMapping(ctx context.Context, m *model.Mapping) error {
	switch m.Action {
	case model.MappingStudent:
		if m.StudentID == nil {
			return fmt.Errorf("%w: mapping to a student without student id", ErrInvalid)
		}
	case model.MappingIgnore, model.MappingNone:
		m.StudentID = nil
	default:
		return fmt.Errorf("%w: mapping action %q", ErrInvalid, m.Action)
	}
	return s.InTx(ctx, func(tx *Store) error {
		if _, err := tx.GetLesson(ctx, m.LessonID); err != nil {
			return err
		}
		now := tx.nowMS()
		err := tx.queryRow(ctx, `INSERT INTO participant_mappings (lesson_id, normalized_name, action, student_id, created_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (lesson_id, normalized_name) DO UPDATE SET action = excluded.action, student_id = excluded.student_id,
				created_by = excluded.created_by, created_at = excluded.created_at
			RETURNING id`, m.LessonID, m.NormalizedName, string(m.Action), int64Arg(m.StudentID), int64Arg(m.CreatedBy), now).Scan(&m.ID)
		if err != nil {
			return fmt.Errorf("store: upsert mapping: %w", err)
		}
		m.CreatedAt = fromMS(now)
		return nil
	})
}

// DeleteMapping removes the decision for a name in a lesson; ErrNotFound when
// there is none.
func (s *Store) DeleteMapping(ctx context.Context, lessonID int64, normalizedName string) error {
	if err := s.execOne(ctx, `DELETE FROM participant_mappings WHERE lesson_id = ? AND normalized_name = ?`, lessonID, normalizedName); err != nil {
		if err == ErrNotFound {
			return err
		}
		return fmt.Errorf("store: delete mapping: %w", err)
	}
	return nil
}
