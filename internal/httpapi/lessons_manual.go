package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"zoom-checker/internal/model"
	"zoom-checker/internal/names"
	"zoom-checker/internal/store"
)

// Manual teacher decisions on the live lesson screen (TZ §27): mapping Zoom
// participants to students, hiding them, and overriding the final status.

const (
	lessonMaxComment = 2000
	lessonMaxReason  = 500
)

// ---- participant lists ----

// handleLessonUnidentified: GET /api/lessons/{id}/unidentified → [Participant]
// with resolution unidentified|conflict.
func (s *Server) handleLessonUnidentified(w http.ResponseWriter, r *http.Request) error {
	return s.writeLessonParticipants(w, r, true)
}

// handleLessonParticipants: GET /api/lessons/{id}/participants → [Participant] (all).
func (s *Server) handleLessonParticipants(w http.ResponseWriter, r *http.Request) error {
	return s.writeLessonParticipants(w, r, false)
}

func (s *Server) writeLessonParticipants(w http.ResponseWriter, r *http.Request, onlyUnidentified bool) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	v, err := s.buildLessonView(r.Context(), id, s.lessonNow())
	if err != nil {
		return storeErr(err, lessonNotFoundMsg)
	}
	out := make([]ParticipantDTO, 0, len(v.Participants))
	for _, p := range v.Participants {
		if !onlyUnidentified || lvNeedsDecision(p.Resolution) {
			out = append(out, p)
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// ---- map / unmap ----

type lessonMapRequest struct {
	StudentID     *int64 `json:"student_id"`
	Action        string `json:"action"`
	RememberAlias bool   `json:"remember_alias"`
	IgnoreAlways  bool   `json:"ignore_always"`
	Reason        string `json:"reason"`
}

// handleLessonMapParticipant: POST /api/lessons/{id}/participants/{participant_id}/map
// → LessonView.
//
//   - action "student" (default when student_id is given): the name is this
//     student in this lesson; remember_alias also stores the name as the
//     student's alias for future lessons — if the name is used by someone else
//     in the group, nothing is saved (409);
//   - "ignore": not a student; ignore_always adds the name to the global ignore list;
//   - "none": never auto-match this name in this lesson (undo a wrong match).
func (s *Server) handleLessonMapParticipant(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	pid, err := pathID(r, "participant_id")
	if err != nil {
		return err
	}
	var req lessonMapRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	l, p, err := s.lessonParticipant(ctx, id, pid)
	if err != nil {
		return err
	}

	action := model.MappingAction(strings.TrimSpace(req.Action))
	if action == "" {
		if req.StudentID == nil {
			return errValidation("Выберите студента или отметьте участника как «Не студент»", map[string]any{"field": "student_id"})
		}
		action = model.MappingStudent
	}
	switch action {
	case model.MappingStudent, model.MappingIgnore, model.MappingNone:
	default:
		return errValidation("Неизвестное действие", map[string]any{"field": "action"})
	}
	reason, err := lessonValidReason(req.Reason)
	if err != nil {
		return err
	}
	var st *model.Student
	if action == model.MappingStudent {
		if req.StudentID == nil || *req.StudentID <= 0 {
			return errValidation("Выберите студента", map[string]any{"field": "student_id"})
		}
		st, err = s.Store.GetStudent(ctx, *req.StudentID)
		if errors.Is(err, store.ErrNotFound) {
			return errValidation("Студент не найден. Возможно, его удалили.", map[string]any{"field": "student_id"})
		} else if err != nil {
			return errInternal(err)
		}
		if st.GroupID != l.GroupID {
			return errValidation("Этот студент не из группы занятия", map[string]any{"field": "student_id"})
		}
	}
	remember := req.RememberAlias && action == model.MappingStudent
	ignoreAlways := req.IgnoreAlways && action == model.MappingIgnore
	if ignoreAlways && utf8.RuneCountInString(strings.TrimSpace(p.DisplayName)) > maxIgnoredNameLength {
		return errValidation("Это имя слишком длинное, чтобы скрывать его на всех занятиях. "+
			"Снимите отметку «Всегда скрывать это имя» — участник будет скрыт только на этом занятии.",
			map[string]any{"field": "ignore_always"})
	}

	prevMapping, err := s.Store.GetMapping(ctx, id, p.NormalizedName)
	if errors.Is(err, store.ErrNotFound) {
		prevMapping = nil
	} else if err != nil {
		return errInternal(err)
	}
	prevText := s.lessonDescribeDecision(ctx, p, prevMapping)
	var newStudentID *int64
	if st != nil {
		sid := st.ID
		newStudentID = &sid
	}
	// The same decision again (double click, a second tab): nothing to audit
	// unless the alias or the ignore list changes below.
	sameDecision := prevMapping != nil && prevMapping.Action == action && lessonSameID(prevMapping.StudentID, newStudentID)

	var alias *model.Alias
	ignoreAdded := false
	err = func() error {
		if ignoreAlways {
			// Same lock as PUT /api/settings (admin_settings.go): the settings row
			// is read-modified-written, and on PostgreSQL (READ COMMITTED) a
			// concurrent admin save would otherwise be lost, or lose this name.
			settingsMu.Lock()
			defer settingsMu.Unlock()
		}
		return s.Store.InTx(ctx, func(tx *store.Store) error {
			alias, ignoreAdded = nil, false
			if remember {
				a := &model.Alias{StudentID: st.ID, Alias: strings.TrimSpace(p.DisplayName), CreatedBy: userIDPtr(r)}
				if err := tx.AddAlias(ctx, a); err != nil {
					var nt *store.NameTakenError
					// Already this student's own name or alias: nothing to remember.
					// (The store detects that with a SELECT, so the transaction is
					// still usable on PostgreSQL.)
					if !errors.As(err, &nt) || nt.StudentID != st.ID {
						return err
					}
				} else {
					alias = a
				}
			}
			if ignoreAlways {
				set, err := tx.GetSettings(ctx)
				if err != nil {
					return err
				}
				if lessonAddIgnoredName(&set, p.DisplayName) {
					if len(set.IgnoredNames) > maxIgnoredNames {
						return errLessonIgnoreListFull
					}
					if err := tx.PutSettings(ctx, set, userIDPtr(r)); err != nil {
						return err
					}
					ignoreAdded = true
				}
			}
			return tx.UpsertMapping(ctx, &model.Mapping{LessonID: id, NormalizedName: p.NormalizedName, Action: action,
				StudentID: newStudentID, CreatedBy: userIDPtr(r)})
		})
	}()
	if err != nil {
		var nt *store.NameTakenError
		switch {
		case errors.As(err, &nt):
			return s.lessonAliasTakenErr(ctx, p.DisplayName, nt)
		case errors.Is(err, errLessonIgnoreListFull):
			return errValidation(fmt.Sprintf("В списке имён, которые скрываются всегда, уже %d имён — больше добавить нельзя. "+
				"Снимите отметку «Всегда скрывать это имя» или попросите администратора убрать лишние имена.", maxIgnoredNames),
				map[string]any{"field": "ignore_always"})
		case errors.Is(err, store.ErrInvalid) && remember:
			return errValidation("У этого участника пустое имя в Zoom — запомнить его нельзя. Снимите отметку «Запомнить это имя».",
				map[string]any{"field": "remember_alias"})
		}
		return storeErr(err, "Занятие или студент не найдены")
	}

	gid := l.GroupID
	if alias != nil {
		sidText := strconv.FormatInt(st.ID, 10)
		s.audit(r, model.AuditEntry{
			Action: model.AuditAliasAdd, EntityType: "student", EntityID: sidText, LessonID: &id, GroupID: &gid,
			NewValue: "«" + alias.Alias + "» → " + lessonStudentRef(st.FullName, st.ID), Reason: reason,
		})
	}
	if ignoreAdded {
		s.audit(r, model.AuditEntry{
			Action: model.AuditSettingsUpdate, EntityType: "settings", EntityID: settingsEntityID, LessonID: &id,
			NewValue: "Добавлено в список скрываемых имён: «" + strings.TrimSpace(p.DisplayName) + "»", Reason: reason,
		})
	}
	if !sameDecision || alias != nil || ignoreAdded {
		next := lessonDescribeMapping(action, st)
		if alias != nil {
			next += "; имя запомнено для следующих занятий"
		}
		if ignoreAdded {
			next += "; имя будет скрываться на всех занятиях"
		}
		s.audit(r, model.AuditEntry{
			Action: model.AuditParticipantMap, EntityType: "participant", EntityID: p.NormalizedName,
			LessonID: &id, GroupID: &gid, PreviousValue: "«" + p.DisplayName + "»: " + prevText,
			NewValue: "«" + p.DisplayName + "»: " + next, Reason: reason,
		})
	}
	// Rebuilt even for the same decision: repeating it is how a teacher retries
	// after a failed rebuild.
	return s.lessonRebuildAndRespond(w, r, id)
}

// errLessonIgnoreListFull aborts a mapping whose "ignore always" would push the
// ignore list over the limit PUT /api/settings enforces.
var errLessonIgnoreListFull = errors.New("ignored names list is full")

// lessonRebuildAndRespond finishes a saved teacher decision: it rebuilds the
// projection and answers with the fresh LessonView. When the rebuild fails the
// decision stays saved (repeating it retries), open screens are still poked, and
// the answer is a 500 — a stale view would claim the time had moved.
func (s *Server) lessonRebuildAndRespond(w http.ResponseWriter, r *http.Request, id int64) error {
	if err := s.lessonRebuild(r, id); err != nil {
		s.lessonPublish(id)
		return errInternal(err)
	}
	s.lessonPublish(id)
	return s.writeLessonView(w, r, id)
}

func lessonSameID(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// handleLessonUnmapParticipant: DELETE /api/lessons/{id}/participants/{participant_id}/map
// → LessonView. Removes the manual decision (automatic matching applies again);
// without a decision it changes nothing (no audit entry). When the decision
// mapped the name to a student, that student's alias equal to the name (what
// "Запомнить это имя" stored) is removed in the same transaction — otherwise
// the name would match the same student again automatically, instead of
// going back to "Не идентифицированы". The group's other lessons are then
// refreshed in the background, since they lose that alias too.
func (s *Server) handleLessonUnmapParticipant(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	pid, err := pathID(r, "participant_id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	l, p, err := s.lessonParticipant(ctx, id, pid)
	if err != nil {
		return err
	}
	m, err := s.Store.GetMapping(ctx, id, p.NormalizedName)
	if errors.Is(err, store.ErrNotFound) {
		// Nothing to undo (a second click): no audit entry, but still rebuild so
		// that repeating the request retries a rebuild that failed the first time.
		return s.lessonRebuildAndRespond(w, r, id)
	} else if err != nil {
		return errInternal(err)
	}
	prevText := s.lessonDescribeDecision(ctx, p, m)
	var (
		alias   *model.Alias
		student *model.Student
	)
	err = s.Store.InTx(ctx, func(tx *store.Store) error {
		alias, student = nil, nil
		// Re-read inside the transaction: a decision changed meanwhile (another
		// tab) must not cost a different student's alias.
		cur, err := tx.GetMapping(ctx, id, p.NormalizedName)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		if err := tx.DeleteMapping(ctx, id, p.NormalizedName); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if cur.Action != model.MappingStudent || cur.StudentID == nil || p.NormalizedName == "" {
			return nil
		}
		st, err := tx.GetStudent(ctx, *cur.StudentID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		for _, a := range st.Aliases {
			if a.NormalizedAlias != p.NormalizedName {
				continue
			}
			removed, err := tx.DeleteAlias(ctx, st.ID, a.ID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			alias, student = removed, st
			break
		}
		return nil
	})
	if err != nil {
		return errInternal(err)
	}
	gid := l.GroupID
	next := "«" + p.DisplayName + "»: сопоставляется автоматически"
	if alias != nil {
		next += "; запомненное имя удалено у студента " + lessonStudentRef(student.FullName, student.ID)
		s.audit(r, model.AuditEntry{
			Action: model.AuditAliasDelete, EntityType: "student", EntityID: strconv.FormatInt(student.ID, 10),
			LessonID: &id, GroupID: &student.GroupID,
			PreviousValue: "«" + alias.Alias + "» → " + lessonStudentRef(student.FullName, student.ID),
			Reason:        "Отменено сопоставление участника на занятии",
		})
	}
	s.audit(r, model.AuditEntry{
		Action: model.AuditParticipantUnmap, EntityType: "participant", EntityID: p.NormalizedName,
		LessonID: &id, GroupID: &gid, PreviousValue: "«" + p.DisplayName + "»: " + prevText, NewValue: next,
	})
	if alias != nil {
		// The group's other lessons matched by this alias too; this one is
		// rebuilt below and answered with.
		groupID := student.GroupID
		s.goBackground(grpRefreshTimeout, func(ctx context.Context) { s.refreshGroupLessons(ctx, groupID) })
	}
	return s.lessonRebuildAndRespond(w, r, id)
}

// lessonParticipant loads a lesson and one of its participants (404 otherwise).
func (s *Server) lessonParticipant(ctx context.Context, lessonID, participantID int64) (*model.Lesson, *model.Participant, error) {
	l, err := s.Store.GetLesson(ctx, lessonID)
	if err != nil {
		return nil, nil, storeErr(err, lessonNotFoundMsg)
	}
	p, err := s.Store.GetParticipant(ctx, participantID)
	if err != nil || p.LessonID != lessonID {
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, nil, errInternal(err)
		}
		return nil, nil, errNotFound("Участник Zoom не найден на этом занятии. Обновите страницу.")
	}
	return l, p, nil
}

// lessonAliasTakenErr explains why the name cannot be remembered (409).
func (s *Server) lessonAliasTakenErr(ctx context.Context, displayName string, nt *store.NameTakenError) error {
	name := strings.TrimSpace(displayName)
	msg := "Не удалось запомнить имя «" + name + "»: оно уже используется в группе."
	if nt.StudentID > 0 {
		if owner, err := s.Store.GetStudent(ctx, nt.StudentID); err == nil {
			msg = "Не удалось запомнить имя «" + name + "»: оно уже закреплено за студентом «" + owner.FullName + "»."
		}
	}
	msg += " Сопоставление не сохранено. Снимите отметку «Запомнить это имя» или выберите другого студента."
	return &APIError{Status: http.StatusConflict, Code: CodeConflict, Message: msg, Err: nt,
		Details: map[string]any{"name": nt.Name, "student_id": nt.StudentID, "is_alias": nt.IsAlias}}
}

// lessonAddIgnoredName appends name to the ignore list unless an equal
// (normalized) name is already there.
func lessonAddIgnoredName(set *model.Settings, name string) bool {
	name = strings.TrimSpace(name)
	n := names.Normalize(name)
	if n == "" {
		return false
	}
	for _, x := range set.IgnoredNames {
		if names.Normalize(x) == n {
			return false
		}
	}
	set.IgnoredNames = append(set.IgnoredNames, name)
	return true
}

// lessonDescribeMapping is the audit text of a teacher decision.
func lessonDescribeMapping(action model.MappingAction, st *model.Student) string {
	switch action {
	case model.MappingStudent:
		if st == nil {
			return "студент"
		}
		return "студент " + lessonStudentRef(st.FullName, st.ID)
	case model.MappingIgnore:
		return "не студент (скрыт)"
	case model.MappingNone:
		return "не сопоставлять автоматически"
	}
	return string(action)
}

// lessonDescribeDecision describes how a participant is resolved now: the
// teacher's decision m when there is one, else the automatic result.
func (s *Server) lessonDescribeDecision(ctx context.Context, p *model.Participant, m *model.Mapping) string {
	studentRef := func(id *int64) *model.Student {
		if id == nil {
			return nil
		}
		st, err := s.Store.GetStudent(ctx, *id)
		if err != nil {
			return &model.Student{ID: *id, FullName: fmt.Sprintf("Студент №%d", *id)}
		}
		return st
	}
	if m != nil {
		return lessonDescribeMapping(m.Action, studentRef(m.StudentID))
	}
	switch p.Resolution {
	case model.ResolutionMatched:
		return "автоматически: " + lessonDescribeMapping(model.MappingStudent, studentRef(p.StudentID))
	case model.ResolutionManual:
		return lessonDescribeMapping(model.MappingStudent, studentRef(p.StudentID))
	case model.ResolutionConflict:
		return "автоматически: подходит нескольким студентам"
	case model.ResolutionIgnored:
		switch p.IgnoreReason {
		case model.IgnoreList:
			return "автоматически: скрыт (имя в списке скрываемых)"
		case model.IgnoreHost:
			return "автоматически: скрыт (организатор конференции)"
		}
		return "скрыт"
	}
	return "автоматически: не идентифицирован"
}

// ---- attendance status / comment ----

// handleLessonUpdateAttendance: PATCH /api/lessons/{id}/attendance/{student_id}
// {status?, comment?, reason?} → LessonView. "status": null returns the student to
// the automatic status; a missing "status" key leaves it unchanged.
func (s *Server) handleLessonUpdateAttendance(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	sid, err := pathID(r, "student_id")
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := decodeJSON(w, r, &raw); err != nil {
		return err
	}
	var (
		setStatus, setComment bool
		status                *model.FinalStatus
		comment, reason       string
	)
	if v, ok := raw["status"]; ok {
		setStatus = true
		if !lessonIsJSONNull(v) {
			var sv string
			if err := json.Unmarshal(v, &sv); err != nil {
				return errValidation("Неизвестный статус", map[string]any{"field": "status"})
			}
			fs := model.FinalStatus(strings.TrimSpace(sv))
			if fs != model.FinalPresent && fs != model.FinalAbsent {
				return errValidation("Неизвестный статус: выберите «Присутствовал» или «Отсутствовал»", map[string]any{"field": "status"})
			}
			status = &fs
		}
	}
	if v, ok := raw["comment"]; ok {
		setComment = true
		if !lessonIsJSONNull(v) {
			if err := json.Unmarshal(v, &comment); err != nil {
				return errValidation("Комментарий должен быть текстом", map[string]any{"field": "comment"})
			}
		}
		comment = strings.TrimSpace(comment)
		if utf8.RuneCountInString(comment) > lessonMaxComment {
			return errValidation(fmt.Sprintf("Комментарий слишком длинный: не больше %d символов", lessonMaxComment),
				map[string]any{"field": "comment"})
		}
	}
	if v, ok := raw["reason"]; ok && !lessonIsJSONNull(v) {
		if err := json.Unmarshal(v, &reason); err != nil {
			return errValidation("Причина должна быть текстом", map[string]any{"field": "reason"})
		}
	}
	if reason, err = lessonValidReason(reason); err != nil {
		return err
	}
	if !setStatus && !setComment {
		return errValidation("Нечего изменять: укажите статус или комментарий", nil)
	}

	ctx := r.Context()
	l, err := s.Store.GetLesson(ctx, id)
	if err != nil {
		return storeErr(err, lessonNotFoundMsg)
	}
	row, err := s.lessonAttendanceRow(r, l, sid)
	if err != nil {
		return err
	}

	statusChanged := setStatus && !lessonSameStatus(row.ManualStatus, status)
	commentChanged := setComment && comment != row.Comment
	if !statusChanged && !commentChanged {
		return s.writeLessonView(w, r, id)
	}
	// Both changes of one request are saved together or not at all.
	var upd *model.Attendance
	now := s.now()
	err = s.Store.InTx(ctx, func(tx *store.Store) error {
		if statusChanged {
			a, err := tx.SetManualStatus(ctx, id, sid, status, userIDPtr(r), now)
			if err != nil {
				return err
			}
			upd = a
		}
		if commentChanged {
			if _, err := tx.SetAttendanceComment(ctx, id, sid, comment, userIDPtr(r), now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return storeErr(err, "Студента нет в списке этого занятия")
	}
	gid := l.GroupID
	sidText := strconv.FormatInt(sid, 10)
	if statusChanged {
		s.audit(r, model.AuditEntry{
			Action: model.AuditAttendanceStatus, EntityType: "attendance", EntityID: sidText, LessonID: &id, GroupID: &gid,
			PreviousValue: lessonDescribeStatus(row), NewValue: lessonDescribeStatus(upd), Reason: reason,
		})
	}
	if commentChanged {
		s.audit(r, model.AuditEntry{
			Action: model.AuditAttendanceComment, EntityType: "attendance", EntityID: sidText, LessonID: &id, GroupID: &gid,
			PreviousValue: row.Comment, NewValue: comment, Reason: reason,
		})
	}
	s.lessonPublish(id)
	return s.writeLessonView(w, r, id)
}

// lessonAttendanceRow returns the attendance row of a student in a lesson. The
// live screen also shows active students that have no row yet (see
// assembleLessonView); for them the projection is rebuilt and, if the row is
// still missing (e.g. the rebuild failed), a default "not seen / absent" row is
// created so the teacher's change can be stored.
func (s *Server) lessonAttendanceRow(r *http.Request, l *model.Lesson, sid int64) (*model.Attendance, error) {
	ctx := r.Context()
	row, err := s.Store.GetAttendance(ctx, l.ID, sid)
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, errInternal(err)
	}
	st, err := s.Store.GetStudent(ctx, sid)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errNotFound("Студент не найден")
	} else if err != nil {
		return nil, errInternal(err)
	}
	if st.GroupID != l.GroupID {
		return nil, errNotFound("Этот студент не из группы занятия")
	}
	notListed := errNotFound("Студента нет в списке этого занятия")
	if st.Status != model.StatusActive {
		return nil, notListed
	}
	s.lessonRebuildLogged(r, l.ID)
	if row, err = s.Store.GetAttendance(ctx, l.ID, sid); err == nil {
		return row, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, errInternal(err)
	}
	// Same rule as assembleLessonView: a finished lesson with rows has a frozen roster.
	cur, err := s.Store.GetLesson(ctx, l.ID)
	if err != nil {
		return nil, storeErr(err, lessonNotFoundMsg)
	}
	if cur.Status == model.LessonFinished {
		rows, err := s.Store.ListAttendance(ctx, l.ID)
		if err != nil {
			return nil, errInternal(err)
		}
		if len(rows) > 0 {
			return nil, notListed
		}
	}
	if _, err := s.Store.UpsertAttendanceComputed(ctx, l.ID, []model.Attendance{{
		StudentID: sid, CurrentStatus: model.CurrentNotSeen, AutoStatus: model.FinalAbsent, FinalStatus: model.FinalAbsent,
	}}); err != nil {
		return nil, errInternal(err)
	}
	row, err = s.Store.GetAttendance(ctx, l.ID, sid)
	if err != nil {
		return nil, storeErr(err, "Студента нет в списке этого занятия")
	}
	return row, nil
}

func lessonSameStatus(a, b *model.FinalStatus) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// lessonDescribeStatus is the audit text of a student's final status.
func lessonDescribeStatus(a *model.Attendance) string {
	word := func(s model.FinalStatus) string {
		if s == model.FinalPresent {
			return "Присутствовал"
		}
		return "Отсутствовал"
	}
	if a.ManualStatus != nil {
		return word(*a.ManualStatus) + " (вручную)"
	}
	return word(a.AutoStatus) + " (автоматически)"
}

func lessonValidReason(v string) (string, error) {
	v = strings.TrimSpace(v)
	if utf8.RuneCountInString(v) > lessonMaxReason {
		return "", errValidation(fmt.Sprintf("Причина слишком длинная: не больше %d символов", lessonMaxReason),
			map[string]any{"field": "reason"})
	}
	return v, nil
}

func lessonIsJSONNull(v json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(v), []byte("null"))
}
