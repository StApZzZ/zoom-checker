package importer

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"zoom-checker/internal/names"
)

// NameFormat says how full names from the file are stored.
type NameFormat string

const (
	// NameAsIs keeps names exactly as in the file.
	NameAsIs NameFormat = "as_is"
	// NameLastFirst keeps "Фамилия Имя" and drops the patronymic (offered
	// when Preview.HasPatronymics, since Zoom names rarely include it).
	// Short initials added to tell namesakes apart ("Иванов Иван А.") are kept.
	NameLastFirst NameFormat = "last_first"
)

// Mode of an import.
type Mode string

const (
	// ModeAdd adds new students and keeps everybody else.
	ModeAdd Mode = "add"
	// ModeReplace makes the file the full roster: active students missing
	// from it are listed in Preview.Archive.
	ModeReplace Mode = "replace"
)

// RowStatus is the verdict for one imported row.
type RowStatus string

const (
	StatusNew       RowStatus = "new"
	StatusExisting  RowStatus = "existing"
	StatusError     RowStatus = "error"
	StatusDuplicate RowStatus = "duplicate"
)

// User-facing messages (Russian).
const (
	// MsgDuplicate is the TZ §8 text for names that are equal after normalization.
	MsgDuplicate = "Невозможно однозначно идентифицировать студентов по Zoom display name."
	MsgEmptyName = "Пустое имя"
	MsgExisting  = "Уже есть в группе."
)

// ExistingName is a student or an alias of the group being imported into.
// Normalized may be left empty; it is then computed from FullName.
//
// Active students and their aliases occupy names (TZ §8). Archived ones
// (Archived set, for an alias: the owner is archived) do not; a new row that
// names an archived student restores it instead of creating a second record,
// so its attendance history is kept.
type ExistingName struct {
	StudentID  int64
	ExternalID string
	FullName   string // student name, or the alias text when IsAlias
	Normalized string
	IsAlias    bool
	Archived   bool
}

// PreviewRow is one validated row.
type PreviewRow struct {
	Line           int
	ExternalID     string
	FullName       string // after NameFormat
	NormalizedName string
	Status         RowStatus
	Message        string // Russian, empty for plain new rows
	StudentID      *int64 // matched student for StatusExisting, archived student for Restore
	Rename         bool   // existing student whose stored full name will change
	// Restore marks a StatusNew row that brings back the archived student
	// StudentID (with the row's name and ID) instead of creating a new one.
	Restore bool
}

// Counts of rows per status; Archive counts Preview.Archive and Restore the
// StatusNew rows that restore an archived student (they are counted in New too).
// Rename counts the StatusExisting rows that change a student's stored name
// (counted in Existing too), so a rename never hides among "already in the group".
type Counts struct {
	New, Existing, Error, Duplicate, Archive, Restore, Rename int
}

// Preview is the result of Validate shown to the user before committing.
type Preview struct {
	Rows []PreviewRow
	// Archive lists, in replace mode, the active students absent from the file.
	Archive        []ExistingName
	Counts         Counts
	HasPatronymics bool // most names have 3+ words: offer NameLastFirst
	OK             bool // there are rows and none is an error or duplicate
}

// Validate checks parsed rows against the group's active students and
// aliases: empty and over-long names, names equal after normalization inside
// the file (TZ §8), clashes with aliases, and which rows are already in the
// group. A row matches a student by external ID first, then by normalized
// name (the name as written in the file before the one after NameFormat).
// A name match whose external ID differs from the student's is a namesake
// error in ModeAdd (TZ §8) and an ID change in ModeReplace, where the file
// is the whole roster. New rows that name an archived student (by ID, or by
// the name after NameFormat) restore it (PreviewRow.Restore). It is pure and
// deterministic; rows keep their input order.
//
// Commit re-validation: rows taken from a preview are already formatted, and
// validating them again with the same mode and format gives the same verdict
// as the preview did for every row of an OK preview. With NameLastFirst this
// needs one more rule, because the original long name is gone by then: a
// student is also found by the short form of its stored name ("Иванов Иван
// Иванович" by "Иванов Иван") when exactly one active student has that short
// form; a short form shared by several students is an error.
func Validate(rows []Row, existing []ExistingName, mode Mode, format NameFormat) Preview {
	ix := indexExisting(existing)
	p := Preview{Rows: make([]PreviewRow, len(rows))}

	// fileNorm is the normalized name as written in the file; with
	// NameLastFirst it still finds students stored under the full name.
	fileNorm := make([]string, len(rows))
	threeWords, named := 0, 0
	for i, r := range rows {
		name := cleanCell(r.FullName)
		fileNorm[i] = names.Normalize(name)
		if name != "" {
			named++
			if len(strings.Fields(name)) >= 3 {
				threeWords++
			}
		}
		if format == NameLastFirst {
			name = lastFirst(name)
		}
		pr := PreviewRow{
			Line:           r.Line,
			ExternalID:     cleanCell(r.ExternalID),
			FullName:       name,
			NormalizedName: names.Normalize(name),
			Status:         StatusNew,
		}
		switch {
		case pr.NormalizedName == "":
			pr.Status, pr.Message = StatusError, MsgEmptyName
		case utf8.RuneCountInString(name) > MaxNameLength:
			pr.Status, pr.Message = StatusError, fmt.Sprintf("Имя слишком длинное (больше %d символов).", MaxNameLength)
		case strings.IndexFunc(name, unicode.IsLetter) < 0:
			pr.Status, pr.Message = StatusError, "В имени нет ни одной буквы. Проверьте эту строку."
		case utf8.RuneCountInString(pr.ExternalID) > MaxExternalIDLength:
			pr.Status, pr.Message = StatusError, fmt.Sprintf("ID слишком длинный (больше %d символов).", MaxExternalIDLength)
		}
		p.Rows[i] = pr
	}
	p.HasPatronymics = named > 0 && threeWords*2 > named

	markDuplicates(p.Rows)
	markDuplicateIDs(p.Rows)

	matched := map[int64]bool{} // students named by any row (keeps them off Archive)
	claimed := map[int64]int{}  // student → line of the row that took it
	for i := range p.Rows {
		pr := &p.Rows[i]
		// The name as written in the file goes first: with NameLastFirst a
		// student stored under exactly that full name is the better match.
		match, how := ix.match(pr.ExternalID, fileNorm[i], pr.NormalizedName)
		shortOwners := 0
		if format == NameLastFirst && pr.NormalizedName != "" {
			shortOwners = len(ix.byShort[pr.NormalizedName])
			if how == matchNone && shortOwners == 1 {
				match, how = ix.byShort[pr.NormalizedName][0], matchByShort
			}
		}
		ok := how != matchNone
		if ok {
			matched[match.StudentID] = true
		}
		if pr.Status != StatusNew {
			continue
		}
		// Matched by the long name in the file while the short form fits
		// several students: a commit (which only has the short form) could
		// not tell them apart.
		if viaLongName := how == matchByName && fileNorm[i] != pr.NormalizedName && match.Normalized == fileNorm[i]; viaLongName && shortOwners > 1 {
			pr.Status = StatusError
			pr.Message = fmt.Sprintf("Без отчества имя «%s» подходит сразу нескольким студентам группы: %s. Оставьте имена полностью или добавьте уточнение, например «%s».",
				pr.FullName, quoteNames(ix.byShort[pr.NormalizedName]), namesakeHint(pr.FullName, pr.NormalizedName))
			continue
		}
		if how == matchByShort && fileNorm[i] != pr.NormalizedName && fileNorm[i] != match.Normalized {
			// Both the file and the group have a patronymic, and they differ:
			// two different people who look the same without it.
			pr.Status = StatusError
			pr.Message = fmt.Sprintf("Без отчества имя «%s» совпадёт со студентом группы «%s». Оставьте имена полностью или добавьте уточнение, например «%s».",
				pr.FullName, match.FullName, namesakeHint(pr.FullName, pr.NormalizedName))
			continue
		}
		if a, isAlias := ix.aliases[pr.NormalizedName]; isAlias && (!ok || a.StudentID != match.StudentID) {
			pr.Status = StatusError
			if owner, known := ix.byID[a.StudentID]; known {
				pr.Message = fmt.Sprintf("Это имя уже записано как дополнительное имя студента «%s». Измените имя в списке или удалите дополнительное имя в карточке студента.", owner.FullName)
			} else {
				pr.Message = "Это имя уже записано как дополнительное имя другого студента (возможно, из архива). Измените имя в списке или удалите это дополнительное имя."
			}
			continue
		}
		if !ok {
			continue
		}
		if other, taken := ix.byName[pr.NormalizedName]; taken && other.StudentID != match.StudentID {
			pr.Status = StatusError
			if how == matchByID {
				pr.Message = fmt.Sprintf("ID «%s» в группе у студента «%s», а имя «%s» — у другого студента. Проверьте ID в этой строке: у каждого студента должен быть свой.", pr.ExternalID, match.FullName, other.FullName)
			} else { // matched by the full name, the shortened one is taken
				pr.Message = fmt.Sprintf("Без отчества имя «%s» совпадёт с другим студентом группы. Оставьте имена полностью или добавьте уточнение, например «%s».", other.FullName, namesakeHint(pr.FullName, pr.NormalizedName))
			}
			continue
		}
		idChange := how != matchByID && pr.ExternalID != "" && match.ExternalID != "" && pr.ExternalID != match.ExternalID
		if idChange && mode != ModeReplace {
			// Same name, different IDs: in add mode this is most likely a
			// namesake joining the group (TZ §8). In replace mode the file is
			// the whole roster, so one such name is the same person.
			pr.Status = StatusError
			pr.Message = fmt.Sprintf("%s В группе уже есть «%s» с другим ID («%s»). Если это другой человек, добавьте уточнение, например «%s». Если тот же — исправьте ID в списке или уберите его.",
				MsgDuplicate, match.FullName, match.ExternalID, namesakeHint(pr.FullName, pr.NormalizedName))
			continue
		}
		if how == matchByID && !samePerson(match, ix.aliasesOf[match.StudentID], fileNorm[i], pr.NormalizedName) {
			// The ID is all the row has in common with the student. A list
			// numbered 1, 2, 3 ("student_id") re-imported after one student
			// left and another took the number would otherwise hand the old
			// student's record and attendance history to the new person.
			pr.Status = StatusError
			pr.Message = fmt.Sprintf("ID «%s» в группе у студента «%s», а в списке здесь «%s». Если это другой человек — исправьте или уберите ID в этой строке. Если это тот же студент и у него сменилось ФИО — сначала измените имя в карточке студента, потом загрузите список снова.",
				pr.ExternalID, match.FullName, pr.FullName)
			continue
		}
		if line, dup := claimed[match.StudentID]; dup {
			pr.Status = StatusError
			pr.Message = fmt.Sprintf("Этот студент уже указан в строке %d.", line)
			continue
		}
		claimed[match.StudentID] = pr.Line
		id := match.StudentID
		pr.Status, pr.StudentID = StatusExisting, &id
		msg := []string{MsgExisting}
		if pr.FullName != match.FullName {
			pr.Rename = true
			msg = append(msg, fmt.Sprintf("Имя будет изменено: «%s» → «%s».", match.FullName, pr.FullName))
		}
		switch {
		case idChange:
			msg = append(msg, fmt.Sprintf("ID будет изменён: «%s» → «%s».", match.ExternalID, pr.ExternalID))
		case pr.ExternalID != "" && match.ExternalID == "":
			msg = append(msg, fmt.Sprintf("Будет добавлен ID «%s».", pr.ExternalID))
		}
		pr.Message = strings.Join(msg, " ")
	}

	ix.restoreArchived(p.Rows, mode, matched, claimed)

	if mode == ModeReplace {
		for _, s := range ix.students {
			if !matched[s.StudentID] {
				p.Archive = append(p.Archive, s)
			}
		}
	}

	for _, r := range p.Rows {
		switch r.Status {
		case StatusNew:
			p.Counts.New++
			if r.Restore {
				p.Counts.Restore++
			}
		case StatusExisting:
			p.Counts.Existing++
			if r.Rename {
				p.Counts.Rename++
			}
		case StatusError:
			p.Counts.Error++
		case StatusDuplicate:
			p.Counts.Duplicate++
		}
	}
	p.Counts.Archive = len(p.Archive)
	p.OK = len(p.Rows) > 0 && p.Counts.Error == 0 && p.Counts.Duplicate == 0
	return p
}

// markDuplicates flags every row whose normalized name occurs more than once.
// Besides the TZ §8 sentence, each row names the other line(s) with the same
// name and covers both causes: a line pasted twice (remove it) and real
// namesakes (tell them apart). Every row of a group gets its own letter, taken
// from the first row's spelling, so following all the hints makes the names
// unique instead of producing "Петрова Анна А." twice.
func markDuplicates(rows []PreviewRow) {
	groups := map[string][]int{}
	for i, r := range rows {
		if r.Status == StatusNew {
			groups[r.NormalizedName] = append(groups[r.NormalizedName], i)
		}
	}
	for norm, idx := range groups {
		if len(idx) < 2 {
			continue
		}
		hints := namesakeHints(rows[idx[0]].FullName, norm, len(idx))
		for k, i := range idx {
			var others []string
			for _, j := range idx {
				if j != i && rows[j].Line > 0 {
					others = append(others, strconv.Itoa(rows[j].Line))
				}
			}
			msg := MsgDuplicate
			switch len(others) {
			case 0:
			case 1:
				msg += " Такое же имя в строке " + others[0] + "."
			default:
				msg += " Такое же имя в строках " + joinRu(others) + "."
			}
			msg += " Если это один и тот же студент — удалите лишнюю строку."
			if k < len(hints) {
				msg += " Если это разные люди — добавьте уточнение, например «" + hints[k] + "»."
			} else {
				msg += " Если это разные люди — добавьте уточнение, например инициал отчества."
			}
			rows[i].Status, rows[i].Message = StatusDuplicate, msg
		}
	}
}

// joinRu joins items as "2, 9 и 31".
func joinRu(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " и " + items[len(items)-1]
}

// namesakeLetters are the initials offered to tell namesakes apart.
const namesakeLetters = "АБВГДЕЖЗИКЛМНОПРСТУФХЦЧШЭЮЯ"

// namesakeHint suggests a name told apart by an initial, as in TZ §8:
// "Иванов Иван" → "Иванов Иван А.". A name that already ends with
// "А." gets the next letter instead of "Иванов Иван А. А.".
func namesakeHint(fullName, normalized string) string {
	if h := namesakeHints(fullName, normalized, 1); len(h) == 1 {
		return h[0]
	}
	return namesakeBase(fullName) + " А."
}

// namesakeHints returns up to n different hints ("… А.", "… Б.", …) for a
// name, skipping a letter that would give the same name back.
func namesakeHints(fullName, normalized string, n int) []string {
	base := namesakeBase(fullName)
	var out []string
	for _, l := range namesakeLetters {
		if len(out) == n {
			break
		}
		if hint := base + " " + string(l) + "."; names.Normalize(hint) != normalized {
			out = append(out, hint)
		}
	}
	return out
}

// namesakeBase is the name without trailing initials ("Иванов Иван А." →
// "Иванов Иван").
func namesakeBase(fullName string) string {
	words := strings.Fields(fullName)
	for len(words) > 1 && initialRe.MatchString(words[len(words)-1]) {
		words = words[:len(words)-1]
	}
	return strings.Join(words, " ")
}

// markDuplicateIDs flags rows sharing one external ID: they would all match
// the same student.
func markDuplicateIDs(rows []PreviewRow) {
	groups := map[string][]int{}
	for i, r := range rows {
		if r.Status == StatusNew && r.ExternalID != "" {
			groups[r.ExternalID] = append(groups[r.ExternalID], i)
		}
	}
	for id, idx := range groups {
		if len(idx) < 2 {
			continue
		}
		lines := make([]string, len(idx))
		for k, i := range idx {
			lines[k] = strconv.Itoa(rows[i].Line)
		}
		msg := fmt.Sprintf("ID «%s» встречается в списке несколько раз (строки %s). У каждого студента должен быть свой ID.", id, strings.Join(lines, ", "))
		for _, i := range idx {
			rows[i].Status, rows[i].Message = StatusError, msg
		}
	}
}

type existingIndex struct {
	students      []ExistingName            // active students, input order
	byID          map[int64]ExistingName    // student id → active student
	byName        map[string]ExistingName   // normalized name → active student
	byExt         map[string]ExistingName   // external id → active student
	byShort       map[string][]ExistingName // normalized lastFirst(name) → active students with a longer name
	aliases       map[string]ExistingName   // normalized alias → alias of an active student
	activeAliases []ExistingName            // aliases of active students, input order
	aliasesOf     map[int64][]ExistingName  // active student id → its aliases

	archByName  map[string]ExistingName  // normalized name → newest archived student
	archByExt   map[string]ExistingName  // external id → newest archived student
	archAliases map[int64][]ExistingName // archived student id → its aliases
	archivedIDs map[int64]bool           // archived students
}

func indexExisting(existing []ExistingName) existingIndex {
	ix := existingIndex{
		byID:        map[int64]ExistingName{},
		byName:      map[string]ExistingName{},
		byExt:       map[string]ExistingName{},
		byShort:     map[string][]ExistingName{},
		aliases:     map[string]ExistingName{},
		aliasesOf:   map[int64][]ExistingName{},
		archByName:  map[string]ExistingName{},
		archByExt:   map[string]ExistingName{},
		archAliases: map[int64][]ExistingName{},
		archivedIDs: map[int64]bool{},
	}
	// newest keeps the archived student with the highest id (the most recent
	// record of that person) under key k.
	newest := func(m map[string]ExistingName, k string, e ExistingName) {
		if prev, ok := m[k]; k != "" && (!ok || e.StudentID > prev.StudentID) {
			m[k] = e
		}
	}
	for _, e := range existing {
		if e.Normalized == "" {
			e.Normalized = names.Normalize(e.FullName)
		}
		e.ExternalID = cleanCell(e.ExternalID)
		switch {
		case e.IsAlias && e.Archived:
			if e.Normalized != "" {
				ix.archAliases[e.StudentID] = append(ix.archAliases[e.StudentID], e)
			}
			continue
		case e.IsAlias:
			if _, dup := ix.aliases[e.Normalized]; !dup && e.Normalized != "" {
				ix.aliases[e.Normalized] = e
				ix.activeAliases = append(ix.activeAliases, e)
				ix.aliasesOf[e.StudentID] = append(ix.aliasesOf[e.StudentID], e)
			}
			continue
		case e.Archived:
			if _, active := ix.byID[e.StudentID]; !active && !ix.archivedIDs[e.StudentID] {
				ix.archivedIDs[e.StudentID] = true
				newest(ix.archByName, e.Normalized, e)
				newest(ix.archByExt, e.ExternalID, e)
			}
			continue
		}
		if _, dup := ix.byID[e.StudentID]; dup || ix.archivedIDs[e.StudentID] {
			continue
		}
		ix.byID[e.StudentID] = e
		ix.students = append(ix.students, e)
		if _, dup := ix.byName[e.Normalized]; !dup && e.Normalized != "" {
			ix.byName[e.Normalized] = e
		}
		if _, dup := ix.byExt[e.ExternalID]; !dup && e.ExternalID != "" {
			ix.byExt[e.ExternalID] = e
		}
		// Only names that NameLastFirst would shorten; a stored short name is
		// found by byName.
		if short := names.Normalize(lastFirst(cleanCell(e.FullName))); short != "" && short != e.Normalized {
			ix.byShort[short] = append(ix.byShort[short], e)
		}
	}
	return ix
}

type matchKind int

const (
	matchNone matchKind = iota
	matchByID
	matchByName
	matchByShort // NameLastFirst: the short form of the stored name
)

// restoreArchived turns new rows that name an archived student into restores
// of that student. Archived students are found by external ID, or by the name
// after NameFormat (never by the long name in the file, which a commit no
// longer has). A name match with a different external ID is another person.
// A restored student brings its aliases back into the group's namespace, so
// it is restored only when none of them is taken by the final roster.
func (ix existingIndex) restoreArchived(rows []PreviewRow, mode Mode, matched map[int64]bool, claimed map[int64]int) {
	if len(ix.archivedIDs) == 0 {
		return
	}
	// Names the group will hold after the import (archived aliases aside).
	taken := map[string]bool{}
	final := map[int64]string{} // active student → normalized name after the import
	for _, r := range rows {
		switch r.Status {
		case StatusNew, StatusExisting:
			taken[r.NormalizedName] = true
			if r.Status == StatusExisting && r.StudentID != nil {
				final[*r.StudentID] = r.NormalizedName
			}
		}
	}
	for _, s := range ix.students {
		if mode == ModeReplace && !matched[s.StudentID] {
			continue // archived by this import
		}
		if _, ok := claimed[s.StudentID]; !ok {
			taken[s.Normalized] = true
			final[s.StudentID] = s.Normalized
		}
	}
	for _, a := range ix.activeAliases {
		if n, stays := final[a.StudentID]; stays && n != a.Normalized {
			taken[a.Normalized] = true // an alias equal to the new name is dropped
		}
	}

	restored := map[int64]bool{}
	for i := range rows {
		pr := &rows[i]
		if pr.Status != StatusNew {
			continue
		}
		a, how := ExistingName{}, matchNone
		if e, ok := ix.archByExt[pr.ExternalID]; ok && pr.ExternalID != "" {
			a, how = e, matchByID
		} else if e, ok := ix.archByName[pr.NormalizedName]; ok {
			a, how = e, matchByName
		}
		if how == matchNone || restored[a.StudentID] {
			continue
		}
		if how == matchByName && pr.ExternalID != "" && a.ExternalID != "" && pr.ExternalID != a.ExternalID {
			continue // a namesake with another ID: a new student
		}
		if how == matchByID && !samePerson(a, ix.archAliases[a.StudentID], pr.NormalizedName) {
			// Only the ID is shared: the number went to another person.
			pr.Message = fmt.Sprintf("В архиве группы под ID «%s» записан «%s» — это другой человек, поэтому будет добавлен новый студент.",
				pr.ExternalID, a.FullName)
			continue
		}
		var clash *ExistingName
		for k, al := range ix.archAliases[a.StudentID] {
			if al.Normalized != pr.NormalizedName && taken[al.Normalized] {
				clash = &ix.archAliases[a.StudentID][k]
				break
			}
		}
		if clash != nil {
			pr.Message = fmt.Sprintf("В архиве группы есть «%s», но вернуть его нельзя: его дополнительное имя «%s» уже занято. Будет добавлен новый студент.", a.FullName, clash.FullName)
			continue
		}
		for _, al := range ix.archAliases[a.StudentID] {
			if al.Normalized != pr.NormalizedName {
				taken[al.Normalized] = true
			}
		}
		restored[a.StudentID] = true
		id := a.StudentID
		pr.StudentID, pr.Restore = &id, true
		msg := []string{"Был в архиве группы — вернётся в список, история посещений сохранится."}
		if pr.FullName != a.FullName {
			pr.Rename = true
			msg = append(msg, fmt.Sprintf("Имя будет изменено: «%s» → «%s».", a.FullName, pr.FullName))
		}
		switch {
		case pr.ExternalID != "" && a.ExternalID == "":
			msg = append(msg, fmt.Sprintf("Будет добавлен ID «%s».", pr.ExternalID))
		case pr.ExternalID != "" && pr.ExternalID != a.ExternalID:
			msg = append(msg, fmt.Sprintf("ID будет изменён: «%s» → «%s».", a.ExternalID, pr.ExternalID))
		}
		pr.Message = strings.Join(msg, " ")
	}
}

// samePerson reports whether a row matched to student st by external ID only
// can still be that person: some word of the row's names (normalized) is a
// word of the student's name or of one of its aliases. A changed surname
// keeps the first name, an added patronymic or a fixed typo keeps the rest,
// while a completely different name means the ID now belongs to someone else.
func samePerson(st ExistingName, aliases []ExistingName, rowNames ...string) bool {
	known := map[string]bool{}
	for _, w := range nameWords(st.Normalized) {
		known[w] = true
	}
	for _, a := range aliases {
		for _, w := range nameWords(a.Normalized) {
			known[w] = true
		}
	}
	for _, n := range rowNames {
		for _, w := range nameWords(n) {
			if known[w] {
				return true
			}
		}
	}
	return false
}

// nameWords splits a normalized name into the words that can tell people
// apart: split at spaces and hyphens (double surnames), punctuation trimmed,
// initials (a single letter) left out.
func nameWords(n string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(n, func(r rune) bool { return unicode.IsSpace(r) || r == '-' }) {
		w = strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		if utf8.RuneCountInString(w) > 1 {
			out = append(out, w)
		}
	}
	return out
}

// quoteNames lists student names as «А», «Б».
func quoteNames(ss []ExistingName) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = "«" + s.FullName + "»"
	}
	return strings.Join(q, ", ")
}

// match finds the existing student for a row: by external ID first, then by
// each normalized name in order (the stored form, then the form in the file).
func (ix existingIndex) match(ext string, normalized ...string) (ExistingName, matchKind) {
	if ext != "" {
		if e, ok := ix.byExt[ext]; ok {
			return e, matchByID
		}
	}
	for _, n := range normalized {
		if e, ok := ix.byName[n]; ok && n != "" {
			return e, matchByName
		}
	}
	return ExistingName{}, matchNone
}

// initialRe matches a disambiguating initial such as "А." or "Ив.".
var initialRe = regexp.MustCompile(`^\pL\.?$|^\pL\pL\.$`)

// lastFirst keeps the first two words (Фамилия Имя) plus trailing initials.
func lastFirst(name string) string {
	words := strings.Fields(name)
	if len(words) <= 2 {
		return name
	}
	kept := words[:2]
	for _, w := range words[2:] {
		if initialRe.MatchString(w) {
			kept = append(kept, w)
		}
	}
	return strings.Join(kept, " ")
}
