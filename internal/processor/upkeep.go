package processor

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"zoom-checker/internal/model"
	"zoom-checker/internal/store"
)

// Upkeep of running lessons that receive no Zoom events (docs §5.6).

const (
	// upkeepEvery is how often Run looks after running lessons.
	upkeepEvery = time.Minute
	// overdueAfter: a running lesson whose planned end passed this long ago
	// and whose open Zoom sessions have been silent for as long is finished
	// automatically. Zoom's meeting.ended was lost (the server was unreachable
	// past Zoom's retries, or the event is not subscribed): without this the
	// lesson stayed "running" for good, its roster never froze and the time of
	// students whose leave was lost too kept growing in exports.
	overdueAfter = 6 * time.Hour
)

// upkeep runs once at start and every upkeepEvery:
//   - with the minimum-percent rule, a student who stays in Zoom becomes
//     present once enough time has passed, without any new event: lessons
//     with such a student are rebuilt so the stored status, the live screen,
//     lesson lists and exports agree;
//   - overdue lessons are finished (finishOverdue).
func (p *Processor) upkeep(ctx context.Context) error {
	settings, err := p.st.GetSettings(ctx)
	if err != nil {
		return fmt.Errorf("processor: upkeep: settings: %w", err)
	}
	if settings.PresentMinPercent > 0 {
		ids, err := p.st.ListLessonsOnlineAbsent(ctx)
		if err != nil {
			return fmt.Errorf("processor: upkeep: %w", err)
		}
		if len(ids) > 0 {
			if err := p.inTx(ctx, func(_ *store.Store, _ time.Time, fx *effects) error {
				for _, id := range ids {
					fx.lessons[id] = true
				}
				return nil
			}); err != nil {
				return fmt.Errorf("processor: upkeep: rebuild running lessons: %w", err)
			}
		}
	}
	overdue, err := p.st.ListActiveLessonsEndedBefore(ctx, p.now().UTC().Add(-overdueAfter))
	if err != nil {
		return fmt.Errorf("processor: upkeep: %w", err)
	}
	for i := range overdue {
		if err := p.finishOverdue(ctx, overdue[i].ID); err != nil {
			return fmt.Errorf("processor: upkeep: lesson %d: %w", overdue[i].ID, err)
		}
	}
	return nil
}

// finishOverdue finishes a running lesson whose open sessions have been silent
// for overdueAfter past its planned end. The end is the latest moment known to
// belong to the lesson: its planned end, the start of a session or the last
// Zoom event of its open sessions, so students who were still in Zoom get
// their time up to there. It is stored like a teacher's finish (with an audit
// entry), so «Возобновить» and a later meeting.ended work as usual.
func (p *Processor) finishOverdue(ctx context.Context, lessonID int64) error {
	return p.inTx(ctx, func(tx *store.Store, now time.Time, fx *effects) error {
		l, err := tx.GetLesson(ctx, lessonID)
		if err != nil {
			return err
		}
		if l.Status != model.LessonActive || l.ManualFinishedAt != nil || now.Sub(l.PlannedEndAt) < overdueAfter {
			return nil
		}
		sessions, err := tx.ListZoomMeetingsByLesson(ctx, lessonID)
		if err != nil {
			return err
		}
		end := l.PlannedEndAt
		var open []string
		for i := range sessions {
			s := &sessions[i]
			if s.EndedAt != nil {
				if s.EndedAt.After(end) {
					end = *s.EndedAt
				}
				continue
			}
			open = append(open, s.MeetingUUID)
			if ref := sessionRef(s); ref.After(end) {
				end = ref
			}
		}
		if len(open) == 0 {
			return nil
		}
		last, err := tx.LatestEventAt(ctx, open)
		if err != nil {
			return err
		}
		if last.After(end) {
			end = last
		}
		if now.Sub(end) < overdueAfter {
			return nil // something happened recently: the meeting may really still run
		}
		end = end.UTC().Truncate(time.Millisecond)
		if err := tx.SetLessonManualFinished(ctx, lessonID, &end); err != nil {
			return err
		}
		id, gid := l.ID, l.GroupID
		if err := tx.InsertAudit(ctx, &model.AuditEntry{
			At:         now,
			UserName:   systemUserName,
			Action:     model.AuditLessonFinish,
			EntityType: "lesson",
			EntityID:   strconv.FormatInt(id, 10),
			LessonID:   &id,
			GroupID:    &gid,
			NewValue:   "Завершено автоматически: " + p.fmtTime(end),
			Reason: "Zoom не сообщил, что конференция закончилась, и после " + p.fmtTime(end) +
				" в ней больше ничего не происходило",
		}); err != nil {
			return fmt.Errorf("audit: %w", err)
		}
		fx.lessons[lessonID] = true
		fx.touched = true
		fx.info("lesson finished automatically: zoom never reported the end of its meeting", "lesson_id", lessonID,
			"finished_at", end)
		return nil
	})
}
