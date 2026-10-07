// Package timefmt formats times for user-facing texts (audit entries, error
// messages, export headers). Teachers may sit in a different time zone than
// the server, so every server-built time names its UTC offset; the labels
// match the web app's utcOffsetLabel (web/src/lib/format.ts).
package timefmt

import (
	"fmt"
	"strings"
	"time"
)

// Layouts shared by messages and exports.
const (
	DateLayout     = "02.01.2006"
	ClockLayout    = "15:04"
	DateTimeLayout = DateLayout + " " + ClockLayout
)

// OffsetLabel is the UTC offset of loc at the instant at: "UTC+3",
// "UTC+5:30", "UTC−3" (minus sign U+2212, as on the web) or "UTC". A nil loc
// is UTC.
func OffsetLabel(loc *time.Location, at time.Time) string {
	if loc == nil {
		loc = time.UTC
	}
	_, off := at.In(loc).Zone()
	if off == 0 {
		return "UTC"
	}
	sign := "+"
	if off < 0 {
		sign, off = "−", -off
	}
	h, m := off/3600, off%3600/60
	if m == 0 {
		return fmt.Sprintf("UTC%s%d", sign, h)
	}
	return fmt.Sprintf("UTC%s%d:%02d", sign, h, m)
}

// DateTime formats t in loc with its offset: "06.10.2026 18:00 (UTC+3)".
func DateTime(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format(DateTimeLayout) + " (" + OffsetLabel(loc, t) + ")"
}

// LoadZone resolves an IANA zone name sent by a browser (the `tz` query
// parameter of exports). It returns nil for an empty, unknown or
// server-relative name ("Local"), so callers fall back to TIMEZONE.
func LoadZone(name string) *time.Location {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 || strings.EqualFold(name, "Local") {
		return nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil
	}
	return loc
}
