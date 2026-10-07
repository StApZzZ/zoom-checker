// Package processor turns stored raw Zoom webhooks into attendance projections
// (docs/architecture.md §5.5–§5.7).
//
// One worker goroutine (Run) takes due raw events from zoom_webhook_events, upserts
// the Zoom session (zoom_meetings) they belong to, links the session to a lesson and
// rebuilds that lesson's projection, all in one transaction per batch, then pokes
// the realtime broker. The HTTP layer calls RebuildLesson / RelinkLesson /
// LinkSession after teacher actions.
//
// Locking: every processor transaction runs under one in-process mutex that is
// always taken BEFORE the transaction begins. SQLite has a single writer and takes
// the write lock at BEGIN; taking a Go lock while already holding the write lock
// (a "per-lesson mutex" acquired inside the worker transaction) would deadlock
// against an API goroutine that holds that lock and waits for BEGIN. Holding the
// mutex until commit also makes concurrent rebuilds of the same lesson strictly
// ordered on PostgreSQL (READ COMMITTED), so a stale rebuild can never overwrite a
// newer one. On SQLite transactions are serialized anyway, so nothing is lost.
// Inside a transaction only the tx Store is used, and the processor yields the
// SQLite write lock regularly under sustained load so writers outside it (the
// webhook ingest) are not starved (write.go).
//
// The exported methods open their own transaction: never call them while holding
// a store transaction (on SQLite the inner BEGIN would wait for the outer one).
package processor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"zoom-checker/internal/model"
	"zoom-checker/internal/realtime"
	"zoom-checker/internal/store"
	"zoom-checker/internal/zoom"
)

const (
	// BatchSize is how many due events one ProcessPending call takes.
	BatchSize = 100
	// DefaultPollInterval is how often Run looks for due events without a Notify
	// (retries whose backoff expired, events stored by another process).
	DefaultPollInterval = 5 * time.Second
	// MaxAttempts is the number of failed attempts after which an event is marked
	// failed (an admin can put it back with "reprocess").
	MaxAttempts = 6

	// Under a backlog one worker transaction keeps taking the next BatchSize due
	// events until the backlog is drained, maxTxEvents were handled or txBudget
	// has passed, and only then rebuilds the affected lessons: a rebuild replays
	// every event of the lesson, so rebuilding after every BatchSize events made
	// a burst on a large lesson quadratic. The budget keeps the SQLite write lock
	// short enough for the webhook endpoint (write.go) and the live view fresh.
	maxTxEvents = 2000
	txBudget    = 200 * time.Millisecond

	maxErrorRunes = 1000
)

// Option configures a Processor.
type Option func(*Processor)

// WithClock replaces time.Now (tests).
func WithClock(now func() time.Time) Option {
	return func(p *Processor) {
		if now != nil {
			p.now = now
		}
	}
}

// WithLocation sets the time zone (config TIMEZONE) of times written into
// user-facing texts, e.g. the audit entry of an automatic reopen. Default UTC,
// shown with a "UTC" marker so it is never mistaken for local time.
func WithLocation(loc *time.Location) Option {
	return func(p *Processor) {
		if loc != nil {
			p.loc = loc
		}
	}
}

// WithPollInterval changes how often Run wakes up without a Notify (tests).
func WithPollInterval(d time.Duration) Option {
	return func(p *Processor) {
		if d > 0 {
			p.poll = d
		}
	}
}

// Processor is the single webhook worker plus lesson projection rebuilds.
type Processor struct {
	st     *store.Store
	broker *realtime.Broker
	log    *slog.Logger
	now    func() time.Time
	loc    *time.Location
	poll   time.Duration
	wake   chan struct{}
	// txBudget bounds the time one worker transaction spends taking more due
	// events (default txBudget; tests raise it).
	txBudget time.Duration

	// runMu serializes ProcessPending calls (the Run loop and direct callers), so
	// the same due event is never handled twice concurrently.
	runMu sync.Mutex
	// mu serializes processor transactions; see the package comment. Lock order:
	// runMu -> mu -> database transaction. Never taken inside a transaction.
	mu sync.Mutex
	// gate keeps the processor from monopolizing the SQLite write lock (write.go).
	gate writeGate
}

// New creates a processor. Call Run in a goroutine. broker may be nil (no
// realtime pokes); log nil means slog.Default().
func New(st *store.Store, broker *realtime.Broker, log *slog.Logger, opts ...Option) *Processor {
	if log == nil {
		log = slog.Default()
	}
	p := &Processor{
		st:       st,
		broker:   broker,
		log:      log,
		now:      time.Now,
		loc:      time.UTC,
		poll:     DefaultPollInterval,
		wake:     make(chan struct{}, 1),
		gate:     newWriteGate(st),
		txBudget: txBudget,
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Location is the time zone of times in user-facing texts (WithLocation).
func (p *Processor) Location() *time.Location { return p.loc }

// Run processes due events until ctx is cancelled: once at start, then on every
// Notify and every poll interval (5 s). Running lessons are looked after once at
// start and every minute (upkeep). It always returns nil.
func (p *Processor) Run(ctx context.Context) error {
	t := time.NewTicker(p.poll)
	defer t.Stop()
	var lastUpkeep time.Time
	for {
		p.drain(ctx)
		if ctx.Err() == nil && time.Since(lastUpkeep) >= upkeepEvery {
			lastUpkeep = time.Now()
			if err := p.upkeep(ctx); err != nil && ctx.Err() == nil {
				p.log.Error("running lessons upkeep failed, will retry", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-p.wake:
		case <-t.C:
		}
	}
}

// drain processes batches until no full batch is left. Back-to-back batches leave
// room for other writers through writeTx's yielding.
func (p *Processor) drain(ctx context.Context) {
	for ctx.Err() == nil {
		n, err := p.ProcessPending(ctx)
		if err != nil {
			if ctx.Err() == nil {
				p.log.Error("zoom events: processing stopped, will retry", "err", err)
			}
			return
		}
		if n < BatchSize {
			return
		}
	}
}

// Notify wakes the worker (non-blocking; pokes coalesce).
func (p *Processor) Notify() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// ProcessPending processes due events synchronously and returns how many were
// handled (given a final status, or a failed attempt recorded).
//
// The events are first tried in a single transaction that takes further batches
// while the backlog lasts (maxTxEvents, txBudget) and rebuilds every affected
// lesson once at its end (fast under bursts). If that fails, the first batch is
// retried one event per transaction so a bad event cannot hold back the others;
// only then do failures count as attempts (backoff 2^attempts s, failed after
// MaxAttempts). Events of later batches stay pending for the next call.
func (p *Processor) ProcessPending(ctx context.Context) (int, error) {
	p.runMu.Lock()
	defer p.runMu.Unlock()

	now := p.now().UTC()
	evs, err := p.st.ListDueEvents(ctx, now, BatchSize)
	if err != nil {
		return 0, fmt.Errorf("processor: list due events: %w", err)
	}
	if len(evs) == 0 {
		return 0, nil
	}
	if len(evs) > 1 {
		n, err := p.runEvents(ctx, evs, now, true)
		if err == nil {
			return n, nil
		}
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		p.log.Warn("zoom events: batch failed, retrying one by one", "events", len(evs), "err", err)
	}
	for i := range evs {
		if ctx.Err() != nil {
			return i, ctx.Err()
		}
		_, err := p.runEvents(ctx, evs[i:i+1], now, false)
		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			return i, ctx.Err()
		}
		if rerr := p.recordFailure(ctx, &evs[i], now, err); rerr != nil {
			return i, rerr
		}
	}
	return len(evs), nil
}

// runEvents handles evs in one transaction: every event gets its final status,
// then each affected lesson is rebuilt once. With more, the transaction goes on
// with the next due events while evs was a full batch (see maxTxEvents). Logs
// and realtime pokes happen only after the commit. It returns how many events
// got their status.
func (p *Processor) runEvents(ctx context.Context, evs []model.WebhookEvent, now time.Time, more bool) (int, error) {
	var (
		fx      *effects
		handled int
	)
	err := p.writeTx(ctx, func(tx *store.Store) error {
		fx, handled = newEffects(), 0
		settings, err := tx.GetSettings(ctx)
		if err != nil {
			return fmt.Errorf("processor: load settings: %w", err)
		}
		start := time.Now()
		for batch := evs; len(batch) > 0; {
			for i := range batch {
				if err := p.handleEvent(ctx, tx, &batch[i], settings, now, fx); err != nil {
					return err
				}
			}
			handled += len(batch)
			if !more || len(batch) < BatchSize || handled >= maxTxEvents || time.Since(start) >= p.txBudget {
				break
			}
			// Handled events are no longer pending, so this is the next page.
			if batch, err = tx.ListDueEvents(ctx, now, min(BatchSize, maxTxEvents-handled)); err != nil {
				return fmt.Errorf("processor: list due events: %w", err)
			}
		}
		return p.rebuildAffected(ctx, tx, now, fx)
	})
	if err != nil {
		return 0, err
	}
	p.finish(fx)
	return handled, nil
}

// handleEvent decides the outcome of one raw event inside tx and stores its status.
func (p *Processor) handleEvent(ctx context.Context, tx *store.Store, raw *model.WebhookEvent, settings model.Settings,
	now time.Time, fx *effects) error {
	res, err := p.applyEvent(ctx, tx, raw, settings, fx)
	if err != nil {
		return fmt.Errorf("processor: event %d (%s): %w", raw.ID, raw.EventType, err)
	}
	if err := tx.UpdateEventStatus(ctx, raw.ID, res.status, raw.Attempts, nil, &now, res.note); err != nil {
		return fmt.Errorf("processor: event %d: store status: %w", raw.ID, err)
	}
	attrs := []any{
		"event_id", raw.ID, "event_type", raw.EventType, "status", string(res.status),
		"meeting_id", raw.MeetingID, "meeting_uuid", raw.MeetingUUID,
	}
	if raw.ParticipantName != "" {
		attrs = append(attrs, "participant", raw.ParticipantName)
	}
	if res.lessonID != nil {
		attrs = append(attrs, "lesson_id", *res.lessonID)
	}
	if res.note != "" {
		attrs = append(attrs, "reason", res.note)
	}
	fx.info("zoom event handled", attrs...)
	return nil
}

// outcome is the result of applying one event.
type outcome struct {
	status   model.EventStatus
	note     string // explanation stored in the error column (ignored events)
	lessonID *int64 // lesson the event's session is linked to
}

// applyEvent implements the worker step of docs §5.7 for one event: dedupe,
// session upsert, linking (§5.5). The lesson rebuild is scheduled in fx.
func (p *Processor) applyEvent(ctx context.Context, tx *store.Store, raw *model.WebhookEvent, settings model.Settings,
	fx *effects) (outcome, error) {
	dup, err := tx.HasEarlierWithDedupeKey(ctx, raw.DedupeKey, raw.ID)
	if err != nil {
		return outcome{}, err
	}
	if dup {
		return outcome{status: model.EventDuplicate}, nil
	}

	ev, perr := parseEvent([]byte(raw.Payload))
	if perr != nil {
		return outcome{status: model.EventIgnored, note: noteParseError(perr)}, nil
	}
	if note := unsupportedNote(ev); note != "" {
		return outcome{status: model.EventIgnored, note: note}, nil
	}

	existing, err := tx.GetZoomMeetingByUUID(ctx, ev.MeetingUUID)
	if errors.Is(err, store.ErrNotFound) {
		existing, err = nil, nil
	}
	if err != nil {
		return outcome{}, err
	}
	if existing == nil && ev.MeetingID == "" && zoom.IsParticipantEvent(ev.Type) {
		return outcome{status: model.EventIgnored, note: noteUnknownSession}, nil
	}

	sess, err := tx.UpsertZoomMeeting(ctx, sessionUpdate(ev, raw, existing))
	if err != nil {
		return outcome{}, err
	}
	// No realtime poke for the session itself: lesson lists only change through a
	// linked lesson (published below), and events of Zoom meetings that are no
	// lesson (account-wide webhooks) must not make every dashboard refetch.
	if existing == nil {
		fx.info("zoom session first seen", "meeting_id", sess.MeetingID, "meeting_uuid", sess.MeetingUUID)
	}

	lessonID, err := p.linkOnEvent(ctx, tx, sess, settings, fx)
	if err != nil {
		return outcome{}, err
	}
	if lessonID != nil {
		fx.lessons[*lessonID] = true
	}
	return outcome{status: model.EventProcessed, lessonID: lessonID}, nil
}

// sessionUpdate builds the zoom_meetings merge for ev. started_at comes only from
// meeting.started (or meeting.ended's start_time when nothing set it), ended_at
// only from meeting.ended; the store never overwrites values with empty ones.
func sessionUpdate(ev *zoom.Event, raw *model.WebhookEvent, existing *model.ZoomMeeting) *model.ZoomMeeting {
	occurred := ev.OccurredAt()
	if occurred.IsZero() {
		occurred = raw.ReceivedAt
	}
	m := &model.ZoomMeeting{
		MeetingUUID:  ev.MeetingUUID,
		MeetingID:    ev.MeetingID,
		Topic:        ev.Topic,
		HostID:       ev.HostID,
		FirstEventAt: occurred,
	}
	switch ev.Type {
	case zoom.EventMeetingStarted:
		m.StartedAt = &occurred
	case zoom.EventMeetingEnded:
		m.EndedAt = &occurred
		if (existing == nil || existing.StartedAt == nil) && ev.StartTime != nil && !ev.StartTime.IsZero() {
			st := ev.StartTime.UTC()
			m.StartedAt = &st
			if st.Before(m.FirstEventAt) {
				m.FirstEventAt = st
			}
		}
	}
	return m
}

// recordFailure counts a failed attempt: backoff 2^attempts seconds, failed after
// MaxAttempts. The raw row is never lost.
func (p *Processor) recordFailure(ctx context.Context, raw *model.WebhookEvent, now time.Time, cause error) error {
	attempts := raw.Attempts + 1
	note := truncateRunes("Не удалось обработать событие: "+cause.Error(), maxErrorRunes)
	attrs := []any{"event_id", raw.ID, "event_type", raw.EventType, "meeting_id", raw.MeetingID,
		"meeting_uuid", raw.MeetingUUID, "attempt", attempts, "err", cause}
	// Written through writeTx like every processor write, so it queues behind the
	// API's rebuilds instead of polling SQLite's busy handler.
	setStatus := func(status model.EventStatus, next *time.Time) error {
		err := p.writeTx(ctx, func(tx *store.Store) error {
			return tx.UpdateEventStatus(ctx, raw.ID, status, attempts, next, nil, note)
		})
		if err != nil {
			return fmt.Errorf("processor: record failure of event %d: %w", raw.ID, err)
		}
		return nil
	}
	if attempts >= MaxAttempts {
		if err := setStatus(model.EventFailed, nil); err != nil {
			return err
		}
		p.log.Error("zoom event failed, giving up", attrs...)
		return nil
	}
	next := now.Add(time.Duration(1<<attempts) * time.Second)
	if err := setStatus(model.EventPending, &next); err != nil {
		return err
	}
	p.log.Error("zoom event failed, will retry", append(attrs, "next_attempt_at", next)...)
	return nil
}

// effects collects what a transaction changed, to act on only after it commits.
type effects struct {
	lessons map[int64]bool // lessons to rebuild (inside the tx) and to publish
	touched bool           // some session or lesson changed: poke the lessons list
	logs    []func(*slog.Logger)
}

func newEffects() *effects { return &effects{lessons: make(map[int64]bool)} }

// info queues an info log line for after the commit, so a rolled-back attempt
// never claims changes that did not happen.
func (fx *effects) info(msg string, attrs ...any) {
	fx.logs = append(fx.logs, func(l *slog.Logger) { l.Info(msg, attrs...) })
}

func (fx *effects) lessonIDs() []int64 {
	ids := make([]int64, 0, len(fx.lessons))
	for id := range fx.lessons {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// rebuildAffected rebuilds every lesson in fx (in id order) inside tx.
func (p *Processor) rebuildAffected(ctx context.Context, tx *store.Store, now time.Time, fx *effects) error {
	for _, id := range fx.lessonIDs() {
		if err := p.rebuildTx(ctx, tx, id, now, fx); err != nil {
			return err
		}
	}
	return nil
}

// finish emits the queued logs and publishes realtime topics after a commit.
func (p *Processor) finish(fx *effects) {
	for _, l := range fx.logs {
		l(p.log)
	}
	if p.broker == nil {
		return
	}
	ids := fx.lessonIDs()
	if len(ids) == 0 && !fx.touched {
		return
	}
	topics := make([]string, 0, len(ids)+1)
	for _, id := range ids {
		topics = append(topics, realtime.LessonTopic(id))
	}
	p.broker.Publish(append(topics, realtime.TopicLessons)...)
}
