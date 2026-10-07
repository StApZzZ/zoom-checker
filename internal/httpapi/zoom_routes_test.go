package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"zoom-checker/internal/model"
	"zoom-checker/internal/names"
)

func TestZoomStatus(t *testing.T) {
	t.Parallel()
	e := newAZWebhookEnv(t)
	teacher := e.createUser("teacher", "", model.RoleTeacher, nil)
	tc := e.sessionFor(teacher, model.AuthLocal, "")

	empty := azDecode[map[string]any](t, e.do("GET", "/api/zoom/status", nil, tc))
	if empty["last_event_at"] != nil || empty["webhook_url"] != "http://app.test/api/webhooks/zoom" ||
		empty["secret_configured"] != true || empty["events_24h"] != float64(0) {
		t.Fatalf("status = %v", empty)
	}
	azWantStatus(t, e.serve(azSignedWebhook(t, azWebhookSecret, azJoinedBody, azNow())), http.StatusOK)
	rec := e.do("GET", "/api/zoom/status", nil, tc)
	azWantStatus(t, rec, http.StatusOK)
	st := azDecode[zoomStatusDTO](t, rec)
	if st.LastEventAt == nil || st.LastEventType != "meeting.participant_joined" || st.Events24h != 1 || st.PendingCount != 1 || st.FailedCount != 0 {
		t.Fatalf("status = %+v", st)
	}
	if !strings.Contains(rec.Body.String(), `"last_event_at":"`) || !strings.Contains(rec.Body.String(), "Z\"") {
		t.Fatalf("time must be RFC 3339 UTC: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), azWebhookSecret) {
		t.Fatal("secret leaked")
	}
	azWantError(t, e.do("GET", "/api/zoom/status", nil, nil), http.StatusUnauthorized, CodeUnauthorized)

	off := newAZEnv(t)
	_, ac := off.adminCookie()
	if st := azDecode[zoomStatusDTO](t, off.do("GET", "/api/zoom/status", nil, ac)); st.SecretConfigured {
		t.Fatal("secret_configured must be false")
	}
}

func TestZoomParse(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	teacher := e.createUser("teacher", "", model.RoleTeacher, nil)
	tc := e.sessionFor(teacher, model.AuthLocal, "")
	q := func(in string) string { return "/api/zoom/parse?input=" + url.QueryEscape(in) }

	rec := e.do("GET", q("https://us02web.zoom.us/j/85746065432?pwd=abc"), nil, tc)
	azWantStatus(t, rec, http.StatusOK)
	if got := azDecode[zoomParseDTO](t, rec); got.MeetingID != "85746065432" || got.Formatted != "857 4606 5432" {
		t.Fatalf("parse = %+v", got)
	}
	msg := azWantError(t, e.do("GET", q("https://zoom.us/my/ivanov"), nil, tc), http.StatusUnprocessableEntity, CodeValidation)
	if msg != names.ErrMeetingIDPersonal.Error() {
		t.Fatalf("message %q", msg)
	}
	azWantError(t, e.do("GET", "/api/zoom/parse", nil, tc), http.StatusUnprocessableEntity, CodeValidation)
	azWantError(t, e.do("GET", q("123"), nil, nil), http.StatusUnauthorized, CodeUnauthorized)
}

func (e *azEnv) insertEvent(status model.EventStatus, typ, name string) int64 {
	e.t.Helper()
	ev := &model.WebhookEvent{EventType: typ, MeetingID: "85746065432", MeetingUUID: "uuid-1", ParticipantName: name,
		DedupeKey: fmt.Sprintf("k-%s-%s", typ, name), Payload: `{"event":"` + typ + `"}`, ReceivedAt: time.Now().UTC(),
		ProcessingStatus: status}
	if status == model.EventFailed {
		ev.Attempts, ev.Error = 6, "boom"
	}
	id, err := e.st.InsertWebhookEvent(context.Background(), ev)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func TestZoomEventsJournal(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	_, admin := e.adminCookie()
	id1 := e.insertEvent(model.EventProcessed, "meeting.participant_joined", "Иванов Иван")
	id2 := e.insertEvent(model.EventFailed, "meeting.participant_left", "Петров Пётр")
	id3 := e.insertEvent(model.EventIgnored, "meeting.chat_message_sent", "")
	id4 := e.insertEvent(model.EventFailed, "meeting.participant_joined", "Сидорова Анна")

	page := azDecode[webhookEventsPageDTO](t, e.do("GET", "/api/zoom/events?limit=2", nil, admin))
	if len(page.Items) != 2 || page.Items[0].ID != id4 || page.Items[1].ID != id3 || page.NextBeforeID == nil || *page.NextBeforeID != id3 {
		t.Fatalf("page 1 = %+v", page)
	}
	rec := e.do("GET", fmt.Sprintf("/api/zoom/events?limit=2&before_id=%d", *page.NextBeforeID), nil, admin)
	page = azDecode[webhookEventsPageDTO](t, rec)
	if len(page.Items) != 2 || page.Items[0].ID != id2 || page.Items[1].ID != id1 || page.NextBeforeID != nil {
		t.Fatalf("page 2 = %+v", page)
	}
	if !strings.Contains(rec.Body.String(), `"next_before_id":null`) || strings.Contains(rec.Body.String(), `"payload"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}

	page = azDecode[webhookEventsPageDTO](t, e.do("GET", "/api/zoom/events?status=failed", nil, admin))
	if len(page.Items) != 2 || page.Items[0].ProcessingStatus != model.EventFailed {
		t.Fatalf("failed = %+v", page)
	}
	page = azDecode[webhookEventsPageDTO](t, e.do("GET", "/api/zoom/events?q="+url.QueryEscape("петров"), nil, admin))
	if len(page.Items) != 1 || page.Items[0].ID != id2 {
		t.Fatalf("search = %+v", page)
	}
	page = azDecode[webhookEventsPageDTO](t, e.do("GET", "/api/zoom/events?type=meeting.chat_message_sent", nil, admin))
	if len(page.Items) != 1 || page.Items[0].ID != id3 {
		t.Fatalf("type = %+v", page)
	}
	empty := e.do("GET", "/api/zoom/events?status=pending", nil, admin)
	if !strings.Contains(empty.Body.String(), `"items":[]`) {
		t.Fatalf("empty page = %s", empty.Body.String())
	}
	azWantError(t, e.do("GET", "/api/zoom/events?status=weird", nil, admin), 422, CodeValidation)
	azWantError(t, e.do("GET", "/api/zoom/events?limit=0", nil, admin), 400, CodeBadRequest)
	azWantError(t, e.do("GET", "/api/zoom/events?before_id=x", nil, admin), 400, CodeBadRequest)
	if p := azDecode[webhookEventsPageDTO](t, e.do("GET", "/api/zoom/events?limit=100000", nil, admin)); len(p.Items) != 4 {
		t.Fatalf("capped limit = %+v", p)
	}

	// Detail with payload.
	detail := azDecode[WebhookEventDetailDTO](t, e.do("GET", fmt.Sprintf("/api/zoom/events/%d", id2), nil, admin))
	if detail.ID != id2 || detail.Payload != `{"event":"meeting.participant_left"}` || detail.Attempts != 6 || detail.Error != "boom" {
		t.Fatalf("detail = %+v", detail)
	}
	azWantError(t, e.do("GET", "/api/zoom/events/99999", nil, admin), 404, CodeNotFound)

	// Reprocess one.
	rec = e.do("POST", fmt.Sprintf("/api/zoom/events/%d/reprocess", id2), map[string]any{}, admin)
	azWantStatus(t, rec, http.StatusOK)
	detail = azDecode[WebhookEventDetailDTO](t, rec)
	if detail.ProcessingStatus != model.EventPending || detail.Attempts != 0 || detail.Error != "" {
		t.Fatalf("reprocessed = %+v", detail)
	}
	if e.proc.notifyCount() != 1 || len(e.azAudit(model.AuditEventReprocess)) != 1 {
		t.Fatalf("notify %d, audit %v", e.proc.notifyCount(), e.azAudit(model.AuditEventReprocess))
	}
	azWantError(t, e.do("POST", "/api/zoom/events/99999/reprocess", map[string]any{}, admin), 404, CodeNotFound)

	// Reprocess all failed: only id4 is still failed.
	rec = e.do("POST", "/api/zoom/events/reprocess-failed", map[string]any{}, admin)
	azWantStatus(t, rec, http.StatusOK)
	if strings.TrimSpace(rec.Body.String()) != `{"count":1}` {
		t.Fatalf("reprocess-failed = %s", rec.Body.String())
	}
	if e.proc.notifyCount() != 2 {
		t.Fatalf("notify %d", e.proc.notifyCount())
	}
	rec = e.do("POST", "/api/zoom/events/reprocess-failed", map[string]any{}, admin)
	if strings.TrimSpace(rec.Body.String()) != `{"count":0}` {
		t.Fatalf("second reprocess-failed = %s", rec.Body.String())
	}
}

func TestZoomSessions(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	ctx := context.Background()
	teacher := e.createUser("teacher", "", model.RoleTeacher, nil)
	tc := e.sessionFor(teacher, model.AuthLocal, "")

	g := &model.Group{Name: "10А"}
	if err := e.st.CreateGroup(ctx, g); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	lesson := &model.Lesson{GroupID: g.ID, Title: "Урок", ZoomMeetingID: "85746065432", PlannedStartAt: start, PlannedEndAt: start.Add(time.Hour)}
	if err := e.st.CreateLesson(ctx, lesson); err != nil {
		t.Fatal(err)
	}
	s1, err := e.st.UpsertZoomMeeting(ctx, &model.ZoomMeeting{MeetingUUID: "uuid-a", MeetingID: "85746065432", Topic: "A", FirstEventAt: start})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := e.st.UpsertZoomMeeting(ctx, &model.ZoomMeeting{MeetingUUID: "uuid-b", MeetingID: "111222333", Topic: "B",
		FirstEventAt: start.Add(time.Hour), LessonID: &lesson.ID})
	if err != nil {
		t.Fatal(err)
	}

	rec := e.do("GET", "/api/zoom/sessions", nil, tc)
	all := azDecode[[]ZoomSessionDTO](t, rec)
	if len(all) != 2 || all[0].ID != s2.ID {
		t.Fatalf("all sessions = %+v", all)
	}
	// Linked sessions carry their lesson's summary (no request per row).
	wantLesson := ZoomSessionLessonDTO{ID: lesson.ID, Title: "Урок", GroupName: "10А", PlannedStartAt: start}
	if l := all[0].Lesson; l == nil || *l != wantLesson {
		t.Fatalf("lesson of a linked session = %+v, want %+v", l, wantLesson)
	}
	if all[1].Lesson != nil || !strings.Contains(rec.Body.String(), `"lesson":null`) {
		t.Fatalf("unlinked session must have lesson null: %s", rec.Body.String())
	}
	unl := azDecode[[]ZoomSessionDTO](t, e.do("GET", "/api/zoom/sessions?unlinked=1", nil, tc))
	if len(unl) != 1 || unl[0].ID != s1.ID || unl[0].LessonID != nil {
		t.Fatalf("unlinked = %+v", unl)
	}
	if one := azDecode[[]ZoomSessionDTO](t, e.do("GET", "/api/zoom/sessions?limit=1", nil, tc)); len(one) != 1 {
		t.Fatalf("limit = %+v", one)
	}
	if many := azDecode[[]ZoomSessionDTO](t, e.do("GET", "/api/zoom/sessions?limit=5000", nil, tc)); len(many) != 2 {
		t.Fatalf("limit above the maximum is capped, not refused: %+v", many)
	}
	azWantError(t, e.do("GET", "/api/zoom/sessions?limit=0", nil, tc), 400, CodeBadRequest)
	azWantError(t, e.do("GET", "/api/zoom/sessions?limit=x", nil, tc), 400, CodeBadRequest)
	azWantError(t, e.do("GET", "/api/zoom/sessions?unlinked=maybe", nil, tc), 400, CodeBadRequest)

	link := fmt.Sprintf("/api/zoom/sessions/%d/link", s1.ID)
	rec = e.do("POST", link, map[string]any{"lesson_id": lesson.ID}, tc)
	azWantStatus(t, rec, http.StatusOK)
	got := azDecode[ZoomSessionDTO](t, rec)
	if got.LessonID == nil || *got.LessonID != lesson.ID || got.LinkMode != model.LinkManual ||
		got.Lesson == nil || *got.Lesson != wantLesson {
		t.Fatalf("linked = %+v", got)
	}
	audit := e.azAudit(model.AuditSessionLink)
	if len(audit) != 1 || audit[0].LessonID == nil || *audit[0].LessonID != lesson.ID || audit[0].UserName == "" {
		t.Fatalf("audit = %+v", audit)
	}

	rec = e.do("POST", link, map[string]any{"lesson_id": nil}, tc)
	azWantStatus(t, rec, http.StatusOK)
	if got := azDecode[ZoomSessionDTO](t, rec); got.LessonID != nil {
		t.Fatalf("unlinked = %+v", got)
	}
	if !strings.Contains(rec.Body.String(), `"lesson_id":null`) {
		t.Fatalf("body = %s", rec.Body.String())
	}

	azWantError(t, e.do("POST", link, map[string]any{"lesson_id": 99999}, tc), 422, CodeValidation)
	azWantError(t, e.do("POST", link, map[string]any{}, tc), 422, CodeValidation)
	azWantError(t, e.do("POST", link, map[string]any{"lesson_id": "x"}, tc), 422, CodeValidation)
	azWantError(t, e.do("POST", "/api/zoom/sessions/99999/link", map[string]any{"lesson_id": lesson.ID}, tc), 404, CodeNotFound)
	if n := len(e.proc.links); n != 2 {
		t.Fatalf("LinkSession calls = %d", n)
	}
}
