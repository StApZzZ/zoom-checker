package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"zoom-checker/internal/model"
)

const meetingCols = `id, meeting_uuid, meeting_id, topic, host_id, lesson_id, link_mode, started_at, ended_at,
	first_event_at, created_at, updated_at`

func scanMeeting(sc scanner) (model.ZoomMeeting, error) {
	var m model.ZoomMeeting
	var lessonID, started, ended sql.NullInt64
	var first, created, updated int64
	if err := sc.Scan(&m.ID, &m.MeetingUUID, &m.MeetingID, &m.Topic, &m.HostID, &lessonID, &m.LinkMode,
		&started, &ended, &first, &created, &updated); err != nil {
		return m, err
	}
	m.LessonID = fromNullInt(lessonID)
	m.StartedAt = fromNullMS(started)
	m.EndedAt = fromNullMS(ended)
	m.FirstEventAt = fromMS(first)
	m.CreatedAt = fromMS(created)
	m.UpdatedAt = fromMS(updated)
	return m, nil
}

// UpsertZoomMeeting inserts the session m.MeetingUUID or merges m into the
// existing row, and returns the stored row. Merge rules:
//   - meeting_id, topic, host_id: replaced only by non-empty values;
//   - started_at, ended_at: replaced only when m provides them (non-nil);
//   - first_event_at: the earlier of stored and m.FirstEventAt (zero = store clock);
//   - lesson_id and link_mode: taken from m on insert only (empty LinkMode = auto),
//     never changed by a merge — use SetZoomMeetingLink.
//
// ErrInvalid when MeetingUUID is empty.
func (s *Store) UpsertZoomMeeting(ctx context.Context, m *model.ZoomMeeting) (*model.ZoomMeeting, error) {
	if m.MeetingUUID == "" {
		return nil, fmt.Errorf("%w: empty meeting uuid", ErrInvalid)
	}
	mode := m.LinkMode
	if mode == "" {
		mode = model.LinkAuto
	}
	if mode != model.LinkAuto && mode != model.LinkManual {
		return nil, fmt.Errorf("%w: link mode %q", ErrInvalid, mode)
	}
	now := s.nowMS()
	first := now
	if !m.FirstEventAt.IsZero() {
		first = toMS(m.FirstEventAt)
	}
	out, err := scanMeeting(s.queryRow(ctx, `INSERT INTO zoom_meetings (meeting_uuid, meeting_id, topic, host_id, lesson_id,
		link_mode, started_at, ended_at, first_event_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (meeting_uuid) DO UPDATE SET
			meeting_id = CASE WHEN excluded.meeting_id <> '' THEN excluded.meeting_id ELSE zoom_meetings.meeting_id END,
			topic = CASE WHEN excluded.topic <> '' THEN excluded.topic ELSE zoom_meetings.topic END,
			host_id = CASE WHEN excluded.host_id <> '' THEN excluded.host_id ELSE zoom_meetings.host_id END,
			started_at = COALESCE(excluded.started_at, zoom_meetings.started_at),
			ended_at = COALESCE(excluded.ended_at, zoom_meetings.ended_at),
			first_event_at = CASE WHEN excluded.first_event_at < zoom_meetings.first_event_at
				THEN excluded.first_event_at ELSE zoom_meetings.first_event_at END,
			updated_at = excluded.updated_at
		RETURNING `+meetingCols,
		m.MeetingUUID, m.MeetingID, m.Topic, m.HostID, int64Arg(m.LessonID), string(mode),
		msArg(m.StartedAt), msArg(m.EndedAt), first, now, now))
	if err != nil {
		return nil, fmt.Errorf("store: upsert zoom meeting: %w", err)
	}
	return &out, nil
}

// GetZoomMeeting returns the session by id or ErrNotFound.
func (s *Store) GetZoomMeeting(ctx context.Context, id int64) (*model.ZoomMeeting, error) {
	return s.getMeeting(ctx, `SELECT `+meetingCols+` FROM zoom_meetings WHERE id = ?`, id)
}

// GetZoomMeetingByUUID returns the session by meeting UUID or ErrNotFound.
func (s *Store) GetZoomMeetingByUUID(ctx context.Context, uuid string) (*model.ZoomMeeting, error) {
	return s.getMeeting(ctx, `SELECT `+meetingCols+` FROM zoom_meetings WHERE meeting_uuid = ?`, uuid)
}

func (s *Store) getMeeting(ctx context.Context, q string, args ...any) (*model.ZoomMeeting, error) {
	m, err := scanMeeting(s.queryRow(ctx, q, args...))
	if err != nil {
		if err = notFound(err); err == ErrNotFound {
			return nil, err
		}
		return nil, fmt.Errorf("store: get zoom meeting: %w", err)
	}
	return &m, nil
}

func (s *Store) listMeetings(ctx context.Context, q string, args ...any) ([]model.ZoomMeeting, error) {
	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list zoom meetings: %w", err)
	}
	out, err := collect(rows, scanMeeting)
	if err != nil {
		return nil, fmt.Errorf("store: list zoom meetings: %w", err)
	}
	return out, nil
}

// ListZoomMeetingsByLesson returns the sessions linked to a lesson ordered by
// start (started_at, falling back to first_event_at), then id.
func (s *Store) ListZoomMeetingsByLesson(ctx context.Context, lessonID int64) ([]model.ZoomMeeting, error) {
	return s.listMeetings(ctx, `SELECT `+meetingCols+` FROM zoom_meetings WHERE lesson_id = ?
		ORDER BY COALESCE(started_at, first_event_at), id`, lessonID)
}

// ZoomMeetingFilter selects sessions for ListZoomMeetings.
type ZoomMeetingFilter struct {
	UnlinkedOnly bool       // lesson_id IS NULL
	Since        *time.Time // first_event_at >= Since
	Limit        int        // 0 = no limit
}

// ListZoomMeetings returns sessions newest first (first_event_at DESC, id DESC).
func (s *Store) ListZoomMeetings(ctx context.Context, f ZoomMeetingFilter) ([]model.ZoomMeeting, error) {
	var where []string
	var args []any
	if f.UnlinkedOnly {
		where = append(where, `lesson_id IS NULL`)
	}
	if f.Since != nil {
		where = append(where, `first_event_at >= ?`)
		args = append(args, toMS(*f.Since))
	}
	q := `SELECT ` + meetingCols + ` FROM zoom_meetings`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	return s.listMeetings(ctx, q+` ORDER BY first_event_at DESC, id DESC`+limitClause(f.Limit), args...)
}

// SetZoomMeetingLink links a session to a lesson (lessonID non-nil) or unlinks it
// (nil) and sets the link mode. Manual unlink = (nil, manual): the session will not
// be auto-linked again. ErrNotFound when the session (or the lesson) does not exist.
func (s *Store) SetZoomMeetingLink(ctx context.Context, id int64, lessonID *int64, mode model.LinkMode) error {
	if mode != model.LinkAuto && mode != model.LinkManual {
		return fmt.Errorf("%w: link mode %q", ErrInvalid, mode)
	}
	return s.InTx(ctx, func(tx *Store) error {
		if lessonID != nil {
			if _, err := tx.GetLesson(ctx, *lessonID); err != nil {
				return err
			}
		}
		if err := tx.execOne(ctx, `UPDATE zoom_meetings SET lesson_id = ?, link_mode = ?, updated_at = ? WHERE id = ?`,
			int64Arg(lessonID), string(mode), tx.nowMS(), id); err != nil {
			if err == ErrNotFound {
				return err
			}
			return fmt.Errorf("store: set zoom meeting link: %w", err)
		}
		return nil
	})
}

// ListLinkableMeetings returns sessions of the given Zoom meeting id that are not
// linked and still in auto mode (candidates for linking when a lesson is created or
// its meeting id/time changes), ordered by first_event_at, id.
func (s *Store) ListLinkableMeetings(ctx context.Context, meetingID string) ([]model.ZoomMeeting, error) {
	return s.listMeetings(ctx, `SELECT `+meetingCols+` FROM zoom_meetings
		WHERE meeting_id = ? AND lesson_id IS NULL AND link_mode = 'auto' ORDER BY first_event_at, id`, meetingID)
}
