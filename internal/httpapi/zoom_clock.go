package httpapi

import (
	"sync"
	"time"
)

// webhookClock compares Zoom's request timestamps with the server clock. A
// server clock that drifted far enough makes every webhook fail the timestamp
// check (zoom.ErrStale): Zoom does not retry a 4xx, so attendance silently
// stops being recorded. GET /api/zoom/status reports what this measured.
//
// The skew of one request is its x-zm-request-timestamp minus our clock at
// arrival. Network delay and Zoom's retries (which keep the original
// timestamp) only make it smaller, so the largest skew of the last hour or two
// is the best estimate of the clock difference.
type webhookClock struct {
	mu           sync.Mutex
	measured     bool
	windowStart  time.Time
	cur, prev    time.Duration // largest skew of the current and the previous window
	hasPrev      bool
	lastRejected time.Time
}

const webhookClockWindow = time.Hour

// observe records the skew of a correctly signed request (accepted or not).
func (c *webhookClock) observe(now, requestTime time.Time) {
	d := requestTime.Sub(now)
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case !c.measured:
		c.measured, c.windowStart, c.cur = true, now, d
	case now.Sub(c.windowStart) >= webhookClockWindow || now.Before(c.windowStart):
		c.prev, c.hasPrev = c.cur, true
		c.windowStart, c.cur = now, d
	case d > c.cur:
		c.cur = d
	}
}

// rejected records a correctly signed request refused for its timestamp.
func (c *webhookClock) rejected(now time.Time) {
	c.mu.Lock()
	c.lastRejected = now
	c.mu.Unlock()
}

// state returns the estimated skew (ok=false before any measurement) and the
// time of the last refusal (zero when none).
func (c *webhookClock) state() (skew time.Duration, ok bool, lastRejected time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	skew = c.cur
	if c.hasPrev && c.prev > skew {
		skew = c.prev
	}
	return skew, c.measured, c.lastRejected
}
