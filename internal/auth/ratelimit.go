package auth

import (
	"cmp"
	"slices"
	"sync"
	"time"
)

// maxLimiterKeys bounds the memory of a LoginLimiter.
const maxLimiterKeys = 10000

// LoginLimiter limits login attempts per key (e.g. client IP + username) with
// a sliding window. It is safe for concurrent use.
//
// Usage: call Allow before verifying credentials and refuse the attempt when
// it returns false; after the check call Failure (wrong credentials) or
// Success. Allow counts the attempt atomically, so parallel requests cannot
// all pass the check before the first failure is recorded; Failure then only
// confirms it, Success forgets the key and Release un-counts only that one
// attempt. An admitted attempt that is never reported (e.g. the directory was
// down) keeps counting until it leaves the window. Allow is therefore not a
// read-only probe: use RetryAfter for that.
type LoginLimiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	maxKeys int
	entries map[string]*limiterEntry
}

type limiterEntry struct {
	// attempts are the counted attempts (failures and admitted attempts not
	// reported yet), ascending, at most max of them.
	attempts []time.Time
	// reserved is how many of attempts were admitted by Allow and not yet
	// reported by Failure.
	reserved int
}

// NewLoginLimiter allows at most maxAttempts attempts per key within window
// (successful logins reset the key). maxAttempts <= 0 disables limiting.
func NewLoginLimiter(maxAttempts int, window time.Duration) *LoginLimiter {
	return &LoginLimiter{
		max:     maxAttempts,
		window:  window,
		maxKeys: maxLimiterKeys,
		entries: make(map[string]*limiterEntry),
	}
}

// Allow reports whether another attempt for key is allowed at now and, if so,
// counts it (see LoginLimiter).
func (l *LoginLimiter) Allow(key string, now time.Time) bool {
	if l.max <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entry(key, now)
	if len(e.attempts) >= l.max {
		return false
	}
	e.attempts = append(e.attempts, now)
	e.reserved++
	return true
}

// RetryAfter returns how long key has to wait before the next attempt is
// allowed (0 = allowed now). Useful for a Retry-After header. It does not
// count an attempt.
func (l *LoginLimiter) RetryAfter(key string, now time.Time) time.Duration {
	if l.max <= 0 {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[key]
	if e == nil {
		return 0
	}
	l.prune(e, now)
	if len(e.attempts) == 0 {
		delete(l.entries, key)
		return 0
	}
	if len(e.attempts) < l.max {
		return 0
	}
	// Blocked until the oldest attempt that still counts leaves the window.
	wait := e.attempts[len(e.attempts)-l.max].Add(l.window).Sub(now)
	return max(wait, time.Second)
}

// Failure records a failed attempt for key at now. An attempt admitted by
// Allow is already counted, so this only confirms it.
func (l *LoginLimiter) Failure(key string, now time.Time) {
	if l.max <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entry(key, now)
	if e.reserved > 0 {
		e.reserved--
		return
	}
	e.attempts = append(e.attempts, now)
	if extra := len(e.attempts) - l.max; extra > 0 {
		e.attempts = slices.Delete(e.attempts, 0, extra)
	}
}

// Release un-counts one attempt admitted by Allow and not reported yet, for
// attempts that must not consume the budget (a successful login on a limiter
// keyed by IP only: Success there would let one valid account reset the
// protection against spraying many usernames, counting it would lock out a
// whole school behind one NAT address). The latest attempt not after now is
// dropped — normally the one Allow just admitted. Without an unreported
// attempt it does nothing.
func (l *LoginLimiter) Release(key string, now time.Time) {
	if l.max <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[key]
	if e == nil {
		return
	}
	l.prune(e, now)
	if e.reserved > 0 {
		i := len(e.attempts) - 1
		for i > 0 && e.attempts[i].After(now) {
			i--
		}
		e.attempts = slices.Delete(e.attempts, i, i+1)
		e.reserved--
	}
	if len(e.attempts) == 0 {
		delete(l.entries, key)
	}
}

// Success forgets the attempts of key.
func (l *LoginLimiter) Success(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// Len returns the number of tracked keys (for tests and metrics).
func (l *LoginLimiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// entry returns the pruned entry for key, creating it (and making room for
// it) when missing. Called with l.mu held.
func (l *LoginLimiter) entry(key string, now time.Time) *limiterEntry {
	e := l.entries[key]
	if e == nil {
		if len(l.entries) >= l.maxKeys {
			l.evict(now)
		}
		e = &limiterEntry{}
		l.entries[key] = e
	}
	l.prune(e, now)
	return e
}

// prune drops attempts that are older than the window.
func (l *LoginLimiter) prune(e *limiterEntry, now time.Time) {
	cutoff := now.Add(-l.window)
	i := 0
	for i < len(e.attempts) && !e.attempts[i].After(cutoff) {
		i++
	}
	if i > 0 {
		e.attempts = slices.Delete(e.attempts, 0, i)
	}
	e.reserved = min(e.reserved, len(e.attempts))
}

// evict makes room for a new key: first drop expired entries, then (if the
// map is still full, e.g. during a flood of distinct keys) 10% of the
// entries with the fewest attempts, oldest first. Keys close to or at the
// limit are evicted last, so a flood of fresh keys cannot reset them.
// Called with l.mu held.
func (l *LoginLimiter) evict(now time.Time) {
	for k, e := range l.entries {
		l.prune(e, now)
		if len(e.attempts) == 0 {
			delete(l.entries, k)
		}
	}
	if len(l.entries) < l.maxKeys {
		return
	}
	type kv struct {
		key  string
		n    int
		last time.Time
	}
	all := make([]kv, 0, len(l.entries))
	for k, e := range l.entries {
		all = append(all, kv{k, len(e.attempts), e.attempts[len(e.attempts)-1]})
	}
	slices.SortFunc(all, func(a, b kv) int {
		return cmp.Or(cmp.Compare(a.n, b.n), a.last.Compare(b.last))
	})
	drop := max(len(all)/10, len(all)-l.maxKeys+1)
	for _, x := range all[:drop] {
		delete(l.entries, x.key)
	}
}
