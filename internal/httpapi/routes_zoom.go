package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"zoom-checker/internal/model"
	"zoom-checker/internal/names"
	"zoom-checker/internal/processor"
	"zoom-checker/internal/store"
	"zoom-checker/internal/zoom"
)

const (
	// maxWebhookBody limits Zoom webhook bodies (real ones are a few KiB).
	maxWebhookBody = 1 << 20

	// CodeInvalidSignature is returned when a webhook fails zoom.Verify.
	CodeInvalidSignature = "invalid_signature"

	eventsDefaultLimit   = 50
	eventsMaxLimit       = 200
	sessionsDefaultLimit = 100
	sessionsMaxLimit     = 500
	eventsMaxQueryLen    = 200

	msgEventNotFound   = "Событие не найдено"
	msgSessionNotFound = "Конференция не найдена: обновите страницу"
)

func (s *Server) registerZoom(mux *http.ServeMux) {
	// Server-to-server from Zoom: no session, authenticated by HMAC; the
	// CSRF protection bypass for it is set up in New.
	mux.HandleFunc("POST /api/webhooks/zoom", s.handleZoomWebhook)

	mux.HandleFunc("GET /api/zoom/status", s.authed(s.handleZoomStatus))
	mux.HandleFunc("GET /api/zoom/parse", s.authed(s.handleZoomParse))

	mux.HandleFunc("GET /api/zoom/events", s.admin(s.handleListEvents))
	mux.HandleFunc("GET /api/zoom/events/{id}", s.admin(s.handleGetEvent))
	mux.HandleFunc("POST /api/zoom/events/{id}/reprocess", s.admin(s.handleReprocessEvent))
	mux.HandleFunc("POST /api/zoom/events/reprocess-failed", s.admin(s.handleReprocessFailed))

	mux.HandleFunc("GET /api/zoom/sessions", s.authed(s.handleListSessions))
	mux.HandleFunc("POST /api/zoom/sessions/{id}/link", s.authed(s.handleLinkSession))
}

// ---------------------------------------------------------------------------
// Webhook (docs §5.7)
// ---------------------------------------------------------------------------

// missingSecretWarnedAt rate-limits the "secret not configured" warning to
// one per minute (Zoom keeps retrying, the log must stay readable).
var missingSecretWarnedAt atomic.Int64

func (s *Server) warnMissingWebhookSecret(now time.Time) {
	last := missingSecretWarnedAt.Load()
	if last != 0 && now.UnixNano()-last < int64(time.Minute) {
		return
	}
	if missingSecretWarnedAt.CompareAndSwap(last, now.UnixNano()) {
		s.Log.Warn("zoom webhook rejected: ZOOM_WEBHOOK_SECRET_TOKEN is not set; copy the Secret Token of the Zoom app into the configuration")
	}
}

// handleZoomWebhook stores a verified raw event and answers at once; the
// processor does the rest asynchronously (store first, then 200, TZ §23).
func (s *Server) handleZoomWebhook(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	secret := s.Config.ZoomWebhookSecret
	if secret == "" {
		s.warnMissingWebhookSecret(now)
		writeError(w, r, s.Log, &APIError{Status: http.StatusServiceUnavailable, Code: CodeUnavailable,
			Message: "Приём событий Zoom не настроен: не задан секретный ключ (ZOOM_WEBHOOK_SECRET_TOKEN)"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			s.Log.Warn("zoom webhook rejected: body too large", "ip", clientIP(r))
			writeError(w, r, s.Log, &APIError{Status: http.StatusRequestEntityTooLarge, Code: CodeBadRequest,
				Message: "Слишком большой запрос"})
			return
		}
		writeError(w, r, s.Log, errBadRequest("Не удалось прочитать запрос"))
		return
	}
	err = zoom.Verify(secret, r.Header, body, now, s.Config.ZoomWebhookMaxAge)
	if err == nil || errors.Is(err, zoom.ErrStale) {
		// Correctly signed: its timestamp is Zoom's clock.
		if at, ok := zoom.RequestTime(r.Header); ok {
			s.zoomClock.observe(now, at)
		}
	}
	if err != nil {
		// The error text never contains the secret or the signatures.
		if errors.Is(err, zoom.ErrStale) {
			s.zoomClock.rejected(now)
			s.Log.Warn("zoom webhook rejected: its time is too far from the server clock; if the server clock is wrong, "+
				"turn on time synchronization (NTP)", "reason", err.Error(), "ip", clientIP(r))
		} else {
			s.Log.Warn("zoom webhook rejected: signature check failed", "reason", err.Error(), "ip", clientIP(r))
		}
		writeError(w, r, s.Log, &APIError{Status: http.StatusUnauthorized, Code: CodeInvalidSignature,
			Message: "Подпись запроса Zoom не прошла проверку"})
		return
	}

	ev, perr := zoom.Parse(body)
	raw := processor.BuildRawEvent(body, ev, perr, now)
	id, err := s.Store.InsertWebhookEvent(r.Context(), raw)
	if err != nil {
		// 5xx makes Zoom retry the delivery later; nothing is lost.
		writeError(w, r, s.Log, errInternal(err))
		return
	}

	if perr != nil {
		s.Log.Warn("zoom webhook payload not understood; stored for diagnostics", "event_id", id, "reason", perr.Error())
	}
	// The stored columns, not the raw payload: they are cleaned (no NUL or
	// control characters) and length-limited by BuildRawEvent.
	s.Log.Info("zoom webhook received", "event_type", raw.EventType, "meeting_id", raw.MeetingID,
		"meeting_uuid", raw.MeetingUUID, "participant", raw.ParticipantName, "event_id", id, "status", raw.ProcessingStatus)

	if perr == nil && ev.Type == zoom.EventURLValidation {
		writeJSON(w, http.StatusOK, zoom.URLValidation(secret, ev.PlainToken))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	s.Processor.Notify()
}

// ---------------------------------------------------------------------------
// Connection status and meeting id parsing
// ---------------------------------------------------------------------------

// zoomStatusDTO is GET /api/zoom/status (types.ts ZoomStatus).
type zoomStatusDTO struct {
	WebhookURL       string     `json:"webhook_url"`
	SecretConfigured bool       `json:"secret_configured"`
	LastEventAt      *time.Time `json:"last_event_at"`
	LastEventType    string     `json:"last_event_type"`
	Events24h        int        `json:"events_24h"`
	FailedCount      int        `json:"failed_count"`
	PendingCount     int        `json:"pending_count"`
	// ClockSkewSeconds is Zoom's request time minus the server clock, measured
	// on recent signed webhooks (positive: the server clock is behind); null
	// before the first one since the server started.
	ClockSkewSeconds *int64 `json:"clock_skew_seconds"`
	// LastRejectedAt is the last signed webhook refused because its time was
	// too far from the server clock (the event was lost); null when none.
	LastRejectedAt *time.Time `json:"last_rejected_at"`
}

func (s *Server) handleZoomStatus(w http.ResponseWriter, r *http.Request) error {
	st, err := s.Store.EventStats(r.Context(), s.now())
	if err != nil {
		return errInternal(err)
	}
	d := zoomStatusDTO{
		WebhookURL:       s.Config.WebhookURL(),
		SecretConfigured: s.Config.ZoomWebhookSecret != "",
		LastEventAt:      utcPtr(st.LastEventAt),
		LastEventType:    st.LastEventType,
		Events24h:        st.Last24h,
		FailedCount:      st.Failed,
		PendingCount:     st.Pending,
	}
	skew, measured, rejected := s.zoomClock.state()
	if measured {
		secs := int64(skew.Round(time.Second) / time.Second)
		d.ClockSkewSeconds = &secs
	}
	if !rejected.IsZero() {
		d.LastRejectedAt = utcPtr(&rejected)
	}
	writeJSON(w, http.StatusOK, d)
	return nil
}

// zoomParseDTO is GET /api/zoom/parse (types.ts ZoomParseResult).
type zoomParseDTO struct {
	MeetingID string `json:"meeting_id"`
	Formatted string `json:"formatted"`
}

func (s *Server) handleZoomParse(w http.ResponseWriter, r *http.Request) error {
	id, err := names.ParseMeetingID(r.URL.Query().Get("input"))
	if err != nil {
		return errValidation(err.Error(), map[string]any{"field": "input"})
	}
	writeJSON(w, http.StatusOK, zoomParseDTO{MeetingID: id, Formatted: names.FormatMeetingID(id)})
	return nil
}

// ---------------------------------------------------------------------------
// Raw event journal (admin)
// ---------------------------------------------------------------------------

// webhookEventsPageDTO is GET /api/zoom/events (types.ts WebhookEventsPage).
type webhookEventsPageDTO struct {
	Items        []WebhookEventSummaryDTO `json:"items"`
	NextBeforeID *int64                   `json:"next_before_id"`
}

var knownEventStatuses = []model.EventStatus{
	model.EventPending, model.EventProcessed, model.EventDuplicate, model.EventIgnored, model.EventFailed,
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	f := store.EventFilter{
		Type:  strings.TrimSpace(q.Get("type")),
		Query: strings.TrimSpace(q.Get("q")),
	}
	if st := strings.TrimSpace(q.Get("status")); st != "" {
		ok := false
		for _, k := range knownEventStatuses {
			ok = ok || string(k) == st
		}
		if !ok {
			return errValidation("Неизвестный статус обработки события", map[string]any{"field": "status"})
		}
		f.Status = model.EventStatus(st)
	}
	if len([]rune(f.Query)) > eventsMaxQueryLen {
		return errValidation("Слишком длинная строка поиска", map[string]any{"field": "q"})
	}
	if len(f.Type) > eventsMaxQueryLen {
		return errValidation("Слишком длинный тип события", map[string]any{"field": "type"})
	}
	before, err := queryInt64(r, "before_id")
	if err != nil {
		return err
	}
	if before != nil {
		if *before <= 0 {
			return errBadRequest("Некорректный параметр before_id")
		}
		f.BeforeID = *before
	}
	limit, err := zoomQueryLimit(r, eventsDefaultLimit, eventsMaxLimit)
	if err != nil {
		return err
	}
	f.Limit = limit + 1 // one extra row tells whether another page exists
	events, err := s.Store.ListWebhookEvents(r.Context(), f)
	if err != nil {
		return errInternal(err)
	}
	page := webhookEventsPageDTO{Items: make([]WebhookEventSummaryDTO, 0, min(len(events), limit))}
	if len(events) > limit {
		events = events[:limit]
		next := events[limit-1].ID
		page.NextBeforeID = &next
	}
	for i := range events {
		page.Items = append(page.Items, toWebhookEventSummaryDTO(&events[i]))
	}
	writeJSON(w, http.StatusOK, page)
	return nil
}

// zoomQueryLimit parses ?limit= (default def, at most maxLimit, at least 1).
func zoomQueryLimit(r *http.Request, def, maxLimit int) (int, error) {
	v, err := queryInt64(r, "limit")
	if err != nil {
		return 0, err
	}
	if v == nil {
		return def, nil
	}
	if *v < 1 {
		return 0, errBadRequest("Некорректный параметр limit")
	}
	return int(min(*v, int64(maxLimit))), nil
}

func (s *Server) handleGetEvent(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	e, err := s.Store.GetWebhookEvent(r.Context(), id)
	if err != nil {
		return storeErr(err, msgEventNotFound)
	}
	writeJSON(w, http.StatusOK, toWebhookEventDetailDTO(e))
	return nil
}

func (s *Server) handleReprocessEvent(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	prev, err := s.Store.GetWebhookEvent(ctx, id)
	if err != nil {
		return storeErr(err, msgEventNotFound)
	}
	if err := s.Store.ResetEventForReprocess(ctx, id); err != nil {
		return storeErr(err, msgEventNotFound)
	}
	s.Processor.Notify()
	s.audit(r, model.AuditEntry{Action: model.AuditEventReprocess, EntityType: "webhook_event",
		EntityID: strconv.FormatInt(id, 10), PreviousValue: string(prev.ProcessingStatus), NewValue: string(model.EventPending)})
	e, err := s.Store.GetWebhookEvent(ctx, id)
	if err != nil {
		return storeErr(err, msgEventNotFound)
	}
	writeJSON(w, http.StatusOK, toWebhookEventDetailDTO(e))
	return nil
}

// reprocessResultDTO is types.ts ReprocessResult.
type reprocessResultDTO struct {
	Count int64 `json:"count"`
}

func (s *Server) handleReprocessFailed(w http.ResponseWriter, r *http.Request) error {
	n, err := s.Store.ResetFailedEvents(r.Context())
	if err != nil {
		return errInternal(err)
	}
	if n > 0 {
		s.Processor.Notify()
		s.audit(r, model.AuditEntry{Action: model.AuditEventReprocess, EntityType: "webhook_event",
			EntityID: "failed", PreviousValue: string(model.EventFailed),
			NewValue: string(model.EventPending) + ": " + strconv.FormatInt(n, 10)})
	}
	writeJSON(w, http.StatusOK, reprocessResultDTO{Count: n})
	return nil
}

// ---------------------------------------------------------------------------
// Zoom sessions (meeting instances) and manual linking
// ---------------------------------------------------------------------------

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) error {
	limit, err := zoomQueryLimit(r, sessionsDefaultLimit, sessionsMaxLimit)
	if err != nil {
		return err
	}
	unlinked := false
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("unlinked"))) {
	case "", "0", "false", "no":
	case "1", "true", "yes":
		unlinked = true
	default:
		return errBadRequest("Некорректный параметр unlinked")
	}
	ms, err := s.Store.ListZoomMeetings(r.Context(), store.ZoomMeetingFilter{UnlinkedOnly: unlinked, Limit: limit})
	if err != nil {
		return errInternal(err)
	}
	var lessonIDs []int64
	for _, m := range ms {
		if m.LessonID != nil && !slices.Contains(lessonIDs, *m.LessonID) {
			lessonIDs = append(lessonIDs, *m.LessonID)
		}
	}
	refs, err := s.Store.LessonRefs(r.Context(), lessonIDs)
	if err != nil {
		return errInternal(err)
	}
	out := make([]ZoomSessionDTO, 0, len(ms))
	for i := range ms {
		out = append(out, toZoomSessionDTO(&ms[i], s.zoomSessionLesson(ms[i].LessonID, refs)))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// zoomSessionLesson picks the summary of lesson id from refs (nil when id is
// nil or the lesson is gone).
func (s *Server) zoomSessionLesson(id *int64, refs map[int64]store.LessonRef) *ZoomSessionLessonDTO {
	if id == nil {
		return nil
	}
	r, ok := refs[*id]
	if !ok {
		return nil
	}
	return toZoomSessionLessonDTO(r)
}

func (s *Server) handleLinkSession(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var body struct {
		LessonID json.RawMessage `json:"lesson_id"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		return err
	}
	var lessonID *int64
	switch raw := strings.TrimSpace(string(body.LessonID)); raw {
	case "":
		return errValidation("Выберите занятие, к которому нужно привязать конференцию", map[string]any{"field": "lesson_id"})
	case "null":
	default:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			return errValidation("Занятие выбрано неверно. Обновите страницу и выберите его ещё раз.", map[string]any{"field": "lesson_id"})
		}
		lessonID = &n
	}
	ctx := r.Context()
	prev, err := s.Store.GetZoomMeeting(ctx, id)
	if err != nil {
		return storeErr(err, msgSessionNotFound)
	}
	var target *model.Lesson
	if lessonID != nil {
		if target, err = s.Store.GetLesson(ctx, *lessonID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return errValidation("Занятие не найдено: возможно, его уже удалили", map[string]any{"field": "lesson_id"})
			}
			return errInternal(err)
		}
	}
	if err := s.Processor.LinkSession(ctx, id, lessonID); err != nil {
		if errors.Is(err, store.ErrNotFound) && lessonID != nil {
			// The session was checked above: the lesson went away in between.
			if _, gerr := s.Store.GetZoomMeeting(ctx, id); gerr == nil {
				return errValidation("Занятие не найдено: возможно, его уже удалили", map[string]any{"field": "lesson_id"})
			}
		}
		return storeErr(err, msgSessionNotFound)
	}

	var before *model.Lesson
	if prev.LessonID != nil {
		if l, err := s.Store.GetLesson(ctx, *prev.LessonID); err == nil {
			before = l
		}
	}
	entry := model.AuditEntry{Action: model.AuditSessionLink, EntityType: "zoom_session",
		EntityID: strconv.FormatInt(id, 10), PreviousValue: s.zoomLessonRefText(prev.LessonID, before),
		NewValue: s.zoomLessonRefText(lessonID, target)}
	// Every affected lesson's history shows the change: the new one got these
	// participants, the old one lost them.
	audited := false
	if lessonID != nil {
		e := entry
		e.LessonID, e.GroupID = lessonID, &target.GroupID
		s.audit(r, e)
		audited = true
	}
	if prev.LessonID != nil && (lessonID == nil || *prev.LessonID != *lessonID) {
		e := entry
		e.LessonID = prev.LessonID
		if before != nil {
			e.GroupID = &before.GroupID
		}
		s.audit(r, e)
		audited = true
	}
	if !audited {
		// Unlinking a session that had no lesson still stops auto-linking it.
		s.audit(r, entry)
	}
	m, err := s.Store.GetZoomMeeting(ctx, id)
	if err != nil {
		return storeErr(err, msgSessionNotFound)
	}
	var refIDs []int64
	if m.LessonID != nil {
		refIDs = []int64{*m.LessonID}
	}
	refs, err := s.Store.LessonRefs(ctx, refIDs)
	if err != nil {
		return errInternal(err)
	}
	writeJSON(w, http.StatusOK, toZoomSessionDTO(m, s.zoomSessionLesson(m.LessonID, refs)))
	return nil
}

// zoomLessonRefText names a lesson in the audit log of a session link:
// title and planned start in the service time zone (l may be nil when the
// lesson is gone; id == nil means "no lesson").
func (s *Server) zoomLessonRefText(id *int64, l *model.Lesson) string {
	switch {
	case id == nil:
		return "не привязана к занятию"
	case l == nil:
		return "занятие №" + strconv.FormatInt(*id, 10)
	}
	return fmt.Sprintf("занятие «%s» %s", l.Title, s.lessonFmtTime(l.PlannedStartAt))
}
