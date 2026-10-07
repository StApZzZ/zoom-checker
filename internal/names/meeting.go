package names

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// Errors returned by ParseMeetingID. Messages are user-facing (Russian).
var (
	ErrMeetingIDEmpty    = errors.New("Укажите ссылку на конференцию Zoom или её номер (Meeting ID)")
	ErrMeetingIDPersonal = errors.New("Ссылка вида zoom.us/my/... не содержит номер конференции. Укажите числовой идентификатор (Meeting ID).")
	ErrMeetingIDInvalid  = errors.New("Не удалось найти номер конференции. Вставьте ссылку из приглашения Zoom или номер из 9–11 цифр.")
)

// URLChars is the character class of a pasted link: printable ASCII except
// quotes and angle brackets. Real links are ASCII (anything else is
// percent-encoded), so a link ends at the first non-ASCII rune — messengers
// glue the following text to it ("https://zoom.us/j/123456789Подключиться").
const URLChars = `[\x21\x23-\x26\x28-\x3B\x3D\x3F-\x7E]`

var (
	urlRe       = regexp.MustCompile(`(?i)(?:https?|zoommtg|zoomus)://` + URLChars + `+`)
	pathIDRe    = regexp.MustCompile(`(?i)/(?:j|s|w|wc/join|wc)/(\d[\d\-]{7,14}\d)(?:[/?#]|$)`)
	labelIDRe   = regexp.MustCompile(`(?i)(?:meeting\s*id|идентификатор\s+(?:конференции|встречи)|id\s+конференции|номер\s+конференции)\s*[:：]?\s*(\d[\d \-\x{00a0}]{7,16}\d)`)
	bareDigitRe = regexp.MustCompile(`^\d[\d \-\x{00a0}]*\d$`)
)

// ParseMeetingID extracts a Zoom meeting number from what a teacher pastes:
// a bare number ("123 456 7890"), a join link (https://us02web.zoom.us/j/85746065432?pwd=...),
// a zoommtg:// link, or a whole invitation text. Returns digits only.
func ParseMeetingID(input string) (string, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return "", ErrMeetingIDEmpty
	}
	if bareDigitRe.MatchString(s) {
		return validDigits(s)
	}
	sawPersonal := false
	for _, raw := range urlRe.FindAllString(s, -1) {
		raw = strings.TrimRight(raw, ".,;)")
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		host := strings.ToLower(u.Hostname())
		if host != "zoom.us" && !strings.HasSuffix(host, ".zoom.us") && host != "zoom.com" && !strings.HasSuffix(host, ".zoom.com") &&
			u.Scheme != "zoommtg" && u.Scheme != "zoomus" {
			continue
		}
		if c := u.Query().Get("confno"); c != "" {
			if id, err := validDigits(c); err == nil {
				return id, nil
			}
		}
		if m := pathIDRe.FindStringSubmatch(u.EscapedPath() + "/"); m != nil {
			if id, err := validDigits(m[1]); err == nil {
				return id, nil
			}
		}
		if strings.HasPrefix(strings.ToLower(u.Path), "/my/") {
			sawPersonal = true
		}
	}
	if m := labelIDRe.FindStringSubmatch(s); m != nil {
		if id, err := validDigits(m[1]); err == nil {
			return id, nil
		}
	}
	if sawPersonal {
		return "", ErrMeetingIDPersonal
	}
	return "", ErrMeetingIDInvalid
}

func validDigits(s string) (string, error) {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == ' ':
		default:
			return "", ErrMeetingIDInvalid
		}
	}
	d := b.String()
	if len(d) < 9 || len(d) > 11 {
		return "", ErrMeetingIDInvalid
	}
	return d, nil
}

// FormatMeetingID groups digits the way Zoom shows them:
// 9 → 3-3-3, 10 → 3-3-4, 11 → 3-4-4. Other lengths are returned unchanged.
func FormatMeetingID(id string) string {
	switch len(id) {
	case 9:
		return id[:3] + " " + id[3:6] + " " + id[6:]
	case 10:
		return id[:3] + " " + id[3:6] + " " + id[6:]
	case 11:
		return id[:3] + " " + id[3:7] + " " + id[7:]
	}
	return id
}
