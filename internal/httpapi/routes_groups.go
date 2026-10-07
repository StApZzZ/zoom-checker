package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"zoom-checker/internal/importer"
	"zoom-checker/internal/model"
	"zoom-checker/internal/names"
	"zoom-checker/internal/realtime"
	"zoom-checker/internal/store"
)

// Group, student and alias management, list import and the group report
// (docs/architecture.md §6). Students and aliases change how Zoom names are
// matched, so every such change rebuilds the group's lessons that are not
// finished yet (refreshGroupLessons).

// Limits of user-entered group/student fields (runes, after trimming).
const (
	grpGroupNameMaxLen        = 100
	grpGroupDescriptionMaxLen = 2000
	grpGroupDurationMin       = 15
	grpGroupDurationMax       = 600
	grpStudentNameMaxLen      = 200
	grpStudentExternalIDMax   = 100
)

const (
	grpMsgGroupNotFound   = "Группа не найдена"
	grpMsgStudentNotFound = "Студент не найден"
	grpMsgAliasNotFound   = "Дополнительное имя не найдено"
	grpMsgGroupNameTaken  = "Группа с таким названием уже есть"
	// grpMsgDuplicate is the TZ §8 text for names that collide in a group.
	grpMsgDuplicate = importer.MsgDuplicate
)

func (s *Server) registerGroups(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/groups", s.authed(s.handleListGroups))
	mux.HandleFunc("POST /api/groups", s.authed(s.handleCreateGroup))
	mux.HandleFunc("GET /api/groups/{id}", s.authed(s.handleGetGroup))
	mux.HandleFunc("PATCH /api/groups/{id}", s.authed(s.handleUpdateGroup))

	mux.HandleFunc("POST /api/groups/{id}/students", s.authed(s.handleCreateStudent))
	mux.HandleFunc("PATCH /api/students/{id}", s.authed(s.handleUpdateStudent))
	mux.HandleFunc("POST /api/students/{id}/aliases", s.authed(s.handleAddAlias))
	mux.HandleFunc("DELETE /api/students/{id}/aliases/{alias_id}", s.authed(s.handleDeleteAlias))

	mux.HandleFunc("POST /api/groups/{id}/import/preview", s.authed(s.handleImportPreview))
	mux.HandleFunc("POST /api/groups/{id}/import/commit", s.authed(s.handleImportCommit))

	mux.HandleFunc("GET /api/groups/{id}/report", s.authed(s.handleGroupReport))
	mux.HandleFunc("GET /api/groups/{id}/report/export", s.authed(s.handleGroupReportExport))
}

// ---------------------------------------------------------------------------
// Groups
// ---------------------------------------------------------------------------

type groupCreateRequest struct {
	Name                   string  `json:"name"`
	Description            *string `json:"description"`
	DefaultDurationMinutes *int    `json:"default_duration_minutes"`
}

type groupUpdateRequest struct {
	Name                   *string       `json:"name"`
	Description            *string       `json:"description"`
	Status                 *model.Status `json:"status"`
	DefaultDurationMinutes *int          `json:"default_duration_minutes"`
}

func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) error {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	switch status {
	case "":
		status = store.GroupFilterActive
	case store.GroupFilterActive, store.GroupFilterArchived, store.GroupFilterAll:
	default:
		return errValidation("Некорректный фильтр групп: ожидается active, archived или all", map[string]any{"field": "status"})
	}
	groups, err := s.Store.ListGroups(r.Context(), status, s.now())
	if err != nil {
		return storeErr(err, "")
	}
	out := make([]GroupSummaryDTO, 0, len(groups))
	for _, g := range groups {
		out = append(out, toGroupSummaryDTO(g))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) error {
	var req groupCreateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	g := &model.Group{Status: model.StatusActive, DefaultDurationMinutes: store.DefaultGroupDurationMinutes}
	var err error
	if g.Name, err = cleanGroupName(req.Name); err != nil {
		return err
	}
	if req.Description != nil {
		if g.Description, err = cleanGroupDescription(*req.Description); err != nil {
			return err
		}
	}
	if req.DefaultDurationMinutes != nil {
		if err := checkGroupDuration(*req.DefaultDurationMinutes); err != nil {
			return err
		}
		g.DefaultDurationMinutes = *req.DefaultDurationMinutes
	}
	if err := s.Store.CreateGroup(r.Context(), g); err != nil {
		return groupWriteErr(err)
	}
	s.audit(r, model.AuditEntry{
		Action: model.AuditGroupCreate, EntityType: "group", EntityID: grpID(g.ID), GroupID: &g.ID,
		NewValue: grpAuditJSON(map[string]any{"name": g.Name, "description": g.Description,
			"default_duration_minutes": g.DefaultDurationMinutes}),
	})
	sum, err := s.Store.GetGroupSummary(r.Context(), g.ID, s.now())
	if err != nil {
		return storeErr(err, grpMsgGroupNotFound)
	}
	s.grpPublish(realtime.TopicLessons)
	writeJSON(w, http.StatusCreated, toGroupSummaryDTO(*sum))
	return nil
}

func (s *Server) handleGetGroup(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return errNotFound(grpMsgGroupNotFound)
	}
	sum, err := s.Store.GetGroupSummary(r.Context(), id, s.now())
	if err != nil {
		return storeErr(err, grpMsgGroupNotFound)
	}
	students, err := s.Store.ListStudents(r.Context(), id, true)
	if err != nil {
		return storeErr(err, grpMsgGroupNotFound)
	}
	out := GroupDetailDTO{GroupSummaryDTO: toGroupSummaryDTO(*sum), Students: make([]StudentDTO, 0, len(students))}
	for i := range students {
		out.Students = append(out.Students, toStudentDTO(&students[i]))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) handleUpdateGroup(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return errNotFound(grpMsgGroupNotFound)
	}
	var req groupUpdateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	g, err := s.Store.GetGroup(r.Context(), id)
	if err != nil {
		return storeErr(err, grpMsgGroupNotFound)
	}
	prev := *g
	if req.Name != nil {
		if g.Name, err = cleanGroupName(*req.Name); err != nil {
			return err
		}
	}
	if req.Description != nil {
		if g.Description, err = cleanGroupDescription(*req.Description); err != nil {
			return err
		}
	}
	if req.Status != nil {
		if *req.Status != model.StatusActive && *req.Status != model.StatusArchived {
			return errValidation("Неизвестный статус группы: ожидается active или archived", map[string]any{"field": "status"})
		}
		g.Status = *req.Status
	}
	if req.DefaultDurationMinutes != nil {
		if err := checkGroupDuration(*req.DefaultDurationMinutes); err != nil {
			return err
		}
		g.DefaultDurationMinutes = *req.DefaultDurationMinutes
	}

	before, after := map[string]any{}, map[string]any{}
	diff := func(key string, old, new any) {
		if old != new {
			before[key], after[key] = old, new
		}
	}
	diff("name", prev.Name, g.Name)
	diff("description", prev.Description, g.Description)
	diff("status", prev.Status, g.Status)
	diff("default_duration_minutes", prev.DefaultDurationMinutes, g.DefaultDurationMinutes)
	if len(after) > 0 {
		if err := s.Store.UpdateGroup(r.Context(), g); err != nil {
			return groupWriteErr(err)
		}
		s.audit(r, model.AuditEntry{
			Action: model.AuditGroupUpdate, EntityType: "group", EntityID: grpID(id), GroupID: &id,
			PreviousValue: grpAuditJSON(before), NewValue: grpAuditJSON(after),
		})
		// The group name and status are shown on lesson screens and lists.
		_, renamed := after["name"]
		_, restatus := after["status"]
		if renamed || restatus {
			s.pokeGroupLessons(r.Context(), id)
		}
	}
	sum, err := s.Store.GetGroupSummary(r.Context(), id, s.now())
	if err != nil {
		return storeErr(err, grpMsgGroupNotFound)
	}
	writeJSON(w, http.StatusOK, toGroupSummaryDTO(*sum))
	return nil
}

func groupWriteErr(err error) error {
	switch {
	case errors.Is(err, store.ErrConflict):
		return &APIError{Status: http.StatusConflict, Code: CodeConflict, Message: grpMsgGroupNameTaken,
			Details: map[string]any{"field": "name"}, Err: err}
	case errors.Is(err, store.ErrInvalid):
		return &APIError{Status: http.StatusUnprocessableEntity, Code: CodeValidation, Message: "Введите название группы",
			Details: map[string]any{"field": "name"}, Err: err}
	}
	return storeErr(err, grpMsgGroupNotFound)
}

func cleanGroupName(raw string) (string, error) {
	name := grpCollapseSpaces(raw)
	if names.Normalize(name) == "" {
		return "", errValidation("Введите название группы", map[string]any{"field": "name"})
	}
	if utf8.RuneCountInString(name) > grpGroupNameMaxLen {
		return "", errValidation(fmt.Sprintf("Название группы слишком длинное: не больше %d символов", grpGroupNameMaxLen),
			map[string]any{"field": "name"})
	}
	return name, nil
}

func cleanGroupDescription(raw string) (string, error) {
	d := grpCleanMultiline(raw)
	if utf8.RuneCountInString(d) > grpGroupDescriptionMaxLen {
		return "", errValidation(fmt.Sprintf("Описание слишком длинное: не больше %d символов", grpGroupDescriptionMaxLen),
			map[string]any{"field": "description"})
	}
	return d, nil
}

func checkGroupDuration(m int) error {
	if m < grpGroupDurationMin || m > grpGroupDurationMax {
		return errValidation(fmt.Sprintf("Длительность занятия должна быть от %d до %d минут", grpGroupDurationMin, grpGroupDurationMax),
			map[string]any{"field": "default_duration_minutes"})
	}
	return nil
}

// ---------------------------------------------------------------------------
// Shared helpers of the group routes
// ---------------------------------------------------------------------------

// grpCollapseSpaces cleans a one-line value (name, alias, ID) the way the
// importer does: invisible format characters (zero-width spaces, BOM, bidi
// marks) and control characters are dropped, every whitespace run becomes
// one space, and the ends are trimmed. Control characters matter beyond
// looks: PostgreSQL text cannot hold NUL.
func grpCollapseSpaces(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// grpCleanMultiline cleans free text (a description): line breaks and tabs
// stay, other control and invisible format characters are dropped, Windows
// line ends become "\n", and the ends are trimmed.
func grpCleanMultiline(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r == '\r':
			return '\n'
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

func grpID(id int64) string { return strconv.FormatInt(id, 10) }

// grpAuditJSON renders an audit value; maps are written with sorted keys.
func grpAuditJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// publish pokes realtime subscribers (no-op without a broker).
func (s *Server) grpPublish(topics ...string) {
	if s.Broker != nil && len(topics) > 0 {
		s.Broker.Publish(topics...)
	}
}

const (
	// grpRefreshTimeout bounds the lesson rebuilds that follow a roster change.
	grpRefreshTimeout = 30 * time.Second
	// grpStalePlannedAge: a lesson still "planned" (no Zoom session ever came)
	// whose planned end is longer ago than this is stale (lessonStalePlanned).
	grpStalePlannedAge = 24 * time.Hour
)

// lessonStalePlanned reports whether l is still "planned" (no Zoom session
// ever came) although its planned end is more than grpStalePlannedAge before
// now: it was cancelled or held elsewhere. Students who joined the group later
// must not appear in it as absent, so it is not rebuilt after roster or
// settings changes (with no participants a rebuild could only add them) and
// its view adds no rows for them.
func lessonStalePlanned(l *model.Lesson, now time.Time) bool {
	return l.Status == model.LessonPlanned && l.PlannedEndAt.Before(now.Add(-grpStalePlannedAge))
}

// refreshGroupLessons re-matches Zoom names after the roster or aliases of a
// group changed: every lesson of the group that is not finished is rebuilt
// (the processor publishes its live screen) — except stale planned lessons
// (grpStalePlannedAge) — and all lesson screens of the group are poked because
// they show student names. Failures are logged — the change itself is already
// saved and the next rebuild picks it up.
func (s *Server) refreshGroupLessons(ctx context.Context, groupID int64) {
	// Finish the rebuilds even if the client goes away mid-request (but not
	// when the server shuts down).
	ctx, release := s.detach(ctx, grpRefreshTimeout)
	defer release()
	lessons, err := s.Store.ListLessons(ctx, store.LessonFilter{GroupID: &groupID})
	if err != nil {
		s.Log.Error("list group lessons for rebuild", "group_id", groupID, "err", err)
		s.grpPublish(realtime.TopicLessons)
		return
	}
	now := s.now()
	topics := make([]string, 0, len(lessons)+1)
	for i := range lessons {
		l := &lessons[i]
		topics = append(topics, realtime.LessonTopic(l.ID))
		if l.Status == model.LessonFinished || s.Processor == nil || lessonStalePlanned(l, now) {
			continue
		}
		if err := s.Processor.RebuildLesson(ctx, l.ID); err != nil {
			s.Log.Error("rebuild lesson after roster change", "group_id", groupID, "lesson_id", l.ID, "err", err)
		}
	}
	s.grpPublish(append(topics, realtime.TopicLessons)...)
}

// pokeGroupLessons tells open screens of the group's lessons (and lesson
// lists) to re-read, without rebuilding anything.
func (s *Server) pokeGroupLessons(ctx context.Context, groupID int64) {
	topics := []string{realtime.TopicLessons}
	lessons, err := s.Store.ListLessons(ctx, store.LessonFilter{GroupID: &groupID})
	if err != nil {
		s.Log.Error("list group lessons", "group_id", groupID, "err", err)
	}
	for _, l := range lessons {
		topics = append(topics, realtime.LessonTopic(l.ID))
	}
	s.grpPublish(topics...)
}

// grpDecodeJSONLimit is decodeJSON with a custom body limit (import commits can
// be larger than ordinary forms).
func grpDecodeJSONLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) error {
	ct := r.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(ct, "application/json") {
		return &APIError{Status: http.StatusUnsupportedMediaType, Code: CodeBadRequest, Message: "Ожидается JSON"}
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		switch {
		case errors.Is(err, io.EOF):
			return errBadRequest("Пустой запрос")
		case errors.As(err, &mbe):
			return &APIError{Status: http.StatusRequestEntityTooLarge, Code: CodeBadRequest, Message: "Слишком большой запрос"}
		}
		return &APIError{Status: http.StatusBadRequest, Code: CodeBadRequest, Message: "Некорректный формат запроса", Err: err}
	}
	return nil
}
