package auth

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func TestLoginLimiterBlocksAfterMaxFailures(t *testing.T) {
	t.Parallel()
	l := NewLoginLimiter(3, time.Minute)
	key := "10.0.0.1|ivanov"
	for i := range 3 {
		if !l.Allow(key, t0.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("attempt %d must be allowed", i+1)
		}
		l.Failure(key, t0.Add(time.Duration(i)*time.Second))
	}
	if l.Allow(key, t0.Add(10*time.Second)) {
		t.Fatal("4th attempt within the window must be blocked")
	}
	if ra := l.RetryAfter(key, t0.Add(10*time.Second)); ra != 50*time.Second {
		t.Fatalf("RetryAfter = %v, want 50s", ra)
	}
	if !l.Allow("10.0.0.1|petrov", t0.Add(10*time.Second)) {
		t.Fatal("other keys are independent")
	}
	if !l.Allow(key, t0.Add(61*time.Second)) {
		t.Fatal("first failure left the window: allowed again")
	}
}

func TestLoginLimiterSlidingWindow(t *testing.T) {
	t.Parallel()
	l := NewLoginLimiter(3, time.Minute)
	k := "k"
	l.Failure(k, t0)
	l.Failure(k, t0.Add(30*time.Second))
	l.Failure(k, t0.Add(50*time.Second))
	if l.Allow(k, t0.Add(55*time.Second)) {
		t.Fatal("blocked at +55s")
	}
	if !l.Allow(k, t0.Add(61*time.Second)) {
		t.Fatal("allowed at +61s (oldest failure expired)")
	}
	l.Failure(k, t0.Add(62*time.Second))
	if l.Allow(k, t0.Add(63*time.Second)) {
		t.Fatal("blocked again after a new failure")
	}
}

func TestLoginLimiterSuccessResets(t *testing.T) {
	t.Parallel()
	l := NewLoginLimiter(2, time.Minute)
	l.Failure("k", t0)
	l.Failure("k", t0)
	if l.Allow("k", t0) {
		t.Fatal("blocked")
	}
	l.Success("k")
	if l.Len() != 0 || !l.Allow("k", t0) {
		t.Fatal("Success must forget the key")
	}
}

func TestLoginLimiterDisabled(t *testing.T) {
	t.Parallel()
	l := NewLoginLimiter(0, time.Minute)
	for range 100 {
		l.Failure("k", t0)
	}
	if !l.Allow("k", t0) || l.Len() != 0 {
		t.Fatal("maxAttempts 0 disables limiting")
	}
}

func TestLoginLimiterMemoryBound(t *testing.T) {
	t.Parallel()
	l := NewLoginLimiter(5, time.Minute)
	l.maxKeys = 100
	for i := range 1000 {
		l.Failure(fmt.Sprintf("flood-%d", i), t0.Add(time.Duration(i)*time.Millisecond))
		if l.Len() > 100 {
			t.Fatalf("limiter grew to %d keys", l.Len())
		}
	}
	// The newest keys survive; the oldest were evicted.
	if l.Allow("flood-999", t0) != true || l.entries["flood-999"] == nil {
		t.Fatal("newest key must be kept")
	}
	if l.entries["flood-0"] != nil {
		t.Fatal("oldest key must be evicted")
	}
	// Expired entries are dropped first.
	l.Failure("late", t0.Add(2*time.Minute))
	if l.Len() != 1 {
		t.Fatalf("expired keys must be evicted, %d left", l.Len())
	}
	if l.Allow("late", t0.Add(2*time.Minute)) != true {
		t.Fatal("one failure is allowed")
	}
}

func TestLoginLimiterConcurrent(t *testing.T) {
	t.Parallel()
	l := NewLoginLimiter(1000, time.Minute)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 200 {
				k := fmt.Sprintf("k%d", i%10)
				l.Allow(k, t0)
				l.Failure(k, t0)
				if i%50 == g {
					l.Success(k)
				}
			}
		})
	}
	wg.Wait()
}

// TestLoginLimiterParallelBurst: Allow counts the attempt atomically, so a
// burst of parallel requests cannot all pass before the first Failure.
func TestLoginLimiterParallelBurst(t *testing.T) {
	t.Parallel()
	l := NewLoginLimiter(5, time.Minute)
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		allowed int
	)
	start := make(chan struct{})
	for range 200 {
		wg.Go(func() {
			<-start
			if l.Allow("ip|victim", t0) {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		})
	}
	close(start)
	wg.Wait()
	if allowed != 5 {
		t.Fatalf("%d parallel attempts allowed, want 5", allowed)
	}
}

func TestLoginLimiterNoDoubleCount(t *testing.T) {
	t.Parallel()
	l := NewLoginLimiter(3, time.Minute)
	k := "k"
	// Two admitted attempts in flight, then both reported as failures.
	if !l.Allow(k, t0) || !l.Allow(k, t0) {
		t.Fatal("allowed")
	}
	l.Failure(k, t0.Add(time.Second))
	l.Failure(k, t0.Add(time.Second))
	if !l.Allow(k, t0.Add(2*time.Second)) {
		t.Fatal("2 failures of 3: the third attempt is allowed")
	}
	l.Failure(k, t0.Add(2*time.Second))
	if l.Allow(k, t0.Add(3*time.Second)) {
		t.Fatal("3 failures: blocked")
	}
	// An admitted attempt that is never reported still counts for the window.
	l2 := NewLoginLimiter(2, time.Minute)
	l2.Allow(k, t0)
	l2.Allow(k, t0)
	if l2.Allow(k, t0) || l2.RetryAfter(k, t0) != time.Minute {
		t.Fatal("unreported attempts count")
	}
	if !l2.Allow(k, t0.Add(61*time.Second)) {
		t.Fatal("unreported attempts expire with the window")
	}
	// A success forgets everything, including attempts still in flight.
	l2.Success(k)
	if l2.Len() != 0 || !l2.Allow(k, t0.Add(62*time.Second)) {
		t.Fatal("Success resets the key")
	}
}

// TestLoginLimiterFloodKeepsBlockedKeys: a flood of fresh keys must not evict
// a key that is at its limit (that would reset the brute-force counter).
func TestLoginLimiterFloodKeepsBlockedKeys(t *testing.T) {
	t.Parallel()
	l := NewLoginLimiter(5, time.Minute)
	l.maxKeys = 100
	for i := range 5 {
		l.Allow("ip|victim", t0.Add(time.Duration(i)*time.Millisecond))
		l.Failure("ip|victim", t0.Add(time.Duration(i)*time.Millisecond))
	}
	for i := range 5000 {
		l.Allow(fmt.Sprintf("ip|flood-%d", i), t0.Add(time.Second+time.Duration(i)*time.Millisecond))
	}
	if l.Len() > 100 {
		t.Fatalf("limiter grew to %d keys", l.Len())
	}
	if l.Allow("ip|victim", t0.Add(10*time.Second)) {
		t.Fatal("the blocked key was evicted by the flood")
	}
}

// TestLoginLimiterRelease: Release gives back exactly the one admitted attempt
// (a successful login on the per-IP limiter) and keeps confirmed failures.
func TestLoginLimiterRelease(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		run  func(l *LoginLimiter) // limit 3 per minute, key "ip"
		// wantAllowed is how many further attempts Allow admits at t0+10s.
		wantAllowed int
		wantKeys    int
	}{
		{"successes never consume the budget", func(l *LoginLimiter) {
			for i := range 10 {
				at := t0.Add(time.Duration(i) * time.Second)
				if !l.Allow("ip", at) {
					t.Fatalf("success %d blocked", i+1)
				}
				l.Release("ip", at)
			}
		}, 3, 0},
		{"failures stay counted", func(l *LoginLimiter) {
			l.Allow("ip", t0)
			l.Failure("ip", t0)
			l.Allow("ip", t0.Add(time.Second))
			l.Release("ip", t0.Add(time.Second))
		}, 2, 1},
		{"release without an admitted attempt does nothing", func(l *LoginLimiter) {
			l.Failure("ip", t0)
			l.Failure("ip", t0)
			l.Release("ip", t0.Add(time.Second))
			l.Release("unknown", t0.Add(time.Second))
		}, 1, 1},
		{"one release per admitted attempt", func(l *LoginLimiter) {
			l.Allow("ip", t0)
			l.Allow("ip", t0)
			l.Release("ip", t0)
			l.Release("ip", t0)
			l.Release("ip", t0) // nothing left to give back
		}, 3, 0},
		{"the latest attempt is given back", func(l *LoginLimiter) {
			l.Allow("ip", t0)                    // in flight, ends with the window at t0+60s
			l.Allow("ip", t0.Add(5*time.Second)) // succeeds
			l.Release("ip", t0.Add(5*time.Second))
		}, 2, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			l := NewLoginLimiter(3, time.Minute)
			c.run(l)
			if l.Len() != c.wantKeys {
				t.Errorf("keys = %d, want %d", l.Len(), c.wantKeys)
			}
			allowed := 0
			for range 5 {
				if l.Allow("ip", t0.Add(10*time.Second)) {
					allowed++
				}
			}
			if allowed != c.wantAllowed {
				t.Errorf("allowed %d more attempts, want %d", allowed, c.wantAllowed)
			}
		})
	}

	// The remaining attempt keeps its own expiry: the one admitted at t0 (not
	// the released one at t0+5s) leaves the window at t0+60s.
	l := NewLoginLimiter(1, time.Minute)
	l.Allow("ip", t0)
	if ra := l.RetryAfter("ip", t0.Add(30*time.Second)); ra != 30*time.Second {
		t.Fatalf("RetryAfter = %v, want 30s", ra)
	}
	l.Release("ip", t0.Add(30*time.Second))
	if !l.Allow("ip", t0.Add(30*time.Second)) {
		t.Fatal("released attempt still blocks")
	}

	// Disabled limiter: Release is a no-op.
	off := NewLoginLimiter(0, time.Minute)
	off.Release("ip", t0)
	if off.Len() != 0 {
		t.Fatal("disabled limiter tracks keys")
	}
}
