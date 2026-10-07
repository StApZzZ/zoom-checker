package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"zoom-checker/internal/model"
	"zoom-checker/internal/names"
	"zoom-checker/internal/store"
)

// settingsMu serializes every read-modify-write of the settings row: PUT
// /api/settings (handlePutSettings) and "always hide this name" on the lesson
// screen (lessons_manual.go). Lock order: settingsMu first, then the database
// transaction — never take it inside a transaction.
var settingsMu sync.Mutex

// Limits of the business settings (docs §3, §7).
const (
	maxIgnoredNames      = 500
	maxIgnoredNameLength = 200
	minLinkWindowHours   = 1
	maxLinkWindowHours   = 72
	// Lessons planned this far back (and all future ones) are rebuilt after a
	// settings change; older ones keep the verdicts they were finished with.
	settingsRebuildHorizon = 60 * 24 * time.Hour
	settingsRebuildTimeout = 30 * time.Minute
)

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) error {
	st, err := s.Store.GetSettings(r.Context())
	if err != nil {
		return errInternal(err)
	}
	if st.IgnoredNames == nil {
		st.IgnoredNames = []string{}
	}
	writeJSON(w, http.StatusOK, st)
	return nil
}

// settingsInput is PUT /api/settings; absent fields keep their stored value.
type settingsInput struct {
	IgnoredNames      *[]string `json:"ignored_names"`
	PresentMinPercent *int      `json:"present_min_percent"`
	LinkWindowHours   *int      `json:"link_window_hours"`
	IgnoreHost        *bool     `json:"ignore_host"`
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) error {
	var in settingsInput
	if err := decodeJSON(w, r, &in); err != nil {
		return err
	}
	if in.PresentMinPercent != nil {
		if p := *in.PresentMinPercent; p < 0 || p > 100 {
			return errValidation("Порог присутствия должен быть от 0 до 100 %", map[string]any{"field": "present_min_percent"})
		}
	}
	if in.LinkWindowHours != nil {
		if h := *in.LinkWindowHours; h < minLinkWindowHours || h > maxLinkWindowHours {
			return errValidation(fmt.Sprintf("Окно привязки конференции к занятию должно быть от %d до %d часов",
				minLinkWindowHours, maxLinkWindowHours), map[string]any{"field": "link_window_hours"})
		}
	}
	var ignored []string
	if in.IgnoredNames != nil {
		list, err := cleanIgnoredNames(*in.IgnoredNames)
		if err != nil {
			return err
		}
		ignored = list
	}

	// Read-merge-write as one unit: two admins saving different fields at the
	// same time must not undo each other's change (the mutex covers PostgreSQL,
	// whose READ COMMITTED transactions would allow a lost update).
	ctx := r.Context()
	var old, next model.Settings
	err := func() error {
		settingsMu.Lock()
		defer settingsMu.Unlock()
		return s.Store.InTx(ctx, func(tx *store.Store) error {
			var err error
			if old, err = tx.GetSettings(ctx); err != nil {
				return err
			}
			next = old
			next.IgnoredNames = slices.Clone(old.IgnoredNames)
			if in.PresentMinPercent != nil {
				next.PresentMinPercent = *in.PresentMinPercent
			}
			if in.LinkWindowHours != nil {
				next.LinkWindowHours = *in.LinkWindowHours
			}
			if in.IgnoreHost != nil {
				next.IgnoreHost = *in.IgnoreHost
			}
			if in.IgnoredNames != nil {
				next.IgnoredNames = ignored
			}
			if next.IgnoredNames == nil {
				next.IgnoredNames = []string{}
			}
			return tx.PutSettings(ctx, next, userIDPtr(r))
		})
	}()
	if err != nil {
		return errInternal(err)
	}
	if old.IgnoredNames == nil {
		old.IgnoredNames = []string{}
	}
	prevJSON, _ := json.Marshal(old)
	nextJSON, _ := json.Marshal(next)
	if string(prevJSON) != string(nextJSON) {
		s.audit(r, model.AuditEntry{Action: model.AuditSettingsUpdate, EntityType: "settings",
			EntityID: settingsEntityID, PreviousValue: string(prevJSON), NewValue: string(nextJSON)})
		now := s.now()
		s.goBackground(settingsRebuildTimeout, func(ctx context.Context) { s.rebuildRecentLessons(ctx, now) })
	}
	writeJSON(w, http.StatusOK, next)
	return nil
}

// cleanIgnoredNames trims the entries, drops empty ones and duplicates (by
// normalized form, the first spelling wins) and enforces the size limits.
func cleanIgnoredNames(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, raw := range in {
		name := strings.TrimSpace(raw)
		norm := names.Normalize(name)
		if norm == "" {
			continue
		}
		if utf8.RuneCountInString(name) > maxIgnoredNameLength {
			return nil, errValidation(fmt.Sprintf("Имя «%s…» слишком длинное: не больше %d символов",
				settingsTruncateRunes(name, 30), maxIgnoredNameLength), map[string]any{"field": "ignored_names"})
		}
		if seen[norm] {
			continue
		}
		seen[norm] = true
		out = append(out, name)
	}
	if len(out) > maxIgnoredNames {
		return nil, errValidation(fmt.Sprintf("Слишком много имён в списке: не больше %d", maxIgnoredNames),
			map[string]any{"field": "ignored_names"})
	}
	return out, nil
}

func settingsTruncateRunes(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

// rebuildRecentLessons recomputes the projections of lessons planned within
// the rebuild horizon or later, so new rules (ignored names, present
// threshold, host flag) apply to them. Stale planned lessons are skipped: they
// have no participants the rules could apply to (lessonStalePlanned). Runs in
// the background (goBackground): it stops early when ctx ends (the server is
// closing).
func (s *Server) rebuildRecentLessons(ctx context.Context, now time.Time) {
	from := now.Add(-settingsRebuildHorizon)
	lessons, err := s.Store.ListLessons(ctx, store.LessonFilter{From: &from})
	if err != nil {
		s.Log.Error("settings changed: list lessons to rebuild", "err", err)
		return
	}
	start := time.Now()
	done, failed := 0, 0
	for i := range lessons {
		if ctx.Err() != nil {
			break
		}
		if lessonStalePlanned(&lessons[i], now) {
			continue
		}
		if err := s.Processor.RebuildLesson(ctx, lessons[i].ID); err != nil {
			failed++
			s.Log.Warn("settings changed: lesson rebuild failed", "lesson_id", lessons[i].ID, "err", err)
			continue
		}
		done++
	}
	s.Log.Info("settings changed: lessons rebuilt", "count", done, "failed", failed,
		"total", len(lessons), "dur_ms", time.Since(start).Milliseconds())
}
