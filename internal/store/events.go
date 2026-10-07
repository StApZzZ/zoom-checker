package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"zoom-checker/internal/db"
	"zoom-checker/internal/model"
)

const eventColsNoPayload = `id, event_type, event_ts, meeting_id, meeting_uuid, participant_name, occurred_at, dedupe_key,
	received_at, processed_at, processing_status, attempts, next_attempt_at, error`

const eventCols = eventColsNoPayload + `, payload`

func scanEvent(withPayload bool) func(sc scanner) (model.WebhookEvent, error) {
	return func(sc scanner) (model.WebhookEvent, error) {
		var e model.WebhookEvent
		var occurred, processed, next sql.NullInt64
		var received int64
		dest := []any{&e.ID, &e.EventType, &e.EventTS, &e.MeetingID, &e.MeetingUUID, &e.ParticipantName, &occurred,
			&e.DedupeKey, &received, &processed, &e.ProcessingStatus, &e.Attempts, &next, &e.Error}
		if withPayload {
			dest = append(dest, &e.Payload)
		}
		if err := sc.Scan(dest...); err != nil {
			return e, err
		}
		e.OccurredAt = fromNullMS(occurred)
		e.ReceivedAt = fromMS(received)
		e.ProcessedAt = fromNullMS(processed)
		e.NextAttemptAt = fromNullMS(next)
		return e, nil
	}
}

func validEventStatus(st model.EventStatus) bool {
	switch st {
	case model.EventPending, model.EventProcessed, model.EventDuplicate, model.EventIgnored, model.EventFailed:
		return true
	}
	return false
}

// InsertWebhookEvent stores a raw webhook row and returns its id (also set in
// e.ID). Empty ProcessingStatus = pending; zero ReceivedAt = store clock.
//
// SQLite keeps every string byte-for-byte. PostgreSQL TEXT cannot hold NUL bytes
// or invalid UTF-8, so on PostgreSQL such bytes in the text fields (payload
// included) are replaced with U+FFFD and the fields of e are updated to what
// was stored — the raw event must never be lost (docs/architecture.md §5.7).
func (s *Store) InsertWebhookEvent(ctx context.Context, e *model.WebhookEvent) (int64, error) {
	if e.ProcessingStatus == "" {
		e.ProcessingStatus = model.EventPending
	}
	if !validEventStatus(e.ProcessingStatus) {
		return 0, fmt.Errorf("%w: event status %q", ErrInvalid, e.ProcessingStatus)
	}
	if e.ReceivedAt.IsZero() {
		e.ReceivedAt = fromMS(s.nowMS())
	}
	if s.db.Dialect == db.Postgres {
		for _, f := range []*string{&e.EventType, &e.MeetingID, &e.MeetingUUID, &e.ParticipantName, &e.DedupeKey,
			&e.Payload, &e.Error} {
			*f = pgSafeText(*f)
		}
	}
	err := s.queryRow(ctx, `INSERT INTO zoom_webhook_events (event_type, event_ts, meeting_id, meeting_uuid, participant_name,
		occurred_at, dedupe_key, payload, received_at, processed_at, processing_status, attempts, next_attempt_at, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		e.EventType, e.EventTS, e.MeetingID, e.MeetingUUID, e.ParticipantName, msArg(e.OccurredAt), e.DedupeKey,
		e.Payload, toMS(e.ReceivedAt), msArg(e.ProcessedAt), string(e.ProcessingStatus), e.Attempts,
		msArg(e.NextAttemptAt), e.Error).Scan(&e.ID)
	if err != nil {
		return 0, fmt.Errorf("store: insert webhook event: %w", err)
	}
	return e.ID, nil
}

func (s *Store) listEvents(ctx context.Context, withPayload bool, q string, args ...any) ([]model.WebhookEvent, error) {
	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list webhook events: %w", err)
	}
	out, err := collect(rows, scanEvent(withPayload))
	if err != nil {
		return nil, fmt.Errorf("store: list webhook events: %w", err)
	}
	return out, nil
}

// ListDueEvents returns up to limit pending events whose next attempt is due
// (next_attempt_at IS NULL or <= now), ordered by id, with payload.
func (s *Store) ListDueEvents(ctx context.Context, now time.Time, limit int) ([]model.WebhookEvent, error) {
	return s.listEvents(ctx, true, `SELECT `+eventCols+` FROM zoom_webhook_events
		WHERE processing_status = 'pending' AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
		ORDER BY id`+limitClause(limit), toMS(now))
}

// UpdateEventStatus records the outcome of a processing attempt: status, attempt
// counter, next attempt time (nil = none), processed time (nil = not processed)
// and error text ("" = none). ErrNotFound when the event does not exist.
func (s *Store) UpdateEventStatus(ctx context.Context, id int64, status model.EventStatus, attempts int,
	nextAttemptAt, processedAt *time.Time, errText string) error {
	if !validEventStatus(status) {
		return fmt.Errorf("%w: event status %q", ErrInvalid, status)
	}
	if err := s.execOne(ctx, `UPDATE zoom_webhook_events SET processing_status = ?, attempts = ?, next_attempt_at = ?,
		processed_at = ?, error = ? WHERE id = ?`,
		string(status), attempts, msArg(nextAttemptAt), msArg(processedAt), errText, id); err != nil {
		if err == ErrNotFound {
			return err
		}
		return fmt.Errorf("store: update event status: %w", err)
	}
	return nil
}

// HasEarlierWithDedupeKey reports whether an event with the same dedupe key and a
// smaller id exists that is not itself marked duplicate (the first delivery).
func (s *Store) HasEarlierWithDedupeKey(ctx context.Context, key string, beforeID int64) (bool, error) {
	var one int
	err := s.queryRow(ctx, `SELECT 1 FROM zoom_webhook_events
		WHERE dedupe_key = ? AND id < ? AND processing_status <> 'duplicate' LIMIT 1`, key, beforeID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: dedupe lookup: %w", err)
	}
	return true, nil
}

// ListParticipantEvents returns every non-duplicate participant event
// (event_type LIKE 'meeting.participant_%', any other status) of the given
// sessions, ordered by id, with payload — the input of a projection rebuild.
func (s *Store) ListParticipantEvents(ctx context.Context, meetingUUIDs []string) ([]model.WebhookEvent, error) {
	out := []model.WebhookEvent{}
	for _, ch := range chunks(len(meetingUUIDs), inChunk) {
		uuids := meetingUUIDs[ch[0]:ch[1]]
		args := make([]any, len(uuids))
		for i, u := range uuids {
			args[i] = u
		}
		evs, err := s.listEvents(ctx, true, `SELECT `+eventCols+` FROM zoom_webhook_events
			WHERE meeting_uuid IN (`+placeholders(len(uuids))+`) AND processing_status <> 'duplicate'
			AND event_type LIKE 'meeting.participant_%' ORDER BY id`, args...)
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	}
	if len(meetingUUIDs) > inChunk {
		// Chunks are each ordered; restore the global id order.
		sortEventsByID(out)
	}
	return out, nil
}

func sortEventsByID(evs []model.WebhookEvent) {
	sort.Slice(evs, func(i, j int) bool { return evs[i].ID < evs[j].ID })
}

// EventFilter selects events for the admin journal (ListWebhookEvents).
type EventFilter struct {
	Status   model.EventStatus // "" = any
	Type     string            // exact event_type, "" = any
	Query    string            // substring of participant_name, meeting_id or meeting_uuid
	BeforeID int64             // id < BeforeID (keyset pagination), 0 = from the newest
	Limit    int               // <= 0 means 100
}

// ListWebhookEvents returns events newest first (id DESC) without payload.
// Query matching is a case-insensitive substring match (Unicode-aware, so
// Cyrillic names work); a query made of digits, spaces and dashes also matches the
// meeting id with the separators removed ("857 4606 5432" finds 85746065432).
func (s *Store) ListWebhookEvents(ctx context.Context, f EventFilter) ([]model.WebhookEvent, error) {
	var where []string
	var args []any
	if f.Status != "" {
		where = append(where, `processing_status = ?`)
		args = append(args, string(f.Status))
	}
	if f.Type != "" {
		where = append(where, `event_type = ?`)
		args = append(args, f.Type)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		pat := "%" + escapeLike(strings.ToLower(q)) + "%"
		lower := s.db.LowerExpr
		cond := `(` + lower("participant_name") + ` LIKE ? ESCAPE '\' OR meeting_id LIKE ? ESCAPE '\' OR ` +
			lower("meeting_uuid") + ` LIKE ? ESCAPE '\'`
		args = append(args, pat, pat, pat)
		if digits := digitsOnly(q); digits != "" && digits != q {
			cond += ` OR meeting_id LIKE ? ESCAPE '\'`
			args = append(args, "%"+digits+"%")
		}
		where = append(where, cond+`)`)
	}
	if f.BeforeID > 0 {
		where = append(where, `id < ?`)
		args = append(args, f.BeforeID)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT ` + eventColsNoPayload + ` FROM zoom_webhook_events`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	return s.listEvents(ctx, false, q+` ORDER BY id DESC`+limitClause(limit), args...)
}

// escapeLike escapes LIKE wildcards with backslash.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// digitsOnly returns the digits of s when s consists only of digits, spaces and
// dashes; otherwise "".
func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-':
		default:
			return ""
		}
	}
	return b.String()
}

// GetWebhookEvent returns one event with payload, or ErrNotFound.
func (s *Store) GetWebhookEvent(ctx context.Context, id int64) (*model.WebhookEvent, error) {
	e, err := scanEvent(true)(s.queryRow(ctx, `SELECT `+eventCols+` FROM zoom_webhook_events WHERE id = ?`, id))
	if err != nil {
		if err = notFound(err); err == ErrNotFound {
			return nil, err
		}
		return nil, fmt.Errorf("store: get webhook event: %w", err)
	}
	return &e, nil
}

// ResetEventForReprocess puts one event back into the queue: status pending,
// attempts 0, no next attempt, processed_at and error cleared. ErrNotFound when missing.
func (s *Store) ResetEventForReprocess(ctx context.Context, id int64) error {
	if err := s.execOne(ctx, `UPDATE zoom_webhook_events SET processing_status = 'pending', attempts = 0,
		next_attempt_at = NULL, processed_at = NULL, error = '' WHERE id = ?`, id); err != nil {
		if err == ErrNotFound {
			return err
		}
		return fmt.Errorf("store: reset event: %w", err)
	}
	return nil
}

// ResetFailedEvents puts every failed event back into the queue (as
// ResetEventForReprocess) and returns how many.
func (s *Store) ResetFailedEvents(ctx context.Context) (int64, error) {
	n, err := s.execAffected(ctx, `UPDATE zoom_webhook_events SET processing_status = 'pending', attempts = 0,
		next_attempt_at = NULL, processed_at = NULL, error = '' WHERE processing_status = 'failed'`)
	if err != nil {
		return 0, fmt.Errorf("store: reset failed events: %w", err)
	}
	return n, nil
}

// retentionBatch is the id range one retention DELETE covers, so the SQLite
// write lock is released between statements and webhooks keep being stored
// meanwhile.
const retentionBatch = 2000

// PurgeEventsBefore is the raw event retention (EVENTS_RETENTION_DAYS): it
// deletes events received before t that nothing needs any more and returns
// how many. It never deletes
//   - events whose meeting_uuid belongs to a Zoom session linked to a lesson:
//     they are the source of truth of that lesson's attendance, which every
//     rebuild (recompute, manual mapping, settings change, relink) computes
//     from them again — purging them would wipe the attendance;
//   - pending events (not processed yet).
//
// What goes: events of sessions never linked (or unlinked) and events without
// a session (url_validation, unsupported types). Projections are not touched.
// It deletes in batches and stops early (returning ctx's error) when ctx ends.
func (s *Store) PurgeEventsBefore(ctx context.Context, t time.Time) (int64, error) {
	return s.purgeEventsBefore(ctx, t, retentionBatch)
}

// purgeEventsBefore walks the ids of the old events in windows of batch ids.
// The kept events (linked sessions) pile up at the low ids forever; a
// "DELETE ... ORDER BY id LIMIT n" would scan all of them again in every
// statement while holding the SQLite write lock, a window scans at most batch
// rows.
func (s *Store) purgeEventsBefore(ctx context.Context, t time.Time, batch int) (int64, error) {
	cutoff := toMS(t)
	var lo, hi sql.NullInt64
	if err := s.queryRow(ctx, `SELECT MIN(id), MAX(id) FROM zoom_webhook_events WHERE received_at < ?`,
		cutoff).Scan(&lo, &hi); err != nil {
		return 0, fmt.Errorf("store: purge old events: %w", err)
	}
	if !lo.Valid || !hi.Valid {
		return 0, nil
	}
	const q = `DELETE FROM zoom_webhook_events
		WHERE id >= ? AND id < ? AND received_at < ? AND processing_status <> 'pending'
		  AND NOT EXISTS (SELECT 1 FROM zoom_meetings m
		                  WHERE m.meeting_uuid = zoom_webhook_events.meeting_uuid AND m.lesson_id IS NOT NULL)`
	step := int64(max(batch, 1))
	var total int64
	for from := lo.Int64; from <= hi.Int64; from += step {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := s.execAffected(ctx, q, from, from+step, cutoff)
		total += n
		if err != nil {
			return total, fmt.Errorf("store: purge old events: %w", err)
		}
	}
	return total, nil
}

// EventStats feeds the Zoom connection status page.
type EventStats struct {
	LastEventAt   *time.Time // received_at of the newest event (nil when none)
	LastEventType string
	Last24h       int // events received in (now-24h, now]
	Failed        int
	Pending       int
}

// EventStats returns queue and freshness statistics as of now.
func (s *Store) EventStats(ctx context.Context, now time.Time) (EventStats, error) {
	var st EventStats
	var last int64
	err := s.queryRow(ctx, `SELECT received_at, event_type FROM zoom_webhook_events ORDER BY id DESC LIMIT 1`).Scan(&last, &st.LastEventType)
	switch {
	case err == sql.ErrNoRows:
	case err != nil:
		return st, fmt.Errorf("store: event stats: %w", err)
	default:
		t := fromMS(last)
		st.LastEventAt = &t
	}
	counts := []struct {
		dst  *int
		q    string
		args []any
	}{
		{&st.Last24h, `SELECT COUNT(*) FROM zoom_webhook_events WHERE received_at > ?`, []any{toMS(now.Add(-24 * time.Hour))}},
		{&st.Failed, `SELECT COUNT(*) FROM zoom_webhook_events WHERE processing_status = 'failed'`, nil},
		{&st.Pending, `SELECT COUNT(*) FROM zoom_webhook_events WHERE processing_status = 'pending'`, nil},
	}
	for _, c := range counts {
		if err := s.queryRow(ctx, c.q, c.args...).Scan(c.dst); err != nil {
			return st, fmt.Errorf("store: event stats: %w", err)
		}
	}
	return st, nil
}

// pgSafeText makes s storable in a PostgreSQL TEXT column: invalid UTF-8
// sequences and NUL bytes become U+FFFD. Valid strings are returned unchanged
// (no allocation).
func pgSafeText(s string) string {
	if utf8.ValidString(s) && !strings.ContainsRune(s, 0) {
		return s
	}
	return strings.ReplaceAll(strings.ToValidUTF8(s, "\uFFFD"), "\x00", "\uFFFD")
}
