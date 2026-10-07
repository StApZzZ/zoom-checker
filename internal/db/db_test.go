package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRebindDollar(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in, want string
	}{
		{"no placeholders", "SELECT 1", "SELECT 1"},
		{"simple", "SELECT * FROM t WHERE a = ? AND b = ?", "SELECT * FROM t WHERE a = $1 AND b = $2"},
		{"many", "VALUES (?,?,?,?,?,?,?,?,?,?,?)", "VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)"},
		{"single quoted", "SELECT '?' , ? FROM t WHERE x = 'a?b'", "SELECT '?' , $1 FROM t WHERE x = 'a?b'"},
		{"escaped quote", "SELECT 'it''s ?', ?", "SELECT 'it''s ?', $1"},
		{"double quoted identifier", `SELECT "we?rd", ? FROM t`, `SELECT "we?rd", $1 FROM t`},
		{"line comment", "SELECT ? -- why?\n, ?", "SELECT $1 -- why?\n, $2"},
		{"cast after placeholder", "SELECT ?::int4", "SELECT $1::int4"},
		{"like pattern", "WHERE a LIKE ? ESCAPE '\\'", "WHERE a LIKE $1 ESCAPE '\\'"},
		{"minus is not comment", "SELECT ? - ?", "SELECT $1 - $2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := rebindDollar(tt.in); got != tt.want {
				t.Errorf("rebindDollar(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRebindDialects(t *testing.T) {
	t.Parallel()
	q := "SELECT ? FROM t WHERE a = '?'"
	if got := (&DB{Dialect: SQLite}).Rebind(q); got != q {
		t.Errorf("sqlite Rebind changed query: %q", got)
	}
	if got, want := (&DB{Dialect: Postgres}).Rebind(q), "SELECT $1 FROM t WHERE a = '?'"; got != want {
		t.Errorf("postgres Rebind = %q, want %q", got, want)
	}
}

func TestSQLitePath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		path    string
		memory  bool
		wantErr bool
	}{
		{"sqlite::memory:", "", true, false},
		{"sqlite://:memory:", "", true, false},
		{"sqlite://data/zoom-checker.db", filepath.FromSlash("data/zoom-checker.db"), false, false},
		{"sqlite://./data/x.db", filepath.FromSlash("./data/x.db"), false, false},
		{"sqlite:///var/lib/zc/x.db", filepath.FromSlash("/var/lib/zc/x.db"), false, false},
		{"sqlite:", "", false, true},
		{"sqlite://", "", false, true},
		{"sqlite://x.db?mode=ro", "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			path, memory, err := sqlitePath(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if path != tt.path || memory != tt.memory {
				t.Errorf("got (%q, %v), want (%q, %v)", path, memory, tt.path, tt.memory)
			}
		})
	}
}

func TestOpenRejectsUnknownScheme(t *testing.T) {
	t.Parallel()
	_, err := Open(context.Background(), "mysql://root:secret@localhost/db")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("error leaks password: %v", err)
	}
}

func TestRedactURL(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"postgres://u:p@h/db":    "postgres://u:xxxxx@h/db",
		"postgres://u@h/db":      "postgres://u@h/db",
		"postgres://h/db":        "postgres://h/db",
		"postgres://u:p@ss@h/d":  "postgres://u:xxxxx@h/d",
		"postgres://u:p/s?s@h/d": "postgres://u:xxxxx@h/d",
		"postgres://h/db?user=u&password=s3cret&sslmode=disable": "postgres://h/db?user=u&password=xxxxx&sslmode=disable",
		"postgresql://h/db?sslpassword=k3y":                      "postgresql://h/db?sslpassword=xxxxx",
		"postgres://u:p@h/db?PASSWORD=x":                         "postgres://u:xxxxx@h/db?PASSWORD=xxxxx",
		"mysql://root:secret@localhost/db":                       "mysql://root:xxxxx@localhost/db",
	}
	for in, want := range tests {
		if got := redactURL(in); got != want {
			t.Errorf("redactURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOpenSQLiteFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "nested", "dir")
	d, err := Open(ctx, "sqlite://"+filepath.ToSlash(filepath.Join(dir, "zc.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Dialect != SQLite {
		t.Fatalf("dialect = %q", d.Dialect)
	}
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir mode = %v, want 0700", perm)
	}
	pragmas := map[string]string{
		"journal_mode": "wal",
		"foreign_keys": "1",
		"busy_timeout": "10000",
		"synchronous":  "1",
	}
	for p, want := range pragmas {
		var got string
		if err := d.QueryRowContext(ctx, "PRAGMA "+p).Scan(&got); err != nil {
			t.Fatalf("pragma %s: %v", p, err)
		}
		if strings.ToLower(got) != want {
			t.Errorf("pragma %s = %q, want %q", p, got, want)
		}
	}
	if d.SQLitePath() == "" {
		t.Error("SQLitePath is empty for a file database")
	}
}

// openTestDBs returns one migrated database per available dialect.
func openTestDBs(t *testing.T) map[Dialect]*DB {
	t.Helper()
	ctx := context.Background()
	out := map[Dialect]*DB{}
	d, err := Open(ctx, "sqlite://"+filepath.ToSlash(filepath.Join(t.TempDir(), "zc.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	out[SQLite] = d
	if pg := openPostgresSchema(t); pg != nil {
		out[Postgres] = pg
	}
	return out
}

var schemaSeq atomic.Int64

// openPostgresSchema opens TEST_POSTGRES_URL with a private, freshly created schema
// (dropped at cleanup). Returns nil when the env var is not set.
func openPostgresSchema(t *testing.T) *DB {
	t.Helper()
	base := os.Getenv("TEST_POSTGRES_URL")
	if base == "" {
		return nil
	}
	ctx := context.Background()
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("zc_dbtest_%d_%d", time.Now().UnixNano(), schemaSeq.Add(1))
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	d, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = d.Close()
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	})
	return d
}

func TestMigrateIdempotent(t *testing.T) {
	t.Parallel()
	for dialect, d := range openTestDBs(t) {
		t.Run(string(dialect), func(t *testing.T) {
			ctx := context.Background()
			for i := range 3 {
				if err := d.Migrate(ctx); err != nil {
					t.Fatalf("migrate #%d: %v", i+1, err)
				}
			}
			v, err := d.MigrationVersion(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if v != 1 {
				t.Errorf("version = %d, want 1", v)
			}
			var n int
			if err := d.QueryRowContext(ctx, d.Rebind("SELECT COUNT(*) FROM users WHERE id > ?"), 0).Scan(&n); err != nil {
				t.Fatalf("users table missing: %v", err)
			}
		})
	}
}

func TestIsUniqueViolation(t *testing.T) {
	t.Parallel()
	for dialect, d := range openTestDBs(t) {
		t.Run(string(dialect), func(t *testing.T) {
			ctx := context.Background()
			if err := d.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			ins := d.Rebind(`INSERT INTO student_groups (name, normalized_name, created_at, updated_at) VALUES (?, ?, ?, ?)`)
			if _, err := d.ExecContext(ctx, ins, "A", "a", 1, 1); err != nil {
				t.Fatal(err)
			}
			_, err := d.ExecContext(ctx, ins, "A", "a", 1, 1)
			if !IsUniqueViolation(err) {
				t.Fatalf("IsUniqueViolation(%v) = false", err)
			}
			if !IsUniqueViolation(fmt.Errorf("wrapped: %w", err)) {
				t.Error("wrapped error not detected")
			}
			// A foreign key / check violation is not a unique violation.
			_, err = d.ExecContext(ctx, d.Rebind(`INSERT INTO student_groups (name, normalized_name, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`), "B", "b", "bogus", 1, 1)
			if err == nil {
				t.Fatal("expected check violation")
			}
			if IsUniqueViolation(err) {
				t.Errorf("check violation reported as unique: %v", err)
			}
			if IsUniqueViolation(nil) || IsUniqueViolation(errors.New("x")) {
				t.Error("false positive")
			}
		})
	}
}

func TestBackupSQLite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	d, err := Open(ctx, "sqlite://"+filepath.ToSlash(filepath.Join(dir, "zc.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO student_groups (name, normalized_name, created_at, updated_at) VALUES ('G', 'g', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "backups", "zc-2026-01-01.db")
	if err := d.Backup(ctx, dest); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("backup mode = %v, want 0600", perm)
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(filepath.Dir(dest))
	if len(entries) != 1 {
		t.Errorf("backup dir has %d entries, want 1", len(entries))
	}
	// The backup is a valid database with the data.
	b, err := Open(ctx, "sqlite://"+filepath.ToSlash(dest))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var name string
	if err := b.QueryRowContext(ctx, `SELECT name FROM student_groups`).Scan(&name); err != nil || name != "G" {
		t.Fatalf("backup content: name=%q err=%v", name, err)
	}
	v, err := b.MigrationVersion(ctx)
	if err != nil || v != 1 {
		t.Errorf("backup migration version = %d, %v", v, err)
	}
	// Existing destination is refused.
	if err := d.Backup(ctx, dest); !errors.Is(err, fs.ErrExist) {
		t.Errorf("second backup err = %v, want fs.ErrExist", err)
	}
}

func TestBackupPostgresUnsupported(t *testing.T) {
	t.Parallel()
	d := &DB{Dialect: Postgres}
	if err := d.Backup(context.Background(), "/tmp/x.db"); !errors.Is(err, ErrBackupUnsupported) {
		t.Errorf("err = %v", err)
	}
}

func TestOpenMemory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d, err := Open(ctx, "sqlite::memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// The single pooled connection keeps the schema alive across calls.
	for range 3 {
		var n int
		if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM lessons`).Scan(&n); err != nil {
			t.Fatal(err)
		}
	}
	if d.SQLitePath() != "" {
		t.Errorf("SQLitePath = %q for memory db", d.SQLitePath())
	}
}

// TestLowerExprPostgresCLocale: in a database created with the plain "C" locale
// PostgreSQL's lower() folds only ASCII; LowerExpr must still fold Cyrillic
// (via pg_c_utf8 on PostgreSQL 17+).
func TestLowerExprPostgresCLocale(t *testing.T) {
	t.Parallel()
	base := os.Getenv("TEST_POSTGRES_URL")
	if base == "" {
		t.Skip("TEST_POSTGRES_URL not set")
	}
	ctx := context.Background()
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var version int
	if err := admin.QueryRowContext(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("zc_clocale_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name+" TEMPLATE template0 ENCODING 'UTF8' LC_COLLATE 'C' LC_CTYPE 'C'"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	}()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	d, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var got string
	if err := d.QueryRowContext(ctx, `SELECT `+d.LowerExpr(`$1::text`), "Иванов ЁЖ Ivan").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if version < 170000 {
		t.Skipf("PostgreSQL %d has no pg_c_utf8; lower() = %q", version, got)
	}
	if got != "иванов ёж ivan" {
		t.Errorf("LowerExpr in a C-locale database = %q", got)
	}
}
