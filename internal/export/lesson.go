package export

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// WriteLessonCSV writes the attendance of one lesson as CSV for Russian Excel
// (UTF-8 BOM, ';', CRLF) with LessonColumns. Times are in s.Timezone, and the
// clock column titles name its UTC offset.
func WriteLessonCSV(w io.Writer, s LessonSheet) error {
	cw, err := newCSV(w)
	if err != nil {
		return err
	}
	loc := location(s.Timezone)
	date := ""
	if !s.PlannedStart.IsZero() {
		date = s.PlannedStart.In(loc).Format(dateLayout)
	}
	group, title := EscapeFormula(s.GroupName), EscapeFormula(s.LessonTitle)
	if err := cw.Write(zonedColumns(LessonColumns, loc, s.PlannedStart)); err != nil {
		return fmt.Errorf("write csv: %w", err)
	}
	for _, r := range s.Rows {
		rec := []string{
			EscapeFormula(r.FullName),
			EscapeFormula(r.ExternalID),
			group,
			title,
			date,
			formatTime(r.FirstJoin, loc),
			formatTime(r.LastLeave, loc),
			strconv.FormatInt(Minutes(r.TotalSeconds), 10),
			optMinutes(r.LateSeconds),
			optMinutes(r.EarlyLeaveSeconds),
			StatusText(r.FinalStatus),
			statusCode(r.FinalStatus),
			yesNo(r.Manual),
			EscapeFormula(r.Comment),
		}
		if err := cw.Write(rec); err != nil {
			return fmt.Errorf("write csv: %w", err)
		}
	}
	return flushCSV(cw)
}

// UnidentifiedColumns are the column titles of the "Не идентифицированы" sheet.
// The clock columns get the zone label, as in LessonColumns.
var UnidentifiedColumns = []string{"Имя в Zoom", colFirstJoin, colLastLeave, "Суммарное время, мин", "Статус"}

// WriteLessonXLSX writes the attendance of one lesson as an Excel workbook:
// sheet "Посещаемость" with LessonColumns (minutes as numbers, date and times
// as real Excel values) and sheet "Не идентифицированы" with Zoom participants
// that are not students of the group.
func WriteLessonXLSX(w io.Writer, s LessonSheet) error {
	loc := location(s.Timezone)
	b := newBook()
	b.props("Посещаемость: "+s.LessonTitle, s.GroupName,
		joinNonEmpty("; ", zoomDescription(s.ZoomMeetingID), zoneDescription(loc, s.PlannedStart)))

	const sh = SheetAttendance
	b.sheet(sh)
	last := len(s.Rows) + 1
	b.headerRow(sh, zonedColumns(LessonColumns, loc, s.PlannedStart), last, 1)
	day := dateOnly(s.PlannedStart, loc)
	for i, r := range s.Rows {
		row := i + 2
		b.str(sh, 1, row, r.FullName)
		b.str(sh, 2, row, r.ExternalID)
		b.str(sh, 3, row, s.GroupName)
		b.str(sh, 4, row, s.LessonTitle)
		if !s.PlannedStart.IsZero() {
			b.time(sh, 5, row, day)
		}
		if r.FirstJoin != nil {
			b.time(sh, 6, row, wallClock(*r.FirstJoin, loc))
		}
		if r.LastLeave != nil {
			b.time(sh, 7, row, wallClock(*r.LastLeave, loc))
		}
		b.int(sh, 8, row, Minutes(r.TotalSeconds))
		if r.LateSeconds != nil {
			b.int(sh, 9, row, Minutes(*r.LateSeconds))
		}
		if r.EarlyLeaveSeconds != nil {
			b.int(sh, 10, row, Minutes(*r.EarlyLeaveSeconds))
		}
		b.str(sh, 11, row, StatusText(r.FinalStatus))
		b.str(sh, 12, row, statusCode(r.FinalStatus))
		b.str(sh, 13, row, yesNo(r.Manual))
		b.str(sh, 14, row, r.Comment)
	}
	if len(s.Rows) > 0 {
		b.styleRange(sh, 5, 2, 5, last, b.date)
		b.styleRange(sh, 6, 2, 7, last, b.clock)
		b.styleRange(sh, 8, 2, 10, last, b.integer)
		b.styleRange(sh, 12, 2, 13, last, b.center)
	}
	b.widths(sh, []float64{34, 14, 18, 28, 12, 12, 12, 13, 12, 12, 15, 11, 11, 40})

	const un = SheetUnidentified
	b.sheet(un)
	lastU := len(s.Unidentified) + 1
	b.headerRow(un, zonedColumns(UnidentifiedColumns, loc, s.PlannedStart), lastU, 1)
	for i, p := range s.Unidentified {
		row := i + 2
		b.str(un, 1, row, p.DisplayName)
		if p.FirstJoin != nil {
			b.time(un, 2, row, wallClock(*p.FirstJoin, loc))
		}
		if p.LastLeave != nil {
			b.time(un, 3, row, wallClock(*p.LastLeave, loc))
		}
		b.int(un, 4, row, Minutes(p.TotalSeconds))
		b.str(un, 5, row, participantStatus(p.Status))
	}
	if len(s.Unidentified) > 0 {
		b.styleRange(un, 2, 2, 3, lastU, b.clock)
		b.styleRange(un, 4, 2, 4, lastU, b.integer)
	}
	b.widths(un, []float64{34, 12, 12, 13, 30})
	return b.write(w)
}

func zoomDescription(id string) string {
	if id == "" {
		return ""
	}
	return "Zoom: " + id
}

// zoneDescription names the zone of the file's times for Excel's file
// properties: "Время: UTC+3 (Europe/Moscow)".
func zoneDescription(loc *time.Location, at time.Time) string {
	d := "Время: " + offsetLabel(loc, at)
	if name := loc.String(); name != "" && name != "UTC" && !strings.HasPrefix(name, "UTC") {
		d += " (" + name + ")"
	}
	return d
}

func joinNonEmpty(sep string, parts ...string) string {
	out := parts[:0:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}
