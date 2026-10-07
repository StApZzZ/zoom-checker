package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"zoom-checker/internal/model"
)

// ---- audit_log ----

const auditCols = `id, at, user_id, user_name, action, entity_type, entity_id, lesson_id, group_id, previous_value,
	new_value, reason`

func scanAudit(sc scanner) (model.AuditEntry, error) {
	var e model.AuditEntry
	var at int64
	var userID, lessonID, groupID sql.NullInt64
	if err := sc.Scan(&e.ID, &at, &userID, &e.UserName, &e.Action, &e.EntityType, &e.EntityID, &lessonID, &groupID,
		&e.PreviousValue, &e.NewValue, &e.Reason); err != nil {
		return e, err
	}
	e.At = fromMS(at)
	e.UserID = fromNullInt(userID)
	e.LessonID = fromNullInt(lessonID)
	e.GroupID = fromNullInt(groupID)
	return e, nil
}

// InsertAudit appends an audit entry and sets e.ID. A zero e.At is replaced by the
// store clock. Call it inside the same transaction as the change it records.
func (s *Store) InsertAudit(ctx context.Context, e *model.AuditEntry) error {
	if e.At.IsZero() {
		e.At = fromMS(s.nowMS())
	}
	err := s.queryRow(ctx, `INSERT INTO audit_log (at, user_id, user_name, action, entity_type, entity_id, lesson_id, group_id,
		previous_value, new_value, reason) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		toMS(e.At), int64Arg(e.UserID), e.UserName, e.Action, e.EntityType, e.EntityID, int64Arg(e.LessonID),
		int64Arg(e.GroupID), e.PreviousValue, e.NewValue, e.Reason).Scan(&e.ID)
	if err != nil {
		return fmt.Errorf("store: insert audit: %w", err)
	}
	return nil
}

// AuditFilter selects audit entries; zero fields do not filter.
type AuditFilter struct {
	LessonID   *int64
	GroupID    *int64
	EntityType string // e.g. "attendance", "participant", "lesson"
	EntityID   string // used together with EntityType
	// BeforeID continues a listing after the entry with this id (keyset
	// pagination over the (at, id) sort key; pass the id of the last entry of the
	// previous page). An id that does not exist yields an empty page.
	BeforeID int64
	Limit    int // <= 0 means 200
}

// ListAudit returns matching entries newest first (at DESC, id DESC).
func (s *Store) ListAudit(ctx context.Context, f AuditFilter) ([]model.AuditEntry, error) {
	var where []string
	var args []any
	if f.LessonID != nil {
		where = append(where, `lesson_id = ?`)
		args = append(args, *f.LessonID)
	}
	if f.GroupID != nil {
		where = append(where, `group_id = ?`)
		args = append(args, *f.GroupID)
	}
	if f.EntityType != "" {
		where = append(where, `entity_type = ?`)
		args = append(args, f.EntityType)
	}
	if f.EntityID != "" {
		where = append(where, `entity_id = ?`)
		args = append(args, f.EntityID)
	}
	if f.BeforeID > 0 {
		// The keyset must be the full sort key: `at` comes from the caller and need
		// not grow with id, so "id < BeforeID" alone would skip or repeat entries.
		where = append(where, `(at, id) < (SELECT b.at, b.id FROM audit_log b WHERE b.id = ?)`)
		args = append(args, f.BeforeID)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	q := `SELECT ` + auditCols + ` FROM audit_log`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	rows, err := s.query(ctx, q+` ORDER BY at DESC, id DESC`+limitClause(limit), args...)
	if err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	out, err := collect(rows, scanAudit)
	if err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	return out, nil
}

// ---- settings ----

// SettingsKey is the settings row that holds model.Settings as JSON.
const SettingsKey = "rules"

// GetSetting returns the raw value stored under key, or ErrNotFound.
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.queryRow(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err != nil {
		if err = notFound(err); err == ErrNotFound {
			return "", err
		}
		return "", fmt.Errorf("store: get setting: %w", err)
	}
	return v, nil
}

// PutSetting stores value under key (insert or replace). by is the admin who
// changed it (nil for the system).
func (s *Store) PutSetting(ctx context.Context, key, value string, by *int64) error {
	_, err := s.exec(ctx, `INSERT INTO settings (key, value, updated_at, updated_by) VALUES (?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		key, value, s.nowMS(), int64Arg(by))
	if err != nil {
		return fmt.Errorf("store: put setting: %w", err)
	}
	return nil
}

// EnsureSetting stores value under key only if the key is absent and returns the
// value that is stored afterwards — the existing one if another process won the
// race. Used for generated secrets (e.g. "server_secret").
func (s *Store) EnsureSetting(ctx context.Context, key, value string) (string, error) {
	if _, err := s.exec(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?) ON CONFLICT (key) DO NOTHING`,
		key, value, s.nowMS()); err != nil {
		return "", fmt.Errorf("store: ensure setting: %w", err)
	}
	return s.GetSetting(ctx, key)
}

// GetSettings returns the business settings: the stored JSON merged over
// model.DefaultSettings() (fields missing from the JSON keep their defaults;
// IgnoredNames is never nil).
func (s *Store) GetSettings(ctx context.Context) (model.Settings, error) {
	st := model.DefaultSettings()
	raw, err := s.GetSetting(ctx, SettingsKey)
	if err == ErrNotFound {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return model.DefaultSettings(), fmt.Errorf("store: decode settings: %w", err)
	}
	if st.IgnoredNames == nil {
		st.IgnoredNames = []string{}
	}
	return st, nil
}

// PutSettings stores the business settings (validation is the caller's job).
func (s *Store) PutSettings(ctx context.Context, st model.Settings, by *int64) error {
	if st.IgnoredNames == nil {
		st.IgnoredNames = []string{}
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("store: encode settings: %w", err)
	}
	return s.PutSetting(ctx, SettingsKey, string(raw), by)
}

// ---- reports ----

// GroupReport is the data of the group attendance matrix.
type GroupReport struct {
	Group      model.Group
	Lessons    []model.Lesson     // planned_start_at in [from, to), ordered by planned_start_at, id
	Students   []model.Student    // active students + any student with a row in Lessons; by name; Aliases nil
	Attendance []model.Attendance // every attendance row of Lessons, ordered by lesson then student id
}

// GroupReport loads the lessons of a group with planned_start_at in [from, to),
// their attendance rows and the students to show (active ones plus archived ones
// that have rows). The result is self-consistent: every attendance row refers to
// a lesson in Lessons and a student in Students (rows written between the reads
// are dropped). ErrNotFound when the group does not exist.
func (s *Store) GroupReport(ctx context.Context, groupID int64, from, to time.Time) (*GroupReport, error) {
	g, err := s.GetGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	rep := &GroupReport{Group: *g}
	gid := groupID
	f, t := from, to
	rep.Lessons, err = s.ListLessons(ctx, LessonFilter{GroupID: &gid, From: &f, To: &t})
	if err != nil {
		return nil, err
	}
	fromV, toV := toMS(from), toMS(to)
	rows, err := s.query(ctx, `SELECT `+studentCols+` FROM students
		WHERE (group_id = ? AND status = 'active')
		OR id IN (SELECT a.student_id FROM attendance a JOIN lessons l ON l.id = a.lesson_id
			WHERE l.group_id = ? AND l.planned_start_at >= ? AND l.planned_start_at < ?)
		ORDER BY normalized_name, id`, groupID, groupID, fromV, toV)
	if err != nil {
		return nil, fmt.Errorf("store: group report students: %w", err)
	}
	if rep.Students, err = collect(rows, scanStudent); err != nil {
		return nil, fmt.Errorf("store: group report students: %w", err)
	}
	rows, err = s.query(ctx, `SELECT `+prefixCols("a", attendanceCols)+` FROM attendance a JOIN lessons l ON l.id = a.lesson_id
		WHERE l.group_id = ? AND l.planned_start_at >= ? AND l.planned_start_at < ?
		ORDER BY l.planned_start_at, l.id, a.student_id`, groupID, fromV, toV)
	if err != nil {
		return nil, fmt.Errorf("store: group report attendance: %w", err)
	}
	all, err := collect(rows, scanAttendance)
	if err != nil {
		return nil, fmt.Errorf("store: group report attendance: %w", err)
	}
	// The three reads are separate statements (no common snapshot); keep only
	// rows whose lesson and student made it into the lists above.
	lessonIDs := make(map[int64]bool, len(rep.Lessons))
	for _, l := range rep.Lessons {
		lessonIDs[l.ID] = true
	}
	studentIDs := make(map[int64]bool, len(rep.Students))
	for _, st := range rep.Students {
		studentIDs[st.ID] = true
	}
	rep.Attendance = all[:0]
	for _, a := range all {
		if lessonIDs[a.LessonID] && studentIDs[a.StudentID] {
			rep.Attendance = append(rep.Attendance, a)
		}
	}
	return rep, nil
}
