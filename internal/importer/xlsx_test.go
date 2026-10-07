package importer

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/xuri/excelize/v2"
)

func buildXLSX(t *testing.T, fill func(f *excelize.File)) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	fill(f)
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("write xlsx: %v", err)
	}
	return buf.Bytes()
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestParseXLSX(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		filename string
		fill     func(t *testing.T, f *excelize.File)
		want     []Row
		det      Detected
	}{
		{
			name:     "title, header, gaps, numeric and text IDs, empty first sheet",
			filename: "Группа.xlsx",
			fill: func(t *testing.T, f *excelize.File) {
				must(t, f.SetSheetName("Sheet1", "Пусто"))
				_, err := f.NewSheet("Группа")
				must(t, err)
				must(t, f.SetCellValue("Группа", "A1", "Список группы DevOps-1"))
				must(t, f.SetSheetRow("Группа", "A3", &[]any{"№ п/п", "ФИО", "Табельный номер"}))
				must(t, f.SetSheetRow("Группа", "A4", &[]any{1, "Иванов  Иван", "007"}))
				must(t, f.SetSheetRow("Группа", "A5", &[]any{2, "Петров Пётр", 12345678901234}))
				must(t, f.SetSheetRow("Группа", "A7", &[]any{3, " Сидоров Алексей ", 42}))
			},
			want: []Row{r(4, "007", "Иванов Иван"), r(5, "12345678901234", "Петров Пётр"), r(7, "42", "Сидоров Алексей")},
			det:  Detected{Format: FormatXLSX, Encoding: EncodingUTF8, NameColumn: "ФИО", HasHeader: true},
		},
		{
			name:     "hidden sheet is used only after visible ones",
			filename: "a.xlsx",
			fill: func(t *testing.T, f *excelize.File) {
				must(t, f.SetCellValue("Sheet1", "A1", "Скрытый Лист"))
				_, err := f.NewSheet("Список")
				must(t, err)
				must(t, f.SetCellValue("Список", "A1", "Иванов Иван"))
				must(t, f.SetCellValue("Список", "A2", "Петров Пётр"))
				f.SetActiveSheet(1)
				must(t, f.SetSheetVisible("Sheet1", false))
			},
			want: []Row{r(1, "", "Иванов Иван"), r(2, "", "Петров Пётр")},
			det:  Detected{Format: FormatXLSX, Encoding: EncodingUTF8, NameColumn: "Колонка 1"},
		},
		{
			name:     "no header: row number and name",
			filename: "a.xlsx",
			fill: func(t *testing.T, f *excelize.File) {
				must(t, f.SetSheetRow("Sheet1", "A1", &[]any{1, "Иванов Иван"}))
				must(t, f.SetSheetRow("Sheet1", "A2", &[]any{2, "Петров Пётр"}))
			},
			want: []Row{r(1, "", "Иванов Иван"), r(2, "", "Петров Пётр")},
			det:  Detected{Format: FormatXLSX, Encoding: EncodingUTF8, NameColumn: "Колонка 2"},
		},
		{
			name:     "typed list numbering in cells is stripped",
			filename: "a.xlsx",
			fill: func(t *testing.T, f *excelize.File) {
				must(t, f.SetCellValue("Sheet1", "A1", "1. Иванов Иван"))
				must(t, f.SetCellValue("Sheet1", "A2", "2) Петров Пётр"))
			},
			want: []Row{r(1, "", "Иванов Иван"), r(2, "", "Петров Пётр")},
			det:  Detected{Format: FormatXLSX, Encoding: EncodingUTF8, NameColumn: "Колонка 1"},
		},
		{
			name:     "split name columns",
			filename: "a.xlsx",
			fill: func(t *testing.T, f *excelize.File) {
				must(t, f.SetSheetRow("Sheet1", "A1", &[]any{"Фамилия", "Имя", "Отчество"}))
				must(t, f.SetSheetRow("Sheet1", "A2", &[]any{"Иванов", "Иван", "Иванович"}))
			},
			want: []Row{r(2, "", "Иванов Иван Иванович")},
			det:  Detected{Format: FormatXLSX, Encoding: EncodingUTF8, NameColumn: "Фамилия + Имя + Отчество", HasHeader: true},
		},
		{
			name:     "recognized by zip magic despite a wrong extension",
			filename: "list.csv",
			fill: func(t *testing.T, f *excelize.File) {
				must(t, f.SetCellValue("Sheet1", "B2", "Иванов Иван"))
			},
			want: []Row{r(2, "", "Иванов Иван")},
			det:  Detected{Format: FormatXLSX, Encoding: EncodingUTF8, NameColumn: "Колонка 2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data := buildXLSX(t, func(f *excelize.File) { tt.fill(t, f) })
			rows, det, err := Parse(tt.filename, data)
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

func TestParseXLSX_Errors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		fill func(t *testing.T, f *excelize.File)
		kind error
	}{
		{"empty workbook", func(*testing.T, *excelize.File) {}, ErrEmptyFile},
		{"header only", func(t *testing.T, f *excelize.File) {
			must(t, f.SetSheetRow("Sheet1", "A1", &[]any{"ID", "ФИО"}))
		}, ErrEmptyFile},
		{"too many rows", func(t *testing.T, f *excelize.File) {
			sw, err := f.NewStreamWriter("Sheet1")
			must(t, err)
			must(t, sw.SetRow("A1", []any{"ФИО"}))
			for i := range MaxRows + 1 {
				cellRef, _ := excelize.CoordinatesToCellName(1, i+2)
				must(t, sw.SetRow(cellRef, []any{fmt.Sprintf("Студентов Студент %d", i)}))
			}
			must(t, sw.Flush())
		}, ErrTooManyRows},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data := buildXLSX(t, func(f *excelize.File) { tt.fill(t, f) })
			_, _, err := Parse("a.xlsx", data)
			if !errors.Is(err, tt.kind) {
				t.Fatalf("Parse error = %v, want %v", err, tt.kind)
			}
			if !containsCyrillic(err.Error()) {
				t.Errorf("error %q is not a Russian user message", err)
			}
		})
	}
}
