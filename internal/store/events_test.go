package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"zoom-checker/internal/db"
	"zoom-checker/internal/model"
)

func TestZoomMeetings(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		g := e.group("G")
		l := e.lesson(g.ID, "123456789", t0, time.Hour)

		first := t0.Add(5 * time.Minute)
		m, err := e.s.UpsertZoomMeeting(e.ctx, &model.ZoomMeeting{MeetingUUID: "uuid==1", MeetingID: "123456789", Topic: "Урок",
			FirstEventAt: first})
		e.must(err)
		if m.ID == 0 || m.LinkMode != model.LinkAuto || m.LessonID != nil || m.StartedAt != nil || !m.FirstEventAt.Equal(first) ||
			m.Topic != "Урок" || m.HostID != "" {
			t.Fatalf("inserted = %+v", m)
		}
		// Merge: empty strings and nil times never erase; earlier first event wins.
		started := t0.Add(2 * time.Minute)
		e.clock.Advance(time.Minute)
		m2, err := e.s.UpsertZoomMeeting(e.ctx, &model.ZoomMeeting{MeetingUUID: "uuid==1", HostID: "host-1", StartedAt: &started,
			FirstEventAt: started})
		e.must(err)
		if m2.ID != m.ID || m2.MeetingID != "123456789" || m2.Topic != "Урок" || m2.HostID != "host-1" ||
			!m2.FirstEventAt.Equal(started) || m2.StartedAt == nil || !m2.StartedAt.Equal(started) || !m2.UpdatedAt.After(m2.CreatedAt) {
			t.Errorf("merged = %+v", m2)
		}
		ended := t0.Add(70 * time.Minute)
		m3, err := e.s.UpsertZoomMeeting(e.ctx, &model.ZoomMeeting{MeetingUUID: "uuid==1", MeetingID: "123456789", Topic: "Новая тема",
			EndedAt: &ended, FirstEventAt: t0.Add(time.Hour)})
		e.must(err)
		if !m3.FirstEventAt.Equal(started) || m3.Topic != "Новая тема" || m3.EndedAt == nil || !m3.EndedAt.Equal(ended) ||
			m3.StartedAt == nil || !m3.StartedAt.Equal(started) {
			t.Errorf("merged again = %+v", m3)
		}
		// lesson_id / link_mode are not touched by a merge.
		e.must(e.s.SetZoomMeetingLink(e.ctx, m.ID, &l.ID, model.LinkManual))
		m4, err := e.s.UpsertZoomMeeting(e.ctx, &model.ZoomMeeting{MeetingUUID: "uuid==1", FirstEventAt: t0.Add(time.Hour)})
		e.must(err)
		if m4.LessonID == nil || *m4.LessonID != l.ID || m4.LinkMode != model.LinkManual {
			t.Errorf("link lost on merge: %+v", m4)
		}
		_, err = e.s.UpsertZoomMeeting(e.ctx, &model.ZoomMeeting{})
		wantErr(t, err, ErrInvalid)
		// Zero FirstEventAt falls back to the clock.
		m5, err := e.s.UpsertZoomMeeting(e.ctx, &model.ZoomMeeting{MeetingUUID: "uuid==2", MeetingID: "123456789"})
		e.must(err)
		if !m5.FirstEventAt.Equal(e.clock.Now().Truncate(time.Millisecond)) {
			t.Errorf("first event = %v", m5.FirstEventAt)
		}
		e.clock.Advance(time.Hour)
		m6, err := e.s.UpsertZoomMeeting(e.ctx, &model.ZoomMeeting{MeetingUUID: "uuid==3", MeetingID: "555555555", FirstEventAt: t0.Add(3 * time.Hour)})
		e.must(err)

		byUUID, err := e.s.GetZoomMeetingByUUID(e.ctx, "uuid==1")
		e.must(err)
		if byUUID.ID != m.ID {
			t.Error("GetZoomMeetingByUUID mismatch")
		}
		_, err = e.s.GetZoomMeetingByUUID(e.ctx, "nope")
		wantErr(t, err, ErrNotFound)
		_, err = e.s.GetZoomMeeting(e.ctx, 999)
		wantErr(t, err, ErrNotFound)

		byLesson, err := e.s.ListZoomMeetingsByLesson(e.ctx, l.ID)
		e.must(err)
		if len(byLesson) != 1 || byLesson[0].ID != m.ID {
			t.Errorf("by lesson = %+v", byLesson)
		}
		all, err := e.s.ListZoomMeetings(e.ctx, ZoomMeetingFilter{})
		e.must(err)
		// first_event_at: m6 = t0+3h, m = t0+2m (merged minimum), m5 = t0+1m (clock).
		if len(all) != 3 || all[0].ID != m6.ID || all[1].ID != m.ID || all[2].ID != m5.ID {
			t.Errorf("all meetings order = %v", meetingIDs(all))
		}
		unl, _ := e.s.ListZoomMeetings(e.ctx, ZoomMeetingFilter{UnlinkedOnly: true, Limit: 1})
		if len(unl) != 1 || unl[0].ID != m6.ID {
			t.Errorf("unlinked = %v", meetingIDs(unl))
		}
		since := t0.Add(2 * time.Minute)
		recent, _ := e.s.ListZoomMeetings(e.ctx, ZoomMeetingFilter{Since: &since})
		if len(recent) != 2 || recent[1].ID != m.ID {
			t.Errorf("since = %v", meetingIDs(recent))
		}
		linkable, err := e.s.ListLinkableMeetings(e.ctx, "123456789")
		e.must(err)
		if len(linkable) != 1 || linkable[0].ID != m5.ID {
			t.Errorf("linkable = %v", meetingIDs(linkable))
		}
		// Manual unlink: not linkable anymore.
		e.must(e.s.SetZoomMeetingLink(e.ctx, m5.ID, nil, model.LinkManual))
		linkable, _ = e.s.ListLinkableMeetings(e.ctx, "123456789")
		if len(linkable) != 0 {
			t.Errorf("manually unlinked meeting still linkable")
		}
		wantErr(t, e.s.SetZoomMeetingLink(e.ctx, 999, nil, model.LinkAuto), ErrNotFound)
		wantErr(t, e.s.SetZoomMeetingLink(e.ctx, m5.ID, ptr(int64(999)), model.LinkAuto), ErrNotFound)
		wantErr(t, e.s.SetZoomMeetingLink(e.ctx, m5.ID, nil, "bogus"), ErrInvalid)
	})
}

func meetingIDs(ms []model.ZoomMeeting) []int64 {
	var out []int64
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func eventIDs(evs []model.WebhookEvent) []int64 {
	var out []int64
	for _, e := range evs {
		out = append(out, e.ID)
	}
	return out
}

func TestWebhookEventQueue(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		ins := func(ev model.WebhookEvent) int64 {
			t.Helper()
			id, err := e.s.InsertWebhookEvent(e.ctx, &ev)
			e.must(err)
			return id
		}
		occ := t0.Add(-time.Second)
		payload := `{"event":"meeting.participant_joined","payload":{"object":{"id":"123456789"}}}` + "\n"
		e1 := ins(model.WebhookEvent{EventType: "meeting.participant_joined", EventTS: 1772442000123, MeetingID: "123456789",
			MeetingUUID: "u1", ParticipantName: "Иванов Иван", OccurredAt: &occ, DedupeKey: "k1", Payload: payload})
		e2 := ins(model.WebhookEvent{EventType: "meeting.participant_joined", MeetingUUID: "u1", DedupeKey: "k1", Payload: "{}"})
		e3 := ins(model.WebhookEvent{EventType: "meeting.started", MeetingUUID: "u1", DedupeKey: "k3", Payload: "{}"})
		e4 := ins(model.WebhookEvent{EventType: "meeting.participant_left", MeetingUUID: "u2", MeetingID: "987654321",
			ParticipantName: "Petrov", DedupeKey: "k4", Payload: "{}"})
		e5 := ins(model.WebhookEvent{EventType: "endpoint.url_validation", DedupeKey: "k5", Payload: "{}", ProcessingStatus: model.EventIgnored})
		e6 := ins(model.WebhookEvent{EventType: "meeting.participant_joined_breakout_room", MeetingUUID: "u3", DedupeKey: "k6", Payload: "{}"})

		got, err := e.s.GetWebhookEvent(e.ctx, e1)
		e.must(err)
		if got.Payload != payload || got.ParticipantName != "Иванов Иван" || got.EventTS != 1772442000123 ||
			got.ProcessingStatus != model.EventPending || got.OccurredAt == nil || !got.OccurredAt.Equal(occ) ||
			!got.ReceivedAt.Equal(t0.Truncate(time.Millisecond)) || got.Attempts != 0 || got.NextAttemptAt != nil {
			t.Errorf("GetWebhookEvent = %+v", got)
		}
		_, err = e.s.GetWebhookEvent(e.ctx, 999)
		wantErr(t, err, ErrNotFound)

		// Queue order and limit.
		due, err := e.s.ListDueEvents(e.ctx, t0, 3)
		e.must(err)
		if want := []int64{e1, e2, e3}; !equalIDs(eventIDs(due), want) {
			t.Errorf("due = %v, want %v", eventIDs(due), want)
		}
		if due[0].Payload != payload {
			t.Error("due events must carry the payload")
		}

		// Dedupe: e2 has an earlier non-duplicate twin.
		dup, err := e.s.HasEarlierWithDedupeKey(e.ctx, "k1", e2)
		e.must(err)
		if !dup {
			t.Error("e2 should be a duplicate")
		}
		if dup, _ := e.s.HasEarlierWithDedupeKey(e.ctx, "k1", e1); dup {
			t.Error("e1 has no earlier twin")
		}
		processed := t0.Add(time.Second)
		e.must(e.s.UpdateEventStatus(e.ctx, e2, model.EventDuplicate, 0, nil, &processed, ""))
		e.must(e.s.UpdateEventStatus(e.ctx, e1, model.EventDuplicate, 0, nil, &processed, ""))
		// Duplicate rows do not count as "the first delivery".
		if dup, _ := e.s.HasEarlierWithDedupeKey(e.ctx, "k1", e2+100); dup {
			t.Error("duplicate rows must be ignored by the dedupe lookup")
		}
		e.must(e.s.UpdateEventStatus(e.ctx, e1, model.EventProcessed, 1, nil, &processed, ""))

		// Backoff: e3 failed once, retry in 2s.
		next := t0.Add(2 * time.Second)
		e.must(e.s.UpdateEventStatus(e.ctx, e3, model.EventPending, 1, &next, nil, "temporary"))
		due, _ = e.s.ListDueEvents(e.ctx, t0, 10)
		if want := []int64{e4, e6}; !equalIDs(eventIDs(due), want) {
			t.Errorf("due during backoff = %v, want %v", eventIDs(due), want)
		}
		due, _ = e.s.ListDueEvents(e.ctx, next, 10)
		if want := []int64{e3, e4, e6}; !equalIDs(eventIDs(due), want) {
			t.Errorf("due after backoff = %v, want %v", eventIDs(due), want)
		}
		if due[0].Attempts != 1 || due[0].Error != "temporary" || due[0].NextAttemptAt == nil || !due[0].NextAttemptAt.Equal(next) {
			t.Errorf("retry state = %+v", due[0])
		}
		e.must(e.s.UpdateEventStatus(e.ctx, e3, model.EventFailed, 6, nil, nil, "permanent"))
		e.must(e.s.UpdateEventStatus(e.ctx, e4, model.EventFailed, 6, nil, nil, "permanent"))
		wantErr(t, e.s.UpdateEventStatus(e.ctx, 999, model.EventProcessed, 0, nil, nil, ""), ErrNotFound)
		wantErr(t, e.s.UpdateEventStatus(e.ctx, e3, "bogus", 0, nil, nil, ""), ErrInvalid)

		// Participant events of sessions: no duplicates, no non-participant types, id order.
		pevs, err := e.s.ListParticipantEvents(e.ctx, []string{"u1", "u2", "u3"})
		e.must(err)
		if want := []int64{e1, e4, e6}; !equalIDs(eventIDs(pevs), want) {
			t.Errorf("participant events = %v, want %v", eventIDs(pevs), want)
		}
		if pevs[0].Payload != payload {
			t.Error("participant events must carry the payload")
		}
		if none, _ := e.s.ListParticipantEvents(e.ctx, nil); len(none) != 0 {
			t.Error("no uuids → no events")
		}

		// Stats.
		e.clock.Advance(time.Hour)
		st, err := e.s.EventStats(e.ctx, t0.Add(time.Hour))
		e.must(err)
		if st.LastEventAt == nil || !st.LastEventAt.Equal(t0.Truncate(time.Millisecond)) ||
			st.LastEventType != "meeting.participant_joined_breakout_room" || st.Last24h != 6 || st.Failed != 2 || st.Pending != 1 {
			t.Errorf("stats = %+v", st)
		}
		st, _ = e.s.EventStats(e.ctx, t0.Add(25*time.Hour))
		if st.Last24h != 0 {
			t.Errorf("stats a day later = %+v", st)
		}

		// Reprocess.
		e.must(e.s.ResetEventForReprocess(e.ctx, e1))
		got, _ = e.s.GetWebhookEvent(e.ctx, e1)
		if got.ProcessingStatus != model.EventPending || got.Attempts != 0 || got.ProcessedAt != nil || got.Error != "" {
			t.Errorf("after reset = %+v", got)
		}
		wantErr(t, e.s.ResetEventForReprocess(e.ctx, 999), ErrNotFound)
		n, err := e.s.ResetFailedEvents(e.ctx)
		e.must(err)
		if n != 2 {
			t.Errorf("ResetFailedEvents = %d", n)
		}
		got, _ = e.s.GetWebhookEvent(e.ctx, e4)
		if got.ProcessingStatus != model.EventPending || got.Attempts != 0 || got.Error != "" {
			t.Errorf("e4 after reset = %+v", got)
		}

		// Retention: the pending events (e1, e3, e4 reset above, e6) stay.
		late := ins(model.WebhookEvent{EventType: "meeting.ended", MeetingUUID: "u1", DedupeKey: "k7", Payload: "{}"}) // received t0+1h
		n, err = e.s.PurgeEventsBefore(e.ctx, t0.Add(time.Minute))
		e.must(err)
		if n != 2 {
			t.Errorf("deleted %d", n)
		}
		left, _ := e.s.ListWebhookEvents(e.ctx, EventFilter{})
		if want := []int64{late, e6, e4, e3, e1}; !equalIDs(eventIDs(left), want) {
			t.Errorf("left = %v", eventIDs(left))
		}
		_ = e5
	})
}

func TestListWebhookEvents(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		var ids []int64
		add := func(typ, mid, uuid, name string, st model.EventStatus) {
			ev := model.WebhookEvent{EventType: typ, MeetingID: mid, MeetingUUID: uuid, ParticipantName: name, DedupeKey: "k",
				Payload: "secret payload", ProcessingStatus: st}
			id, err := e.s.InsertWebhookEvent(e.ctx, &ev)
			e.must(err)
			ids = append(ids, id)
		}
		add("meeting.participant_joined", "85746065432", "AbC==", "Иванов Иван", model.EventProcessed)     // 0
		add("meeting.participant_joined", "85746065432", "AbC==", "Ivan_Petrov 50%", model.EventProcessed) // 1
		add("meeting.participant_left", "85746065432", "AbC==", "Иванов Иван", model.EventFailed)          // 2
		add("meeting.started", "111222333", "XyZ==", "", model.EventProcessed)                             // 3
		add("meeting.participant_joined", "111222333", "XyZ==", "SIDOROV", model.EventDuplicate)           // 4

		all, err := e.s.ListWebhookEvents(e.ctx, EventFilter{})
		e.must(err)
		if want := []int64{ids[4], ids[3], ids[2], ids[1], ids[0]}; !equalIDs(eventIDs(all), want) {
			t.Errorf("all = %v", eventIDs(all))
		}
		for _, ev := range all {
			if ev.Payload != "" {
				t.Fatal("list must not include payloads")
			}
		}
		cases := []struct {
			name string
			f    EventFilter
			want []int64
		}{
			{"status", EventFilter{Status: model.EventFailed}, []int64{ids[2]}},
			{"type", EventFilter{Type: "meeting.participant_joined"}, []int64{ids[4], ids[1], ids[0]}},
			{"name", EventFilter{Query: "иванов"}, []int64{ids[2], ids[0]}},
			{"ascii case-insensitive", EventFilter{Query: "sidorov"}, []int64{ids[4]}},
			{"wildcards are literal", EventFilter{Query: "_petrov 50%"}, []int64{ids[1]}},
			{"underscore literal", EventFilter{Query: "n_p"}, []int64{ids[1]}},
			{"meeting id with spaces", EventFilter{Query: "857 4606 5432"}, []int64{ids[2], ids[1], ids[0]}},
			{"uuid", EventFilter{Query: "XyZ"}, []int64{ids[4], ids[3]}},
			{"before id + limit", EventFilter{BeforeID: ids[3], Limit: 2}, []int64{ids[2], ids[1]}},
			{"combined", EventFilter{Type: "meeting.participant_joined", Query: "85746065432", Status: model.EventProcessed}, []int64{ids[1], ids[0]}},
		}
		for _, c := range cases {
			got, err := e.s.ListWebhookEvents(e.ctx, c.f)
			e.must(err)
			if !equalIDs(eventIDs(got), c.want) {
				t.Errorf("%s: got %v, want %v", c.name, eventIDs(got), c.want)
			}
		}
	})
}

// TestWebhookEventUnstorableBytes: a signed body with NUL bytes or invalid UTF-8
// must still be stored (PostgreSQL TEXT rejects both); SQLite keeps it exactly.
func TestWebhookEventUnstorableBytes(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		raw := "{\"user_name\":\"Ivan\x00\",\"x\":\"\xff\xfe\"}"
		ev := &model.WebhookEvent{EventType: "meeting.participant_joined", ParticipantName: "Iv\x00an", DedupeKey: "k", Payload: raw}
		id, err := e.s.InsertWebhookEvent(e.ctx, ev)
		e.must(err)
		got, err := e.s.GetWebhookEvent(e.ctx, id)
		e.must(err)
		wantPayload, wantName := raw, "Iv\x00an"
		if e.s.Dialect() == db.Postgres {
			wantPayload = "{\"user_name\":\"Ivan�\",\"x\":\"�\"}"
			wantName = "Iv�an"
		}
		if got.Payload != wantPayload || got.ParticipantName != wantName {
			t.Errorf("stored payload %q name %q, want %q %q", got.Payload, got.ParticipantName, wantPayload, wantName)
		}
		if ev.Payload != got.Payload {
			t.Errorf("e.Payload not updated to the stored value: %q", ev.Payload)
		}
	})
	if got := pgSafeText("Иванов"); got != "Иванов" {
		t.Errorf("valid text changed: %q", got)
	}
}

// TestListParticipantEventsAcrossChunks: with more session UUIDs than one IN list
// holds, the result must still be in global id order.
func TestListParticipantEventsAcrossChunks(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		uuids := make([]string, inChunk+20)
		for i := range uuids {
			uuids[i] = fmt.Sprintf("uuid-%04d", i)
		}
		// The oldest event belongs to a UUID of the second chunk.
		order := []string{uuids[inChunk+5], uuids[0], uuids[inChunk+5], uuids[3]}
		var want []int64
		for _, u := range order {
			id, err := e.s.InsertWebhookEvent(e.ctx, &model.WebhookEvent{EventType: "meeting.participant_joined", MeetingUUID: u,
				DedupeKey: u, Payload: "{}"})
			e.must(err)
			want = append(want, id)
		}
		got, err := e.s.ListParticipantEvents(e.ctx, uuids)
		e.must(err)
		if !equalIDs(eventIDs(got), want) {
			t.Errorf("events = %v, want %v", eventIDs(got), want)
		}
	})
}

// TestPurgeEventsKeepsLessonSources: retention never deletes the raw events a
// lesson's attendance is rebuilt from (sessions linked to a lesson), nor
// events still waiting to be processed.
func TestPurgeEventsKeepsLessonSources(t *testing.T) {
	t.Parallel()
	forEachDB(t, func(t *testing.T, e *env) {
		g := e.group("10А")
		l := e.lesson(g.ID, "123456789", t0.Add(-400*24*time.Hour), time.Hour)
		old := t0.Add(-400 * 24 * time.Hour)
		meeting := func(uuid string, lessonID *int64, mode model.LinkMode) {
			t.Helper()
			m, err := e.s.UpsertZoomMeeting(e.ctx, &model.ZoomMeeting{MeetingUUID: uuid, MeetingID: "123456789", FirstEventAt: old})
			e.must(err)
			e.must(e.s.SetZoomMeetingLink(e.ctx, m.ID, lessonID, mode))
		}
		meeting("linked==", &l.ID, model.LinkAuto)
		meeting("manual==", &l.ID, model.LinkManual)
		meeting("free==", nil, model.LinkAuto)
		meeting("unlinked-by-hand==", nil, model.LinkManual)

		n := 0
		ins := func(typ, uuid string, status model.EventStatus, received time.Time) int64 {
			t.Helper()
			n++
			id, err := e.s.InsertWebhookEvent(e.ctx, &model.WebhookEvent{EventType: typ, MeetingUUID: uuid,
				DedupeKey: fmt.Sprintf("k%d", n), Payload: "{}", ProcessingStatus: status, ReceivedAt: received})
			e.must(err)
			return id
		}
		const joined, started = "meeting.participant_joined", "meeting.started"
		keep := []int64{
			ins(joined, "linked==", model.EventProcessed, old),
			ins(joined, "linked==", model.EventDuplicate, old),
			ins(started, "linked==", model.EventProcessed, old),
			ins("meeting.participant_joined_breakout_room", "linked==", model.EventProcessed, old),
			ins(joined, "manual==", model.EventFailed, old),
			ins(joined, "free==", model.EventPending, old),  // not processed yet
			ins(joined, "free==", model.EventProcessed, t0), // young
			ins("endpoint.url_validation", "", model.EventIgnored, t0),
		}
		gone := []int64{
			ins(joined, "free==", model.EventProcessed, old),
			ins(joined, "free==", model.EventFailed, old),
			ins(joined, "unlinked-by-hand==", model.EventProcessed, old),
			ins(joined, "never-a-session==", model.EventIgnored, old),
			ins("endpoint.url_validation", "", model.EventIgnored, old),
			ins("meeting.participant_joined", "", model.EventIgnored, old),
		}
		cutoff := t0.Add(-365 * 24 * time.Hour)
		// A cancelled run (the server is stopping) deletes nothing.
		cancelled, cancel := context.WithCancel(e.ctx)
		cancel()
		if n, err := e.s.purgeEventsBefore(cancelled, cutoff, 4); !errors.Is(err, context.Canceled) || n != 0 {
			t.Errorf("cancelled purge = %d %v", n, err)
		}
		// A tiny batch exercises the loop over several DELETE statements.
		deleted, err := e.s.purgeEventsBefore(e.ctx, cutoff, 4)
		e.must(err)
		if deleted != int64(len(gone)) {
			t.Errorf("deleted %d, want %d", deleted, len(gone))
		}
		left, err := e.s.ListWebhookEvents(e.ctx, EventFilter{})
		e.must(err)
		got := eventIDs(left)
		slices.Sort(got)
		if !equalIDs(got, keep) {
			t.Errorf("left %v, want %v", got, keep)
		}
		if again, err := e.s.PurgeEventsBefore(e.ctx, cutoff); err != nil || again != 0 {
			t.Errorf("second purge = %d %v", again, err)
		}

		// Once the lesson is deleted its sessions are unlinked: their events go too.
		e.must(e.s.DeleteLesson(e.ctx, l.ID))
		deleted, err = e.s.PurgeEventsBefore(e.ctx, cutoff)
		e.must(err)
		if deleted != 5 {
			t.Errorf("after the lesson was deleted: deleted %d, want 5", deleted)
		}
	})
}
