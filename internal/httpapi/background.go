package httpapi

import (
	"context"
	"sync"
	"time"
)

// background tracks work that outlives the request which started it: lesson
// rebuilds after a saved change (they must finish even when the client goes
// away) and goroutines such as the rebuild after a settings change. Close
// cancels that work and waits for it, so the server's owner can close the
// database only after nothing uses it any more.
type background struct {
	ctx    context.Context // cancelled by close
	cancel context.CancelFunc

	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

func newBackground() *background {
	b := &background{}
	b.ctx, b.cancel = context.WithCancel(context.Background())
	return b
}

// add registers one unit of work; ok is false once close has started (the
// work must then not run).
func (b *background) add() (done func(), ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, false
	}
	b.wg.Add(1)
	return b.wg.Done, true
}

// close stops new work, cancels running work and waits for it until ctx ends.
func (b *background) close(ctx context.Context) error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.cancel()
	finished := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// goBackground runs fn in a tracked goroutine whose context ends after timeout
// or when the server closes. It reports false (fn not run) after Close.
func (s *Server) goBackground(timeout time.Duration, fn func(ctx context.Context)) bool {
	done, ok := s.bg.add()
	if !ok {
		return false
	}
	go func() {
		defer done()
		ctx, cancel := context.WithTimeout(s.bg.ctx, timeout)
		defer cancel()
		fn(ctx)
	}()
	return true
}

// detach returns a context for work a request starts that must survive the
// client going away (a half-done rebuild would only be retried by the next Zoom
// event) but not the server: it keeps parent's values, drops its cancellation,
// ends after timeout or when the server closes, and is tracked by Close.
// release must be called when the work is done. After Close the context is
// already cancelled.
func (s *Server) detach(parent context.Context, timeout time.Duration) (ctx context.Context, release func()) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
	done, ok := s.bg.add()
	if !ok {
		cancel()
		return ctx, func() {}
	}
	stop := context.AfterFunc(s.bg.ctx, cancel)
	return ctx, func() {
		stop()
		cancel()
		done()
	}
}

// Close cancels background work started by handlers (rebuilds after roster,
// alias or settings changes) and waits for it to stop, or until ctx ends
// (then it returns ctx's error). Call it after the HTTP servers have shut down
// and before closing the database. Requests served after Close skip their
// background work.
func (s *Server) Close(ctx context.Context) error {
	return s.bg.close(ctx)
}
