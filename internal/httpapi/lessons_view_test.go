package httpapi

import (
	"strings"
	"testing"
	"time"

	"zoom-checker/internal/model"
	"zoom-checker/internal/names"
)

// ltViewFixture: group with three active students and one archived, lesson
// 18:00–19:30, now 18:30.
//   - Иванов: phone 18:00–18:20 and laptop 18:10–open (overlap counted once);
//   - Петров: 18:05–18:15, left;
//   - Сидоров: never connected, no attendance row yet (added after the rebuild);
//   - Архивов: archived, has a row (history kept);
//   - Zoom "Ваня": unidentified, online since 18:10; "Организатор": ignored host.
func ltViewFixture() *lessonViewData {
	start := ltAt("18:00")
	l := model.Lesson{ID: 7, GroupID: 3, Title: "Linux 4", ZoomMeetingID: ltMeetingID,
		PlannedStartAt: start, PlannedEndAt: start.Add(90 * time.Minute), Status: model.LessonActive}
	st := func(id int64, name string, status model.Status) model.Student {
		return model.Student{ID: id, GroupID: 3, FullName: name, NormalizedName: names.Normalize(name), Status: status, ExternalID: "ext-" + name}
	}
	pp := func(id int64, name string, res model.Resolution, sid *int64, status model.CurrentStatus, first time.Time) model.Participant {
		f := first
		return model.Participant{ID: id, LessonID: 7, DisplayName: name, NormalizedName: names.Normalize(name), Resolution: res,
			StudentID: sid, CurrentStatus: status, FirstJoinAt: &f, Devices: 1}
	}
	row := func(id, sid int64, cur model.CurrentStatus, final model.FinalStatus, total int64) model.Attendance {
		return model.Attendance{ID: id, LessonID: 7, StudentID: sid, CurrentStatus: cur, AutoStatus: final, FinalStatus: final,
			TotalDurationSeconds: total}
	}
	iv := func(id int64, part, row int64, uuid string, join time.Time, leave *time.Time) model.Interval {
		out := model.Interval{ID: id, LessonID: 7, ZoomParticipantID: &part, MeetingUUID: "m1", ZoomParticipantUUID: uuid,
			JoinAt: join, LeaveAt: leave}
		if row != 0 {
			out.AttendanceID = &row
		}
		return out
	}
	return &lessonViewData{
		Lesson:    l,
		GroupName: "DevOps-1",
		Students: []model.Student{
			st(1, "Иванов Иван", model.StatusActive),
			st(2, "Петров Пётр", model.StatusActive),
			st(3, "Сидоров Алексей", model.StatusActive),
			st(4, "Архивов Артём", model.StatusArchived),
		},
		Rows: []model.Attendance{
			// Totals were projected at 18:25 — the view must recompute them as of now.
			row(11, 1, model.CurrentOnline, model.FinalPresent, 1500),
			row(12, 2, model.CurrentLeft, model.FinalPresent, 600),
			row(14, 4, model.CurrentNotSeen, model.FinalAbsent, 0),
		},
		Participants: []model.Participant{
			pp(21, "Иванов Иван", model.ResolutionMatched, ltPtr(int64(1)), model.CurrentOnline, ltAt("18:00")),
			pp(22, "Петров Пётр", model.ResolutionMatched, ltPtr(int64(2)), model.CurrentLeft, ltAt("18:05")),
			pp(23, "Ваня", model.ResolutionUnidentified, nil, model.CurrentOnline, ltAt("18:10")),
			{ID: 24, LessonID: 7, DisplayName: "Организатор", NormalizedName: "организатор", Resolution: model.ResolutionIgnored,
				IgnoreReason: model.IgnoreHost, CurrentStatus: model.CurrentOnline, FirstJoinAt: ltPtr(ltAt("17:55"))},
		},
		Intervals: []model.Interval{
			iv(31, 21, 11, "phone", ltAt("18:00"), ltPtr(ltAt("18:20"))),
			iv(32, 21, 11, "laptop", ltAt("18:10"), nil),
			iv(33, 22, 12, "p", ltAt("18:05"), ltPtr(ltAt("18:15"))),
			iv(34, 23, 0, "v", ltAt("18:10"), nil),
			iv(35, 24, 0, "h", ltAt("17:55"), nil),
		},
	}
}

func ltRow(t *testing.T, v LessonViewDTO, id int64) StudentRowDTO {
	t.Helper()
	for _, r := range v.Students {
		if r.StudentID == id {
			return r
		}
	}
	t.Fatalf("student %d not in view", id)
	return StudentRowDTO{}
}

func ltPart(t *testing.T, v LessonViewDTO, name string) ParticipantDTO {
	t.Helper()
	for _, p := range v.Participants {
		if p.DisplayName == name {
			return p
		}
	}
	t.Fatalf("participant %q not in view", name)
	return ParticipantDTO{}
}

func TestLessonViewTotalsTickAsOfNow(t *testing.T) {
	t.Parallel()
	d := ltViewFixture()
	// 18:00–18:20 ∪ 18:10–now: the 10 overlapping minutes count once (30, not 40).
	v := assembleLessonView(d, ltAt("18:30"))
	iv := ltRow(t, v, 1)
	if iv.TotalSeconds != 30*60 {
		t.Errorf("Иванов total at 18:30 = %d, want %d (two devices overlap counted once)", iv.TotalSeconds, 30*60)
	}
	if iv.DevicesOnline != 1 {
		t.Errorf("devices_online = %d, want 1 (only the laptop is still connected)", iv.DevicesOnline)
	}
	// One minute later the open interval ticks.
	v2 := assembleLessonView(d, ltAt("18:31"))
	if got := ltRow(t, v2, 1).TotalSeconds; got != 31*60 {
		t.Errorf("Иванов total at 18:31 = %d, want %d", got, 31*60)
	}
	// At 19:00: union of 18:00–18:20 and 18:10–19:00 = 60 min, not 20+50.
	v3 := assembleLessonView(d, ltAt("19:00"))
	if got := ltRow(t, v3, 1).TotalSeconds; got != 60*60 {
		t.Errorf("Иванов total at 19:00 = %d, want %d", got, 60*60)
	}
	// Closed intervals do not tick.
	if got := ltRow(t, v3, 2).TotalSeconds; got != 10*60 {
		t.Errorf("Петров total = %d, want 600", got)
	}
	// Participants tick too.
	if got := ltPart(t, v3, "Ваня").TotalSeconds; got != 50*60 {
		t.Errorf("Ваня total = %d, want %d", got, 50*60)
	}
	if !v3.AsOf.Equal(ltAt("19:00")) {
		t.Errorf("as_of = %v", v3.AsOf)
	}
}

func TestLessonViewDevicesOnline(t *testing.T) {
	t.Parallel()
	d := ltViewFixture()
	// Phone still open too: two devices.
	d.Intervals[0].LeaveAt = nil
	// Same laptop also in a breakout room: same participant UUID → still one device.
	bo := d.Intervals[1]
	bo.ID = 99
	bo.JoinAt = ltAt("18:25")
	d.Intervals = append(d.Intervals, bo)
	v := assembleLessonView(d, ltAt("18:30"))
	r := ltRow(t, v, 1)
	if r.DevicesOnline != 2 {
		t.Errorf("devices_online = %d, want 2", r.DevicesOnline)
	}
	if r.TotalSeconds != 30*60 {
		t.Errorf("total = %d, want 1800", r.TotalSeconds)
	}
}

func TestLessonViewRowsCountsAndOrder(t *testing.T) {
	t.Parallel()
	d := ltViewFixture()
	v := assembleLessonView(d, ltAt("18:30"))

	// Sidorov has no row but is an active student of an unfinished lesson.
	var gotNames []string
	for _, r := range v.Students {
		gotNames = append(gotNames, r.FullName)
	}
	want := []string{"Архивов Артём", "Иванов Иван", "Петров Пётр", "Сидоров Алексей"}
	if len(gotNames) != len(want) {
		t.Fatalf("students = %v, want %v", gotNames, want)
	}
	for i := range want {
		if gotNames[i] != want[i] {
			t.Fatalf("students = %v, want %v", gotNames, want)
		}
	}
	sid := ltRow(t, v, 3)
	if sid.CurrentStatus != model.CurrentNotSeen || sid.FinalStatus != model.FinalAbsent || sid.TotalSeconds != 0 ||
		sid.ZoomNames == nil || sid.ExternalID != "ext-Сидоров Алексей" {
		t.Errorf("synthesized row = %+v", sid)
	}
	if !ltRow(t, v, 4).Archived || ltRow(t, v, 1).Archived {
		t.Error("archived flag wrong")
	}
	if zn := ltRow(t, v, 1).ZoomNames; len(zn) != 1 || zn[0] != "Иванов Иван" {
		t.Errorf("zoom_names = %v", zn)
	}

	c := v.Counts
	wantC := CountsDTO{InGroup: 4, Online: 1, Seen: 2, NotSeen: 2, Unidentified: 1, Present: 2, Absent: 2}
	if c != wantC {
		t.Errorf("counts = %+v, want %+v", c, wantC)
	}
	if c.Online+(c.Seen-c.Online)+c.NotSeen != c.InGroup || c.Present+c.Absent != c.InGroup {
		t.Errorf("counts inconsistent: %+v", c)
	}

	// Participants: unidentified first, then by first join (host joined first).
	var order []string
	for _, p := range v.Participants {
		order = append(order, p.DisplayName)
	}
	wantOrder := []string{"Ваня", "Организатор", "Иванов Иван", "Петров Пётр"}
	for i := range wantOrder {
		if order[i] != wantOrder[i] {
			t.Fatalf("participants order = %v, want %v", order, wantOrder)
		}
	}
	if p := ltPart(t, v, "Иванов Иван"); p.StudentName == nil || *p.StudentName != "Иванов Иван" || p.Manual {
		t.Errorf("matched participant = %+v", p)
	}
	if p := ltPart(t, v, "Ваня"); p.StudentName != nil || p.CandidateStudentIDs == nil {
		t.Errorf("unidentified participant = %+v", p)
	}
	if v.Lesson.Group.Name != "DevOps-1" || v.Lesson.ZoomMeetingIDFormatted != "857 4606 5432" || v.Lesson.Sessions == nil {
		t.Errorf("lesson = %+v", v.Lesson)
	}
}

func TestLessonViewFinishedRosterFrozen(t *testing.T) {
	t.Parallel()
	d := ltViewFixture()
	d.Lesson.Status = model.LessonFinished
	v := assembleLessonView(d, ltAt("19:40"))
	for _, r := range v.Students {
		if r.StudentID == 3 {
			t.Fatal("student without a row must not be added to a finished lesson")
		}
	}
	if v.Counts.InGroup != 3 {
		t.Errorf("in_group = %d", v.Counts.InGroup)
	}

	// Never rebuilt (no rows at all): every active student is shown.
	d.Rows = nil
	v = assembleLessonView(d, ltAt("19:40"))
	if v.Counts.InGroup != 3 || v.Counts.NotSeen != 3 || v.Counts.Absent != 3 {
		t.Errorf("counts without rows = %+v", v.Counts)
	}
}

// A planned lesson that never took place (no Zoom session) and ended more
// than a day ago is no longer refreshed after roster changes; its view does
// not add students who joined the group later either, so the screen, the
// lesson list counts and the stored rows agree.
func TestLessonViewStalePlannedRosterKept(t *testing.T) {
	t.Parallel()
	d := ltViewFixture()
	d.Lesson.Status = model.LessonPlanned
	d.Participants, d.Intervals = nil, nil
	for i := range d.Rows {
		d.Rows[i].CurrentStatus, d.Rows[i].FinalStatus, d.Rows[i].TotalDurationSeconds = model.CurrentNotSeen, model.FinalAbsent, 0
	}
	shows := func(v LessonViewDTO, id int64) bool {
		for _, r := range v.Students {
			if r.StudentID == id {
				return true
			}
		}
		return false
	}
	end := d.Lesson.PlannedEndAt
	if v := assembleLessonView(d, end.Add(23*time.Hour)); !shows(v, 3) || v.Counts.InGroup != 4 {
		t.Errorf("planned lesson that ended less than a day ago: in_group %d", v.Counts.InGroup)
	}
	v := assembleLessonView(d, end.Add(25*time.Hour))
	if shows(v, 3) || v.Counts.InGroup != 3 {
		t.Errorf("stale planned lesson gained a student without a row: in_group %d", v.Counts.InGroup)
	}
	// Never rebuilt (no rows at all): every active student is shown.
	d.Rows = nil
	if v := assembleLessonView(d, end.Add(25*time.Hour)); v.Counts.InGroup != 3 || v.Counts.NotSeen != 3 {
		t.Errorf("counts without rows = %+v", v.Counts)
	}
}

func TestLessonViewManualFlagsAndFallbacks(t *testing.T) {
	t.Parallel()
	d := ltViewFixture()
	d.Participants[1].Resolution = model.ResolutionManual
	d.Participants[3].IgnoreReason = model.IgnoreManual
	d.Mappings = []model.Mapping{{LessonID: 7, NormalizedName: "ваня", Action: model.MappingNone}}
	// An interval without attendance_id still counts for the student its name resolves to.
	d.Intervals[2].AttendanceID = nil
	// A row without stored intervals keeps its projected total.
	d.Rows[2].TotalDurationSeconds = 42
	v := assembleLessonView(d, ltAt("18:30"))
	if !ltPart(t, v, "Петров Пётр").Manual || !ltPart(t, v, "Организатор").Manual || !ltPart(t, v, "Ваня").Manual {
		t.Error("manual flags not set")
	}
	if ltPart(t, v, "Иванов Иван").Manual {
		t.Error("matched participant marked manual")
	}
	if got := ltRow(t, v, 2).TotalSeconds; got != 600 {
		t.Errorf("Петров total via participant = %d, want 600", got)
	}
	if got := ltRow(t, v, 4).TotalSeconds; got != 42 {
		t.Errorf("row without intervals = %d, want 42", got)
	}
}

func TestLessonSeriesHelpers(t *testing.T) {
	t.Parallel()
	titles := []struct {
		in   string
		i    int
		want string
	}{
		{"Занятие №4", 0, "Занятие №4"},
		{"Занятие №4", 1, "Занятие №5"},
		{"Linux 4", 2, "Linux 6"},
		{"Урок 09", 1, "Урок 10"},
		{"Урок 007", 2, "Урок 009"},
		{"Linux #9", 1, "Linux #10"},
		{"Введение", 3, "Введение"},
		{"2026", 1, "2027"},
	}
	for _, tc := range titles {
		if got := lessonSeriesTitle(tc.in, tc.i); got != tc.want {
			t.Errorf("lessonSeriesTitle(%q, %d) = %q, want %q", tc.in, tc.i, got, tc.want)
		}
	}

	// Berlin leaves summer time on 25.10.2026: 18:00 local stays 18:00 local.
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 20, 18, 0, 0, 0, berlin)
	next := lessonAddWeeks(start, 1, berlin)
	if got := next.In(berlin); got.Hour() != 18 || got.Day() != 27 {
		t.Errorf("next week = %v, want 27.10 18:00 local", got)
	}
	if d := next.Sub(start.UTC()); d != 7*24*time.Hour+time.Hour {
		t.Errorf("DST week length = %v", d)
	}
}

func TestLessonParseZoomJoinURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, id, url string
	}{
		{"857 4606 5432", "85746065432", ""},
		{"https://us02web.zoom.us/j/85746065432?pwd=abc", "85746065432", "https://us02web.zoom.us/j/85746065432?pwd=abc"},
		{"Подключиться: https://zoom.us/download и https://us06web.zoom.us/j/85746065432?pwd=x.\nMeeting ID: 857 4606 5432",
			"85746065432", "https://us06web.zoom.us/j/85746065432?pwd=x"},
		{"Личная ссылка https://zoom.us/my/ivanov\nИдентификатор конференции: 857 4606 5432", "85746065432", "https://zoom.us/my/ivanov"},
		{"https://example.com/j/85746065432 Meeting ID: 857 4606 5432", "85746065432", ""},
		// Links longer than 500 characters are not stored (the meeting id still is).
		{"https://zoom.us/j/85746065432?pwd=" + strings.Repeat("a", 500), "85746065432", ""},
		// Text glued to the link (pasted from a messenger) is not part of it.
		{"https://zoom.us/j/123456789Подключиться", "123456789", "https://zoom.us/j/123456789"},
		{"«https://us02web.zoom.us/j/85746065432?pwd=abc»", "85746065432", "https://us02web.zoom.us/j/85746065432?pwd=abc"},
	}
	for _, tc := range cases {
		id, u, err := lessonParseZoom(tc.in)
		if err != nil || id != tc.id || u != tc.url {
			t.Errorf("lessonParseZoom(%q) = %q, %q, %v; want %q, %q", tc.in, id, u, err, tc.id, tc.url)
		}
	}
	if _, _, err := lessonParseZoom("https://zoom.us/my/ivanov"); err == nil {
		t.Error("personal link without id must fail")
	}
}

func TestLessonContentDisposition(t *testing.T) {
	t.Parallel()
	got := lessonContentDisposition("Посещаемость DevOps-1 Linux/4 06.10.2026.xlsx")
	want := `attachment; filename="Poseshchaemost DevOps-1 Linux_4 06.10.2026.xlsx"; filename*=UTF-8''` +
		`%D0%9F%D0%BE%D1%81%D0%B5%D1%89%D0%B0%D0%B5%D0%BC%D0%BE%D1%81%D1%82%D1%8C%20DevOps-1%20Linux_4%2006.10.2026.xlsx`
	if got != want {
		t.Errorf("content disposition:\n got %s\nwant %s", got, want)
	}
}
