// Package db opens the service database (SQLite by default, PostgreSQL optional),
// applies the embedded migrations and hides the few dialect differences that the
// store layer has to care about (placeholders, unique-violation detection, backups).
// See docs/architecture.md §1 and §4.
package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"zoom-checker/internal/config"
)

// Dialect identifies the SQL flavour of an open database.
type Dialect string

// Supported dialects. The values double as migration sub-directory names.
const (
	SQLite   Dialect = "sqlite"
	Postgres Dialect = "postgres"
)

// ErrBackupUnsupported is returned by Backup for databases that cannot be backed
// up from inside the process (PostgreSQL: use pg_dump).
var ErrBackupUnsupported = errors.New("db: online backup is supported for SQLite only; use pg_dump for PostgreSQL")

// sqlitePragmas are applied to every new SQLite connection. busy_timeout comes first
// so that switching to WAL waits for other connections instead of failing.
// _txlock=immediate makes every transaction take the write lock at BEGIN, which
// avoids SQLITE_BUSY on read→write lock upgrades (the busy handler cannot retry those).
const sqlitePragmas = "_pragma=busy_timeout(10000)" +
	"&_pragma=journal_mode(WAL)" +
	"&_pragma=foreign_keys(1)" +
	"&_pragma=synchronous(NORMAL)" +
	"&_txlock=immediate"

// Pool sizes. SQLite serializes writers anyway (WAL allows concurrent readers), so a
// small pool is enough; an in-memory database must use exactly one connection because
// every connection would otherwise see its own empty database.
const (
	sqliteMaxConns   = 4
	postgresMaxConns = 10
)

//go:embed migrations
var migrationsFS embed.FS

// sqliteLowerFunc is a Unicode-aware lower() for SQLite (the built-in one only
// folds ASCII), so case-insensitive search works for Cyrillic names too.
const sqliteLowerFunc = "zc_lower"

func init() {
	sqlite.MustRegisterDeterministicScalarFunction(sqliteLowerFunc, 1,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			switch v := args[0].(type) {
			case string:
				return strings.ToLower(v), nil
			case []byte:
				return strings.ToLower(string(v)), nil
			default: // NULL and numbers pass through
				return v, nil
			}
		})
}

// LowerExpr wraps a SQL expression in a Unicode-aware lower-case function of the
// dialect. PostgreSQL's lower() follows the database locale and folds only ASCII
// under the plain "C" locale; for such databases Open picks the built-in
// pg_c_utf8 collation (PostgreSQL 17+) when available.
func (d *DB) LowerExpr(expr string) string {
	if d.Dialect == SQLite {
		return sqliteLowerFunc + "(" + expr + ")"
	}
	return "lower(" + expr + d.pgLowerCollate + ")"
}

// pgCUTF8Collate makes lower() Unicode-aware independent of the OS locale.
const pgCUTF8Collate = " COLLATE pg_c_utf8"

// probePGLower reports what LowerExpr must add after the expression so that
// lower() folds Cyrillic: "" when the database locale already does, the
// pg_c_utf8 collation when the locale does not but the server has it (17+),
// "" otherwise (search then stays ASCII-only case-insensitive).
func probePGLower(ctx context.Context, sqlDB *sql.DB) string {
	var ok bool
	if err := sqlDB.QueryRowContext(ctx, `SELECT lower('ЁЖ') = 'ёж'`).Scan(&ok); err == nil && ok {
		return ""
	}
	if err := sqlDB.QueryRowContext(ctx, `SELECT lower('ЁЖ'`+pgCUTF8Collate+`) = 'ёж'`).Scan(&ok); err == nil && ok {
		return pgCUTF8Collate
	}
	slog.WarnContext(ctx, "PostgreSQL lower() does not fold non-ASCII letters (database locale \"C\"); "+
		"event search will be case-sensitive for Cyrillic names")
	return ""
}

// DB is an open database handle together with its dialect.
type DB struct {
	*sql.DB
	Dialect Dialect

	path           string // SQLite file path ("" for in-memory / PostgreSQL)
	pgLowerCollate string // PostgreSQL: collation clause for LowerExpr (see probePGLower)
}

// Open connects to databaseURL and pings it. Accepted forms:
//
//	sqlite://relative/path.db     (relative to the working directory)
//	sqlite:///absolute/path.db
//	sqlite::memory:               (private in-memory database, tests only)
//	postgres://user:pass@host:5432/db?sslmode=...   (also postgresql://)
//
// For SQLite the parent directory is created with mode 0700 and the connection
// pragmas (WAL, busy_timeout=10s, foreign_keys, synchronous=NORMAL, immediate
// transactions) are set on every connection.
func Open(ctx context.Context, databaseURL string) (*DB, error) {
	switch {
	case strings.HasPrefix(databaseURL, "postgres://"), strings.HasPrefix(databaseURL, "postgresql://"):
		return openPostgres(ctx, databaseURL)
	case strings.HasPrefix(databaseURL, "sqlite:"):
		return openSQLite(ctx, databaseURL)
	default:
		return nil, fmt.Errorf("db: unsupported DATABASE_URL scheme (want sqlite:// or postgres://): %q", redactURL(databaseURL))
	}
}

// sqlitePath extracts the file path from a sqlite: URL. memory reports ":memory:".
func sqlitePath(databaseURL string) (path string, memory bool, err error) {
	rest := strings.TrimPrefix(databaseURL, "sqlite:")
	if rest == ":memory:" || rest == "//:memory:" {
		return "", true, nil
	}
	rest = strings.TrimPrefix(rest, "//")
	if rest == "" {
		return "", false, fmt.Errorf("db: empty SQLite path in %q", databaseURL)
	}
	// The driver treats everything after '?' as its own parameters; a literal '?' or
	// '#' in a file name would silently change the meaning, so refuse it.
	if strings.ContainsAny(rest, "?#") {
		return "", false, fmt.Errorf("db: SQLite path must not contain '?' or '#': %q", rest)
	}
	return filepath.FromSlash(rest), false, nil
}

func openSQLite(ctx context.Context, databaseURL string) (*DB, error) {
	path, memory, err := sqlitePath(databaseURL)
	if err != nil {
		return nil, err
	}
	dsn := ":memory:?" + sqlitePragmas
	if !memory {
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, fmt.Errorf("db: create database directory: %w", err)
			}
		}
		dsn = path + "?" + sqlitePragmas
	}
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open sqlite: %w", err)
	}
	if memory {
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetMaxIdleConns(1)
	} else {
		sqlDB.SetMaxOpenConns(sqliteMaxConns)
		sqlDB.SetMaxIdleConns(sqliteMaxConns)
	}
	// Connections are cheap but carry pragma state; never expire them (an expiring
	// in-memory connection would drop the whole database).
	sqlDB.SetConnMaxLifetime(0)
	sqlDB.SetConnMaxIdleTime(0)
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("db: connect sqlite %q: %w", path, err)
	}
	return &DB{DB: sqlDB, Dialect: SQLite, path: path}, nil
}

func openPostgres(ctx context.Context, databaseURL string) (*DB, error) {
	sqlDB, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("db: open postgres %s: %w", redactURL(databaseURL), err)
	}
	sqlDB.SetMaxOpenConns(postgresMaxConns)
	sqlDB.SetMaxIdleConns(postgresMaxConns / 2)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)
	sqlDB.SetConnMaxLifetime(time.Hour)
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("db: connect postgres %s: %w", redactURL(databaseURL), err)
	}
	return &DB{DB: sqlDB, Dialect: Postgres, pgLowerCollate: probePGLower(ctx, sqlDB)}, nil
}

// SQLitePath returns the database file path for a file-backed SQLite database and
// "" for an in-memory SQLite or a PostgreSQL database.
func (d *DB) SQLitePath() string { return d.path }

// redactURL hides the passwords of a connection URL for error messages
// (config.RedactDatabaseURL).
func redactURL(s string) string { return config.RedactDatabaseURL(s) }

// Rebind converts '?' placeholders to PostgreSQL's $1..$n. Question marks inside
// single-quoted string literals, double-quoted identifiers and "--" line comments
// are left alone. For SQLite the query is returned unchanged.
func (d *DB) Rebind(q string) string {
	if d.Dialect != Postgres {
		return q
	}
	return rebindDollar(q)
}

func rebindDollar(q string) string {
	if strings.IndexByte(q, '?') < 0 {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 16)
	n := 0
	var quote byte // 0, '\'' or '"'
	comment := false
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case comment:
			if c == '\n' {
				comment = false
			}
		case quote != 0:
			// A doubled quote ('' or "") toggles twice and so stays inside the literal.
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '-' && i+1 < len(q) && q[i+1] == '-':
			comment = true
		case c == '?':
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// IsUniqueViolation reports whether err is a UNIQUE / PRIMARY KEY constraint
// violation from either driver.
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			return true
		}
		return false
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code == "23505" // unique_violation
	}
	return false
}

// gooseDialect maps our dialect to goose's.
func (d *DB) gooseDialect() (goose.Dialect, error) {
	switch d.Dialect {
	case SQLite:
		return goose.DialectSQLite3, nil
	case Postgres:
		return goose.DialectPostgres, nil
	}
	return "", fmt.Errorf("db: unknown dialect %q", d.Dialect)
}

// provider builds a goose provider over the embedded migrations of this dialect.
func (d *DB) provider() (*goose.Provider, error) {
	gd, err := d.gooseDialect()
	if err != nil {
		return nil, err
	}
	sub, err := fs.Sub(migrationsFS, "migrations/"+string(d.Dialect))
	if err != nil {
		return nil, fmt.Errorf("db: migrations for %s: %w", d.Dialect, err)
	}
	opts := []goose.ProviderOption{goose.WithDisableGlobalRegistry(true)}
	if d.Dialect == Postgres {
		// Several instances may start at once against one PostgreSQL database; an
		// advisory session lock makes them migrate one after another.
		// (SQLite: BEGIN IMMEDIATE already serializes writers.)
		// Probe every second for up to 5 minutes (goose's default probes every 5 s).
		locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(1, 300))
		if err != nil {
			return nil, fmt.Errorf("db: migration locker: %w", err)
		}
		opts = append(opts, goose.WithSessionLocker(locker))
	}
	p, err := goose.NewProvider(gd, d.DB, sub, opts...)
	if err != nil {
		return nil, fmt.Errorf("db: migration provider: %w", err)
	}
	return p, nil
}

// Migrate applies all pending embedded migrations. It is idempotent: with nothing
// pending it only verifies the version table. Applied migrations are logged.
func (d *DB) Migrate(ctx context.Context) error {
	p, err := d.provider()
	if err != nil {
		return err
	}
	results, err := p.Up(ctx)
	if err != nil {
		return fmt.Errorf("db: migrate: %w", err)
	}
	for _, r := range results {
		slog.InfoContext(ctx, "database migration applied",
			"dialect", string(d.Dialect),
			"version", r.Source.Version,
			"file", filepath.Base(r.Source.Path),
			"duration", r.Duration.Round(time.Millisecond).String())
	}
	return nil
}

// MigrationVersion returns the highest applied migration version (0 when none).
func (d *DB) MigrationVersion(ctx context.Context) (int64, error) {
	p, err := d.provider()
	if err != nil {
		return 0, err
	}
	v, err := p.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("db: migration version: %w", err)
	}
	return v, nil
}

// Backup writes a consistent copy of a SQLite database to destPath using
// VACUUM INTO (safe while the service is running). destPath must not exist; the
// copy is written to a temporary file in the same directory and moved into place
// only when complete, so a crash never leaves a half-written backup under the final
// name. The parent directory is created (0700) if needed; the file mode is 0600.
// For PostgreSQL it returns ErrBackupUnsupported.
func (d *DB) Backup(ctx context.Context, destPath string) (err error) {
	if d.Dialect != SQLite {
		return ErrBackupUnsupported
	}
	if destPath == "" {
		return errors.New("db: backup: empty destination path")
	}
	if _, err := os.Lstat(destPath); err == nil {
		return fmt.Errorf("db: backup: %s: %w", destPath, fs.ErrExist)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("db: backup: %w", err)
	}
	dir := filepath.Dir(destPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("db: backup: create directory: %w", err)
	}
	// VACUUM INTO accepts an existing *empty* file, so CreateTemp gives us a unique
	// name with mode 0600 for free.
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(destPath)+".tmp-*")
	if err != nil {
		return fmt.Errorf("db: backup: %w", err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer func() {
		// Best effort: the temp file is gone after a successful rename.
		_ = os.Remove(tmpPath)
	}()
	if _, err := d.ExecContext(ctx, "VACUUM INTO ?", tmpPath); err != nil {
		return fmt.Errorf("db: backup: vacuum into: %w", err)
	}
	if err := syncFile(tmpPath); err != nil {
		return fmt.Errorf("db: backup: %w", err)
	}
	// Link fails if destPath appeared meanwhile, never overwriting an existing file.
	if err := os.Link(tmpPath, destPath); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("db: backup: %s: %w", destPath, fs.ErrExist)
		}
		// File systems without hard links: fall back to rename.
		if err := os.Rename(tmpPath, destPath); err != nil {
			return fmt.Errorf("db: backup: move into place: %w", err)
		}
	}
	return nil
}

func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
