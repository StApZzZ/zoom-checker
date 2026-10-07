package store

import (
	"fmt"
	"testing"
	"time"

	"zoom-checker/internal/model"
)

func TestUpsertParticipants(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		g := e.group("G")
		st := e.student(g.ID, "Иванов Иван")
		l := e.lesson(g.ID, "123456789", t0, time.Hour)
		other := e.lesson(g.ID, "123456789", t0.Add(24*time.Hour), time.Hour)
		join := t0.Add(time.Minute)
		ps := []model.Participant{
			{NormalizedName: "иванов иван", DisplayName: "Иванов Иван", Resolution: model.ResolutionMatched, StudentID: &st.ID,
				CurrentStatus: model.CurrentOnline, FirstJoinAt: &join, TotalSeconds: 60, Devices: 2},
			{NormalizedName: "guest", DisplayName: "Guest", Resolution: model.ResolutionConflict, CandidateStudentIDs: []int64{5, 7},
				CurrentStatus: model.CurrentLeft},
			{NormalizedName: "host", DisplayName: "Host", Resolution: model.ResolutionIgnored, IgnoreReason: model.IgnoreHost,
				CurrentStatus: model.CurrentOnline},
			{NormalizedName: "", DisplayName: "​", Resolution: model.ResolutionUnidentified, CurrentStatus: model.CurrentLeft},
		}
		ids, err := e.s.UpsertParticipants(e.ctx, l.ID, ps)
		e.must(err)
		if len(ids) != 4 {
			t.Fatalf("ids = %v", ids)
		}
		// Another lesson with the same names is independent.
		otherIDs, err := e.s.UpsertParticipants(e.ctx, other.ID, ps[:1])
		e.must(err)
		if otherIDs["иванов иван"] == ids["иванов иван"] {
			t.Error("participant ids shared across lessons")
		}

		got, err := e.s.GetParticipant(e.ctx, ids["guest"])
		e.must(err)
		if got.Resolution != model.ResolutionConflict || fmt.Sprint(got.CandidateStudentIDs) != "[5 7]" || got.StudentID != nil ||
			got.LessonID != l.ID || got.DisplayName != "Guest" || !got.ComputedAt.Equal(t0.Truncate(time.Millisecond)) {
			t.Errorf("guest = %+v", got)
		}
		iv, _ := e.s.GetParticipant(e.ctx, ids["иванов иван"])
		if iv.StudentID == nil || *iv.StudentID != st.ID || iv.Devices != 2 || iv.TotalSeconds != 60 || iv.FirstJoinAt == nil ||
			!iv.FirstJoinAt.Equal(join) || iv.LastLeaveAt != nil || iv.CandidateStudentIDs != nil {
			t.Errorf("ivan = %+v", iv)
		}

		// Rebuild: ids stay stable for remaining names, values are replaced,
		// vanished names are deleted together with their intervals.
		hostID := ids["host"]
		e.must(e.s.ReplaceIntervals(e.ctx, l.ID, []model.Interval{{ZoomParticipantID: &hostID, MeetingUUID: "u", ZoomUserName: "Host", JoinAt: t0}}))
		e.clock.Advance(time.Minute)
		leave := t0.Add(30 * time.Minute)
		ps2 := []model.Participant{
			{NormalizedName: "иванов иван", DisplayName: "ИВАНОВ ИВАН", Resolution: model.ResolutionManual, StudentID: &st.ID,
				CurrentStatus: model.CurrentLeft, FirstJoinAt: &join, LastLeaveAt: &leave, TotalSeconds: 1740, Devices: 1},
			{NormalizedName: "guest", DisplayName: "Guest", Resolution: model.ResolutionUnidentified, CurrentStatus: model.CurrentLeft},
			{NormalizedName: "new", DisplayName: "New", Resolution: model.ResolutionUnidentified, CurrentStatus: model.CurrentOnline},
		}
		ids2, err := e.s.UpsertParticipants(e.ctx, l.ID, ps2)
		e.must(err)
		if ids2["иванов иван"] != ids["иванов иван"] || ids2["guest"] != ids["guest"] || ids2["new"] == 0 {
			t.Errorf("ids not stable: %v vs %v", ids, ids2)
		}
		list, err := e.s.ListParticipants(e.ctx, l.ID)
		e.must(err)
		if len(list) != 3 {
			t.Fatalf("participants = %+v", list)
		}
		for _, p := range list {
			if p.NormalizedName == "guest" && (p.CandidateStudentIDs != nil || p.Resolution != model.ResolutionUnidentified) {
				t.Errorf("guest not replaced: %+v", p)
			}
			if p.NormalizedName == "иванов иван" && (p.DisplayName != "ИВАНОВ ИВАН" || p.LastLeaveAt == nil || p.TotalSeconds != 1740 ||
				!p.UpdatedAt.Equal(e.clock.Now().Truncate(time.Millisecond))) {
				t.Errorf("ivan not replaced: %+v", p)
			}
		}
		_, err = e.s.GetParticipant(e.ctx, hostID)
		wantErr(t, err, ErrNotFound)
		if ivs, _ := e.s.ListIntervals(e.ctx, l.ID); len(ivs) != 0 {
			t.Errorf("intervals of deleted participant survived: %+v", ivs)
		}
		if o, _ := e.s.ListParticipants(e.ctx, other.ID); len(o) != 1 {
			t.Errorf("other lesson touched: %+v", o)
		}

		// Empty list clears the lesson.
		ids3, err := e.s.UpsertParticipants(e.ctx, l.ID, nil)
		e.must(err)
		if len(ids3) != 0 {
			t.Errorf("ids3 = %v", ids3)
		}
		if list, _ := e.s.ListParticipants(e.ctx, l.ID); len(list) != 0 {
			t.Errorf("not cleared: %+v", list)
		}
		_, err = e.s.UpsertParticipants(e.ctx, l.ID, []model.Participant{ps[0], ps[0]})
		wantErr(t, err, ErrInvalid)
		_, err = e.s.GetParticipant(e.ctx, 99999)
		wantErr(t, err, ErrNotFound)
	})
}

func TestUpsertParticipantsLargeBatch(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		g := e.group("G")
		l := e.lesson(g.ID, "123456789", t0, time.Hour)
		var ps []model.Participant
		for i := range 2*upsertChunk + 17 {
			ps = append(ps, model.Participant{NormalizedName: fmt.Sprintf("p%04d", i), DisplayName: "P", Resolution: model.ResolutionUnidentified,
				CurrentStatus: model.CurrentOnline})
		}
		ids, err := e.s.UpsertParticipants(e.ctx, l.ID, ps)
		e.must(err)
		if len(ids) != len(ps) {
			t.Fatalf("ids = %d", len(ids))
		}
		ivs := make([]model.Interval, 0, len(ps))
		for _, p := range ps {
			id := ids[p.NormalizedName]
			ivs = append(ivs, model.Interval{ZoomParticipantID: &id, MeetingUUID: "u", ZoomUserName: "P", JoinAt: t0})
		}
		e.must(e.s.ReplaceIntervals(e.ctx, l.ID, ivs))
		got, err := e.s.ListIntervals(e.ctx, l.ID)
		e.must(err)
		if len(got) != len(ps) {
			t.Errorf("intervals = %d", len(got))
		}
		ids2, err := e.s.UpsertParticipants(e.ctx, l.ID, ps[:upsertChunk+1])
		e.must(err)
		for n, id := range ids2 {
			if ids[n] != id {
				t.Fatalf("id of %s changed", n)
			}
		}
		if list, _ := e.s.ListParticipants(e.ctx, l.ID); len(list) != upsertChunk+1 {
			t.Errorf("participants after shrink = %d", len(list))
		}
	})
}

func TestAttendanceUpsertAndManual(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		g := e.group("G")
		a := e.student(g.ID, "A")
		b := e.student(g.ID, "B")
		c := e.student(g.ID, "C")
		l := e.lesson(g.ID, "123456789", t0, time.Hour)
		join := t0.Add(5 * time.Minute)
		rows := []model.Attendance{
			{StudentID: b.ID, FirstJoinAt: &join, TotalDurationSeconds: 120, LateSeconds: ptr(int64(300)),
				CurrentStatus: model.CurrentOnline, AutoStatus: model.FinalPresent,
				// Manual fields in the input are ignored.
				ManualStatus: ptr(model.FinalAbsent), Comment: "ignored"},
			{StudentID: a.ID, CurrentStatus: model.CurrentNotSeen, AutoStatus: model.FinalAbsent},
		}
		ids, err := e.s.UpsertAttendanceComputed(e.ctx, l.ID, rows)
		e.must(err)
		if len(ids) != 2 || ids[a.ID] == 0 || ids[b.ID] == 0 {
			t.Fatalf("ids = %v", ids)
		}
		gotB, err := e.s.GetAttendance(e.ctx, l.ID, b.ID)
		e.must(err)
		if gotB.FinalStatus != model.FinalPresent || gotB.ManualStatus != nil || gotB.Comment != "" || gotB.LateSeconds == nil ||
			*gotB.LateSeconds != 300 || gotB.EarlyLeaveSeconds != nil || gotB.TotalDurationSeconds != 120 ||
			gotB.FirstJoinAt == nil || !gotB.FirstJoinAt.Equal(join) || gotB.ID != ids[b.ID] {
			t.Errorf("b = %+v", gotB)
		}

		// Teacher overrides A to present with a comment.
		by := int64(9)
		at := t0.Add(10 * time.Minute)
		upd, err := e.s.SetManualStatus(e.ctx, l.ID, a.ID, ptr(model.FinalPresent), &by, at)
		e.must(err)
		if upd.ManualStatus == nil || *upd.ManualStatus != model.FinalPresent || upd.FinalStatus != model.FinalPresent ||
			upd.AutoStatus != model.FinalAbsent || upd.ManualBy == nil || *upd.ManualBy != 9 || upd.ManualAt == nil || !upd.ManualAt.Equal(at) {
			t.Errorf("after manual = %+v", upd)
		}
		upd, err = e.s.SetAttendanceComment(e.ctx, l.ID, a.ID, "Был без камеры", &by, at.Add(time.Minute))
		e.must(err)
		if upd.Comment != "Был без камеры" || upd.FinalStatus != model.FinalPresent {
			t.Errorf("after comment = %+v", upd)
		}

		// Rebuild keeps manual columns and recomputes final status; ids stay stable.
		e.clock.Advance(time.Minute)
		rows2 := []model.Attendance{
			{StudentID: a.ID, CurrentStatus: model.CurrentNotSeen, AutoStatus: model.FinalAbsent},
			{StudentID: b.ID, FirstJoinAt: &join, TotalDurationSeconds: 60, CurrentStatus: model.CurrentLeft, AutoStatus: model.FinalAbsent,
				EarlyLeaveSeconds: ptr(int64(1800))},
			{StudentID: c.ID, CurrentStatus: model.CurrentNotSeen, AutoStatus: model.FinalAbsent},
		}
		ids2, err := e.s.UpsertAttendanceComputed(e.ctx, l.ID, rows2)
		e.must(err)
		if ids2[a.ID] != ids[a.ID] || ids2[b.ID] != ids[b.ID] || ids2[c.ID] == 0 {
			t.Errorf("ids changed: %v → %v", ids, ids2)
		}
		gotA, _ := e.s.GetAttendance(e.ctx, l.ID, a.ID)
		if gotA.ManualStatus == nil || *gotA.ManualStatus != model.FinalPresent || gotA.FinalStatus != model.FinalPresent ||
			gotA.Comment != "Был без камеры" || gotA.ManualBy == nil || gotA.ManualAt == nil || gotA.AutoStatus != model.FinalAbsent ||
			!gotA.ComputedAt.Equal(e.clock.Now().Truncate(time.Millisecond)) || !gotA.CreatedAt.Equal(t0.Truncate(time.Millisecond)) {
			t.Errorf("a after rebuild = %+v", gotA)
		}
		gotB, _ = e.s.GetAttendance(e.ctx, l.ID, b.ID)
		if gotB.FinalStatus != model.FinalAbsent || gotB.CurrentStatus != model.CurrentLeft || gotB.EarlyLeaveSeconds == nil ||
			*gotB.EarlyLeaveSeconds != 1800 || gotB.LateSeconds != nil {
			t.Errorf("b after rebuild = %+v", gotB)
		}

		// Back to automatic.
		upd, err = e.s.SetManualStatus(e.ctx, l.ID, a.ID, nil, &by, at)
		e.must(err)
		if upd.ManualStatus != nil || upd.FinalStatus != model.FinalAbsent || upd.Comment != "Был без камеры" {
			t.Errorf("after clearing = %+v", upd)
		}
		_, err = e.s.SetManualStatus(e.ctx, l.ID, 9999, ptr(model.FinalPresent), nil, at)
		wantErr(t, err, ErrNotFound)
		_, err = e.s.SetManualStatus(e.ctx, l.ID, a.ID, ptr(model.FinalStatus("maybe")), nil, at)
		wantErr(t, err, ErrInvalid)
		_, err = e.s.SetAttendanceComment(e.ctx, 9999, a.ID, "x", nil, at)
		wantErr(t, err, ErrNotFound)
		_, err = e.s.UpsertAttendanceComputed(e.ctx, l.ID, []model.Attendance{rows2[0], rows2[0]})
		wantErr(t, err, ErrInvalid)

		// List ordered by student name.
		list, err := e.s.ListAttendance(e.ctx, l.ID)
		e.must(err)
		if len(list) != 3 || list[0].StudentID != a.ID || list[1].StudentID != b.ID || list[2].StudentID != c.ID {
			t.Errorf("list order = %+v", list)
		}

		// Intervals per student; deleting attendance detaches its intervals.
		pids, err := e.s.UpsertParticipants(e.ctx, l.ID, []model.Participant{
			{NormalizedName: "b", DisplayName: "B", Resolution: model.ResolutionMatched, StudentID: &b.ID, CurrentStatus: model.CurrentLeft},
			{NormalizedName: "b phone", DisplayName: "B phone", Resolution: model.ResolutionManual, StudentID: &b.ID, CurrentStatus: model.CurrentLeft},
			{NormalizedName: "x", DisplayName: "X", Resolution: model.ResolutionUnidentified, CurrentStatus: model.CurrentOnline},
		})
		e.must(err)
		pb, pbp, px := pids["b"], pids["b phone"], pids["x"]
		bAtt := ids[b.ID]
		l1, l2 := t0.Add(20*time.Minute), t0.Add(40*time.Minute)
		ivs := []model.Interval{
			{ZoomParticipantID: &pbp, AttendanceID: &bAtt, MeetingUUID: "u1", ZoomParticipantUUID: "pu2", ZoomUserName: "B phone",
				JoinAt: t0.Add(15 * time.Minute), LeaveAt: &l2, DurationSeconds: ptr(int64(1500)), LeaveReason: "left the meeting"},
			{ZoomParticipantID: &pb, AttendanceID: &bAtt, MeetingUUID: "u1", ZoomParticipantUUID: "pu1", ZoomUserName: "B",
				JoinAt: t0.Add(5 * time.Minute), LeaveAt: &l1, DurationSeconds: ptr(int64(900))},
			{ZoomParticipantID: &px, MeetingUUID: "u1", ZoomUserName: "X", JoinAt: t0.Add(time.Minute)},
		}
		e.must(e.s.ReplaceIntervals(e.ctx, l.ID, ivs))
		all, err := e.s.ListIntervals(e.ctx, l.ID)
		e.must(err)
		if len(all) != 3 || all[0].ZoomUserName != "X" || all[0].LeaveAt != nil || all[0].DurationSeconds != nil || all[0].AttendanceID != nil {
			t.Fatalf("intervals = %+v", all)
		}
		mine, err := e.s.ListStudentIntervals(e.ctx, l.ID, b.ID)
		e.must(err)
		if len(mine) != 2 || mine[0].ZoomUserName != "B" || mine[1].LeaveReason != "left the meeting" || mine[1].ZoomParticipantUUID != "pu2" ||
			*mine[1].DurationSeconds != 1500 || !mine[1].LeaveAt.Equal(l2) || mine[1].LessonID != l.ID {
			t.Errorf("student intervals = %+v", mine)
		}
		// Replace is idempotent and replaces.
		e.must(e.s.ReplaceIntervals(e.ctx, l.ID, ivs[:1]))
		if all, _ := e.s.ListIntervals(e.ctx, l.ID); len(all) != 1 {
			t.Errorf("after replace = %d", len(all))
		}
		n, err := e.s.DeleteAttendance(e.ctx, l.ID, []int64{b.ID, c.ID, 9999})
		e.must(err)
		if n != 2 {
			t.Errorf("deleted %d", n)
		}
		all, _ = e.s.ListIntervals(e.ctx, l.ID)
		if len(all) != 1 || all[0].AttendanceID != nil {
			t.Errorf("interval not detached: %+v", all)
		}
		if mine, _ := e.s.ListStudentIntervals(e.ctx, l.ID, b.ID); len(mine) != 0 {
			t.Errorf("student intervals after delete = %+v", mine)
		}
		_, err = e.s.GetAttendance(e.ctx, l.ID, b.ID)
		wantErr(t, err, ErrNotFound)
		if n, _ := e.s.DeleteAttendance(e.ctx, l.ID, nil); n != 0 {
			t.Errorf("empty delete = %d", n)
		}
	})
}

func TestMappings(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		g := e.group("G")
		st := e.student(g.ID, "A")
		l := e.lesson(g.ID, "123456789", t0, time.Hour)
		by := int64(3)
		m := &model.Mapping{LessonID: l.ID, NormalizedName: "vanya", Action: model.MappingStudent, StudentID: &st.ID, CreatedBy: &by}
		e.must(e.s.UpsertMapping(e.ctx, m))
		if m.ID == 0 {
			t.Fatal("no id")
		}
		got, err := e.s.GetMapping(e.ctx, l.ID, "vanya")
		e.must(err)
		if got.Action != model.MappingStudent || got.StudentID == nil || *got.StudentID != st.ID || got.CreatedBy == nil || *got.CreatedBy != 3 {
			t.Errorf("mapping = %+v", got)
		}
		// Replace with "ignore": student id is cleared, id stays.
		m2 := &model.Mapping{LessonID: l.ID, NormalizedName: "vanya", Action: model.MappingIgnore, StudentID: &st.ID}
		e.must(e.s.UpsertMapping(e.ctx, m2))
		got, _ = e.s.GetMapping(e.ctx, l.ID, "vanya")
		if got.ID != m.ID || got.Action != model.MappingIgnore || got.StudentID != nil || got.CreatedBy != nil {
			t.Errorf("replaced mapping = %+v", got)
		}
		e.must(e.s.UpsertMapping(e.ctx, &model.Mapping{LessonID: l.ID, NormalizedName: "anna", Action: model.MappingNone}))
		list, err := e.s.ListMappings(e.ctx, l.ID)
		e.must(err)
		if len(list) != 2 || list[0].NormalizedName != "anna" {
			t.Errorf("list = %+v", list)
		}
		wantErr(t, e.s.UpsertMapping(e.ctx, &model.Mapping{LessonID: l.ID, NormalizedName: "x", Action: model.MappingStudent}), ErrInvalid)
		wantErr(t, e.s.UpsertMapping(e.ctx, &model.Mapping{LessonID: l.ID, NormalizedName: "x", Action: "bogus"}), ErrInvalid)
		wantErr(t, e.s.UpsertMapping(e.ctx, &model.Mapping{LessonID: 999, NormalizedName: "x", Action: model.MappingNone}), ErrNotFound)
		e.must(e.s.DeleteMapping(e.ctx, l.ID, "vanya"))
		wantErr(t, e.s.DeleteMapping(e.ctx, l.ID, "vanya"), ErrNotFound)
		_, err = e.s.GetMapping(e.ctx, l.ID, "vanya")
		wantErr(t, err, ErrNotFound)
	})
}

// TestUpsertAttendanceLargeBatch: more rows than one multi-row INSERT carries;
// every student gets a row id and a re-run keeps the ids.
func TestUpsertAttendanceLargeBatch(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		g := e.group("Big")
		var creates []ImportCreate
		for i := range upsertChunk*2 + 7 {
			creates = append(creates, ImportCreate{FullName: fmt.Sprintf("Student %03d", i)})
		}
		res, err := e.s.ApplyImport(e.ctx, g.ID, ImportOps{Create: creates})
		e.must(err)
		l := e.lesson(g.ID, "123456789", t0, time.Hour)
		rows := make([]model.Attendance, len(res.CreatedIDs))
		for i, id := range res.CreatedIDs {
			rows[i] = model.Attendance{StudentID: id, CurrentStatus: model.CurrentNotSeen, AutoStatus: model.FinalAbsent}
		}
		ids, err := e.s.UpsertAttendanceComputed(e.ctx, l.ID, rows)
		e.must(err)
		if len(ids) != len(rows) {
			t.Fatalf("got %d ids for %d rows", len(ids), len(rows))
		}
		again, err := e.s.UpsertAttendanceComputed(e.ctx, l.ID, rows)
		e.must(err)
		for sid, id := range ids {
			if again[sid] != id {
				t.Fatalf("id of student %d changed: %d → %d", sid, id, again[sid])
			}
		}
		counts, err := e.s.LessonCounts(e.ctx, []int64{l.ID})
		e.must(err)
		if c := counts[l.ID]; c.InGroup != len(rows) || c.NotSeen != len(rows) || c.Absent != len(rows) {
			t.Errorf("counts = %+v", c)
		}
	})
}
