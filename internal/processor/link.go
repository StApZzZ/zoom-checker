package processor

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"zoom-checker/internal/model"
	"zoom-checker/internal/store"
	"zoom-checker/internal/timefmt"
)

// Session ↔ lesson linking (docs/architecture.md §5.5).

// systemUserName is the author of audit entries the processor writes itself.
const systemUserName = "Система"

const (
	// linkAfterEnd is how long after a lesson's planned end a session may
	// start and still be auto-linked to it: a lesson that started a little late
	// or ran over, never an unrelated meeting hours later on the same personal
	// meeting ID (that one would reopen and stretch a finished lesson).
	linkAfterEnd = 30 * time.Minute
	// restartGap is how soon after the previous session of an overrunning
	// lesson ended a new session counts as its restart.
	restartGap = 10 * time.Minute
	// earlyEndSlack: a session that ended more than this before a lesson's
	// planned start was a different meeting (a test call, the meeting before)
	// and must not make the lesson finished before it began.
	earlyEndSlack = 10 * time.Minute
)

// linkRules is how far around a lesson's planned time a session may start to
// be auto-linked to it: up to before ahead of the planned start (setting
// link_window_hours) and up to after past the planned end.
type linkRules struct{ before, after time.Duration }

func rulesFor(s model.Settings) linkRules {
	return linkRules{before: time.Duration(max(s.LinkWindowHours, 0)) * time.Hour, after: linkAfterEnd}
}

// sessionRef is the moment a session is matched against planned lesson times:
// its start, or its earliest event when meeting.started was not (yet) received.
func sessionRef(s *model.ZoomMeeting) time.Time {
	if s.StartedAt != nil {
		return *s.StartedAt
	}
	return s.FirstEventAt
}

// Candidate tiers, best first (docs/architecture.md §5.5).
const (
	tierRunning  = iota // the session started inside the lesson's planned time
	tierRestart         // restart of an overrunning lesson (its session ended just before)
	tierUpcoming        // the lesson starts soon (the meeting was opened early)
	tierPast            // the lesson ended a little before (started late, ran over)
)

// bestCandidate returns the lesson a session would be auto-linked to, nil when
// there is none. Candidates have the session's Zoom meeting id and planned
// times with planned_start − before ≤ ref ≤ planned_end + after; a lesson the
// session ended well before is skipped. They are ranked by tier (running,
// restart, upcoming, past), then by distance (to the planned start for running
// and upcoming lessons, to the planned end otherwise), then by lower id. So a
// restart in the second half of a lesson stays with it instead of going to the
// next lesson on the same meeting, and a meeting opened in a break goes to the
// lesson that is about to start.
func bestCandidate(ctx context.Context, tx *store.Store, s *model.ZoomMeeting, rules linkRules) (*model.Lesson, error) {
	if s.MeetingID == "" {
		return nil, nil
	}
	ref := sessionRef(s)
	cands, err := tx.FindLessonsByMeeting(ctx, s.MeetingID, ref.Add(-rules.after), ref.Add(rules.before)) // = CandidateRange
	if err != nil {
		return nil, err
	}
	var (
		best     *model.Lesson
		bestTier int
		bestDist time.Duration
	)
	for i := range cands {
		l := &cands[i]
		if s.EndedAt != nil && s.EndedAt.Before(l.PlannedStartAt.Add(-earlyEndSlack)) {
			continue
		}
		var tier int
		var dist time.Duration
		switch {
		case ref.Before(l.PlannedStartAt):
			tier, dist = tierUpcoming, l.PlannedStartAt.Sub(ref)
		case !ref.After(l.PlannedEndAt):
			tier, dist = tierRunning, ref.Sub(l.PlannedStartAt)
		default:
			tier, dist = tierPast, ref.Sub(l.PlannedEndAt)
			restart, err := isRestartOf(ctx, tx, s, l, ref)
			if err != nil {
				return nil, err
			}
			if restart {
				tier = tierRestart
			}
		}
		if best == nil || tier < bestTier || (tier == bestTier && (dist < bestDist || (dist == bestDist && l.ID < best.ID))) {
			best, bestTier, bestDist = l, tier, dist
		}
	}
	return best, nil
}

// isRestartOf reports whether session s, starting at ref after lesson l's
// planned end, continues l: another session of l ended after the planned end
// (the lesson was running over) within restartGap of ref.
func isRestartOf(ctx context.Context, tx *store.Store, s *model.ZoomMeeting, l *model.Lesson, ref time.Time) (bool, error) {
	linked, err := tx.ListZoomMeetingsByLesson(ctx, l.ID)
	if err != nil {
		return false, err
	}
	for i := range linked {
		o := &linked[i]
		if o.ID == s.ID || o.EndedAt == nil || !o.EndedAt.After(l.PlannedEndAt) {
			continue
		}
		if gap := ref.Sub(*o.EndedAt); gap <= restartGap && gap >= -restartGap {
			return true, nil
		}
	}
	return false, nil
}

// linkOnEvent resolves the lesson of a session after one of its events (§5.5 steps
// 2–3): an existing link wins, a manual unlink is final, otherwise auto-link.
// An auto-linked session whose meeting.ended shows it ended well before its
// lesson's planned start is handed to its best candidate or unlinked: it was
// another meeting on the same Zoom ID and must not finish the lesson early.
func (p *Processor) linkOnEvent(ctx context.Context, tx *store.Store, s *model.ZoomMeeting, settings model.Settings,
	fx *effects) (*int64, error) {
	if s.LessonID != nil {
		if s.LinkMode != model.LinkAuto || s.EndedAt == nil {
			return s.LessonID, nil
		}
		l, err := tx.GetLesson(ctx, *s.LessonID)
		if err != nil {
			return nil, err
		}
		if !s.EndedAt.Before(l.PlannedStartAt.Add(-earlyEndSlack)) {
			return s.LessonID, nil
		}
		return p.relinkAuto(ctx, tx, s, rulesFor(settings), "zoom session unlinked: it ended before the lesson began", fx)
	}
	if s.LinkMode == model.LinkManual {
		return nil, nil // unlinked by a person: never auto-link again
	}
	l, err := bestCandidate(ctx, tx, s, rulesFor(settings))
	if err != nil || l == nil {
		return nil, err
	}
	if err := p.attach(ctx, tx, s, l, model.LinkAuto, fx); err != nil {
		return nil, err
	}
	return &l.ID, nil
}

// relinkAuto moves auto-linked session s to its best candidate, or unlinks it
// (auto mode, so a better fitting lesson can still claim it) when there is
// none. The lesson it leaves is rebuilt too. It returns the new lesson id.
func (p *Processor) relinkAuto(ctx context.Context, tx *store.Store, s *model.ZoomMeeting, rules linkRules,
	unlinkMsg string, fx *effects) (*int64, error) {
	best, err := bestCandidate(ctx, tx, s, rules)
	if err != nil {
		return nil, err
	}
	if best != nil {
		if s.LessonID != nil && *s.LessonID == best.ID {
			return s.LessonID, nil
		}
		if err := p.attach(ctx, tx, s, best, model.LinkAuto, fx); err != nil {
			return nil, err
		}
		return &best.ID, nil
	}
	if s.LessonID == nil {
		return nil, nil
	}
	prev := *s.LessonID
	if err := tx.SetZoomMeetingLink(ctx, s.ID, nil, model.LinkAuto); err != nil {
		return nil, fmt.Errorf("unlink session %d: %w", s.ID, err)
	}
	s.LessonID, s.LinkMode = nil, model.LinkAuto
	fx.lessons[prev] = true
	fx.touched = true
	fx.info(unlinkMsg, "lesson_id", prev, "meeting_id", s.MeetingID, "meeting_uuid", s.MeetingUUID)
	return nil, nil
}

// attach links session s to lesson l with the given mode. A lesson the teacher
// finished manually is reopened when the new session started after the finish
// (e.g. the meeting was restarted to continue the class).
func (p *Processor) attach(ctx context.Context, tx *store.Store, s *model.ZoomMeeting, l *model.Lesson,
	mode model.LinkMode, fx *effects) error {
	if err := tx.SetZoomMeetingLink(ctx, s.ID, &l.ID, mode); err != nil {
		return fmt.Errorf("link session %d to lesson %d: %w", s.ID, l.ID, err)
	}
	prev := s.LessonID
	s.LessonID, s.LinkMode = &l.ID, mode
	fx.lessons[l.ID] = true
	fx.touched = true
	fx.info("zoom session linked", "meeting_id", s.MeetingID, "meeting_uuid", s.MeetingUUID, "lesson_id", l.ID,
		"link_mode", string(mode))
	if prev != nil && *prev != l.ID {
		fx.lessons[*prev] = true
	}

	if l.ManualFinishedAt == nil || !sessionRef(s).After(*l.ManualFinishedAt) {
		return nil
	}
	finished := *l.ManualFinishedAt
	if err := tx.SetLessonManualFinished(ctx, l.ID, nil); err != nil {
		return fmt.Errorf("reopen lesson %d: %w", l.ID, err)
	}
	l.ManualFinishedAt = nil
	lessonID, groupID := l.ID, l.GroupID
	// Same wording as a teacher's reopen (httpapi), so the history reads alike.
	if err := tx.InsertAudit(ctx, &model.AuditEntry{
		At:            p.now().UTC(),
		UserName:      systemUserName,
		Action:        model.AuditLessonReopen,
		EntityType:    "lesson",
		EntityID:      strconv.FormatInt(l.ID, 10),
		LessonID:      &lessonID,
		GroupID:       &groupID,
		PreviousValue: "Завершено вручную: " + p.fmtTime(finished),
		NewValue:      "Занятие возобновлено автоматически",
		Reason: "Конференция в Zoom запущена снова (" + p.fmtTime(sessionRef(s)) +
			") после того, как занятие завершили",
	}); err != nil {
		return fmt.Errorf("reopen lesson %d: audit: %w", l.ID, err)
	}
	fx.info("lesson reopened: a new zoom session started after the manual finish", "lesson_id", l.ID,
		"meeting_uuid", s.MeetingUUID)
	return nil
}

// fmtTime formats t for user-facing texts in the configured time zone with
// its offset ("06.10.2026 12:31 (UTC+3)", like the HTTP layer).
func (p *Processor) fmtTime(t time.Time) string {
	return timefmt.DateTime(t, p.loc)
}

// RelinkLesson re-evaluates session links after a lesson was created or edited
// (§5.5 step 4), then rebuilds every affected lesson and publishes:
//   - auto-linked sessions of this lesson whose best candidate is no longer this
//     lesson (meeting id changed, out of the window, another lesson is closer) are
//     moved to their best candidate or unlinked;
//   - unlinked auto-mode sessions with the lesson's meeting id whose best candidate
//     is this lesson are linked (lesson created after the meeting started);
//   - auto-linked sessions of nearby lessons with the same meeting id whose best
//     candidate is now this lesson move here (the lesson that fits better was
//     created late).
//
// Manually linked or unlinked sessions are never touched. ErrNotFound when the
// lesson does not exist.
func (p *Processor) RelinkLesson(ctx context.Context, lessonID int64) error {
	return p.inTx(ctx, func(tx *store.Store, now time.Time, fx *effects) error {
		lesson, err := tx.GetLesson(ctx, lessonID)
		if err != nil {
			return fmt.Errorf("processor: relink lesson %d: %w", lessonID, err)
		}
		settings, err := tx.GetSettings(ctx)
		if err != nil {
			return fmt.Errorf("processor: relink lesson %d: settings: %w", lessonID, err)
		}
		rules := rulesFor(settings)
		fx.lessons[lessonID] = true
		fx.touched = true

		// 1. Sessions of this lesson that no longer belong here.
		linked, err := tx.ListZoomMeetingsByLesson(ctx, lessonID)
		if err != nil {
			return err
		}
		for i := range linked {
			if linked[i].LinkMode != model.LinkAuto {
				continue
			}
			if _, err := p.relinkAuto(ctx, tx, &linked[i], rules, "zoom session unlinked: it no longer matches the lesson", fx); err != nil {
				return err
			}
		}

		// 2. Unlinked sessions of this meeting id (lesson created after the meeting).
		free, err := tx.ListLinkableMeetings(ctx, lesson.ZoomMeetingID)
		if err != nil {
			return err
		}
		for i := range free {
			if err := p.claim(ctx, tx, &free[i], lesson, rules, fx); err != nil {
				return err
			}
		}

		// 3. Sessions auto-linked to nearby lessons that fit this one better. A session
		// can only be a candidate of both when their link ranges overlap.
		from, to := nearbyRange(lesson, rules)
		near, err := tx.FindLessonsByMeeting(ctx, lesson.ZoomMeetingID, from, to)
		if err != nil {
			return err
		}
		for _, other := range near {
			if other.ID == lessonID {
				continue
			}
			sessions, err := tx.ListZoomMeetingsByLesson(ctx, other.ID)
			if err != nil {
				return err
			}
			for i := range sessions {
				if sessions[i].LinkMode != model.LinkAuto {
					continue
				}
				if err := p.claim(ctx, tx, &sessions[i], lesson, rules, fx); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// nearbyRange is the planned-time range of lessons whose link candidates may
// overlap l's: a session starting in [l.start − before, l.end + after] can be a
// candidate of any lesson whose planned time overlaps that range widened once
// more by after and before.
func nearbyRange(l *model.Lesson, rules linkRules) (from, to time.Time) {
	span := rules.before + rules.after
	return l.PlannedStartAt.Add(-span), l.PlannedEndAt.Add(span)
}

// NearbyRange is the planned-time range (for store.FindLessonsByMeeting) of the
// lessons that may compete with a lesson planned [start, end] for the same Zoom
// sessions: after such a lesson was deleted, these get RelinkLesson.
func NearbyRange(s model.Settings, start, end time.Time) (from, to time.Time) {
	return nearbyRange(&model.Lesson{PlannedStartAt: start, PlannedEndAt: end}, rulesFor(s))
}

// CandidateRange is the planned-time range (for store.FindLessonsByMeeting) of
// the lessons a session starting at ref may be auto-linked to.
func CandidateRange(s model.Settings, ref time.Time) (from, to time.Time) {
	rules := rulesFor(s)
	return ref.Add(-rules.after), ref.Add(rules.before)
}

// claim links s to lesson when lesson is its best candidate.
func (p *Processor) claim(ctx context.Context, tx *store.Store, s *model.ZoomMeeting, lesson *model.Lesson,
	rules linkRules, fx *effects) error {
	best, err := bestCandidate(ctx, tx, s, rules)
	if err != nil || best == nil || best.ID != lesson.ID {
		return err
	}
	// attach may clear the manual finish on the shared lesson value, so later
	// sessions compare against the current state.
	return p.attach(ctx, tx, s, lesson, model.LinkAuto, fx)
}

// LinkSession manually links/unlinks a Zoom session and rebuilds affected lessons.
// lessonID nil unlinks for good (link_mode manual: never auto-linked again).
// ErrNotFound when the session or the lesson does not exist.
func (p *Processor) LinkSession(ctx context.Context, sessionID int64, lessonID *int64) error {
	return p.inTx(ctx, func(tx *store.Store, now time.Time, fx *effects) error {
		s, err := tx.GetZoomMeeting(ctx, sessionID)
		if err != nil {
			return fmt.Errorf("processor: link session %d: %w", sessionID, err)
		}
		fx.touched = true
		if s.LessonID != nil {
			fx.lessons[*s.LessonID] = true
		}
		if lessonID == nil {
			if err := tx.SetZoomMeetingLink(ctx, s.ID, nil, model.LinkManual); err != nil {
				return fmt.Errorf("processor: unlink session %d: %w", sessionID, err)
			}
			fx.info("zoom session unlinked by a user", "meeting_id", s.MeetingID, "meeting_uuid", s.MeetingUUID)
			return nil
		}
		lesson, err := tx.GetLesson(ctx, *lessonID)
		if err != nil {
			return fmt.Errorf("processor: link session %d: lesson %d: %w", sessionID, *lessonID, err)
		}
		return p.attach(ctx, tx, s, lesson, model.LinkManual, fx)
	})
}

// inTx runs fn in one processor transaction (writeTx), rebuilds the lessons fn
// marked as affected in the same transaction, and after the commit logs and
// publishes.
func (p *Processor) inTx(ctx context.Context, fn func(tx *store.Store, now time.Time, fx *effects) error) error {
	var fx *effects
	err := p.writeTx(ctx, func(tx *store.Store) error {
		fx = newEffects()
		now := p.now().UTC()
		if err := fn(tx, now, fx); err != nil {
			return err
		}
		return p.rebuildAffected(ctx, tx, now, fx)
	})
	if err != nil {
		return err
	}
	p.finish(fx)
	return nil
}
