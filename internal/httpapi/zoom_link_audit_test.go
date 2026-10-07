package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"zoom-checker/internal/model"
	"zoom-checker/internal/store"
)

// Moving a Zoom session between lessons shows up in the history of both
// lessons, with readable lesson names in the service time zone.
func TestZoomSessionLinkAudit(t *testing.T) {
	t.Parallel()
	msk, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	e := newAZEnv(t, func(s *azSetup) { s.cfg.Timezone = msk })
	ctx := context.Background()
	teacher := e.createUser("teacher", "", model.RoleTeacher, nil)
	tc := e.sessionFor(teacher, model.AuthLocal, "")

	g := &model.Group{Name: "11Б"}
	if err := e.st.CreateGroup(ctx, g); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	mk := func(title string, at time.Time) *model.Lesson {
		l := &model.Lesson{GroupID: g.ID, Title: title, ZoomMeetingID: "85746065432", PlannedStartAt: at, PlannedEndAt: at.Add(time.Hour)}
		if err := e.st.CreateLesson(ctx, l); err != nil {
			t.Fatal(err)
		}
		return l
	}
	a, b := mk("Алгебра", start), mk("Геометрия", start.Add(24*time.Hour))
	sess, err := e.st.UpsertZoomMeeting(ctx, &model.ZoomMeeting{MeetingUUID: "uuid-m", MeetingID: "85746065432",
		FirstEventAt: start, LessonID: &a.ID})
	if err != nil {
		t.Fatal(err)
	}
	link := fmt.Sprintf("/api/zoom/sessions/%d/link", sess.ID)
	lessonAudit := func(id int64) []model.AuditEntry {
		t.Helper()
		out, err := e.st.ListAudit(ctx, store.AuditFilter{LessonID: &id})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	// A -> B: both lessons get an entry.
	azWantStatus(t, e.do("POST", link, map[string]any{"lesson_id": b.ID}, tc), http.StatusOK)
	inA, inB := lessonAudit(a.ID), lessonAudit(b.ID)
	if len(inA) != 1 || len(inB) != 1 {
		t.Fatalf("audit A %+v, B %+v", inA, inB)
	}
	if inB[0].PreviousValue != "занятие «Алгебра» 06.10.2026 12:00 (UTC+3)" || inB[0].NewValue != "занятие «Геометрия» 07.10.2026 12:00 (UTC+3)" ||
		inB[0].GroupID == nil || *inB[0].GroupID != g.ID || inA[0].NewValue != inB[0].NewValue {
		t.Fatalf("audit B = %+v, A = %+v", inB[0], inA[0])
	}

	// Linking to the same lesson again: one entry, on that lesson only.
	azWantStatus(t, e.do("POST", link, map[string]any{"lesson_id": b.ID}, tc), http.StatusOK)
	if len(lessonAudit(a.ID)) != 1 || len(lessonAudit(b.ID)) != 2 {
		t.Fatal("re-link must be audited on the linked lesson only")
	}

	// Unlink: recorded on the lesson that lost the session.
	azWantStatus(t, e.do("POST", link, map[string]any{"lesson_id": nil}, tc), http.StatusOK)
	inB = lessonAudit(b.ID)
	if len(inB) != 3 || inB[0].NewValue != "не привязана к занятию" {
		t.Fatalf("unlink audit = %+v", inB)
	}

	// Unlinking a session without a lesson still leaves a trace.
	before := len(e.azAudit(model.AuditSessionLink))
	azWantStatus(t, e.do("POST", link, map[string]any{"lesson_id": nil}, tc), http.StatusOK)
	if after := e.azAudit(model.AuditSessionLink); len(after) != before+1 {
		t.Fatalf("no audit entry for unlinking an unlinked session: %d -> %d", before, len(after))
	}
}

// Plain-language messages for a broken link request (no field names or "null").
func TestZoomSessionLinkMessages(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	teacher := e.createUser("teacher", "", model.RoleTeacher, nil)
	tc := e.sessionFor(teacher, model.AuthLocal, "")
	sess, err := e.st.UpsertZoomMeeting(context.Background(), &model.ZoomMeeting{MeetingUUID: "u", MeetingID: "85746065432",
		FirstEventAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	link := fmt.Sprintf("/api/zoom/sessions/%d/link", sess.ID)
	for _, body := range []any{map[string]any{}, map[string]any{"lesson_id": "x"}, map[string]any{"lesson_id": -1},
		json.RawMessage(`{"lesson_id": 1.5}`)} {
		msg := azWantError(t, e.do("POST", link, body, tc), http.StatusUnprocessableEntity, CodeValidation)
		if strings.Contains(msg, "null") || strings.Contains(msg, "lesson_id") {
			t.Fatalf("technical message %q", msg)
		}
	}
	msg := azWantError(t, e.do("POST", "/api/zoom/sessions/99999/link", map[string]any{"lesson_id": nil}, tc),
		http.StatusNotFound, CodeNotFound)
	if !strings.Contains(msg, "Конференция") {
		t.Fatalf("not found message %q", msg)
	}
}

// The webhook log line carries the cleaned participant name, never control
// characters or an unbounded string from the payload.
func TestWebhookLogsCleanedFields(t *testing.T) {
	t.Parallel()
	e := newAZWebhookEnv(t)
	long := strings.Repeat("Я", 2000)
	body := strings.Replace(azJoinedBody, `"user_name":"Иванов  Иван"`, `"user_name":"Иванов\u0000Иван`+long+`"`, 1)
	azWantStatus(t, e.serve(azSignedWebhook(t, azWebhookSecret, body, azNow())), http.StatusOK)
	logs := e.logs.String()
	if strings.Contains(logs, "\x00") || strings.Contains(logs, `\x00`) || strings.Contains(logs, long) {
		t.Fatalf("raw participant name logged:\n%.500s", logs)
	}
	if !strings.Contains(logs, "zoom webhook received") || !strings.Contains(logs, "meeting.participant_joined") ||
		!strings.Contains(logs, "ИвановИван") {
		t.Fatalf("webhook log line missing fields:\n%.500s", logs)
	}
}
