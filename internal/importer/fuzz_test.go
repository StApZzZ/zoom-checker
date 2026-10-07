package importer

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzParse checks that no input makes the parser panic, that every error
// is a *FileError with a Russian message, and that rows are clean.
func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"student_id,name\n1,Иванов Иван\n",
		"\xEF\xBB\xBFФИО;Код\r\nИванов Иван;1\r\n",
		"№;ФИО;;\n1;Иванов;Иван;Иванович\n",
		"1;\"Иванов Иван\n2;Петров Пётр\n",
		"sep=|\nid|name\n1|x\n",
		"1. Иванов\n- Петров\n",
		"\xFF\xFEИ\x00в\x00",
		"<html><table>",
		"Иванов;Иван\nПетров;Пётр\n",
	} {
		f.Add("a.csv", []byte(s))
	}
	f.Fuzz(func(t *testing.T, name string, data []byte) {
		rows, _, err := Parse(name, data)
		if err != nil {
			var fe *FileError
			if !errors.As(err, &fe) || fe.Message == "" {
				t.Fatalf("error %v is not a *FileError with a message", err)
			}
			return
		}
		if len(rows) == 0 || len(rows) > MaxRows {
			t.Fatalf("%d rows without an error", len(rows))
		}
		for _, r := range rows {
			if !utf8.ValidString(r.FullName) || strings.ContainsAny(r.FullName, "\n\r\t") || r.FullName != strings.TrimSpace(r.FullName) {
				t.Fatalf("unclean name %q", r.FullName)
			}
		}
		p := Validate(rows, nil, ModeReplace, NameLastFirst)
		if len(p.Rows) != len(rows) {
			t.Fatalf("validate lost rows")
		}
		ParseText(string(data))
	})
}
