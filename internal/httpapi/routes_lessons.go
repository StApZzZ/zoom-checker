package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"zoom-checker/internal/model"
	"zoom-checker/internal/names"
	"zoom-checker/internal/processor"
	"zoom-checker/internal/realtime"
	"zoom-checker/internal/store"
	"zoom-checker/internal/timefmt"
)

// Lesson routes (docs/architecture.md §6). The live screen payload is built by
// buildLessonView (lessons_view.go); manual decisions live in lessons_manual.go,
// exports in lessons_export.go and SSE streams in lessons_sse.go.

const (
	lessonMaxTitle       = 200
	lessonMaxJoinURL     = 500
	lessonMaxDuration    = 12 * time.Hour
	lessonMaxRepeat      = 52
	lessonFinishSlack    = time.Minute // "finish at" may be this much in the future (clock skew)
	lessonRebuildTimeout = time.Minute
	lessonNotFoundMsg    = "Занятие не найдено. Возможно, его уже удалили."
)

func (s *Server) registerLessons(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/lessons", s.authed(s.handleLessonList))
	mux.HandleFunc("POST /api/lessons", s.authed(s.handleLessonCreate))
	mux.HandleFunc("GET /api/lessons/{id}", s.authed(s.handleLessonGet))
	mux.HandleFunc("PATCH /api/lessons/{id}", s.authed(s.handleLessonUpdate))
	mux.HandleFunc("DELETE /api/lessons/{id}", s.authed(s.handleLessonDelete))
	mux.HandleFunc("POST /api/lessons/{id}/finish", s.authed(s.handleLessonFinish))
	mux.HandleFunc("POST /api/lessons/{id}/reopen", s.authed(s.handleLessonReopen))
	mux.HandleFunc("POST /api/lessons/{id}/recompute", s.authed(s.handleLessonRecompute))
	mux.HandleFunc("GET /api/lessons/{id}/attendance", s.authed(s.handleLessonView))
	mux.HandleFunc("GET /api/lessons/{id}/unidentified", s.authed(s.handleLessonUnidentified))
	mux.HandleFunc("GET /api/lessons/{id}/participants", s.authed(s.handleLessonParticipants))
	mux.HandleFunc("POST /api/lessons/{id}/participants/{participant_id}/map", s.authed(s.handleLessonMapParticipant))
	mux.HandleFunc("DELETE /api/lessons/{id}/participants/{participant_id}/map", s.authed(s.handleLessonUnmapParticipant))
	mux.HandleFunc("PATCH /api/lessons/{id}/attendance/{student_id}", s.authed(s.handleLessonUpdateAttendance))
	mux.HandleFunc("GET /api/lessons/{id}/students/{student_id}/history", s.authed(s.handleLessonStudentHistory))
	mux.HandleFunc("GET /api/lessons/{id}/audit", s.authed(s.handleLessonAudit))
	mux.HandleFunc("GET /api/lessons/{id}/export", s.authed(s.handleLessonExport))
	mux.HandleFunc("GET /api/lessons/{id}/stream", s.authed(s.handleLessonStream))
	mux.HandleFunc("GET /api/stream", s.authed(s.handleLessonsStream))
}

// ---- list ----

// handleLessonList: GET /api/lessons?from=&to=&group_id=&status= → [LessonSummary].
// from/to are RFC 3339 instants or YYYY-MM-DD days in the configured time zone;
// a day in "to" is inclusive (the filter ends at the start of the next day), an
// instant is exclusive.
func (s *Server) handleLessonList(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	var f store.LessonFilter
	var err error
	if f.From, err = s.lessonRangeParam(q.Get("from"), false); err != nil {
		return err
	}
	if f.To, err = s.lessonRangeParam(q.Get("to"), true); err != nil {
		return err
	}
	if f.GroupID, err = queryInt64(r, "group_id"); err != nil {
		return err
	}
	if st := strings.TrimSpace(q.Get("status")); st != "" {
		ls := model.LessonStatus(st)
		if ls != model.LessonPlanned && ls != model.LessonActive && ls != model.LessonFinished {
			return errValidation("Неизвестный статус занятия", map[string]any{"field": "status"})
		}
		f.Status = &ls
	}
	lessons, err := s.Store.ListLessons(r.Context(), f)
	if err != nil {
		return errInternal(err)
	}
	groups, err := s.Store.ListGroups(r.Context(), store.GroupFilterAll, s.now())
	if err != nil {
		return errInternal(err)
	}
	groupNames := make(map[int64]string, len(groups))
	for _, g := range groups {
		groupNames[g.ID] = g.Name
	}
	ids := make([]int64, len(lessons))
	for i := range lessons {
		ids[i] = lessons[i].ID
	}
	counts, err := s.Store.LessonCounts(r.Context(), ids)
	if err != nil {
		return errInternal(err)
	}
	out := make([]LessonSummaryDTO, 0, len(lessons))
	for i := range lessons {
		l := &lessons[i]
		out = append(out, toLessonSummaryDTO(l, groupNames[l.GroupID], counts[l.ID]))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// lessonRangeParam parses a from/to filter value; nil when empty.
func (s *Server) lessonRangeParam(v string, upper bool) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	if d, err := time.ParseInLocation(time.DateOnly, v, s.lessonLoc()); err == nil {
		if upper {
			d = d.AddDate(0, 0, 1) // the whole day is included
		}
		u := d.UTC()
		return &u, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		u := t.UTC()
		return &u, nil
	}
	return nil, errValidation("Некорректная дата в фильтре. Используйте формат ГГГГ-ММ-ДД.", map[string]any{"value": v})
}

// ---- create ----

type lessonCreateRequest struct {
	GroupID        int64  `json:"group_id"`
	Title          string `json:"title"`
	Zoom           string `json:"zoom"`
	PlannedStartAt string `json:"planned_start_at"`
	PlannedEndAt   string `json:"planned_end_at"`
	RepeatWeekly   *int   `json:"repeat_weekly"`
}

// handleLessonCreate: POST /api/lessons → 201 [Lesson]. repeat_weekly creates a
// weekly series on the same wall-clock time in the configured time zone; a number
// at the end of the title grows by one per occurrence ("Занятие №4", "№5", ...).
func (s *Server) handleLessonCreate(w http.ResponseWriter, r *http.Request) error {
	var req lessonCreateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	ctx := r.Context()

	if req.GroupID <= 0 {
		return errValidation("Выберите группу", map[string]any{"field": "group_id"})
	}
	g, err := s.Store.GetGroup(ctx, req.GroupID)
	if errors.Is(err, store.ErrNotFound) {
		return errValidation("Группа не найдена. Возможно, её удалили.", map[string]any{"field": "group_id"})
	} else if err != nil {
		return errInternal(err)
	}
	if g.Status != model.StatusActive {
		return errValidation("Группа в архиве. Сначала верните её из архива.", map[string]any{"field": "group_id"})
	}
	title, err := lessonValidTitle(req.Title)
	if err != nil {
		return err
	}
	meetingID, joinURL, err := lessonParseZoom(req.Zoom)
	if err != nil {
		return err
	}
	start, err := s.lessonParseTime(req.PlannedStartAt, "planned_start_at", "Укажите дату и время начала занятия")
	if err != nil {
		return err
	}
	end, err := s.lessonParseTime(req.PlannedEndAt, "planned_end_at", "Укажите время окончания занятия")
	if err != nil {
		return err
	}
	if err := lessonValidSpan(start, end); err != nil {
		return err
	}
	repeat := 1
	if req.RepeatWeekly != nil && *req.RepeatWeekly != 0 {
		repeat = *req.RepeatWeekly
	}
	if repeat < 1 || repeat > lessonMaxRepeat {
		return errValidation(fmt.Sprintf("Количество повторов — от 1 до %d недель", lessonMaxRepeat),
			map[string]any{"field": "repeat_weekly"})
	}

	loc := s.lessonLoc()
	created := make([]model.Lesson, 0, repeat)
	err = s.Store.InTx(ctx, func(tx *store.Store) error {
		created = created[:0]
		for i := range repeat {
			l := model.Lesson{
				GroupID:        g.ID,
				Title:          lessonSeriesTitle(title, i),
				ZoomMeetingID:  meetingID,
				ZoomJoinURL:    joinURL,
				PlannedStartAt: lessonAddWeeks(start, i, loc),
				PlannedEndAt:   lessonAddWeeks(end, i, loc),
				CreatedBy:      userIDPtr(r),
			}
			if err := tx.CreateLesson(ctx, &l); err != nil {
				return err
			}
			created = append(created, l)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrInvalid) {
			return errValidation("Проверьте время и номер конференции занятия", nil)
		}
		return storeErr(err, "Группа не найдена")
	}

	gid := g.ID
	for i := range created {
		l := &created[i]
		lid := l.ID
		s.audit(r, model.AuditEntry{
			Action: model.AuditLessonCreate, EntityType: "lesson", EntityID: strconv.FormatInt(lid, 10),
			LessonID: &lid, GroupID: &gid, NewValue: s.lessonDescribe(l, g.Name),
		})
		s.lessonRelink(r, lid)
	}
	s.lessonPublish(0)

	out := make([]LessonDTO, 0, len(created))
	for i := range created {
		dto, err := s.lessonDTO(ctx, created[i].ID)
		if err != nil {
			return storeErr(err, lessonNotFoundMsg)
		}
		out = append(out, *dto)
	}
	writeJSON(w, http.StatusCreated, out)
	return nil
}

// ---- get / update / delete ----

func (s *Server) handleLessonGet(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	dto, err := s.lessonDTO(r.Context(), id)
	if err != nil {
		return storeErr(err, lessonNotFoundMsg)
	}
	writeJSON(w, http.StatusOK, dto)
	return nil
}

type lessonUpdateRequest struct {
	Title          *string `json:"title"`
	Zoom           *string `json:"zoom"`
	PlannedStartAt *string `json:"planned_start_at"`
	PlannedEndAt   *string `json:"planned_end_at"`
}

// handleLessonUpdate: PATCH /api/lessons/{id} → Lesson. Changing the meeting id or
// the planned time re-evaluates which Zoom sessions belong to the lesson.
func (s *Server) handleLessonUpdate(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var req lessonUpdateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	old, err := s.Store.GetLesson(ctx, id)
	if err != nil {
		return storeErr(err, lessonNotFoundMsg)
	}
	upd := *old
	if req.Title != nil {
		if upd.Title, err = lessonValidTitle(*req.Title); err != nil {
			return err
		}
	}
	if req.Zoom != nil {
		meetingID, joinURL, err := lessonParseZoom(*req.Zoom)
		if err != nil {
			return err
		}
		// Keep the stored link when only the number was pasted for the same meeting.
		if joinURL == "" && meetingID == old.ZoomMeetingID {
			joinURL = old.ZoomJoinURL
		}
		upd.ZoomMeetingID, upd.ZoomJoinURL = meetingID, joinURL
	}
	if req.PlannedStartAt != nil {
		if upd.PlannedStartAt, err = s.lessonParseTime(*req.PlannedStartAt, "planned_start_at", "Укажите дату и время начала занятия"); err != nil {
			return err
		}
	}
	if req.PlannedEndAt != nil {
		if upd.PlannedEndAt, err = s.lessonParseTime(*req.PlannedEndAt, "planned_end_at", "Укажите время окончания занятия"); err != nil {
			return err
		}
	}
	if err := lessonValidSpan(upd.PlannedStartAt, upd.PlannedEndAt); err != nil {
		return err
	}

	prev, next := s.lessonDiff(old, &upd)
	if prev == "" && next == "" {
		dto, err := s.lessonDTO(ctx, id)
		if err != nil {
			return storeErr(err, lessonNotFoundMsg)
		}
		writeJSON(w, http.StatusOK, dto)
		return nil
	}
	if err := s.Store.UpdateLesson(ctx, &upd); err != nil {
		if errors.Is(err, store.ErrInvalid) {
			return errValidation("Проверьте время и номер конференции занятия", nil)
		}
		return storeErr(err, lessonNotFoundMsg)
	}
	gid := old.GroupID
	s.audit(r, model.AuditEntry{
		Action: model.AuditLessonUpdate, EntityType: "lesson", EntityID: strconv.FormatInt(id, 10),
		LessonID: &id, GroupID: &gid, PreviousValue: prev, NewValue: next,
	})
	if upd.ZoomMeetingID != old.ZoomMeetingID || !upd.PlannedStartAt.Equal(old.PlannedStartAt) ||
		!upd.PlannedEndAt.Equal(old.PlannedEndAt) {
		s.lessonRelink(r, id)
	}
	s.lessonPublish(id)
	dto, err := s.lessonDTO(ctx, id)
	if err != nil {
		return storeErr(err, lessonNotFoundMsg)
	}
	writeJSON(w, http.StatusOK, dto)
	return nil
}

// handleLessonDelete: DELETE /api/lessons/{id} → 204. Linked Zoom sessions are
// detached (store.DeleteLesson) and handed to the remaining lesson that fits
// them best (lessonRelinkAfterDelete).
func (s *Server) handleLessonDelete(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	l, err := s.Store.GetLesson(ctx, id)
	if err != nil {
		return storeErr(err, lessonNotFoundMsg)
	}
	groupName := ""
	if g, err := s.Store.GetGroup(ctx, l.GroupID); err == nil {
		groupName = g.Name
	}
	// Listed before the delete detaches them.
	sessions, err := s.Store.ListZoomMeetingsByLesson(ctx, id)
	if err != nil {
		return errInternal(err)
	}
	if err := s.Store.DeleteLesson(ctx, id); err != nil {
		return storeErr(err, lessonNotFoundMsg)
	}
	gid := l.GroupID
	s.audit(r, model.AuditEntry{
		Action: model.AuditLessonDelete, EntityType: "lesson", EntityID: strconv.FormatInt(id, 10),
		LessonID: &id, GroupID: &gid, PreviousValue: s.lessonDescribe(l, groupName),
	})
	// Open live screens notice the deletion (their next snapshot fails and the stream closes).
	s.lessonPublish(id)
	s.lessonRelinkAfterDelete(r, l, sessions)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// lessonRelinkAfterDelete hands the Zoom sessions of a deleted lesson (now
// unlinked, auto mode) to the remaining lesson they fit best. Typical case: a
// lesson created twice by mistake, the wrong copy deleted after the meeting
// ended — no Zoom event will come any more, so without this the remaining
// lesson stayed empty for good. Every lesson that could be a candidate of a
// detached session is re-linked: lessons with the deleted lesson's meeting id
// whose link ranges overlap its own (processor.NearbyRange; this also covers a
// session linked while the delete ran), and the candidates of each detached session at its own
// start — it may have been linked by hand from another meeting id or another
// day. Failures are logged: the delete itself succeeded, and the next Zoom
// event of a session links it.
func (s *Server) lessonRelinkAfterDelete(r *http.Request, deleted *model.Lesson, sessions []model.ZoomMeeting) {
	if s.Processor == nil || len(sessions) == 0 {
		return
	}
	ctx, release := s.detach(r.Context(), lessonRebuildTimeout)
	defer release()
	settings, err := s.Store.GetSettings(ctx)
	if err != nil {
		s.Log.Error("relink after lesson delete: settings", "lesson_id", deleted.ID, "err", err)
		return
	}
	type probe struct {
		meetingID string
		from, to  time.Time
	}
	// Lessons that competed with the deleted one for its sessions, and each
	// session's own candidates (it may have been linked by hand from another
	// meeting id or another day).
	from, to := processor.NearbyRange(settings, deleted.PlannedStartAt, deleted.PlannedEndAt)
	probes := []probe{{deleted.ZoomMeetingID, from, to}}
	for _, m := range sessions {
		if m.MeetingID == "" {
			continue
		}
		ref := m.FirstEventAt
		if m.StartedAt != nil {
			ref = *m.StartedAt
		}
		from, to := processor.CandidateRange(settings, ref)
		probes = append(probes, probe{m.MeetingID, from, to})
	}
	done := map[int64]bool{deleted.ID: true}
	for _, p := range probes {
		near, err := s.Store.FindLessonsByMeeting(ctx, p.meetingID, p.from, p.to)
		if err != nil {
			s.Log.Error("relink after lesson delete: find lessons", "lesson_id", deleted.ID, "err", err)
			continue
		}
		for _, l := range near {
			if done[l.ID] {
				continue
			}
			done[l.ID] = true
			if err := s.Processor.RelinkLesson(ctx, l.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
				s.Log.Error("relink after lesson delete failed", "deleted_lesson_id", deleted.ID, "lesson_id", l.ID, "err", err)
			}
		}
	}
}

// ---- finish / reopen / recompute ----

type lessonFinishRequest struct {
	At *string `json:"at"`
}

// handleLessonFinish: POST /api/lessons/{id}/finish {at?} → LessonView. The end
// time defaults to now; it may not be in the future nor before the lesson started.
func (s *Server) handleLessonFinish(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var req lessonFinishRequest
	if err := lessonDecodeOptionalJSON(w, r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	l, err := s.Store.GetLesson(ctx, id)
	if err != nil {
		return storeErr(err, lessonNotFoundMsg)
	}
	now := s.now()
	at := now
	explicit := req.At != nil && strings.TrimSpace(*req.At) != ""
	if explicit {
		if at, err = s.lessonParseTime(*req.At, "at", ""); err != nil {
			return err
		}
	}
	if at.After(now.Add(lessonFinishSlack)) {
		return errValidation("Время окончания не может быть в будущем", map[string]any{"field": "at"})
	}
	start := l.PlannedStartAt
	if l.ActualStartAt != nil {
		start = *l.ActualStartAt
	}
	if at.Before(start) {
		msg := "Время окончания не может быть раньше начала занятия (" + s.lessonFmtTime(start) + ")"
		if !explicit {
			msg = "Занятие ещё не началось — завершить его пока нельзя"
		}
		return errValidation(msg, map[string]any{"field": "at"})
	}
	at = at.UTC().Truncate(time.Millisecond)
	if err := s.Store.SetLessonManualFinished(ctx, id, &at); err != nil {
		return storeErr(err, lessonNotFoundMsg)
	}
	gid := l.GroupID
	prev := ""
	if l.ManualFinishedAt != nil {
		prev = "Завершено вручную: " + s.lessonFmtTime(*l.ManualFinishedAt)
	}
	s.audit(r, model.AuditEntry{
		Action: model.AuditLessonFinish, EntityType: "lesson", EntityID: strconv.FormatInt(id, 10),
		LessonID: &id, GroupID: &gid, PreviousValue: prev, NewValue: "Завершено вручную: " + s.lessonFmtTime(at),
	})
	if err := s.lessonRebuild(r, id); err != nil {
		// The finish time is saved even though the rebuild failed: open screens
		// must still learn about it (the processor publishes only on success).
		s.lessonPublish(id)
		return errInternal(err)
	}
	return s.writeLessonView(w, r, id)
}

// handleLessonReopen: POST /api/lessons/{id}/reopen → LessonView. It clears the
// manual finish. A lesson that ended together with its Zoom meeting cannot be
// reopened by hand (docs §5.4 rule 5: it stays finished until a new Zoom session
// starts), so that is a 409 with an explanation instead of a silent no-op that
// the screen would report as "reopened". When the teacher finished it manually
// and the meeting has ended too, clearing the manual time is still allowed: the
// lesson then ends at the Zoom end time (the way to undo a wrong manual time).
func (s *Server) handleLessonReopen(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	l, err := s.Store.GetLesson(ctx, id)
	if err != nil {
		return storeErr(err, lessonNotFoundMsg)
	}
	sessions, err := s.Store.ListZoomMeetingsByLesson(ctx, id)
	if err != nil {
		return errInternal(err)
	}
	zoomEnd := lessonZoomEnded(sessions)
	if l.ManualFinishedAt == nil && zoomEnd != nil {
		return errConflict("Занятие завершилось вместе с конференцией в Zoom ("+s.lessonFmtTime(*zoomEnd)+
			"), поэтому возобновить его вручную нельзя. Если конференцию запустят снова, занятие откроется само.",
			map[string]any{"zoom_ended_at": zoomEnd.UTC()})
	}
	if l.ManualFinishedAt != nil {
		if err := s.Store.SetLessonManualFinished(ctx, id, nil); err != nil {
			return storeErr(err, lessonNotFoundMsg)
		}
		next := "Занятие возобновлено"
		if zoomEnd != nil {
			next = "Ручное завершение отменено; занятие остаётся завершённым: конференция в Zoom закончилась " +
				s.lessonFmtTime(*zoomEnd)
		}
		gid := l.GroupID
		s.audit(r, model.AuditEntry{
			Action: model.AuditLessonReopen, EntityType: "lesson", EntityID: strconv.FormatInt(id, 10),
			LessonID: &id, GroupID: &gid, PreviousValue: "Завершено вручную: " + s.lessonFmtTime(*l.ManualFinishedAt),
			NewValue: next,
		})
	}
	if err := s.lessonRebuild(r, id); err != nil {
		if l.ManualFinishedAt != nil {
			s.lessonPublish(id) // the reopen is saved; see handleLessonFinish
		}
		return errInternal(err)
	}
	return s.writeLessonView(w, r, id)
}

// handleLessonRecompute: POST /api/lessons/{id}/recompute → LessonView.
func (s *Server) handleLessonRecompute(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if _, err := s.Store.GetLesson(r.Context(), id); err != nil {
		return storeErr(err, lessonNotFoundMsg)
	}
	if err := s.lessonRebuild(r, id); err != nil {
		return errInternal(err)
	}
	return s.writeLessonView(w, r, id)
}

// ---- views, audit, history ----

// handleLessonView: GET /api/lessons/{id}/attendance → LessonView.
func (s *Server) handleLessonView(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	return s.writeLessonView(w, r, id)
}

// writeLessonView answers with the current LessonView of a lesson.
func (s *Server) writeLessonView(w http.ResponseWriter, r *http.Request, id int64) error {
	v, err := s.buildLessonView(r.Context(), id, s.lessonNow())
	if err != nil {
		return storeErr(err, lessonNotFoundMsg)
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

// handleLessonAudit: GET /api/lessons/{id}/audit → [AuditEntry], newest first.
func (s *Server) handleLessonAudit(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if _, err := s.Store.GetLesson(r.Context(), id); err != nil {
		return storeErr(err, lessonNotFoundMsg)
	}
	entries, err := s.Store.ListAudit(r.Context(), store.AuditFilter{LessonID: &id, Limit: 1000})
	if err != nil {
		return errInternal(err)
	}
	out := make([]AuditDTO, 0, len(entries))
	for i := range entries {
		out = append(out, toAuditDTO(&entries[i]))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// lessonStudentHistoryDTO is GET /api/lessons/{id}/students/{student_id}/history.
type lessonStudentHistoryDTO struct {
	Intervals []IntervalDTO `json:"intervals"`
	Audit     []AuditDTO    `json:"audit"`
}

// handleLessonStudentHistory returns the student's presence intervals and the
// audit entries of this lesson about the student, newest first:
//   - attendance.* entries with entity_id = student id (status, comment);
//   - alias.* entries with entity_type "student" and entity_id = student id;
//   - participant.* entries whose previous or new value mentions the student.
//     Audit texts always reference a student as "<ФИО> (id <N>)", so the marker
//     "(id <N>)" identifies the mention exactly.
func (s *Server) handleLessonStudentHistory(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	sid, err := pathID(r, "student_id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	l, err := s.Store.GetLesson(ctx, id)
	if err != nil {
		return storeErr(err, lessonNotFoundMsg)
	}
	st, err := s.Store.GetStudent(ctx, sid)
	if err != nil {
		return storeErr(err, "Студент не найден")
	}
	if st.GroupID != l.GroupID {
		return errNotFound("Этот студент не из группы занятия")
	}
	ivs, err := s.Store.ListStudentIntervals(ctx, id, sid)
	if err != nil {
		return errInternal(err)
	}
	entries, err := s.Store.ListAudit(ctx, store.AuditFilter{LessonID: &id, Limit: 5000})
	if err != nil {
		return errInternal(err)
	}
	sidText := strconv.FormatInt(sid, 10)
	marker := lessonStudentMarker(sid)
	out := lessonStudentHistoryDTO{Intervals: make([]IntervalDTO, 0, len(ivs)), Audit: []AuditDTO{}}
	for i := range ivs {
		out.Intervals = append(out.Intervals, toIntervalDTO(&ivs[i]))
	}
	for i := range entries {
		e := &entries[i]
		var keep bool
		switch {
		case strings.HasPrefix(e.Action, "attendance."):
			keep = e.EntityID == sidText
		case strings.HasPrefix(e.Action, "alias."):
			keep = e.EntityType == "student" && e.EntityID == sidText
		case strings.HasPrefix(e.Action, "participant."):
			keep = strings.Contains(e.PreviousValue, marker) || strings.Contains(e.NewValue, marker)
		}
		if keep {
			out.Audit = append(out.Audit, toAuditDTO(e))
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// ---- helpers ----

// lessonDTO loads a lesson with its group name and sessions.
func (s *Server) lessonDTO(ctx context.Context, id int64) (*LessonDTO, error) {
	l, err := s.Store.GetLesson(ctx, id)
	if err != nil {
		return nil, err
	}
	g, err := s.Store.GetGroup(ctx, l.GroupID)
	if err != nil {
		return nil, fmt.Errorf("lesson %d: group: %w", id, err)
	}
	sessions, err := s.Store.ListZoomMeetingsByLesson(ctx, id)
	if err != nil {
		return nil, err
	}
	dto := toLessonDTO(l, g.Name, sessions)
	return &dto, nil
}

// lessonRebuild recomputes the projection of a lesson. It survives the client
// going away (a half-done rebuild would only be retried by the next event),
// not the server closing (detach).
func (s *Server) lessonRebuild(r *http.Request, id int64) error {
	if s.Processor == nil {
		return nil
	}
	ctx, release := s.detach(r.Context(), lessonRebuildTimeout)
	defer release()
	if err := s.Processor.RebuildLesson(ctx, id); err != nil {
		return fmt.Errorf("rebuild lesson %d: %w", id, err)
	}
	return nil
}

// lessonRebuildLogged rebuilds after a change that is already saved: a failure is
// logged, not returned, because the change itself succeeded (the next Zoom event
// or "Пересчитать" rebuilds again).
func (s *Server) lessonRebuildLogged(r *http.Request, id int64) {
	if err := s.lessonRebuild(r, id); err != nil {
		s.Log.Error("lesson rebuild failed", "lesson_id", id, "err", err)
	}
}

// lessonRelink re-evaluates the Zoom sessions of a created/edited lesson
// (failure is logged: the lesson itself is saved).
func (s *Server) lessonRelink(r *http.Request, id int64) {
	if s.Processor == nil {
		return
	}
	ctx, release := s.detach(r.Context(), lessonRebuildTimeout)
	defer release()
	if err := s.Processor.RelinkLesson(ctx, id); err != nil {
		s.Log.Error("lesson relink failed", "lesson_id", id, "err", err)
	}
}

// lessonPublish pokes the live screen of lesson id (0 = none) and lesson lists.
func (s *Server) lessonPublish(id int64) {
	if s.Broker == nil {
		return
	}
	if id > 0 {
		s.Broker.Publish(realtime.LessonTopic(id), realtime.TopicLessons)
		return
	}
	s.Broker.Publish(realtime.TopicLessons)
}

// lessonLoc is the configured time zone (exports, day filters, series).
func (s *Server) lessonLoc() *time.Location {
	if s.Config != nil && s.Config.Timezone != nil {
		return s.Config.Timezone
	}
	return time.UTC
}

// exportLoc is the zone of a file download: the optional `tz` query parameter
// (the browser sends its own IANA zone, so the file shows the times seen on
// the screen), or TIMEZONE when it is missing or unknown.
func (s *Server) exportLoc(r *http.Request) *time.Location {
	if loc := timefmt.LoadZone(r.URL.Query().Get("tz")); loc != nil {
		return loc
	}
	return s.lessonLoc()
}

// viewClockMaxBackstep is the largest backward step of the system clock that
// lessonNow absorbs (keeps as_of where it was). A larger step is a real clock
// correction: as_of follows the new time, otherwise the durations of everyone
// online would stay inflated by the size of the step until the clock caught up.
// Clients treat a view more than this older than theirs as such a reset
// (web/src/lib isNewerView uses the same 2 minutes).
const viewClockMaxBackstep = 2 * time.Minute

// lessonNow is "now" for lesson views (their as_of): UTC with the store's
// millisecond precision, and never earlier than a view this server already
// handed out — unless the clock went back by more than viewClockMaxBackstep.
// Clients keep the newest view by as_of (stream snapshots, action answers,
// refetches); when the system clock steps back a little (NTP or VM time sync),
// every later snapshot would look older than the one on screen and be
// dropped, so the live screen would freeze until the clock caught up.
func (s *Server) lessonNow() time.Time {
	now := s.now().Truncate(time.Millisecond).UnixMilli()
	for {
		last := s.viewClock.Load()
		switch {
		case now > last:
			if s.viewClock.CompareAndSwap(last, now) {
				return time.UnixMilli(now).UTC()
			}
		case last-now <= viewClockMaxBackstep.Milliseconds():
			return time.UnixMilli(last).UTC()
		default:
			if s.viewClock.CompareAndSwap(last, now) {
				s.Log.Warn("system clock went back; lesson views follow the new time",
					"by", (time.Duration(last-now) * time.Millisecond).String())
				return time.UnixMilli(now).UTC()
			}
		}
	}
}

// lessonFmtTime formats a time for audit texts and messages with the zone
// offset ("06.10.2026 18:00 (UTC+3)"): a teacher whose browser is in another
// zone sees why the time differs from the screen.
func (s *Server) lessonFmtTime(t time.Time) string {
	return timefmt.DateTime(t, s.lessonLoc())
}

// lessonDescribe is the human-readable summary of a lesson for the audit log.
func (s *Server) lessonDescribe(l *model.Lesson, groupName string) string {
	loc := s.lessonLoc()
	return fmt.Sprintf("«%s», группа «%s», Zoom %s, %s–%s (%s)", l.Title, groupName,
		names.FormatMeetingID(l.ZoomMeetingID), l.PlannedStartAt.In(loc).Format(timefmt.DateTimeLayout),
		l.PlannedEndAt.In(loc).Format(timefmt.ClockLayout), timefmt.OffsetLabel(loc, l.PlannedStartAt))
}

// lessonDiff returns human-readable previous/new texts of the changed fields.
func (s *Server) lessonDiff(old, upd *model.Lesson) (prev, next string) {
	var p, n []string
	if old.Title != upd.Title {
		p = append(p, "Название: «"+old.Title+"»")
		n = append(n, "Название: «"+upd.Title+"»")
	}
	if old.ZoomMeetingID != upd.ZoomMeetingID {
		p = append(p, "Zoom: "+names.FormatMeetingID(old.ZoomMeetingID))
		n = append(n, "Zoom: "+names.FormatMeetingID(upd.ZoomMeetingID))
	}
	if old.ZoomJoinURL != upd.ZoomJoinURL {
		link := func(u string) string {
			if u == "" {
				return "Ссылка: нет"
			}
			return "Ссылка: " + u
		}
		p = append(p, link(old.ZoomJoinURL))
		n = append(n, link(upd.ZoomJoinURL))
	}
	if !old.PlannedStartAt.Equal(upd.PlannedStartAt) {
		p = append(p, "Начало: "+s.lessonFmtTime(old.PlannedStartAt))
		n = append(n, "Начало: "+s.lessonFmtTime(upd.PlannedStartAt))
	}
	if !old.PlannedEndAt.Equal(upd.PlannedEndAt) {
		p = append(p, "Окончание: "+s.lessonFmtTime(old.PlannedEndAt))
		n = append(n, "Окончание: "+s.lessonFmtTime(upd.PlannedEndAt))
	}
	return strings.Join(p, "; "), strings.Join(n, "; ")
}

// lessonZoomEnded returns when the Zoom meeting of a lesson ended — the latest
// end of its sessions — or nil while any linked session is still running or no
// session is linked (the engine's rule for a lesson finished by Zoom).
func lessonZoomEnded(sessions []model.ZoomMeeting) *time.Time {
	var end *time.Time
	for i := range sessions {
		e := sessions[i].EndedAt
		if e == nil {
			return nil
		}
		if end == nil || e.After(*end) {
			end = e
		}
	}
	return end
}

// lessonStudentMarker is how audit texts reference a student; the history
// endpoint searches for it.
func lessonStudentMarker(id int64) string { return "(id " + strconv.FormatInt(id, 10) + ")" }

// lessonStudentRef is "<ФИО> (id <N>)".
func lessonStudentRef(name string, id int64) string {
	return strings.TrimSpace(name) + " " + lessonStudentMarker(id)
}

func lessonValidTitle(v string) (string, error) {
	t := strings.TrimSpace(v)
	if t == "" {
		return "", errValidation("Укажите название занятия", map[string]any{"field": "title"})
	}
	if utf8.RuneCountInString(t) > lessonMaxTitle {
		return "", errValidation(fmt.Sprintf("Название слишком длинное: не больше %d символов", lessonMaxTitle),
			map[string]any{"field": "title"})
	}
	return t, nil
}

func lessonValidSpan(start, end time.Time) error {
	if !end.After(start) {
		return errValidation("Время окончания должно быть позже начала", map[string]any{"field": "planned_end_at"})
	}
	if end.Sub(start) > lessonMaxDuration {
		return errValidation("Занятие не может длиться дольше 12 часов", map[string]any{"field": "planned_end_at"})
	}
	return nil
}

// lessonParseTime accepts RFC 3339 (what the client sends) and, leniently, a
// local "YYYY-MM-DDTHH:MM[:SS]" in the configured time zone.
func (s *Server) lessonParseTime(v, field, emptyMsg string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		if emptyMsg == "" {
			emptyMsg = "Укажите время"
		}
		return time.Time{}, errValidation(emptyMsg, map[string]any{"field": field})
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t.UTC(), nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, v, s.lessonLoc()); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, errValidation("Некорректные дата или время", map[string]any{"field": field})
}

var lessonURLRe = regexp.MustCompile(`(?i)https?://` + names.URLChars + `+`)

// lessonParseZoom extracts the meeting id (422 with the parser's Russian message)
// and the join link: the first zoom.us / zoom.com web link that points to the same
// meeting, else the first zoom link at all (e.g. a personal room link next to a
// "Meeting ID:" line); links longer than 500 characters are not stored.
func lessonParseZoom(input string) (meetingID, joinURL string, err error) {
	meetingID, err = names.ParseMeetingID(input)
	if err != nil {
		return "", "", errValidation(err.Error(), map[string]any{"field": "zoom"})
	}
	var first string
	for _, raw := range lessonURLRe.FindAllString(input, -1) {
		raw = strings.TrimRight(raw, ".,;:!?)]}")
		u, err := url.Parse(raw)
		if err != nil || len(raw) > lessonMaxJoinURL {
			continue
		}
		host := strings.ToLower(u.Hostname())
		if host != "zoom.us" && !strings.HasSuffix(host, ".zoom.us") && host != "zoom.com" && !strings.HasSuffix(host, ".zoom.com") {
			continue
		}
		if id, err := names.ParseMeetingID(raw); err == nil && id == meetingID {
			return meetingID, raw, nil
		}
		if first == "" {
			first = raw
		}
	}
	return meetingID, first, nil
}

var lessonTitleNumberRe = regexp.MustCompile(`^(.*?)(\d{1,9})$`)

// lessonSeriesTitle numbers the occurrences of a weekly series: when the title
// ends with a number it grows by i ("Linux 4" → "Linux 5"), keeping zero padding.
func lessonSeriesTitle(title string, i int) string {
	if i == 0 {
		return title
	}
	m := lessonTitleNumberRe.FindStringSubmatch(title)
	if m == nil {
		return title
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return title
	}
	num := strconv.Itoa(n + i)
	if len(m[2]) > 1 && m[2][0] == '0' && len(num) < len(m[2]) {
		num = strings.Repeat("0", len(m[2])-len(num)) + num
	}
	return m[1] + num
}

// lessonAddWeeks moves t by i weeks keeping the wall-clock time in loc, so a
// series stays at 18:00 local across daylight saving changes.
func lessonAddWeeks(t time.Time, i int, loc *time.Location) time.Time {
	if i == 0 {
		return t.UTC()
	}
	lt := t.In(loc)
	return time.Date(lt.Year(), lt.Month(), lt.Day()+7*i, lt.Hour(), lt.Minute(), lt.Second(), lt.Nanosecond(), loc).UTC()
}

// lessonDecodeOptionalJSON is decodeJSON for endpoints whose body may be empty
// (POST /finish from a script without a body).
func lessonDecodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	ct := r.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(ct, "application/json") {
		return &APIError{Status: http.StatusUnsupportedMediaType, Code: CodeBadRequest, Message: "Ожидается JSON"}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSONBody))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return &APIError{Status: http.StatusRequestEntityTooLarge, Code: CodeBadRequest, Message: "Слишком большой запрос"}
		}
		return errBadRequest("Не удалось прочитать запрос")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return &APIError{Status: http.StatusBadRequest, Code: CodeBadRequest, Message: "Некорректный формат запроса", Err: err}
	}
	return nil
}
