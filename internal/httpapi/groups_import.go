package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"zoom-checker/internal/importer"
	"zoom-checker/internal/model"
	"zoom-checker/internal/names"
	"zoom-checker/internal/store"
)

// Import of a student list (TZ §8, §25): the preview parses and validates
// without saving; the commit re-validates the rows the client got from the
// preview and applies them atomically.

// grpImportBodySlack is what a request may carry beyond the list itself
// (multipart headers, JSON quoting).
const grpImportBodySlack = 1 << 20

// grpImportCommitLimit bounds the JSON body of a commit: MaxRows rows of long
// names and IDs.
const grpImportCommitLimit = 8 << 20

const (
	grpMsgImportHasErrors = "В списке есть ошибки. Исправьте отмеченные строки и попробуйте ещё раз."
	grpMsgImportEmpty     = "Список пуст: добавьте хотя бы одного студента."
	grpMsgImportNoFile    = "Выберите файл со списком студентов или вставьте список текстом."
	grpMsgImportArchive   = "Нет в новом списке — будет перенесён в архив. История посещений сохранится."
	grpMsgImportChanged   = "Пока вы проверяли список, в группе что-то изменилось. Проверьте список ещё раз."
)

type importCommitRow struct {
	ExternalID string `json:"external_id"`
	FullName   string `json:"full_name"`
	// Line (additive, optional) is the row's line in the previewed file; it
	// is echoed in details.preview of a rejected commit so "строка N" there
	// matches the file. Without it rows are numbered by position (1, 2, ...).
	Line int `json:"line"`
}

type importCommitRequest struct {
	Mode       string            `json:"mode"`
	NameFormat string            `json:"name_format"`
	Rows       []importCommitRow `json:"rows"`
}

// grpImportSlots bounds the imports handled at once: each one holds its
// upload (up to 10 MiB) and the parsed rows in memory, so parallel requests
// must not stack up without limit.
const (
	grpImportSlots   = 2
	grpImportWait    = 15 * time.Second
	grpMsgImportBusy = "Сейчас загружается другой список. Попробуйте ещё раз через минуту."
)

// acquireImport waits (at most grpImportWait) for an import slot before the
// request body is read. The returned func frees the slot.
func (s *Server) acquireImport(w http.ResponseWriter, r *http.Request) (func(), error) {
	t := time.NewTimer(grpImportWait)
	defer t.Stop()
	select {
	case s.importSlots <- struct{}{}:
		return func() { <-s.importSlots }, nil
	case <-r.Context().Done():
	case <-t.C:
	}
	w.Header().Set("Retry-After", "60")
	return nil, &APIError{Status: http.StatusServiceUnavailable, Code: CodeUnavailable, Message: grpMsgImportBusy}
}

func (s *Server) handleImportPreview(w http.ResponseWriter, r *http.Request) error {
	groupID, err := pathID(r, "id")
	if err != nil {
		return errNotFound(grpMsgGroupNotFound)
	}
	if _, err := s.Store.GetGroup(r.Context(), groupID); err != nil {
		return storeErr(err, grpMsgGroupNotFound)
	}
	release, err := s.acquireImport(w, r)
	if err != nil {
		return err
	}
	defer release()
	rows, det, fields, err := s.readImportSource(w, r)
	if err != nil {
		return err
	}
	mode, format, err := grpImportOptions(r.URL.Query(), fields)
	if err != nil {
		return err
	}
	existing, _, err := grpImportExisting(r.Context(), s.Store, groupID)
	if err != nil {
		return err
	}
	p := importer.Validate(rows, existing, mode, format)
	writeJSON(w, http.StatusOK, toImportPreviewDTO(p, mode, format, det))
	return nil
}

func (s *Server) handleImportCommit(w http.ResponseWriter, r *http.Request) error {
	groupID, err := pathID(r, "id")
	if err != nil {
		return errNotFound(grpMsgGroupNotFound)
	}
	release, err := s.acquireImport(w, r)
	if err != nil {
		return err
	}
	defer release()
	var req importCommitRequest
	if err := grpDecodeJSONLimit(w, r, &req, grpImportCommitLimit); err != nil {
		return err
	}
	if _, err := s.Store.GetGroup(r.Context(), groupID); err != nil {
		return storeErr(err, grpMsgGroupNotFound)
	}
	mode, format, err := grpImportOptions(url.Values{}, map[string]string{"mode": req.Mode, "name_format": req.NameFormat})
	if err != nil {
		return err
	}
	if len(req.Rows) > importer.MaxRows {
		return errValidation(fmt.Sprintf("В списке больше %d строк. Разделите его на несколько частей.", importer.MaxRows), nil)
	}
	rows := make([]importer.Row, len(req.Rows))
	for i, cr := range req.Rows {
		line := cr.Line
		if line <= 0 {
			line = i + 1
		}
		rows[i] = importer.Row{Line: line, ExternalID: cr.ExternalID, FullName: cr.FullName}
	}

	// The check and the save share one transaction, so the plan is applied
	// to the very roster it was built from: a student archived or renamed by
	// someone else in the meantime is neither silently brought back nor
	// overwritten (on SQLite the transaction holds the write lock from the
	// start; elsewhere ApplyImport re-checks the name rule).
	var (
		p    importer.Preview
		res  *store.ImportResult
		drop []model.Alias // own aliases that become the student's name
	)
	err = s.Store.InTx(r.Context(), func(tx *store.Store) error {
		existing, students, err := grpImportExisting(r.Context(), tx, groupID)
		if err != nil {
			return err
		}
		// The rows come from a preview and are already formatted; validating
		// them with the same format again is idempotent (see importer.Validate).
		p = importer.Validate(rows, existing, mode, format)
		if !p.OK {
			return errGrpImportRejected
		}
		var ops store.ImportOps
		ops, drop = grpImportPlan(p, students)
		for _, a := range drop {
			if _, err := tx.DeleteAlias(r.Context(), a.StudentID, a.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		res, err = tx.ApplyImport(r.Context(), groupID, ops)
		return err
	})
	if errors.Is(err, errGrpImportRejected) {
		msg := grpMsgImportHasErrors
		switch {
		case len(rows) == 0:
			msg = grpMsgImportEmpty
		case p.Counts.Duplicate > 0:
			msg = importer.MsgDuplicate
		}
		det := importer.Detected{Format: importer.FormatText, Encoding: importer.EncodingUTF8}
		return errValidation(msg, map[string]any{"preview": toImportPreviewDTO(p, mode, format, det)})
	}
	if err != nil {
		var nt *store.NameTakenError
		if errors.As(err, &nt) {
			return &APIError{Status: http.StatusConflict, Code: CodeConflict, Message: grpMsgImportChanged,
				Details: map[string]any{"name": nt.Name, "student_id": nt.StudentID, "is_alias": nt.IsAlias}, Err: err}
		}
		return storeErr(err, grpMsgGroupNotFound)
	}

	// ApplyImport counts every student whose record changed: restored ones
	// (always) and existing ones that got a new name or ID. The categories of
	// the answer do not overlap: "existing" are the matched rows that changed
	// nothing (the client shows them as "без изменений").
	changed := min(max(0, res.Updated-p.Counts.Restore), p.Counts.Existing)
	out := ImportResultDTO{
		Created:  len(res.CreatedIDs),
		Existing: p.Counts.Existing - changed,
		Updated:  changed,
		Archived: res.Archived,
		Restored: p.Counts.Restore,
	}
	for _, a := range drop {
		s.audit(r, model.AuditEntry{
			Action: model.AuditAliasDelete, EntityType: "student", EntityID: grpID(a.StudentID), GroupID: &groupID,
			PreviousValue: a.Alias, Reason: "Дополнительное имя стало основным именем при загрузке списка",
		})
	}
	s.audit(r, model.AuditEntry{
		Action: model.AuditGroupImport, EntityType: "group", EntityID: grpID(groupID), GroupID: &groupID,
		NewValue: grpAuditJSON(map[string]any{
			"mode": mode, "name_format": format, "rows": len(rows),
			"created": out.Created, "existing": out.Existing, "updated": out.Updated,
			"archived": out.Archived, "restored": out.Restored,
		}),
	})
	s.refreshGroupLessons(r.Context(), groupID)
	writeJSON(w, http.StatusOK, out)
	return nil
}

// errGrpImportRejected aborts the commit transaction when the re-check of
// the rows finds errors; the handler answers 422 with the preview.
var errGrpImportRejected = errors.New("import rejected by validation")

// grpImportPlan turns an OK preview into store operations: new rows are
// created, matched and restored students are updated (a list without IDs
// keeps the stored ones), and students missing from a replace list are
// archived. drop lists the own aliases that become a student's name: they
// must go before the rename (the store would refuse the name as taken).
func grpImportPlan(p importer.Preview, students []model.Student) (ops store.ImportOps, drop []model.Alias) {
	byID := make(map[int64]*model.Student, len(students))
	for i := range students {
		byID[students[i].ID] = &students[i]
	}
	for _, pr := range p.Rows {
		if pr.Status == importer.StatusNew && !pr.Restore {
			ops.Create = append(ops.Create, store.ImportCreate{ExternalID: pr.ExternalID, FullName: pr.FullName})
			continue
		}
		if pr.StudentID == nil || byID[*pr.StudentID] == nil {
			continue // cannot happen for an OK preview
		}
		st := byID[*pr.StudentID]
		ext := pr.ExternalID
		if ext == "" {
			ext = st.ExternalID // a list without IDs keeps the stored ones
		}
		ops.Update = append(ops.Update, store.ImportUpdate{StudentID: st.ID, ExternalID: ext, FullName: pr.FullName})
		n := names.Normalize(pr.FullName)
		for _, a := range st.Aliases {
			if a.NormalizedAlias == n {
				drop = append(drop, a)
			}
		}
	}
	for _, a := range p.Archive {
		ops.Archive = append(ops.Archive, a.StudentID)
	}
	return ops, drop
}

// grpImportExisting returns the group's students (archived ones too, with
// aliases) in the form importer.Validate expects, and the students themselves.
func grpImportExisting(ctx context.Context, st *store.Store, groupID int64) ([]importer.ExistingName, []model.Student, error) {
	students, err := st.ListStudents(ctx, groupID, true)
	if err != nil {
		return nil, nil, storeErr(err, grpMsgGroupNotFound)
	}
	out := make([]importer.ExistingName, 0, len(students))
	for _, st := range students {
		archived := st.Status != model.StatusActive
		out = append(out, importer.ExistingName{StudentID: st.ID, ExternalID: st.ExternalID,
			FullName: st.FullName, Normalized: st.NormalizedName, Archived: archived})
		for _, a := range st.Aliases {
			out = append(out, importer.ExistingName{StudentID: st.ID, FullName: a.Alias,
				Normalized: a.NormalizedAlias, IsAlias: true, Archived: archived})
		}
	}
	return out, students, nil
}

// readImportSource reads the list of a preview request: a multipart upload
// (field "file") or JSON {"text": "..."}. fields holds the optional
// mode / name_format sent in the body.
func (s *Server) readImportSource(w http.ResponseWriter, r *http.Request) ([]importer.Row, importer.Detected, map[string]string, error) {
	fields := map[string]string{}
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType == "multipart/form-data" {
		filename, data, err := grpReadUpload(w, r, fields)
		if err != nil {
			return nil, importer.Detected{}, nil, err
		}
		rows, det, err := importer.ParseContext(r.Context(), filename, data)
		if err != nil {
			return nil, det, nil, grpImportParseErr(err)
		}
		return rows, det, fields, nil
	}

	var req struct {
		Text       *string `json:"text"`
		Mode       string  `json:"mode"`
		NameFormat string  `json:"name_format"`
	}
	if err := grpDecodeJSONLimit(w, r, &req, importer.MaxFileSize+grpImportBodySlack); err != nil {
		if ae := (*APIError)(nil); errors.As(err, &ae) && ae.Status == http.StatusUnsupportedMediaType {
			ae.Message = "Загрузите файл (multipart/form-data, поле «file») или отправьте текст списка в JSON"
		}
		return nil, importer.Detected{}, nil, err
	}
	if req.Text == nil {
		return nil, importer.Detected{}, nil, errValidation(grpMsgImportNoFile, map[string]any{"field": "text"})
	}
	fields["mode"], fields["name_format"] = req.Mode, req.NameFormat
	rows, det, err := importer.ParsePastedText(*req.Text)
	if err != nil {
		return nil, det, nil, grpImportParseErr(err)
	}
	return rows, det, fields, nil
}

// grpReadUpload streams the multipart body without temporary files and
// returns the first "file" part (at most importer.MaxFileSize bytes).
func grpReadUpload(w http.ResponseWriter, r *http.Request, fields map[string]string) (string, []byte, error) {
	tooLarge := func() error {
		return &APIError{Status: http.StatusRequestEntityTooLarge, Code: CodeBadRequest,
			Message: fmt.Sprintf("Файл слишком большой (больше %d МБ). Оставьте в нём только список студентов.", importer.MaxFileSize>>20)}
	}
	r.Body = http.MaxBytesReader(w, r.Body, importer.MaxFileSize+grpImportBodySlack)
	mr, err := r.MultipartReader()
	if err != nil {
		return "", nil, errBadRequest("Не удалось прочитать загруженный файл. Попробуйте ещё раз.")
	}
	var (
		filename string
		data     []byte
		found    bool
	)
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				return "", nil, tooLarge()
			}
			return "", nil, &APIError{Status: http.StatusBadRequest, Code: CodeBadRequest,
				Message: "Не удалось прочитать загруженный файл. Попробуйте ещё раз.", Err: err}
		}
		switch name := part.FormName(); {
		case name == "file" && !found:
			found, filename = true, part.FileName()
			data, err = io.ReadAll(io.LimitReader(part, importer.MaxFileSize+1))
			if err != nil {
				var mbe *http.MaxBytesError
				if errors.As(err, &mbe) {
					return "", nil, tooLarge()
				}
				return "", nil, &APIError{Status: http.StatusBadRequest, Code: CodeBadRequest,
					Message: "Не удалось прочитать загруженный файл. Попробуйте ещё раз.", Err: err}
			}
			if len(data) > importer.MaxFileSize {
				return "", nil, tooLarge()
			}
		case name == "mode" || name == "name_format":
			v, _ := io.ReadAll(io.LimitReader(part, 64))
			fields[name] = strings.TrimSpace(string(v))
		}
		_ = part.Close()
	}
	if !found {
		return "", nil, errValidation(grpMsgImportNoFile, map[string]any{"field": "file"})
	}
	return filename, data, nil
}

func grpImportParseErr(err error) error {
	if errors.Is(err, importer.ErrBusy) {
		return &APIError{Status: http.StatusServiceUnavailable, Code: CodeUnavailable, Message: importer.UserMessage(err), Err: err}
	}
	if errors.Is(err, importer.ErrTooLarge) {
		return &APIError{Status: http.StatusRequestEntityTooLarge, Code: CodeBadRequest, Message: importer.UserMessage(err), Err: err}
	}
	return &APIError{Status: http.StatusUnprocessableEntity, Code: CodeValidation, Message: importer.UserMessage(err), Err: err}
}

// grpImportOptions reads mode (add|replace, default add) and name_format
// (as_is|last_first, default as_is) from the query, falling back to fields.
func grpImportOptions(q url.Values, fields map[string]string) (importer.Mode, importer.NameFormat, error) {
	get := func(k string) string {
		if v := strings.TrimSpace(q.Get(k)); v != "" {
			return v
		}
		return strings.TrimSpace(fields[k])
	}
	mode := importer.Mode(get("mode"))
	switch mode {
	case "":
		mode = importer.ModeAdd
	case importer.ModeAdd, importer.ModeReplace:
	default:
		return "", "", errValidation("Неизвестный режим загрузки: ожидается add (добавить) или replace (заменить список)",
			map[string]any{"field": "mode"})
	}
	format := importer.NameFormat(get("name_format"))
	switch format {
	case "":
		format = importer.NameAsIs
	case importer.NameAsIs, importer.NameLastFirst:
	default:
		return "", "", errValidation("Неизвестный формат имён: ожидается as_is (как в файле) или last_first (фамилия и имя)",
			map[string]any{"field": "name_format"})
	}
	return mode, format, nil
}

func toImportPreviewDTO(p importer.Preview, mode importer.Mode, format importer.NameFormat, det importer.Detected) ImportPreviewDTO {
	out := ImportPreviewDTO{
		Mode: mode, NameFormat: format, HasPatronymics: p.HasPatronymics,
		Rows: make([]ImportRowDTO, 0, len(p.Rows)+len(p.Archive)),
		Counts: ImportCountsDTO{New: p.Counts.New, Existing: p.Counts.Existing, Error: p.Counts.Error,
			Duplicate: p.Counts.Duplicate, Archive: p.Counts.Archive, Restore: p.Counts.Restore, Rename: p.Counts.Rename},
		Detected: ImportDetectedDTO{Format: det.Format, Encoding: det.Encoding,
			Delimiter: grpDelimiterLabel(det.Delimiter), NameColumn: det.NameColumn},
	}
	if out.Detected.Format == "" {
		out.Detected.Format = importer.FormatText
	}
	for _, r := range p.Rows {
		out.Rows = append(out.Rows, ImportRowDTO{
			Line: r.Line, ExternalID: r.ExternalID, FullName: r.FullName, NormalizedName: r.NormalizedName,
			Status: string(r.Status), Message: r.Message, StudentID: r.StudentID, Restore: r.Restore, Rename: r.Rename,
		})
	}
	for _, a := range p.Archive {
		id := a.StudentID
		out.Rows = append(out.Rows, ImportRowDTO{
			ExternalID: a.ExternalID, FullName: a.FullName, NormalizedName: a.Normalized,
			Status: "archive", Message: grpMsgImportArchive, StudentID: &id,
		})
	}
	return out
}

// grpDelimiterLabel names a detected delimiter in plain words.
func grpDelimiterLabel(d string) string {
	switch d {
	case "\t":
		return "табуляция"
	case ",":
		return "запятая"
	case ";":
		return "точка с запятой"
	case "|":
		return "вертикальная черта"
	}
	return d
}
