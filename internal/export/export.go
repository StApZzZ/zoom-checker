// Package export writes lesson attendance and group attendance reports as CSV
// and XLSX (TZ §26). CSV files are made for Russian Excel: UTF-8 with BOM, ';'
// as the delimiter and CRLF line ends. User-supplied text is protected from
// formula injection: in CSV a leading = + - @ TAB CR gets an apostrophe, in
// XLSX every text value is stored as a string cell (never as a formula).
//
// Callers pass every student of the group (scenario 9: including those who
// never connected); rows are written in the given order.
package export

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"zoom-checker/internal/model"
	"zoom-checker/internal/timefmt"
)

// LessonSheet is the data of one lesson export.
type LessonSheet struct {
	GroupName     string
	LessonTitle   string
	PlannedStart  time.Time
	ZoomMeetingID string
	Timezone      *time.Location // nil = UTC
	Rows          []LessonRow
	Unidentified  []ParticipantRow
}

// LessonRow is one student of the group.
type LessonRow struct {
	FullName          string
	ExternalID        string
	FirstJoin         *time.Time
	LastLeave         *time.Time
	TotalSeconds      int64
	LateSeconds       *int64
	EarlyLeaveSeconds *int64
	FinalStatus       model.FinalStatus // "" is written as absent
	Manual            bool
	Comment           string
}

// ParticipantRow is a Zoom participant that is not a student of the group.
// Status may be a resolution or presence code (unidentified, conflict,
// ignored, online, left, ...), which is translated, or any ready text.
type ParticipantRow struct {
	DisplayName  string
	FirstJoin    *time.Time
	LastLeave    *time.Time
	TotalSeconds int64
	Status       string
}

// Sheet names of the lesson workbook.
const (
	SheetAttendance   = "Посещаемость"
	SheetUnidentified = "Не идентифицированы"
	SheetMinutes      = "Минуты"
)

// LessonColumns are the column titles of the lesson export (CSV and XLSX).
// The two clock columns (colFirstJoin, colLastLeave) get the file's UTC offset
// appended, e.g. "Первый вход (UTC+3)": a file passed on to someone else still
// says which zone its times are in (see zonedColumns).
var LessonColumns = []string{
	"ФИО", "ID студента", "Группа", "Занятие", "Дата", colFirstJoin, colLastLeave,
	"Суммарное время, мин", "Опоздание, мин", "Ранний уход, мин",
	"Статус", "Код статуса", "Отмечено вручную", "Комментарий",
}

// Clock column titles; the export appends the zone label to them.
const (
	colFirstJoin = "Первый вход"
	colLastLeave = "Последний выход"
)

// zonedColumns returns titles with " (UTC+3)" added to the clock columns. The
// offset is taken at `at` (the lesson start), so daylight saving is respected.
func zonedColumns(titles []string, loc *time.Location, at time.Time) []string {
	label := " (" + offsetLabel(loc, at) + ")"
	out := make([]string, len(titles))
	for i, t := range titles {
		if t == colFirstJoin || t == colLastLeave {
			t += label
		}
		out[i] = t
	}
	return out
}

// EscapeFormula protects a user-supplied CSV value from being run as a
// formula by spreadsheet software: values starting with = + - @ TAB or CR
// get a leading apostrophe.
func EscapeFormula(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// offsetLabel is timefmt.OffsetLabel at `at`, or now when `at` is unknown (a
// zero time would give the zone's historical local mean time).
func offsetLabel(loc *time.Location, at time.Time) string {
	if at.IsZero() {
		at = time.Now()
	}
	return timefmt.OffsetLabel(loc, at)
}

// Minutes truncates seconds to whole minutes, the same rule the UI uses
// (formatDuration/formatLate in web/src/lib/format.ts), so a file and the
// screen show identical numbers: 59 s is 0 min, 90 s is 1 min.
func Minutes(seconds int64) int64 {
	if seconds <= 0 {
		return 0
	}
	return seconds / 60
}

// StatusText is the Russian label of a final status.
func StatusText(s model.FinalStatus) string {
	switch s {
	case model.FinalPresent:
		return "присутствовал"
	case model.FinalAbsent, "":
		return "отсутствовал"
	}
	return string(s)
}

func statusCode(s model.FinalStatus) string {
	if s == "" {
		return string(model.FinalAbsent)
	}
	return string(s)
}

func yesNo(b bool) string {
	if b {
		return "да"
	}
	return "нет"
}

var participantStatusText = map[string]string{
	string(model.ResolutionUnidentified): "Не идентифицирован",
	string(model.ResolutionConflict):     "Подходит нескольким студентам",
	string(model.ResolutionIgnored):      "Скрыт",
	string(model.ResolutionMatched):      "Сопоставлен",
	string(model.ResolutionManual):       "Сопоставлен вручную",
	string(model.CurrentOnline):          "В Zoom",
	string(model.CurrentLeft):            "Вышел",
}

func participantStatus(s string) string {
	if t, ok := participantStatusText[s]; ok {
		return t
	}
	return s
}

func location(tz *time.Location) *time.Location {
	if tz == nil {
		return time.UTC
	}
	return tz
}

const (
	dateLayout     = timefmt.DateLayout
	timeLayout     = timefmt.ClockLayout
	dateTimeLayout = timefmt.DateTimeLayout
)

func formatTime(t *time.Time, loc *time.Location) string {
	if t == nil {
		return ""
	}
	return t.In(loc).Format(timeLayout)
}

func optMinutes(sec *int64) string {
	if sec == nil {
		return ""
	}
	return strconv.FormatInt(Minutes(*sec), 10)
}

// wallClock re-expresses t's local wall clock in loc as a UTC time, because
// excelize converts time.Time values to Excel serials by absolute instant and
// Excel cells have no time zone.
func wallClock(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}

func dateOnly(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// newCSV writes the UTF-8 BOM and returns a writer configured for Russian Excel.
func newCSV(w io.Writer) (*csv.Writer, error) {
	if _, err := io.WriteString(w, "\uFEFF"); err != nil {
		return nil, fmt.Errorf("write bom: %w", err)
	}
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	cw.UseCRLF = true
	return cw, nil
}

func flushCSV(cw *csv.Writer) error {
	cw.Flush()
	if err := cw.Error(); err != nil {
		return fmt.Errorf("write csv: %w", err)
	}
	return nil
}

// book wraps an excelize file and keeps the first error, so that writing many
// cells does not need an error check per call.
type book struct {
	f   *excelize.File
	err error

	header, text, center, integer, date, clock, manual, legend int
}

func newBook() *book {
	b := &book{f: excelize.NewFile()}
	border := []excelize.Border{{Type: "bottom", Color: "808080", Style: 1}}
	b.header = b.style(&excelize.Style{
		Font:      &excelize.Font{Bold: true},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"EDEDED"}},
		Border:    border,
		Alignment: &excelize.Alignment{WrapText: true, Vertical: "center"},
	})
	b.text = b.style(&excelize.Style{Alignment: &excelize.Alignment{Vertical: "top"}})
	b.center = b.style(&excelize.Style{Alignment: &excelize.Alignment{Horizontal: "center"}})
	b.integer = b.style(&excelize.Style{NumFmt: 1})
	b.date = b.style(&excelize.Style{CustomNumFmt: ptr("dd.mm.yyyy")})
	b.clock = b.style(&excelize.Style{CustomNumFmt: ptr("hh:mm")})
	b.manual = b.style(&excelize.Style{
		Font:      &excelize.Font{Italic: true},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"FFF2CC"}},
		Alignment: &excelize.Alignment{Horizontal: "center"},
	})
	b.legend = b.style(&excelize.Style{Font: &excelize.Font{Italic: true, Color: "595959"}})
	return b
}

// props sets the workbook metadata shown in Excel's file properties.
func (b *book) props(title, subject, description string) {
	if b.err == nil {
		b.err = b.f.SetDocProps(&excelize.DocProperties{
			Title:       title,
			Subject:     subject,
			Description: description,
			Creator:     "Zoom Checker",
			Language:    "ru-RU",
		})
	}
}

func ptr[T any](v T) *T { return &v }

func (b *book) style(s *excelize.Style) int {
	if b.err != nil {
		return 0
	}
	id, err := b.f.NewStyle(s)
	if err != nil {
		b.err = fmt.Errorf("new style: %w", err)
	}
	return id
}

// sheet creates (or renames the default sheet to) name.
func (b *book) sheet(name string) {
	if b.err != nil {
		return
	}
	if list := b.f.GetSheetList(); len(list) == 1 && list[0] == "Sheet1" {
		b.err = b.f.SetSheetName("Sheet1", name)
		return
	}
	if _, err := b.f.NewSheet(name); err != nil {
		b.err = fmt.Errorf("new sheet: %w", err)
	}
}

func cellName(col, row int) string {
	name, _ := excelize.CoordinatesToCellName(col, row)
	return name
}

func colName(col int) string {
	name, _ := excelize.ColumnNumberToName(col)
	return name
}

// str writes a string cell; strings are never evaluated as formulas.
func (b *book) str(sheet string, col, row int, v string) {
	if b.err == nil && v != "" {
		b.err = b.f.SetCellStr(sheet, cellName(col, row), v)
	}
}

func (b *book) int(sheet string, col, row int, v int64) {
	if b.err == nil {
		b.err = b.f.SetCellInt(sheet, cellName(col, row), v)
	}
}

func (b *book) time(sheet string, col, row int, t time.Time) {
	if b.err == nil {
		b.err = b.f.SetCellValue(sheet, cellName(col, row), t)
	}
}

func (b *book) styleRange(sheet string, col1, row1, col2, row2, style int) {
	if b.err == nil && row2 >= row1 && col2 >= col1 {
		b.err = b.f.SetCellStyle(sheet, cellName(col1, row1), cellName(col2, row2), style)
	}
}

// headerRow writes bold titles in row 1, freezes it (and the first freezeCols
// columns) and adds an autofilter down to lastRow.
func (b *book) headerRow(sheet string, titles []string, lastRow, freezeCols int) {
	for i, t := range titles {
		b.str(sheet, i+1, 1, t)
	}
	b.styleRange(sheet, 1, 1, len(titles), 1, b.header)
	if b.err != nil {
		return
	}
	b.err = b.f.SetPanes(sheet, &excelize.Panes{
		Freeze:      true,
		XSplit:      freezeCols,
		YSplit:      1,
		TopLeftCell: cellName(freezeCols+1, 2),
		ActivePane:  map[bool]string{true: "bottomRight", false: "bottomLeft"}[freezeCols > 0],
	})
	if b.err == nil {
		ref := cellName(1, 1) + ":" + cellName(len(titles), max(lastRow, 1))
		b.err = b.f.AutoFilter(sheet, ref, nil)
	}
}

func (b *book) widths(sheet string, widths []float64) {
	for i, w := range widths {
		if b.err != nil {
			return
		}
		c := colName(i + 1)
		b.err = b.f.SetColWidth(sheet, c, c, w)
	}
}

func (b *book) write(w io.Writer) error {
	defer func() { _ = b.f.Close() }()
	if b.err != nil {
		return fmt.Errorf("build xlsx: %w", b.err)
	}
	b.f.SetActiveSheet(0)
	if err := b.f.Write(w); err != nil {
		return fmt.Errorf("write xlsx: %w", err)
	}
	return nil
}
