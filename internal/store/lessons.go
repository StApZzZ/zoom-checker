package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"zoom-checker/internal/model"
)

const lessonCols = `id, group_id, title, zoom_meeting_id, zoom_join_url, planned_start_at, planned_end_at,
	actual_start_at, actual_end_at, status, manual_finished_at, created_by, created_at, updated_at`

func scanLesson(sc scanner) (model.Lesson, error) {
	var l model.Lesson
	var plannedStart, plannedEnd, created, updated int64
	var actualStart, actualEnd, manualFinished, createdBy sql.NullInt64
	if err := sc.Scan(&l.ID, &l.GroupID, &l.Title, &l.ZoomMeetingID, &l.ZoomJoinURL, &plannedStart, &plannedEnd,
		&actualStart, &actualEnd, &l.Status, &manualFinished, &createdBy, &created, &updated); err != nil {
		return l, err
	}
	l.PlannedStartAt = fromMS(plannedStart)
	l.PlannedEndAt = fromMS(plannedEnd)
	l.ActualStartAt = fromNullMS(actualStart)
	l.ActualEndAt = fromNullMS(actualEnd)
	l.ManualFinishedAt = fromNullMS(manualFinished)
	l.CreatedBy = fromNullInt(createdBy)
	l.CreatedAt = fromMS(created)
	l.UpdatedAt = fromMS(updated)
	return l, nil
}

func validLessonStatus(st model.LessonStatus) bool {
	switch st {
	case model.LessonPlanned, model.LessonActive, model.LessonFinished:
		return true
	}
	return false
}

// CreateLesson inserts l (GroupID, Title, ZoomMeetingID, ZoomJoinURL, planned
// times, CreatedBy; actual times / status / ManualFinishedAt are stored as given,
// empty Status = planned). Sets ID and timestamps.
// Errors: ErrNotFound (group), ErrInvalid (empty meeting id, zero planned times, end
// before start).
func (s *Store) CreateLesson(ctx context.Context, l *model.Lesson) error {
	if err := checkLesson(l); err != nil {
		return err
	}
	if l.Status == "" {
		l.Status = model.LessonPlanned
	}
	if !validLessonStatus(l.Status) {
		return fmt.Errorf("%w: lesson status %q", ErrInvalid, l.Status)
	}
	return s.InTx(ctx, func(tx *Store) error {
		if _, err := tx.GetGroup(ctx, l.GroupID); err != nil {
			return err
		}
		now := tx.nowMS()
		err := tx.queryRow(ctx, `INSERT INTO lessons (group_id, title, zoom_meeting_id, zoom_join_url, planned_start_at,
			planned_end_at, actual_start_at, actual_end_at, status, manual_finished_at, created_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
			l.GroupID, l.Title, l.ZoomMeetingID, l.ZoomJoinURL, toMS(l.PlannedStartAt), toMS(l.PlannedEndAt),
			msArg(l.ActualStartAt), msArg(l.ActualEndAt), string(l.Status), msArg(l.ManualFinishedAt),
			int64Arg(l.CreatedBy), now, now).Scan(&l.ID)
		if err != nil {
			return fmt.Errorf("store: create lesson: %w", err)
		}
		l.CreatedAt = fromMS(now)
		l.UpdatedAt = l.CreatedAt
		return nil
	})
}

func checkLesson(l *model.Lesson) error {
	if l.ZoomMeetingID == "" {
		return fmt.Errorf("%w: empty zoom meeting id", ErrInvalid)
	}
	// A zero time.Time would be stored as year 1 and silently never match a link
	// window or report range.
	if l.PlannedStartAt.IsZero() || l.PlannedEndAt.IsZero() {
		return fmt.Errorf("%w: planned start and end are required", ErrInvalid)
	}
	if l.PlannedEndAt.Before(l.PlannedStartAt) {
		return fmt.Errorf("%w: lesson ends before it starts", ErrInvalid)
	}
	return nil
}

// GetLesson returns the lesson or ErrNotFound.
func (s *Store) GetLesson(ctx context.Context, id int64) (*model.Lesson, error) {
	l, err := scanLesson(s.queryRow(ctx, `SELECT `+lessonCols+` FROM lessons WHERE id = ?`, id))
	if err != nil {
		if err = notFound(err); err == ErrNotFound {
			return nil, err
		}
		return nil, fmt.Errorf("store: get lesson: %w", err)
	}
	return &l, nil
}

// LessonFilter selects lessons for ListLessons. Nil/zero fields do not filter.
type LessonFilter struct {
	From    *time.Time          // planned_start_at >= From
	To      *time.Time          // planned_start_at < To
	GroupID *int64              // only this group
	Status  *model.LessonStatus // only this status
	Desc    bool                // newest first (default: oldest first)
	Limit   int                 // 0 = no limit
}

// ListLessons returns lessons matching f ordered by planned_start_at, id
// (descending when f.Desc).
func (s *Store) ListLessons(ctx context.Context, f LessonFilter) ([]model.Lesson, error) {
	var where []string
	var args []any
	if f.From != nil {
		where = append(where, `planned_start_at >= ?`)
		args = append(args, toMS(*f.From))
	}
	if f.To != nil {
		where = append(where, `planned_start_at < ?`)
		args = append(args, toMS(*f.To))
	}
	if f.GroupID != nil {
		where = append(where, `group_id = ?`)
		args = append(args, *f.GroupID)
	}
	if f.Status != nil {
		where = append(where, `status = ?`)
		args = append(args, string(*f.Status))
	}
	q := `SELECT ` + lessonCols + ` FROM lessons`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	if f.Desc {
		q += ` ORDER BY planned_start_at DESC, id DESC`
	} else {
		q += ` ORDER BY planned_start_at, id`
	}
	rows, err := s.query(ctx, q+limitClause(f.Limit), args...)
	if err != nil {
		return nil, fmt.Errorf("store: list lessons: %w", err)
	}
	out, err := collect(rows, scanLesson)
	if err != nil {
		return nil, fmt.Errorf("store: list lessons: %w", err)
	}
	return out, nil
}

// UpdateLesson writes Title, ZoomMeetingID, ZoomJoinURL, PlannedStartAt and
// PlannedEndAt of l.ID (group, computed and manual-finish columns are changed by
// their own methods). Sets UpdatedAt. Errors: ErrNotFound, ErrInvalid.
func (s *Store) UpdateLesson(ctx context.Context, l *model.Lesson) error {
	if err := checkLesson(l); err != nil {
		return err
	}
	now := s.nowMS()
	if err := s.execOne(ctx, `UPDATE lessons SET title = ?, zoom_meeting_id = ?, zoom_join_url = ?, planned_start_at = ?,
		planned_end_at = ?, updated_at = ? WHERE id = ?`,
		l.Title, l.ZoomMeetingID, l.ZoomJoinURL, toMS(l.PlannedStartAt), toMS(l.PlannedEndAt), now, l.ID); err != nil {
		if err == ErrNotFound {
			return err
		}
		return fmt.Errorf("store: update lesson: %w", err)
	}
	l.UpdatedAt = fromMS(now)
	return nil
}

// DeleteLesson deletes a lesson. Its projections (participants, attendance,
// intervals) and mappings go with it (ON DELETE CASCADE). Linked Zoom sessions are
// detached and switched back to auto link mode, so they can attach to another
// lesson later (a manual link to a deleted lesson carries no intent any more).
// ErrNotFound when missing.
func (s *Store) DeleteLesson(ctx context.Context, id int64) error {
	return s.InTx(ctx, func(tx *Store) error {
		if _, err := tx.exec(ctx, `UPDATE zoom_meetings SET lesson_id = NULL, link_mode = 'auto', updated_at = ? WHERE lesson_id = ?`,
			tx.nowMS(), id); err != nil {
			return fmt.Errorf("store: delete lesson: detach sessions: %w", err)
		}
		if err := tx.execOne(ctx, `DELETE FROM lessons WHERE id = ?`, id); err != nil {
			if err == ErrNotFound {
				return err
			}
			return fmt.Errorf("store: delete lesson: %w", err)
		}
		return nil
	})
}

// LessonRef is the short description of a lesson that lists linking to it
// show (e.g. the Zoom sessions page).
type LessonRef struct {
	ID             int64
	Title          string
	GroupID        int64
	GroupName      string
	PlannedStartAt time.Time
}

// LessonRefs returns LessonRef for each existing lesson in ids (unknown ids are
// absent from the map).
func (s *Store) LessonRefs(ctx context.Context, ids []int64) (map[int64]LessonRef, error) {
	out := make(map[int64]LessonRef, len(ids))
	for _, ch := range chunks(len(ids), inChunk) {
		part := ids[ch[0]:ch[1]]
		rows, err := s.query(ctx, `SELECT l.id, l.title, l.group_id, g.name, l.planned_start_at
			FROM lessons l JOIN student_groups g ON g.id = l.group_id
			WHERE l.id IN (`+placeholders(len(part))+`)`, int64sArgs(part)...)
		if err != nil {
			return nil, fmt.Errorf("store: lesson refs: %w", err)
		}
		if _, err := collect(rows, func(sc scanner) (struct{}, error) {
			var r LessonRef
			var start int64
			if err := sc.Scan(&r.ID, &r.Title, &r.GroupID, &r.GroupName, &start); err != nil {
				return struct{}{}, err
			}
			r.PlannedStartAt = fromMS(start)
			out[r.ID] = r
			return struct{}{}, nil
		}); err != nil {
			return nil, fmt.Errorf("store: lesson refs: %w", err)
		}
	}
	return out, nil
}

// FindLessonsByMeeting returns the lessons with zoom_meeting_id = meetingID
// whose planned time overlaps [from, to]: planned_start_at <= to and
// planned_end_at >= from. Ordered by planned_start_at, then id. The processor
// ranks them as link candidates of a session (docs/architecture.md §5.5).
func (s *Store) FindLessonsByMeeting(ctx context.Context, meetingID string, from, to time.Time) ([]model.Lesson, error) {
	rows, err := s.query(ctx, `SELECT `+lessonCols+` FROM lessons
		WHERE zoom_meeting_id = ? AND planned_start_at <= ? AND planned_end_at >= ?
		ORDER BY planned_start_at, id`, meetingID, toMS(to), toMS(from))
	if err != nil {
		return nil, fmt.Errorf("store: find lessons by meeting: %w", err)
	}
	out, err := collect(rows, scanLesson)
	if err != nil {
		return nil, fmt.Errorf("store: find lessons by meeting: %w", err)
	}
	return out, nil
}

// SetLessonComputed stores the projection results of a lesson: actual start/end
// (nil clears) and status. ErrNotFound when missing.
func (s *Store) SetLessonComputed(ctx context.Context, lessonID int64, actualStart, actualEnd *time.Time, status model.LessonStatus) error {
	if !validLessonStatus(status) {
		return fmt.Errorf("%w: lesson status %q", ErrInvalid, status)
	}
	if err := s.execOne(ctx, `UPDATE lessons SET actual_start_at = ?, actual_end_at = ?, status = ?, updated_at = ? WHERE id = ?`,
		msArg(actualStart), msArg(actualEnd), string(status), s.nowMS(), lessonID); err != nil {
		if err == ErrNotFound {
			return err
		}
		return fmt.Errorf("store: set lesson computed: %w", err)
	}
	return nil
}

// SetLessonManualFinished sets (teacher pressed "finish") or clears (nil, "reopen")
// manual_finished_at. The caller rebuilds the projection afterwards.
// ErrNotFound when missing.
func (s *Store) SetLessonManualFinished(ctx context.Context, lessonID int64, at *time.Time) error {
	if err := s.execOne(ctx, `UPDATE lessons SET manual_finished_at = ?, updated_at = ? WHERE id = ?`,
		msArg(at), s.nowMS(), lessonID); err != nil {
		if err == ErrNotFound {
			return err
		}
		return fmt.Errorf("store: set lesson manual finish: %w", err)
	}
	return nil
}

// Counts are the stat tiles of a lesson (docs/architecture.md §6 Counts).
type Counts struct {
	InGroup      int // attendance rows (students of the lesson roster)
	Online       int // current_status = online
	Seen         int // online or left
	NotSeen      int // not_seen
	Unidentified int // zoom participants with resolution unidentified or conflict
	Present      int // final_status = present
	Absent       int // final_status = absent
}

// LessonCounts computes Counts for each existing lesson in lessonIDs (unknown ids
// are absent from the map). Values come from attendance and zoom_participants. A
// lesson that has no attendance rows yet (never rebuilt) reports its group's
// active students as InGroup, NotSeen and Absent — exactly what a rebuild would
// produce before anyone joins.
func (s *Store) LessonCounts(ctx context.Context, lessonIDs []int64) (map[int64]Counts, error) {
	out := make(map[int64]Counts, len(lessonIDs))
	for _, ch := range chunks(len(lessonIDs), inChunk) {
		ids := lessonIDs[ch[0]:ch[1]]
		in := placeholders(len(ids))
		args := int64sArgs(ids)

		// Lessons with their group's active student count (fallback roster size).
		rows, err := s.query(ctx, `SELECT l.id, (SELECT COUNT(*) FROM students s WHERE s.group_id = l.group_id AND s.status = 'active')
			FROM lessons l WHERE l.id IN (`+in+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("store: lesson counts: %w", err)
		}
		fallback := map[int64]int{}
		if _, err := collect(rows, func(sc scanner) (struct{}, error) {
			var id int64
			var n int
			err := sc.Scan(&id, &n)
			fallback[id] = n
			return struct{}{}, err
		}); err != nil {
			return nil, fmt.Errorf("store: lesson counts: %w", err)
		}

		rows, err = s.query(ctx, `SELECT lesson_id, COUNT(*),
			COUNT(CASE WHEN current_status = 'online' THEN 1 END),
			COUNT(CASE WHEN current_status IN ('online', 'left') THEN 1 END),
			COUNT(CASE WHEN current_status = 'not_seen' THEN 1 END),
			COUNT(CASE WHEN final_status = 'present' THEN 1 END),
			COUNT(CASE WHEN final_status = 'absent' THEN 1 END)
			FROM attendance WHERE lesson_id IN (`+in+`) GROUP BY lesson_id`, args...)
		if err != nil {
			return nil, fmt.Errorf("store: lesson counts: %w", err)
		}
		if _, err := collect(rows, func(sc scanner) (struct{}, error) {
			var id int64
			var c Counts
			err := sc.Scan(&id, &c.InGroup, &c.Online, &c.Seen, &c.NotSeen, &c.Present, &c.Absent)
			out[id] = c
			return struct{}{}, err
		}); err != nil {
			return nil, fmt.Errorf("store: lesson counts: %w", err)
		}
		for id, n := range fallback {
			if _, ok := out[id]; !ok {
				out[id] = Counts{InGroup: n, NotSeen: n, Absent: n}
			}
		}

		rows, err = s.query(ctx, `SELECT lesson_id, COUNT(*) FROM zoom_participants
			WHERE lesson_id IN (`+in+`) AND resolution IN ('unidentified', 'conflict') GROUP BY lesson_id`, args...)
		if err != nil {
			return nil, fmt.Errorf("store: lesson counts: %w", err)
		}
		if _, err := collect(rows, func(sc scanner) (struct{}, error) {
			var id int64
			var n int
			err := sc.Scan(&id, &n)
			if c, ok := out[id]; ok {
				c.Unidentified = n
				out[id] = c
			}
			return struct{}{}, err
		}); err != nil {
			return nil, fmt.Errorf("store: lesson counts: %w", err)
		}
	}
	return out, nil
}
