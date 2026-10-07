package auth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingKey is an argon2 stand-in that records how many computations run at
// once and blocks until release is closed.
type countingKey struct {
	running, peak, calls atomic.Int32
	release              chan struct{}
}

func (c *countingKey) key(_, _ []byte, _, _ uint32, _ uint8, keyLen uint32) []byte {
	c.calls.Add(1)
	n := c.running.Add(1)
	for {
		p := c.peak.Load()
		if n <= p || c.peak.CompareAndSwap(p, n) {
			break
		}
	}
	<-c.release
	c.running.Add(-1)
	return make([]byte, keyLen)
}

func TestArgonPoolBoundsConcurrency(t *testing.T) {
	t.Parallel()
	const slots, callers = 3, 12
	ck := &countingKey{release: make(chan struct{})}
	p := newArgonPool(slots, time.Minute)
	p.key = ck.key
	plain := newArgonPool(1, 0)
	plain.key = ck.keyNoBlock
	hash, err := plain.hash(context.Background(), false, "secret-pw")
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Go(func() {
			if _, err := p.verify(context.Background(), true, hash, "secret-pw"); err != nil {
				errs <- err
			}
		})
	}
	// Wait until the pool is saturated, then check nobody got past it.
	deadline := time.Now().Add(5 * time.Second)
	for ck.running.Load() < slots && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	if got := ck.running.Load(); got != slots {
		t.Fatalf("running = %d, want %d (the pool must cap concurrent hashes)", got, slots)
	}
	close(ck.release)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("verify: %v", err)
	}
	if got := ck.peak.Load(); got > slots {
		t.Errorf("peak concurrency = %d, want <= %d", got, slots)
	}
	if got := ck.calls.Load(); got != callers+1 {
		t.Errorf("calls = %d, want %d", got, callers+1)
	}
}

func (c *countingKey) keyNoBlock(_, _ []byte, _, _ uint32, _ uint8, keyLen uint32) []byte {
	c.calls.Add(1)
	return make([]byte, keyLen)
}

func TestArgonPoolBusy(t *testing.T) {
	t.Parallel()
	hash, err := HashPassword("secret-pw")
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name    string
		ctx     context.Context
		bounded bool
		wantCtx error
	}{
		{"full pool, bounded caller times out", context.Background(), true, nil},
		{"cancelled context", cancelled, true, context.Canceled},
		{"cancelled context, unbounded caller", cancelled, false, context.Canceled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ck := &countingKey{release: make(chan struct{})}
			p := newArgonPool(1, 30*time.Millisecond)
			p.key = ck.keyNoBlock
			p.slots <- struct{}{} // the only slot is taken
			start := time.Now()
			ok, err := p.verify(tc.ctx, tc.bounded, hash, "secret-pw")
			if ok || !errors.Is(err, ErrBusy) {
				t.Fatalf("verify = %v, %v; want ErrBusy", ok, err)
			}
			if tc.wantCtx != nil && !errors.Is(err, tc.wantCtx) {
				t.Errorf("err = %v, want it to wrap %v", err, tc.wantCtx)
			}
			if _, err := p.hash(tc.ctx, tc.bounded, "secret-pw"); !errors.Is(err, ErrBusy) {
				t.Errorf("hash err = %v, want ErrBusy", err)
			}
			if n := ck.calls.Load(); n != 0 {
				t.Errorf("argon2 ran %d times although no slot was free", n)
			}
			if d := time.Since(start); d > 2*time.Second {
				t.Errorf("gave up after %v, want about the pool wait", d)
			}
		})
	}
}

// An unbounded caller (CLI, bootstrap) waits for a slot instead of failing.
func TestArgonPoolUnboundedWaits(t *testing.T) {
	t.Parallel()
	ck := &countingKey{}
	p := newArgonPool(1, time.Millisecond)
	p.key = ck.keyNoBlock
	p.slots <- struct{}{}
	go func() {
		time.Sleep(50 * time.Millisecond)
		<-p.slots
	}()
	if _, err := p.hash(context.Background(), false, "secret-pw"); err != nil {
		t.Fatalf("hash = %v, want it to wait for the slot", err)
	}
	if len(p.slots) != 0 {
		t.Errorf("slot not released after the hash")
	}
}

func TestPasswordContextVariants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h, err := HashPasswordContext(ctx, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := VerifyPasswordContext(ctx, h, "correct horse"); !ok || err != nil {
		t.Errorf("VerifyPasswordContext(right) = %v, %v", ok, err)
	}
	if ok, err := VerifyPassword(h, "wrong horse"); ok || err != nil {
		t.Errorf("VerifyPassword(wrong) = %v, %v", ok, err)
	}
	if n := argonSlotCount(); n < 2 || n > 4 {
		t.Errorf("argonSlotCount = %d, want 2..4", n)
	}
}
