package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"zoom-checker/internal/model"
	"zoom-checker/internal/store"
	"zoom-checker/internal/zoom"
)

const (
	azWebhookSecret     = "secret_token"
	azURLValidationBody = `{"payload":{"plainToken":"qgg8vlvZRS6UYooatFL8Aw"},"event_ts":1654503849680,"event":"endpoint.url_validation"}`
	azJoinedBody        = `{"event":"meeting.participant_joined","event_ts":1791277275841,"payload":{"account_id":"acc",` +
		`"object":{"id":"85746065432","uuid":"/kV8nE2xQ+O0hR3dWm1x9A==","host_id":"host1","topic":"Математика",` +
		`"start_time":"2026-10-06T09:00:03Z","participant":{"user_id":"16778240","user_name":"Иванов  Иван",` +
		`"participant_uuid":"D2FF531A-1CEE-C0E7-EB63-A05B8F09F41C","join_time":"2026-10-06T09:01:15Z"}}}}`
)

func azNow() time.Time { return time.Now().UTC() }

// azSignedWebhook builds a webhook request signed the way Zoom does it.
func azSignedWebhook(t *testing.T, secret, body string, ts time.Time) *http.Request {
	t.Helper()
	r := azRequest(t, "POST", "/api/webhooks/zoom", body)
	r.Header.Set("Content-Type", "application/json")
	tsH, sig := zoom.Sign(secret, ts, []byte(body))
	r.Header.Set(zoom.HeaderTimestamp, tsH)
	r.Header.Set(zoom.HeaderSignature, sig)
	r.RemoteAddr = "170.114.0.10:443"
	return r
}

func newAZWebhookEnv(t *testing.T) *azEnv {
	return newAZEnv(t, func(s *azSetup) { s.cfg.ZoomWebhookSecret = azWebhookSecret })
}

func (e *azEnv) storedEvents() []model.WebhookEvent {
	e.t.Helper()
	evs, err := e.st.ListWebhookEvents(context.Background(), store.EventFilter{Limit: 100})
	if err != nil {
		e.t.Fatal(err)
	}
	return evs
}

func TestWebhookMissingSecret(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	for range 3 {
		rec := e.serve(azSignedWebhook(t, "whatever", azJoinedBody, azNow()))
		azWantError(t, rec, http.StatusServiceUnavailable, CodeUnavailable)
	}
	if len(e.storedEvents()) != 0 || e.proc.notifyCount() != 0 {
		t.Fatal("nothing may be stored without a secret")
	}
}

func TestWebhookRejectsBadRequests(t *testing.T) {
	t.Parallel()
	e := newAZWebhookEnv(t)
	now := azNow()
	cases := []struct {
		name string
		req  func() *http.Request
	}{
		{"wrong secret", func() *http.Request { return azSignedWebhook(t, "other secret", azJoinedBody, now) }},
		{"tampered body", func() *http.Request {
			r := azSignedWebhook(t, azWebhookSecret, azJoinedBody, now)
			tampered := strings.Replace(azJoinedBody, "Иванов", "Петров", 1)
			r.Body = http.NoBody
			r2 := azRequest(t, "POST", "/api/webhooks/zoom", tampered)
			r2.Header = r.Header
			return r2
		}},
		{"missing headers", func() *http.Request { return azRequest(t, "POST", "/api/webhooks/zoom", azJoinedBody) }},
		{"stale timestamp", func() *http.Request { return azSignedWebhook(t, azWebhookSecret, azJoinedBody, now.Add(-3*time.Hour)) }},
		// An hour ahead only means our clock is behind (accepted); two days is not Zoom.
		{"far in the future", func() *http.Request {
			return azSignedWebhook(t, azWebhookSecret, azJoinedBody, now.Add(48*time.Hour))
		}},
	}
	for _, c := range cases {
		rec := e.serve(c.req())
		msg := azWantError(t, rec, http.StatusUnauthorized, CodeInvalidSignature)
		if strings.Contains(rec.Body.String(), azWebhookSecret) || msg == "" {
			t.Fatalf("%s: body %s", c.name, rec.Body.String())
		}
	}
	if n := len(e.storedEvents()); n != 0 {
		t.Fatalf("%d rejected events stored", n)
	}
	logs := e.logs.String()
	if !strings.Contains(logs, "signature check failed") || strings.Contains(logs, azWebhookSecret) || strings.Contains(logs, "v0=") {
		t.Fatalf("logs:\n%s", logs)
	}
}

func TestWebhookURLValidation(t *testing.T) {
	t.Parallel()
	e := newAZWebhookEnv(t)
	rec := e.serve(azSignedWebhook(t, azWebhookSecret, azURLValidationBody, azNow()))
	azWantStatus(t, rec, http.StatusOK)
	// Known vector: HMAC-SHA256("secret_token", "qgg8vlvZRS6UYooatFL8Aw").
	want := `{"plainToken":"qgg8vlvZRS6UYooatFL8Aw","encryptedToken":"0abd212d4653d125d6cbf403b3f529f1804ffabd9578aabfd1af0e4b2b1701f2"}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("answer = %s, want %s", got, want)
	}
	evs := e.storedEvents()
	if len(evs) != 1 || evs[0].EventType != zoom.EventURLValidation || evs[0].ProcessingStatus != model.EventIgnored {
		t.Fatalf("stored = %+v", evs)
	}
}

func TestWebhookParticipantJoined(t *testing.T) {
	t.Parallel()
	e := newAZWebhookEnv(t)
	start := time.Now()
	rec := e.serve(azSignedWebhook(t, azWebhookSecret, azJoinedBody, azNow()))
	azWantStatus(t, rec, http.StatusOK)
	if strings.TrimSpace(rec.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("webhook took %v", d)
	}
	if e.proc.notifyCount() != 1 {
		t.Fatalf("Notify called %d times", e.proc.notifyCount())
	}
	evs := e.storedEvents()
	if len(evs) != 1 {
		t.Fatalf("stored %d events", len(evs))
	}
	ev, err := e.st.GetWebhookEvent(context.Background(), evs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if ev.ProcessingStatus != model.EventPending || ev.EventType != zoom.EventParticipantJoined ||
		ev.MeetingID != "85746065432" || ev.MeetingUUID != "/kV8nE2xQ+O0hR3dWm1x9A==" ||
		ev.ParticipantName == "" || ev.Payload != azJoinedBody || ev.DedupeKey == "" {
		t.Fatalf("stored event = %+v", ev)
	}
	logs := e.logs.String()
	if !strings.Contains(logs, "zoom webhook received") || !strings.Contains(logs, "event_type=meeting.participant_joined") ||
		!strings.Contains(logs, "meeting_id=85746065432") || !strings.Contains(logs, "participant=") {
		t.Fatalf("log line missing:\n%s", logs)
	}

	// Redelivery is stored again (the worker marks the duplicate).
	azWantStatus(t, e.serve(azSignedWebhook(t, azWebhookSecret, azJoinedBody, azNow())), http.StatusOK)
	if evs := e.storedEvents(); len(evs) != 2 || evs[0].DedupeKey != evs[1].DedupeKey {
		t.Fatalf("redelivery = %+v", evs)
	}
}

func TestWebhookIgnoredEvents(t *testing.T) {
	t.Parallel()
	e := newAZWebhookEnv(t)
	for _, body := range []string{
		`{"event":"meeting.chat_message_sent","event_ts":1,"payload":{"object":{"id":"1","uuid":"u"}}}`,
		`not json at all`,
	} {
		rec := e.serve(azSignedWebhook(t, azWebhookSecret, body, azNow()))
		azWantStatus(t, rec, http.StatusOK)
	}
	evs := e.storedEvents()
	if len(evs) != 2 {
		t.Fatalf("stored %d", len(evs))
	}
	for _, ev := range evs {
		if ev.ProcessingStatus != model.EventIgnored || ev.Error == "" {
			t.Fatalf("event %+v should be ignored with a reason", ev)
		}
	}
}

func TestWebhookBodyTooLarge(t *testing.T) {
	t.Parallel()
	e := newAZWebhookEnv(t)
	big := `{"event":"x","pad":"` + strings.Repeat("a", 1<<20) + `"}`
	rec := e.serve(azSignedWebhook(t, azWebhookSecret, big, azNow()))
	azWantStatus(t, rec, http.StatusRequestEntityTooLarge)
	if len(e.storedEvents()) != 0 {
		t.Fatal("oversized body stored")
	}
	// Exactly at the limit is accepted (and ignored as unknown).
	exact := `{"event":"x","pad":"` + strings.Repeat("a", (1<<20)-len(`{"event":"x","pad":""}`)) + `"}`
	if len(exact) != 1<<20 {
		t.Fatalf("len %d", len(exact))
	}
	azWantStatus(t, e.serve(azSignedWebhook(t, azWebhookSecret, exact, azNow())), http.StatusOK)
}

func TestWebhookDatabaseDown(t *testing.T) {
	t.Parallel()
	e := newAZWebhookEnv(t)
	_ = e.st.DB().Close()
	rec := e.serve(azSignedWebhook(t, azWebhookSecret, azJoinedBody, azNow()))
	azWantError(t, rec, http.StatusInternalServerError, CodeInternal)
	if e.proc.notifyCount() != 0 {
		t.Fatal("Notify without a stored event")
	}
}

func TestWebhookNeedsNoSession(t *testing.T) {
	t.Parallel()
	e := newAZWebhookEnv(t)
	r := azSignedWebhook(t, azWebhookSecret, azJoinedBody, azNow())
	r.AddCookie(azGarbageCookie())
	azWantStatus(t, e.serve(r), http.StatusOK)
	if !bytes.Contains([]byte(e.logs.String()), []byte("status=200")) {
		t.Fatalf("request log missing:\n%s", e.logs.String())
	}
}

func azGarbageCookie() *http.Cookie { return &http.Cookie{Name: SessionCookie, Value: "garbage"} }
