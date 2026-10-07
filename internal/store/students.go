package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"

	"zoom-checker/internal/db"
	"zoom-checker/internal/model"
	"zoom-checker/internal/names"
)

// Name namespace of a group (TZ §8, docs/architecture.md §4): the normalized full
// names of its ACTIVE students together with the normalized aliases of its ACTIVE
// students must all be distinct — a Zoom display name may resolve to at most one
// student. Archived students (and their aliases) do not block names; restoring an
// archived student re-checks its name and aliases. In addition the database keeps
// aliases unique per group regardless of the owner's status
// (student_aliases_group_alias), so an archived student's alias still blocks the
// same alias on another student.
//
// All mutations that touch the namespace run in a transaction and, on PostgreSQL,
// take a per-group advisory lock first so concurrent requests cannot both pass the
// check (SQLite transactions are already serialized by BEGIN IMMEDIATE).

const studentCols = `id, group_id, external_id, full_name, normalized_name, status, created_at, updated_at`

const aliasCols = `id, student_id, group_id, alias, normalized_alias, created_by, created_at`

func scanStudent(sc scanner) (model.Student, error) {
	var st model.Student
	var created, updated int64
	if err := sc.Scan(&st.ID, &st.GroupID, &st.ExternalID, &st.FullName, &st.NormalizedName, &st.Status,
		&created, &updated); err != nil {
		return st, err
	}
	st.CreatedAt = fromMS(created)
	st.UpdatedAt = fromMS(updated)
	return st, nil
}

func scanAlias(sc scanner) (model.Alias, error) {
	var a model.Alias
	var createdBy sql.NullInt64
	var created int64
	if err := sc.Scan(&a.ID, &a.StudentID, &a.GroupID, &a.Alias, &a.NormalizedAlias, &createdBy, &created); err != nil {
		return a, err
	}
	a.CreatedBy = fromNullInt(createdBy)
	a.CreatedAt = fromMS(created)
	return a, nil
}

// groupNamesLockBase namespaces the PostgreSQL advisory lock keys used for group
// name changes ("zcna" in ASCII), keeping them apart from goose's migration lock.
const groupNamesLockBase int64 = 0x7a636e61 << 32

// lockGroupNames serializes namespace changes of one group (PostgreSQL only; the
// lock is released at commit/rollback). Must be called inside a transaction.
func (s *Store) lockGroupNames(ctx context.Context, groupID int64) error {
	if s.db.Dialect != db.Postgres {
		return nil
	}
	if _, err := s.exec(ctx, `SELECT pg_advisory_xact_lock(?)`, groupNamesLockBase+groupID); err != nil {
		return fmt.Errorf("store: lock group names: %w", err)
	}
	return nil
}

// lockedStudent takes the name lock of the student's group and only then reads
// the student (with aliases). Reading first would let a check act on a stale
// status or alias list: on PostgreSQL (READ COMMITTED) a concurrent restore or
// alias addition of the same student may commit while we wait for the lock.
// The group id itself is safe to read before locking — it never changes.
func (s *Store) lockedStudent(ctx context.Context, id int64) (*model.Student, error) {
	var groupID int64
	if err := s.queryRow(ctx, `SELECT group_id FROM students WHERE id = ?`, id).Scan(&groupID); err != nil {
		if err = notFound(err); err == ErrNotFound {
			return nil, err
		}
		return nil, fmt.Errorf("store: get student: %w", err)
	}
	if err := s.lockGroupNames(ctx, groupID); err != nil {
		return nil, err
	}
	return s.GetStudent(ctx, id)
}

// nameOwner returns who holds normalized name n in the namespace of the group,
// ignoring entries of excludeStudentID (0 = none). ok=false when n is free.
func (s *Store) nameOwner(ctx context.Context, groupID int64, n string, excludeStudentID int64) (owner int64, isAlias, ok bool, err error) {
	err = s.queryRow(ctx, `SELECT id, 0 FROM students
		WHERE group_id = ? AND status = 'active' AND normalized_name = ? AND id <> ?
		UNION ALL
		SELECT a.student_id, 1 FROM student_aliases a JOIN students st ON st.id = a.student_id
		WHERE a.group_id = ? AND a.normalized_alias = ? AND st.status = 'active' AND a.student_id <> ?
		LIMIT 1`, groupID, n, excludeStudentID, groupID, n, excludeStudentID).Scan(&owner, &isAlias)
	if err == sql.ErrNoRows {
		return 0, false, false, nil
	}
	if err != nil {
		return 0, false, false, fmt.Errorf("store: check name: %w", err)
	}
	return owner, isAlias, true, nil
}

// checkNameFree returns a *NameTakenError when n is taken in the group's namespace.
func (s *Store) checkNameFree(ctx context.Context, groupID int64, n string, excludeStudentID int64) error {
	owner, isAlias, taken, err := s.nameOwner(ctx, groupID, n, excludeStudentID)
	if err != nil {
		return err
	}
	if taken {
		return &NameTakenError{Name: n, StudentID: owner, IsAlias: isAlias}
	}
	return nil
}

// ListStudents returns the students of a group ordered by normalized name (then id),
// each with Aliases filled (never nil). Archived students are included only when
// includeArchived is true.
func (s *Store) ListStudents(ctx context.Context, groupID int64, includeArchived bool) ([]model.Student, error) {
	q := `SELECT ` + studentCols + ` FROM students WHERE group_id = ?`
	if !includeArchived {
		q += ` AND status = 'active'`
	}
	rows, err := s.query(ctx, q+` ORDER BY normalized_name, id`, groupID)
	if err != nil {
		return nil, fmt.Errorf("store: list students: %w", err)
	}
	students, err := collect(rows, scanStudent)
	if err != nil {
		return nil, fmt.Errorf("store: list students: %w", err)
	}
	if len(students) == 0 {
		return []model.Student{}, nil
	}
	rows, err = s.query(ctx, `SELECT `+aliasCols+` FROM student_aliases WHERE group_id = ? ORDER BY normalized_alias, id`, groupID)
	if err != nil {
		return nil, fmt.Errorf("store: list aliases: %w", err)
	}
	aliases, err := collect(rows, scanAlias)
	if err != nil {
		return nil, fmt.Errorf("store: list aliases: %w", err)
	}
	byStudent := make(map[int64][]model.Alias, len(students))
	for _, a := range aliases {
		byStudent[a.StudentID] = append(byStudent[a.StudentID], a)
	}
	for i := range students {
		students[i].Aliases = byStudent[students[i].ID]
		if students[i].Aliases == nil {
			students[i].Aliases = []model.Alias{}
		}
	}
	return students, nil
}

// GetStudent returns one student with Aliases filled, or ErrNotFound.
func (s *Store) GetStudent(ctx context.Context, id int64) (*model.Student, error) {
	st, err := scanStudent(s.queryRow(ctx, `SELECT `+studentCols+` FROM students WHERE id = ?`, id))
	if err != nil {
		if err = notFound(err); err == ErrNotFound {
			return nil, err
		}
		return nil, fmt.Errorf("store: get student: %w", err)
	}
	rows, err := s.query(ctx, `SELECT `+aliasCols+` FROM student_aliases WHERE student_id = ? ORDER BY normalized_alias, id`, id)
	if err != nil {
		return nil, fmt.Errorf("store: get student aliases: %w", err)
	}
	st.Aliases, err = collect(rows, scanAlias)
	if err != nil {
		return nil, fmt.Errorf("store: get student aliases: %w", err)
	}
	if st.Aliases == nil {
		st.Aliases = []model.Alias{}
	}
	return &st, nil
}

// CreateStudent inserts st into st.GroupID. NormalizedName is computed from
// FullName; empty Status becomes active. Sets ID and timestamps.
// Errors: ErrInvalid (empty name, bad status), ErrNotFound (no such group),
// *NameTakenError (an active student or alias of the group already uses the name).
func (s *Store) CreateStudent(ctx context.Context, st *model.Student) error {
	st.NormalizedName = names.Normalize(st.FullName)
	if st.NormalizedName == "" {
		return fmt.Errorf("%w: empty student name", ErrInvalid)
	}
	if st.Status == "" {
		st.Status = model.StatusActive
	}
	if !validStatus(st.Status) {
		return fmt.Errorf("%w: student status %q", ErrInvalid, st.Status)
	}
	return s.InTx(ctx, func(tx *Store) error {
		if err := tx.lockGroupNames(ctx, st.GroupID); err != nil {
			return err
		}
		if _, err := tx.GetGroup(ctx, st.GroupID); err != nil {
			return err
		}
		if st.Status == model.StatusActive {
			if err := tx.checkNameFree(ctx, st.GroupID, st.NormalizedName, 0); err != nil {
				return err
			}
		}
		id, now, err := tx.insertStudent(ctx, st.GroupID, st.ExternalID, st.FullName, st.NormalizedName, st.Status)
		if err != nil {
			return err
		}
		st.ID = id
		st.CreatedAt = fromMS(now)
		st.UpdatedAt = st.CreatedAt
		if st.Aliases == nil {
			st.Aliases = []model.Alias{}
		}
		return nil
	})
}

func (s *Store) insertStudent(ctx context.Context, groupID int64, extID, fullName, normalized string, status model.Status) (int64, int64, error) {
	now := s.nowMS()
	var id int64
	err := s.queryRow(ctx, `INSERT INTO students (group_id, external_id, full_name, normalized_name, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`, groupID, extID, fullName, normalized, string(status), now, now).Scan(&id)
	if err != nil {
		if db.IsUniqueViolation(err) {
			return 0, 0, &NameTakenError{Name: normalized}
		}
		return 0, 0, fmt.Errorf("store: create student: %w", err)
	}
	return id, now, nil
}

// UpdateStudent writes FullName (re-normalized), ExternalID and Status of the
// student st.ID (GroupID cannot change and is refreshed from the database).
// Renaming an active student, or restoring an archived one, checks the group's name
// namespace: the student's own aliases count as taken for its new name, and on
// restore every alias of the student is checked too.
// Errors: ErrNotFound, ErrInvalid, *NameTakenError.
func (s *Store) UpdateStudent(ctx context.Context, st *model.Student) error {
	st.NormalizedName = names.Normalize(st.FullName)
	if st.NormalizedName == "" {
		return fmt.Errorf("%w: empty student name", ErrInvalid)
	}
	if !validStatus(st.Status) {
		return fmt.Errorf("%w: student status %q", ErrInvalid, st.Status)
	}
	return s.InTx(ctx, func(tx *Store) error {
		cur, err := tx.lockedStudent(ctx, st.ID)
		if err != nil {
			return err
		}
		st.GroupID = cur.GroupID
		if st.Status == model.StatusActive {
			restoring := cur.Status != model.StatusActive
			renamed := cur.NormalizedName != st.NormalizedName
			if restoring || renamed {
				for _, a := range cur.Aliases {
					if a.NormalizedAlias == st.NormalizedName {
						return &NameTakenError{Name: st.NormalizedName, StudentID: st.ID, IsAlias: true}
					}
				}
				if err := tx.checkNameFree(ctx, cur.GroupID, st.NormalizedName, st.ID); err != nil {
					return err
				}
			}
			if restoring {
				for _, a := range cur.Aliases {
					if err := tx.checkNameFree(ctx, cur.GroupID, a.NormalizedAlias, st.ID); err != nil {
						return err
					}
				}
			}
		}
		now := tx.nowMS()
		_, err = tx.exec(ctx, `UPDATE students SET full_name = ?, normalized_name = ?, external_id = ?, status = ?, updated_at = ?
			WHERE id = ?`, st.FullName, st.NormalizedName, st.ExternalID, string(st.Status), now, st.ID)
		if err != nil {
			if db.IsUniqueViolation(err) {
				return &NameTakenError{Name: st.NormalizedName}
			}
			return fmt.Errorf("store: update student: %w", err)
		}
		st.CreatedAt = cur.CreatedAt
		st.UpdatedAt = fromMS(now)
		st.Aliases = cur.Aliases
		return nil
	})
}

// AddAlias adds an extra exact display name to a.StudentID. Alias is required;
// NormalizedAlias, GroupID, ID and CreatedAt are set by the store (CreatedBy is
// kept). The alias must differ from the student's own name, and for an active
// student must be free in the group's namespace; aliases are unique per group in
// any case. Errors: ErrNotFound (student), ErrInvalid (empty), *NameTakenError.
func (s *Store) AddAlias(ctx context.Context, a *model.Alias) error {
	a.NormalizedAlias = names.Normalize(a.Alias)
	if a.NormalizedAlias == "" {
		return fmt.Errorf("%w: empty alias", ErrInvalid)
	}
	return s.InTx(ctx, func(tx *Store) error {
		st, err := tx.lockedStudent(ctx, a.StudentID)
		if err != nil {
			return err
		}
		if st.NormalizedName == a.NormalizedAlias {
			return &NameTakenError{Name: a.NormalizedAlias, StudentID: st.ID}
		}
		if st.Status == model.StatusActive {
			if err := tx.checkNameFree(ctx, st.GroupID, a.NormalizedAlias, 0); err != nil {
				return err
			}
		}
		// Aliases of archived students still occupy the alias index.
		var owner int64
		err = tx.queryRow(ctx, `SELECT student_id FROM student_aliases WHERE group_id = ? AND normalized_alias = ?`,
			st.GroupID, a.NormalizedAlias).Scan(&owner)
		if err == nil {
			return &NameTakenError{Name: a.NormalizedAlias, StudentID: owner, IsAlias: true}
		} else if err != sql.ErrNoRows {
			return fmt.Errorf("store: add alias: %w", err)
		}
		now := tx.nowMS()
		err = tx.queryRow(ctx, `INSERT INTO student_aliases (student_id, group_id, alias, normalized_alias, created_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?) RETURNING id`, st.ID, st.GroupID, a.Alias, a.NormalizedAlias, int64Arg(a.CreatedBy), now).Scan(&a.ID)
		if err != nil {
			if db.IsUniqueViolation(err) {
				return &NameTakenError{Name: a.NormalizedAlias, IsAlias: true}
			}
			return fmt.Errorf("store: add alias: %w", err)
		}
		a.GroupID = st.GroupID
		a.CreatedAt = fromMS(now)
		return nil
	})
}

// DeleteAlias removes alias aliasID of student studentID and returns the deleted
// row (for the audit log). ErrNotFound when the alias does not exist or belongs to
// another student.
func (s *Store) DeleteAlias(ctx context.Context, studentID, aliasID int64) (*model.Alias, error) {
	var out *model.Alias
	err := s.InTx(ctx, func(tx *Store) error {
		a, err := scanAlias(tx.queryRow(ctx, `SELECT `+aliasCols+` FROM student_aliases WHERE id = ? AND student_id = ?`, aliasID, studentID))
		if err != nil {
			if err = notFound(err); err == ErrNotFound {
				return err
			}
			return fmt.Errorf("store: delete alias: %w", err)
		}
		if _, err := tx.exec(ctx, `DELETE FROM student_aliases WHERE id = ?`, aliasID); err != nil {
			return fmt.Errorf("store: delete alias: %w", err)
		}
		out = &a
		return nil
	})
	return out, err
}

// GroupName is one entry of a group's name namespace.
type GroupName struct {
	Normalized string // normalized full name or alias
	Display    string // as entered (full name or alias)
	StudentID  int64  // owner
	IsAlias    bool
}

// ListGroupNames returns the namespace of a group: the names of its active
// students and the aliases of its active students, ordered by normalized name.
// Used by the importer and the UI for validation before writing.
func (s *Store) ListGroupNames(ctx context.Context, groupID int64) ([]GroupName, error) {
	rows, err := s.query(ctx, `SELECT normalized_name, full_name, id, 0 FROM students
		WHERE group_id = ? AND status = 'active'
		UNION ALL
		SELECT a.normalized_alias, a.alias, a.student_id, 1 FROM student_aliases a JOIN students st ON st.id = a.student_id
		WHERE a.group_id = ? AND st.status = 'active'
		ORDER BY 1, 3`, groupID, groupID)
	if err != nil {
		return nil, fmt.Errorf("store: list group names: %w", err)
	}
	out, err := collect(rows, func(sc scanner) (GroupName, error) {
		var n GroupName
		err := sc.Scan(&n.Normalized, &n.Display, &n.StudentID, &n.IsAlias)
		return n, err
	})
	if err != nil {
		return nil, fmt.Errorf("store: list group names: %w", err)
	}
	return out, nil
}

// ImportOps is a validated import plan for one group (see ApplyImport).
type ImportOps struct {
	Create  []ImportCreate // new active students
	Update  []ImportUpdate // existing students of the group: set name/external id, make active
	Archive []int64        // existing students of the group to archive
}

// ImportCreate describes a student to create.
type ImportCreate struct {
	ExternalID string
	FullName   string
}

// ImportUpdate overwrites FullName and ExternalID of StudentID and makes the
// student active (restoring it if it was archived).
type ImportUpdate struct {
	StudentID  int64
	ExternalID string
	FullName   string
}

// ImportResult reports what ApplyImport changed.
type ImportResult struct {
	CreatedIDs []int64 // in the order of ImportOps.Create
	Updated    int     // students whose row actually changed
	Archived   int     // students that were active and are archived now
}

// ApplyImport applies ops to a group atomically. It first computes the final
// state of the group in memory and verifies the namespace rule on it (so swaps of
// names between students and renames onto names that are being archived work);
// any violation returns *NameTakenError and nothing is written. Then it archives,
// updates and creates, in one transaction.
// Errors: ErrNotFound (group), ErrInvalid (empty names, student of another group,
// the same student twice), *NameTakenError.
func (s *Store) ApplyImport(ctx context.Context, groupID int64, ops ImportOps) (*ImportResult, error) {
	res := &ImportResult{}
	err := s.InTx(ctx, func(tx *Store) error {
		if err := tx.lockGroupNames(ctx, groupID); err != nil {
			return err
		}
		if _, err := tx.GetGroup(ctx, groupID); err != nil {
			return err
		}
		current, err := tx.ListStudents(ctx, groupID, true)
		if err != nil {
			return err
		}
		byID := make(map[int64]*model.Student, len(current))
		for i := range current {
			byID[current[i].ID] = &current[i]
		}

		// Final state in memory.
		type final struct {
			status     model.Status
			name, ext  string
			normalized string
		}
		state := make(map[int64]final, len(current))
		for _, st := range current {
			state[st.ID] = final{st.Status, st.FullName, st.ExternalID, st.NormalizedName}
		}
		touched := map[int64]bool{}
		for _, id := range ops.Archive {
			if byID[id] == nil {
				return fmt.Errorf("%w: student %d is not in group %d", ErrInvalid, id, groupID)
			}
			if touched[id] {
				return fmt.Errorf("%w: student %d appears twice in the import", ErrInvalid, id)
			}
			touched[id] = true
			f := state[id]
			f.status = model.StatusArchived
			state[id] = f
		}
		for _, u := range ops.Update {
			if byID[u.StudentID] == nil {
				return fmt.Errorf("%w: student %d is not in group %d", ErrInvalid, u.StudentID, groupID)
			}
			if touched[u.StudentID] {
				return fmt.Errorf("%w: student %d appears twice in the import", ErrInvalid, u.StudentID)
			}
			touched[u.StudentID] = true
			n := names.Normalize(u.FullName)
			if n == "" {
				return fmt.Errorf("%w: empty student name", ErrInvalid)
			}
			state[u.StudentID] = final{model.StatusActive, u.FullName, u.ExternalID, n}
		}
		createNorm := make([]string, len(ops.Create))
		for i, c := range ops.Create {
			createNorm[i] = names.Normalize(c.FullName)
			if createNorm[i] == "" {
				return fmt.Errorf("%w: empty student name", ErrInvalid)
			}
		}

		// Namespace check on the final state. Deterministic order for stable errors.
		type entry struct {
			owner   int64 // 0 = new student
			isAlias bool
		}
		ns := map[string]entry{}
		claim := func(n string, e entry) error {
			if prev, ok := ns[n]; ok {
				// Report the holder the import does not change (an untouched existing
				// student), otherwise any existing student, rather than a new row.
				owner, isAlias := prev.owner, prev.isAlias
				if owner == 0 || (touched[owner] && e.owner != 0 && !touched[e.owner]) {
					owner, isAlias = e.owner, e.isAlias
				}
				return &NameTakenError{Name: n, StudentID: owner, IsAlias: isAlias}
			}
			ns[n] = e
			return nil
		}
		ids := make([]int64, 0, len(state))
		for id := range state {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			f := state[id]
			if f.status != model.StatusActive {
				continue
			}
			if err := claim(f.normalized, entry{owner: id}); err != nil {
				return err
			}
			for _, a := range byID[id].Aliases {
				if err := claim(a.NormalizedAlias, entry{owner: id, isAlias: true}); err != nil {
					return err
				}
			}
		}
		for _, n := range createNorm {
			if err := claim(n, entry{}); err != nil {
				return err
			}
		}

		// Apply. 1) archive — frees names.
		now := tx.nowMS()
		for _, id := range ops.Archive {
			if byID[id].Status == model.StatusArchived {
				continue
			}
			if _, err := tx.exec(ctx, `UPDATE students SET status = 'archived', updated_at = ? WHERE id = ?`, now, id); err != nil {
				return fmt.Errorf("store: import archive: %w", err)
			}
			res.Archived++
		}
		// 2) updates. Students whose active name changes first get a unique
		// placeholder so that swaps/chains never hit the partial unique index midway.
		var changed []ImportUpdate
		for _, u := range ops.Update {
			cur := byID[u.StudentID]
			f := state[u.StudentID]
			if cur.FullName == f.name && cur.ExternalID == f.ext && cur.Status == model.StatusActive {
				continue
			}
			changed = append(changed, u)
		}
		for _, u := range changed {
			cur := byID[u.StudentID]
			if cur.Status == model.StatusActive && cur.NormalizedName == state[u.StudentID].normalized {
				continue // keeps its own name; no transient collision possible
			}
			// Upper case never survives names.Normalize, so the placeholder cannot
			// collide with a real normalized name (PostgreSQL text cannot hold NUL).
			placeholder := "\x01IMPORT:" + strconv.FormatInt(u.StudentID, 10)
			if _, err := tx.exec(ctx, `UPDATE students SET normalized_name = ? WHERE id = ?`, placeholder, u.StudentID); err != nil {
				return fmt.Errorf("store: import update: %w", err)
			}
		}
		for _, u := range changed {
			f := state[u.StudentID]
			if _, err := tx.exec(ctx, `UPDATE students SET full_name = ?, normalized_name = ?, external_id = ?, status = 'active',
				updated_at = ? WHERE id = ?`, f.name, f.normalized, f.ext, now, u.StudentID); err != nil {
				if db.IsUniqueViolation(err) {
					return &NameTakenError{Name: f.normalized}
				}
				return fmt.Errorf("store: import update: %w", err)
			}
			res.Updated++
		}
		// 3) creates.
		for i, c := range ops.Create {
			id, _, err := tx.insertStudent(ctx, groupID, c.ExternalID, c.FullName, createNorm[i], model.StatusActive)
			if err != nil {
				return err
			}
			res.CreatedIDs = append(res.CreatedIDs, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}
