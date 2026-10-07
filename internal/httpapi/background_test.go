package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"zoom-checker/internal/model"
)

// bgProc blocks every rebuild until its context ends, like a long rebuild
// that is interrupted by the shutdown.
type bgProc struct {
	started  chan int64
	finished atomic.Int32
}

func (p *bgProc) Notify() {}
func (p *bgProc) RebuildLesson(ctx context.Context, id int64) error {
	p.started <- id
	<-ctx.Done()
	time.Sleep(30 * time.Millisecond) // rolling back takes a moment
	p.finished.Add(1)
	return ctx.Err()
}
func (p *bgProc) RelinkLesson(context.Context, int64) error        { return nil }
func (p *bgProc) LinkSession(context.Context, int64, *int64) error { return nil }

// The rebuild after a settings change runs in the background; Close cancels
// it and returns only after it stopped, so the caller may close the database.
func TestServerCloseWaitsForSettingsRebuild(t *testing.T) {
	t.Parallel()
	proc := &bgProc{started: make(chan int64, 4)}
	e := newAZEnv(t, func(s *azSetup) { s.deps.Processor = proc })
	_, admin := e.adminCookie()
	g := &model.Group{Name: "10А"}
	if err := e.st.CreateGroup(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Add(-time.Hour)
	l := &model.Lesson{GroupID: g.ID, Title: "Урок", ZoomMeetingID: "85746065432", PlannedStartAt: start, PlannedEndAt: start.Add(time.Hour)}
	if err := e.st.CreateLesson(context.Background(), l); err != nil {
		t.Fatal(err)
	}

	azWantStatus(t, e.do("PUT", "/api/settings", map[string]any{"present_min_percent": 50}, admin), http.StatusOK)
	select {
	case id := <-proc.started:
		if id != l.ID {
			t.Fatalf("rebuilt lesson %d, want %d", id, l.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("background rebuild did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.srv.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if n := proc.finished.Load(); n != 1 {
		t.Fatalf("Close returned before the rebuild stopped (finished %d)", n)
	}

	// After Close no new background work starts; the request itself still works.
	azWantStatus(t, e.do("PUT", "/api/settings", map[string]any{"present_min_percent": 60}, admin), http.StatusOK)
	select {
	case <-proc.started:
		t.Fatal("background rebuild started after Close")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestServerBackgroundTracking(t *testing.T) {
	t.Parallel()

	t.Run("detach outlives the request, not Close", func(t *testing.T) {
		t.Parallel()
		s := &Server{bg: newBackground()}
		parent, cancelParent := context.WithCancel(context.WithValue(context.Background(), bgTestKey("k"), "v"))
		ctx, release := s.detach(parent, time.Minute)
		cancelParent()
		if ctx.Err() != nil || ctx.Value(bgTestKey("k")) != "v" {
			t.Fatalf("detached context: err %v value %v", ctx.Err(), ctx.Value(bgTestKey("k")))
		}
		closed := make(chan error, 1)
		go func() { closed <- s.Close(context.Background()) }()
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("Close did not cancel the detached context")
		}
		select {
		case err := <-closed:
			t.Fatalf("Close returned %v before the work was released", err)
		case <-time.After(50 * time.Millisecond):
		}
		release()
		if err := <-closed; err != nil {
			t.Fatal(err)
		}
		// After Close: the context is cancelled from the start and nothing is tracked.
		ctx2, release2 := s.detach(context.Background(), time.Minute)
		defer release2()
		if ctx2.Err() == nil {
			t.Fatal("detach after Close must return a cancelled context")
		}
		if s.goBackground(time.Minute, func(context.Context) { t.Error("ran after Close") }) {
			t.Fatal("goBackground after Close reported true")
		}
	})

	t.Run("Close gives up at its deadline", func(t *testing.T) {
		t.Parallel()
		s := &Server{bg: newBackground()}
		stuck := make(chan struct{})
		if !s.goBackground(time.Minute, func(context.Context) { <-stuck }) { // ignores cancellation
			t.Fatal("goBackground refused")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if err := s.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Close = %v, want deadline exceeded", err)
		}
		close(stuck)
		if err := s.Close(context.Background()); err != nil {
			t.Fatalf("second Close = %v", err)
		}
	})

	t.Run("background work ends at its timeout", func(t *testing.T) {
		t.Parallel()
		s := &Server{bg: newBackground()}
		done := make(chan error, 1)
		s.goBackground(20*time.Millisecond, func(ctx context.Context) { <-ctx.Done(); done <- ctx.Err() })
		select {
		case err := <-done:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("ctx err = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout not applied")
		}
	})
}

type bgTestKey string
