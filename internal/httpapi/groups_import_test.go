package httpapi

import (
	"bytes"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/charmap"

	"zoom-checker/internal/importer"
	"zoom-checker/internal/model"
)

func gtPreviewPath(groupID int64, query string) string {
	p := "/api/groups/" + grpID(groupID) + "/import/preview"
	if query != "" {
		p += "?" + query
	}
	return p
}

func gtCommitPath(groupID int64) string { return "/api/groups/" + grpID(groupID) + "/import/commit" }

func gtPreview(t *testing.T, r gtResp) ImportPreviewDTO {
	t.Helper()
	if r.status != http.StatusOK {
		t.Fatalf("preview: %d %s", r.status, r.body)
	}
	var p ImportPreviewDTO
	r.decode(t, &p)
	return p
}

// gtCommitRows turns preview rows into a commit body (what the client sends).
func gtCommitRows(p ImportPreviewDTO) []map[string]string {
	var rows []map[string]string
	for _, r := range p.Rows {
		if r.Status == "new" || r.Status == "existing" {
			rows = append(rows, map[string]string{"external_id": r.ExternalID, "full_name": r.FullName})
		}
	}
	return rows
}

func TestImportPreviewCSVWindows1251(t *testing.T) {
	t.Parallel()
	e := newGTEnv(t)
	g := e.group("DevOps-1")
	ivan := e.student(g.ID, "Иванов Иван", "0012")

	data, err := charmap.Windows1251.NewEncoder().Bytes([]byte("ФИО;Табельный номер\r\nИванов Иван;0012\r\nЁлкина Алёна;0013\r\n;\r\nПетров Пётр;0014\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	r := e.upload(gtPreviewPath(g.ID, ""), "Список группы.csv", data)
	p := gtPreview(t, r)
	if p.Mode != importer.ModeAdd || p.NameFormat != importer.NameAsIs || p.HasPatronymics {
		t.Errorf("options %+v", p)
	}
	wantDet := ImportDetectedDTO{Format: "csv", Encoding: "windows-1251", Delimiter: "точка с запятой", NameColumn: "ФИО"}
	if p.Detected != wantDet {
		t.Errorf("detected %+v, want %+v", p.Detected, wantDet)
	}
	if p.Counts != (ImportCountsDTO{New: 2, Existing: 1}) {
		t.Errorf("counts %+v", p.Counts)
	}
	if len(p.Rows) != 3 {
		t.Fatalf("rows %+v", p.Rows)
	}
	r0 := p.Rows[0]
	if r0.Line != 2 || r0.Status != "existing" || r0.StudentID == nil || *r0.StudentID != ivan.ID || r0.ExternalID != "0012" || r0.Message != importer.MsgExisting {
		t.Errorf("row 0 %+v", r0)
	}
	if r1 := p.Rows[1]; r1.Line != 3 || r1.FullName != "Ёлкина Алёна" || r1.NormalizedName != "елкина алена" || r1.Status != "new" || r1.StudentID != nil {
		t.Errorf("row 1 %+v", r1)
	}
	for _, key := range []string{`"student_id":null`, `"line":5`, `"name_format":"as_is"`, `"has_patronymics":false`, `"restore":0`} {
		if !strings.Contains(string(r.body), key) {
			t.Errorf("body lacks %s: %s", key, r.body)
		}
	}
	// Nothing was saved.
	if d := e.detail(g.ID); len(d.Students) != 1 {
		t.Errorf("preview saved students: %+v", d.Students)
	}
}

func TestImportPreviewXLSX(t *testing.T) {
	t.Parallel()
	e := newGTEnv(t)
	g := e.group("Excel-1")

	f := excelize.NewFile()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.SetCellValue("Sheet1", "A1", "Список группы Excel-1"))
	must(f.SetSheetRow("Sheet1", "A3", &[]any{"№ п/п", "Фамилия", "Имя", "Отчество", "E-mail"}))
	must(f.SetSheetRow("Sheet1", "A4", &[]any{1, "Иванов", "Иван", "Иванович", "Ivanov@Mail.ru"}))
	must(f.SetSheetRow("Sheet1", "A5", &[]any{2, "Петрова", "Анна", "Сергеевна", "anna@mail.ru"}))
	must(f.SetSheetRow("Sheet1", "A6", &[]any{3, "Сидоров", "Алексей", "", ""}))
	var buf bytes.Buffer
	must(f.Write(&buf))
	_ = f.Close()

	p := gtPreview(t, e.upload(gtPreviewPath(g.ID, ""), "group.xlsx", buf.Bytes()))
	wantDet := ImportDetectedDTO{Format: "xlsx", Encoding: "utf-8", Delimiter: "", NameColumn: "Фамилия + Имя + Отчество"}
	if p.Detected != wantDet {
		t.Errorf("detected %+v", p.Detected)
	}
	if !p.HasPatronymics || p.Counts.New != 3 {
		t.Errorf("preview %+v", p)
	}
	got := []string{}
	for _, r := range p.Rows {
		got = append(got, fmt.Sprintf("%d|%s|%s", r.Line, r.ExternalID, r.FullName))
	}
	want := []string{"4|ivanov@mail.ru|Иванов Иван Иванович", "5|anna@mail.ru|Петрова Анна Сергеевна", "6||Сидоров Алексей"}
	if !slices.Equal(got, want) {
		t.Errorf("rows %q, want %q (row numbers are not IDs, e-mails are)", got, want)
	}

	// Offered "Фамилия Имя" format.
	p = gtPreview(t, e.upload(gtPreviewPath(g.ID, "name_format=last_first&mode=replace"), "group.xlsx", buf.Bytes()))
	if p.NameFormat != importer.NameLastFirst || p.Mode != importer.ModeReplace || p.Rows[0].FullName != "Иванов Иван" || p.Rows[0].NormalizedName != "иванов иван" {
		t.Errorf("last_first %+v", p)
	}
}

func TestImportPreviewPastedText(t *testing.T) {
	t.Parallel()
	e := newGTEnv(t)
	g := e.group("Text-1")
	e.student(g.ID, "Петров Пётр", "")

	r := e.do(http.MethodPost, gtPreviewPath(g.ID, ""), map[string]any{
		"text": "ФИО\tТабельный номер\nИванов Иван\t0012\nПетров Пётр\t0013\nиванов  ИВАН\t0014\n",
	})
	p := gtPreview(t, r)
	if p.Detected != (ImportDetectedDTO{Format: "text", Encoding: "utf-8", Delimiter: "табуляция", NameColumn: "ФИО"}) {
		t.Errorf("detected %+v", p.Detected)
	}
	if p.Counts != (ImportCountsDTO{Existing: 1, Duplicate: 2}) {
		t.Errorf("counts %+v", p.Counts)
	}
	if !strings.HasPrefix(p.Rows[0].Message, importer.MsgDuplicate) || p.Rows[1].Status != "existing" ||
		p.Rows[1].Message != "Уже есть в группе. Будет добавлен ID «0013»." {
		t.Errorf("rows %+v", p.Rows)
	}

	// Numbered list: the numbers are not IDs; comma delimiter label.
	p = gtPreview(t, e.do(http.MethodPost, gtPreviewPath(g.ID, ""), map[string]any{"text": "1,Сидоров Алексей\n2,Петров Пётр\n"}))
	if p.Detected.Delimiter != "запятая" || p.Rows[0].ExternalID != "" || p.Rows[1].Status != "existing" {
		t.Errorf("numbered %+v", p)
	}
	// Options may come in the JSON body too.
	p = gtPreview(t, e.do(http.MethodPost, gtPreviewPath(g.ID, ""), map[string]any{"text": "Сидоров Алексей", "mode": "replace"}))
	if p.Mode != importer.ModeReplace || p.Counts.Archive != 1 {
		t.Errorf("body options %+v", p)
	}
}

func TestImportPreviewReplaceListsArchiveRows(t *testing.T) {
	t.Parallel()
	e := newGTEnv(t)
	g := e.group("Replace-1")
	e.student(g.ID, "Иванов Иван", "")
	petr := e.student(g.ID, "Петров Пётр", "P1")

	r := e.do(http.MethodPost, gtPreviewPath(g.ID, "mode=replace"), map[string]any{"text": "Иванов Иван\nСидоров Алексей"})
	p := gtPreview(t, r)
	if p.Counts != (ImportCountsDTO{New: 1, Existing: 1, Archive: 1}) || len(p.Rows) != 3 {
		t.Fatalf("preview %+v", p)
	}
	a := p.Rows[2]
	if a.Status != "archive" || a.Line != 0 || a.StudentID == nil || *a.StudentID != petr.ID || a.FullName != "Петров Пётр" ||
		a.ExternalID != "P1" || a.NormalizedName != "петров петр" || !strings.Contains(a.Message, "архив") {
		t.Errorf("archive row %+v", a)
	}
	// Add mode never archives.
	p = gtPreview(t, e.do(http.MethodPost, gtPreviewPath(g.ID, "mode=add"), map[string]any{"text": "Иванов Иван"}))
	if p.Counts.Archive != 0 || len(p.Rows) != 1 {
		t.Errorf("add mode %+v", p)
	}
}

func TestImportPreviewErrors(t *testing.T) {
	t.Parallel()
	e := newGTEnv(t)
	g := e.group("Errors-1")

	many := &strings.Builder{}
	for i := range importer.MaxRows + 1 {
		fmt.Fprintf(many, "Иванов Иван %d\n", i)
	}
	tests := []struct {
		name   string
		resp   func() gtResp
		status int
		msg    string
	}{
		{"empty text", func() gtResp { return e.do(http.MethodPost, gtPreviewPath(g.ID, ""), map[string]any{"text": " \n "}) },
			http.StatusUnprocessableEntity, "не нашлось ни одного студента"},
		{"no text", func() gtResp { return e.do(http.MethodPost, gtPreviewPath(g.ID, ""), map[string]any{}) },
			http.StatusUnprocessableEntity, "Выберите файл"},
		{"too many rows", func() gtResp {
			return e.do(http.MethodPost, gtPreviewPath(g.ID, ""), map[string]any{"text": many.String()})
		},
			http.StatusUnprocessableEntity, fmt.Sprintf("больше %d строк", importer.MaxRows)},
		{"text too large", func() gtResp {
			return e.do(http.MethodPost, gtPreviewPath(g.ID, ""), map[string]any{"text": strings.Repeat("a", importer.MaxFileSize+1)})
		}, http.StatusRequestEntityTooLarge, "слишком большой"},
		{"bad mode", func() gtResp {
			return e.do(http.MethodPost, gtPreviewPath(g.ID, "mode=merge"), map[string]any{"text": "Иванов Иван"})
		},
			http.StatusUnprocessableEntity, "Неизвестный режим"},
		{"bad name format", func() gtResp {
			return e.do(http.MethodPost, gtPreviewPath(g.ID, "name_format=first_last"), map[string]any{"text": "Иванов Иван"})
		}, http.StatusUnprocessableEntity, "Неизвестный формат"},
		{"pdf", func() gtResp { return e.upload(gtPreviewPath(g.ID, ""), "list.pdf", []byte("%PDF-1.7 ...")) },
			http.StatusUnprocessableEntity, "PDF не поддерживаются"},
		{"empty file", func() gtResp { return e.upload(gtPreviewPath(g.ID, ""), "list.csv", nil) },
			http.StatusUnprocessableEntity, "не нашлось ни одного студента"},
		{"file too large", func() gtResp {
			return e.upload(gtPreviewPath(g.ID, ""), "big.csv", bytes.Repeat([]byte("Иванов Иван\n"), importer.MaxFileSize/20+10))
		}, http.StatusRequestEntityTooLarge, "Файл слишком большой"},
		{"body far too large", func() gtResp {
			return e.upload(gtPreviewPath(g.ID, ""), "big.csv", bytes.Repeat([]byte("a"), importer.MaxFileSize+2<<20))
		}, http.StatusRequestEntityTooLarge, "Файл слишком большой"},
		{"multipart without a file", func() gtResp {
			body := "--XX\r\nContent-Disposition: form-data; name=\"mode\"\r\n\r\nadd\r\n--XX--\r\n"
			return e.raw(http.MethodPost, gtPreviewPath(g.ID, ""), "multipart/form-data; boundary=XX", strings.NewReader(body), true)
		}, http.StatusUnprocessableEntity, "Выберите файл"},
		{"wrong content type", func() gtResp {
			return e.raw(http.MethodPost, gtPreviewPath(g.ID, ""), "text/plain", strings.NewReader("Иванов Иван"), true)
		}, http.StatusUnsupportedMediaType, "Загрузите файл"},
		{"unknown group", func() gtResp {
			return e.do(http.MethodPost, gtPreviewPath(999, ""), map[string]any{"text": "Иванов Иван"})
		},
			http.StatusNotFound, "Группа не найдена"},
	}
	for _, tt := range tests {
		r := tt.resp()
		if r.status != tt.status || !strings.Contains(r.apiErr(t).Message, tt.msg) {
			t.Errorf("%s: %d %s", tt.name, r.status, r.body)
		}
	}
}

func TestImportCommit(t *testing.T) {
	t.Parallel()
	e := newGTEnv(t)
	g := e.group("Commit-1")
	planned := e.lesson(g.ID, "Будущее", gtNow.Add(time.Hour), model.LessonPlanned)
	e.lesson(g.ID, "Прошло", gtNow.Add(-24*time.Hour), model.LessonFinished)
	ivan := e.student(g.ID, "Иванов Иван", "7")
	e.proc.reset()

	p := gtPreview(t, e.do(http.MethodPost, gtPreviewPath(g.ID, ""), map[string]any{
		"text": "ID;ФИО\n7;Иванов Иван Иванович\n8;Петров Пётр\n;Сидоров Алексей\n",
	}))
	r := e.do(http.MethodPost, gtCommitPath(g.ID), map[string]any{"mode": "add", "rows": gtCommitRows(p)})
	e.want(r, http.StatusOK)
	var res ImportResultDTO
	r.decode(t, &res)
	// Иванов matched by ID and got the long name: "updated", not also
	// "existing" (the client shows existing as "без изменений").
	if res != (ImportResultDTO{Created: 2, Updated: 1}) {
		t.Errorf("result %+v", res)
	}
	for _, key := range []string{`"created":2`, `"existing":0`, `"updated":1`, `"archived":0`, `"restored":0`} {
		if !strings.Contains(string(r.body), key) {
			t.Errorf("body lacks %s: %s", key, r.body)
		}
	}
	d := e.detail(g.ID)
	got := map[string]string{}
	for _, s := range d.Students {
		got[s.FullName] = s.ExternalID
	}
	if len(d.Students) != 3 || got["Иванов Иван Иванович"] != "7" || got["Петров Пётр"] != "8" || got["Сидоров Алексей"] != "" {
		t.Errorf("students %+v", d.Students)
	}
	if d.Students[0].ID != ivan.ID {
		t.Errorf("Иванов must keep his record: %+v", d.Students[0])
	}
	if ids := e.proc.ids(); !slices.Equal(ids, []int64{planned.ID}) {
		t.Errorf("rebuilt %v", ids)
	}
	a := e.audit(g.ID)
	last := a[len(a)-1]
	if last.Action != model.AuditGroupImport || last.EntityType != "group" || last.EntityID != grpID(g.ID) ||
		!strings.Contains(last.NewValue, `"created":2`) || !strings.Contains(last.NewValue, `"mode":"add"`) {
		t.Errorf("audit %+v", last)
	}

	// A list without IDs keeps the stored ones.
	r = e.do(http.MethodPost, gtCommitPath(g.ID), map[string]any{"mode": "add",
		"rows": []map[string]string{{"external_id": "", "full_name": "Петров Пётр"}}})
	e.want(r, http.StatusOK)
	r.decode(t, &res)
	if res != (ImportResultDTO{Existing: 1}) {
		t.Errorf("no-op result %+v", res)
	}
	for _, s := range e.detail(g.ID).Students {
		if s.FullName == "Петров Пётр" && s.ExternalID != "8" {
			t.Errorf("ID lost: %+v", s)
		}
	}
}

func TestImportCommitRejectsDuplicates(t *testing.T) {
	t.Parallel()
	e := newGTEnv(t)
	g := e.group("Dup-1")
	e.student(g.ID, "Сидоров Алексей", "")

	r := e.do(http.MethodPost, gtCommitPath(g.ID), map[string]any{"mode": "replace", "rows": []map[string]string{
		{"external_id": "", "full_name": "Иванов Иван"},
		{"external_id": "", "full_name": "Петров Пётр"},
		{"external_id": "", "full_name": "иванов  ИВАН"},
	}})
	e.want(r, http.StatusUnprocessableEntity)
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details struct {
				Preview ImportPreviewDTO `json:"preview"`
			} `json:"details"`
		} `json:"error"`
	}
	r.decode(t, &body)
	er := body.Error
	if er.Code != CodeValidation || er.Message != importer.MsgDuplicate {
		t.Errorf("error %+v", er)
	}
	pv := er.Details.Preview
	if pv.Mode != importer.ModeReplace || pv.Counts != (ImportCountsDTO{New: 1, Duplicate: 2, Archive: 1}) || len(pv.Rows) != 4 {
		t.Errorf("preview %+v", pv)
	}
	if pv.Rows[0].Status != "duplicate" || pv.Rows[0].Line != 1 || pv.Rows[2].Line != 3 ||
		!strings.HasPrefix(pv.Rows[0].Message, importer.MsgDuplicate) || pv.Rows[3].Status != "archive" {
		t.Errorf("rows %+v", pv.Rows)
	}
	// Nothing changed.
	if d := e.detail(g.ID); len(d.Students) != 1 || d.Students[0].Status != model.StatusActive {
		t.Errorf("students changed: %+v", d.Students)
	}

	// Optional file line numbers are echoed, so the re-shown preview points
	// at the lines of the file the teacher has.
	r = e.do(http.MethodPost, gtCommitPath(g.ID), map[string]any{"mode": "add", "rows": []map[string]any{
		{"external_id": "", "full_name": "Иванов Иван", "line": 4},
		{"external_id": "", "full_name": "иванов иван", "line": 9},
		{"external_id": "", "full_name": "Петров Пётр"},
	}})
	e.want(r, http.StatusUnprocessableEntity)
	r.decode(t, &body)
	var lines []int
	for _, row := range body.Error.Details.Preview.Rows {
		lines = append(lines, row.Line)
	}
	if !slices.Equal(lines, []int{4, 9, 3}) {
		t.Errorf("lines %v, rows %+v", lines, body.Error.Details.Preview.Rows)
	}

	// Other errors and an empty list.
	r = e.do(http.MethodPost, gtCommitPath(g.ID), map[string]any{"mode": "add", "rows": []map[string]string{{"full_name": "12345"}}})
	e.want(r, http.StatusUnprocessableEntity)
	if er := r.apiErr(t); er.Message != grpMsgImportHasErrors || er.Details["preview"] == nil {
		t.Errorf("error rows %+v", er)
	}
	r = e.do(http.MethodPost, gtCommitPath(g.ID), map[string]any{"mode": "replace", "rows": []map[string]string{}})
	e.want(r, http.StatusUnprocessableEntity)
	if er := r.apiErr(t); er.Message != grpMsgImportEmpty || er.Details["preview"] == nil {
		t.Errorf("empty %+v", er)
	}
	e.want(e.do(http.MethodPost, gtCommitPath(g.ID), map[string]any{"mode": "merge", "rows": []map[string]string{{"full_name": "A"}}}),
		http.StatusUnprocessableEntity)
	e.want(e.do(http.MethodPost, gtCommitPath(999), map[string]any{"mode": "add", "rows": []map[string]string{{"full_name": "A Б"}}}),
		http.StatusNotFound)
	rows := make([]map[string]string, importer.MaxRows+1)
	for i := range rows {
		rows[i] = map[string]string{"full_name": fmt.Sprintf("Студент %d", i)}
	}
	e.want(e.do(http.MethodPost, gtCommitPath(g.ID), map[string]any{"mode": "add", "rows": rows}), http.StatusUnprocessableEntity)
	if acts := gtActions(e.audit(g.ID)); slices.Contains(acts, model.AuditGroupImport) {
		t.Errorf("a rejected import was audited: %q", acts)
	}
}

func TestImportCommitReplaceArchivesAndRestores(t *testing.T) {
	t.Parallel()
	e := newGTEnv(t)
	g := e.group("Restore-1")
	ivan := e.student(g.ID, "Иванов Иван", "")
	petr := e.student(g.ID, "Петров Пётр", "")
	r := e.do(http.MethodPost, "/api/students/"+grpID(petr.ID)+"/aliases", map[string]any{"alias": "Петя"})
	e.want(r, http.StatusCreated)

	commit := func(names ...string) ImportResultDTO {
		t.Helper()
		rows := []map[string]string{}
		for _, n := range names {
			rows = append(rows, map[string]string{"external_id": "", "full_name": n})
		}
		r := e.do(http.MethodPost, gtCommitPath(g.ID), map[string]any{"mode": "replace", "rows": rows})
		e.want(r, http.StatusOK)
		var res ImportResultDTO
		r.decode(t, &res)
		return res
	}
	if res := commit("Иванов Иван", "Сидоров Алексей"); res != (ImportResultDTO{Created: 1, Existing: 1, Archived: 1}) {
		t.Errorf("first %+v", res)
	}
	// The preview shows that Петров comes back from the archive.
	p := gtPreview(t, e.do(http.MethodPost, gtPreviewPath(g.ID, "mode=replace"), map[string]any{"text": "Иванов Иван\nПетров Пётр\nСидоров Алексей"}))
	if p.Counts != (ImportCountsDTO{New: 1, Existing: 2, Restore: 1}) || !p.Rows[1].Restore || *p.Rows[1].StudentID != petr.ID ||
		!strings.Contains(p.Rows[1].Message, "Был в архиве группы") {
		t.Errorf("restore preview %+v", p)
	}
	if res := commit("Иванов Иван", "Петров Пётр", "Сидоров Алексей"); res != (ImportResultDTO{Existing: 2, Restored: 1}) {
		t.Errorf("second %+v", res)
	}
	d := e.detail(g.ID)
	if len(d.Students) != 3 || d.StudentCount != 3 {
		t.Fatalf("students %+v", d.Students)
	}
	for _, s := range d.Students {
		if s.Status != model.StatusActive {
			t.Errorf("not active: %+v", s)
		}
		if s.ID == petr.ID && (len(s.Aliases) != 1 || s.Aliases[0].Alias != "Петя") {
			t.Errorf("restored student lost the alias: %+v", s)
		}
	}
	_ = ivan
}

// Renaming a student to one of its own aliases drops the alias in the same
// transaction instead of failing on the name check.
func TestImportCommitRenameToOwnAlias(t *testing.T) {
	t.Parallel()
	e := newGTEnv(t)
	g := e.group("Alias-1")
	ivan := e.student(g.ID, "Иванов Иван", "7")
	e.want(e.do(http.MethodPost, "/api/students/"+grpID(ivan.ID)+"/aliases", map[string]any{"alias": "Ivanov Ivan"}), http.StatusCreated)
	e.want(e.do(http.MethodPost, "/api/students/"+grpID(ivan.ID)+"/aliases", map[string]any{"alias": "Ваня"}), http.StatusCreated)

	p := gtPreview(t, e.do(http.MethodPost, gtPreviewPath(g.ID, ""), map[string]any{"text": "ID;ФИО\n7;Ivanov  Ivan\n"}))
	if p.Rows[0].Status != "existing" || *p.Rows[0].StudentID != ivan.ID {
		t.Fatalf("preview %+v", p.Rows)
	}
	r := e.do(http.MethodPost, gtCommitPath(g.ID), map[string]any{"mode": "add", "rows": gtCommitRows(p)})
	e.want(r, http.StatusOK)
	var res ImportResultDTO
	r.decode(t, &res)
	if res != (ImportResultDTO{Updated: 1}) {
		t.Errorf("result %+v", res)
	}
	d := e.detail(g.ID)
	if s := d.Students[0]; s.FullName != "Ivanov Ivan" || s.ExternalID != "7" || len(s.Aliases) != 1 || s.Aliases[0].Alias != "Ваня" {
		t.Errorf("student %+v", s)
	}
	acts := gtActions(e.audit(g.ID))
	if !slices.Contains(acts, model.AuditAliasDelete) || acts[len(acts)-1] != model.AuditGroupImport {
		t.Errorf("audit %q", acts)
	}
}

// With "Фамилия Имя" the commit only gets the short names, yet it must find
// the students stored under the long ones (as the preview did).
func TestImportCommitLastFirst(t *testing.T) {
	t.Parallel()
	e := newGTEnv(t)
	g := e.group("Short-1")
	ivan := e.student(g.ID, "Иванов Иван Иванович", "")
	e.student(g.ID, "Петрова Анна Сергеевна", "")

	p := gtPreview(t, e.do(http.MethodPost, gtPreviewPath(g.ID, "mode=replace&name_format=last_first"), map[string]any{
		"text": "Иванов Иван Иванович\nПетрова Анна Сергеевна\nСидоров Алексей Петрович\n",
	}))
	if !p.HasPatronymics || p.Counts != (ImportCountsDTO{New: 1, Existing: 2, Rename: 2}) || p.Rows[0].FullName != "Иванов Иван" {
		t.Fatalf("preview %+v", p)
	}
	r := e.do(http.MethodPost, gtCommitPath(g.ID), map[string]any{"mode": "replace", "name_format": "last_first", "rows": gtCommitRows(p)})
	e.want(r, http.StatusOK)
	var res ImportResultDTO
	r.decode(t, &res)
	if res != (ImportResultDTO{Created: 1, Updated: 2}) {
		t.Errorf("result %+v", res)
	}
	d := e.detail(g.ID)
	names := []string{}
	for _, s := range d.Students {
		names = append(names, s.FullName)
		if s.Status != model.StatusActive {
			t.Errorf("archived by mistake: %+v", s)
		}
	}
	if !slices.Equal(names, []string{"Иванов Иван", "Петрова Анна", "Сидоров Алексей"}) || d.Students[0].ID != ivan.ID {
		t.Errorf("students %q", names)
	}
}

// Product rule: row numbers never become IDs, so a renumbered list matches
// the same students by name.
func TestImportRenumberedListMatchesByName(t *testing.T) {
	t.Parallel()
	e := newGTEnv(t)
	g := e.group("Numbers-1")
	p := gtPreview(t, e.do(http.MethodPost, gtPreviewPath(g.ID, ""), map[string]any{"text": "№;ФИО\n1;Иванов Иван\n2;Петров Пётр\n"}))
	e.want(e.do(http.MethodPost, gtCommitPath(g.ID), map[string]any{"mode": "add", "rows": gtCommitRows(p)}), http.StatusOK)
	for _, s := range e.detail(g.ID).Students {
		if s.ExternalID != "" {
			t.Errorf("a row number was stored as ID: %+v", s)
		}
	}
	p = gtPreview(t, e.do(http.MethodPost, gtPreviewPath(g.ID, "mode=replace"), map[string]any{"text": "1;Петров Пётр\n2;Сидоров Алексей\n3;Иванов Иван\n"}))
	if p.Counts != (ImportCountsDTO{New: 1, Existing: 2}) {
		t.Errorf("counts %+v", p.Counts)
	}
	for _, r := range p.Rows {
		if r.Status == "existing" && r.Message != importer.MsgExisting {
			t.Errorf("row %+v must not change anything", r)
		}
	}
}
