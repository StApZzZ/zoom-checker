package httpapi

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"zoom-checker/internal/export"
	"zoom-checker/internal/model"
)

// handleLessonExport: GET /api/lessons/{id}/export?format=csv|xlsx (default xlsx)
// [&tz=IANA zone]. The file has every roster student, including those who never
// connected (TZ scenario 9), plus the Zoom participants that are not students.
// Times are in tz (the browser's zone) or TIMEZONE.
func (s *Server) handleLessonExport(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "xlsx"
	}
	if format != "csv" && format != "xlsx" {
		return errValidation("Неизвестный формат файла: выберите Excel (xlsx) или CSV", map[string]any{"field": "format"})
	}
	v, err := s.buildLessonView(r.Context(), id, s.lessonNow())
	if err != nil {
		return storeErr(err, lessonNotFoundMsg)
	}
	loc := s.exportLoc(r)
	sheet := s.lessonSheet(v, loc)

	var buf bytes.Buffer
	contentType := "text/csv; charset=utf-8"
	if format == "csv" {
		err = export.WriteLessonCSV(&buf, sheet)
	} else {
		contentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
		err = export.WriteLessonXLSX(&buf, sheet)
	}
	if err != nil {
		return errInternal(fmt.Errorf("export lesson %d: %w", id, err))
	}
	name := fmt.Sprintf("Посещаемость %s %s %s.%s", v.Lesson.Group.Name, v.Lesson.Title,
		v.Lesson.PlannedStartAt.In(loc).Format("02.01.2006"), format)
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", lessonContentDisposition(name))
	h.Set("Content-Length", fmt.Sprint(buf.Len()))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
	return nil
}

// lessonSheet converts the live view into the export input; times are in loc.
func (s *Server) lessonSheet(v *LessonViewDTO, loc *time.Location) export.LessonSheet {
	sh := export.LessonSheet{
		GroupName:     v.Lesson.Group.Name,
		LessonTitle:   v.Lesson.Title,
		PlannedStart:  v.Lesson.PlannedStartAt,
		ZoomMeetingID: v.Lesson.ZoomMeetingIDFormatted,
		Timezone:      loc,
		Rows:          make([]export.LessonRow, 0, len(v.Students)),
	}
	for _, st := range v.Students {
		sh.Rows = append(sh.Rows, export.LessonRow{
			FullName: st.FullName, ExternalID: st.ExternalID, FirstJoin: st.FirstJoinAt, LastLeave: st.LastLeaveAt,
			TotalSeconds: st.TotalSeconds, LateSeconds: st.LateSeconds, EarlyLeaveSeconds: st.EarlyLeaveSeconds,
			FinalStatus: st.FinalStatus, Manual: st.ManualStatus != nil, Comment: st.Comment,
		})
	}
	for _, p := range v.Participants {
		if p.Resolution == model.ResolutionMatched || p.Resolution == model.ResolutionManual {
			continue
		}
		sh.Unidentified = append(sh.Unidentified, export.ParticipantRow{
			DisplayName: p.DisplayName, FirstJoin: p.FirstJoinAt, LastLeave: p.LastLeaveAt,
			TotalSeconds: p.TotalSeconds, Status: string(p.Resolution),
		})
	}
	return sh
}

// lessonContentDisposition builds an attachment header with an ASCII fallback
// filename (Cyrillic transliterated) and the exact UTF-8 name as RFC 5987 filename*.
func lessonContentDisposition(name string) string {
	name = lessonSafeFilename(name)
	return `attachment; filename="` + lessonASCIIFilename(name) + `"; filename*=UTF-8''` + lessonRFC5987(name)
}

// lessonSafeFilename removes characters that are not allowed in file names on
// common systems and limits the length.
func lessonSafeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f || unicode.Is(unicode.Cf, r):
			continue
		case strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteRune('_')
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.Join(strings.Fields(b.String()), " ")
	if utf8.RuneCountInString(s) > 150 {
		ext := ""
		if i := strings.LastIndexByte(s, '.'); i > 0 && len(s)-i <= 5 {
			ext = s[i:]
			s = s[:i]
		}
		rs := []rune(s)
		s = strings.TrimSpace(string(rs[:150-utf8.RuneCountInString(ext)])) + ext
	}
	if s == "" {
		s = "export"
	}
	return s
}

var lessonTranslit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh", 'з': "z", 'и': "i",
	'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t",
	'у': "u", 'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch", 'ъ': "", 'ы': "y", 'ь': "",
	'э': "e", 'ю': "yu", 'я': "ya", '№': "N",
}

// lessonASCIIFilename is the fallback for clients without filename* support.
func lessonASCIIFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		lower := unicode.ToLower(r)
		if t, ok := lessonTranslit[lower]; ok {
			if lower != r && t != "" {
				t = strings.ToUpper(t[:1]) + t[1:]
			}
			b.WriteString(t)
			continue
		}
		switch {
		case r < 0x80 && (unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(" .-_()+,", r)):
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// lessonRFC5987 percent-encodes s as an RFC 5987 ext-value (attr-char kept).
func lessonRFC5987(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') ||
			strings.IndexByte("!#$&+-.^_`|~", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}
