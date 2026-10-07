package importer

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// record is one non-blank source line (text) or row (XLSX) split into cells.
type record struct {
	line   int
	fields []string
}

// table is the cell grid produced by a format-specific reader.
type table struct {
	recs  []record
	delim string
}

// delimCandidates in tie-break priority: tab (pasted Excel cells / "Unicode
// text"), semicolon (Russian Excel CSV), comma, pipe.
var delimCandidates = []rune{'\t', ';', ',', '|'}

const delimSampleLines = 200

// splitText splits decoded text into records, detecting the delimiter.
func splitText(text string) table {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")

	// Some tools write a "sep=;" hint line for Excel. Blank it out (keeping
	// line numbers) and trust it.
	var delim rune
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if len(t) > 4 && strings.EqualFold(t[:4], "sep=") {
			if rs := []rune(t[4:]); len(rs) == 1 && validDelim(rs[0]) {
				delim = rs[0]
				lines[i] = ""
			}
		}
		break
	}
	if delim == 0 {
		delim = detectDelimiter(lines)
	}
	if delim == 0 {
		return singleColumn(lines)
	}
	recs, err := readCSV(strings.Join(lines, "\n"), delim)
	if err != nil {
		return singleColumn(lines)
	}
	return table{recs: recs, delim: string(delim)}
}

func validDelim(r rune) bool {
	return r != 0 && r != '"' && r != '\r' && r != '\n' && r != '\uFFFD' && !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

// detectDelimiter picks the candidate that splits the most lines into the same
// number (>= 2) of cells. Quotes are respected through encoding/csv, so a
// comma inside "Иванов, Иван" does not count. Returns 0 for single-column text.
func detectDelimiter(lines []string) rune {
	sample := make([]string, 0, delimSampleLines)
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			sample = append(sample, l)
			if len(sample) == delimSampleLines {
				break
			}
		}
	}
	n := len(sample)
	if n == 0 {
		return 0
	}
	text := strings.Join(sample, "\n")
	var (
		best      rune
		bestScore float64
	)
	for _, d := range delimCandidates {
		if !strings.ContainsRune(text, d) {
			continue
		}
		recs, err := readCSV(text, d)
		if err != nil {
			continue
		}
		counts := map[int]int{}
		multi := 0
		for _, r := range recs {
			if blank(r.fields) {
				continue
			}
			counts[len(r.fields)]++
			if len(r.fields) >= 2 {
				multi++
			}
		}
		mode, modeN := 0, 0
		for c, k := range counts {
			if k > modeN || (k == modeN && c > mode) {
				mode, modeN = c, k
			}
		}
		if mode < 2 {
			continue
		}
		// Most lines must actually be split; one odd line (e.g. a title) is
		// tolerated in short lists.
		if multi*5 < n*4 && (n < 4 || multi < n-1) {
			continue
		}
		if score := float64(modeN) / float64(n); score > bestScore {
			best, bestScore = d, score
		}
	}
	return best
}

// readCSV splits text into records. With LazyQuotes one unterminated opening
// quote (`1;"Иванов Иван`) makes encoding/csv swallow the rest of the file
// into a single cell; when a cell spanning several lines holds the delimiter
// after a line break, whole records were swallowed and every line is read
// on its own instead. Genuine multi-line cells (Alt+Enter in Excel) rarely
// contain the delimiter, so they are kept.
func readCSV(text string, delim rune) ([]record, error) {
	recs, err := readCSVAll(text, delim)
	if err == nil && !swallowedRecords(recs, delim) {
		return recs, nil
	}
	return readCSVLines(text, delim)
}

func newCSVReader(text string, delim rune) *csv.Reader {
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = delim
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	// With a tab delimiter, TrimLeadingSpace would swallow empty cells.
	r.TrimLeadingSpace = delim != '\t'
	return r
}

func readCSVAll(text string, delim rune) ([]record, error) {
	r := newCSVReader(text, delim)
	var recs []record
	for {
		fields, err := r.Read()
		if errors.Is(err, io.EOF) {
			return recs, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read csv: %w", err)
		}
		line, _ := r.FieldPos(0)
		recs = append(recs, record{line: line, fields: fields})
	}
}

func swallowedRecords(recs []record, delim rune) bool {
	for _, r := range recs {
		for _, f := range r.fields {
			if i := strings.IndexByte(f, '\n'); i >= 0 && strings.ContainsRune(f[i:], delim) {
				return true
			}
		}
	}
	return false
}

// readCSVLines reads every line as a separate record. A line with an odd
// number of quotes loses its first quote, so a stray opening quote cannot
// glue the rest of the line into one cell.
func readCSVLines(text string, delim rune) ([]record, error) {
	var recs []record
	for i, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if strings.Count(l, `"`)%2 == 1 {
			l = strings.Replace(l, `"`, "", 1)
		}
		fields, err := newCSVReader(l, delim).Read()
		if errors.Is(err, io.EOF) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read csv line %d: %w", i+1, err)
		}
		recs = append(recs, record{line: i + 1, fields: fields})
	}
	return recs, nil
}

func singleColumn(lines []string) table {
	var recs []record
	for i, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		recs = append(recs, record{line: i + 1, fields: []string{unquote(l)}})
	}
	return table{recs: recs}
}

// unquote removes CSV-style quotes around a whole line.
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
	}
	return s
}

func blank(fields []string) bool {
	for _, f := range fields {
		if cleanCell(f) != "" {
			return false
		}
	}
	return true
}

// cleanCell drops invisible format characters (zero-width spaces, BOM, bidi
// marks) and control characters (NUL and the like, which PostgreSQL text
// cannot hold), turns every whitespace run (incl. NBSP and tabs) into one
// space and trims. Letter case is kept.
func cleanCell(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	pending := false
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Cf, r):
			continue
		case unicode.IsSpace(r):
			pending = b.Len() > 0
			continue
		case unicode.IsControl(r):
			continue
		}
		if pending {
			b.WriteByte(' ')
			pending = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Header aliases, compared after headerKey(). Russian and English variants
// that teachers and HR/LMS exports actually use.
//
// Only a column whose header explicitly names an identifier becomes
// Row.ExternalID. Row numbers ("№", "п/п", a leading column of 1, 2, 3 in a
// list without a header) are ignored: lists get renumbered, and a re-import of
// a renumbered list must match students by name, not by position.
var (
	// ID columns: an identifier from another system.
	idStrongAliases = keySet("student_id", "student id", "studentid", "id", "external_id", "external id", "ext_id",
		"id студента", "id слушателя", "id участника", "табельный", "табельный номер", "таб. номер", "таб. №",
		"код", "код студента", "код слушателя", "личный номер", "номер студента", "номер слушателя",
		"студенческий", "студенческий билет", "номер студенческого", "номер студенческого билета",
		"номер зачетки", "номер зачетной книжки", "зачетка", "зачетная книжка",
		// The same with "№" for "номер", as Russian forms usually write it.
		"табельный №", "личный №", "№ студенческого", "№ студенческого билета", "№ студ билета",
		"номер студ билета", "студ билет", "№ зачетки", "№ зачетной книжки", "№ личного дела", "номер личного дела",
		"snils", "снилс", "user_id", "user id", "идентификатор")
	// E-mail columns are a stable ID too, but a real ID column wins over them.
	idEmailAliases = keySet("email", "e-mail", "e mail", "mail", "почта", "эл. почта", "электронная почта",
		"адрес электронной почты", "email address", "e-mail address")
	// Row number columns: ignored. Rows with a number but no name are
	// pre-numbered blank lines and are skipped.
	rowNumberAliases = keySet("номер", "№", "№№", "№ п/п", "№№ п/п", "№ пп", "n", "n п/п", "п/п", "#", "no", "no.", "nr",
		"номер п/п", "порядковый номер")
	fullNameAliases = keySet("name", "full_name", "full name", "fullname", "fio", "фио", "ф.и.о.", "ф. и. о.",
		"студент", "студенты", "слушатель", "слушатели", "участник", "участники", "ученик", "обучающийся",
		"сотрудник", "student", "student name", "student_name", "фио студента", "фио слушателя", "фио участника",
		"фамилия имя", "фамилия и имя", "фамилия, имя", "фамилия имя отчество", "фамилия, имя, отчество",
		"имя фамилия", "полное имя")
	lastNameAliases   = keySet("фамилия", "last name", "last_name", "lastname", "surname", "family name")
	firstNameAliases  = keySet("имя", "first name", "first_name", "firstname", "given name")
	middleNameAliases = keySet("отчество", "middle name", "middle_name", "patronymic")
)

func keySet(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[headerKey(n)] = true
	}
	return m
}

// headerKey canonicalizes a header cell: lower case, ё→е, without spaces and
// punctuation that people add or drop ("Ф.И.О.:" == "фио", "Full_Name" == "fullname").
func headerKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(cleanCell(s)) {
		switch {
		case r == 'ё':
			b.WriteRune('е')
		case unicode.IsSpace(r), strings.ContainsRune("._-,:;*\"'«»()", r):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// layout says which cells hold what.
type layout struct {
	header int // index into recs, -1 = no header
	idCol  int // -1 = none
	// rowNumbers marks idCol as list numbering: its values are never an
	// external ID, and a numbered row without a name is skipped.
	rowNumbers        bool
	idLower           bool // idCol holds e-mails: compared case-insensitively
	nameCol           int  // used when !split
	split             bool
	lastCol, firstCol int
	middleCol         int // -1 = none
	label             string
}

const headerSearchRows = 10

// parseTable turns cells into rows: finds the header (it may follow a few
// title rows), picks the ID and name columns, and builds full names.
func parseTable(t table) ([]Row, Detected) {
	det := Detected{Delimiter: t.delim}
	recs := make([]record, 0, len(t.recs))
	for _, r := range t.recs {
		if !blank(r.fields) {
			recs = append(recs, r)
		}
	}
	if len(recs) == 0 {
		return nil, det
	}
	lay := detectLayout(recs)
	det.HasHeader = lay.header >= 0
	det.NameColumn = lay.label
	if rows, label, ok := horizontalList(recs, lay); ok {
		det.NameColumn = label
		return rows, det
	}

	var rows []Row
	for _, rec := range recs[lay.header+1:] {
		// The header repeated on every printed page or in a glued list.
		if lay.header >= 0 && sameRow(rec, recs[lay.header]) {
			continue
		}
		var name string
		if lay.split {
			parts := make([]string, 0, 3)
			for _, c := range []int{lay.lastCol, lay.firstCol, lay.middleCol} {
				if v := cleanCell(cell(rec, c)); v != "" {
					parts = append(parts, v)
				}
			}
			name = strings.Join(parts, " ")
		} else {
			name = cleanCell(cell(rec, lay.nameCol))
		}
		// Names never start with list numbering or bullets ("1. Иванов",
		// "- Петров"); the number is not an external ID.
		name = stripListMarker(name)
		var ext string
		if lay.idCol >= 0 && !lay.rowNumbers {
			ext = cleanCell(cell(rec, lay.idCol))
			if lay.idLower {
				ext = strings.ToLower(ext)
			}
		}
		if name == "" && ext == "" {
			continue
		}
		rows = append(rows, Row{Line: rec.line, ExternalID: ext, FullName: name})
	}
	return rows, det
}

// horizontalList handles a single line of names copied from an e-mail or an
// Excel row ("Иванов Иван, Петров Пётр, Сидоров Алексей"): without a header
// or IDs, when every cell is a word-only name, each cell is a student.
func horizontalList(recs []record, lay layout) ([]Row, string, bool) {
	if len(recs) != 1 || lay.header >= 0 || lay.idCol >= 0 || lay.split || len(recs[0].fields) < 2 {
		return nil, "", false
	}
	var rows []Row
	for _, f := range recs[0].fields {
		v := cleanCell(f)
		if v == "" {
			continue
		}
		if strings.IndexFunc(v, unicode.IsLetter) < 0 || strings.IndexFunc(v, unicode.IsDigit) >= 0 {
			return nil, "", false
		}
		if len(rows) > MaxRows {
			break // too many anyway; callers reject more than MaxRows rows
		}
		rows = append(rows, Row{Line: recs[0].line, FullName: v})
	}
	if len(rows) < 2 {
		return nil, "", false
	}
	return rows, fmt.Sprintf("Строка %d", recs[0].line), true
}

func cell(rec record, col int) string {
	if col < 0 || col >= len(rec.fields) {
		return ""
	}
	return rec.fields[col]
}

func detectLayout(recs []record) layout {
	for i := 0; i < len(recs) && i < headerSearchRows; i++ {
		if lay, ok := headerLayout(recs, i, exactHeaderKind); ok {
			return lay
		}
	}
	// No known header: the first line may still be a label such as
	// "Список группы" or "Фамилия и имя слушателя".
	if lay, ok := headerLayout(recs, 0, looseHeaderKind); ok {
		return lay
	}
	return contentLayout(recs)
}

type headerKind int

const (
	hkNone headerKind = iota
	hkIDStrong
	hkIDEmail
	hkRowNumber
	hkFull
	hkLast
	hkFirst
	hkMiddle
)

func exactHeaderKind(c string) headerKind {
	k := headerKey(c)
	switch {
	case k == "":
		return hkNone
	case idStrongAliases[k]:
		return hkIDStrong
	case idEmailAliases[k]:
		return hkIDEmail
	case rowNumberAliases[k]:
		return hkRowNumber
	case fullNameAliases[k]:
		return hkFull
	case lastNameAliases[k]:
		return hkLast
	case firstNameAliases[k]:
		return hkFirst
	case middleNameAliases[k]:
		return hkMiddle
	}
	// "ФИО обучающегося", "Ф.И.О. (полностью)": no first name or surname is
	// the word "ФИО", while "Фиона" must not match.
	if w := strings.FieldsFunc(strings.ToLower(cleanCell(c)), func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("(),:;", r)
	}); len(w) > 1 && headerKey(w[0]) == "фио" {
		return hkFull
	}
	return hkNone
}

// looseHeaderWords are whole words that never occur in a person's name but
// often in a free-form name column label.
var looseHeaderWords = keySet("фио", "студент", "студенты", "слушатель", "слушатели", "участник", "участники",
	"обучающийся", "обучающиеся", "учащийся", "учащиеся", "список", "name", "names", "student", "students",
	"фамилия", "отчество")

func looseHeaderKind(c string) headerKind {
	if k := exactHeaderKind(c); k != hkNone {
		return k
	}
	for _, w := range strings.FieldsFunc(strings.ToLower(c), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if looseHeaderWords[headerKey(w)] {
			return hkFull
		}
	}
	return hkNone
}

// headerLayout checks whether recs[i] is a header row.
func headerLayout(recs []record, i int, kindOf func(string) headerKind) (layout, bool) {
	lay := layout{header: i, idCol: -1, nameCol: -1, lastCol: -1, firstCol: -1, middleCol: -1}
	h := recs[i].fields
	h = h[:min(len(h), maxScanCols)]
	full, emailID, rowNum, firstDigits := -1, -1, -1, -1
	for j, c := range h {
		if firstDigits < 0 && shortDigitsRe.MatchString(cleanCell(c)) {
			firstDigits = j
		}
		set := func(col *int) {
			if *col < 0 {
				*col = j
			}
		}
		switch kindOf(c) {
		case hkIDStrong:
			set(&lay.idCol)
		case hkIDEmail:
			set(&emailID)
		case hkRowNumber:
			set(&rowNum)
		case hkFull:
			set(&full)
		case hkLast:
			set(&lay.lastCol)
		case hkFirst:
			set(&lay.firstCol)
		case hkMiddle:
			set(&lay.middleCol)
		}
	}
	switch {
	case lay.idCol >= 0:
	case emailID >= 0:
		lay.idCol, lay.idLower = emailID, true
	case rowNum >= 0:
		lay.idCol, lay.rowNumbers = rowNum, true
	}
	label := func(j int) string { return cleanCell(h[j]) }
	switch {
	case full >= 0:
		lay.nameCol, lay.label = full, label(full)
	case lay.lastCol >= 0 && lay.firstCol >= 0:
		lay.split = true
		lay.label = label(lay.lastCol) + " + " + label(lay.firstCol)
		if lay.middleCol >= 0 {
			lay.label += " + " + label(lay.middleCol)
		}
	case lay.firstCol >= 0: // "Имя" alone is the whole name
		lay.nameCol, lay.label = lay.firstCol, label(lay.firstCol)
	case lay.lastCol >= 0:
		lay.nameCol, lay.label = lay.lastCol, label(lay.lastCol)
	case lay.idCol >= 0:
		// Known ID header, unknown name header ("№ | Список группы"):
		// take the first other column that holds words.
		w := width(recs)
		lay.nameCol = lettersColumn(recs[i+1:], w, lay.idCol)
		if lay.nameCol < 0 {
			lay.nameCol = firstOtherColumn(w, lay.idCol)
		}
		lay.label = columnLabel(lay.nameCol)
		if v := cleanCell(cell(recs[i], lay.nameCol)); v != "" {
			lay.label = v
		}
	default:
		return layout{}, false
	}
	// A number left of the names means a data row ("2;Петров Пётр;Студент");
	// numbers right of them are fine (lesson numbers in a journal header).
	if firstDigits >= 0 {
		left := lay.nameCol
		if lay.split {
			left = min(lay.lastCol, lay.firstCol)
		}
		if firstDigits < left {
			return layout{}, false
		}
	}
	// A value that repeats elsewhere in the same column is data (e.g. a role
	// column full of "Студент"), so this row is not a header. An exact copy
	// of the whole row is the header repeated on the next printed page.
	for _, c := range []int{lay.idCol, lay.nameCol, lay.lastCol, lay.firstCol, lay.middleCol} {
		if c < 0 {
			continue
		}
		k := headerKey(cell(recs[i], c))
		if k == "" {
			continue
		}
		for j, r := range recs {
			if j != i && headerKey(cell(r, c)) == k && !sameRow(r, recs[i]) {
				return layout{}, false
			}
		}
	}
	// A name split over columns under one (merged) header cell: "ФИО | | ".
	if !lay.split && lay.nameCol >= 0 && cleanCell(cell(recs[i], lay.nameCol+1)) == "" {
		var data []record
		for _, r := range recs[i+1:] {
			if !sameRow(r, recs[i]) {
				data = append(data, r)
			}
		}
		lay.splitName(data, 1)
		if lay.middleCol >= 0 && cleanCell(cell(recs[i], lay.middleCol)) != "" {
			lay.middleCol = -1 // a column with its own header is something else
		}
	}
	return lay, true
}

// sameRow reports whether two records hold the same cells (compared as
// header keys, so spacing and punctuation do not matter).
func sameRow(a, b record) bool {
	for j := range max(len(a.fields), len(b.fields)) {
		if headerKey(cell(a, j)) != headerKey(cell(b, j)) {
			return false
		}
	}
	return true
}

// splitName turns the name column into split columns when the name is
// written over adjacent columns without their own headers: surname | first
// name (| patronymic). Each column must hold mostly single words; the next
// columns must not be a category such as a role column full of "Студент".
// minRows is the number of data rows needed to trust the guess.
func (lay *layout) splitName(data []record, minRows int) {
	c := lay.nameCol
	last := stats(data, c)
	if last.nonEmpty < minRows || !last.mostly(last.oneWord) {
		return
	}
	nameLike := func(col int) (colStats, bool) {
		if col == lay.idCol {
			return colStats{}, false
		}
		s := stats(data, col)
		if s.nonEmpty == 0 || !s.mostly(s.oneWord) {
			return s, false
		}
		category := s.nonEmpty >= 2 && maxSame(data, col)*2 > s.nonEmpty
		return s, !category
	}
	first, ok := nameLike(c + 1)
	if !ok || first.nonEmpty*5 < last.nonEmpty*4 {
		return
	}
	lay.split, lay.lastCol, lay.firstCol, lay.middleCol = true, c, c+1, -1
	if _, ok := nameLike(c + 2); ok {
		lay.middleCol = c + 2
	}
}

// contentLayout guesses columns of a list without a header: one column is
// names; otherwise a leading column of short numbers or of "1." / "1)" is list
// numbering (dropped), and names are the first column that holds words.
// Without a header nothing is an external ID: an unlabeled column of numbers
// is far more often a row number than an identifier, and taking row numbers
// for IDs would attach a renumbered list to the wrong students.
func contentLayout(recs []record) layout {
	lay := layout{header: -1, idCol: -1, nameCol: 0, lastCol: -1, firstCol: -1, middleCol: -1}
	w := width(recs)
	if w <= 1 {
		lay.label = columnLabel(0)
		return lay
	}
	c0 := stats(recs, 0)
	switch {
	case c0.mostly(c0.numbering):
		lay.nameCol = lettersColumn(recs, w, 0)
	case c0.mostly(c0.digits):
		lay.idCol, lay.rowNumbers = 0, true
		lay.nameCol = lettersColumn(recs, w, 0)
	default:
		c1 := stats(recs, 1)
		if w == 2 && c1.mostly(c1.digits) && c0.letters*2 > c0.nonEmpty {
			lay.idCol, lay.rowNumbers, lay.nameCol = 1, true, 0
		} else {
			lay.nameCol = lettersColumn(recs, w, -1)
		}
	}
	if lay.nameCol < 0 {
		lay.nameCol = firstOtherColumn(w, lay.idCol)
	}
	lay.label = columnLabel(lay.nameCol)
	// Surname and first name copied as two columns without a header.
	if lay.splitName(recs, 2); lay.split {
		lay.label = columnLabel(lay.lastCol) + " + " + columnLabel(lay.firstCol)
		if lay.middleCol >= 0 {
			lay.label += " + " + columnLabel(lay.middleCol)
		}
	}
	return lay
}

func columnLabel(col int) string { return fmt.Sprintf("Колонка %d", col+1) }

// width is the number of columns up to the last non-empty cell in any record.
func width(recs []record) int {
	w := 0
	for _, r := range recs {
		for j := len(r.fields) - 1; j >= w; j-- {
			if cleanCell(r.fields[j]) != "" {
				w = j + 1
				break
			}
		}
	}
	return w
}

func firstOtherColumn(w, skip int) int {
	if skip == 0 && w > 1 {
		return 1
	}
	return 0
}

// maxScanCols bounds the columns searched for names: a list never keeps
// names that far right, and scanning every column of every row would let one
// very wide row make parsing quadratic.
const maxScanCols = 64

// lettersColumn returns the first column (except skip) where most non-empty
// cells contain letters, or -1.
func lettersColumn(recs []record, w, skip int) int {
	for c := range min(w, maxScanCols) {
		if c == skip {
			continue
		}
		s := stats(recs, c)
		if s.nonEmpty > 0 && s.letters*2 > s.nonEmpty {
			return c
		}
	}
	return -1
}

// colStats counts what the non-empty cells of a column look like. oneWord
// counts single words of letters ("Иванов", "Анна-Мария", "А.").
type colStats struct {
	nonEmpty, digits, numbering, letters, oneWord int
}

// mostly reports whether at least 80% of non-empty cells match.
func (s colStats) mostly(n int) bool { return s.nonEmpty > 0 && n*5 >= s.nonEmpty*4 }

var (
	shortDigitsRe = regexp.MustCompile(`^[0-9]{1,12}$`)
	numberingRe   = regexp.MustCompile(`^[0-9]{1,4}[.)]$`)
	oneWordRe     = regexp.MustCompile(`^\pL+(?:[-'’]\pL+)*\.?$`)
)

func stats(recs []record, col int) colStats {
	var s colStats
	for _, r := range recs {
		v := cleanCell(cell(r, col))
		if v == "" {
			continue
		}
		s.nonEmpty++
		switch {
		case shortDigitsRe.MatchString(v):
			s.digits++
		case numberingRe.MatchString(v):
			s.numbering++
		}
		if strings.IndexFunc(v, unicode.IsLetter) >= 0 {
			s.letters++
		}
		if oneWordRe.MatchString(v) {
			s.oneWord++
		}
	}
	return s
}

// maxSame is how often the most frequent value of a column occurs.
func maxSame(recs []record, col int) int {
	seen := map[string]int{}
	n := 0
	for _, r := range recs {
		if k := headerKey(cell(r, col)); k != "" {
			seen[k]++
			n = max(n, seen[k])
		}
	}
	return n
}

// numberedRe matches list numbering in front of a name: "1. ", "1.", "1) ",
// "12 " (Word lists that lost their dot). Digits are never an external ID here.
var numberedRe = regexp.MustCompile(`^[0-9]{1,4}(?:[.)] ?| )(\pL.*)$`)

func isBullet(r rune) bool {
	return strings.ContainsRune("-–—•·*▪●○◦‣⁃►→✓", r)
}

// stripListMarker removes one leading bullet or list number from a name.
func stripListMarker(s string) string {
	if m := numberedRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	if r, size := utf8.DecodeRuneInString(s); isBullet(r) {
		rest := strings.TrimLeft(s[size:], " ")
		if f, _ := utf8.DecodeRuneInString(rest); unicode.IsLetter(f) {
			return rest
		}
	}
	return s
}
