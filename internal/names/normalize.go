// Package names implements display-name normalization (TZ §7) and Zoom meeting ID parsing (TZ §5).
package names

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Normalize prepares a display name for exact comparison:
//  1. Unicode NFKC (composes "е"+U+0308 into "ё", maps NBSP / full-width forms to plain ones);
//  2. drops invisible format runes (zero-width spaces/joiners, BOM, bidi marks, soft hyphen);
//  3. lower-cases;
//  4. replaces "ё" with "е";
//  5. collapses every run of whitespace into one ASCII space and trims.
//
// No fuzzy matching, transliteration or initials handling is done on purpose:
// two names are the same student only if they are equal after these steps.
func Normalize(s string) string {
	s = norm.NFKC.String(s)
	var b strings.Builder
	b.Grow(len(s))
	pendingSpace := false
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Cf, r):
			continue
		case unicode.IsSpace(r):
			pendingSpace = b.Len() > 0
			continue
		}
		if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		r = unicode.ToLower(r)
		if r == 'ё' {
			r = 'е'
		}
		b.WriteRune(r)
	}
	return b.String()
}
