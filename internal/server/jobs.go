package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"zoom-checker/internal/config"
	"zoom-checker/internal/db"
	"zoom-checker/internal/store"
)

const (
	sessionCleanupEvery = time.Hour
	// Daily maintenance runs at 03:30 in the configured time zone: a quiet hour
	// for schools, after midnight so the backup carries the previous day.
	maintenanceHour   = 3
	maintenanceMinute = 30
	backupPrefix      = "zoom-checker-"
	backupSuffix      = ".db"
)

// MinEventsRetentionDays is the shortest raw webhook event retention that is
// applied; shorter EVENTS_RETENTION_DAYS values are raised to this (with a
// warning). Events of sessions linked to a lesson are never purged (their
// lesson is rebuilt from them), but a session that is not linked yet can still
// be linked by hand on the Zoom page later — its events must still be there
// then, at least for the last few months.
const MinEventsRetentionDays = 90

// EffectiveEventsRetentionDays returns the retention actually applied for an
// EVENTS_RETENTION_DAYS value: 0 (or less) keeps events forever, anything else
// is at least MinEventsRetentionDays.
func EffectiveEventsRetentionDays(days int) int {
	if days <= 0 {
		return 0
	}
	return max(days, MinEventsRetentionDays)
}

var backupNameRE = regexp.MustCompile(`^zoom-checker-\d{4}-\d{2}-\d{2}\.db$`)

// jobs runs periodic housekeeping: expired session cleanup, raw event
// retention and SQLite backups.
type jobs struct {
	cfg *config.Config
	db  *db.DB
	st  *store.Store
	log *slog.Logger
	now func() time.Time
}

func (j *jobs) run(ctx context.Context) {
	j.cleanSessions(ctx)
	j.startupBackup(ctx)

	hourly := time.NewTicker(sessionCleanupEvery)
	defer hourly.Stop()
	daily := time.NewTimer(j.untilDailyRun())
	defer daily.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hourly.C:
			j.cleanSessions(ctx)
		case <-daily.C:
			j.dailyMaintenance(ctx)
			daily.Reset(j.untilDailyRun())
		}
	}
}

// untilDailyRun is the wait before the next daily maintenance, measured on
// j.now (time.Until would mix in the real clock: with a test clock in the past
// the wait would stay negative and the timer would fire in a tight loop).
func (j *jobs) untilDailyRun() time.Duration {
	now := j.now()
	return nextDailyRun(now, j.cfg.Timezone).Sub(now)
}

// nextDailyRun returns the next 03:30 in loc strictly after now. time.Date
// normalizes days and DST gaps, so the run never happens twice on one day.
func nextDailyRun(now time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	l := now.In(loc)
	next := time.Date(l.Year(), l.Month(), l.Day(), maintenanceHour, maintenanceMinute, 0, 0, loc)
	if !next.After(now) {
		next = time.Date(l.Year(), l.Month(), l.Day()+1, maintenanceHour, maintenanceMinute, 0, 0, loc)
	}
	return next
}

func (j *jobs) cleanSessions(ctx context.Context) {
	n, err := j.st.DeleteExpiredSessions(ctx, j.now().UTC())
	switch {
	case err != nil && ctx.Err() == nil:
		j.log.Warn("could not delete expired sessions", "err", err)
	case n > 0:
		j.log.Debug("deleted expired sessions", "count", n)
	}
}

func (j *jobs) dailyMaintenance(ctx context.Context) {
	j.applyRetention(ctx)
	if _, err := j.backup(ctx); err != nil && ctx.Err() == nil {
		j.log.Error("daily database backup failed", "err", err)
	}
}

// applyRetention deletes raw webhook events older than EVENTS_RETENTION_DAYS
// (0 = keep forever, never less than MinEventsRetentionDays) that nothing
// needs: events of sessions linked to a lesson and pending events are kept
// whatever their age (store.PurgeEventsBefore), so rebuilding an old lesson
// never loses its attendance.
func (j *jobs) applyRetention(ctx context.Context) {
	days := EffectiveEventsRetentionDays(j.cfg.EventsRetentionDays)
	if days <= 0 {
		return
	}
	cutoff := j.now().UTC().AddDate(0, 0, -days)
	n, err := j.st.PurgeEventsBefore(ctx, cutoff)
	if err != nil {
		if ctx.Err() == nil {
			j.log.Error("raw event retention failed", "err", err)
		}
		return
	}
	if n > 0 {
		j.log.Info("deleted old raw webhook events", "count", n, "older_than_days", days)
	}
}

func (j *jobs) backupsEnabled() bool {
	return j.cfg.BackupKeep > 0 && j.db.Dialect == db.SQLite && j.db.SQLitePath() != ""
}

func (j *jobs) backupDir() string { return filepath.Join(j.cfg.DataDir, "backups") }

// startupBackup makes a backup right away when the newest one is older than a
// day (or there is none), so a server that is rarely up at 03:30 still gets them.
func (j *jobs) startupBackup(ctx context.Context) {
	if !j.backupsEnabled() {
		return
	}
	names, err := listBackups(j.backupDir())
	if err != nil {
		j.log.Warn("could not list backups", "dir", j.backupDir(), "err", err)
	}
	if len(names) > 0 {
		if st, err := os.Stat(filepath.Join(j.backupDir(), names[0])); err == nil && j.now().Sub(st.ModTime()) < 24*time.Hour {
			return
		}
	}
	if _, err := j.backup(ctx); err != nil && ctx.Err() == nil {
		j.log.Error("database backup failed", "err", err)
	}
}

// backup writes DATA_DIR/backups/zoom-checker-YYYY-MM-DD.db (date in TIMEZONE)
// and keeps the BACKUP_KEEP newest files. It returns the path written, or ""
// when backups are disabled or today's file already exists.
func (j *jobs) backup(ctx context.Context) (string, error) {
	if !j.backupsEnabled() {
		return "", nil
	}
	dir := j.backupDir()
	name := backupPrefix + j.now().In(j.cfg.Timezone).Format("2006-01-02") + backupSuffix
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		return "", nil
	}
	start := time.Now()
	if err := j.db.Backup(ctx, path); err != nil {
		return "", fmt.Errorf("backup to %s: %w", path, err)
	}
	j.log.Info("database backup written", "file", path, "dur_ms", time.Since(start).Milliseconds())
	if removed, err := pruneBackups(dir, j.cfg.BackupKeep); err != nil {
		j.log.Warn("could not remove old backups", "dir", dir, "err", err)
	} else if removed > 0 {
		j.log.Info("removed old backups", "count", removed, "keep", j.cfg.BackupKeep)
	}
	return path, nil
}

// listBackups returns backup file names, newest first (the date in the name
// sorts chronologically). A missing directory is an empty list.
func listBackups(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && backupNameRE.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names, nil
}

// pruneBackups keeps the keep newest backups in dir and deletes the rest.
func pruneBackups(dir string, keep int) (int, error) {
	if keep <= 0 {
		return 0, nil
	}
	names, err := listBackups(dir)
	if err != nil {
		return 0, err
	}
	removed := 0
	var errs []error
	for _, n := range names[min(keep, len(names)):] {
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}
