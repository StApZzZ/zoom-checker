package importer

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Inputs that real users produce and that once went wrong: stray quotes,
// headers repeated on every printed page, names split over columns without
// their own headers, broken bytes, web pages saved as ".xls".
func TestParse_MessyFiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		filename string
		data     string
		want     []Row
		label    string
		header   bool
	}{
		{
			name:     "unterminated quote does not swallow the following lines",
			filename: "a.csv",
			data:     "1;\"Иванов Иван\n2;Петров Пётр\n3;Сидоров Алексей\n4;Орлов Олег\n",
			want:     []Row{r(1, "", "Иванов Иван"), r(2, "", "Петров Пётр"), r(3, "", "Сидоров Алексей"), r(4, "", "Орлов Олег")},
			label:    "Колонка 2",
		},
		{
			name:     "stray quote in the first column",
			filename: "a.csv",
			data:     "ФИО;Код\n\"Иванов Иван;1\nПетров Пётр;2\n",
			want:     []Row{r(2, "1", "Иванов Иван"), r(3, "2", "Петров Пётр")},
			label:    "ФИО",
			header:   true,
		},
		{
			name:     "a real multi-line cell (Alt+Enter) is kept",
			filename: "a.csv",
			data:     "ФИО;Комментарий\nИванов Иван;\"первая строка\nвторая строка\"\nПетров Пётр;\n",
			want:     []Row{r(2, "", "Иванов Иван"), r(4, "", "Петров Пётр")},
			label:    "ФИО",
			header:   true,
		},
		{
			name:     "header repeated on every printed page",
			filename: "a.csv",
			data:     "№;ФИО\n1;Иванов Иван\n2;Петров Пётр\n№;ФИО\n3;Сидоров Алексей\n",
			want:     []Row{r(2, "", "Иванов Иван"), r(3, "", "Петров Пётр"), r(5, "", "Сидоров Алексей")},
			label:    "ФИО",
			header:   true,
		},
		{
			name:     "surname and first name in two columns without a header",
			filename: "a.csv",
			data:     "Иванов;Иван\nПетров;Пётр\nСидоров;Алексей\n",
			want:     []Row{r(1, "", "Иванов Иван"), r(2, "", "Петров Пётр"), r(3, "", "Сидоров Алексей")},
			label:    "Колонка 1 + Колонка 2",
		},
		{
			name:     "row number, surname, first name, patronymic without a header",
			filename: "a.txt",
			data:     "1\tИванов\tИван\tИванович\n2\tПетрова\tАнна\t\n3\tОрлов\tОлег\tОлегович\n",
			want:     []Row{r(1, "", "Иванов Иван Иванович"), r(2, "", "Петрова Анна"), r(3, "", "Орлов Олег Олегович")},
			label:    "Колонка 2 + Колонка 3 + Колонка 4",
		},
		{
			name:     "a role column is not a first name",
			filename: "a.csv",
			data:     "Иванов;Студент\nПетров;Студент\nСидоров;Староста\n",
			want:     []Row{r(1, "", "Иванов"), r(2, "", "Петров"), r(3, "", "Сидоров")},
			label:    "Колонка 1",
		},
		{
			name:     "name split under one merged ФИО header cell",
			filename: "a.csv",
			data:     "№;ФИО;;;Группа\n1;Иванов;Иван;Иванович;ДО-21\n2;Петрова;Анна;;ДО-21\n",
			want:     []Row{r(2, "", "Иванов Иван Иванович"), r(3, "", "Петрова Анна")},
			label:    "ФИО",
			header:   true,
		},
		{
			name:     "a column with its own header is not a patronymic",
			filename: "a.csv",
			data:     "ФИО;;Город\nИванов;Иван;Москва\nПетров;Пётр;Тверь\n",
			want:     []Row{r(2, "", "Иванов Иван"), r(3, "", "Петров Пётр")},
			label:    "ФИО",
			header:   true,
		},
		{
			name:     "«ФИО обучающегося» under a title row, e-mail is the ID (lower-cased)",
			filename: "a.csv",
			data:     "Список группы ДО-21;\nФИО обучающегося;Email\nИванов Иван;I@X.ru\n",
			want:     []Row{r(3, "i@x.ru", "Иванов Иван")},
			label:    "ФИО обучающегося",
			header:   true,
		},
		{
			name:     "a student called Фиона is not a header",
			filename: "a.txt",
			data:     "Фиона Смит\nИванов Иван\n",
			want:     []Row{r(1, "", "Фиона Смит"), r(2, "", "Иванов Иван")},
			label:    "Колонка 1",
		},
		{
			name:     "UTF-8 with one broken byte stays UTF-8",
			filename: "a.csv",
			data:     "ФИО\nИванов Иван\nПетров Пётр\xff\n",
			want:     []Row{r(2, "", "Иванов Иван"), r(3, "", "Петров Пётр�")},
			label:    "ФИО",
			header:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rows, det, err := Parse(tt.filename, []byte(tt.data))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !reflect.DeepEqual(rows, tt.want) {
				t.Errorf("rows:\n got %+v\nwant %+v", rows, tt.want)
			}
			if det.NameColumn != tt.label || det.HasHeader != tt.header {
				t.Errorf("detected %+v, want name column %q header %v", det, tt.label, tt.header)
			}
		})
	}
}

func TestParse_DocumentsAreRejected(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, filename, data, msg string
	}{
		{"HTML saved as .xls", "report.xls", "<html>\n<body><table><tr><td>Иванов Иван</td></tr></table></body>\n</html>\n", msgMarkup},
		{"XML spreadsheet 2003", "report.xls", "<?xml version=\"1.0\"?>\n<Workbook xmlns=\"urn:schemas-microsoft-com:office:spreadsheet\">\n", msgMarkup},
		{"HTML with leading blank lines and BOM", "a.xls", "\xEF\xBB\xBF\r\n  <!DOCTYPE html><html></html>", msgMarkup},
		{"RTF", "list.rtf", "{\\rtf1\\ansi Иванов Иван\\par}\n", msgWord},
		{"legacy .doc", "list.doc", "\xD0\xCF\x11\xE0\xA1\xB1\x1A\xE1" + string(make([]byte, 64)), msgWord},
		{".docx", "list.docx", "PK\x03\x04garbage", msgWord},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := Parse(tt.filename, []byte(tt.data))
			if !errors.Is(err, ErrUnsupportedFormat) {
				t.Fatalf("err = %v, want ErrUnsupportedFormat", err)
			}
			if UserMessage(err) != tt.msg {
				t.Errorf("message = %q, want %q", UserMessage(err), tt.msg)
			}
		})
	}
	// A list that merely starts with "<" is still a list.
	rows, _, err := Parse("a.txt", []byte("<Иванов Иван>\nПетров Пётр\n"))
	if err != nil || len(rows) != 2 {
		t.Errorf("rows=%+v err=%v", rows, err)
	}
}

func TestMostlyUTF8(t *testing.T) {
	t.Parallel()
	if !mostlyUTF8([]byte("Иванов Иван\xff")) {
		t.Error("UTF-8 with one broken byte must stay UTF-8")
	}
	if mostlyUTF8(cp1251(t, "Иванов Иван\nПетров Пётр\nФЁДОРОВ ЁЖИК №5 «ок»\n")) {
		t.Error("windows-1251 text must not be taken for UTF-8")
	}
	if mostlyUTF8([]byte("abc\xff")) {
		t.Error("ASCII with a broken byte has no UTF-8 evidence")
	}
}

// Crafted uploads must not hang the server: one very wide row among many
// rows without letters used to make column guessing O(width × rows), and a
// 10 MB file of short lines allocated gigabytes before being rejected.
func TestParse_HostileSizes(t *testing.T) {
	t.Parallel()
	run := func(name string, data []byte, want error) {
		t.Helper()
		done := make(chan error, 1)
		go func() {
			_, _, err := Parse(name, data)
			done <- err
		}()
		select {
		case err := <-done:
			if want != nil && !errors.Is(err, want) {
				t.Errorf("%s: err = %v, want %v", name, err, want)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("%s: Parse did not finish in 20s", name)
		}
	}
	wide := strings.Repeat("1,", 300_000) + "\n" + strings.Repeat("1,2\n", 9_000)
	run("wide.csv", []byte(wide), nil)
	run("lines.txt", []byte(strings.Repeat("a\n", 500_000)), ErrTooManyRows)
	run("mac.txt", []byte(strings.Repeat("a\r", 500_000)), ErrTooManyRows)
	run("one-line.txt", []byte(strings.Repeat("Иван,", 900_000)), ErrTooManyRows)
}

func TestNonBlankLines(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]int{"": 0, "a": 1, "a\nb": 2, "a\r\nb\r\n": 2, "a\rb\r": 2, " \n\t\r\n a \n": 1} {
		if got := nonBlankLines(in); got != want {
			t.Errorf("nonBlankLines(%q) = %d, want %d", in, got, want)
		}
	}
}
