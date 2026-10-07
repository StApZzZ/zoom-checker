package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"zoom-checker/internal/model"
)

// TestGroupsPostgres runs the group flow on PostgreSQL, which is stricter
// than SQLite (no NUL in text, aborted transactions after a failed
// statement). Skipped unless TEST_POSTGRES_URL is set, e.g.
//
//	TEST_POSTGRES_URL='postgres://postgres:test@localhost:55432/zc?sslmode=disable' go test ./internal/httpapi/ -run Postgres
func TestGroupsPostgres(t *testing.T) {
	base := os.Getenv("TEST_POSTGRES_URL")
	if base == "" {
		t.Skip("TEST_POSTGRES_URL not set")
	}
	t.Parallel()
	e := newGTEnvDB(t, gtFreshPostgres(t, base))

	// Control characters are cleaned before they reach PostgreSQL.
	r := e.do(http.MethodPost, "/api/groups", map[string]any{"name": "PG\x00-1", "description": "a\x00b"})
	e.want(r, http.StatusCreated)
	var g GroupSummaryDTO
	r.decode(t, &g)
	ivan := e.student(g.ID, "Иванов\x00 Иван", "7")
	petr := e.student(g.ID, "Петров Пётр", "")
	e.want(e.do(http.MethodPost, "/api/students/"+grpID(ivan.ID)+"/aliases", map[string]any{"alias": "Ivanov Ivan"}), http.StatusCreated)
	e.want(e.do(http.MethodPost, "/api/students/"+grpID(petr.ID)+"/aliases", map[string]any{"alias": "Петя"}), http.StatusCreated)
	e.want(e.do(http.MethodPost, "/api/groups/"+grpID(g.ID)+"/students", map[string]any{"full_name": "иванов иван"}), http.StatusConflict)
	e.want(e.do(http.MethodPost, "/api/groups/"+grpID(g.ID)+"/students", map[string]any{"full_name": "Сидоров Алексей", "external_id": "7"}),
		http.StatusUnprocessableEntity)

	// Replace: Иванов takes his alias as the name, Петров goes to the archive.
	p := gtPreview(t, e.do(http.MethodPost, gtPreviewPath(g.ID, "mode=replace"), map[string]any{"text": "ID;ФИО\n7;Ivanov Ivan\n;Сидоров\x00 Алексей\n"}))
	r = e.do(http.MethodPost, gtCommitPath(g.ID), map[string]any{"mode": "replace", "rows": gtCommitRows(p)})
	e.want(r, http.StatusOK)
	var res ImportResultDTO
	r.decode(t, &res)
	if res != (ImportResultDTO{Created: 1, Updated: 1, Archived: 1}) {
		t.Errorf("replace %+v", res)
	}
	// A rejected commit leaves everything as it was.
	r = e.do(http.MethodPost, gtCommitPath(g.ID), map[string]any{"mode": "add", "rows": []map[string]string{
		{"full_name": "Кузнецов Олег"}, {"full_name": "кузнецов олег"},
	}})
	e.want(r, http.StatusUnprocessableEntity)
	// Bring Петров back with his alias.
	r = e.do(http.MethodPost, gtCommitPath(g.ID), map[string]any{"mode": "add", "rows": []map[string]string{{"full_name": "Петров Пётр"}}})
	e.want(r, http.StatusOK)
	r.decode(t, &res)
	if res != (ImportResultDTO{Restored: 1}) {
		t.Errorf("restore %+v", res)
	}
	d := e.detail(g.ID)
	got := map[string]string{}
	for _, s := range d.Students {
		var aliases []string
		for _, a := range s.Aliases {
			aliases = append(aliases, a.Alias)
		}
		got[s.FullName] = string(s.Status) + "|" + s.ExternalID + "|" + strings.Join(aliases, ",")
	}
	want := map[string]string{"Ivanov Ivan": "active|7|", "Петров Пётр": "active||Петя", "Сидоров Алексей": "active||"}
	if fmt.Sprint(got) != fmt.Sprint(want) || g.Name != "PG-1" || g.Description != "ab" {
		t.Errorf("students %v, group %q / %q", got, g.Name, g.Description)
	}

	// Report over an open period and its export.
	l := e.lesson(g.ID, "Занятие", gtNow.Add(-24*time.Hour), model.LessonFinished)
	if _, err := e.st.UpsertAttendanceComputed(e.ctx, l.ID, []model.Attendance{
		{StudentID: ivan.ID, CurrentStatus: model.CurrentLeft, AutoStatus: model.FinalPresent, TotalDurationSeconds: 3600},
	}); err != nil {
		t.Fatal(err)
	}
	rep, _ := gtReport(t, e, g.ID, "")
	if len(rep.Lessons) != 1 || rep.From != nil || rep.To != nil {
		t.Errorf("report %+v", rep)
	}
	e.want(e.do(http.MethodGet, "/api/groups/"+grpID(g.ID)+"/report/export?format=xlsx", nil), http.StatusOK)
}

// gtFreshPostgres creates an empty database next to the one in base and
// returns its URL; it is dropped when the test ends.
func gtFreshPostgres(t *testing.T, base string) string {
	t.Helper()
	ctx := context.Background()
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	name := fmt.Sprintf("zc_httpapi_%d_%d", os.Getpid(), time.Now().UnixNano()%1_000_000_000)
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		_ = admin.Close()
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = admin.Close()
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}
