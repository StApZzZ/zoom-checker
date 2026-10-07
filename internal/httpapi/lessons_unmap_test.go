package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"zoom-checker/internal/model"
)

// "Отменить сопоставление" after "Это студент… + Запомнить это имя": the
// remembered alias goes away with the mapping, so the name is unidentified
// again here and in the group's other lessons (not silently re-matched).
func TestLessonUnmapRemovesRememberedAlias(t *testing.T) {
	t.Parallel()
	e, z := newLTRealEnv(t)
	g := e.group("DevOps-3")
	alex := e.student(g.ID, "Александров Александр", "").ID
	e.clock.Set(ltAt("17:00"))
	first := e.lesson(g.ID, "Linux 1", ltAt("18:00"))
	second := e.lesson(g.ID, "Linux 2", ltAt("18:00").Add(24*time.Hour))
	view := func(id int64) LessonViewDTO {
		t.Helper()
		var v LessonViewDTO
		e.ok(e.do("GET", fmt.Sprintf("/api/lessons/%d/attendance", id), nil), http.StatusOK, &v)
		return v
	}

	z.send(z.started(ltAt("18:00")), z.joined("Саша", "sasha", ltAt("18:01")))
	sasha := ltPart(t, view(first.ID), "Саша")
	var v LessonViewDTO
	e.ok(e.do("POST", fmt.Sprintf("/api/lessons/%d/participants/%d/map", first.ID, sasha.ID),
		map[string]any{"student_id": alex, "remember_alias": true}), http.StatusOK, &v)
	if p := ltPart(t, v, "Саша"); p.Resolution != model.ResolutionManual {
		t.Fatalf("after map: %+v", p)
	}
	// An alias added on the group page stays: only the name of the mapping goes.
	e.ok(e.do("POST", fmt.Sprintf("/api/students/%d/aliases", alex), map[string]any{"alias": "Alex"}), http.StatusCreated, nil)

	// Next day "Саша" is matched automatically through the remembered name.
	z2 := &ltZoom{e: e, p: z.p, uuid: "sess-2=="}
	day2 := ltAt("18:00").Add(24 * time.Hour)
	z2.send(z2.started(day2), z2.joined("Саша", "sasha2", day2.Add(time.Minute)))
	if p := ltPart(t, view(second.ID), "Саша"); p.Resolution != model.ResolutionMatched || p.StudentID == nil || *p.StudentID != alex {
		t.Fatalf("second lesson before unmap: %+v", p)
	}

	e.ok(e.do("DELETE", fmt.Sprintf("/api/lessons/%d/participants/%d/map", first.ID, sasha.ID), nil), http.StatusOK, &v)
	if p := ltPart(t, v, "Саша"); p.Resolution != model.ResolutionUnidentified || p.Manual {
		t.Fatalf("after unmap: %+v", p)
	}
	st, err := e.st.GetStudent(e.ctx, alex)
	if err != nil || len(st.Aliases) != 1 || st.Aliases[0].Alias != "Alex" {
		t.Fatalf("aliases after unmap: %+v %v", st, err)
	}
	var unmap, aliasDel *model.AuditEntry
	for _, a := range e.audit(first.ID) {
		switch a.Action {
		case model.AuditParticipantUnmap:
			unmap = &a
		case model.AuditAliasDelete:
			aliasDel = &a
		}
	}
	if unmap == nil || !strings.Contains(unmap.NewValue, "запомненное имя удалено") {
		t.Fatalf("unmap audit = %+v", unmap)
	}
	if aliasDel == nil || !strings.Contains(aliasDel.PreviousValue, "«Саша»") || aliasDel.GroupID == nil || *aliasDel.GroupID != g.ID {
		t.Fatalf("alias audit = %+v", aliasDel)
	}

	// The other lesson is refreshed in the background.
	for deadline := time.Now().Add(5 * time.Second); ; {
		ps, err := e.st.ListParticipants(e.ctx, second.ID)
		if err == nil && len(ps) == 1 && ps[0].Resolution == model.ResolutionUnidentified {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("second lesson after unmap: %+v %v", ps, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := e.srv.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Unmapping a decision that remembered nothing leaves aliases alone.
	e2, z3 := newLTRealEnv(t)
	g2 := e2.group("DevOps-4")
	bob := e2.student(g2.ID, "Борисов Борис", "").ID
	e2.clock.Set(ltAt("17:00"))
	l := e2.lesson(g2.ID, "Linux 1", ltAt("18:00"))
	e2.ok(e2.do("POST", fmt.Sprintf("/api/students/%d/aliases", bob), map[string]any{"alias": "Боря"}), http.StatusCreated, nil)
	z3.send(z3.started(ltAt("18:00")), z3.joined("Боря", "b", ltAt("18:01")))
	e2.ok(e2.do("GET", fmt.Sprintf("/api/lessons/%d/attendance", l.ID), nil), http.StatusOK, &v)
	borya := ltPart(t, v, "Боря")
	e2.ok(e2.do("POST", fmt.Sprintf("/api/lessons/%d/participants/%d/map", l.ID, borya.ID),
		map[string]any{"action": "ignore"}), http.StatusOK, &v)
	e2.ok(e2.do("DELETE", fmt.Sprintf("/api/lessons/%d/participants/%d/map", l.ID, borya.ID), nil), http.StatusOK, &v)
	if p := ltPart(t, v, "Боря"); p.Resolution != model.ResolutionMatched {
		t.Fatalf("after unmapping an ignore decision: %+v", p)
	}
	if st, _ := e2.st.GetStudent(e2.ctx, bob); len(st.Aliases) != 1 {
		t.Fatalf("alias of an ignore decision must stay: %+v", st.Aliases)
	}
}
