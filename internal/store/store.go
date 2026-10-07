// Package store holds all SQL of the service. One Store type wraps either the
// connection pool or a transaction; every query is written with '?' placeholders
// and passed through db.Rebind, so the same code runs on SQLite and PostgreSQL.
//
// Conventions shared by every method:
//   - All times are UTC time.Time in Go and unix milliseconds in the database
//     (precision is therefore 1 ms; sub-millisecond parts are truncated). The
//     conversion helpers in this file are the only place that does it.
//   - Missing rows are reported as ErrNotFound (returned unwrapped, so both
//     errors.Is and == work). Unique-key collisions become ErrConflict, except
//     student-name collisions inside a group which become ErrNameTaken (a
//     *NameTakenError carrying the owner).
//   - Methods that write several statements run them in one transaction (joining
//     the caller's transaction when called on a tx Store from InTx).
//   - Normalized names (groups, students, aliases) are computed by the store with
//     names.Normalize; callers never pass them in.
//   - created_at / updated_at are taken from the store clock (WithClock), unless a
//     method documents that it uses a time from its arguments.
//   - List methods return an empty, non-nil slice when nothing matches (so the
//     result encodes as [] in JSON, never null).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"zoom-checker/internal/db"
)

// Sentinel errors.
var (
	// ErrNotFound means the requested row does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict means a unique key is already used (e.g. user name, group name,
	// meeting UUID).
	ErrConflict = errors.New("store: conflict")
	// ErrNameTaken means a student name or alias collides with another active
	// student name or alias of the same group. The concrete error is a
	// *NameTakenError (use errors.As to get the owner).
	ErrNameTaken = errors.New("store: name already used in the group")
	// ErrInvalid means the input cannot be stored (e.g. an empty normalized name,
	// unknown enum value, duplicate keys inside one batch).
	ErrInvalid = errors.New("store: invalid input")
)

// NameTakenError reports which name collided and who owns it.
// errors.Is(err, ErrNameTaken) is true for it.
type NameTakenError struct {
	Name      string // normalized name that is taken
	StudentID int64  // current owner; 0 when the owner is another row of the same import batch
	IsAlias   bool   // the owner uses Name as an alias (false: as the student's full name)
}

func (e *NameTakenError) Error() string {
	what := "name"
	if e.IsAlias {
		what = "alias"
	}
	if e.StudentID == 0 {
		return fmt.Sprintf("store: name %q is used twice in the import", e.Name)
	}
	return fmt.Sprintf("store: name %q is already the %s of student %d", e.Name, what, e.StudentID)
}

// Is makes errors.Is(err, ErrNameTaken) work.
func (e *NameTakenError) Is(target error) bool { return target == ErrNameTaken }

// querier is satisfied by *sql.DB and *sql.Tx.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store gives typed access to the database. The zero value is not usable; create
// it with New. A Store is safe for concurrent use; a transaction Store (the one
// passed to the InTx callback) must only be used inside that callback.
type Store struct {
	db  *db.DB
	q   querier
	tx  *sql.Tx
	now func() time.Time
}

// Option configures a Store.
type Option func(*Store)

// WithClock replaces time.Now as the source of created_at/updated_at stamps
// (tests use a fixed clock).
func WithClock(now func() time.Time) Option {
	return func(s *Store) {
		if now != nil {
			s.now = now
		}
	}
}

// New returns a Store over d.
func New(d *db.DB, opts ...Option) *Store {
	s := &Store{db: d, q: d.DB, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

// DB returns the underlying database handle.
func (s *Store) DB() *db.DB { return s.db }

// Dialect returns the database dialect.
func (s *Store) Dialect() db.Dialect { return s.db.Dialect }

// InTx runs fn inside one database transaction and commits when fn returns nil;
// any error (or panic) rolls back. Called on a Store that is already inside a
// transaction, it simply runs fn with that same Store (no savepoints), so methods
// can compose freely.
//
// On SQLite the transaction takes the write lock at BEGIN (immediate mode), so
// transactions are serialized. On PostgreSQL it runs at READ COMMITTED; a failed
// statement aborts the transaction, so after any error fn must return it.
func (s *Store) InTx(ctx context.Context, fn func(tx *Store) error) error {
	if s.tx != nil {
		return fn(s)
	}
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = sqlTx.Rollback()
		}
	}()
	txs := &Store{db: s.db, q: sqlTx, tx: sqlTx, now: s.now}
	if err := fn(txs); err != nil {
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	committed = true
	return nil
}

// InTransaction reports whether s is bound to a transaction.
func (s *Store) InTransaction() bool { return s.tx != nil }

func (s *Store) exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return s.q.ExecContext(ctx, s.db.Rebind(q), args...)
}

func (s *Store) query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return s.q.QueryContext(ctx, s.db.Rebind(q), args...)
}

func (s *Store) queryRow(ctx context.Context, q string, args ...any) *sql.Row {
	return s.q.QueryRowContext(ctx, s.db.Rebind(q), args...)
}

// execAffected runs q and returns the number of affected rows.
func (s *Store) execAffected(ctx context.Context, q string, args ...any) (int64, error) {
	res, err := s.exec(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// execOne runs q and returns ErrNotFound when no row was affected.
func (s *Store) execOne(ctx context.Context, q string, args ...any) error {
	n, err := s.execAffected(ctx, q, args...)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// nowMS is the store clock in unix ms.
func (s *Store) nowMS() int64 { return s.now().UnixMilli() }

// ---- scanning helpers ----

type scanner interface{ Scan(dest ...any) error }

// collect scans every row with scan and closes rows. The result is never nil.
func collect[T any](rows *sql.Rows, scan func(scanner) (T, error)) ([]T, error) {
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// notFound maps sql.ErrNoRows to ErrNotFound.
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// ---- time / nullable conversion (the only place that knows the storage format) ----

// toMS converts t to unix milliseconds.
func toMS(t time.Time) int64 { return t.UnixMilli() }

// fromMS converts unix milliseconds to a UTC time.
func fromMS(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// msArg returns nil for a nil time, else unix ms — for nullable *_at columns.
func msArg(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UnixMilli()
}

// fromNullMS converts a nullable ms column.
func fromNullMS(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMS(v.Int64)
	return &t
}

// int64Arg returns nil for a nil pointer.
func int64Arg(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func fromNullInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

// stringArg returns nil for a nil string pointer.
func stringArg[T ~string](p *T) any {
	if p == nil {
		return nil
	}
	return string(*p)
}

// placeholders returns "?,?,...,?" with n entries.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

// int64sArgs converts ids into query args.
func int64sArgs(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// chunks splits n items into [lo,hi) ranges of at most size items.
func chunks(n, size int) [][2]int {
	var out [][2]int
	for lo := 0; lo < n; lo += size {
		out = append(out, [2]int{lo, min(lo+size, n)})
	}
	return out
}

// inChunk is the maximum number of values passed in one IN (...) list.
const inChunk = 500

// joinIDs / splitIDs store []int64 as a comma list (zoom_participants.candidate_student_ids).
func joinIDs(ids []int64) string {
	if len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

func splitIDs(s string) ([]int64, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("store: bad id list %q: %w", s, err)
		}
		out = append(out, id)
	}
	return out, nil
}

// limitClause renders " LIMIT n" for n > 0.
func limitClause(n int) string {
	if n <= 0 {
		return ""
	}
	return " LIMIT " + strconv.Itoa(n)
}
