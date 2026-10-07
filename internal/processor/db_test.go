package processor

import (
	"context"
	"database/sql"
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
)

// The suite runs on SQLite (a fresh file per test). With TEST_POSTGRES_URL set it
// runs on PostgreSQL instead, every test in its own database cloned from a
// migrated template, e.g.
//
//	docker run -d --name zc-test-pg -e POSTGRES_PASSWORD=test -e POSTGRES_DB=zc -p 55432:5432 postgres:18-alpine
//	TEST_POSTGRES_URL='postgres://postgres:test@localhost:55432/zc?sslmode=disable' go test ./internal/processor/

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

// openTestDB opens a migrated, empty database for one test.
func openTestDB(t testing.TB) *db.DB {
	t.Helper()
	if base := os.Getenv("TEST_POSTGRES_URL"); base != "" {
		return openPostgres(t, base)
	}
	ctx := context.Background()
	d, err := db.Open(ctx, "sqlite://"+filepath.ToSlash(filepath.Join(t.TempDir(), "zc.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return d
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

func openPostgres(t testing.TB, base string) *db.DB {
	t.Helper()
	ctx := context.Background()
	pgOnce.Do(func() {
		if pgAdmin, pgErr = sql.Open("pgx", base); pgErr != nil {
			return
		}
		pgTemplate = fmt.Sprintf("zc_proc_tpl_%d_%d", os.Getpid(), time.Now().UnixNano()%1_000_000)
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
