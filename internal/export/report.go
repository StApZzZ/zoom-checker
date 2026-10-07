package export

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"zoom-checker/internal/model"
)

// GroupReport is the attendance matrix of a group over a period.
type GroupReport struct {
	GroupName string
	Timezone  *time.Location // nil = UTC
	Lessons   []ReportLesson
	Students  []ReportStudent
}

// ReportLesson is one column of the matrix.
type ReportLesson struct {
	ID    int64
	Title string
	Start time.Time
}

// ReportStudent is one row of the matrix; Cells is keyed by ReportLesson.ID.
type ReportStudent struct {
	FullName   string
	ExternalID string
	Cells      map[int64]ReportCell
}

// ReportCell is a student's result for one lesson. A cell absent from the map
// or with Missing set means there is no attendance record (e.g. the student
// joined the group later) and is left blank and not counted.
type ReportCell struct {
	Status  model.FinalStatus
	Seconds int64
	Manual  bool
	Missing bool
}

// Matrix marks.
const (
	MarkPresent = "+"
	MarkAbsent  = "\u2212" // minus sign: never read as a formula or a negative number
)

// Total column titles.
const (
	colAttended = "Посещено"
	colLessons  = "Всего занятий"
	colPercent  = "Посещаемость, %"
	colMinutes  = "Всего, мин"
	rowPresent  = "Присутствовало"
)

const reportLegend = "+ присутствовал, \u2212 отсутствовал, пусто — нет данных (студента ещё не было в группе). Жёлтым курсивом — отмечено вручную."

func (s ReportStudent) cell(id int64) (ReportCell, bool) {
	c, ok := s.Cells[id]
	if !ok || c.Missing {
		return ReportCell{}, false
	}
	return c, true
}

func mark(c ReportCell) string {
	if c.Status == model.FinalPresent {
		return MarkPresent
	}
	return MarkAbsent
}

type studentTotals struct{ attended, lessons, seconds int64 }

func (t studentTotals) percent() (int64, bool) {
	if t.lessons == 0 {
		return 0, false
	}
	return int64(float64(t.attended)*100/float64(t.lessons) + 0.5), true
}

func (r GroupReport) totals(s ReportStudent) studentTotals {
	var t studentTotals
	for _, l := range r.Lessons {
		c, ok := s.cell(l.ID)
		if !ok {
			continue
		}
		t.lessons++
		t.seconds += c.Seconds
		if c.Status == model.FinalPresent {
			t.attended++
		}
	}
	return t
}

// presentCounts is the number of present students per lesson.
func (r GroupReport) presentCounts() []int64 {
	out := make([]int64, len(r.Lessons))
	for i, l := range r.Lessons {
		for _, s := range r.Students {
			if c, ok := s.cell(l.ID); ok && c.Status == model.FinalPresent {
				out[i]++
			}
		}
	}
	return out
}

func lessonTitle(l ReportLesson, loc *time.Location, sep string) string {
	var parts []string
	if !l.Start.IsZero() {
		parts = append(parts, l.Start.In(loc).Format(dateTimeLayout))
	}
	if l.Title != "" {
		parts = append(parts, l.Title)
	}
	return strings.Join(parts, sep)
}

// WriteGroupReportCSV writes the matrix student × lesson ("+" present,
// "−" (U+2212) absent, blank = no data) with totals, for Russian Excel.
func WriteGroupReportCSV(w io.Writer, r GroupReport) error {
	cw, err := newCSV(w)
	if err != nil {
		return err
	}
	loc := location(r.Timezone)
	head := []string{"ФИО", "ID студента"}
	for _, l := range r.Lessons {
		head = append(head, EscapeFormula(lessonTitle(l, loc, " ")))
	}
	head = append(head, colAttended, colLessons, colPercent)
	if err := cw.Write(head); err != nil {
		return fmt.Errorf("write csv: %w", err)
	}
	for _, s := range r.Students {
		rec := []string{EscapeFormula(s.FullName), EscapeFormula(s.ExternalID)}
		for _, l := range r.Lessons {
			v := ""
			if c, ok := s.cell(l.ID); ok {
				v = mark(c)
			}
			rec = append(rec, v)
		}
		t := r.totals(s)
		pct := ""
		if p, ok := t.percent(); ok {
			pct = strconv.FormatInt(p, 10)
		}
		rec = append(rec, strconv.FormatInt(t.attended, 10), strconv.FormatInt(t.lessons, 10), pct)
		if err := cw.Write(rec); err != nil {
			return fmt.Errorf("write csv: %w", err)
		}
	}
	foot := []string{rowPresent, ""}
	for _, n := range r.presentCounts() {
		foot = append(foot, strconv.FormatInt(n, 10))
	}
	foot = append(foot, "", "", "")
	if err := cw.Write(foot); err != nil {
		return fmt.Errorf("write csv: %w", err)
	}
	return flushCSV(cw)
}

// WriteGroupReportXLSX writes the matrix as an Excel workbook: sheet
// "Посещаемость" with "+"/"−" marks (manual decisions highlighted), totals
// and a per-lesson count row, and sheet "Минуты" with minutes per lesson.
func WriteGroupReportXLSX(w io.Writer, r GroupReport) error {
	loc := location(r.Timezone)
	b := newBook()
	var first time.Time
	if len(r.Lessons) > 0 {
		first = r.Lessons[0].Start
	}
	b.props("Отчёт о посещаемости: "+r.GroupName, r.GroupName, zoneDescription(loc, first))

	nl := len(r.Lessons)
	lessonCols := make([]string, nl)
	for i, l := range r.Lessons {
		lessonCols[i] = lessonTitle(l, loc, "\n")
	}
	nameWidths := []float64{34, 14}
	lessonWidths := make([]float64, nl)
	for i := range lessonWidths {
		lessonWidths[i] = 13
	}
	nameHead := []string{"ФИО", "ID студента"}
	last := len(r.Students) + 1

	// Sheet 1: marks.
	const sh = SheetAttendance
	b.sheet(sh)
	head := slices.Concat(nameHead, lessonCols, []string{colAttended, colLessons, colPercent})
	b.headerRow(sh, head, last, 2)
	for i, s := range r.Students {
		row := i + 2
		b.str(sh, 1, row, s.FullName)
		b.str(sh, 2, row, s.ExternalID)
		for j, l := range r.Lessons {
			c, ok := s.cell(l.ID)
			if !ok {
				continue
			}
			b.str(sh, j+3, row, mark(c))
			if c.Manual {
				b.styleRange(sh, j+3, row, j+3, row, b.manual)
			} else {
				b.styleRange(sh, j+3, row, j+3, row, b.center)
			}
		}
		t := r.totals(s)
		b.int(sh, nl+3, row, t.attended)
		b.int(sh, nl+4, row, t.lessons)
		if p, ok := t.percent(); ok {
			b.int(sh, nl+5, row, p)
		}
	}
	if len(r.Students) > 0 {
		b.styleRange(sh, nl+3, 2, nl+5, last, b.integer)
	}
	foot := last + 2 // one blank row keeps the totals out of the autofilter range
	b.str(sh, 1, foot, rowPresent)
	for j, n := range r.presentCounts() {
		b.int(sh, j+3, foot, n)
	}
	b.styleRange(sh, 1, foot, nl+2, foot, b.header)
	b.str(sh, 1, foot+2, reportLegend+" Время занятий — "+offsetLabel(loc, first)+".")
	b.styleRange(sh, 1, foot+2, 1, foot+2, b.legend)
	b.widths(sh, slices.Concat(nameWidths, lessonWidths, []float64{11, 11, 13}))
	b.headerHeight(sh, nl)

	// Sheet 2: minutes.
	const mn = SheetMinutes
	b.sheet(mn)
	headM := slices.Concat(nameHead, lessonCols, []string{colMinutes})
	b.headerRow(mn, headM, last, 2)
	for i, s := range r.Students {
		row := i + 2
		b.str(mn, 1, row, s.FullName)
		b.str(mn, 2, row, s.ExternalID)
		for j, l := range r.Lessons {
			if c, ok := s.cell(l.ID); ok {
				b.int(mn, j+3, row, Minutes(c.Seconds))
			}
		}
		b.int(mn, nl+3, row, Minutes(r.totals(s).seconds))
	}
	if len(r.Students) > 0 {
		b.styleRange(mn, 3, 2, nl+3, last, b.integer)
	}
	b.widths(mn, slices.Concat(nameWidths, lessonWidths, []float64{12}))
	b.headerHeight(mn, nl)
	return b.write(w)
}

// headerHeight makes room for two-line lesson titles.
func (b *book) headerHeight(sheet string, lessons int) {
	if b.err == nil && lessons > 0 {
		b.err = b.f.SetRowHeight(sheet, 1, 45)
	}
}
