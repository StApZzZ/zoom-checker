package timefmt

import (
	"testing"
	"time"
)

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	return loc
}

func TestOffsetLabel(t *testing.T) {
	t.Parallel()
	winter := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	summer := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		zone string
		at   time.Time
		want string
	}{
		{"UTC", winter, "UTC"},
		{"Europe/Moscow", winter, "UTC+3"},
		{"Asia/Yekaterinburg", winter, "UTC+5"},
		{"Asia/Kolkata", winter, "UTC+5:30"},
		{"America/Sao_Paulo", winter, "UTC−3"},
		{"Europe/Berlin", winter, "UTC+1"},
		{"Europe/Berlin", summer, "UTC+2"},
	} {
		if got := OffsetLabel(mustZone(t, tc.zone), tc.at); got != tc.want {
			t.Errorf("OffsetLabel(%s, %s) = %q, want %q", tc.zone, tc.at.Format(time.DateOnly), got, tc.want)
		}
	}
	if got := OffsetLabel(nil, winter); got != "UTC" {
		t.Errorf("OffsetLabel(nil) = %q, want UTC", got)
	}
}

func TestDateTime(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 6, 15, 0, 0, 0, time.UTC)
	if got, want := DateTime(at, mustZone(t, "Europe/Moscow")), "06.10.2026 18:00 (UTC+3)"; got != want {
		t.Errorf("DateTime = %q, want %q", got, want)
	}
	if got, want := DateTime(at, nil), "06.10.2026 15:00 (UTC)"; got != want {
		t.Errorf("DateTime(nil) = %q, want %q", got, want)
	}
}

func TestLoadZone(t *testing.T) {
	t.Parallel()
	mustZone(t, "Asia/Yekaterinburg")
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"Asia/Yekaterinburg", true},
		{" Europe/Moscow ", true},
		{"UTC", true},
		{"", false},
		{"Local", false},
		{"local", false},
		{"Mars/Base", false},
		{"../../etc/passwd", false},
		{"Europe/Moscow\x00", false},
	} {
		if got := LoadZone(tc.name); (got != nil) != tc.ok {
			t.Errorf("LoadZone(%q) = %v, want ok=%v", tc.name, got, tc.ok)
		}
	}
}
