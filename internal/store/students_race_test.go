package store

import (
	"context"
	"testing"
	"time"

	"zoom-checker/internal/db"
	"zoom-checker/internal/model"
)

// waitForAdvisoryWaiter blocks until some other session of the test database is
// waiting for an advisory lock (PostgreSQL), or sleeps briefly on SQLite, where a
// concurrent writer simply waits in BEGIN IMMEDIATE.
func waitForAdvisoryWaiter(t *testing.T, e *env) {
	t.Helper()
	if e.s.Dialect() != db.Postgres {
		time.Sleep(150 * time.Millisecond)
		return
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		err := e.s.DB().QueryRowContext(e.ctx, `SELECT COUNT(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND wait_event = 'advisory'`).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no session ever waited for the group name lock")
}

// TestNameRuleNoStaleReads: a namespace mutation must look at the student as it is
// after the per-group lock, not as it was before. Otherwise (PostgreSQL, READ
// COMMITTED) a concurrent restore or alias addition on the same student slips
// through the cross-table check.
func TestNameRuleNoStaleReads(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		t.Run("alias added while the owner is being restored", func(t *testing.T) {
			g := e.group("Stale A")
			holder := e.student(g.ID, "Contested")
			x := e.student(g.ID, "X")
			x.Status = model.StatusArchived
			e.must(e.s.UpdateStudent(e.ctx, x))

			ready, release := make(chan struct{}), make(chan struct{})
			txErr := make(chan error, 1)
			go func() {
				txErr <- e.s.InTx(e.ctx, func(tx *Store) error {
					restored := *x
					restored.Status = model.StatusActive
					if err := tx.UpdateStudent(e.ctx, &restored); err != nil {
						close(ready)
						return err
					}
					close(ready)
					<-release
					return nil
				})
			}()
			<-ready
			aliasErr := make(chan error, 1)
			go func() {
				aliasErr <- e.s.AddAlias(context.Background(), &model.Alias{StudentID: x.ID, Alias: "contested"})
			}()
			waitForAdvisoryWaiter(t, e)
			close(release)
			e.must(<-txErr)
			nameTaken(t, <-aliasErr, holder.ID, false)
		})

		t.Run("restore while an alias is being added", func(t *testing.T) {
			g := e.group("Stale B")
			holder := e.student(g.ID, "Bob")
			x := e.student(g.ID, "X")
			x.Status = model.StatusArchived
			e.must(e.s.UpdateStudent(e.ctx, x))

			ready, release := make(chan struct{}), make(chan struct{})
			txErr := make(chan error, 1)
			go func() {
				txErr <- e.s.InTx(e.ctx, func(tx *Store) error {
					// X is archived, so the alias may duplicate an active name for now.
					if err := tx.AddAlias(e.ctx, &model.Alias{StudentID: x.ID, Alias: "BOB"}); err != nil {
						close(ready)
						return err
					}
					close(ready)
					<-release
					return nil
				})
			}()
			<-ready
			restoreErr := make(chan error, 1)
			go func() {
				restored := *x
				restored.Status = model.StatusActive
				restoreErr <- e.s.UpdateStudent(context.Background(), &restored)
			}()
			waitForAdvisoryWaiter(t, e)
			close(release)
			e.must(<-txErr)
			nameTaken(t, <-restoreErr, holder.ID, false)

			names, err := e.s.ListGroupNames(e.ctx, g.ID)
			e.must(err)
			for _, n := range names {
				if n.Normalized == "bob" && n.StudentID != holder.ID {
					t.Fatalf("namespace violated: %+v", names)
				}
			}
		})
	})
}
