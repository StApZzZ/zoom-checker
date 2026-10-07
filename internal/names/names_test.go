package names

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"  ИВАНОВ   Иван ":        "иванов иван", // TZ §36 scenario 2
		"Иванов Иван":             "иванов иван",
		"иванов иван":             "иванов иван",
		"\u00a0Фёдоров\tМаксим\n": "федоров максим",
		"Фе\u0308доров":           "федоров", // decomposed ё
		"ЁЛКИН Пётр":              "елкин петр",
		"Иван\u200bов Иван":       "иванов иван", // zero-width space
		"\ufeffПетров Пётр":       "петров петр", // BOM
		"Йошкин Кот":              "йошкин кот",  // й must stay й
		"Ｉｖａｎ":                    "ivan",        // full-width
		"":                        "",
		" \t ":                    "",
		"Иван И.":                 "иван и.",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	// Different people must stay different: no fuzzy matching (TZ §7).
	if Normalize("Иван") == Normalize("Иванов Иван") || Normalize("Vanya") == Normalize("Ваня") {
		t.Error("normalization must not merge different names")
	}
}

func TestParseMeetingID(t *testing.T) {
	t.Parallel()
	ok := map[string]string{
		"1234567890":                   "1234567890",
		"  123 456 7890 ":              "1234567890",
		"123-456-7890":                 "1234567890",
		"857 4606 5432":                "85746065432",
		"123456789":                    "123456789",
		"https://zoom.us/j/1234567890": "1234567890",
		"https://us02web.zoom.us/j/85746065432?pwd=abcd": "85746065432",
		"https://company.zoom.us/j/85746065432#success":  "85746065432",
		"https://zoom.us/s/1234567890":                   "1234567890",
		"https://zoom.us/w/1234567890?tk=xyz":            "1234567890",
		"https://zoom.us/wc/join/1234567890":             "1234567890",
		"https://zoom.us/wc/1234567890/join":             "1234567890",
		"zoommtg://zoom.us/join?confno=1234567890&pwd=x": "1234567890",
		"Подключиться к конференции Zoom\nhttps://us06web.zoom.us/j/87654321098?pwd=QWERTY.1\n\nИдентификатор конференции: 876 5432 1098\nКод доступа: 123456": "87654321098",
		"Join Zoom Meeting\nMeeting ID: 857 4606 5432\nPasscode: 1": "85746065432",
		// Text glued to the link (pasted from a messenger): the link ends at the
		// first non-ASCII rune.
		"https://zoom.us/j/123456789Подключиться":                     "123456789",
		"Ссылка:https://us02web.zoom.us/j/85746065432?pwd=abcdКод: 1": "85746065432",
		"«https://zoom.us/j/1234567890»":                              "1234567890",
		"https://zoom.us/j/1234567890\u00a0— начало в 18:00":          "1234567890",
		"zoommtg://zoom.us/join?confno=1234567890Вход":                "1234567890",
		"<https://zoom.us/j/1234567890>":                              "1234567890",
	}
	for in, want := range ok {
		got, err := ParseMeetingID(in)
		if err != nil || got != want {
			t.Errorf("ParseMeetingID(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := map[string]error{
		"":                                      ErrMeetingIDEmpty,
		"   ":                                   ErrMeetingIDEmpty,
		"https://zoom.us/my/ivanov":             ErrMeetingIDPersonal,
		"12345":                                 ErrMeetingIDInvalid,
		"123456789012":                          ErrMeetingIDInvalid,
		"https://evil.example/j/1234567890":     ErrMeetingIDInvalid,
		"https://zoom.us.evil.com/j/1234567890": ErrMeetingIDInvalid,
		"просто текст":                          ErrMeetingIDInvalid,
	}
	for in, want := range bad {
		if _, err := ParseMeetingID(in); !errors.Is(err, want) {
			t.Errorf("ParseMeetingID(%q) err = %v, want %v", in, err, want)
		}
	}
}

func TestFormatMeetingID(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"123456789":   "123 456 789",
		"1234567890":  "123 456 7890",
		"85746065432": "857 4606 5432",
		"12":          "12",
	}
	for in, want := range cases {
		if got := FormatMeetingID(in); got != want {
			t.Errorf("FormatMeetingID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestURLCharsPrintableASCIIOnly(t *testing.T) {
	t.Parallel()
	re := regexp.MustCompile(`^` + URLChars + `$`)
	for r := rune(0); r < 0x500; r++ {
		want := r >= 0x21 && r <= 0x7e && !strings.ContainsRune(`"'<>`, r)
		if got := re.MatchString(string(r)); got != want {
			t.Errorf("URLChars matches %q (%U) = %v, want %v", r, r, got, want)
		}
	}
}
