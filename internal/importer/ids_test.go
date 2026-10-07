package importer

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Only a header that names an identifier makes Row.ExternalID; row numbers
// are dropped (product decision: lists are renumbered all the time).
func TestParse_IDHeaders(t *testing.T) {
	t.Parallel()
	rowNumbers := []string{"№", "№ п/п", "№№", "п/п", "N", "n", "#", "Номер", "No", "No.", "№ пп", "Порядковый номер"}
	// Ambiguous headers are not IDs either ("№ студента" may be the position in the list).
	rowNumbers = append(rowNumbers, "№ студента")
	ids := []string{"student_id", "ID", "Id", "external_id", "ID студента", "Табельный", "Табельный номер",
		"Код", "Номер студента", "Студенческий билет", "Номер студенческого билета", "Номер зачётки",
		"Номер зачетки", "Зачётка", "Зачетка", "SNILS", "СНИЛС", "Идентификатор",
		"№ зачётной книжки", "№ зачетки", "№ студенческого билета", "№ студ. билета", "Табельный №", "№ личного дела"}
	emails := []string{"Email", "E-mail", "e-mail", "Почта", "Электронная почта", "Эл. почта"}

	parse := func(t *testing.T, header string) Row {
		t.Helper()
		rows, det, err := Parse("a.csv", []byte(header+";ФИО\nAbc-17;Иванов Иван\n"))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if len(rows) != 1 || !det.HasHeader || det.NameColumn != "ФИО" {
			t.Fatalf("rows=%+v det=%+v", rows, det)
		}
		return rows[0]
	}
	for _, h := range rowNumbers {
		t.Run("row number "+h, func(t *testing.T) {
			t.Parallel()
			if got := parse(t, h); got.ExternalID != "" || got.FullName != "Иванов Иван" {
				t.Errorf("%q: got %+v, want no external ID", h, got)
			}
		})
	}
	for _, h := range ids {
		t.Run("id "+h, func(t *testing.T) {
			t.Parallel()
			if got := parse(t, h); got.ExternalID != "Abc-17" {
				t.Errorf("%q: got %+v, want external ID Abc-17", h, got)
			}
		})
	}
	for _, h := range emails {
		t.Run("email "+h, func(t *testing.T) {
			t.Parallel()
			if got := parse(t, h); got.ExternalID != "abc-17" {
				t.Errorf("%q: got %+v, want the lower-cased e-mail", h, got)
			}
		})
	}
}

func TestParse_IDColumnPriority(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, data, ext string
	}{
		{"a real ID wins over an e-mail left of it", "E-mail;ФИО;ID\nA@x.ru;Иванов Иван;77\n", "77"},
		{"a real ID wins over a row number", "№;ID;ФИО\n1;77;Иванов Иван\n", "77"},
		{"an e-mail wins over a row number", "№;ФИО;Почта\n1;Иванов Иван;a@x.ru\n", "a@x.ru"},
		{"numbered blank rows under № are skipped, rows with an ID are kept", "№;ФИО\n1;\n2;Иванов Иван\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rows, _, err := Parse("a.csv", []byte(tt.data))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(rows) != 1 || rows[0].ExternalID != tt.ext || rows[0].FullName != "Иванов Иван" {
				t.Errorf("rows = %+v, want one row with external ID %q", rows, tt.ext)
			}
		})
	}
}

// A renumbered list must still match the same students — by name, not by the
// position the old list had (the reason row numbers are not IDs).
func TestReimportRenumberedListMatchesByName(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, v1, v2 string }{
		{"№ column", "№;ФИО\n1;Иванов Иван\n2;Петров Пётр\n", "№;ФИО\n1;Петров Пётр\n2;Иванов Иван\n3;Сидоров Алексей\n"},
		{"no header", "1;Иванов Иван\n2;Петров Пётр\n", "1;Петров Пётр\n2;Иванов Иван\n3;Сидоров Алексей\n"},
		{"pasted numbered list", "1. Иванов Иван\n2. Петров Пётр\n", "1. Петров Пётр\n2. Иванов Иван\n3. Сидоров Алексей\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			first, _, err := Parse("v1.csv", []byte(tt.v1))
			if err != nil {
				t.Fatal(err)
			}
			// What the group holds after the first import.
			var existing []ExistingName
			for i, r := range first {
				existing = append(existing, ExistingName{StudentID: int64(i + 1), ExternalID: r.ExternalID, FullName: r.FullName})
			}
			second, _, err := Parse("v2.csv", []byte(tt.v2))
			if err != nil {
				t.Fatal(err)
			}
			p := Validate(second, existing, ModeReplace, NameAsIs)
			if !p.OK || len(p.Archive) != 0 {
				t.Fatalf("preview not OK: %+v", p)
			}
			want := []struct {
				status RowStatus
				id     int64
			}{{StatusExisting, 2}, {StatusExisting, 1}, {StatusNew, 0}}
			for i, w := range want {
				got := p.Rows[i]
				if got.Status != w.status || (w.id != 0 && (got.StudentID == nil || *got.StudentID != w.id)) || got.Rename {
					t.Errorf("row %d = %+v, want %s student %d", i, got, w.status, w.id)
				}
				if got.Status == StatusExisting && got.Message != MsgExisting {
					t.Errorf("row %d: message %q, want no changes", i, got.Message)
				}
			}
		})
	}

	// Students stored by an older version with row numbers as IDs: a
	// renumbered list without IDs keeps them and changes nothing.
	existing := []ExistingName{{StudentID: 1, ExternalID: "1", FullName: "Иванов Иван"}, {StudentID: 2, ExternalID: "2", FullName: "Петров Пётр"}}
	rows, _, err := Parse("v2.csv", []byte("№;ФИО\n1;Петров Пётр\n2;Иванов Иван\n"))
	if err != nil {
		t.Fatal(err)
	}
	p := Validate(rows, existing, ModeReplace, NameAsIs)
	if !p.OK || *p.Rows[0].StudentID != 2 || *p.Rows[1].StudentID != 1 || p.Rows[0].Message != MsgExisting {
		t.Errorf("legacy row-number IDs: %+v", p.Rows)
	}
}

func TestParsePastedText(t *testing.T) {
	t.Parallel()
	rows, det, err := ParsePastedText("\uFEFF1. Иванов Иван\n2. Петров Пётр\n")
	if err != nil || len(rows) != 2 || rows[0].ExternalID != "" || det.Format != FormatText || det.Encoding != EncodingUTF8 {
		t.Fatalf("rows=%+v det=%+v err=%v", rows, det, err)
	}

	many := &strings.Builder{}
	for i := range MaxRows + 1 {
		fmt.Fprintf(many, "Иванов Иван %d\n", i)
	}
	for _, tt := range []struct {
		name, text string
		kind       error
	}{
		{"empty", " \n\t\n", ErrEmptyFile},
		{"too many rows", many.String(), ErrTooManyRows},
		{"too many lines", strings.Repeat("a\n", 3*MaxRows), ErrTooManyRows},
		{"too large", strings.Repeat("a", MaxFileSize+1), ErrTooLarge},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := ParsePastedText(tt.text)
			if !errors.Is(err, tt.kind) {
				t.Fatalf("err = %v, want %v", err, tt.kind)
			}
			if msg := UserMessage(err); !containsCyrillic(msg) || strings.Contains(msg, "файл") && tt.kind == ErrEmptyFile {
				t.Errorf("message %q", msg)
			}
		})
	}
}

// Control characters (NUL from a broken export or a paste) never reach a
// name or an ID: PostgreSQL text cannot hold NUL.
func TestParse_DropsControlCharacters(t *testing.T) {
	t.Parallel()
	rows, _ := ParseText("ID\tФИО\n\x007\x01\tИванов\x00 Иван\x1b\n")
	if len(rows) != 1 || rows[0].ExternalID != "7" || rows[0].FullName != "Иванов Иван" {
		t.Fatalf("rows = %+v", rows)
	}
	p := Validate([]Row{{Line: 1, FullName: "Петров\x00Пётр\x7f", ExternalID: "\x00"}}, nil, ModeAdd, NameAsIs)
	if r := p.Rows[0]; r.FullName != "ПетровПётр" || r.ExternalID != "" || r.Status != StatusNew {
		t.Errorf("validated %+v", r)
	}
}
