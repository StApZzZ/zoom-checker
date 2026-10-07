package httpapi

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"zoom-checker/internal/importer"
	"zoom-checker/internal/model"
)

func TestGroupsRequireLogin(t *testing.T) {
	t.Parallel()
	e := newGTEnv(t)
	for _, p := range []struct{ method, path string }{
		{http.MethodGet, "/api/groups"},
		{http.MethodPost, "/api/groups"},
		{http.MethodGet, "/api/groups/1"},
		{http.MethodPatch, "/api/students/1"},
		{http.MethodPost, "/api/groups/1/import/preview"},
		{http.MethodGet, "/api/groups/1/report/export"},
	} {
		r := e.raw(p.method, p.path, "application/json", strings.NewReader("{}"), false)
		if r.status != http.StatusUnauthorized || r.apiErr(t).Code != CodeUnauthorized {
			t.Errorf("%s %s: %d %s", p.method, p.path, r.status, r.body)
		}
	}
}

func TestGroupsCreateListGet(t *testing.T) {
	t.Parallel()
	e := newGTEnv(t)

	r := e.do(http.MethodPost, "/api/groups", map[string]any{"name": "  DevOps-2026   01 ", "description": " Вечерняя группа "})
	e.want(r, http.StatusCreated)
	var g GroupSummaryDTO
	r.decode(t, &g)
	if g.Name != "DevOps-2026 01" || g.Description != "Вечерняя группа" || g.Status != model.StatusActive ||
		g.DefaultDurationMinutes != 90 || g.StudentCount != 0 || g.ID == 0 {
		t.Errorf("created %+v", g)
	}
	for _, key := range []string{`"last_lesson_at":null`, `"next_lesson_at":null`, `"student_count":0`, `"created_at":"`} {
		if !strings.Contains(string(r.body), key) {
			t.Errorf("body %s lacks %s", r.body, key)
		}
	}

	// Duplicate names (after normalization) are a conflict.
	r = e.do(http.MethodPost, "/api/groups", map[string]any{"name": "devops-2026 01"})
	e.want(r, http.StatusConflict)
	if er := r.apiErr(t); er.Code != CodeConflict || er.Message != "Группа с таким названием уже есть" {
		t.Errorf("conflict %+v", er)
	}

	for _, tt := range []struct {
		name string
		body map[string]any
	}{
		{"empty name", map[string]any{"name": "   "}},
		{"name too long", map[string]any{"name": strings.Repeat("я", 101)}},
		{"invisible name", map[string]any{"name": "​"}},
		{"duration too short", map[string]any{"name": "A", "default_duration_minutes": 14}},
		{"duration too long", map[string]any{"name": "A", "default_duration_minutes": 601}},
		{"description too long", map[string]any{"name": "A", "description": strings.Repeat("x", 2001)}},
	} {
		r := e.do(http.MethodPost, "/api/groups", tt.body)
		if r.status != http.StatusUnprocessableEntity || r.apiErr(t).Code != CodeValidation {
			t.Errorf("%s: %d %s", tt.name, r.status, r.body)
		}
	}
	r = e.do(http.MethodPost, "/api/groups", map[string]any{"name": strings.Repeat("я", 100), "default_duration_minutes": 15})
	e.want(r, http.StatusCreated)
	var long GroupSummaryDTO
	r.decode(t, &long)
	if long.DefaultDurationMinutes != 15 {
		t.Errorf("duration %d", long.DefaultDurationMinutes)
	}

	// Archive the long one: the default list shows active groups only.
	e.want(e.do(http.MethodPatch, "/api/groups/"+grpID(long.ID), map[string]any{"status": "archived"}), http.StatusOK)
	list := func(q string) []string {
		r := e.do(http.MethodGet, "/api/groups"+q, nil)
		e.want(r, http.StatusOK)
		var gs []GroupSummaryDTO
		r.decode(t, &gs)
		var out []string
		for _, g := range gs {
			out = append(out, g.Name[:min(len(g.Name), 12)])
		}
		return out
	}
	if got := list(""); !slices.Equal(got, []string{"DevOps-2026 "}) {
		t.Errorf("default list %q", got)
	}
	if got := list("?status=active"); len(got) != 1 {
		t.Errorf("active %q", got)
	}
	if got := list("?status=archived"); len(got) != 1 || got[0] == "DevOps-2026 " {
		t.Errorf("archived %q", got)
	}
	if got := list("?status=all"); len(got) != 2 {
		t.Errorf("all %q", got)
	}
	e.want(e.do(http.MethodGet, "/api/groups?status=deleted", nil), http.StatusUnprocessableEntity)

	// An empty list is [] (never null).
	e2 := newGTEnv(t)
	r = e2.do(http.MethodGet, "/api/groups", nil)
	e2.want(r, http.StatusOK)
	if strings.TrimSpace(string(r.body)) != "[]" {
		t.Errorf("empty list body %s", r.body)
	}

	// Detail.
	d := e.detail(g.ID)
	if d.ID != g.ID || d.Name != g.Name || d.Students == nil || len(d.Students) != 0 {
		t.Errorf("detail %+v", d)
	}
	if !strings.Contains(string(e.do(http.MethodGet, "/api/groups/"+grpID(g.ID), nil).body), `"students":[]`) {
		t.Error("students must be [] for an empty group")
	}
	e.want(e.do(http.MethodGet, "/api/groups/999", nil), http.StatusNotFound)
	e.want(e.do(http.MethodGet, "/api/groups/abc", nil), http.StatusNotFound)
}

func TestGroupsUpdate(t *testing.T) {
	t.Parallel()
	e := newGTEnv(t)
	g := e.group("Linux-1")
	other := e.group("Linux-2")
	l := e.lesson(g.ID, "Занятие 1", gtNow.Add(time.Hour), model.LessonPlanned)
	ch, cancel := e.broker.Subscribe("lesson:" + grpID(l.ID))
	defer cancel()

	r := e.do(http.MethodPatch, "/api/groups/"+grpID(g.ID), map[string]any{
		"name": "Linux-1 (вечер)", "description": "Пн/Ср", "default_duration_minutes": 120,
	})
	e.want(r, http.StatusOK)
	var got GroupSummaryDTO
	r.decode(t, &got)
	if got.Name != "Linux-1 (вечер)" || got.Description != "Пн/Ср" || got.DefaultDurationMinutes != 120 || got.Status != model.StatusActive {
		t.Errorf("updated %+v", got)
	}
	if got.NextLessonAt == nil || !got.NextLessonAt.Equal(l.PlannedStartAt) {
		t.Errorf("next lesson %v", got.NextLessonAt)
	}
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Error("renaming a group must poke its lesson screens")
	}

	// Conflicts and validation.
	r = e.do(http.MethodPatch, "/api/groups/"+grpID(g.ID), map[string]any{"name": " linux-2 "})
	e.want(r, http.StatusConflict)
	if r.apiErr(t).Message != "Группа с таким названием уже есть" {
		t.Errorf("conflict %s", r.body)
	}
	for _, body := range []map[string]any{
		{"status": "deleted"}, {"name": ""}, {"default_duration_minutes": 0}, {"default_duration_minutes": 700},
	} {
		if r := e.do(http.MethodPatch, "/api/groups/"+grpID(g.ID), body); r.status != http.StatusUnprocessableEntity {
			t.Errorf("%v: %d %s", body, r.status, r.body)
		}
	}
	e.want(e.do(http.MethodPatch, "/api/groups/999", map[string]any{"name": "X"}), http.StatusNotFound)

	// No change: no audit entry.
	e.want(e.do(http.MethodPatch, "/api/groups/"+grpID(other.ID), map[string]any{"name": "Linux-2"}), http.StatusOK)

	a := e.audit(g.ID)
	if acts := gtActions(a); !slices.Equal(acts, []string{model.AuditGroupCreate, model.AuditGroupUpdate}) {
		t.Fatalf("audit %q", acts)
	}
	if a[1].UserName != "Мария Петровна" || !strings.Contains(a[1].PreviousValue, `"name":"Linux-1"`) ||
		!strings.Contains(a[1].NewValue, `"default_duration_minutes":120`) || strings.Contains(a[1].NewValue, "status") {
		t.Errorf("update audit %+v", a[1])
	}
	if acts := gtActions(e.audit(other.ID)); len(acts) != 1 {
		t.Errorf("unchanged group audit %q", acts)
	}
}

func TestGroupsStudents(t *testing.T) {
	t.Parallel()
	e := newGTEnv(t)
	g := e.group("Python-1")
	planned := e.lesson(g.ID, "Будущее", gtNow.Add(24*time.Hour), model.LessonPlanned)
	active := e.lesson(g.ID, "Идёт", gtNow.Add(-30*time.Minute), model.LessonActive)
	e.lesson(g.ID, "Прошло", gtNow.Add(-7*24*time.Hour), model.LessonFinished)
	otherGroup := e.group("Python-2")
	e.lesson(otherGroup.ID, "Чужое", gtNow.Add(time.Hour), model.LessonPlanned)
	wantRebuilt := []int64{planned.ID, active.ID}

	r := e.do(http.MethodPost, "/api/groups/"+grpID(g.ID)+"/students", map[string]any{"full_name": "  Иванов   Иван ", "external_id": " 007 "})
	e.want(r, http.StatusCreated)
	var ivan StudentDTO
	r.decode(t, &ivan)
	if ivan.FullName != "Иванов Иван" || ivan.NormalizedName != "иванов иван" || ivan.ExternalID != "007" ||
		ivan.GroupID != g.ID || ivan.Status != model.StatusActive || ivan.Aliases == nil {
		t.Errorf("student %+v", ivan)
	}
	if !strings.Contains(string(r.body), `"aliases":[]`) {
		t.Errorf("aliases must be []: %s", r.body)
	}
	if got := e.proc.ids(); !slices.Equal(got, wantRebuilt) {
		t.Errorf("rebuilt %v, want the unfinished lessons %v", got, wantRebuilt)
	}

	// TZ §8: a second «Иванов Иван» is refused with the standard text.
	r = e.do(http.MethodPost, "/api/groups/"+grpID(g.ID)+"/students", map[string]any{"full_name": "ИВАНОВ иван"})
	e.want(r, http.StatusConflict)
	if er := r.apiErr(t); er.Code != CodeConflict || !strings.Contains(er.Message, importer.MsgDuplicate) ||
		!strings.Contains(er.Message, "«Иванов Иван»") || er.Details["student_id"] != float64(ivan.ID) {
		t.Errorf("duplicate %+v", er)
	}
	for _, body := range []map[string]any{
		{"full_name": ""}, {"full_name": " \t "}, {"full_name": strings.Repeat("я", 201)},
		{"full_name": "Петров Пётр", "external_id": strings.Repeat("1", 101)},
	} {
		if r := e.do(http.MethodPost, "/api/groups/"+grpID(g.ID)+"/students", body); r.status != http.StatusUnprocessableEntity {
			t.Errorf("%v: %d %s", body, r.status, r.body)
		}
	}
	e.want(e.do(http.MethodPost, "/api/groups/999/students", map[string]any{"full_name": "Петров Пётр"}), http.StatusNotFound)
	petr := e.student(g.ID, strings.Repeat("П", 200), "")

	// Rename, change ID, archive and restore.
	e.proc.reset()
	r = e.do(http.MethodPatch, "/api/students/"+grpID(petr.ID), map[string]any{"full_name": "Петров Пётр", "external_id": "P1"})
	e.want(r, http.StatusOK)
	r.decode(t, &petr)
	if petr.FullName != "Петров Пётр" || petr.ExternalID != "P1" || petr.NormalizedName != "петров петр" {
		t.Errorf("updated %+v", petr)
	}
	if got := e.proc.ids(); !slices.Equal(got, wantRebuilt) {
		t.Errorf("rebuilt %v", got)
	}
	r = e.do(http.MethodPatch, "/api/students/"+grpID(petr.ID), map[string]any{"status": "archived"})
	e.want(r, http.StatusOK)
	// The name of an archived student is free again.
	twin := e.student(g.ID, "Петров Пётр", "")
	r = e.do(http.MethodPatch, "/api/students/"+grpID(petr.ID), map[string]any{"status": "active"})
	e.want(r, http.StatusConflict)
	if !strings.Contains(r.apiErr(t).Message, importer.MsgDuplicate) {
		t.Errorf("restore conflict %s", r.body)
	}
	e.want(e.do(http.MethodPatch, "/api/students/"+grpID(twin.ID), map[string]any{"full_name": "Иванов Иван"}), http.StatusConflict)
	for _, body := range []map[string]any{{"status": "gone"}, {"full_name": ""}, {"external_id": strings.Repeat("x", 101)}} {
		if r := e.do(http.MethodPatch, "/api/students/"+grpID(twin.ID), body); r.status != http.StatusUnprocessableEntity {
			t.Errorf("%v: %d %s", body, r.status, r.body)
		}
	}
	e.want(e.do(http.MethodPatch, "/api/students/999", map[string]any{"full_name": "X"}), http.StatusNotFound)

	// The group detail lists everybody, archived students included.
	d := e.detail(g.ID)
	if d.StudentCount != 2 || len(d.Students) != 3 {
		t.Fatalf("detail %+v", d)
	}
	statuses := map[int64]model.Status{}
	for _, s := range d.Students {
		statuses[s.ID] = s.Status
	}
	if statuses[petr.ID] != model.StatusArchived || statuses[twin.ID] != model.StatusActive || statuses[ivan.ID] != model.StatusActive {
		t.Errorf("statuses %v", statuses)
	}

	acts := gtActions(e.audit(g.ID))
	want := []string{model.AuditGroupCreate, model.AuditStudentCreate, model.AuditStudentCreate,
		model.AuditStudentUpdate, model.AuditStudentUpdate, model.AuditStudentCreate}
	if !slices.Equal(acts, want) {
		t.Errorf("audit %q, want %q", acts, want)
	}
}

func TestGroupsAliases(t *testing.T) {
	t.Parallel()
	e := newGTEnv(t)
	g := e.group("Go-1")
	active := e.lesson(g.ID, "Идёт", gtNow.Add(-10*time.Minute), model.LessonActive)
	e.lesson(g.ID, "Прошло", gtNow.Add(-48*time.Hour), model.LessonFinished)
	ivan := e.student(g.ID, "Иванов Иван", "")
	petr := e.student(g.ID, "Петров Пётр", "")
	e.proc.reset()

	path := "/api/students/" + grpID(ivan.ID) + "/aliases"
	r := e.do(http.MethodPost, path, map[string]any{"alias": "  Ivan   iPhone "})
	e.want(r, http.StatusCreated)
	var a AliasDTO
	r.decode(t, &a)
	if a.ID == 0 || a.Alias != "Ivan iPhone" {
		t.Errorf("alias %+v", a)
	}
	if got := e.proc.ids(); !slices.Equal(got, []int64{active.ID}) {
		t.Errorf("rebuilt %v", got)
	}

	tests := []struct {
		name   string
		path   string
		alias  string
		status int
		msg    string
	}{
		{"another student's name", path, "петров  пётр", http.StatusConflict, "так зовут студента «Петров Пётр»"},
		{"another student's alias", "/api/students/" + grpID(petr.ID) + "/aliases", "IVAN IPHONE", http.StatusConflict, "дополнительное имя студента «Иванов Иван»"},
		{"own name", path, "Иванов Иван", http.StatusUnprocessableEntity, "основное имя"},
		{"own alias again", path, "ivan iphone", http.StatusConflict, "уже есть дополнительное имя «Ivan iPhone»"},
		{"empty", path, "  ", http.StatusUnprocessableEntity, "Введите дополнительное имя"},
		{"too long", path, strings.Repeat("a", 201), http.StatusUnprocessableEntity, "слишком длинное"},
		{"unknown student", "/api/students/999/aliases", "X", http.StatusNotFound, "Студент не найден"},
	}
	for _, tt := range tests {
		r := e.do(http.MethodPost, tt.path, map[string]any{"alias": tt.alias})
		if r.status != tt.status || !strings.Contains(r.apiErr(t).Message, tt.msg) {
			t.Errorf("%s: %d %s", tt.name, r.status, r.body)
		}
	}
	// A student cannot take another student's alias as the name either.
	r = e.do(http.MethodPatch, "/api/students/"+grpID(petr.ID), map[string]any{"full_name": "Ivan iPhone"})
	e.want(r, http.StatusConflict)
	if er := r.apiErr(t); !strings.Contains(er.Message, "дополнительное имя студента «Иванов Иван»") || er.Details["is_alias"] != true {
		t.Errorf("rename onto an alias %+v", er)
	}

	// Renaming a student to its own alias turns the alias into the name.
	r = e.do(http.MethodPatch, "/api/students/"+grpID(ivan.ID), map[string]any{"full_name": "ivan IPHONE"})
	e.want(r, http.StatusOK)
	var renamed StudentDTO
	r.decode(t, &renamed)
	if renamed.FullName != "ivan IPHONE" || len(renamed.Aliases) != 0 {
		t.Errorf("renamed %+v", renamed)
	}

	// Delete.
	r = e.do(http.MethodPost, path, map[string]any{"alias": "Ваня"})
	e.want(r, http.StatusCreated)
	r.decode(t, &a)
	e.proc.reset()
	r = e.do(http.MethodDelete, path+"/"+grpID(a.ID), nil)
	e.want(r, http.StatusNoContent)
	if len(r.body) != 0 {
		t.Errorf("204 with a body %q", r.body)
	}
	if got := e.proc.ids(); !slices.Equal(got, []int64{active.ID}) {
		t.Errorf("rebuilt %v", got)
	}
	e.want(e.do(http.MethodDelete, path+"/"+grpID(a.ID), nil), http.StatusNotFound)
	e.want(e.do(http.MethodDelete, "/api/students/"+grpID(petr.ID)+"/aliases/"+grpID(a.ID), nil), http.StatusNotFound)
	e.want(e.do(http.MethodDelete, "/api/students/999/aliases/1", nil), http.StatusNotFound)

	acts := gtActions(e.audit(g.ID))
	want := []string{model.AuditGroupCreate, model.AuditStudentCreate, model.AuditStudentCreate, model.AuditAliasAdd,
		model.AuditAliasDelete, model.AuditStudentUpdate, model.AuditAliasAdd, model.AuditAliasDelete}
	if !slices.Equal(acts, want) {
		t.Errorf("audit %q, want %q", acts, want)
	}
}

// Control and invisible characters never reach the database (PostgreSQL text
// cannot hold NUL); descriptions keep their line breaks.
func TestGroupsCleanInput(t *testing.T) {
	t.Parallel()
	e := newGTEnv(t)
	r := e.do(http.MethodPost, "/api/groups", map[string]any{
		"name": "Linux\x00-1​", "description": " Пн/Ср\r\n18:00\x00 \x07",
	})
	e.want(r, http.StatusCreated)
	var g GroupSummaryDTO
	r.decode(t, &g)
	if g.Name != "Linux-1" || g.Description != "Пн/Ср\n18:00" {
		t.Errorf("group %q / %q", g.Name, g.Description)
	}
	st := e.student(g.ID, "Иванов\x00 ​Иван\x1f", "\x00 7\t")
	if st.FullName != "Иванов Иван" || st.NormalizedName != "иванов иван" || st.ExternalID != "7" {
		t.Errorf("student %+v", st)
	}
	r = e.do(http.MethodPost, "/api/students/"+grpID(st.ID)+"/aliases", map[string]any{"alias": "Ваня\x00"})
	e.want(r, http.StatusCreated)
	var a AliasDTO
	r.decode(t, &a)
	if a.Alias != "Ваня" {
		t.Errorf("alias %q", a.Alias)
	}
	// Nothing but control characters is an empty name.
	e.want(e.do(http.MethodPost, "/api/groups/"+grpID(g.ID)+"/students", map[string]any{"full_name": "\x00\x01"}), http.StatusUnprocessableEntity)

	// The import cleans pasted text the same way.
	p := gtPreview(t, e.do(http.MethodPost, gtPreviewPath(g.ID, ""), map[string]any{"text": "Петров\x00 Пётр\nИванов Иван\x00\n"}))
	if p.Rows[0].FullName != "Петров Пётр" || p.Rows[1].Status != "existing" {
		t.Errorf("preview %+v", p.Rows)
	}
}

// Bringing a student back from the archive can clash with a name taken in the
// meantime; the message says what happened in plain words.
func TestGroupsRestoreConflictMessages(t *testing.T) {
	t.Parallel()
	e := newGTEnv(t)
	g := e.group("Restore-2")
	petr := e.student(g.ID, "Петров Пётр", "")
	anna := e.student(g.ID, "Петрова Анна", "")
	for _, alias := range []string{"Аня", "Анечка"} {
		e.want(e.do(http.MethodPost, "/api/students/"+grpID(anna.ID)+"/aliases", map[string]any{"alias": alias}), http.StatusCreated)
	}
	for _, id := range []int64{petr.ID, anna.ID} {
		e.want(e.do(http.MethodPatch, "/api/students/"+grpID(id), map[string]any{"status": "archived"}), http.StatusOK)
	}
	// Names of archived students and their aliases are free again.
	twin := e.student(g.ID, "Петров  Пётр", "")
	other := e.student(g.ID, "аня", "")

	tests := []struct {
		name string
		id   int64
		want []string
	}{
		{"name taken", petr.ID, []string{"Не получилось вернуть «Петров Пётр» из архива", "так зовут студента «Петров Пётр»", importer.MsgDuplicate}},
		{"alias taken", anna.ID, []string{"Не получилось вернуть «Петрова Анна» из архива", "дополнительное имя «Аня» уже занято",
			"так зовут студента «аня»", "Удалите это дополнительное имя в карточке «Петрова Анна»"}},
	}
	for _, tt := range tests {
		r := e.do(http.MethodPatch, "/api/students/"+grpID(tt.id), map[string]any{"status": "active"})
		e.want(r, http.StatusConflict)
		er := r.apiErr(t)
		for _, w := range tt.want {
			if !strings.Contains(er.Message, w) {
				t.Errorf("%s: %q lacks %q", tt.name, er.Message, w)
			}
		}
	}
	// A rename clash keeps the usual wording.
	r := e.do(http.MethodPatch, "/api/students/"+grpID(other.ID), map[string]any{"full_name": "Петров Пётр"})
	e.want(r, http.StatusConflict)
	if er := r.apiErr(t); !strings.HasPrefix(er.Message, "Это имя уже есть в группе: так зовут студента «Петров Пётр».") {
		t.Errorf("rename %q", er.Message)
	}
	// Aliases stay unique even when their owner is archived; the message
	// says so, since that student is not in the list on the screen.
	r = e.do(http.MethodPost, "/api/students/"+grpID(twin.ID)+"/aliases", map[string]any{"alias": "АНЕЧКА"})
	e.want(r, http.StatusConflict)
	if er := r.apiErr(t); !strings.Contains(er.Message, "это дополнительное имя студента «Петрова Анна» (в архиве группы)") {
		t.Errorf("archived owner %q", er.Message)
	}
}

// Imports find students by ID first, so two active students of a group may
// not share one.
func TestGroupsExternalIDMustBeUnique(t *testing.T) {
	t.Parallel()
	e := newGTEnv(t)
	g := e.group("IDs-1")
	ivan := e.student(g.ID, "Иванов Иван", "7")
	petr := e.student(g.ID, "Петров Пётр", "8")

	r := e.do(http.MethodPost, "/api/groups/"+grpID(g.ID)+"/students", map[string]any{"full_name": "Сидоров Алексей", "external_id": " 7 "})
	e.want(r, http.StatusUnprocessableEntity)
	if er := r.apiErr(t); !strings.Contains(er.Message, "ID «7» уже записан у студента «Иванов Иван»") ||
		er.Details["field"] != "external_id" || er.Details["student_id"] != float64(ivan.ID) {
		t.Errorf("create %+v", er)
	}
	r = e.do(http.MethodPatch, "/api/students/"+grpID(petr.ID), map[string]any{"external_id": "7"})
	e.want(r, http.StatusUnprocessableEntity)
	// Keeping one's own ID, or changing only the name, is fine.
	e.want(e.do(http.MethodPatch, "/api/students/"+grpID(petr.ID), map[string]any{"external_id": "8", "full_name": "Петров Пётр А."}), http.StatusOK)
	// Archived students do not hold their ID.
	e.want(e.do(http.MethodPatch, "/api/students/"+grpID(ivan.ID), map[string]any{"status": "archived"}), http.StatusOK)
	st := e.student(g.ID, "Сидоров Алексей", "7")
	if st.ExternalID != "7" {
		t.Errorf("student %+v", st)
	}
	// Other groups do not matter.
	g2 := e.group("IDs-2")
	e.student(g2.ID, "Кузнецов Олег", "8")
}

// Roster changes do not rebuild stale planned lessons (no Zoom session ever
// came and the planned end is more than a day ago): a student added today must
// not appear as absent in last week's cancelled lesson.
func TestGroupRefreshSkipsStalePlannedLessons(t *testing.T) {
	t.Parallel()
	e := newGTEnv(t)
	g := e.group("Go-1")
	future := e.lesson(g.ID, "Будущее", gtNow.Add(24*time.Hour), model.LessonPlanned)
	recent := e.lesson(g.ID, "Только что", gtNow.Add(-3*time.Hour), model.LessonPlanned)     // ended 1.5 h ago
	edge := e.lesson(g.ID, "Почти сутки", gtNow.Add(-25*time.Hour), model.LessonPlanned)     // ended 23.5 h ago
	stale := e.lesson(g.ID, "Неделю назад", gtNow.Add(-7*24*time.Hour), model.LessonPlanned) // never held
	stuck := e.lesson(g.ID, "Без конца", gtNow.Add(-3*24*time.Hour), model.LessonActive)     // Zoom never sent meeting.ended
	e.lesson(g.ID, "Прошло", gtNow.Add(-2*24*time.Hour), model.LessonFinished)

	e.want(e.do(http.MethodPost, "/api/groups/"+grpID(g.ID)+"/students", map[string]any{"full_name": "Новиков Олег"}), http.StatusCreated)
	want := []int64{future.ID, recent.ID, edge.ID, stuck.ID}
	slices.Sort(want)
	if got := e.proc.ids(); !slices.Equal(got, want) {
		t.Fatalf("rebuilt %v, want %v (stale planned lesson %d must be skipped)", got, want, stale.ID)
	}
}
