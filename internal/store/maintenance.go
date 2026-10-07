package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"zoom-checker/internal/model"
)

// Queries of the processor's periodic upkeep of running lessons.

// ListLessonsOnlineAbsent returns the ids of running lessons (status active)
// with a student who is in Zoom but still absent by the automatic rule. Under
// the minimum-percent rule only such rows change status as time passes, so
// only these lessons need a rebuild without new Zoom events.
func (s *Store) ListLessonsOnlineAbsent(ctx context.Context) ([]int64, error) {
	rows, err := s.query(ctx, `SELECT DISTINCT a.lesson_id FROM attendance a JOIN lessons l ON l.id = a.lesson_id
		WHERE l.status = 'active' AND a.current_status = 'online' AND a.auto_status = 'absent' ORDER BY a.lesson_id`)
	if err != nil {
		return nil, fmt.Errorf("store: lessons with online absent students: %w", err)
	}
	ids, err := collect(rows, func(sc scanner) (int64, error) {
		var id int64
		err := sc.Scan(&id)
		return id, err
	})
	if err != nil {
		return nil, fmt.Errorf("store: lessons with online absent students: %w", err)
	}
	return ids, nil
}

// ListActiveLessonsEndedBefore returns the running lessons (status active)
// planned to end before t, oldest first.
func (s *Store) ListActiveLessonsEndedBefore(ctx context.Context, t time.Time) ([]model.Lesson, error) {
	rows, err := s.query(ctx, `SELECT `+lessonCols+` FROM lessons
		WHERE status = 'active' AND planned_end_at < ? ORDER BY planned_end_at, id`, toMS(t))
	if err != nil {
		return nil, fmt.Errorf("store: overdue lessons: %w", err)
	}
	out, err := collect(rows, scanLesson)
	if err != nil {
		return nil, fmt.Errorf("store: overdue lessons: %w", err)
	}
	return out, nil
}

// LatestEventAt returns when the newest stored event of the given sessions
// happened (its received time when the payload had none); zero when there is
// none.
func (s *Store) LatestEventAt(ctx context.Context, meetingUUIDs []string) (time.Time, error) {
	var latest time.Time
	for _, ch := range chunks(len(meetingUUIDs), inChunk) {
		uuids := meetingUUIDs[ch[0]:ch[1]]
		args := make([]any, len(uuids))
		for i, u := range uuids {
			args[i] = u
		}
		var ms sql.NullInt64
		if err := s.queryRow(ctx, `SELECT MAX(COALESCE(occurred_at, received_at)) FROM zoom_webhook_events
			WHERE meeting_uuid IN (`+placeholders(len(uuids))+`)`, args...).Scan(&ms); err != nil {
			return time.Time{}, fmt.Errorf("store: latest event: %w", err)
		}
		if ms.Valid && fromMS(ms.Int64).After(latest) {
			latest = fromMS(ms.Int64)
		}
	}
	return latest, nil
}
