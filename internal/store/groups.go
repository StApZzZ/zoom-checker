package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"zoom-checker/internal/db"
	"zoom-checker/internal/model"
	"zoom-checker/internal/names"
)

const groupCols = `id, name, normalized_name, description, status, default_duration_minutes, created_at, updated_at`

// DefaultGroupDurationMinutes is used when a group is created without a duration.
const DefaultGroupDurationMinutes = 90

func scanGroup(sc scanner, g *model.Group, extra ...any) error {
	var created, updated int64
	dest := append([]any{&g.ID, &g.Name, &g.NormalizedName, &g.Description, &g.Status, &g.DefaultDurationMinutes,
		&created, &updated}, extra...)
	if err := sc.Scan(dest...); err != nil {
		return err
	}
	g.CreatedAt = fromMS(created)
	g.UpdatedAt = fromMS(updated)
	return nil
}

// GroupSummary is a group with list-page aggregates.
type GroupSummary struct {
	model.Group
	StudentCount int        // active students
	LastLessonAt *time.Time // latest planned_start_at <= now (nil when none)
	NextLessonAt *time.Time // earliest planned_start_at > now (nil when none)
}

// Group status filters for ListGroups.
const (
	GroupFilterActive   = "active"
	GroupFilterArchived = "archived"
	GroupFilterAll      = "all"
)

func validStatus(st model.Status) bool {
	return st == model.StatusActive || st == model.StatusArchived
}

// CreateGroup inserts g. NormalizedName is computed from Name; empty Status
// becomes active; DefaultDurationMinutes <= 0 becomes 90. Sets ID and timestamps.
// ErrInvalid for an empty name, ErrConflict when a group with the same normalized
// name exists (archived groups included).
func (s *Store) CreateGroup(ctx context.Context, g *model.Group) error {
	g.NormalizedName = names.Normalize(g.Name)
	if g.NormalizedName == "" {
		return fmt.Errorf("%w: empty group name", ErrInvalid)
	}
	if g.Status == "" {
		g.Status = model.StatusActive
	}
	if !validStatus(g.Status) {
		return fmt.Errorf("%w: group status %q", ErrInvalid, g.Status)
	}
	if g.DefaultDurationMinutes <= 0 {
		g.DefaultDurationMinutes = DefaultGroupDurationMinutes
	}
	now := s.nowMS()
	err := s.queryRow(ctx, `INSERT INTO student_groups (name, normalized_name, description, status,
		default_duration_minutes, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		g.Name, g.NormalizedName, g.Description, string(g.Status), g.DefaultDurationMinutes, now, now).Scan(&g.ID)
	if err != nil {
		if db.IsUniqueViolation(err) {
			return fmt.Errorf("%w: group %q already exists", ErrConflict, g.Name)
		}
		return fmt.Errorf("store: create group: %w", err)
	}
	g.CreatedAt = fromMS(now)
	g.UpdatedAt = g.CreatedAt
	return nil
}

// GetGroup returns the group or ErrNotFound.
func (s *Store) GetGroup(ctx context.Context, id int64) (*model.Group, error) {
	return s.getGroup(ctx, `SELECT `+groupCols+` FROM student_groups WHERE id = ?`, id)
}

// GetGroupByNormalizedName finds a group by its normalized name; the argument is
// normalized again, so a raw name works too. ErrNotFound when missing.
func (s *Store) GetGroupByNormalizedName(ctx context.Context, normalized string) (*model.Group, error) {
	return s.getGroup(ctx, `SELECT `+groupCols+` FROM student_groups WHERE normalized_name = ?`, names.Normalize(normalized))
}

func (s *Store) getGroup(ctx context.Context, q string, args ...any) (*model.Group, error) {
	var g model.Group
	if err := scanGroup(s.queryRow(ctx, q, args...), &g); err != nil {
		if err = notFound(err); err == ErrNotFound {
			return nil, err
		}
		return nil, fmt.Errorf("store: get group: %w", err)
	}
	return &g, nil
}

// groupSummarySelect computes the aggregates with correlated subqueries (index
// lessons_group (group_id, planned_start_at) and students_group serve them).
var groupSummarySelect = `SELECT ` + prefixCols("g", groupCols) + `,
	(SELECT COUNT(*) FROM students s WHERE s.group_id = g.id AND s.status = 'active'),
	(SELECT MAX(l.planned_start_at) FROM lessons l WHERE l.group_id = g.id AND l.planned_start_at <= ?),
	(SELECT MIN(l.planned_start_at) FROM lessons l WHERE l.group_id = g.id AND l.planned_start_at > ?)
	FROM student_groups g`

func scanGroupSummary(sc scanner) (GroupSummary, error) {
	var gs GroupSummary
	var last, next sql.NullInt64
	if err := scanGroup(sc, &gs.Group, &gs.StudentCount, &last, &next); err != nil {
		return gs, err
	}
	gs.LastLessonAt = fromNullMS(last)
	gs.NextLessonAt = fromNullMS(next)
	return gs, nil
}

// ListGroups returns groups filtered by status ("active", "archived", "all"; ""
// means all) ordered by normalized name, with aggregates relative to now.
func (s *Store) ListGroups(ctx context.Context, status string, now time.Time) ([]GroupSummary, error) {
	q := groupSummarySelect
	args := []any{toMS(now), toMS(now)}
	switch status {
	case GroupFilterAll, "":
	case GroupFilterActive, GroupFilterArchived:
		q += ` WHERE g.status = ?`
		args = append(args, status)
	default:
		return nil, fmt.Errorf("%w: group status filter %q", ErrInvalid, status)
	}
	q += ` ORDER BY g.normalized_name, g.id`
	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list groups: %w", err)
	}
	out, err := collect(rows, scanGroupSummary)
	if err != nil {
		return nil, fmt.Errorf("store: list groups: %w", err)
	}
	return out, nil
}

// GetGroupSummary returns one group with the same aggregates as ListGroups.
func (s *Store) GetGroupSummary(ctx context.Context, id int64, now time.Time) (*GroupSummary, error) {
	q := groupSummarySelect + ` WHERE g.id = ?`
	gs, err := scanGroupSummary(s.queryRow(ctx, q, toMS(now), toMS(now), id))
	if err != nil {
		if err = notFound(err); err == ErrNotFound {
			return nil, err
		}
		return nil, fmt.Errorf("store: get group summary: %w", err)
	}
	return &gs, nil
}

// UpdateGroup writes Name (re-normalized), Description, Status and
// DefaultDurationMinutes (<= 0 keeps 90). Sets NormalizedName and UpdatedAt.
// ErrNotFound, ErrConflict (name taken), ErrInvalid (empty name, bad status).
func (s *Store) UpdateGroup(ctx context.Context, g *model.Group) error {
	g.NormalizedName = names.Normalize(g.Name)
	if g.NormalizedName == "" {
		return fmt.Errorf("%w: empty group name", ErrInvalid)
	}
	if !validStatus(g.Status) {
		return fmt.Errorf("%w: group status %q", ErrInvalid, g.Status)
	}
	if g.DefaultDurationMinutes <= 0 {
		g.DefaultDurationMinutes = DefaultGroupDurationMinutes
	}
	now := s.nowMS()
	err := s.execOne(ctx, `UPDATE student_groups SET name = ?, normalized_name = ?, description = ?, status = ?,
		default_duration_minutes = ?, updated_at = ? WHERE id = ?`,
		g.Name, g.NormalizedName, g.Description, string(g.Status), g.DefaultDurationMinutes, now, g.ID)
	if err != nil {
		if err == ErrNotFound {
			return err
		}
		if db.IsUniqueViolation(err) {
			return fmt.Errorf("%w: group %q already exists", ErrConflict, g.Name)
		}
		return fmt.Errorf("store: update group: %w", err)
	}
	g.UpdatedAt = fromMS(now)
	return nil
}

// prefixCols turns "a, b,\n c" into "p.a, p.b, p.c".
func prefixCols(prefix, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = prefix + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}
