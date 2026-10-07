package httpapi

import (
	"bytes"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"zoom-checker/internal/export"
	"zoom-checker/internal/model"
)

// gtReportFixture: three students (Сидоров archived later) and four lessons:
// L0 (August), L1 and L2 (finished, September), L3 (planned, next week).
type gtReportFixture struct {
	g              GroupSummaryDTO
	a, b, c        StudentDTO
	l0, l1, l2, l3 *model.Lesson
}

func newGTReportFixture(t *testing.T, e *gtEnv) gtReportFixture {
	t.Helper()
	f := gtReportFixture{g: e.group("Report-1")}
	f.a = e.student(f.g.ID, "Иванов Иван", "7")
	f.b = e.student(f.g.ID, "Петров Пётр", "")
	f.c = e.student(f.g.ID, "Сидоров Алексей", "")
	f.l0 = e.lesson(f.g.ID, "Вводное", time.Date(2026, 8, 1, 15, 0, 0, 0, time.UTC), model.LessonFinished)
	f.l1 = e.lesson(f.g.ID, "Linux #1", time.Date(2026, 9, 15, 15, 0, 0, 0, time.UTC), model.LessonFinished)
	f.l2 = e.lesson(f.g.ID, "=Linux #2", time.Date(2026, 9, 22, 15, 0, 0, 0, time.UTC), model.LessonFinished)
	f.l3 = e.lesson(f.g.ID, "Linux #3", time.Date(2026, 10, 13, 15, 0, 0, 0, time.UTC), model.LessonPlanned)

	late := int64(120)
	row := func(student int64, auto model.FinalStatus, secs int64, late *int64) model.Attendance {
		cur := model.CurrentLeft
		if secs == 0 {
			cur = model.CurrentNotSeen
		}
		return model.Attendance{StudentID: student, CurrentStatus: cur, AutoStatus: auto, TotalDurationSeconds: secs, LateSeconds: late}
	}
	upsert := func(l *model.Lesson, rows ...model.Attendance) {
		t.Helper()
		if _, err := e.st.UpsertAttendanceComputed(e.ctx, l.ID, rows); err != nil {
			t.Fatal(err)
		}
	}
	manual := func(l *model.Lesson, student int64, st model.FinalStatus) {
		t.Helper()
		if _, err := e.st.SetManualStatus(e.ctx, l.ID, student, &st, &e.user.ID, gtNow); err != nil {
			t.Fatal(err)
		}
	}
	upsert(f.l0, row(f.a.ID, model.FinalPresent, 3000, nil))
	upsert(f.l1, row(f.a.ID, model.FinalPresent, 5400, &late), row(f.b.ID, model.FinalAbsent, 0, nil), row(f.c.ID, model.FinalPresent, 600, nil))
	upsert(f.l2, row(f.a.ID, model.FinalAbsent, 0, nil))
	manual(f.l2, f.a.ID, model.FinalPresent)
	// Provisional rows of a lesson that has not happened yet; one teacher decision.
	upsert(f.l3, row(f.a.ID, model.FinalAbsent, 0, nil), row(f.b.ID, model.FinalAbsent, 0, nil))
	manual(f.l3, f.b.ID, model.FinalAbsent)

	e.want(e.do(http.MethodPatch, "/api/students/"+grpID(f.c.ID), map[string]any{"status": "archived"}), http.StatusOK)
	return f
}

func gtReport(t *testing.T, e *gtEnv, groupID int64, query string) (GroupReportDTO, gtResp) {
	t.Helper()
	r := e.do(http.MethodGet, "/api/groups/"+grpID(groupID)+"/report"+query, nil)
	e.want(r, http.StatusOK)
	var rep GroupReportDTO
	r.decode(t, &rep)
	return rep, r
}

func TestGroupReportJSON(t *testing.T) {
	t.Parallel()
	e := newGTEnv(t)
	f := newGTReportFixture(t, e)

	rep, r := gtReport(t, e, f.g.ID, "?from=2026-09-01&to=2026-10-31")
	if rep.Group != (GroupRef{ID: f.g.ID, Name: "Report-1"}) {
		t.Errorf("group %+v", rep.Group)
	}
	// Dates are days in Moscow; "to" is inclusive.
	if !strings.Contains(string(r.body), `"from":"2026-08-31T21:00:00Z"`) || !strings.Contains(string(r.body), `"to":"2026-10-31T20:59:59.999Z"`) {
		t.Errorf("period in %s", r.body)
	}
	var lessonIDs []int64
	for _, l := range rep.Lessons {
		lessonIDs = append(lessonIDs, l.ID)
	}
	if !slices.Equal(lessonIDs, []int64{f.l1.ID, f.l2.ID, f.l3.ID}) {
		t.Fatalf("lessons %+v", rep.Lessons)
	}
	if l := rep.Lessons[2]; l.Title != "Linux #3" || l.Status != model.LessonPlanned || !l.PlannedStartAt.Equal(f.l3.PlannedStartAt) {
		t.Errorf("lesson %+v", l)
	}
	if len(rep.Students) != 3 {
		t.Fatalf("students %+v", rep.Students)
	}
	a, b, c := rep.Students[0], rep.Students[1], rep.Students[2]
	if a.StudentID != f.a.ID || a.FullName != "Иванов Иван" || a.ExternalID != "7" || a.Archived || a.PresentCount != 2 || a.AbsentCount != 0 {
		t.Errorf("a %+v", a)
	}
	if c.StudentID != f.c.ID || !c.Archived || c.PresentCount != 1 {
		t.Errorf("archived student %+v", c)
	}
	if b.PresentCount != 0 || b.AbsentCount != 2 {
		t.Errorf("b %+v", b)
	}
	view := func(s GroupReportStudentDTO) []string {
		var out []string
		for i, c := range s.Cells {
			st := "null"
			if c.FinalStatus != nil {
				st = string(*c.FinalStatus)
			}
			late := "null"
			if c.LateSeconds != nil {
				late = grpID(*c.LateSeconds)
			}
			if c.LessonID != rep.Lessons[i].ID {
				st = "WRONG ORDER"
			}
			out = append(out, st+"/"+map[bool]string{true: "manual", false: "auto"}[c.Manual]+"/"+grpID(c.TotalSeconds)+"/"+late)
		}
		return out
	}
	for _, tt := range []struct {
		s    GroupReportStudentDTO
		want []string
	}{
		{a, []string{"present/auto/5400/120", "present/manual/0/null", "null/auto/0/null"}},
		{b, []string{"absent/auto/0/null", "null/auto/0/null", "absent/manual/0/null"}},
		{c, []string{"present/auto/600/null", "null/auto/0/null", "null/auto/0/null"}},
	} {
		if got := view(tt.s); !slices.Equal(got, tt.want) {
			t.Errorf("%s cells %q, want %q", tt.s.FullName, got, tt.want)
		}
	}
	if !strings.Contains(string(r.body), `"final_status":null`) || !strings.Contains(string(r.body), `"late_seconds":null`) {
		t.Errorf("nulls must be explicit: %s", r.body)
	}

	// Without bounds the period is open ("Всё время" on the report page):
	// every lesson, however old or far ahead, and null bounds.
	old := e.lesson(f.g.ID, "Прошлый год", time.Date(2025, 3, 1, 15, 0, 0, 0, time.UTC), model.LessonFinished)
	far := e.lesson(f.g.ID, "Следующий год", time.Date(2027, 9, 1, 15, 0, 0, 0, time.UTC), model.LessonPlanned)
	rep, r = gtReport(t, e, f.g.ID, "")
	if len(rep.Lessons) != 6 || rep.Lessons[0].ID != old.ID || rep.Lessons[5].ID != far.ID ||
		!strings.Contains(string(r.body), `"from":null`) || !strings.Contains(string(r.body), `"to":null`) {
		t.Errorf("open period: %d lessons, %s", len(rep.Lessons), r.body[:min(len(r.body), 200)])
	}
	// One bound only: the other side stays open.
	rep, r = gtReport(t, e, f.g.ID, "?from=2026-09-20")
	if len(rep.Lessons) != 3 || rep.Lessons[0].ID != f.l2.ID || !strings.Contains(string(r.body), `"from":"2026-09-19T21:00:00Z"`) ||
		!strings.Contains(string(r.body), `"to":null`) {
		t.Errorf("from only: %d lessons, %s", len(rep.Lessons), r.body[:min(len(r.body), 200)])
	}
	rep, r = gtReport(t, e, f.g.ID, "?to=2026-09-15")
	if len(rep.Lessons) != 3 || rep.Lessons[2].ID != f.l1.ID || !strings.Contains(string(r.body), `"from":null`) ||
		!strings.Contains(string(r.body), `"to":"2026-09-15T20:59:59.999Z"`) {
		t.Errorf("to only: %d lessons, %s", len(rep.Lessons), r.body[:min(len(r.body), 200)])
	}

	// RFC 3339 bounds are instants; "to" includes a lesson starting exactly then.
	rep, _ = gtReport(t, e, f.g.ID, "?from="+url.QueryEscape("2026-09-20T00:00:00+03:00")+"&to="+url.QueryEscape("2026-09-22T15:00:00Z"))
	if len(rep.Lessons) != 1 || rep.Lessons[0].ID != f.l2.ID {
		t.Errorf("rfc3339 period: %+v", rep.Lessons)
	}

	// An empty period still lists the students, with no cells.
	rep, r = gtReport(t, e, f.g.ID, "?from=2025-01-01&to=2025-01-31")
	if len(rep.Lessons) != 0 || len(rep.Students) != 2 || !strings.Contains(string(r.body), `"lessons":[]`) || !strings.Contains(string(r.body), `"cells":[]`) {
		t.Errorf("empty period %s", r.body)
	}

	for _, q := range []string{"?from=2026-13-01", "?to=yesterday", "?from=2026-10-02&to=2026-10-01", "?from=01.09.2026"} {
		r := e.do(http.MethodGet, "/api/groups/"+grpID(f.g.ID)+"/report"+q, nil)
		if r.status != http.StatusUnprocessableEntity || r.apiErr(t).Code != CodeValidation {
			t.Errorf("%s: %d %s", q, r.status, r.body)
		}
	}
	e.want(e.do(http.MethodGet, "/api/groups/999/report", nil), http.StatusNotFound)
}

func TestGroupReportExport(t *testing.T) {
	t.Parallel()
	e := newGTEnv(t)
	f := newGTReportFixture(t, e)
	base := "/api/groups/" + grpID(f.g.ID) + "/report/export?from=2026-09-01&to=2026-10-31&format="

	r := e.do(http.MethodGet, base+"xlsx", nil)
	e.want(r, http.StatusOK)
	if ct := r.header.Get("Content-Type"); ct != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Errorf("content type %q", ct)
	}
	cd := r.header.Get("Content-Disposition")
	wantFallback := `attachment; filename="attendance-group-` + grpID(f.g.ID) + `-2026-09-01-2026-10-31.xlsx"; filename*=UTF-8''`
	if !strings.HasPrefix(cd, wantFallback) {
		t.Errorf("disposition %q", cd)
	}
	name, err := url.PathUnescape(strings.TrimPrefix(cd, wantFallback))
	if err != nil || name != "Посещаемость Report-1 01.09.2026–31.10.2026.xlsx" {
		t.Errorf("utf-8 file name %q (%v)", name, err)
	}
	for _, c := range cd {
		if c > 127 {
			t.Fatalf("header must be ASCII: %q", cd)
		}
	}
	x, err := excelize.OpenReader(bytes.NewReader(r.body))
	if err != nil {
		t.Fatalf("xlsx does not open: %v", err)
	}
	defer func() { _ = x.Close() }()
	rows, err := x.GetRows(export.SheetAttendance)
	if err != nil {
		t.Fatal(err)
	}
	flat := ""
	for _, row := range rows {
		flat += strings.Join(row, "|") + "\n"
	}
	for _, want := range []string{"Иванов Иван", "Петров Пётр", "Сидоров Алексей", export.MarkPresent, export.MarkAbsent} {
		if !strings.Contains(flat, want) {
			t.Errorf("xlsx lacks %q:\n%s", want, flat)
		}
	}
	if strings.Contains(flat, "Вводное") {
		t.Errorf("lesson outside the period exported:\n%s", flat)
	}

	r = e.do(http.MethodGet, base+"csv", nil)
	e.want(r, http.StatusOK)
	if ct := r.header.Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Errorf("content type %q", ct)
	}
	if cd := r.header.Get("Content-Disposition"); !strings.Contains(cd, `.csv"; filename*=UTF-8''`) || !strings.HasSuffix(cd, ".csv") {
		t.Errorf("disposition %q", cd)
	}
	body := string(r.body)
	if !strings.HasPrefix(body, "\uFEFF") || !strings.Contains(body, "ФИО;ID студента;") {
		t.Errorf("csv must start with a BOM and use ';': %q", body[:min(len(body), 80)])
	}
	if !strings.Contains(body, "\r\nИванов Иван;7;+;+;;") || !strings.Contains(body, "\r\nПетров Пётр;;"+export.MarkAbsent+";;"+export.MarkAbsent+";") {
		t.Errorf("csv rows:\n%s", body)
	}
	if !strings.Contains(body, ";22.09.2026 18:00 =Linux #2;") {
		t.Errorf("lesson columns are titled in Moscow time:\n%s", body)
	}

	// Default format is xlsx; unknown formats are refused.
	r = e.do(http.MethodGet, "/api/groups/"+grpID(f.g.ID)+"/report/export", nil)
	e.want(r, http.StatusOK)
	if !strings.HasSuffix(r.header.Get("Content-Disposition"), ".xlsx") {
		t.Errorf("default format: %q", r.header.Get("Content-Disposition"))
	}
	e.want(e.do(http.MethodGet, base+"pdf", nil), http.StatusUnprocessableEntity)
	e.want(e.do(http.MethodGet, "/api/groups/999/report/export?format=csv", nil), http.StatusNotFound)
	e.want(e.do(http.MethodGet, "/api/groups/"+grpID(f.g.ID)+"/report/export?format=csv&from=bad", nil), http.StatusUnprocessableEntity)
}

func TestGroupReportFileNames(t *testing.T) {
	t.Parallel()
	if got := grpSafeFilename(` a/b\c:d*e?f"g<h>i|j` + "\x00\u200b  k "); got != "a_b_c_d_e_f_g_h_i_j k" {
		t.Errorf("safe name %q", got)
	}
	if got := grpSafeFilename(" \u200b "); got != "группа" {
		t.Errorf("empty safe name %q", got)
	}
	if got := grpSafeFilename(strings.Repeat("я", 100)); len([]rune(got)) != 80 {
		t.Errorf("long name %d runes", len([]rune(got)))
	}
	if got := grpRFC5987("Отчёт a-b_c.d~e f(1)'%"); got != "%D0%9E%D1%82%D1%87%D1%91%D1%82%20a-b_c.d~e%20f%281%29%27%25" {
		t.Errorf("rfc5987 %q", got)
	}
}

// Lessons that were not held (still planned: no Zoom session) hold only
// provisional rows: their cells are empty unless a teacher decided, and the
// file leaves them out entirely when nobody decided anything. An open period
// is named by the lessons it holds.
func TestGroupReportExportSkipsLessonsNotHeld(t *testing.T) {
	t.Parallel()
	e := newGTEnv(t)
	f := newGTReportFixture(t, e)
	next := e.lesson(f.g.ID, "Linux #4 (ещё не было)", time.Date(2026, 10, 20, 15, 0, 0, 0, time.UTC), model.LessonPlanned)
	missed := e.lesson(f.g.ID, "Linux #2.5 (Zoom не подключился)", time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC), model.LessonPlanned)
	for _, l := range []*model.Lesson{next, missed} {
		if _, err := e.st.UpsertAttendanceComputed(e.ctx, l.ID, []model.Attendance{
			{StudentID: f.a.ID, CurrentStatus: model.CurrentNotSeen, AutoStatus: model.FinalAbsent},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// The JSON keeps every lesson of the period (the page hides planned ones
	// itself), with empty cells for the provisional rows.
	rep, _ := gtReport(t, e, f.g.ID, "")
	if len(rep.Lessons) != 6 || rep.Lessons[3].ID != missed.ID || rep.Lessons[5].ID != next.ID {
		t.Fatalf("json lessons %+v", rep.Lessons)
	}
	a := rep.Students[0]
	if a.StudentID != f.a.ID || a.Cells[3].FinalStatus != nil || a.Cells[5].FinalStatus != nil || a.AbsentCount != 0 || a.PresentCount != 3 {
		t.Errorf("provisional rows counted: %+v", a)
	}

	r := e.do(http.MethodGet, "/api/groups/"+grpID(f.g.ID)+"/report/export?format=csv", nil)
	e.want(r, http.StatusOK)
	body := string(r.body)
	if strings.Contains(body, "Linux #4") || strings.Contains(body, "Linux #2.5") {
		t.Errorf("a lesson that was not held was exported:\n%s", body)
	}
	// Linux #3 is in the future too, but a teacher already marked a student.
	for _, want := range []string{"Вводное", "Linux #1", "Linux #3"} {
		if !strings.Contains(body, want) {
			t.Errorf("csv lacks %q:\n%s", want, body)
		}
	}
	cd := r.header.Get("Content-Disposition")
	wantFallback := `attachment; filename="attendance-group-` + grpID(f.g.ID) + `-2026-08-01-2026-10-13.csv"; filename*=UTF-8''`
	if !strings.HasPrefix(cd, wantFallback) {
		t.Fatalf("disposition %q", cd)
	}
	if name, _ := url.PathUnescape(strings.TrimPrefix(cd, wantFallback)); name != "Посещаемость Report-1 01.08.2026–13.10.2026.csv" {
		t.Errorf("file name %q", name)
	}
}

func TestGroupReportFileNamesOpenPeriod(t *testing.T) {
	t.Parallel()
	day := func(y int, m time.Month, d int) *time.Time {
		v := time.Date(y, m, d, 21, 0, 0, 0, time.UTC) // next day in Moscow
		return &v
	}
	lessons := []*model.Lesson{{PlannedStartAt: *day(2026, 9, 1)}, {PlannedStartAt: *day(2026, 9, 29)}}
	tests := []struct {
		name               string
		from, to           *time.Time
		lessons            []*model.Lesson
		wantName, wantASCI string
	}{
		{"both bounds", day(2026, 8, 31), day(2026, 10, 30), lessons,
			"Посещаемость Г-1 01.09.2026–31.10.2026.xlsx", "attendance-group-7-2026-09-01-2026-10-31.xlsx"},
		{"open, named by lessons", nil, nil, lessons,
			"Посещаемость Г-1 02.09.2026–30.09.2026.xlsx", "attendance-group-7-2026-09-02-2026-09-30.xlsx"},
		{"from only, no lessons", day(2026, 8, 31), nil, nil,
			"Посещаемость Г-1 с 01.09.2026.xlsx", "attendance-group-7-from-2026-09-01.xlsx"},
		{"to only, no lessons", nil, day(2026, 10, 30), nil,
			"Посещаемость Г-1 по 31.10.2026.xlsx", "attendance-group-7-to-2026-10-31.xlsx"},
		{"open, no lessons", nil, nil, nil, "Посещаемость Г-1.xlsx", "attendance-group-7.xlsx"},
	}
	for _, tt := range tests {
		name, ascii := grpReportFileNames(7, "Г-1", tt.from, tt.to, tt.lessons, gtMoscow, "xlsx")
		if name != tt.wantName || ascii != tt.wantASCI {
			t.Errorf("%s: %q / %q, want %q / %q", tt.name, name, ascii, tt.wantName, tt.wantASCI)
		}
	}
}
