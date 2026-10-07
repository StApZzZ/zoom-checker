package store

import (
	"errors"
	"testing"
	"time"

	"zoom-checker/internal/model"
)

func TestGroups(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		g := &model.Group{Name: "  DevOps-2026-01 ", Description: "Вечерняя"}
		e.must(e.s.CreateGroup(e.ctx, g))
		if g.NormalizedName != "devops-2026-01" || g.Status != model.StatusActive || g.DefaultDurationMinutes != 90 {
			t.Errorf("defaults: %+v", g)
		}
		wantErr(t, e.s.CreateGroup(e.ctx, &model.Group{Name: "DEVOPS-2026-01"}), ErrConflict)
		wantErr(t, e.s.CreateGroup(e.ctx, &model.Group{Name: " ​ "}), ErrInvalid)

		got, err := e.s.GetGroup(e.ctx, g.ID)
		e.must(err)
		if got.Name != g.Name || got.Description != "Вечерняя" || !got.CreatedAt.Equal(t0.Truncate(time.Millisecond)) {
			t.Errorf("GetGroup = %+v", got)
		}
		byName, err := e.s.GetGroupByNormalizedName(e.ctx, "DevOps-2026-01")
		e.must(err)
		if byName.ID != g.ID {
			t.Error("GetGroupByNormalizedName mismatch")
		}
		_, err = e.s.GetGroup(e.ctx, 999)
		wantErr(t, err, ErrNotFound)

		g2 := e.group("Python-1")
		archived := &model.Group{Name: "Old", Status: model.StatusArchived, DefaultDurationMinutes: 45}
		e.must(e.s.CreateGroup(e.ctx, archived))

		// Summary aggregates.
		e.student(g.ID, "Иванов Иван")
		e.student(g.ID, "Петров Пётр")
		old := e.student(g.ID, "Сидоров")
		old.Status = model.StatusArchived
		e.must(e.s.UpdateStudent(e.ctx, old))
		now := t0.Add(24 * time.Hour)
		e.lesson(g.ID, "123456789", now.Add(-48*time.Hour), time.Hour)
		past := e.lesson(g.ID, "123456789", now.Add(-time.Hour), time.Hour)
		next := e.lesson(g.ID, "123456789", now.Add(time.Hour), time.Hour)
		e.lesson(g.ID, "123456789", now.Add(48*time.Hour), time.Hour)

		all, err := e.s.ListGroups(e.ctx, GroupFilterAll, now)
		e.must(err)
		if len(all) != 3 || all[0].ID != g.ID || all[1].ID != archived.ID || all[2].ID != g2.ID {
			t.Fatalf("ListGroups(all) order: %+v", all)
		}
		s0 := all[0]
		if s0.StudentCount != 2 {
			t.Errorf("student count = %d", s0.StudentCount)
		}
		wantTime(t, "last lesson", s0.LastLessonAt, &past.PlannedStartAt)
		wantTime(t, "next lesson", s0.NextLessonAt, &next.PlannedStartAt)
		if all[2].LastLessonAt != nil || all[2].NextLessonAt != nil || all[2].StudentCount != 0 {
			t.Errorf("empty group summary: %+v", all[2])
		}
		active, err := e.s.ListGroups(e.ctx, GroupFilterActive, now)
		e.must(err)
		if len(active) != 2 {
			t.Errorf("active groups = %d", len(active))
		}
		arch, err := e.s.ListGroups(e.ctx, GroupFilterArchived, now)
		e.must(err)
		if len(arch) != 1 || arch[0].DefaultDurationMinutes != 45 {
			t.Errorf("archived groups = %+v", arch)
		}
		if l, err := e.s.ListGroups(e.ctx, "", now); err != nil || len(l) != 3 {
			t.Errorf("ListGroups(\"\") = %d, %v", len(l), err)
		}
		_, err = e.s.ListGroups(e.ctx, "bogus", now)
		wantErr(t, err, ErrInvalid)
		sum, err := e.s.GetGroupSummary(e.ctx, g.ID, now)
		e.must(err)
		if sum.StudentCount != 2 || sum.NextLessonAt == nil {
			t.Errorf("GetGroupSummary = %+v", sum)
		}
		_, err = e.s.GetGroupSummary(e.ctx, 999, now)
		wantErr(t, err, ErrNotFound)

		// Update.
		e.clock.Advance(time.Second)
		g.Name = "DevOps 2026/01"
		g.Status = model.StatusArchived
		g.DefaultDurationMinutes = 120
		e.must(e.s.UpdateGroup(e.ctx, g))
		got, _ = e.s.GetGroup(e.ctx, g.ID)
		if got.NormalizedName != "devops 2026/01" || got.Status != model.StatusArchived || got.DefaultDurationMinutes != 120 ||
			!got.UpdatedAt.After(got.CreatedAt) {
			t.Errorf("after update: %+v", got)
		}
		g.Name = "python-1"
		wantErr(t, e.s.UpdateGroup(e.ctx, g), ErrConflict)
		g.Name = "x"
		g.Status = "bogus"
		wantErr(t, e.s.UpdateGroup(e.ctx, g), ErrInvalid)
		wantErr(t, e.s.UpdateGroup(e.ctx, &model.Group{ID: 999, Name: "n", Status: model.StatusActive}), ErrNotFound)
	})
}

func nameTaken(t *testing.T, err error, owner int64, isAlias bool) {
	t.Helper()
	var nt *NameTakenError
	if !errors.As(err, &nt) {
		t.Fatalf("err = %v, want *NameTakenError", err)
	}
	if !errors.Is(err, ErrNameTaken) {
		t.Fatal("errors.Is(err, ErrNameTaken) = false")
	}
	if nt.StudentID != owner || nt.IsAlias != isAlias {
		t.Fatalf("NameTakenError = %+v, want owner %d alias %v", nt, owner, isAlias)
	}
}

func TestStudentsAndAliases(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		g := e.group("G1")
		other := e.group("G2")
		ivan := e.student(g.ID, "Иванов  Иван")
		if ivan.NormalizedName != "иванов иван" || ivan.Status != model.StatusActive || ivan.Aliases == nil {
			t.Fatalf("created: %+v", ivan)
		}
		petr := e.student(g.ID, "Петров Пётр")
		if petr.NormalizedName != "петров петр" {
			t.Errorf("normalized = %q", petr.NormalizedName)
		}
		// Same name in another group is fine.
		e.student(other.ID, "Иванов Иван")

		// Duplicate active name.
		err := e.s.CreateStudent(e.ctx, &model.Student{GroupID: g.ID, FullName: "ИВАНОВ иван"})
		nameTaken(t, err, ivan.ID, false)
		wantErr(t, e.s.CreateStudent(e.ctx, &model.Student{GroupID: g.ID, FullName: "  "}), ErrInvalid)
		wantErr(t, e.s.CreateStudent(e.ctx, &model.Student{GroupID: 999, FullName: "X"}), ErrNotFound)

		// Aliases: alias vs other student's name, vs own name, vs other alias.
		a1 := &model.Alias{StudentID: ivan.ID, Alias: "Ivan Phone", CreatedBy: ptr(int64(7))}
		e.must(e.s.AddAlias(e.ctx, a1))
		if a1.ID == 0 || a1.GroupID != g.ID || a1.NormalizedAlias != "ivan phone" {
			t.Errorf("alias: %+v", a1)
		}
		nameTaken(t, e.s.AddAlias(e.ctx, &model.Alias{StudentID: ivan.ID, Alias: "петров петр"}), petr.ID, false)
		nameTaken(t, e.s.AddAlias(e.ctx, &model.Alias{StudentID: ivan.ID, Alias: "Иванов Иван"}), ivan.ID, false)
		nameTaken(t, e.s.AddAlias(e.ctx, &model.Alias{StudentID: petr.ID, Alias: "IVAN PHONE"}), ivan.ID, true)
		nameTaken(t, e.s.AddAlias(e.ctx, &model.Alias{StudentID: ivan.ID, Alias: "ivan phone"}), ivan.ID, true)
		wantErr(t, e.s.AddAlias(e.ctx, &model.Alias{StudentID: ivan.ID, Alias: ""}), ErrInvalid)
		wantErr(t, e.s.AddAlias(e.ctx, &model.Alias{StudentID: 999, Alias: "x"}), ErrNotFound)

		// Student name vs alias of another student (cross-table rule).
		nameTaken(t, e.s.CreateStudent(e.ctx, &model.Student{GroupID: g.ID, FullName: "Ivan phone"}), ivan.ID, true)
		petr.FullName = "Ivan Phone"
		nameTaken(t, e.s.UpdateStudent(e.ctx, petr), ivan.ID, true)
		// Renaming onto own alias is refused too.
		ivan.FullName = "Ivan Phone"
		nameTaken(t, e.s.UpdateStudent(e.ctx, ivan), ivan.ID, true)

		got, err := e.s.GetStudent(e.ctx, ivan.ID)
		e.must(err)
		if len(got.Aliases) != 1 || got.Aliases[0].Alias != "Ivan Phone" || got.Aliases[0].CreatedBy == nil || *got.Aliases[0].CreatedBy != 7 {
			t.Errorf("GetStudent aliases = %+v", got.Aliases)
		}

		// Archived students do not block names; their aliases leave the namespace.
		got.Status = model.StatusArchived
		e.must(e.s.UpdateStudent(e.ctx, got))
		ivan2 := e.student(g.ID, "Иванов Иван") // name free now
		// The alias index is per group regardless of the owner's status...
		nameTaken(t, e.s.AddAlias(e.ctx, &model.Alias{StudentID: ivan2.ID, Alias: "ivan phone"}), ivan.ID, true)
		// ...but an archived student's alias does not block a student name.
		phone := e.student(g.ID, "Ivan Phone")
		nameTaken(t, e.s.AddAlias(e.ctx, &model.Alias{StudentID: ivan2.ID, Alias: "ivan phone"}), phone.ID, false)
		// Restoring the archived student is blocked by the new holders of its name/alias.
		got.Status = model.StatusActive
		nameTaken(t, e.s.UpdateStudent(e.ctx, got), ivan2.ID, false)
		got.FullName = "Иванов Иван Старший"
		err = e.s.UpdateStudent(e.ctx, got)
		var nt *NameTakenError
		if !errors.As(err, &nt) || nt.Name != "ivan phone" {
			t.Fatalf("restore with taken alias: %v", err)
		}
		// After deleting the alias the restore works.
		deleted, err := e.s.DeleteAlias(e.ctx, ivan.ID, a1.ID)
		e.must(err)
		if deleted.Alias != "Ivan Phone" {
			t.Errorf("deleted alias = %+v", deleted)
		}
		_, err = e.s.DeleteAlias(e.ctx, ivan.ID, a1.ID)
		wantErr(t, err, ErrNotFound)
		_, err = e.s.DeleteAlias(e.ctx, petr.ID, 12345)
		wantErr(t, err, ErrNotFound)
		e.must(e.s.UpdateStudent(e.ctx, got))
		if got.GroupID != g.ID || got.Status != model.StatusActive || got.NormalizedName != "иванов иван старший" {
			t.Errorf("restored: %+v", got)
		}
		// Archived students may get aliases (checked against the alias index only).
		got.Status = model.StatusArchived
		e.must(e.s.UpdateStudent(e.ctx, got))
		e.must(e.s.AddAlias(e.ctx, &model.Alias{StudentID: got.ID, Alias: "петров петр"}))
		got.Status = model.StatusActive
		nameTaken(t, e.s.UpdateStudent(e.ctx, got), petr.ID, false)

		wantErr(t, e.s.UpdateStudent(e.ctx, &model.Student{ID: 999, FullName: "x", Status: model.StatusActive}), ErrNotFound)
		wantErr(t, e.s.UpdateStudent(e.ctx, &model.Student{ID: ivan.ID, FullName: "x", Status: "bogus"}), ErrInvalid)

		// Lists.
		e.must(e.s.AddAlias(e.ctx, &model.Alias{StudentID: petr.ID, Alias: "Petya"}))
		act, err := e.s.ListStudents(e.ctx, g.ID, false)
		e.must(err)
		var gotNames []string
		for _, st := range act {
			gotNames = append(gotNames, st.NormalizedName)
			if st.Aliases == nil {
				t.Errorf("nil aliases for %d", st.ID)
			}
		}
		if want := "[ivan phone иванов иван петров петр]"; sprint(gotNames) != want {
			t.Errorf("active students = %v, want %v", gotNames, want)
		}
		if act[2].ID != petr.ID || len(act[2].Aliases) != 1 || act[2].Aliases[0].NormalizedAlias != "petya" {
			t.Errorf("petr aliases = %+v", act[2])
		}
		allSt, err := e.s.ListStudents(e.ctx, g.ID, true)
		e.must(err)
		if len(allSt) != 4 {
			t.Errorf("all students = %d", len(allSt))
		}
		empty, err := e.s.ListStudents(e.ctx, 999, true)
		e.must(err)
		if empty == nil || len(empty) != 0 {
			t.Errorf("empty list = %#v", empty)
		}

		names, err := e.s.ListGroupNames(e.ctx, g.ID)
		e.must(err)
		var flat []string
		for _, n := range names {
			flat = append(flat, n.Normalized)
			if n.Normalized == "petya" && (!n.IsAlias || n.StudentID != petr.ID || n.Display != "Petya") {
				t.Errorf("alias entry = %+v", n)
			}
		}
		// The archived student's aliases are not in the namespace.
		if want := "[ivan phone petya иванов иван петров петр]"; sprint(flat) != want {
			t.Errorf("group names = %v, want %v", flat, want)
		}
	})
}

func sprint(v []string) string {
	out := "["
	for i, s := range v {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out + "]"
}

func TestApplyImport(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		g := e.group("Import")
		a := e.student(g.ID, "Alpha")
		b := e.student(g.ID, "Beta")
		c := e.student(g.ID, "Gamma")
		arch := e.student(g.ID, "Delta")
		arch.Status = model.StatusArchived
		e.must(e.s.UpdateStudent(e.ctx, arch))
		e.must(e.s.AddAlias(e.ctx, &model.Alias{StudentID: c.ID, Alias: "Gam"}))

		// Swap names of a and b, archive c, restore "Delta" under a new external id,
		// create a new student taking c's freed name.
		res, err := e.s.ApplyImport(e.ctx, g.ID, ImportOps{
			Update: []ImportUpdate{
				{StudentID: a.ID, FullName: "Beta", ExternalID: "1"},
				{StudentID: b.ID, FullName: "Alpha", ExternalID: "2"},
				{StudentID: arch.ID, FullName: "Delta", ExternalID: "4"},
			},
			Archive: []int64{c.ID},
			Create:  []ImportCreate{{FullName: "Gamma", ExternalID: "5"}, {FullName: "Epsilon"}},
		})
		e.must(err)
		if len(res.CreatedIDs) != 2 || res.Updated != 3 || res.Archived != 1 {
			t.Errorf("result = %+v", res)
		}
		list, err := e.s.ListStudents(e.ctx, g.ID, true)
		e.must(err)
		state := map[int64]model.Student{}
		for _, st := range list {
			state[st.ID] = st
		}
		if s := state[a.ID]; s.NormalizedName != "beta" || s.ExternalID != "1" {
			t.Errorf("a = %+v", s)
		}
		if s := state[b.ID]; s.NormalizedName != "alpha" || s.ExternalID != "2" {
			t.Errorf("b = %+v", s)
		}
		if s := state[c.ID]; s.Status != model.StatusArchived {
			t.Errorf("c = %+v", s)
		}
		if s := state[arch.ID]; s.Status != model.StatusActive || s.ExternalID != "4" {
			t.Errorf("restored = %+v", s)
		}
		if s := state[res.CreatedIDs[0]]; s.NormalizedName != "gamma" || s.ExternalID != "5" || s.Status != model.StatusActive {
			t.Errorf("created = %+v", s)
		}

		// No-op update is not counted.
		res, err = e.s.ApplyImport(e.ctx, g.ID, ImportOps{Update: []ImportUpdate{{StudentID: a.ID, FullName: "Beta", ExternalID: "1"}}})
		e.must(err)
		if res.Updated != 0 || len(res.CreatedIDs) != 0 {
			t.Errorf("noop result = %+v", res)
		}

		snapshot := func() string {
			l, err := e.s.ListStudents(e.ctx, g.ID, true)
			e.must(err)
			out := ""
			for _, st := range l {
				out += st.NormalizedName + "/" + string(st.Status) + "/" + st.ExternalID + ";"
			}
			return out
		}
		before := snapshot()

		// Conflicts are detected on the final state and nothing is written.
		_, err = e.s.ApplyImport(e.ctx, g.ID, ImportOps{
			Archive: []int64{arch.ID},
			Create:  []ImportCreate{{FullName: "Zeta"}, {FullName: "zeta"}},
		})
		nameTaken(t, err, 0, false)
		// New student vs existing alias (alias "gam" belongs to archived c → free; restore c first).
		_, err = e.s.ApplyImport(e.ctx, g.ID, ImportOps{
			Update: []ImportUpdate{{StudentID: c.ID, FullName: "Gamma Old"}},
			Create: []ImportCreate{{FullName: "GAM"}},
		})
		nameTaken(t, err, c.ID, true)
		// Update onto an untouched active name.
		_, err = e.s.ApplyImport(e.ctx, g.ID, ImportOps{Update: []ImportUpdate{{StudentID: a.ID, FullName: "Alpha"}}})
		nameTaken(t, err, b.ID, false) // the untouched holder is reported, not the renamed student
		_, err = e.s.ApplyImport(e.ctx, g.ID, ImportOps{Archive: []int64{a.ID}, Update: []ImportUpdate{{StudentID: a.ID, FullName: "A"}}})
		wantErr(t, err, ErrInvalid)
		other := e.group("Other")
		stranger := e.student(other.ID, "Stranger")
		_, err = e.s.ApplyImport(e.ctx, g.ID, ImportOps{Archive: []int64{stranger.ID}})
		wantErr(t, err, ErrInvalid)
		_, err = e.s.ApplyImport(e.ctx, g.ID, ImportOps{Create: []ImportCreate{{FullName: "\t"}}})
		wantErr(t, err, ErrInvalid)
		_, err = e.s.ApplyImport(e.ctx, 999, ImportOps{})
		wantErr(t, err, ErrNotFound)
		if after := snapshot(); after != before {
			t.Errorf("failed imports changed data:\n%s\n%s", before, after)
		}

		// A restore onto a name that is being freed in the same import works.
		res, err = e.s.ApplyImport(e.ctx, g.ID, ImportOps{
			Archive: []int64{res0(t, e, g.ID, "gamma")},
			Update:  []ImportUpdate{{StudentID: c.ID, FullName: "Gamma"}},
		})
		e.must(err)
		if res.Archived != 1 || res.Updated != 1 {
			t.Errorf("result = %+v", res)
		}
	})
}

// res0 returns the id of the active student with the given normalized name.
func res0(t *testing.T, e *env, groupID int64, normalized string) int64 {
	t.Helper()
	l, err := e.s.ListStudents(e.ctx, groupID, false)
	e.must(err)
	for _, st := range l {
		if st.NormalizedName == normalized {
			return st.ID
		}
	}
	t.Fatalf("no active student %q", normalized)
	return 0
}

// TestNameRuleConcurrent races student creations and alias additions for the
// same name; the cross-table rule must let exactly one of them win.
func TestNameRuleConcurrent(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		for round := range 5 {
			g := e.group("Race " + string(rune('A'+round)))
			owners := make([]*model.Student, 4)
			for i := range owners {
				owners[i] = e.student(g.ID, "Owner "+string(rune('a'+i)))
			}
			const workers = 8
			errs := make(chan error, workers)
			start := make(chan struct{})
			for i := range workers {
				go func() {
					<-start
					if i%2 == 0 {
						errs <- e.s.CreateStudent(e.ctx, &model.Student{GroupID: g.ID, FullName: "Contested Name"})
					} else {
						errs <- e.s.AddAlias(e.ctx, &model.Alias{StudentID: owners[i/2].ID, Alias: "contested  name"})
					}
				}()
			}
			close(start)
			wins := 0
			for range workers {
				err := <-errs
				switch {
				case err == nil:
					wins++
				case errors.Is(err, ErrNameTaken):
				default:
					t.Fatalf("unexpected error: %v", err)
				}
			}
			if wins != 1 {
				t.Fatalf("round %d: %d winners, want exactly 1", round, wins)
			}
			names, err := e.s.ListGroupNames(e.ctx, g.ID)
			e.must(err)
			n := 0
			for _, gn := range names {
				if gn.Normalized == "contested name" {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("round %d: name stored %d times", round, n)
			}
		}
	})
}
