package importer

import (
	"bytes"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
	xunicode "golang.org/x/text/encoding/unicode"
)

var (
	bomUTF8    = []byte{0xEF, 0xBB, 0xBF}
	bomUTF16LE = []byte{0xFF, 0xFE}
	bomUTF16BE = []byte{0xFE, 0xFF}
)

// decodeText converts raw file bytes to a Go string and reports the encoding.
// Order: BOMs, UTF-16 without BOM (NUL byte pattern of Excel "Unicode text"),
// valid UTF-8, then windows-1251 as the fallback used by Russian Excel CSV.
func decodeText(data []byte) (string, string, error) {
	var (
		text string
		enc  string
		err  error
	)
	switch {
	case bytes.HasPrefix(data, bomUTF8):
		text, enc = strings.ToValidUTF8(string(data[len(bomUTF8):]), "\uFFFD"), EncodingUTF8
	case bytes.HasPrefix(data, bomUTF16LE):
		enc = EncodingUTF16LE
		text, err = decodeUTF16(data[len(bomUTF16LE):], xunicode.LittleEndian)
	case bytes.HasPrefix(data, bomUTF16BE):
		enc = EncodingUTF16BE
		text, err = decodeUTF16(data[len(bomUTF16BE):], xunicode.BigEndian)
	default:
		switch guessUTF16(data) {
		case EncodingUTF16LE:
			enc = EncodingUTF16LE
			text, err = decodeUTF16(data, xunicode.LittleEndian)
		case EncodingUTF16BE:
			enc = EncodingUTF16BE
			text, err = decodeUTF16(data, xunicode.BigEndian)
		default:
			if bytes.IndexByte(data, 0) >= 0 {
				// NUL bytes without a UTF-16 pattern: an image, archive, etc.
				return "", "", fileErr(ErrUnreadable, msgBinary, nil)
			}
			if utf8.Valid(data) {
				text, enc = string(data), EncodingUTF8
			} else if mostlyUTF8(data) {
				// A UTF-8 file with a stray broken byte: reading all of it as
				// windows-1251 would garble every name, so only that byte is lost.
				text, enc = strings.ToValidUTF8(string(data), "\uFFFD"), EncodingUTF8
			} else {
				enc = EncodingWindows1251
				var b []byte
				b, err = charmap.Windows1251.NewDecoder().Bytes(data)
				text = string(b)
			}
		}
	}
	if err != nil {
		return "", "", fileErr(ErrUnreadable, msgUnreadable, err)
	}
	text = strings.TrimPrefix(text, "\uFEFF")
	if looksBinary(text) {
		return "", "", fileErr(ErrUnreadable, msgBinary, nil)
	}
	if msg := markupMessage(text); msg != "" {
		return "", "", fileErr(ErrUnsupportedFormat, msg, nil)
	}
	return text, enc, nil
}

// mostlyUTF8 reports whether invalid bytes are rare next to valid multi-byte
// UTF-8 characters. windows-1251 Cyrillic almost never forms valid UTF-8
// sequences, so real windows-1251 text has far more invalid bytes than valid
// multi-byte characters.
func mostlyUTF8(data []byte) bool {
	var multi, invalid int
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		switch {
		case r == utf8.RuneError && size == 1:
			invalid++
		case size > 1:
			multi++
		}
		data = data[size:]
	}
	return multi > 0 && invalid*5 <= multi
}

// markupMessage recognizes documents that are text but not a list: HTML or
// XML that systems save with an .xls extension ("Excel" export of a web
// page) and RTF from Word. Returns "" for anything else.
func markupMessage(text string) string {
	head := strings.ToLower(strings.TrimLeftFunc(text[:min(len(text), 4096)], unicode.IsSpace))
	switch {
	case strings.HasPrefix(head, "{\\rtf"):
		return msgWord
	case !strings.HasPrefix(head, "<"):
		return ""
	}
	for _, tag := range []string{"<html", "<!doctype", "<table", "<?xml", "<workbook", "<body", "<meta"} {
		if strings.Contains(head, tag) {
			return msgMarkup
		}
	}
	return ""
}

func decodeUTF16(data []byte, order xunicode.Endianness) (string, error) {
	b, err := xunicode.UTF16(order, xunicode.IgnoreBOM).NewDecoder().Bytes(data)
	return string(b), err
}

// guessUTF16 detects BOM-less UTF-16 by where NUL bytes sit: ASCII characters
// (digits, delimiters, spaces, line breaks) have a zero high byte, which lands
// on odd offsets in little endian and even offsets in big endian. Cyrillic
// letters (U+04xx) have no zero byte, so only the parity matters, not the count.
func guessUTF16(data []byte) string {
	n := min(len(data), 8192)
	var even, odd int
	for i := range n {
		if data[i] == 0 {
			if i%2 == 0 {
				even++
			} else {
				odd++
			}
		}
	}
	switch {
	case odd >= 2 && odd >= 4*even:
		return EncodingUTF16LE
	case even >= 2 && even >= 4*odd:
		return EncodingUTF16BE
	}
	return ""
}

// looksBinary reports whether decoded text contains control characters that
// never occur in a list of names (beyond the occasional stray one).
func looksBinary(text string) bool {
	var bad, total int
	for _, r := range text {
		total++
		if r == 0 {
			return true
		}
		if r != '\t' && r != '\n' && r != '\r' && r != '\f' && unicode.IsControl(r) {
			bad++
		}
	}
	return bad > 2 && bad*100 > total
}
