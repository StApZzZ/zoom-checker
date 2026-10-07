package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"unicode/utf8"

	"zoom-checker/internal/model"
	"zoom-checker/internal/names"
	"zoom-checker/internal/store"
)

type studentCreateRequest struct {
	FullName   string `json:"full_name"`
	ExternalID string `json:"external_id"`
}

type studentUpdateRequest struct {
	FullName   *string       `json:"full_name"`
	ExternalID *string       `json:"external_id"`
	Status     *model.Status `json:"status"`
}

type aliasCreateRequest struct {
	Alias string `json:"alias"`
}

func (s *Server) handleCreateStudent(w http.ResponseWriter, r *http.Request) error {
	groupID, err := pathID(r, "id")
	if err != nil {
		return errNotFound(grpMsgGroupNotFound)
	}
	var req studentCreateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	st := &model.Student{GroupID: groupID, Status: model.StatusActive}
	if st.FullName, err = cleanStudentName(req.FullName, "full_name"); err != nil {
		return err
	}
	if st.ExternalID, err = cleanExternalID(req.ExternalID); err != nil {
		return err
	}
	if err := s.checkExternalIDFree(r.Context(), st); err != nil {
		return err
	}
	if err := s.Store.CreateStudent(r.Context(), st); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errNotFound(grpMsgGroupNotFound)
		}
		return s.studentNameErr(r.Context(), err, st, false)
	}
	st.Aliases = []model.Alias{}
	s.audit(r, model.AuditEntry{
		Action: model.AuditStudentCreate, EntityType: "student", EntityID: grpID(st.ID), GroupID: &groupID,
		NewValue: grpAuditJSON(map[string]any{"full_name": st.FullName, "external_id": st.ExternalID}),
	})
	s.refreshGroupLessons(r.Context(), groupID)
	writeJSON(w, http.StatusCreated, toStudentDTO(st))
	return nil
}

func (s *Server) handleUpdateStudent(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return errNotFound(grpMsgStudentNotFound)
	}
	var req studentUpdateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	st, err := s.Store.GetStudent(r.Context(), id)
	if err != nil {
		return storeErr(err, grpMsgStudentNotFound)
	}
	prev := *st
	if req.FullName != nil {
		if st.FullName, err = cleanStudentName(*req.FullName, "full_name"); err != nil {
			return err
		}
	}
	if req.ExternalID != nil {
		if st.ExternalID, err = cleanExternalID(*req.ExternalID); err != nil {
			return err
		}
	}
	if req.Status != nil {
		if *req.Status != model.StatusActive && *req.Status != model.StatusArchived {
			return errValidation("Неизвестный статус студента: ожидается active или archived", map[string]any{"field": "status"})
		}
		st.Status = *req.Status
	}

	before, after := map[string]any{}, map[string]any{}
	if prev.FullName != st.FullName {
		before["full_name"], after["full_name"] = prev.FullName, st.FullName
	}
	if prev.ExternalID != st.ExternalID {
		before["external_id"], after["external_id"] = prev.ExternalID, st.ExternalID
	}
	if prev.Status != st.Status {
		before["status"], after["status"] = prev.Status, st.Status
	}
	if len(after) == 0 {
		writeJSON(w, http.StatusOK, toStudentDTO(st))
		return nil
	}
	if _, changed := after["external_id"]; changed {
		if err := s.checkExternalIDFree(r.Context(), st); err != nil {
			return err
		}
	}
	restoring := prev.Status == model.StatusArchived && st.Status == model.StatusActive

	// Renaming a student to one of its own aliases: the alias becomes the
	// name, so it is dropped in the same transaction (otherwise the store
	// would refuse the name as taken by that alias).
	var dropped []model.Alias
	err = s.Store.InTx(r.Context(), func(tx *store.Store) error {
		dropped = dropped[:0]
		n := names.Normalize(st.FullName)
		for _, a := range prev.Aliases {
			if a.NormalizedAlias != n {
				continue
			}
			if _, err := tx.DeleteAlias(r.Context(), st.ID, a.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			dropped = append(dropped, a)
		}
		return tx.UpdateStudent(r.Context(), st)
	})
	if err != nil {
		return s.studentNameErr(r.Context(), err, st, restoring)
	}
	gid := st.GroupID
	for _, a := range dropped {
		s.audit(r, model.AuditEntry{
			Action: model.AuditAliasDelete, EntityType: "student", EntityID: grpID(st.ID), GroupID: &gid,
			PreviousValue: a.Alias, Reason: "Дополнительное имя стало основным именем студента",
		})
	}
	s.audit(r, model.AuditEntry{
		Action: model.AuditStudentUpdate, EntityType: "student", EntityID: grpID(st.ID), GroupID: &gid,
		PreviousValue: grpAuditJSON(before), NewValue: grpAuditJSON(after),
	})
	s.refreshGroupLessons(r.Context(), gid)

	fresh, err := s.Store.GetStudent(r.Context(), st.ID)
	if err != nil {
		return storeErr(err, grpMsgStudentNotFound)
	}
	writeJSON(w, http.StatusOK, toStudentDTO(fresh))
	return nil
}

func (s *Server) handleAddAlias(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return errNotFound(grpMsgStudentNotFound)
	}
	var req aliasCreateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	alias, err := cleanStudentName(req.Alias, "alias")
	if err != nil {
		return err
	}
	st, err := s.Store.GetStudent(r.Context(), id)
	if err != nil {
		return storeErr(err, grpMsgStudentNotFound)
	}
	n := names.Normalize(alias)
	if n == st.NormalizedName {
		return errValidation("Это и так основное имя студента. Дополнительное имя должно отличаться от него.", map[string]any{"field": "alias"})
	}
	for _, a := range st.Aliases {
		if a.NormalizedAlias == n {
			return errConflict(fmt.Sprintf("У студента уже есть дополнительное имя «%s».", a.Alias),
				map[string]any{"field": "alias", "alias_id": a.ID})
		}
	}
	a := &model.Alias{StudentID: st.ID, Alias: alias, CreatedBy: userIDPtr(r)}
	if err := s.Store.AddAlias(r.Context(), a); err != nil {
		return s.studentNameErr(r.Context(), err, st, false)
	}
	gid := st.GroupID
	s.audit(r, model.AuditEntry{
		Action: model.AuditAliasAdd, EntityType: "student", EntityID: grpID(st.ID), GroupID: &gid,
		NewValue: a.Alias,
	})
	s.refreshGroupLessons(r.Context(), gid)
	writeJSON(w, http.StatusCreated, AliasDTO{ID: a.ID, Alias: a.Alias})
	return nil
}

func (s *Server) handleDeleteAlias(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return errNotFound(grpMsgStudentNotFound)
	}
	aliasID, err := pathID(r, "alias_id")
	if err != nil {
		return errNotFound(grpMsgAliasNotFound)
	}
	st, err := s.Store.GetStudent(r.Context(), id)
	if err != nil {
		return storeErr(err, grpMsgStudentNotFound)
	}
	a, err := s.Store.DeleteAlias(r.Context(), st.ID, aliasID)
	if err != nil {
		return storeErr(err, grpMsgAliasNotFound)
	}
	gid := st.GroupID
	s.audit(r, model.AuditEntry{
		Action: model.AuditAliasDelete, EntityType: "student", EntityID: grpID(st.ID), GroupID: &gid,
		PreviousValue: a.Alias,
	})
	s.refreshGroupLessons(r.Context(), gid)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// studentNameErr explains a name clash in plain words, naming the student
// that already uses the name when it is known (TZ §8). st is the student
// being created or changed; restoring tells that the change brings it back
// from the archive, where the clash is not about a name the teacher typed.
// Other errors go through storeErr.
func (s *Server) studentNameErr(ctx context.Context, err error, st *model.Student, restoring bool) error {
	var nt *store.NameTakenError
	if !errors.As(err, &nt) || nt.StudentID == 0 || nt.StudentID == st.ID {
		return storeErr(err, grpMsgStudentNotFound)
	}
	owner, oerr := s.Store.GetStudent(ctx, nt.StudentID)
	if oerr != nil {
		return storeErr(err, grpMsgStudentNotFound)
	}
	who := "«" + owner.FullName + "»"
	if owner.Status == model.StatusArchived {
		who += " (в архиве группы)" // not in the list the teacher is looking at
	}
	how := "так зовут студента " + who
	if nt.IsAlias {
		how = "это дополнительное имя студента " + who
	}
	var msg string
	switch {
	case !restoring:
		msg = fmt.Sprintf("Это имя уже есть в группе: %s. %s", how, grpMsgDuplicate)
	case nt.Name == names.Normalize(st.FullName):
		msg = fmt.Sprintf("Не получилось вернуть «%s» из архива: %s. %s Сначала измените имя одного из них.",
			st.FullName, how, grpMsgDuplicate)
	default:
		alias := nt.Name
		for _, a := range st.Aliases {
			if a.NormalizedAlias == nt.Name {
				alias = a.Alias
			}
		}
		msg = fmt.Sprintf("Не получилось вернуть «%s» из архива: дополнительное имя «%s» уже занято — %s. Удалите это дополнительное имя в карточке «%s» и попробуйте ещё раз.",
			st.FullName, alias, how, st.FullName)
	}
	return &APIError{Status: http.StatusConflict, Code: CodeConflict, Message: msg,
		Details: map[string]any{"name": nt.Name, "student_id": nt.StudentID, "student_name": owner.FullName, "is_alias": nt.IsAlias},
		Err:     err}
}

// checkExternalIDFree refuses an ID that another active student of the group
// already has: imports find students by ID first, so a shared ID would attach
// a list row to the wrong person. Students without an ID are not checked.
func (s *Server) checkExternalIDFree(ctx context.Context, st *model.Student) error {
	if st.ExternalID == "" {
		return nil
	}
	students, err := s.Store.ListStudents(ctx, st.GroupID, false)
	if err != nil {
		return storeErr(err, grpMsgGroupNotFound)
	}
	for _, o := range students {
		if o.ID != st.ID && o.ExternalID == st.ExternalID {
			return errValidation(fmt.Sprintf("ID «%s» уже записан у студента «%s». У каждого студента должен быть свой ID — проверьте его или оставьте поле пустым.", st.ExternalID, o.FullName),
				map[string]any{"field": "external_id", "student_id": o.ID, "student_name": o.FullName})
		}
	}
	return nil
}

// cleanStudentName validates a full name or alias: 1..200 characters after
// trimming (inner whitespace runs collapse to one space).
func cleanStudentName(raw, field string) (string, error) {
	name := grpCollapseSpaces(raw)
	if names.Normalize(name) == "" {
		msg := "Введите фамилию и имя студента"
		if field == "alias" {
			msg = "Введите дополнительное имя — так, как студент подписан в Zoom"
		}
		return "", errValidation(msg, map[string]any{"field": field})
	}
	if utf8.RuneCountInString(name) > grpStudentNameMaxLen {
		return "", errValidation(fmt.Sprintf("Имя слишком длинное: не больше %d символов", grpStudentNameMaxLen),
			map[string]any{"field": field})
	}
	return name, nil
}

func cleanExternalID(raw string) (string, error) {
	ext := grpCollapseSpaces(raw)
	if utf8.RuneCountInString(ext) > grpStudentExternalIDMax {
		return "", errValidation(fmt.Sprintf("ID слишком длинный: не больше %d символов", grpStudentExternalIDMax),
			map[string]any{"field": "external_id"})
	}
	return ext, nil
}
