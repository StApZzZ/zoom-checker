package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"zoom-checker/internal/db"
	"zoom-checker/internal/model"
)

// The suite runs every test against SQLite (always) and PostgreSQL when
// TEST_POSTGRES_URL is set, e.g.
//
//	docker run -d --name zc-test-pg -e POSTGRES_PASSWORD=test -e POSTGRES_DB=zc -p 55432:5432 postgres:18-alpine
//	TEST_POSTGRES_URL='postgres://postgres:test@localhost:55432/zc?sslmode=disable' go test ./internal/store/
//
// PostgreSQL isolation: a template database is migrated once per test binary and
// every test gets its own database cloned from it (CREATE DATABASE ... TEMPLATE).

var t0 = time.Date(2026, 3, 2, 9, 0, 0, 123_000_000, time.UTC)

// fakeClock is a settable clock shared by a test's Store.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	return c.now
}

type env struct {
	t     *testing.T
	ctx   context.Context
	s     *Store
	clock *fakeClock
}

// forEachDB runs fn as parallel subtests "sqlite" and "postgres".
func forEachDB(t *testing.T, fn func(t *testing.T, e *env)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()
		d, err := db.Open(context.Background(), "sqlite://"+filepath.ToSlash(filepath.Join(t.TempDir(), "zc.db")))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Close() })
		if err := d.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		fn(t, newEnv(t, d))
	})
	t.Run("postgres", func(t *testing.T) {
		if os.Getenv("TEST_POSTGRES_URL") == "" {
			t.Skip("TEST_POSTGRES_URL not set")
		}
		t.Parallel()
		fn(t, newEnv(t, openPostgres(t)))
	})
}

func newEnv(t *testing.T, d *db.DB) *env {
	c := &fakeClock{now: t0}
	return &env{t: t, ctx: context.Background(), s: New(d, WithClock(c.Now)), clock: c}
}

var (
	pgOnce     sync.Once
	pgAdmin    *sql.DB
	pgTemplate string
	pgErr      error
	pgSeq      atomic.Int64
)

func pgURL(base, dbName string) string {
	u, err := url.Parse(base)
	if err != nil {
		panic(err)
	}
	u.Path = "/" + dbName
	return u.String()
}

// openPostgres creates a fresh database from the migrated template.
func openPostgres(t *testing.T) *db.DB {
	t.Helper()
	base := os.Getenv("TEST_POSTGRES_URL")
	ctx := context.Background()
	pgOnce.Do(func() {
		pgAdmin, pgErr = sql.Open("pgx", base)
		if pgErr != nil {
			return
		}
		pgTemplate = fmt.Sprintf("zc_tpl_%d_%d", os.Getpid(), time.Now().UnixNano()%1_000_000)
		if _, pgErr = pgAdmin.ExecContext(ctx, "CREATE DATABASE "+pgTemplate); pgErr != nil {
			return
		}
		var tpl *db.DB
		if tpl, pgErr = db.Open(ctx, pgURL(base, pgTemplate)); pgErr != nil {
			return
		}
		pgErr = tpl.Migrate(ctx)
		_ = tpl.Close()
	})
	if pgErr != nil {
		t.Fatalf("postgres template: %v", pgErr)
	}
	name := fmt.Sprintf("%s_t%d", pgTemplate, pgSeq.Add(1))
	if _, err := pgAdmin.ExecContext(ctx, "CREATE DATABASE "+name+" TEMPLATE "+pgTemplate); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	d, err := db.Open(ctx, pgURL(base, name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = d.Close()
		_, _ = pgAdmin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	return d
}

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler)) // silence "migration applied" lines
	code := m.Run()
	if pgAdmin != nil {
		if pgTemplate != "" {
			_, _ = pgAdmin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+pgTemplate+" WITH (FORCE)")
		}
		_ = pgAdmin.Close()
	}
	os.Exit(code)
}

// ---- fixtures & assertions ----

func (e *env) must(err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) group(name string) *model.Group {
	e.t.Helper()
	g := &model.Group{Name: name}
	e.must(e.s.CreateGroup(e.ctx, g))
	return g
}

func (e *env) student(groupID int64, name string) *model.Student {
	e.t.Helper()
	st := &model.Student{GroupID: groupID, FullName: name}
	e.must(e.s.CreateStudent(e.ctx, st))
	return st
}

func (e *env) lesson(groupID int64, meetingID string, start time.Time, dur time.Duration) *model.Lesson {
	e.t.Helper()
	l := &model.Lesson{GroupID: groupID, Title: "Lesson", ZoomMeetingID: meetingID, PlannedStartAt: start, PlannedEndAt: start.Add(dur)}
	e.must(e.s.CreateLesson(e.ctx, l))
	return l
}

func ptr[T any](v T) *T { return &v }

func wantErr(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("err = %v, want %v", err, target)
	}
}

func wantTime(t *testing.T, what string, got *time.Time, want *time.Time) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil || want == nil:
		t.Errorf("%s = %v, want %v", what, got, want)
	case !got.Equal(*want) || got.Location() != time.UTC:
		t.Errorf("%s = %v (%v), want %v UTC", what, *got, got.Location(), *want)
	}
}

// ---- generic behaviour ----

func TestMigrateIdempotentOnStoreDB(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		for range 2 {
			e.must(e.s.DB().Migrate(e.ctx))
		}
		v, err := e.s.DB().MigrationVersion(e.ctx)
		e.must(err)
		if v != 1 {
			t.Errorf("version = %d", v)
		}
	})
}

func TestTimeRoundTrip(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		g := e.group("G")
		msk := time.FixedZone("MSK", 3*3600)
		// Sub-millisecond parts are truncated; any input zone comes back as UTC.
		start := time.Date(2026, 5, 17, 12, 34, 56, 789_654_321, msk)
		end := start.Add(90 * time.Minute)
		actual := time.Date(1999, 12, 31, 23, 59, 59, 999_999_999, time.UTC)
		l := &model.Lesson{GroupID: g.ID, Title: "T", ZoomMeetingID: "123456789", PlannedStartAt: start, PlannedEndAt: end,
			ActualStartAt: &actual}
		e.must(e.s.CreateLesson(e.ctx, l))
		got, err := e.s.GetLesson(e.ctx, l.ID)
		e.must(err)
		wantStart := start.Truncate(time.Millisecond).UTC()
		if !got.PlannedStartAt.Equal(wantStart) || got.PlannedStartAt.Location() != time.UTC {
			t.Errorf("planned start = %v, want %v", got.PlannedStartAt, wantStart)
		}
		if got.PlannedStartAt.Nanosecond() != 789_000_000 {
			t.Errorf("ms precision lost: %d ns", got.PlannedStartAt.Nanosecond())
		}
		wantTime(t, "actual start", got.ActualStartAt, ptr(actual.Truncate(time.Millisecond)))
		wantTime(t, "actual end", got.ActualEndAt, nil)
		if !got.CreatedAt.Equal(t0.Truncate(time.Millisecond)) {
			t.Errorf("created_at = %v, want clock %v", got.CreatedAt, t0)
		}
	})
}

func TestInTx(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		boom := errors.New("boom")
		// Rollback on error.
		err := e.s.InTx(e.ctx, func(tx *Store) error {
			if !tx.InTransaction() {
				t.Error("tx store not in transaction")
			}
			if err := tx.CreateGroup(e.ctx, &model.Group{Name: "Rolled back"}); err != nil {
				return err
			}
			return boom
		})
		wantErr(t, err, boom)
		_, err = e.s.GetGroupByNormalizedName(e.ctx, "rolled back")
		wantErr(t, err, ErrNotFound)

		// Nested InTx joins the outer transaction; commit applies both.
		err = e.s.InTx(e.ctx, func(tx *Store) error {
			if err := tx.CreateGroup(e.ctx, &model.Group{Name: "Outer"}); err != nil {
				return err
			}
			return tx.InTx(e.ctx, func(inner *Store) error {
				if inner != tx {
					t.Error("nested InTx did not reuse the transaction store")
				}
				return inner.CreateGroup(e.ctx, &model.Group{Name: "Inner"})
			})
		})
		e.must(err)
		for _, n := range []string{"outer", "inner"} {
			if _, err := e.s.GetGroupByNormalizedName(e.ctx, n); err != nil {
				t.Errorf("group %q: %v", n, err)
			}
		}

		// Nested error rolls back everything.
		err = e.s.InTx(e.ctx, func(tx *Store) error {
			if err := tx.CreateGroup(e.ctx, &model.Group{Name: "Outer2"}); err != nil {
				return err
			}
			return tx.InTx(e.ctx, func(inner *Store) error { return boom })
		})
		wantErr(t, err, boom)
		_, err = e.s.GetGroupByNormalizedName(e.ctx, "outer2")
		wantErr(t, err, ErrNotFound)

		// Panic rolls back and propagates.
		func() {
			defer func() {
				if recover() == nil {
					t.Error("panic was swallowed")
				}
			}()
			_ = e.s.InTx(e.ctx, func(tx *Store) error {
				_ = tx.CreateGroup(e.ctx, &model.Group{Name: "Panicked"})
				panic("boom")
			})
		}()
		_, err = e.s.GetGroupByNormalizedName(e.ctx, "panicked")
		wantErr(t, err, ErrNotFound)
		if e.s.InTransaction() {
			t.Error("base store reports a transaction")
		}
	})
}

func TestHelpers(t *testing.T) {
	t.Parallel()
	if got := placeholders(3); got != "?,?,?" {
		t.Errorf("placeholders = %q", got)
	}
	if got := placeholders(0); got != "" {
		t.Errorf("placeholders(0) = %q", got)
	}
	if got := chunks(5, 2); fmt.Sprint(got) != "[[0 2] [2 4] [4 5]]" {
		t.Errorf("chunks = %v", got)
	}
	if got := chunks(0, 2); len(got) != 0 {
		t.Errorf("chunks(0) = %v", got)
	}
	ids, err := splitIDs(joinIDs([]int64{3, 1, 22}))
	if err != nil || fmt.Sprint(ids) != "[3 1 22]" {
		t.Errorf("ids round trip = %v, %v", ids, err)
	}
	if ids, err := splitIDs(""); err != nil || ids != nil {
		t.Errorf("splitIDs(\"\") = %v, %v", ids, err)
	}
	if _, err := splitIDs("1,x"); err == nil {
		t.Error("splitIDs accepted garbage")
	}
	if got := prefixCols("a", "x, y,\n\tz"); got != "a.x, a.y, a.z" {
		t.Errorf("prefixCols = %q", got)
	}
	if got := escapeLike(`50%_\`); got != `50\%\_\\` {
		t.Errorf("escapeLike = %q", got)
	}
	if got := digitsOnly("857 4606-5432"); got != "85746065432" {
		t.Errorf("digitsOnly = %q", got)
	}
	if got := digitsOnly("abc 1"); got != "" {
		t.Errorf("digitsOnly = %q", got)
	}
	nt := &NameTakenError{Name: "x", StudentID: 5}
	if !errors.Is(fmt.Errorf("wrap: %w", nt), ErrNameTaken) || errors.Is(nt, ErrConflict) {
		t.Error("NameTakenError Is mismatch")
	}
}

// TestTimeConversion checks the ms helpers, including pre-1970 instants where
// truncation must go down (floor), not towards zero.
func TestTimeConversion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   time.Time
		want int64
	}{
		{time.Unix(0, 0), 0},
		{time.Date(1969, 12, 31, 23, 59, 59, 999_500_000, time.UTC), -1},
		{time.Date(1969, 12, 31, 23, 59, 59, 0, time.UTC), -1000},
		{time.Date(2026, 3, 2, 12, 0, 0, 1_999_999, time.FixedZone("X", -5*3600)), 1772470800001},
	}
	for _, tt := range tests {
		if got := toMS(tt.in); got != tt.want {
			t.Errorf("toMS(%v) = %d, want %d", tt.in, got, tt.want)
		}
		// time.Truncate rounds towards the zero time, i.e. down for every instant.
		if back := fromMS(tt.want); back.Location() != time.UTC || !back.Equal(tt.in.Truncate(time.Millisecond)) {
			t.Errorf("fromMS(%d) = %v, want %v UTC", tt.want, back, tt.in.Truncate(time.Millisecond))
		}
	}
	if got := fromMS(-1); !got.Equal(time.Date(1969, 12, 31, 23, 59, 59, 999_000_000, time.UTC)) {
		t.Errorf("fromMS(-1) = %v", got)
	}
	if msArg(nil) != nil || fromNullMS(sql.NullInt64{}) != nil {
		t.Error("nil handling")
	}
}

// TestEmptyListsAreNonNil: list methods return [] (not nil) so handlers encode
// JSON arrays.
func TestEmptyListsAreNonNil(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		ctx := e.ctx
		check := func(name string, isNil bool, err error) {
			t.Helper()
			e.must(err)
			if isNil {
				t.Errorf("%s returned nil", name)
			}
		}
		users, err := e.s.ListUsers(ctx)
		check("ListUsers", users == nil, err)
		groups, err := e.s.ListGroups(ctx, GroupFilterAll, t0)
		check("ListGroups", groups == nil, err)
		gnames, err := e.s.ListGroupNames(ctx, 1)
		check("ListGroupNames", gnames == nil, err)
		lessons, err := e.s.ListLessons(ctx, LessonFilter{})
		check("ListLessons", lessons == nil, err)
		cands, err := e.s.FindLessonsByMeeting(ctx, "1", t0, t0.Add(time.Hour))
		check("FindLessonsByMeeting", cands == nil, err)
		ms, err := e.s.ListZoomMeetings(ctx, ZoomMeetingFilter{})
		check("ListZoomMeetings", ms == nil, err)
		evs, err := e.s.ListWebhookEvents(ctx, EventFilter{})
		check("ListWebhookEvents", evs == nil, err)
		due, err := e.s.ListDueEvents(ctx, t0, 10)
		check("ListDueEvents", due == nil, err)
		pevs, err := e.s.ListParticipantEvents(ctx, nil)
		check("ListParticipantEvents", pevs == nil, err)
		ps, err := e.s.ListParticipants(ctx, 1)
		check("ListParticipants", ps == nil, err)
		att, err := e.s.ListAttendance(ctx, 1)
		check("ListAttendance", att == nil, err)
		ivs, err := e.s.ListIntervals(ctx, 1)
		check("ListIntervals", ivs == nil, err)
		maps, err := e.s.ListMappings(ctx, 1)
		check("ListMappings", maps == nil, err)
		audit, err := e.s.ListAudit(ctx, AuditFilter{})
		check("ListAudit", audit == nil, err)
	})
}
