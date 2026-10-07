package httpapi

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"
)

// TestMain silences slog.Default: db.Migrate and other helpers log through it
// ("database migration applied" at INFO for every test database). Tests that
// assert on log lines pass their own logger in Deps.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}

// closeServerAtCleanup stops the background work of srv when the test ends.
// Call it after the database cleanup was registered: cleanups run in reverse
// order, so rebuilds still running stop before the database closes.
func closeServerAtCleanup(t *testing.T, srv *Server) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Close(ctx); err != nil {
			t.Errorf("server close: %v", err)
		}
	})
}
