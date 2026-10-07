package importer

import (
	"reflect"
	"strings"
	"testing"

	"zoom-checker/internal/names"
)

// roster: three active students and one alias.
func roster() []ExistingName {
	return []ExistingName{
		{StudentID: 10, ExternalID: "A1", FullName: "Иванов Иван", Normalized: "иванов иван"},
		{StudentID: 11, ExternalID: "", FullName: "Фёдоров Пётр"}, // Normalized computed by Validate
		{StudentID: 12, ExternalID: "A3", FullName: "Сидоров Алексей", Normalized: "сидоров алексей"},
		{StudentID: 11, FullName: "Петя Ф", Normalized: "петя ф", IsAlias: true},
	}
}

func ptr(v int64) *int64 { return &v }

func TestValidate_Statuses(t *testing.T) {
	t.Parallel()
	rows := []Row{
		r(2, "", "Новикова Анна"),           // new
		r(3, "", "ИВАНОВ  иван"),            // existing by name, display differs → rename
		r(4, "A3", "Сидоров Алексей"),       // existing by ext, same name
		r(5, "", "Федоров Пётр"),            // existing by name (ё→е), rename
		r(6, "", ""),                        // empty
		r(7, "", "Козлов Кирилл"),           // duplicate
		r(8, "", "козлов  кирилл"),          // duplicate
		r(9, "", "Петя Ф"),                  // alias of another student
		r(10, "", strings.Repeat("я", 201)), // too long
		r(11, "", "12345"),                  // no letters
	}
	p := Validate(rows, roster(), ModeAdd, NameAsIs)

	type want struct {
		status RowStatus
		id     *int64
		rename bool
		msg    string // substring
	}
	wants := []want{
		{StatusNew, nil, false, ""},
		{StatusExisting, ptr(10), true, "«Иванов Иван» → «ИВАНОВ иван»"},
		{StatusExisting, ptr(12), false, MsgExisting},
		{StatusExisting, ptr(11), true, "«Фёдоров Пётр» → «Федоров Пётр»"},
		{StatusError, nil, false, MsgEmptyName},
		{StatusDuplicate, nil, false, MsgDuplicate},
		{StatusDuplicate, nil, false, MsgDuplicate},
		{StatusError, nil, false, "дополнительное имя студента «Фёдоров Пётр»"},
		{StatusError, nil, false, "слишком длинное"},
		{StatusError, nil, false, "нет ни одной буквы"},
	}
	if len(p.Rows) != len(wants) {
		t.Fatalf("got %d rows, want %d", len(p.Rows), len(wants))
	}
	for i, w := range wants {
		got := p.Rows[i]
		if got.Line != rows[i].Line {
			t.Errorf("row %d: line %d", i, got.Line)
		}
		if got.Status != w.status || got.Rename != w.rename || !reflect.DeepEqual(got.StudentID, w.id) {
			t.Errorf("row %d (%q): status=%s rename=%v id=%v, want %s %v %v", i, got.FullName, got.Status, got.Rename, got.StudentID, w.status, w.rename, w.id)
		}
		if w.msg == "" && got.Message != "" {
			t.Errorf("row %d: unexpected message %q", i, got.Message)
		}
		if !strings.Contains(got.Message, w.msg) {
			t.Errorf("row %d: message %q does not contain %q", i, got.Message, w.msg)
		}
	}
	if got := p.Rows[1].NormalizedName; got != "иванов иван" {
		t.Errorf("normalized = %q", got)
	}
	if got := p.Rows[1].FullName; got != "ИВАНОВ иван" {
		t.Errorf("full name should be whitespace-cleaned with case kept, got %q", got)
	}
	wantCounts := Counts{New: 1, Existing: 3, Error: 4, Duplicate: 2, Rename: 2}
	if p.Counts != wantCounts {
		t.Errorf("counts = %+v, want %+v", p.Counts, wantCounts)
	}
	if p.OK {
		t.Error("OK must be false with errors")
	}
	if p.Archive != nil {
		t.Errorf("add mode must not archive, got %+v", p.Archive)
	}
}

func TestValidate_DuplicateMessageExact(t *testing.T) {
	t.Parallel()
	p := Validate([]Row{r(1, "", "Иванов Иван"), r(2, "", "Петров Пётр"), r(3, "", "иванов   ИВАН")}, nil, ModeAdd, NameAsIs)
	want1 := "Невозможно однозначно идентифицировать студентов по Zoom display name. Такое же имя в строке 3." +
		" Если это один и тот же студент — удалите лишнюю строку. Если это разные люди — добавьте уточнение, например «Иванов Иван А.»."
	if p.Rows[0].Status != StatusDuplicate || p.Rows[0].Message != want1 {
		t.Errorf("row 1 = %s %q", p.Rows[0].Status, p.Rows[0].Message)
	}
	if !strings.HasPrefix(p.Rows[2].Message, "Невозможно однозначно идентифицировать студентов по Zoom display name.") {
		t.Errorf("row 3 message %q", p.Rows[2].Message)
	}
	if p.Rows[1].Status != StatusNew {
		t.Errorf("row 2 = %s", p.Rows[1].Status)
	}
	if p.Counts.Duplicate != 2 || p.OK {
		t.Errorf("counts %+v OK=%v", p.Counts, p.OK)
	}
}

func TestValidate_ExternalID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mode   Mode
		row    Row
		status RowStatus
		id     *int64
		rename bool
		msg    string
	}{
		{"match by id, renamed", ModeAdd, r(1, "A1", "Иванов Иван Иванович"), StatusExisting, ptr(10), true, "Имя будет изменено"},
		{"match by name, id added", ModeAdd, r(1, "B7", "Фёдоров Пётр"), StatusExisting, ptr(11), false, "Будет добавлен ID «B7»"},
		{"match by name without id keeps the stored id", ModeAdd, r(1, "", "Иванов Иван"), StatusExisting, ptr(10), false, MsgExisting},
		// TZ §8: a namesake joining the group must not be merged into the
		// existing student.
		{"add mode: same name, other id is a namesake", ModeAdd, r(1, "Z9", "Иванов Иван"), StatusError, nil, false,
			MsgDuplicate + " В группе уже есть «Иванов Иван» с другим ID («A1»). Если это другой человек, добавьте уточнение, например «Иванов Иван А.»."},
		{"replace mode: same name, other id changes the id", ModeReplace, r(1, "Z9", "Иванов Иван"), StatusExisting, ptr(10), false, "ID будет изменён: «A1» → «Z9»"},
		{"id wins and new name clashes with another student", ModeAdd, r(1, "A3", "Иванов Иван"), StatusError, nil, false,
			"ID «A3» в группе у студента «Сидоров Алексей», а имя «Иванов Иван» — у другого студента."},
		{"name equal to an alias of a student is an error", ModeAdd, r(1, "", "Петя Ф"), StatusError, nil, false, "дополнительное имя"},
		{"unknown id, unknown name", ModeAdd, r(1, "Q1", "Орлов Олег"), StatusNew, nil, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := Validate([]Row{tt.row}, roster(), tt.mode, NameAsIs)
			got := p.Rows[0]
			if got.Status != tt.status || got.Rename != tt.rename || !reflect.DeepEqual(got.StudentID, tt.id) {
				t.Fatalf("got status=%s rename=%v id=%v msg=%q", got.Status, got.Rename, got.StudentID, got.Message)
			}
			if !strings.Contains(got.Message, tt.msg) {
				t.Errorf("message %q does not contain %q", got.Message, tt.msg)
			}
		})
	}
}

func TestValidate_OwnAliasViaExternalID(t *testing.T) {
	t.Parallel()
	existing := []ExistingName{
		{StudentID: 1, ExternalID: "7", FullName: "Иванов Иван"},
		{StudentID: 1, FullName: "Ivanov Ivan", IsAlias: true},
	}
	p := Validate([]Row{r(1, "7", "Ivanov Ivan")}, existing, ModeAdd, NameAsIs)
	if got := p.Rows[0]; got.Status != StatusExisting || !got.Rename || *got.StudentID != 1 {
		t.Errorf("got %+v", got)
	}
	if !p.OK {
		t.Error("OK must be true")
	}
}

func TestValidate_SameStudentTwiceAndDuplicateIDs(t *testing.T) {
	t.Parallel()
	// Row 1 takes student 10 by ID (renaming), row 2 names the same student by
	// its current name.
	p := Validate([]Row{r(1, "A1", "Иванов Иван А."), r(2, "", "Иванов Иван")}, roster(), ModeAdd, NameAsIs)
	if p.Rows[0].Status != StatusExisting {
		t.Errorf("row 1 = %+v", p.Rows[0])
	}
	if p.Rows[1].Status != StatusError || !strings.Contains(p.Rows[1].Message, "строке 1") {
		t.Errorf("row 2 = %+v", p.Rows[1])
	}

	p = Validate([]Row{r(3, "5", "Орлов Олег"), r(4, "5", "Белов Борис"), r(5, "6", "Котов Кирилл")}, nil, ModeAdd, NameAsIs)
	for i := range 2 {
		if p.Rows[i].Status != StatusError || !strings.Contains(p.Rows[i].Message, "строки 3, 4") {
			t.Errorf("row %d = %+v", i, p.Rows[i])
		}
	}
	if p.Rows[2].Status != StatusNew {
		t.Errorf("row 3 = %+v", p.Rows[2])
	}
}

func TestValidate_ReplaceModeArchive(t *testing.T) {
	t.Parallel()
	rows := []Row{
		r(1, "", "Иванов Иван"),     // keeps 10
		r(2, "", "Новикова Анна"),   // new
		r(3, "", "Сидоров Алексей"), // keeps 12 even though duplicated below
		r(4, "", "сидоров алексей"),
	}
	p := Validate(rows, roster(), ModeReplace, NameAsIs)
	if len(p.Archive) != 1 || p.Archive[0].StudentID != 11 || p.Archive[0].FullName != "Фёдоров Пётр" {
		t.Fatalf("archive = %+v", p.Archive)
	}
	if p.Archive[0].Normalized != "федоров петр" {
		t.Errorf("archive normalized = %q", p.Archive[0].Normalized)
	}
	want := Counts{New: 1, Existing: 1, Duplicate: 2, Archive: 1}
	if p.Counts != want {
		t.Errorf("counts = %+v, want %+v", p.Counts, want)
	}

	p = Validate([]Row{r(1, "A1", "Иванов Иван"), r(2, "A3", "Сидоров Алексей"), r(3, "", "Фёдоров Пётр")}, roster(), ModeReplace, NameAsIs)
	if len(p.Archive) != 0 || !p.OK || p.Counts.Existing != 3 {
		t.Errorf("full roster: archive=%+v counts=%+v ok=%v", p.Archive, p.Counts, p.OK)
	}
}

func TestValidate_EmptyInputNotOK(t *testing.T) {
	t.Parallel()
	p := Validate(nil, roster(), ModeReplace, NameAsIs)
	if p.OK {
		t.Error("an empty list must not be OK (replace would archive everybody)")
	}
	if len(p.Archive) != 3 {
		t.Errorf("archive = %d", len(p.Archive))
	}
}

func TestValidate_NameFormat(t *testing.T) {
	t.Parallel()
	rows := []Row{
		r(1, "", "Иванов Иван Иванович"),
		r(2, "", "Петров Пётр Петрович"),
		r(3, "", "Смирнова Анна"),
		r(4, "", "Кузнецов Олег Игоревич А."),
	}
	p := Validate(rows, nil, ModeAdd, NameAsIs)
	if !p.HasPatronymics {
		t.Error("HasPatronymics must be true when most names have 3+ words")
	}
	if p.Rows[0].FullName != "Иванов Иван Иванович" {
		t.Errorf("as_is changed the name: %q", p.Rows[0].FullName)
	}

	p = Validate(rows, nil, ModeAdd, NameLastFirst)
	got := []string{p.Rows[0].FullName, p.Rows[1].FullName, p.Rows[2].FullName, p.Rows[3].FullName}
	want := []string{"Иванов Иван", "Петров Пётр", "Смирнова Анна", "Кузнецов Олег А."}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("last_first = %q, want %q", got, want)
	}
	if !p.HasPatronymics {
		t.Error("HasPatronymics is computed from the original names")
	}
	if p.Rows[0].NormalizedName != "иванов иван" {
		t.Errorf("normalized after format = %q", p.Rows[0].NormalizedName)
	}

	// Dropping patronymics can create namesakes; they must be reported.
	p = Validate([]Row{r(1, "", "Иванов Иван Иванович"), r(2, "", "Иванов Иван Петрович")}, nil, ModeAdd, NameLastFirst)
	if p.Counts.Duplicate != 2 {
		t.Errorf("namesakes after last_first: %+v", p.Counts)
	}

	p = Validate([]Row{r(1, "", "Иванов Иван"), r(2, "", "Петров Пётр"), r(3, "", "Сидоров Алексей Игоревич")}, nil, ModeAdd, NameAsIs)
	if p.HasPatronymics {
		t.Error("HasPatronymics must be false when most names have 2 words")
	}
}

func TestLastFirst(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"Иванов":                   "Иванов",
		"Иванов Иван":              "Иванов Иван",
		"Иванов Иван Иванович":     "Иванов Иван",
		"Иванов Иван А.":           "Иванов Иван А.",
		"Иванов Иван Иванович Ив.": "Иванов Иван Ив.",
		"Алиев Рашид Мамед оглы":   "Алиев Рашид",
		"": "",
	}
	for in, want := range tests {
		if got := lastFirst(in); got != want {
			t.Errorf("lastFirst(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidate_OK(t *testing.T) {
	t.Parallel()
	rows, _ := ParseText("1. Новикова Анна\n2. Орлов Олег\n3. Иванов Иван")
	p := Validate(rows, roster(), ModeAdd, NameAsIs)
	if !p.OK {
		t.Fatalf("expected OK, got %+v", p)
	}
	if p.Counts != (Counts{New: 2, Existing: 1}) {
		t.Errorf("counts = %+v", p.Counts)
	}
}

// Switching to "Фамилия Имя" must not lose students stored with the full
// name: they are matched by the name in the file and renamed.
func TestValidate_LastFirstMatchesStoredFullName(t *testing.T) {
	t.Parallel()
	existing := []ExistingName{{StudentID: 5, FullName: "Иванов Иван Иванович"}}
	p := Validate([]Row{r(1, "", "Иванов Иван Иванович")}, existing, ModeReplace, NameLastFirst)
	got := p.Rows[0]
	if got.Status != StatusExisting || got.StudentID == nil || *got.StudentID != 5 || !got.Rename {
		t.Fatalf("row = %+v", got)
	}
	if got.FullName != "Иванов Иван" || !strings.Contains(got.Message, "«Иванов Иван Иванович» → «Иванов Иван»") {
		t.Errorf("row = %+v", got)
	}
	if len(p.Archive) != 0 || !p.OK {
		t.Errorf("archive = %+v ok = %v", p.Archive, p.OK)
	}
}

func TestValidate_AliasOfUnknownStudent(t *testing.T) {
	t.Parallel()
	// The alias belongs to a student that is not in the active list (archived).
	existing := []ExistingName{{StudentID: 99, FullName: "Ваня", IsAlias: true}}
	p := Validate([]Row{r(1, "", "ваня")}, existing, ModeAdd, NameAsIs)
	got := p.Rows[0]
	if got.Status != StatusError || strings.Contains(got.Message, "«Ваня»") || !strings.Contains(got.Message, "другого студента") {
		t.Errorf("row = %+v", got)
	}
}

func TestNamesakeHint(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, want string }{
		{"Иванов Иван", "Иванов Иван А."},
		{"Иванов Иван А.", "Иванов Иван Б."},
		{"Иванов Иван Б.", "Иванов Иван А."},
		{"Иванов", "Иванов А."},
	}
	for _, tt := range tests {
		if got := namesakeHint(tt.name, names.Normalize(tt.name)); got != tt.want {
			t.Errorf("namesakeHint(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
	p := Validate([]Row{r(1, "", "Иванов Иван А."), r(2, "", "иванов иван а.")}, nil, ModeAdd, NameAsIs)
	if want := MsgDuplicate + " Такое же имя в строке 2. Если это один и тот же студент — удалите лишнюю строку." +
		" Если это разные люди — добавьте уточнение, например «Иванов Иван Б.»."; p.Rows[0].Message != want {
		t.Errorf("message = %q, want %q", p.Rows[0].Message, want)
	}
}

// TestDuplicateMessages: each repeated row names the other line(s), says to
// remove a line written twice, and suggests its own letter, spelled like the
// group's first row, so following every hint makes the names unique.
func TestDuplicateMessages(t *testing.T) {
	t.Parallel()
	rows := []Row{
		r(2, "", "Петрова Анна"),
		r(5, "", "Иванов Иван"),
		r(9, "", "Федоров Артём"),
		r(31, "", "  петрова   АННА "),
		r(40, "", "Федоров Артем"),
		r(41, "", "федоров артём"),
	}
	p := Validate(rows, nil, ModeAdd, NameAsIs)
	const tail = " Если это один и тот же студент — удалите лишнюю строку. Если это разные люди — добавьте уточнение, например "
	want := map[int]string{
		0: MsgDuplicate + " Такое же имя в строке 31." + tail + "«Петрова Анна А.».",
		3: MsgDuplicate + " Такое же имя в строке 2." + tail + "«Петрова Анна Б.».",
		2: MsgDuplicate + " Такое же имя в строках 40 и 41." + tail + "«Федоров Артём А.».",
		4: MsgDuplicate + " Такое же имя в строках 9 и 41." + tail + "«Федоров Артём Б.».",
		5: MsgDuplicate + " Такое же имя в строках 9 и 40." + tail + "«Федоров Артём В.».",
	}
	for i, w := range want {
		if got := p.Rows[i]; got.Status != StatusDuplicate || got.Message != w {
			t.Errorf("row %d (line %d): %s %q\nwant %q", i, got.Line, got.Status, got.Message, w)
		}
	}
	if p.Rows[1].Status != StatusNew || p.Counts.Duplicate != 5 || p.OK {
		t.Errorf("row 1 = %+v, counts = %+v, ok = %v", p.Rows[1], p.Counts, p.OK)
	}
	// The hints really are different names.
	seen := map[string]bool{}
	for _, h := range namesakeHints("Федоров Артём", names.Normalize("Федоров Артём"), 3) {
		if seen[names.Normalize(h)] {
			t.Errorf("hint %q repeats", h)
		}
		seen[names.Normalize(h)] = true
	}
}

// Shortening to "Фамилия Имя" must not attribute a row to another student
// whose stored name happens to equal the short form.
func TestValidate_LastFirstCollision(t *testing.T) {
	t.Parallel()
	existing := []ExistingName{
		{StudentID: 1, FullName: "Иванов Иван Иванович"},
		{StudentID: 2, FullName: "Иванов Иван"},
	}
	p := Validate([]Row{r(1, "", "Иванов Иван Иванович"), r(2, "", "Иванов Иван")}, existing, ModeReplace, NameLastFirst)
	// Both rows shorten to the same name: a duplicate inside the file.
	if p.Counts.Duplicate != 2 {
		t.Errorf("counts = %+v", p.Counts)
	}
	p = Validate([]Row{r(1, "", "Иванов Иван Иванович")}, existing, ModeAdd, NameLastFirst)
	got := p.Rows[0]
	if got.Status != StatusError || !strings.Contains(got.Message, "Без отчества имя «Иванов Иван» совпадёт с другим студентом") {
		t.Errorf("row = %+v", got)
	}
}

// A row matched only by its external ID, under a completely different name,
// is another person who got a freed number (a list numbered 1, 2, 3 in a
// "student_id" column): it must not take over the student's record.
func TestValidate_IDTakenByAnotherPerson(t *testing.T) {
	t.Parallel()
	existing := []ExistingName{
		{StudentID: 101, ExternalID: "1", FullName: "Иванов Иван"},
		{StudentID: 101, FullName: "Ваня И", IsAlias: true},
		{StudentID: 102, ExternalID: "2", FullName: "Петров Пётр"},
		{StudentID: 103, ExternalID: "3", FullName: "Иванова Мария"},
		{StudentID: 104, ExternalID: "4", FullName: "Петрова-Водкина Анна"},
		{StudentID: 301, ExternalID: "7", FullName: "Кузнецов Пётр", Archived: true},
	}
	tests := []struct {
		name    string
		row     Row
		status  RowStatus
		id      *int64
		rename  bool
		restore bool
		msg     string
	}{
		{"different person under the same number", r(1, "1", "Новиков Олег"), StatusError, nil, false, false,
			"ID «1» в группе у студента «Иванов Иван», а в списке здесь «Новиков Олег»"},
		{"surname changed after marriage", r(1, "3", "Петрова Мария"), StatusExisting, ptr(103), true, false, "Имя будет изменено"},
		{"patronymic added", r(1, "1", "Иванов Иван Иванович"), StatusExisting, ptr(101), true, false, "Имя будет изменено"},
		{"the student's alias", r(1, "1", "Ваня И"), StatusExisting, ptr(101), true, false, "Имя будет изменено"},
		{"double surname shortened", r(1, "4", "Водкина Анна"), StatusExisting, ptr(104), true, false, "Имя будет изменено"},
		{"an initial alone is no common word", r(1, "2", "Сидоров А. П."), StatusError, nil, false, false, "ID «2»"},
		{"archived number taken by another person", r(1, "7", "Орлов Олег"), StatusNew, nil, false, false,
			"под ID «7» записан «Кузнецов Пётр» — это другой человек"},
		{"archived student comes back", r(1, "7", "Кузнецов Пётр Ильич"), StatusNew, ptr(301), true, true, "Был в архиве"},
	}
	for _, mode := range []Mode{ModeAdd, ModeReplace} {
		for _, tt := range tests {
			t.Run(string(mode)+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				p := Validate([]Row{tt.row}, existing, mode, NameAsIs)
				got := p.Rows[0]
				if got.Status != tt.status || got.Rename != tt.rename || got.Restore != tt.restore || !reflect.DeepEqual(got.StudentID, tt.id) {
					t.Fatalf("got status=%s rename=%v restore=%v id=%v msg=%q", got.Status, got.Rename, got.Restore, got.StudentID, got.Message)
				}
				if !strings.Contains(got.Message, tt.msg) {
					t.Errorf("message %q does not contain %q", got.Message, tt.msg)
				}
				if tt.status == StatusError && p.OK {
					t.Error("a blocked row must block the import")
				}
			})
		}
	}

	// The finding's file: one new person took a freed number, the rest kept theirs.
	file := []Row{r(1, "1", "Новиков Олег"), r(2, "2", "Петров Пётр"), r(3, "3", "Иванова Мария")}
	for _, mode := range []Mode{ModeAdd, ModeReplace} {
		p := Validate(file, existing, mode, NameAsIs)
		if p.OK || p.Counts.Error != 1 || p.Counts.Existing != 2 {
			t.Errorf("%s: OK=%v counts=%+v, want the renumbered row blocked", mode, p.OK, p.Counts)
		}
	}
}
