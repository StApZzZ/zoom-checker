package export

import (
	"bytes"
	"strings"
	"testing"

	"zoom-checker/internal/model"
)

func reportFixture() GroupReport {
	present, absent := model.FinalPresent, model.FinalAbsent
	return GroupReport{
		GroupName: "DevOps-1",
		Timezone:  msk,
		Lessons: []ReportLesson{
			{ID: 1, Title: "Linux #4", Start: *at("2026-10-06T15:00:00Z")},
			{ID: 2, Title: "", Start: *at("2026-10-13T15:00:00Z")},
			{ID: 3, Title: "Docker", Start: *at("2026-10-20T15:00:00Z")},
		},
		Students: []ReportStudent{
			{FullName: "Иванов Иван", ExternalID: "1", Cells: map[int64]ReportCell{
				1: {Status: present, Seconds: 5000},
				2: {Status: absent, Manual: true},
				3: {Status: present, Seconds: 3600, Manual: true},
			}},
			{FullName: "Петров Пётр", Cells: map[int64]ReportCell{
				2: {Status: present, Seconds: 600},
				3: {Status: absent, Missing: true},
			}},
			{FullName: "=cmd", ExternalID: "-7"}, // no cells at all
		},
	}
}

func TestWriteGroupReportCSV(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := WriteGroupReportCSV(&buf, reportFixture()); err != nil {
		t.Fatal(err)
	}
	want := "\xEF\xBB\xBF" +
		"ФИО;ID студента;06.10.2026 18:00 Linux #4;13.10.2026 18:00;20.10.2026 18:00 Docker;Посещено;Всего занятий;Посещаемость, %\r\n" +
		"Иванов Иван;1;+;\u2212;+;2;3;67\r\n" +
		"Петров Пётр;;;+;;1;1;100\r\n" +
		"'=cmd;'-7;;;;0;0;\r\n" +
		"Присутствовало;;1;1;1;;;\r\n"
	if got := buf.String(); got != want {
		t.Errorf("csv mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestWriteGroupReportXLSX(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := WriteGroupReportXLSX(&buf, reportFixture()); err != nil {
		t.Fatal(err)
	}
	f := openXLSX(t, buf.Bytes())
	if got := f.GetSheetList(); len(got) != 2 || got[0] != SheetAttendance || got[1] != SheetMinutes {
		t.Fatalf("sheets = %q", got)
	}

	rows, err := f.GetRows(SheetAttendance)
	if err != nil {
		t.Fatal(err)
	}
	wantHead := []string{"ФИО", "ID студента", "06.10.2026 18:00\nLinux #4", "13.10.2026 18:00", "20.10.2026 18:00\nDocker", "Посещено", "Всего занятий", "Посещаемость, %"}
	if strings.Join(rows[0], "|") != strings.Join(wantHead, "|") {
		t.Errorf("header = %q", rows[0])
	}
	want := map[int][]string{
		1: {"Иванов Иван", "1", "+", "\u2212", "+", "2", "3", "67"},
		2: {"Петров Пётр", "", "", "+", "", "1", "1", "100"},
		3: {"=cmd", "-7", "", "", "", "0", "0"},
		5: {"Присутствовало", "", "1", "1", "1"},
	}
	for i, w := range want {
		if strings.Join(rows[i], "|") != strings.Join(w, "|") {
			t.Errorf("row %d = %q, want %q", i+1, rows[i], w)
		}
	}
	if len(rows[4]) != 0 {
		t.Errorf("row 5 must be blank, got %q", rows[4])
	}
	if legend := strings.Join(rows[7], ""); !strings.Contains(legend, "вручную") || !strings.Contains(legend, "Время занятий — UTC+3.") {
		t.Errorf("legend row = %q", rows[7])
	}
	assertNumber(t, f, SheetAttendance, "F2", "2")
	assertNumber(t, f, SheetAttendance, "H2", "67")
	assertString(t, f, SheetAttendance, "A4", "=cmd")
	assertString(t, f, SheetAttendance, "B4", "-7")

	// Manual decisions are highlighted.
	id, err := f.GetCellStyle(SheetAttendance, "D2")
	if err != nil {
		t.Fatal(err)
	}
	st, err := f.GetStyle(id)
	if err != nil {
		t.Fatal(err)
	}
	if st.Font == nil || !st.Font.Italic {
		t.Error("manual cell is not italic")
	}
	panes, err := f.GetPanes(SheetAttendance)
	if err != nil {
		t.Fatal(err)
	}
	if !panes.Freeze || panes.XSplit != 2 || panes.YSplit != 1 {
		t.Errorf("panes = %+v", panes)
	}

	mins, err := f.GetRows(SheetMinutes)
	if err != nil {
		t.Fatal(err)
	}
	wantMin := [][]string{
		{"ФИО", "ID студента", "06.10.2026 18:00\nLinux #4", "13.10.2026 18:00", "20.10.2026 18:00\nDocker", "Всего, мин"},
		{"Иванов Иван", "1", "83", "0", "60", "143"},
		{"Петров Пётр", "", "", "10", "", "10"},
		{"=cmd", "-7", "", "", "", "0"},
	}
	if len(mins) != len(wantMin) {
		t.Fatalf("minutes rows = %q", mins)
	}
	for i := range wantMin {
		if strings.Join(mins[i], "|") != strings.Join(wantMin[i], "|") {
			t.Errorf("minutes row %d = %q, want %q", i+1, mins[i], wantMin[i])
		}
	}
	assertNumber(t, f, SheetMinutes, "C2", "83")
}

func TestGroupReport_NoLessons(t *testing.T) {
	t.Parallel()
	r := GroupReport{GroupName: "G", Students: []ReportStudent{{FullName: "Иванов Иван"}}}
	var buf bytes.Buffer
	if err := WriteGroupReportCSV(&buf, r); err != nil {
		t.Fatal(err)
	}
	want := "\xEF\xBB\xBFФИО;ID студента;Посещено;Всего занятий;Посещаемость, %\r\nИванов Иван;;0;0;\r\nПрисутствовало;;;;\r\n"
	if buf.String() != want {
		t.Errorf("got %q\nwant %q", buf.String(), want)
	}
	buf.Reset()
	if err := WriteGroupReportXLSX(&buf, r); err != nil {
		t.Fatal(err)
	}
	openXLSX(t, buf.Bytes())
}
