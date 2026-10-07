package importer

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"
	xunicode "golang.org/x/text/encoding/unicode"
)

func cp1251(t *testing.T, s string) []byte {
	t.Helper()
	b, err := charmap.Windows1251.NewEncoder().Bytes([]byte(s))
	if err != nil {
		t.Fatalf("encode cp1251: %v", err)
	}
	return b
}

func utf16(t *testing.T, s string, order xunicode.Endianness, bom bool) []byte {
	t.Helper()
	policy := xunicode.IgnoreBOM
	if bom {
		policy = xunicode.UseBOM
	}
	b, err := xunicode.UTF16(order, policy).NewEncoder().Bytes([]byte(s))
	if err != nil {
		t.Fatalf("encode utf16: %v", err)
	}
	return b
}

// r is a compact Row constructor for expectations.
func r(line int, ext, name string) Row { return Row{Line: line, ExternalID: ext, FullName: name} }

func TestParseText_Files(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		filename string
		data     func(t *testing.T) []byte
		want     []Row
		det      Detected
	}{
		{
			name:     "TZ example, comma, english header",
			filename: "group.csv",
			data: func(*testing.T) []byte {
				return []byte("student_id,name\n1,Иванов Иван\n2,Петров Пётр\n3,Сидоров Алексей\n")
			},
			want: []Row{r(2, "1", "Иванов Иван"), r(3, "2", "Петров Пётр"), r(4, "3", "Сидоров Алексей")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF8, Delimiter: ",", NameColumn: "name", HasHeader: true},
		},
		{
			name:     "Russian Excel CSV: BOM, semicolon, CRLF, russian header",
			filename: "Список.csv",
			data: func(*testing.T) []byte {
				return []byte("\xEF\xBB\xBFID студента;ФИО;Почта\r\n17;Иванов  Иван;i@x.ru\r\n18;  Петрова Анна ;\r\n")
			},
			want: []Row{r(2, "17", "Иванов Иван"), r(3, "18", "Петрова Анна")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF8, Delimiter: ";", NameColumn: "ФИО", HasHeader: true},
		},
		{
			name:     "tab separated with № column: row numbers are not IDs",
			filename: "list.txt",
			data: func(*testing.T) []byte {
				return []byte("№\tФ.И.О.\n1\tИванов Иван\n2\tПетров Пётр\n")
			},
			want: []Row{r(2, "", "Иванов Иван"), r(3, "", "Петров Пётр")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF8, Delimiter: "\t", NameColumn: "Ф.И.О.", HasHeader: true},
		},
		{
			name:     "quoted fields with delimiters and quotes inside",
			filename: "q.csv",
			data: func(*testing.T) []byte {
				return []byte("id,full_name\n1,\"Иванов, Иван\"\n2,\"Петров \"\"Пётр\"\"\"\n3, \"Сидоров Алексей\"\n")
			},
			want: []Row{r(2, "1", "Иванов, Иван"), r(3, "2", `Петров "Пётр"`), r(4, "3", "Сидоров Алексей")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF8, Delimiter: ",", NameColumn: "full_name", HasHeader: true},
		},
		{
			name:     "windows-1251 bytes",
			filename: "group.csv",
			data: func(t *testing.T) []byte {
				return cp1251(t, "ФИО;Табельный номер\r\nИванов Иван;0012\r\nЁлкина Алёна;0013\r\n")
			},
			want: []Row{r(2, "0012", "Иванов Иван"), r(3, "0013", "Ёлкина Алёна")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingWindows1251, Delimiter: ";", NameColumn: "ФИО", HasHeader: true},
		},
		{
			name:     "windows-1251 single column without header",
			filename: "names.txt",
			data: func(t *testing.T) []byte {
				return cp1251(t, "Иванов Иван\r\nПетров Пётр\r\n")
			},
			want: []Row{r(1, "", "Иванов Иван"), r(2, "", "Петров Пётр")},
			det:  Detected{Format: FormatText, Encoding: EncodingWindows1251, NameColumn: "Колонка 1"},
		},
		{
			name:     "Excel Unicode text: UTF-16LE with BOM, tabs",
			filename: "export.txt",
			data: func(t *testing.T) []byte {
				return utf16(t, "Студент\tКод\r\nИванов Иван\t5\r\nПетров Пётр\t6\r\n", xunicode.LittleEndian, true)
			},
			want: []Row{r(2, "5", "Иванов Иван"), r(3, "6", "Петров Пётр")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF16LE, Delimiter: "\t", NameColumn: "Студент", HasHeader: true},
		},
		{
			name:     "UTF-16LE without BOM detected by NUL pattern",
			filename: "export.txt",
			data: func(t *testing.T) []byte {
				return utf16(t, "Иванов Иван\nПетров Пётр\n", xunicode.LittleEndian, false)
			},
			want: []Row{r(1, "", "Иванов Иван"), r(2, "", "Петров Пётр")},
			det:  Detected{Format: FormatText, Encoding: EncodingUTF16LE, NameColumn: "Колонка 1"},
		},
		{
			name:     "UTF-16BE with BOM",
			filename: "export.txt",
			data: func(t *testing.T) []byte {
				return utf16(t, "1;Иванов Иван\n2;Петров Пётр\n", xunicode.BigEndian, true)
			},
			want: []Row{r(1, "", "Иванов Иван"), r(2, "", "Петров Пётр")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF16BE, Delimiter: ";", NameColumn: "Колонка 2"},
		},
		{
			name:     "separate Фамилия / Имя / Отчество columns",
			filename: "fio.csv",
			data: func(*testing.T) []byte {
				return []byte("Фамилия;Имя;Отчество;Код\nИванов;Иван;Иванович;77\nПетрова;Анна;;78\n")
			},
			want: []Row{r(2, "77", "Иванов Иван Иванович"), r(3, "78", "Петрова Анна")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF8, Delimiter: ";", NameColumn: "Фамилия + Имя + Отчество", HasHeader: true},
		},
		{
			name:     "english split header",
			filename: "en.csv",
			data: func(*testing.T) []byte {
				return []byte("First Name,Last Name,Student ID\nJohn,Smith,5\n")
			},
			want: []Row{r(2, "5", "Smith John")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF8, Delimiter: ",", NameColumn: "Last Name + First Name", HasHeader: true},
		},
		{
			name:     "Имя alone is the full name",
			filename: "a.csv",
			data: func(*testing.T) []byte {
				return []byte("Имя\nИванов Иван\nПетров Пётр\n")
			},
			want: []Row{r(2, "", "Иванов Иван"), r(3, "", "Петров Пётр")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF8, NameColumn: "Имя", HasHeader: true},
		},
		{
			name:     "ФИО column wins over Имя",
			filename: "a.csv",
			data: func(*testing.T) []byte {
				return []byte("Имя;ФИО\nИван;Иванов Иван\n")
			},
			want: []Row{r(2, "", "Иванов Иван")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF8, Delimiter: ";", NameColumn: "ФИО", HasHeader: true},
		},
		{
			name:     "no header, one column, blank lines and messy spaces",
			filename: "names.txt",
			data: func(*testing.T) []byte {
				return []byte("Иванов Иван\n\n   \n  Сидоров \u00a0 Алексей  \n\"Петров Пётр\"\n")
			},
			want: []Row{r(1, "", "Иванов Иван"), r(4, "", "Сидоров Алексей"), r(5, "", "Петров Пётр")},
			det:  Detected{Format: FormatText, Encoding: EncodingUTF8, NameColumn: "Колонка 1"},
		},
		{
			name:     "no header, row number then name",
			filename: "x.csv",
			data: func(*testing.T) []byte {
				return []byte("1;Иванов Иван\n2;Петров Пётр\n")
			},
			want: []Row{r(1, "", "Иванов Иван"), r(2, "", "Петров Пётр")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF8, Delimiter: ";", NameColumn: "Колонка 2"},
		},
		{
			name:     "no header, an unlabeled number column is not an ID",
			filename: "x.csv",
			data: func(*testing.T) []byte {
				return []byte("Иванов Иван;101\nПетров Пётр;102\n")
			},
			want: []Row{r(1, "", "Иванов Иван"), r(2, "", "Петров Пётр")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF8, Delimiter: ";", NameColumn: "Колонка 1"},
		},
		{
			name:     "no header, role column full of «Студент» is data, not a header",
			filename: "x.csv",
			data: func(*testing.T) []byte {
				return []byte("Иванов Иван;Студент\nПетров Пётр;Студент\n")
			},
			want: []Row{r(1, "", "Иванов Иван"), r(2, "", "Петров Пётр")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF8, Delimiter: ";", NameColumn: "Колонка 1"},
		},
		{
			name:     "title rows above the header are skipped",
			filename: "x.csv",
			data: func(*testing.T) []byte {
				return []byte("Список группы DevOps-1;;\n;;\nФИО;ID;Примечание\nИванов Иван;1;староста\n")
			},
			want: []Row{r(4, "1", "Иванов Иван")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF8, Delimiter: ";", NameColumn: "ФИО", HasHeader: true},
		},
		{
			name:     "pre-numbered blank rows under № п/п are skipped",
			filename: "x.csv",
			data: func(*testing.T) []byte {
				return []byte("№ п/п;ФИО\n1;Иванов Иван\n2;\n3;\n")
			},
			want: []Row{r(2, "", "Иванов Иван")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF8, Delimiter: ";", NameColumn: "ФИО", HasHeader: true},
		},
		{
			name:     "real ID with empty name is kept for validation",
			filename: "x.csv",
			data: func(*testing.T) []byte {
				return []byte("student_id;name\n5;\n6;Петров Пётр\n")
			},
			want: []Row{r(2, "5", ""), r(3, "6", "Петров Пётр")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF8, Delimiter: ";", NameColumn: "name", HasHeader: true},
		},
		{
			name:     "sep= hint line",
			filename: "x.csv",
			data: func(*testing.T) []byte {
				return []byte("sep=,\nid,name\n1,Иванов Иван\n")
			},
			want: []Row{r(3, "1", "Иванов Иван")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF8, Delimiter: ",", NameColumn: "name", HasHeader: true},
		},
		{
			name:     "pipe delimiter",
			filename: "x.txt",
			data: func(*testing.T) []byte {
				return []byte("id|name\n1|Иванов Иван\n")
			},
			want: []Row{r(2, "1", "Иванов Иван")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF8, Delimiter: "|", NameColumn: "name", HasHeader: true},
		},
		{
			name:     "semicolon wins over decimal commas",
			filename: "x.csv",
			data: func(*testing.T) []byte {
				return []byte("1;Иванов Иван;4,5\n2;Петров Пётр;3,7\n")
			},
			want: []Row{r(1, "", "Иванов Иван"), r(2, "", "Петров Пётр")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF8, Delimiter: ";", NameColumn: "Колонка 2"},
		},
		{
			name:     "a comma in a few names does not make a delimiter",
			filename: "x.txt",
			data: func(*testing.T) []byte {
				return []byte("Иванов Иван\nПетров, Пётр\nСидоров Алексей\nКузнецова Мария\nОрлов Олег\n")
			},
			want: []Row{r(1, "", "Иванов Иван"), r(2, "", "Петров, Пётр"), r(3, "", "Сидоров Алексей"), r(4, "", "Кузнецова Мария"), r(5, "", "Орлов Олег")},
			det:  Detected{Format: FormatText, Encoding: EncodingUTF8, NameColumn: "Колонка 1"},
		},
		{
			name:     "numbered and bulleted list in a text file",
			filename: "list.txt",
			data: func(*testing.T) []byte {
				return []byte("1. Иванов Иван\n2) Петров Пётр\n- Сидоров Алексей\n• Smith John\n10.Орлов Олег\n")
			},
			want: []Row{r(1, "", "Иванов Иван"), r(2, "", "Петров Пётр"), r(3, "", "Сидоров Алексей"), r(4, "", "Smith John"), r(5, "", "Орлов Олег")},
			det:  Detected{Format: FormatText, Encoding: EncodingUTF8, NameColumn: "Колонка 1"},
		},
		{
			name:     "free-form first line is a header",
			filename: "list.txt",
			data: func(*testing.T) []byte {
				return []byte("Список студентов группы ДО-21\nИванов Иван\nПетров Пётр\n")
			},
			want: []Row{r(2, "", "Иванов Иван"), r(3, "", "Петров Пётр")},
			det:  Detected{Format: FormatText, Encoding: EncodingUTF8, NameColumn: "Список студентов группы ДО-21", HasHeader: true},
		},
		{
			name:     "invisible characters and NBSP are cleaned, case kept",
			filename: "list.txt",
			data: func(*testing.T) []byte {
				return []byte("\u200bиВАНОВ\u00a0Иван\u200d\n")
			},
			want: []Row{r(1, "", "иВАНОВ Иван")},
			det:  Detected{Format: FormatText, Encoding: EncodingUTF8, NameColumn: "Колонка 1"},
		},
		{
			name:     "csv extension without delimiter is still csv",
			filename: "names.CSV",
			data: func(*testing.T) []byte {
				return []byte("ФИО\nИванов Иван\n")
			},
			want: []Row{r(2, "", "Иванов Иван")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF8, NameColumn: "ФИО", HasHeader: true},
		},
		{
			name:     "ID header with an unknown name header",
			filename: "x.csv",
			data: func(*testing.T) []byte {
				return []byte("Код;Полное ФИО слушателя курса\n1;Иванов Иван\n2;- Петров Пётр\n")
			},
			want: []Row{r(2, "1", "Иванов Иван"), r(3, "2", "Петров Пётр")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF8, Delimiter: ";", NameColumn: "Полное ФИО слушателя курса", HasHeader: true},
		},
		{
			name:     "ID header, no column with words",
			filename: "x.csv",
			data: func(*testing.T) []byte {
				return []byte("ID;Data\n5;777\n")
			},
			want: []Row{r(2, "5", "777")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF8, Delimiter: ";", NameColumn: "Data", HasHeader: true},
		},
		{
			name:     "journal header with lesson numbers right of the names",
			filename: "journal.csv",
			data: func(*testing.T) []byte {
				return []byte("№;ФИО;1;2;3\n1;Иванов Иван;+;+;-\n2;Петров Пётр;+;;+\n")
			},
			want: []Row{r(2, "", "Иванов Иван"), r(3, "", "Петров Пётр")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF8, Delimiter: ";", NameColumn: "ФИО", HasHeader: true},
		},
		{
			name:     "a lone «Студент» in a data row is not a header",
			filename: "x.csv",
			data: func(*testing.T) []byte {
				return []byte("1;Иванов Иван;Староста\n2;Петров Пётр;Студент\n3;Сидоров Алексей;\n")
			},
			want: []Row{r(1, "", "Иванов Иван"), r(2, "", "Петров Пётр"), r(3, "", "Сидоров Алексей")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF8, Delimiter: ";", NameColumn: "Колонка 2"},
		},
		{
			name:     "old Mac line endings",
			filename: "x.csv",
			data: func(*testing.T) []byte {
				return []byte("id;name\r1;Иванов Иван\r2;Петров Пётр")
			},
			want: []Row{r(2, "1", "Иванов Иван"), r(3, "2", "Петров Пётр")},
			det:  Detected{Format: FormatCSV, Encoding: EncodingUTF8, Delimiter: ";", NameColumn: "name", HasHeader: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rows, det, err := Parse(tt.filename, tt.data(t))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !reflect.DeepEqual(rows, tt.want) {
				t.Errorf("rows:\n got %+v\nwant %+v", rows, tt.want)
			}
			if det != tt.det {
				t.Errorf("detected:\n got %+v\nwant %+v", det, tt.det)
			}
		})
	}
}

func TestParse_Errors(t *testing.T) {
	t.Parallel()
	many := &bytes.Buffer{}
	many.WriteString("ФИО\n")
	for i := range MaxRows + 1 {
		fmt.Fprintf(many, "Студентов Студент %d\n", i)
	}
	tests := []struct {
		name     string
		filename string
		data     []byte
		kind     error
	}{
		{"empty", "a.csv", nil, ErrEmptyFile},
		{"only whitespace", "a.csv", []byte(" \r\n\t\n"), ErrEmptyFile},
		{"only a header", "a.csv", []byte("student_id;ФИО\n"), ErrEmptyFile},
		{"binary with NUL bytes", "photo.csv", []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x01\x00\x00\x00\x01\x00\x08\x06\x00\x00\x00\x5c\x72\xa8\x66\x00\x00\x00"), ErrUnreadable},
		{"control characters", "a.txt", []byte("\x01\x02\x03\x04\x05\x06\x07\x08 abc"), ErrUnreadable},
		{"legacy xls", "old.xls", append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, make([]byte, 64)...), ErrUnsupportedFormat},
		{"pdf", "list.pdf", []byte("%PDF-1.7\n..."), ErrUnsupportedFormat},
		{"xlsx extension but not a zip", "list.xlsx", []byte("Иванов Иван"), ErrUnreadable},
		{"zip that is not a workbook", "list.zip", []byte("PK\x03\x04garbage"), ErrUnreadable},
		{"too large", "a.csv", bytes.Repeat([]byte("a"), MaxFileSize+1), ErrTooLarge},
		{"too many rows", "a.csv", many.Bytes(), ErrTooManyRows},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rows, _, err := Parse(tt.filename, tt.data)
			if err == nil {
				t.Fatalf("Parse: want error %v, got %d rows", tt.kind, len(rows))
			}
			if !errors.Is(err, tt.kind) {
				t.Fatalf("Parse: error %v is not %v", err, tt.kind)
			}
			var fe *FileError
			if !errors.As(err, &fe) {
				t.Fatalf("error %T is not *FileError", err)
			}
			msg := UserMessage(err)
			if msg == "" || msg != err.Error() || !containsCyrillic(msg) {
				t.Errorf("UserMessage = %q, want a Russian message equal to Error()", msg)
			}
		})
	}
}

func containsCyrillic(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r >= 'А' && r <= 'я' })
}

func TestUserMessage_Generic(t *testing.T) {
	t.Parallel()
	if got := UserMessage(errors.New("boom")); got != msgUnreadable {
		t.Errorf("UserMessage(other) = %q", got)
	}
}

func TestParseText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		text  string
		want  []Row
		delim string
		label string
	}{
		{"comma row number", "1, Иванов Иван\n2, Петров Пётр", []Row{r(1, "", "Иванов Иван"), r(2, "", "Петров Пётр")}, ",", "Колонка 2"},
		{"semicolon row number", "1;Иванов Иван\n2;Петров Пётр\n", []Row{r(1, "", "Иванов Иван"), r(2, "", "Петров Пётр")}, ";", "Колонка 2"},
		{"tab row number", "1\tИванов Иван\n2\tПетров Пётр", []Row{r(1, "", "Иванов Иван"), r(2, "", "Петров Пётр")}, "\t", "Колонка 2"},
		{"single line with a number", "7, Иванов Иван", []Row{r(1, "", "Иванов Иван")}, ",", "Колонка 2"},
		{"pasted Excel cells with an ID header", "ID\tФИО\n17\tИванов Иван\n18\tПетров Пётр\n", []Row{r(2, "17", "Иванов Иван"), r(3, "18", "Петров Пётр")}, "\t", "ФИО"},
		{"numbered with dot", "1. Иванов Иван\n2. Петров Пётр\n", []Row{r(1, "", "Иванов Иван"), r(2, "", "Петров Пётр")}, "", "Колонка 1"},
		{"numbered with paren", "1) Иванов Иван\n2) Петров Пётр\n", []Row{r(1, "", "Иванов Иван"), r(2, "", "Петров Пётр")}, "", "Колонка 1"},
		{"numbered without dot", "9 Иванов Иван\n10 Петров Пётр\n", []Row{r(1, "", "Иванов Иван"), r(2, "", "Петров Пётр")}, "", "Колонка 1"},
		{"word list numbering with tab", "1.\tИванов Иван\n2.\tПетров Пётр\n", []Row{r(1, "", "Иванов Иван"), r(2, "", "Петров Пётр")}, "\t", "Колонка 2"},
		{"bullets", "- Иванов Иван\n– Петров Пётр\n* Сидоров Алексей\n", []Row{r(1, "", "Иванов Иван"), r(2, "", "Петров Пётр"), r(3, "", "Сидоров Алексей")}, "", "Колонка 1"},
		{"plain names, CRLF, blank lines", "Иванов Иван\r\n\r\nПетров Пётр\r\n", []Row{r(1, "", "Иванов Иван"), r(3, "", "Петров Пётр")}, "", "Колонка 1"},
		{"pasted Excel cells with header", "ФИО\tТабельный номер\tГруппа\nИванов Иван\t0012\tA\nПетров Пётр\t0013\tA\n", []Row{r(2, "0012", "Иванов Иван"), r(3, "0013", "Петров Пётр")}, "\t", "ФИО"},
		{"pasted Excel cells, split name with empty patronymic", "Фамилия\tИмя\tОтчество\nИванов\tИван\t\nПетрова\tАнна\tСергеевна\n", []Row{r(2, "", "Иванов Иван"), r(3, "", "Петрова Анна Сергеевна")}, "\t", "Фамилия + Имя + Отчество"},
		{"leading BOM", "\uFEFFИванов Иван", []Row{r(1, "", "Иванов Иван")}, "", "Колонка 1"},
		{"hyphenated name is not a bullet", "Петров-Водкин Кузьма", []Row{r(1, "", "Петров-Водкин Кузьма")}, "", "Колонка 1"},
		{"horizontal list from an e-mail", "Иванов Иван, Петров Пётр,  Сидоров Алексей", []Row{r(1, "", "Иванов Иван"), r(1, "", "Петров Пётр"), r(1, "", "Сидоров Алексей")}, ",", "Строка 1"},
		{"single line name and number stays one student", "Иванов Иван;101", []Row{r(1, "", "Иванов Иван")}, ";", "Колонка 1"},
		{"empty", "  \n\n", nil, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rows, det := ParseText(tt.text)
			if !reflect.DeepEqual(rows, tt.want) {
				t.Errorf("rows:\n got %+v\nwant %+v", rows, tt.want)
			}
			if det.Format != FormatText || det.Encoding != EncodingUTF8 {
				t.Errorf("format/encoding = %q/%q", det.Format, det.Encoding)
			}
			if det.Delimiter != tt.delim {
				t.Errorf("delimiter = %q, want %q", det.Delimiter, tt.delim)
			}
			if det.NameColumn != tt.label {
				t.Errorf("name column = %q, want %q", det.NameColumn, tt.label)
			}
		})
	}
}

func TestStripListMarker(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"1. Иванов":      "Иванов",
		"1.Иванов":       "Иванов",
		"12) Иванов":     "Иванов",
		"3 Иванов":       "Иванов",
		"• Иванов":       "Иванов",
		"—Иванов":        "Иванов",
		"Иванов":         "Иванов",
		"1.5 Иванов":     "1.5 Иванов",
		"12345678 Ivan":  "12345678 Ivan",
		"- 42":           "- 42",
		"":               "",
		"Иванов 1. Иван": "Иванов 1. Иван",
	}
	for in, want := range tests {
		if got := stripListMarker(in); got != want {
			t.Errorf("stripListMarker(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGuessUTF16(t *testing.T) {
	t.Parallel()
	le := utf16(t, "Иванов Иван\n", xunicode.LittleEndian, false)
	be := utf16(t, "Иванов Иван\n", xunicode.BigEndian, false)
	if got := guessUTF16(le); got != EncodingUTF16LE {
		t.Errorf("LE guessed as %q", got)
	}
	if got := guessUTF16(be); got != EncodingUTF16BE {
		t.Errorf("BE guessed as %q", got)
	}
	if got := guessUTF16([]byte("Иванов Иван\n")); got != "" {
		t.Errorf("UTF-8 guessed as %q", got)
	}
}
