package store

import (
	"testing"
	"time"

	"zoom-checker/internal/model"
)

func TestLessonsCRUD(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		g := e.group("G")
		g2 := e.group("G2")
		uid := int64(42)
		l := &model.Lesson{GroupID: g.ID, Title: "Введение", ZoomMeetingID: "85746065432", ZoomJoinURL: "https://zoom.us/j/85746065432",
			PlannedStartAt: t0, PlannedEndAt: t0.Add(90 * time.Minute), CreatedBy: &uid}
		e.must(e.s.CreateLesson(e.ctx, l))
		if l.ID == 0 || l.Status != model.LessonPlanned {
			t.Fatalf("created %+v", l)
		}
		got, err := e.s.GetLesson(e.ctx, l.ID)
		e.must(err)
		if got.Title != "Введение" || got.ZoomJoinURL != l.ZoomJoinURL || got.CreatedBy == nil || *got.CreatedBy != 42 ||
			got.Status != model.LessonPlanned || got.ActualStartAt != nil || got.ManualFinishedAt != nil {
			t.Errorf("GetLesson = %+v", got)
		}
		_, err = e.s.GetLesson(e.ctx, 999)
		wantErr(t, err, ErrNotFound)
		wantErr(t, e.s.CreateLesson(e.ctx, &model.Lesson{GroupID: 999, ZoomMeetingID: "1", PlannedStartAt: t0, PlannedEndAt: t0}), ErrNotFound)
		wantErr(t, e.s.CreateLesson(e.ctx, &model.Lesson{GroupID: g.ID, ZoomMeetingID: "", PlannedStartAt: t0, PlannedEndAt: t0}), ErrInvalid)
		wantErr(t, e.s.CreateLesson(e.ctx, &model.Lesson{GroupID: g.ID, ZoomMeetingID: "1", PlannedStartAt: t0, PlannedEndAt: t0.Add(-time.Second)}), ErrInvalid)
		wantErr(t, e.s.CreateLesson(e.ctx, &model.Lesson{GroupID: g.ID, ZoomMeetingID: "1"}), ErrInvalid) // zero planned times
		wantErr(t, e.s.UpdateLesson(e.ctx, &model.Lesson{ID: l.ID, ZoomMeetingID: "1", PlannedEndAt: t0}), ErrInvalid)

		// Update.
		e.clock.Advance(time.Minute)
		got.Title = "Новое"
		got.ZoomMeetingID = "123456789"
		got.PlannedStartAt = t0.Add(time.Hour)
		got.PlannedEndAt = t0.Add(2 * time.Hour)
		got.Status = model.LessonFinished // ignored by UpdateLesson
		e.must(e.s.UpdateLesson(e.ctx, got))
		again, _ := e.s.GetLesson(e.ctx, l.ID)
		if again.Title != "Новое" || again.ZoomMeetingID != "123456789" || !again.PlannedStartAt.Equal(t0.Add(time.Hour)) ||
			again.Status != model.LessonPlanned || !again.UpdatedAt.Equal(e.clock.Now().Truncate(time.Millisecond)) {
			t.Errorf("after update = %+v", again)
		}
		wantErr(t, e.s.UpdateLesson(e.ctx, &model.Lesson{ID: 999, ZoomMeetingID: "1", PlannedStartAt: t0, PlannedEndAt: t0}), ErrNotFound)

		// Computed columns and manual finish.
		as, ae := t0.Add(61*time.Minute), t0.Add(119*time.Minute)
		e.must(e.s.SetLessonComputed(e.ctx, l.ID, &as, &ae, model.LessonFinished))
		mf := t0.Add(118 * time.Minute)
		e.must(e.s.SetLessonManualFinished(e.ctx, l.ID, &mf))
		again, _ = e.s.GetLesson(e.ctx, l.ID)
		wantTime(t, "actual start", again.ActualStartAt, &as)
		wantTime(t, "actual end", again.ActualEndAt, &ae)
		wantTime(t, "manual finished", again.ManualFinishedAt, &mf)
		if again.Status != model.LessonFinished {
			t.Errorf("status = %q", again.Status)
		}
		e.must(e.s.SetLessonComputed(e.ctx, l.ID, &as, nil, model.LessonActive))
		e.must(e.s.SetLessonManualFinished(e.ctx, l.ID, nil))
		again, _ = e.s.GetLesson(e.ctx, l.ID)
		if again.ActualEndAt != nil || again.ManualFinishedAt != nil || again.Status != model.LessonActive {
			t.Errorf("after reopen = %+v", again)
		}
		wantErr(t, e.s.SetLessonComputed(e.ctx, l.ID, nil, nil, "bogus"), ErrInvalid)
		wantErr(t, e.s.SetLessonComputed(e.ctx, 999, nil, nil, model.LessonPlanned), ErrNotFound)
		wantErr(t, e.s.SetLessonManualFinished(e.ctx, 999, nil), ErrNotFound)

		// List filters.
		day := 24 * time.Hour
		l2 := e.lesson(g.ID, "111111111", t0.Add(day), time.Hour)
		l3 := e.lesson(g2.ID, "111111111", t0.Add(2*day), time.Hour)
		l4 := e.lesson(g.ID, "111111111", t0.Add(3*day), time.Hour)
		ids := func(ls []model.Lesson) []int64 {
			var out []int64
			for _, x := range ls {
				out = append(out, x.ID)
			}
			return out
		}
		all, err := e.s.ListLessons(e.ctx, LessonFilter{})
		e.must(err)
		if got, want := ids(all), []int64{l.ID, l2.ID, l3.ID, l4.ID}; !equalIDs(got, want) {
			t.Errorf("all = %v, want %v", got, want)
		}
		from, to := t0.Add(day), t0.Add(3*day) // to is exclusive
		rng, _ := e.s.ListLessons(e.ctx, LessonFilter{From: &from, To: &to})
		if got, want := ids(rng), []int64{l2.ID, l3.ID}; !equalIDs(got, want) {
			t.Errorf("range = %v, want %v", got, want)
		}
		byGroup, _ := e.s.ListLessons(e.ctx, LessonFilter{GroupID: &g.ID, Desc: true, Limit: 2})
		if got, want := ids(byGroup), []int64{l4.ID, l2.ID}; !equalIDs(got, want) {
			t.Errorf("group desc limit = %v, want %v", got, want)
		}
		st := model.LessonActive
		byStatus, _ := e.s.ListLessons(e.ctx, LessonFilter{Status: &st})
		if got, want := ids(byStatus), []int64{l.ID}; !equalIDs(got, want) {
			t.Errorf("status = %v, want %v", got, want)
		}
	})
}

func equalIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFindLessonsByMeeting(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		g := e.group("G")
		const mid = "85746065432"
		ref := t0
		from, to := ref.Add(-30*time.Minute), ref.Add(time.Hour)
		// Inside, starting before from and ending inside, starting exactly at to
		// (edge, included), ending exactly at from (edge, included), after to,
		// before from, another meeting id.
		inside := e.lesson(g.ID, mid, ref, 30*time.Minute)
		overlapsStart := e.lesson(g.ID, mid, ref.Add(-2*time.Hour), 2*time.Hour)
		startsAtTo := e.lesson(g.ID, mid, to, time.Hour)
		endsAtFrom := e.lesson(g.ID, mid, from.Add(-time.Hour), time.Hour)
		e.lesson(g.ID, mid, to.Add(time.Minute), time.Hour)
		e.lesson(g.ID, mid, from.Add(-2*time.Hour), time.Hour+59*time.Minute)
		e.lesson(g.ID, "999999999", ref, time.Hour)

		got, err := e.s.FindLessonsByMeeting(e.ctx, mid, from, to)
		e.must(err)
		var ids []int64
		for _, l := range got {
			ids = append(ids, l.ID)
		}
		want := []int64{overlapsStart.ID, endsAtFrom.ID, inside.ID, startsAtTo.ID}
		if !equalIDs(ids, want) {
			t.Errorf("lessons = %v, want %v", ids, want)
		}
		none, err := e.s.FindLessonsByMeeting(e.ctx, "000", from, to)
		e.must(err)
		if len(none) != 0 {
			t.Errorf("unexpected lessons %v", none)
		}
	})
}

func TestDeleteLessonCascade(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		g := e.group("G")
		st := e.student(g.ID, "Иванов Иван")
		l := e.lesson(g.ID, "123456789", t0, time.Hour)
		keep := e.lesson(g.ID, "123456789", t0.Add(24*time.Hour), time.Hour)

		pids, err := e.s.UpsertParticipants(e.ctx, l.ID, []model.Participant{{NormalizedName: "иванов иван", DisplayName: "Иванов Иван",
			Resolution: model.ResolutionMatched, StudentID: &st.ID, CurrentStatus: model.CurrentLeft}})
		e.must(err)
		aids, err := e.s.UpsertAttendanceComputed(e.ctx, l.ID, []model.Attendance{{StudentID: st.ID, CurrentStatus: model.CurrentLeft,
			AutoStatus: model.FinalPresent}})
		e.must(err)
		pid, aid := pids["иванов иван"], aids[st.ID]
		e.must(e.s.ReplaceIntervals(e.ctx, l.ID, []model.Interval{{ZoomParticipantID: &pid, AttendanceID: &aid, MeetingUUID: "u1",
			ZoomUserName: "Иванов Иван", JoinAt: t0}}))
		e.must(e.s.UpsertMapping(e.ctx, &model.Mapping{LessonID: l.ID, NormalizedName: "teacher", Action: model.MappingIgnore}))
		m1, err := e.s.UpsertZoomMeeting(e.ctx, &model.ZoomMeeting{MeetingUUID: "u1", MeetingID: "123456789", FirstEventAt: t0})
		e.must(err)
		m2, err := e.s.UpsertZoomMeeting(e.ctx, &model.ZoomMeeting{MeetingUUID: "u2", MeetingID: "123456789", FirstEventAt: t0})
		e.must(err)
		e.must(e.s.SetZoomMeetingLink(e.ctx, m1.ID, &l.ID, model.LinkAuto))
		e.must(e.s.SetZoomMeetingLink(e.ctx, m2.ID, &l.ID, model.LinkManual))
		// A row in the other lesson must survive.
		_, err = e.s.UpsertAttendanceComputed(e.ctx, keep.ID, []model.Attendance{{StudentID: st.ID, CurrentStatus: model.CurrentNotSeen,
			AutoStatus: model.FinalAbsent}})
		e.must(err)

		e.must(e.s.DeleteLesson(e.ctx, l.ID))
		wantErr(t, e.s.DeleteLesson(e.ctx, l.ID), ErrNotFound)
		_, err = e.s.GetLesson(e.ctx, l.ID)
		wantErr(t, err, ErrNotFound)
		_, err = e.s.GetParticipant(e.ctx, pid)
		wantErr(t, err, ErrNotFound)
		_, err = e.s.GetAttendance(e.ctx, l.ID, st.ID)
		wantErr(t, err, ErrNotFound)
		ivs, _ := e.s.ListIntervals(e.ctx, l.ID)
		maps, _ := e.s.ListMappings(e.ctx, l.ID)
		if len(ivs) != 0 || len(maps) != 0 {
			t.Errorf("leftovers: %d intervals, %d mappings", len(ivs), len(maps))
		}
		for _, id := range []int64{m1.ID, m2.ID} {
			m, err := e.s.GetZoomMeeting(e.ctx, id)
			e.must(err)
			if m.LessonID != nil || m.LinkMode != model.LinkAuto {
				t.Errorf("meeting %d after delete = %+v", id, m)
			}
		}
		if _, err := e.s.GetAttendance(e.ctx, keep.ID, st.ID); err != nil {
			t.Errorf("other lesson attendance: %v", err)
		}
	})
}

func TestLessonCounts(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		g := e.group("G")
		var studs []*model.Student
		for _, n := range []string{"A", "B", "C", "D"} {
			studs = append(studs, e.student(g.ID, n))
		}
		l := e.lesson(g.ID, "123456789", t0, time.Hour)
		fresh := e.lesson(g.ID, "123456789", t0.Add(time.Hour), time.Hour) // never rebuilt
		_, err := e.s.UpsertAttendanceComputed(e.ctx, l.ID, []model.Attendance{
			{StudentID: studs[0].ID, CurrentStatus: model.CurrentOnline, AutoStatus: model.FinalPresent},
			{StudentID: studs[1].ID, CurrentStatus: model.CurrentLeft, AutoStatus: model.FinalPresent},
			{StudentID: studs[2].ID, CurrentStatus: model.CurrentNotSeen, AutoStatus: model.FinalAbsent},
		})
		e.must(err)
		// Manual override: C present after all.
		_, err = e.s.SetManualStatus(e.ctx, l.ID, studs[2].ID, ptr(model.FinalPresent), nil, t0)
		e.must(err)
		_, err = e.s.UpsertParticipants(e.ctx, l.ID, []model.Participant{
			{NormalizedName: "a", DisplayName: "A", Resolution: model.ResolutionMatched, StudentID: &studs[0].ID, CurrentStatus: model.CurrentOnline},
			{NormalizedName: "x", DisplayName: "X", Resolution: model.ResolutionUnidentified, CurrentStatus: model.CurrentOnline},
			{NormalizedName: "y", DisplayName: "Y", Resolution: model.ResolutionConflict, CandidateStudentIDs: []int64{1, 2}, CurrentStatus: model.CurrentLeft},
			{NormalizedName: "host", DisplayName: "Host", Resolution: model.ResolutionIgnored, IgnoreReason: model.IgnoreHost, CurrentStatus: model.CurrentOnline},
		})
		e.must(err)
		counts, err := e.s.LessonCounts(e.ctx, []int64{l.ID, fresh.ID, 999})
		e.must(err)
		if len(counts) != 2 {
			t.Fatalf("counts for %d lessons", len(counts))
		}
		want := Counts{InGroup: 3, Online: 1, Seen: 2, NotSeen: 1, Unidentified: 2, Present: 3, Absent: 0}
		if counts[l.ID] != want {
			t.Errorf("counts = %+v, want %+v", counts[l.ID], want)
		}
		if got, want := counts[fresh.ID], (Counts{InGroup: 4, NotSeen: 4, Absent: 4}); got != want {
			t.Errorf("fresh counts = %+v, want %+v", got, want)
		}
		empty, err := e.s.LessonCounts(e.ctx, nil)
		e.must(err)
		if len(empty) != 0 {
			t.Errorf("empty = %v", empty)
		}
	})
}

func TestLessonRefs(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		g := e.group("10А")
		h := e.group("11Б")
		a := e.lesson(g.ID, "123456789", t0, time.Hour)
		b := e.lesson(h.ID, "987654321", t0.Add(24*time.Hour), time.Hour)
		refs, err := e.s.LessonRefs(e.ctx, []int64{a.ID, b.ID, 9999})
		e.must(err)
		want := map[int64]LessonRef{
			a.ID: {ID: a.ID, Title: "Lesson", GroupID: g.ID, GroupName: "10А", PlannedStartAt: t0.Truncate(time.Millisecond)},
			b.ID: {ID: b.ID, Title: "Lesson", GroupID: h.ID, GroupName: "11Б", PlannedStartAt: t0.Add(24 * time.Hour).Truncate(time.Millisecond)},
		}
		if len(refs) != len(want) {
			t.Fatalf("refs = %+v", refs)
		}
		for id, w := range want {
			if got := refs[id]; got != w || got.PlannedStartAt.Location() != time.UTC {
				t.Errorf("ref %d = %+v, want %+v", id, got, w)
			}
		}
		if none, err := e.s.LessonRefs(e.ctx, nil); err != nil || len(none) != 0 {
			t.Errorf("no ids: %v %v", none, err)
		}
	})
}
