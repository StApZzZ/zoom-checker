package importer

import (
	"reflect"
	"strings"
	"testing"
)

// group: two active students (one with an alias) and two archived ones (one
// with an alias).
func groupWithArchive() []ExistingName {
	return []ExistingName{
		{StudentID: 1, ExternalID: "A1", FullName: "Иванов Иван"},
		{StudentID: 2, FullName: "Петров Пётр"},
		{StudentID: 2, FullName: "Петя", IsAlias: true},
		{StudentID: 3, ExternalID: "A3", FullName: "Сидоров Алексей", Archived: true},
		{StudentID: 4, FullName: "Орлова Ольга", Archived: true},
		{StudentID: 4, FullName: "Оля", IsAlias: true, Archived: true},
	}
}

func TestValidate_RestoreArchived(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mode    Mode
		rows    []Row
		restore []int64 // StudentID per row, 0 = not a restore
		msg     []string
		counts  Counts
	}{
		{
			name:    "by name",
			mode:    ModeAdd,
			rows:    []Row{r(1, "", "сидоров  алексей"), r(2, "", "Новикова Анна")},
			restore: []int64{3, 0},
			msg:     []string{"Был в архиве группы", ""},
			counts:  Counts{New: 2, Restore: 1},
		},
		{
			name:    "by external ID, with a new name",
			mode:    ModeAdd,
			rows:    []Row{r(1, "A3", "Сидоров Алексей Петрович")},
			restore: []int64{3},
			msg:     []string{"Имя будет изменено: «Сидоров Алексей» → «Сидоров Алексей Петрович»."},
			counts:  Counts{New: 1, Restore: 1},
		},
		{
			name:    "an ID is added on restore",
			mode:    ModeAdd,
			rows:    []Row{r(1, "Z1", "Орлова Ольга")},
			restore: []int64{4},
			msg:     []string{"Будет добавлен ID «Z1»."},
			counts:  Counts{New: 1, Restore: 1},
		},
		{
			name:    "same name with another ID is a different person",
			mode:    ModeAdd,
			rows:    []Row{r(1, "Z9", "Сидоров Алексей")},
			restore: []int64{0},
			msg:     []string{""},
			counts:  Counts{New: 1},
		},
		{
			name:    "an archived alias does not block a name",
			mode:    ModeAdd,
			rows:    []Row{r(1, "", "Оля")},
			restore: []int64{0},
			msg:     []string{""},
			counts:  Counts{New: 1},
		},
		{
			name:    "its alias is taken by a new row: created anew",
			mode:    ModeAdd,
			rows:    []Row{r(1, "", "Орлова Ольга"), r(2, "", "Оля")},
			restore: []int64{0, 0},
			msg:     []string{"дополнительное имя «Оля» уже занято", ""},
			counts:  Counts{New: 2},
		},
		{
			name:    "renamed to its own alias",
			mode:    ModeAdd,
			rows:    []Row{r(1, "", "Оля")},
			restore: []int64{0}, // found by name only — "Оля" is not its name
			msg:     []string{""},
			counts:  Counts{New: 1},
		},
		{
			name:    "replace mode: restore and archive in one go",
			mode:    ModeReplace,
			rows:    []Row{r(1, "", "Иванов Иван"), r(2, "", "Орлова Ольга")},
			restore: []int64{0, 4},
			msg:     []string{MsgExisting, "Был в архиве группы"},
			counts:  Counts{New: 1, Existing: 1, Archive: 1, Restore: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := Validate(tt.rows, groupWithArchive(), tt.mode, NameAsIs)
			if p.Counts != tt.counts {
				t.Errorf("counts = %+v, want %+v", p.Counts, tt.counts)
			}
			for i, want := range tt.restore {
				got := p.Rows[i]
				if want == 0 {
					if got.Restore {
						t.Errorf("row %d: unexpected restore %+v", i, got)
					}
				} else if !got.Restore || got.Status != StatusNew || got.StudentID == nil || *got.StudentID != want {
					t.Errorf("row %d = %+v, want restore of %d", i, got, want)
				}
				if tt.msg[i] == "" && got.Status == StatusNew && got.Message != "" {
					t.Errorf("row %d: unexpected message %q", i, got.Message)
				}
				if !strings.Contains(got.Message, tt.msg[i]) {
					t.Errorf("row %d: message %q does not contain %q", i, got.Message, tt.msg[i])
				}
			}
			for _, a := range p.Archive {
				if a.StudentID == 3 || a.StudentID == 4 {
					t.Errorf("an archived student is listed for archiving: %+v", a)
				}
			}
		})
	}
}

// An archived student's alias that an active student would keep blocks the
// restore, and two rows never restore the same archived student.
func TestValidate_RestoreClashesWithActiveNames(t *testing.T) {
	t.Parallel()
	existing := append(groupWithArchive(), ExistingName{StudentID: 5, FullName: "Петя", IsAlias: true, Archived: true},
		ExistingName{StudentID: 5, FullName: "Кузнецов Пётр", Archived: true})
	p := Validate([]Row{r(1, "", "Кузнецов Пётр")}, existing, ModeAdd, NameAsIs)
	if p.Rows[0].Restore || !strings.Contains(p.Rows[0].Message, "«Петя» уже занято") {
		t.Errorf("row = %+v", p.Rows[0])
	}
	// In replace mode Петров Пётр (owner of the active alias «Петя») is archived,
	// so the alias is free again and the restore goes through.
	p = Validate([]Row{r(1, "", "Кузнецов Пётр")}, existing, ModeReplace, NameAsIs)
	if !p.Rows[0].Restore || *p.Rows[0].StudentID != 5 {
		t.Errorf("replace: row = %+v", p.Rows[0])
	}

	p = Validate([]Row{r(1, "A3", "Сидоров Алексей"), r(2, "", "Новый Студент"), r(3, "", "Сидоров Алексей")}, groupWithArchive(), ModeAdd, NameAsIs)
	if p.Counts.Duplicate != 2 || p.Counts.Restore != 0 {
		t.Errorf("duplicates must not restore: %+v", p.Counts)
	}
}

// The newest archived record of a name is restored.
func TestValidate_RestoreNewestArchived(t *testing.T) {
	t.Parallel()
	existing := []ExistingName{
		{StudentID: 7, FullName: "Иванов Иван", Archived: true},
		{StudentID: 9, FullName: "Иванов Иван", Archived: true},
		{StudentID: 8, FullName: "иванов иван", Archived: true},
	}
	p := Validate([]Row{r(1, "", "Иванов Иван")}, existing, ModeAdd, NameAsIs)
	if !p.Rows[0].Restore || *p.Rows[0].StudentID != 9 {
		t.Errorf("row = %+v", p.Rows[0])
	}
}

// Validating the rows of an OK preview again (as the commit does, with the
// already formatted names) gives the same verdict for every row.
func TestValidate_CommitMatchesPreview(t *testing.T) {
	t.Parallel()
	existing := []ExistingName{
		{StudentID: 1, FullName: "Иванов Иван Иванович"},
		{StudentID: 2, FullName: "Петрова Анна"},
		{StudentID: 3, ExternalID: "S3", FullName: "Сидоров Алексей Игоревич"},
		{StudentID: 4, FullName: "Кузнецов Олег Олегович"},
		{StudentID: 5, FullName: "Орлов Олег Петрович", Archived: true},
		{StudentID: 6, FullName: "Белова Вера", Archived: true},
		{StudentID: 7, FullName: "Fedorov Fedor", IsAlias: true},
		{StudentID: 7, FullName: "Фёдоров Фёдор Фёдорович"},
	}
	files := []struct {
		name   string
		format NameFormat
		mode   Mode
		rows   []Row
	}{
		{"last_first, replace", NameLastFirst, ModeReplace, []Row{
			r(1, "", "Иванов Иван Иванович"), // exact long name → renamed
			r(2, "", "Петрова Анна Сергеевна"),
			r(3, "S3", "Сидоров Алексей Игоревич"),
			r(4, "", "Кузнецов Олег"), // written short in the file
			r(5, "", "Орлов Олег Петрович"),
			r(6, "", "Белова Вера Павловна"),
			r(7, "", "Новиков Николай Николаевич"),
		}},
		{"as_is, add", NameAsIs, ModeAdd, []Row{
			r(1, "", "Иванов Иван Иванович"),
			r(2, "X2", "Петрова Анна"),
			r(3, "", "Белова Вера"),
			r(4, "", "Новиков Николай"),
		}},
	}
	for _, f := range files {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			preview := Validate(f.rows, existing, f.mode, f.format)
			if !preview.OK {
				t.Fatalf("preview not OK: %+v", preview.Rows)
			}
			commitRows := make([]Row, len(preview.Rows))
			for i, pr := range preview.Rows {
				commitRows[i] = Row{Line: i + 1, ExternalID: pr.ExternalID, FullName: pr.FullName}
			}
			commit := Validate(commitRows, existing, f.mode, f.format)
			if !commit.OK {
				t.Fatalf("commit not OK: %+v", commit.Rows)
			}
			type verdict struct {
				Status   RowStatus
				Student  int64
				Restore  bool
				FullName string
			}
			view := func(p Preview) []verdict {
				var out []verdict
				for _, r := range p.Rows {
					v := verdict{Status: r.Status, Restore: r.Restore, FullName: r.FullName}
					if r.StudentID != nil {
						v.Student = *r.StudentID
					}
					out = append(out, v)
				}
				return out
			}
			if !reflect.DeepEqual(view(preview), view(commit)) {
				t.Errorf("commit differs from preview:\npreview %+v\ncommit  %+v", view(preview), view(commit))
			}
			if !reflect.DeepEqual(preview.Archive, commit.Archive) {
				t.Errorf("archive differs: %+v vs %+v", preview.Archive, commit.Archive)
			}
		})
	}

	// The last_first preview: every long name found its student, Белова Вера
	// is restored by the short name and Орлов (stored long, archived) is new.
	p := Validate(files[0].rows, existing, ModeReplace, NameLastFirst)
	wantIDs := []int64{1, 2, 3, 4, 0, 6, 0}
	for i, want := range wantIDs {
		got := p.Rows[i].StudentID
		if (want == 0) != (got == nil) || (got != nil && *got != want) {
			t.Errorf("row %d: student %v, want %d (%+v)", i, got, want, p.Rows[i])
		}
	}
	if len(p.Archive) != 1 || p.Archive[0].StudentID != 7 {
		t.Errorf("archive = %+v", p.Archive)
	}
}

func TestValidate_LastFirstShortForms(t *testing.T) {
	t.Parallel()
	existing := []ExistingName{
		{StudentID: 1, FullName: "Иванов Иван Иванович"},
		{StudentID: 2, FullName: "Иванов Иван Петрович"},
		{StudentID: 3, FullName: "Петров Пётр Петрович"},
	}
	// The short form fits two students: a commit could not tell them apart.
	p := Validate([]Row{r(1, "", "Иванов Иван Иванович")}, existing, ModeAdd, NameLastFirst)
	if got := p.Rows[0]; got.Status != StatusError || !strings.Contains(got.Message, "подходит сразу нескольким студентам группы: «Иванов Иван Иванович», «Иванов Иван Петрович»") {
		t.Errorf("ambiguous short form: %+v", got)
	}
	// A different patronymic is a different person.
	p = Validate([]Row{r(1, "", "Петров Пётр Сергеевич")}, existing, ModeAdd, NameLastFirst)
	if got := p.Rows[0]; got.Status != StatusError || !strings.Contains(got.Message, "совпадёт со студентом группы «Петров Пётр Петрович»") {
		t.Errorf("other patronymic: %+v", got)
	}
	// Written without a patronymic: the same person, renamed to the short form.
	p = Validate([]Row{r(1, "", "Петров Пётр")}, existing, ModeAdd, NameLastFirst)
	if got := p.Rows[0]; got.Status != StatusExisting || *got.StudentID != 3 || !got.Rename {
		t.Errorf("short in the file: %+v", got)
	}
	// As is, a short name is a new student.
	p = Validate([]Row{r(1, "", "Петров Пётр")}, existing, ModeAdd, NameAsIs)
	if got := p.Rows[0]; got.Status != StatusNew || got.StudentID != nil {
		t.Errorf("as_is: %+v", got)
	}
}
