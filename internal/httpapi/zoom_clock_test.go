package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// A server clock that is off: webhooks Zoom signed "in the future" are still
// accepted (the clock is merely behind), a refusal for the time is named in
// the log, and GET /api/zoom/status tells the admin what is wrong.
func TestWebhookClockSkew(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 10, 6, 15, 0, 0, 0, time.UTC)
	clock := &slClock{t: t0}
	e := newAZEnv(t, func(s *azSetup) {
		s.cfg.ZoomWebhookSecret = azWebhookSecret
		s.deps.Now = clock.now
	})
	admin, _ := e.adminCookie()
	ac := e.sessionFor(admin, "local", "")
	status := func() zoomStatusDTO {
		t.Helper()
		rec := e.do("GET", "/api/zoom/status", nil, ac)
		azWantStatus(t, rec, http.StatusOK)
		return azDecode[zoomStatusDTO](t, rec)
	}
	if st := status(); st.ClockSkewSeconds != nil || st.LastRejectedAt != nil {
		t.Fatalf("before any webhook: %+v", st)
	}

	// Our clock is 6 minutes behind Zoom's: accepted.
	azWantStatus(t, e.serve(azSignedWebhook(t, azWebhookSecret, azJoinedBody, t0.Add(6*time.Minute))), http.StatusOK)
	// A retry carries its original, older timestamp: it does not hide the skew.
	azWantStatus(t, e.serve(azSignedWebhook(t, azWebhookSecret, azJoinedBody, t0.Add(-85*time.Minute))), http.StatusOK)
	st := status()
	if st.ClockSkewSeconds == nil || *st.ClockSkewSeconds != 360 || st.LastRejectedAt != nil {
		t.Fatalf("status = %+v, want skew 360 s and no refusal", st)
	}

	// Our clock 3 h ahead: older than ZOOM_WEBHOOK_MAX_AGE, refused.
	clock.set(t0.Add(10 * time.Minute))
	azWantStatus(t, e.serve(azSignedWebhook(t, azWebhookSecret, azJoinedBody, t0.Add(10*time.Minute-3*time.Hour))), http.StatusUnauthorized)
	st = status()
	if st.LastRejectedAt == nil || !st.LastRejectedAt.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("last_rejected_at = %v", st.LastRejectedAt)
	}
	if !strings.Contains(e.logs.String(), "NTP") {
		t.Errorf("the log does not point at the clock:\n%s", e.logs.String())
	}
	// A forged request is no measurement.
	azWantStatus(t, e.serve(azSignedWebhook(t, "other secret", azJoinedBody, t0.Add(20*time.Hour))), http.StatusUnauthorized)
	if st := status(); *st.ClockSkewSeconds != 360 {
		t.Fatalf("an unsigned request changed the skew: %+v", st)
	}
}

func TestWebhookClockWindows(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 10, 6, 15, 0, 0, 0, time.UTC)
	var c webhookClock
	c.observe(t0, t0.Add(5*time.Minute))
	c.observe(t0.Add(10*time.Minute), t0.Add(9*time.Minute)) // a delayed delivery
	if skew, ok, _ := c.state(); !ok || skew != 5*time.Minute {
		t.Fatalf("skew = %v, %v; want 5m", skew, ok)
	}
	// The clock was fixed: after two windows the old skew is forgotten.
	c.observe(t0.Add(70*time.Minute), t0.Add(70*time.Minute))
	if skew, _, _ := c.state(); skew != 5*time.Minute {
		t.Fatalf("previous window forgotten too early: %v", skew)
	}
	c.observe(t0.Add(140*time.Minute), t0.Add(140*time.Minute))
	if skew, _, _ := c.state(); skew != 0 {
		t.Fatalf("skew = %v, want 0 once the clock is right", skew)
	}
}
