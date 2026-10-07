package httpapi

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"zoom-checker/internal/export"
	"zoom-checker/internal/model"
	"zoom-checker/internal/store"
)

// Group attendance report: the student × lesson matrix over a period, as JSON
// for the report page and as an XLSX/CSV download (TZ §26).
//
// The period is open on every side the request leaves out: without from/to
// the report covers all lessons of the group. The report page's default
// preset "Всё время" sends no bounds and expects exactly that (types.ts
// GroupReport.from/to are null then); a hidden default window would silently
// drop older lessons from an "all time" report and its export.

const grpDateLayout = "2006-01-02"

// Store bounds used for an open side of the period (lessons are stored as
// unix milliseconds, so these cover every possible lesson).
var (
	grpReportMinTime = time.UnixMilli(0).UTC()
	grpReportMaxTime = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
)

// grpReportData is the report of one request, ready for both outputs.
type grpReportData struct {
	rep       *store.GroupReport
	from, to  *time.Time // inclusive bounds; nil = open on that side
	loc       *time.Location
	attByPair map[[2]int64]*model.Attendance
	decided   map[int64]bool // lessons with at least one teacher decision
}

func (s *Server) handleGroupReport(w http.ResponseWriter, r *http.Request) error {
	d, err := s.loadGroupReport(r)
	if err != nil {
		return err
	}
	out := GroupReportDTO{
		Group:    GroupRef{ID: d.rep.Group.ID, Name: d.rep.Group.Name},
		From:     utcPtr(d.from),
		To:       utcPtr(d.to),
		Lessons:  make([]GroupReportLessonDTO, 0, len(d.rep.Lessons)),
		Students: make([]GroupReportStudentDTO, 0, len(d.rep.Students)),
	}
	for _, l := range d.rep.Lessons {
		out.Lessons = append(out.Lessons, GroupReportLessonDTO{ID: l.ID, Title: l.Title,
			PlannedStartAt: l.PlannedStartAt.UTC(), Status: l.Status})
	}
	for _, st := range d.rep.Students {
		row := GroupReportStudentDTO{
			StudentID: st.ID, FullName: st.FullName, ExternalID: st.ExternalID,
			Archived: st.Status == model.StatusArchived,
			Cells:    make([]GroupReportCellDTO, 0, len(d.rep.Lessons)),
		}
		for i := range d.rep.Lessons {
			l := &d.rep.Lessons[i]
			cell := GroupReportCellDTO{LessonID: l.ID}
			if a := d.cell(l, st.ID); a != nil {
				fs := a.FinalStatus
				cell.FinalStatus, cell.Manual = &fs, a.ManualStatus != nil
				cell.TotalSeconds, cell.LateSeconds = a.TotalDurationSeconds, a.LateSeconds
				switch fs {
				case model.FinalPresent:
					row.PresentCount++
				case model.FinalAbsent:
					row.AbsentCount++
				}
			}
			row.Cells = append(row.Cells, cell)
		}
		out.Students = append(out.Students, row)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) handleGroupReportExport(w http.ResponseWriter, r *http.Request) error {
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	switch format {
	case "":
		format = "xlsx"
	case "xlsx", "csv":
	default:
		return errValidation("Неизвестный формат выгрузки: ожидается xlsx или csv", map[string]any{"field": "format"})
	}
	d, err := s.loadGroupReport(r)
	if err != nil {
		return err
	}
	rep := export.GroupReport{
		GroupName: d.rep.Group.Name,
		Timezone:  d.loc,
		Lessons:   make([]export.ReportLesson, 0, len(d.rep.Lessons)),
		Students:  make([]export.ReportStudent, 0, len(d.rep.Students)),
	}
	// Lessons that have not been held would only be empty columns, so the
	// file leaves them out, as the report page does.
	lessons := make([]*model.Lesson, 0, len(d.rep.Lessons))
	for i := range d.rep.Lessons {
		if l := &d.rep.Lessons[i]; !d.skipInExport(l) {
			lessons = append(lessons, l)
			rep.Lessons = append(rep.Lessons, export.ReportLesson{ID: l.ID, Title: l.Title, Start: l.PlannedStartAt})
		}
	}
	for _, st := range d.rep.Students {
		row := export.ReportStudent{FullName: st.FullName, ExternalID: st.ExternalID, Cells: map[int64]export.ReportCell{}}
		for _, l := range lessons {
			if a := d.cell(l, st.ID); a != nil {
				row.Cells[l.ID] = export.ReportCell{Status: a.FinalStatus, Seconds: a.TotalDurationSeconds, Manual: a.ManualStatus != nil}
			}
		}
		rep.Students = append(rep.Students, row)
	}

	var buf bytes.Buffer
	contentType := "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	if format == "csv" {
		contentType = "text/csv; charset=utf-8"
		err = export.WriteGroupReportCSV(&buf, rep)
	} else {
		err = export.WriteGroupReportXLSX(&buf, rep)
	}
	if err != nil {
		return errInternal(fmt.Errorf("export group report: %w", err))
	}
	name, fallback := grpReportFileNames(d.rep.Group.ID, d.rep.Group.Name, d.from, d.to, lessons, d.loc, format)

	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", grpAttachmentDisposition(name, fallback))
	h.Set("Content-Length", strconv.Itoa(buf.Len()))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
	return nil
}

// loadGroupReport parses the period and loads the report data.
func (s *Server) loadGroupReport(r *http.Request) (*grpReportData, error) {
	groupID, err := pathID(r, "id")
	if err != nil {
		return nil, errNotFound(grpMsgGroupNotFound)
	}
	// The export passes the browser's zone (tz) so lesson times and the dates
	// in the file name match the screen; the report page does not send it.
	loc := s.exportLoc(r)
	from, to, err := grpReportPeriod(r.URL.Query(), loc)
	if err != nil {
		return nil, err
	}
	// The store takes a half-open millisecond range [lo, hi).
	lo, hi := grpReportMinTime, grpReportMaxTime
	if from != nil {
		if lo = from.Truncate(time.Millisecond); lo.Before(*from) {
			lo = lo.Add(time.Millisecond)
		}
	}
	if to != nil {
		hi = to.Truncate(time.Millisecond).Add(time.Millisecond)
	}
	rep, err := s.Store.GroupReport(r.Context(), groupID, lo, hi)
	if err != nil {
		return nil, storeErr(err, grpMsgGroupNotFound)
	}
	d := &grpReportData{rep: rep, from: from, to: to, loc: loc,
		attByPair: make(map[[2]int64]*model.Attendance, len(rep.Attendance)),
		decided:   map[int64]bool{}}
	for i := range rep.Attendance {
		a := &rep.Attendance[i]
		d.attByPair[[2]int64{a.LessonID, a.StudentID}] = a
		if a.ManualStatus != nil {
			d.decided[a.LessonID] = true
		}
	}
	return d, nil
}

// notHeld reports a lesson that has not taken place as far as the service
// knows: it is still "planned" (no Zoom session is linked), so its attendance
// rows are only a provisional "absent" for everybody.
func notHeld(l *model.Lesson) bool { return l.Status == model.LessonPlanned }

// skipInExport reports a lesson that carries no information for the file:
// not held and nobody decided anything for it by hand. A weekly series
// planned months ahead would otherwise add a run of empty columns.
func (d *grpReportData) skipInExport(l *model.Lesson) bool {
	return notHeld(l) && !d.decided[l.ID]
}

// cell returns the attendance row to show for a student in a lesson, or nil
// for an empty cell: no row, or a lesson that has not been held (provisional
// rows) unless a teacher already decided for this student.
func (d *grpReportData) cell(l *model.Lesson, studentID int64) *model.Attendance {
	a := d.attByPair[[2]int64{l.ID, studentID}]
	if a == nil {
		return nil
	}
	if notHeld(l) && a.ManualStatus == nil {
		return nil
	}
	return a
}

// grpReportPeriod reads from/to: YYYY-MM-DD in loc (to is inclusive: the
// whole day) or RFC 3339 instants. A missing bound is nil: the period is open
// on that side. Returns inclusive bounds.
func grpReportPeriod(q url.Values, loc *time.Location) (from, to *time.Time, err error) {
	if v := strings.TrimSpace(q.Get("from")); v != "" {
		t, _, ok := grpParseDate(v, loc)
		if !ok {
			return nil, nil, errValidation("Некорректная дата начала периода. Ожидается ГГГГ-ММ-ДД.", map[string]any{"field": "from"})
		}
		from = &t
	}
	if v := strings.TrimSpace(q.Get("to")); v != "" {
		t, dateOnly, ok := grpParseDate(v, loc)
		if !ok {
			return nil, nil, errValidation("Некорректная дата конца периода. Ожидается ГГГГ-ММ-ДД.", map[string]any{"field": "to"})
		}
		if dateOnly {
			t = t.AddDate(0, 0, 1).Add(-time.Millisecond)
		}
		to = &t
	}
	if from != nil && to != nil && from.After(*to) {
		return nil, nil, errValidation("Начало периода позже его конца. Проверьте даты.", map[string]any{"field": "from"})
	}
	return from, to, nil
}

// grpParseDate accepts YYYY-MM-DD (start of that day in loc) or RFC 3339.
// Any four-digit year fits the stored unix milliseconds.
func grpParseDate(v string, loc *time.Location) (t time.Time, dateOnly, ok bool) {
	if d, err := time.ParseInLocation(grpDateLayout, v, loc); err == nil {
		return d, true, true
	}
	if d, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return d, false, true
	}
	return time.Time{}, false, false
}

// grpReportFileNames returns the UTF-8 file name of an export ("Посещаемость
// <группа> 01.09.2026–31.10.2026.xlsx") and its ASCII fallback. An open side
// of the period is named by the first or last exported lesson; with no bound
// and no lesson the name has no dates.
func grpReportFileNames(groupID int64, groupName string, from, to *time.Time, lessons []*model.Lesson, loc *time.Location, ext string) (string, string) {
	first, last := from, to
	if len(lessons) > 0 {
		if first == nil {
			first = &lessons[0].PlannedStartAt
		}
		if last == nil {
			last = &lessons[len(lessons)-1].PlannedStartAt
		}
	}
	ru := func(t *time.Time) string { return t.In(loc).Format("02.01.2006") }
	iso := func(t *time.Time) string { return t.In(loc).Format(grpDateLayout) }
	name := "Посещаемость " + grpSafeFilename(groupName)
	fallback := "attendance-group-" + strconv.FormatInt(groupID, 10)
	switch {
	case first != nil && last != nil:
		name += " " + ru(first) + "–" + ru(last)
		fallback += "-" + iso(first) + "-" + iso(last)
	case first != nil:
		name += " с " + ru(first)
		fallback += "-from-" + iso(first)
	case last != nil:
		name += " по " + ru(last)
		fallback += "-to-" + iso(last)
	}
	return name + "." + ext, fallback + "." + ext
}

// grpAttachmentDisposition builds a Content-Disposition header with a UTF-8
// file name (RFC 6266 / RFC 5987 filename*) and an ASCII fallback for old
// clients.
func grpAttachmentDisposition(name, asciiFallback string) string {
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, asciiFallback, grpRFC5987(name))
}

// grpRFC5987 percent-encodes s as an RFC 5987 ext-value (attr-char kept).
func grpRFC5987(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < utf8.RuneSelf && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte("!#$&+-.^_`|~", c) >= 0) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

// grpSafeFilename drops characters that file systems or browsers reject and
// keeps the name short.
func grpSafeFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
		case strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if utf8.RuneCountInString(out) > 80 {
		out = string([]rune(out)[:80])
	}
	if out == "" {
		out = "группа"
	}
	return out
}
