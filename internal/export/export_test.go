package export

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"zoom-checker/internal/model"
)

var msk = time.FixedZone("MSK", 3*3600)

func at(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &t
}

func sec(v int64) *int64 { return &v }

func lessonFixture() LessonSheet {
	return LessonSheet{
		GroupName:     "DevOps-1",
		LessonTitle:   "Linux #4",
		PlannedStart:  *at("2026-10-06T15:00:00Z"),
		ZoomMeetingID: "85746065432",
		Timezone:      msk,
		Rows: []LessonRow{
			{FullName: "Иванов Иван", ExternalID: "1", FirstJoin: at("2026-10-06T15:02:10Z"), LastLeave: at("2026-10-06T16:31:40Z"),
				TotalSeconds: 84*60 + 29, LateSeconds: sec(130), FinalStatus: model.FinalPresent},
			{FullName: "Петров Пётр", FirstJoin: at("2026-10-06T15:40:00Z"), LastLeave: at("2026-10-06T15:49:00Z"),
				TotalSeconds: 540, LateSeconds: sec(2400), EarlyLeaveSeconds: sec(2490), FinalStatus: model.FinalPresent,
				Manual: true, Comment: "Был с телефона; плохая связь"},
			{FullName: "Сидоров Алексей", ExternalID: "3", FinalStatus: model.FinalAbsent},
			{FullName: "Кузнецова Мария", ExternalID: "4"}, // no status yet: written as absent
		},
		Unidentified: []ParticipantRow{
			{DisplayName: "iPhone", FirstJoin: at("2026-10-06T15:05:00Z"), LastLeave: at("2026-10-06T15:35:00Z"), TotalSeconds: 1800, Status: "unidentified"},
			{DisplayName: "Иванов", TotalSeconds: 0, Status: "Ручной текст"},
		},
	}
}

func TestWriteLessonCSV(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := WriteLessonCSV(&buf, lessonFixture()); err != nil {
		t.Fatal(err)
	}
	want := "\xEF\xBB\xBF" +
		"ФИО;ID студента;Группа;Занятие;Дата;Первый вход (UTC+3);Последний выход (UTC+3);Суммарное время, мин;Опоздание, мин;Ранний уход, мин;Статус;Код статуса;Отмечено вручную;Комментарий\r\n" +
		"Иванов Иван;1;DevOps-1;Linux #4;06.10.2026;18:02;19:31;84;2;;присутствовал;present;нет;\r\n" +
		"Петров Пётр;;DevOps-1;Linux #4;06.10.2026;18:40;18:49;9;40;41;присутствовал;present;да;\"Был с телефона; плохая связь\"\r\n" +
		"Сидоров Алексей;3;DevOps-1;Linux #4;06.10.2026;;;0;;;отсутствовал;absent;нет;\r\n" +
		"Кузнецова Мария;4;DevOps-1;Linux #4;06.10.2026;;;0;;;отсутствовал;absent;нет;\r\n"
	if got := buf.String(); got != want {
		t.Errorf("csv mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestWriteLessonCSV_EmptyAndUTC(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	s := LessonSheet{GroupName: "G", LessonTitle: "L", PlannedStart: *at("2026-10-06T22:30:00Z")}
	if err := WriteLessonCSV(&buf, s); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimPrefix(buf.String(), "\xEF\xBB\xBF"), "\r\n")
	if len(lines) != 2 || lines[1] != "" || !strings.HasPrefix(lines[0], "ФИО;") {
		t.Errorf("unexpected output %q", buf.String())
	}
}

func TestEscapeFormula(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"=1+1":          "'=1+1",
		"+7 999":        "'+7 999",
		"-5":            "'-5",
		"@SUM(A1)":      "'@SUM(A1)",
		"\tx":           "'\tx",
		"\rx":           "'\rx",
		"":              "",
		"Иванов Иван":   "Иванов Иван",
		"Иванов-Петров": "Иванов-Петров",
		" =still text":  " =still text",
		"\u2212":        "\u2212",
	}
	for in, want := range tests {
		if got := EscapeFormula(in); got != want {
			t.Errorf("EscapeFormula(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestMinutes pins the rounding rule: whole minutes, rounded down. It must
// match formatDuration/formatLate in web/src/lib/format.ts (format.test.ts has
// the same table), so a file and the screen never differ by a minute.
func TestMinutes(t *testing.T) {
	t.Parallel()
	tests := map[int64]int64{0: 0, 29: 0, 30: 0, 45: 0, 49: 0, 59: 0, 60: 1, 89: 1, 90: 1, 96: 1, 5069: 84, 5070: 84, -90: 0}
	for in, want := range tests {
		if got := Minutes(in); got != want {
			t.Errorf("Minutes(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestLessonCSV_FormulaInjection(t *testing.T) {
	t.Parallel()
	s := LessonSheet{
		GroupName: "@group", LessonTitle: "=HYPERLINK(\"http://x\")", PlannedStart: *at("2026-10-06T15:00:00Z"),
		Rows: []LessonRow{{FullName: "=cmd|' /C calc'!A0", ExternalID: "+7", Comment: "-1+1", FinalStatus: model.FinalAbsent}},
	}
	var buf bytes.Buffer
	if err := WriteLessonCSV(&buf, s); err != nil {
		t.Fatal(err)
	}
	row := strings.Split(buf.String(), "\r\n")[1]
	want := `'=cmd|' /C calc'!A0;'+7;'@group;"'=HYPERLINK(""http://x"")";06.10.2026;;;0;;;отсутствовал;absent;нет;'-1+1`
	if row != want {
		t.Errorf("row:\n got %q\nwant %q", row, want)
	}
}

func openXLSX(t *testing.T, data []byte) *excelize.File {
	t.Helper()
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("open xlsx: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func cellValue(t *testing.T, f *excelize.File, sheet, ref string) string {
	t.Helper()
	v, err := f.GetCellValue(sheet, ref)
	if err != nil {
		t.Fatalf("GetCellValue %s!%s: %v", sheet, ref, err)
	}
	return v
}

func assertNumber(t *testing.T, f *excelize.File, sheet, ref, want string) {
	t.Helper()
	typ, err := f.GetCellType(sheet, ref)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := f.GetCellValue(sheet, ref, excelize.Options{RawCellValue: true})
	if err != nil {
		t.Fatal(err)
	}
	if typ != excelize.CellTypeUnset && typ != excelize.CellTypeNumber {
		t.Errorf("%s!%s type = %v, want a number", sheet, ref, typ)
	}
	if raw != want {
		t.Errorf("%s!%s raw = %q, want %q", sheet, ref, raw, want)
	}
}

func assertString(t *testing.T, f *excelize.File, sheet, ref, want string) {
	t.Helper()
	typ, err := f.GetCellType(sheet, ref)
	if err != nil {
		t.Fatal(err)
	}
	if typ != excelize.CellTypeSharedString && typ != excelize.CellTypeInlineString {
		t.Errorf("%s!%s type = %v, want a string", sheet, ref, typ)
	}
	formula, err := f.GetCellFormula(sheet, ref)
	if err != nil {
		t.Fatal(err)
	}
	if formula != "" {
		t.Errorf("%s!%s has formula %q", sheet, ref, formula)
	}
	if got := cellValue(t, f, sheet, ref); got != want {
		t.Errorf("%s!%s = %q, want %q", sheet, ref, got, want)
	}
}

func TestWriteLessonXLSX(t *testing.T) {
	t.Parallel()
	s := lessonFixture()
	s.Rows = append(s.Rows, LessonRow{FullName: "=1+1", ExternalID: "@id", Comment: "+comment", FinalStatus: model.FinalAbsent})
	var buf bytes.Buffer
	if err := WriteLessonXLSX(&buf, s); err != nil {
		t.Fatal(err)
	}
	f := openXLSX(t, buf.Bytes())

	if got := f.GetSheetList(); len(got) != 2 || got[0] != SheetAttendance || got[1] != SheetUnidentified {
		t.Fatalf("sheets = %q", got)
	}
	rows, err := f.GetRows(SheetAttendance)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1+len(s.Rows) {
		t.Fatalf("got %d rows, want %d (all students incl. never connected)", len(rows), 1+len(s.Rows))
	}
	wantHead := zonedColumns(LessonColumns, msk, s.PlannedStart)
	if strings.Join(rows[0], "|") != strings.Join(wantHead, "|") || rows[0][5] != "Первый вход (UTC+3)" || rows[0][6] != "Последний выход (UTC+3)" {
		t.Errorf("header = %q", rows[0])
	}
	wantRow2 := []string{"Иванов Иван", "1", "DevOps-1", "Linux #4", "06.10.2026", "18:02", "19:31", "84", "2", "", "присутствовал", "present", "нет"}
	if strings.Join(rows[1], "|") != strings.Join(wantRow2, "|") {
		t.Errorf("row 2 = %q\nwant    %q", rows[1], wantRow2)
	}
	if got := rows[3]; got[0] != "Сидоров Алексей" || got[5] != "" || got[7] != "0" || got[10] != "отсутствовал" {
		t.Errorf("never connected row = %q", got)
	}
	if got := rows[2][13]; got != "Был с телефона; плохая связь" {
		t.Errorf("comment = %q", got)
	}

	// Numbers are numbers, the date is a real date serial.
	assertNumber(t, f, SheetAttendance, "H2", "84")
	assertNumber(t, f, SheetAttendance, "I3", "40")
	assertNumber(t, f, SheetAttendance, "J3", "41")    // 2490 s: rounded down, as on the screen
	assertNumber(t, f, SheetAttendance, "E2", "46301") // 2026-10-06
	raw, _ := f.GetCellValue(SheetAttendance, "F2", excelize.Options{RawCellValue: true})
	if !strings.HasPrefix(raw, "46301.75") { // 18:02 local wall clock
		t.Errorf("F2 raw = %q, want 46301.75…", raw)
	}

	// Formula injection: user text stays a plain string cell.
	assertString(t, f, SheetAttendance, "A6", "=1+1")
	assertString(t, f, SheetAttendance, "B6", "@id")
	assertString(t, f, SheetAttendance, "N6", "+comment")

	// Bold header, frozen header row, autofilter.
	styleID, err := f.GetCellStyle(SheetAttendance, "A1")
	if err != nil {
		t.Fatal(err)
	}
	st, err := f.GetStyle(styleID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Font == nil || !st.Font.Bold {
		t.Error("header is not bold")
	}
	panes, err := f.GetPanes(SheetAttendance)
	if err != nil {
		t.Fatal(err)
	}
	if !panes.Freeze || panes.YSplit != 1 {
		t.Errorf("panes = %+v", panes)
	}
	foundFilter := false
	for _, dn := range f.GetDefinedName() {
		if dn.Name == "_xlnm._FilterDatabase" && dn.Scope == SheetAttendance && strings.Contains(dn.RefersTo, "$A$1:$N$6") {
			foundFilter = true
		}
	}
	if !foundFilter {
		t.Errorf("no autofilter on %s: %+v", SheetAttendance, f.GetDefinedName())
	}
	if w, _ := f.GetColWidth(SheetAttendance, "A"); w < 20 {
		t.Errorf("name column width = %v", w)
	}

	un, err := f.GetRows(SheetUnidentified)
	if err != nil {
		t.Fatal(err)
	}
	wantUn := [][]string{
		{"Имя в Zoom", "Первый вход (UTC+3)", "Последний выход (UTC+3)", "Суммарное время, мин", "Статус"},
		{"iPhone", "18:05", "18:35", "30", "Не идентифицирован"},
		{"Иванов", "", "", "0", "Ручной текст"},
	}
	if len(un) != len(wantUn) {
		t.Fatalf("unidentified rows = %q", un)
	}
	for i := range wantUn {
		if strings.Join(un[i], "|") != strings.Join(wantUn[i], "|") {
			t.Errorf("unidentified row %d = %q, want %q", i+1, un[i], wantUn[i])
		}
	}
	props, err := f.GetDocProps()
	if err != nil {
		t.Fatal(err)
	}
	if props.Title != "Посещаемость: Linux #4" || props.Description != "Zoom: 85746065432; Время: UTC+3 (MSK)" {
		t.Errorf("doc props = %+v", props)
	}
}

func TestWriteLessonXLSX_Empty(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := WriteLessonXLSX(&buf, LessonSheet{GroupName: "G"}); err != nil {
		t.Fatal(err)
	}
	f := openXLSX(t, buf.Bytes())
	rows, err := f.GetRows(SheetAttendance)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %q, err = %v", rows, err)
	}
}

type failWriter struct{}

var errWrite = errors.New("disk full")

func (failWriter) Write([]byte) (int, error) { return 0, errWrite }

func TestWriteErrorsPropagate(t *testing.T) {
	t.Parallel()
	s := lessonFixture()
	r := reportFixture()
	for name, fn := range map[string]func() error{
		"lesson csv":  func() error { return WriteLessonCSV(failWriter{}, s) },
		"lesson xlsx": func() error { return WriteLessonXLSX(failWriter{}, s) },
		"report csv":  func() error { return WriteGroupReportCSV(failWriter{}, r) },
		"report xlsx": func() error { return WriteGroupReportXLSX(failWriter{}, r) },
	} {
		if err := fn(); !errors.Is(err, errWrite) {
			t.Errorf("%s: err = %v, want %v", name, err, errWrite)
		}
	}
}

// A lesson or report without a start time must not show 01.01.0001.
func TestZeroTimesAreBlank(t *testing.T) {
	t.Parallel()
	s := LessonSheet{GroupName: "G", LessonTitle: "L", Rows: []LessonRow{{FullName: "Иванов Иван", FinalStatus: model.FinalPresent}}}
	var buf bytes.Buffer
	if err := WriteLessonCSV(&buf, s); err != nil {
		t.Fatal(err)
	}
	if row := strings.Split(buf.String(), "\r\n")[1]; row != "Иванов Иван;;G;L;;;;0;;;присутствовал;present;нет;" {
		t.Errorf("csv row = %q", row)
	}
	buf.Reset()
	if err := WriteLessonXLSX(&buf, s); err != nil {
		t.Fatal(err)
	}
	if v := cellValue(t, openXLSX(t, buf.Bytes()), SheetAttendance, "E2"); v != "" {
		t.Errorf("xlsx date = %q, want blank", v)
	}

	r := GroupReport{Lessons: []ReportLesson{{ID: 1, Title: "=Без даты"}}, Students: []ReportStudent{{FullName: "Иванов Иван"}}}
	buf.Reset()
	if err := WriteGroupReportCSV(&buf, r); err != nil {
		t.Fatal(err)
	}
	if head := strings.Split(strings.TrimPrefix(buf.String(), "\xEF\xBB\xBF"), "\r\n")[0]; head != "ФИО;ID студента;'=Без даты;Посещено;Всего занятий;Посещаемость, %" {
		t.Errorf("report header = %q", head)
	}
}

// TestLessonExportZone: the file's times and its clock column titles follow
// the zone it was asked for (the teacher's browser zone), so a file shared
// with someone else still says which zone its times are in.
func TestLessonExportZone(t *testing.T) {
	t.Parallel()
	ekb, err := time.LoadLocation("Asia/Yekaterinburg")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	s := lessonFixture()
	s.Timezone = ekb
	var buf bytes.Buffer
	if err := WriteLessonCSV(&buf, s); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimPrefix(buf.String(), "\xEF\xBB\xBF"), "\r\n")
	head, first := strings.Split(lines[0], ";"), strings.Split(lines[1], ";")
	if head[5] != "Первый вход (UTC+5)" || head[6] != "Последний выход (UTC+5)" {
		t.Errorf("header = %q", head)
	}
	if first[5] != "20:02" || first[6] != "21:31" {
		t.Errorf("times = %q %q, want 20:02 21:31", first[5], first[6])
	}

	// No zone at all is UTC, and says so.
	s.Timezone = nil
	buf.Reset()
	if err := WriteLessonCSV(&buf, s); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), ";Первый вход (UTC);Последний выход (UTC);") {
		t.Errorf("UTC header missing: %q", strings.SplitN(buf.String(), "\r\n", 2)[0])
	}
}
