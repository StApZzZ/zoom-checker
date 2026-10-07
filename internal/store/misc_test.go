package store

import (
	"reflect"
	"testing"
	"time"

	"zoom-checker/internal/model"
)

func TestAudit(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		lid, gid := int64(10), int64(20)
		uid := int64(1)
		entries := []model.AuditEntry{
			{At: t0, UserID: &uid, UserName: "admin", Action: model.AuditAttendanceStatus, EntityType: "attendance", EntityID: "10:5",
				LessonID: &lid, GroupID: &gid, PreviousValue: "absent", NewValue: "present", Reason: "был с телефона"},
			{At: t0.Add(time.Minute), Action: model.AuditAttendanceComment, EntityType: "attendance", EntityID: "10:6", LessonID: &lid},
			{At: t0.Add(2 * time.Minute), Action: model.AuditGroupUpdate, EntityType: "group", EntityID: "20", GroupID: &gid},
			{Action: model.AuditSettingsUpdate, EntityType: "settings"}, // zero At → clock (t0)
		}
		for i := range entries {
			e.must(e.s.InsertAudit(e.ctx, &entries[i]))
			if entries[i].ID == 0 {
				t.Fatal("no id")
			}
		}
		if !entries[3].At.Equal(t0) {
			t.Errorf("default At = %v", entries[3].At)
		}
		all, err := e.s.ListAudit(e.ctx, AuditFilter{})
		e.must(err)
		// at DESC, id DESC: entries 2, 1, 3 (t0, higher id), 0.
		order := []int64{entries[2].ID, entries[1].ID, entries[3].ID, entries[0].ID}
		var got []int64
		for _, a := range all {
			got = append(got, a.ID)
		}
		if !equalIDs(got, order) {
			t.Errorf("order = %v, want %v", got, order)
		}
		first := all[3]
		if first.UserName != "admin" || first.UserID == nil || *first.UserID != 1 || first.Reason != "был с телефона" ||
			first.PreviousValue != "absent" || first.NewValue != "present" || first.LessonID == nil || first.GroupID == nil ||
			!first.At.Equal(t0.Truncate(time.Millisecond)) {
			t.Errorf("entry = %+v", first)
		}
		cases := []struct {
			name string
			f    AuditFilter
			want int
		}{
			{"lesson", AuditFilter{LessonID: &lid}, 2},
			{"group", AuditFilter{GroupID: &gid}, 2},
			{"entity", AuditFilter{EntityType: "attendance", EntityID: "10:6"}, 1},
			{"entity type", AuditFilter{EntityType: "attendance"}, 2},
			{"limit", AuditFilter{Limit: 1}, 1},
			// After entries[2] (newest) come 1, 3 and 0 — entry 3 has a higher id
			// than 2 but an older `at`, and must not be skipped.
			{"before", AuditFilter{BeforeID: entries[2].ID}, 3},
			{"before unknown id", AuditFilter{BeforeID: 99999}, 0},
		}
		for _, c := range cases {
			l, err := e.s.ListAudit(e.ctx, c.f)
			e.must(err)
			if len(l) != c.want {
				t.Errorf("%s: %d entries, want %d", c.name, len(l), c.want)
			}
		}
		// Walking the pages one entry at a time visits every entry once, in order.
		var walked []int64
		var before int64
		for range len(entries) + 1 {
			page, err := e.s.ListAudit(e.ctx, AuditFilter{BeforeID: before, Limit: 1})
			e.must(err)
			if len(page) == 0 {
				break
			}
			walked = append(walked, page[0].ID)
			before = page[0].ID
		}
		if !equalIDs(walked, order) {
			t.Errorf("paged walk = %v, want %v", walked, order)
		}
	})
}

func TestSettings(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		st, err := e.s.GetSettings(e.ctx)
		e.must(err)
		if !reflect.DeepEqual(st, model.DefaultSettings()) {
			t.Errorf("defaults = %+v", st)
		}
		// Partial JSON merges over defaults.
		e.must(e.s.PutSetting(e.ctx, SettingsKey, `{"present_min_percent": 50}`, nil))
		st, err = e.s.GetSettings(e.ctx)
		e.must(err)
		want := model.DefaultSettings()
		want.PresentMinPercent = 50
		if !reflect.DeepEqual(st, want) {
			t.Errorf("merged = %+v, want %+v", st, want)
		}
		by := int64(1)
		full := model.Settings{IgnoredNames: []string{"преподаватель", "zoom room"}, PresentMinPercent: 70, LinkWindowHours: 3, IgnoreHost: false}
		e.must(e.s.PutSettings(e.ctx, full, &by))
		st, err = e.s.GetSettings(e.ctx)
		e.must(err)
		if !reflect.DeepEqual(st, full) {
			t.Errorf("stored = %+v, want %+v", st, full)
		}
		e.must(e.s.PutSettings(e.ctx, model.Settings{LinkWindowHours: 6}, nil))
		st, _ = e.s.GetSettings(e.ctx)
		if st.IgnoredNames == nil || len(st.IgnoredNames) != 0 || st.IgnoreHost {
			t.Errorf("explicit false/empty must be kept: %+v", st)
		}
		e.must(e.s.PutSetting(e.ctx, SettingsKey, `not json`, nil))
		if _, err := e.s.GetSettings(e.ctx); err == nil {
			t.Error("bad JSON accepted")
		}

		_, err = e.s.GetSetting(e.ctx, "server_secret")
		wantErr(t, err, ErrNotFound)
		v, err := e.s.EnsureSetting(e.ctx, "server_secret", "first")
		e.must(err)
		if v != "first" {
			t.Errorf("ensure = %q", v)
		}
		v, err = e.s.EnsureSetting(e.ctx, "server_secret", "second")
		e.must(err)
		if v != "first" {
			t.Errorf("ensure must keep the first value, got %q", v)
		}
		e.must(e.s.PutSetting(e.ctx, "server_secret", "third", nil))
		if v, _ := e.s.GetSetting(e.ctx, "server_secret"); v != "third" {
			t.Errorf("put = %q", v)
		}
	})
}

func TestGroupReport(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		g := e.group("G")
		otherG := e.group("Other")
		a := e.student(g.ID, "A")
		b := e.student(g.ID, "B")
		gone := e.student(g.ID, "Gone")        // archived, has a row → included
		never := e.student(g.ID, "Never seen") // archived, no row → excluded
		e.student(otherG.ID, "Other student")
		day := 24 * time.Hour
		l1 := e.lesson(g.ID, "123456789", t0, time.Hour)
		l2 := e.lesson(g.ID, "123456789", t0.Add(day), time.Hour)
		outside := e.lesson(g.ID, "123456789", t0.Add(3*day), time.Hour)
		e.lesson(otherG.ID, "123456789", t0, time.Hour)
		for _, l := range []*model.Lesson{l1, l2, outside} {
			_, err := e.s.UpsertAttendanceComputed(e.ctx, l.ID, []model.Attendance{
				{StudentID: a.ID, CurrentStatus: model.CurrentLeft, AutoStatus: model.FinalPresent},
				{StudentID: b.ID, CurrentStatus: model.CurrentNotSeen, AutoStatus: model.FinalAbsent},
			})
			e.must(err)
		}
		_, err := e.s.UpsertAttendanceComputed(e.ctx, l1.ID, []model.Attendance{{StudentID: gone.ID, CurrentStatus: model.CurrentLeft,
			AutoStatus: model.FinalPresent}})
		e.must(err)
		for _, st := range []*model.Student{gone, never} {
			st.Status = model.StatusArchived
			e.must(e.s.UpdateStudent(e.ctx, st))
		}

		rep, err := e.s.GroupReport(e.ctx, g.ID, t0, t0.Add(2*day))
		e.must(err)
		if rep.Group.ID != g.ID {
			t.Errorf("group = %+v", rep.Group)
		}
		if len(rep.Lessons) != 2 || rep.Lessons[0].ID != l1.ID || rep.Lessons[1].ID != l2.ID {
			t.Errorf("lessons = %+v", rep.Lessons)
		}
		var names []string
		for _, st := range rep.Students {
			names = append(names, st.NormalizedName+"/"+string(st.Status))
		}
		if want := "[a/active b/active gone/archived]"; sprint(names) != want {
			t.Errorf("students = %v, want %v", names, want)
		}
		if len(rep.Attendance) != 5 {
			t.Errorf("attendance rows = %d", len(rep.Attendance))
		}
		for _, a := range rep.Attendance {
			if a.LessonID != l1.ID && a.LessonID != l2.ID {
				t.Errorf("row of lesson %d outside the range", a.LessonID)
			}
		}

		empty, err := e.s.GroupReport(e.ctx, g.ID, t0.Add(10*day), t0.Add(11*day))
		e.must(err)
		if len(empty.Lessons) != 0 || len(empty.Attendance) != 0 || len(empty.Students) != 2 || empty.Lessons == nil || empty.Attendance == nil {
			t.Errorf("empty range = %+v", empty)
		}
		_, err = e.s.GroupReport(e.ctx, 999, t0, t0)
		wantErr(t, err, ErrNotFound)
	})
}
