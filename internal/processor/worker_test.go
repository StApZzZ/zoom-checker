package processor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"zoom-checker/internal/attendance"
	"zoom-checker/internal/db"
	"zoom-checker/internal/model"
	"zoom-checker/internal/realtime"
	"zoom-checker/internal/zoom"
)

// failSession makes every upsert of the given session fail inside the database,
// a real processing error for that one event.
func (e *env) failSession(uuid string) {
	e.t.Helper()
	stmts := []string{fmt.Sprintf(`CREATE TRIGGER fail_session BEFORE INSERT ON zoom_meetings
		WHEN NEW.meeting_uuid = '%s' BEGIN SELECT RAISE(ABORT, 'simulated failure'); END`, uuid)}
	if e.st.Dialect() == db.Postgres {
		stmts = []string{
			fmt.Sprintf(`CREATE FUNCTION fail_session() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN IF NEW.meeting_uuid = '%s' THEN RAISE EXCEPTION 'simulated failure'; END IF; RETURN NEW; END $$`, uuid),
			`CREATE TRIGGER fail_session BEFORE INSERT ON zoom_meetings FOR EACH ROW EXECUTE FUNCTION fail_session()`,
		}
	}
	for _, q := range stmts {
		if _, err := e.db.ExecContext(e.ctx, q); err != nil {
			e.t.Fatal(err)
		}
	}
}

// fixSession removes the failure installed by failSession.
func (e *env) fixSession() {
	e.t.Helper()
	q := `DROP TRIGGER fail_session`
	if e.st.Dialect() == db.Postgres {
		q = `DROP TRIGGER fail_session ON zoom_meetings`
	}
	if _, err := e.db.ExecContext(e.ctx, q); err != nil {
		e.t.Fatal(err)
	}
}

func TestFailureRetryBackoffThenFailed(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	bad := newMeeting(testMeeting, "uuid-bad", hm(9, 0))
	c.failSession("uuid-bad")

	badEv := c.deliver(bad.joined(dev("Петров Пётр", "p"), hm(9, 1)))
	goodEv := c.deliver(c.m.joined(dev("Иванов Иван", "a"), hm(9, 1))) // same batch: must not be held back
	now := c.clock.Now()
	if n, err := c.p.ProcessPending(c.ctx); err != nil || n != 2 {
		t.Fatalf("ProcessPending = %d, %v", n, err)
	}
	if ev := c.event(goodEv.ID); ev.ProcessingStatus != model.EventProcessed {
		t.Fatalf("good event %s", ev.ProcessingStatus)
	}
	if r := c.row(c.lesson, c.sid("Иванов Иван")); r.CurrentStatus != model.CurrentOnline {
		t.Fatalf("good event not applied: %+v", r)
	}

	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		ev := c.event(badEv.ID)
		if ev.Attempts != attempt || !strings.Contains(ev.Error, "simulated failure") {
			t.Fatalf("attempt %d: %+v", attempt, ev)
		}
		if attempt == MaxAttempts {
			if ev.ProcessingStatus != model.EventFailed || ev.NextAttemptAt != nil {
				t.Fatalf("after %d attempts: %s next=%v, want failed", attempt, ev.ProcessingStatus, tstr(ev.NextAttemptAt))
			}
			break
		}
		backoff := time.Duration(1<<attempt) * time.Second
		if ev.ProcessingStatus != model.EventPending || ev.NextAttemptAt == nil || !ev.NextAttemptAt.Equal(now.Add(backoff)) {
			t.Fatalf("attempt %d: %s next=%v, want pending at +%v", attempt, ev.ProcessingStatus, tstr(ev.NextAttemptAt), backoff)
		}
		// Not due yet: nothing to do.
		c.clock.Set(now.Add(backoff - time.Millisecond))
		if n, err := c.p.ProcessPending(c.ctx); err != nil || n != 0 {
			t.Fatalf("before the backoff expired: %d, %v", n, err)
		}
		now = now.Add(backoff)
		c.clock.Set(now)
		if n, err := c.p.ProcessPending(c.ctx); err != nil || n != 1 {
			t.Fatalf("retry %d: %d, %v", attempt+1, n, err)
		}
	}
	c.clock.Advance(time.Hour)
	if n := c.process(); n != 0 {
		t.Fatalf("failed event was retried (%d)", n)
	}

	// Once the cause is fixed, an admin's "reprocess" brings it through.
	c.fixSession()
	if err := c.st.ResetEventForReprocess(c.ctx, badEv.ID); err != nil {
		t.Fatal(err)
	}
	c.process()
	if ev := c.event(badEv.ID); ev.ProcessingStatus != model.EventProcessed || ev.Error != "" {
		t.Fatalf("after reprocess: %+v", ev)
	}
	if s := c.session("uuid-bad"); s.LessonID == nil || *s.LessonID != c.lesson {
		t.Fatalf("session %+v", s)
	}
	if r := c.row(c.lesson, c.sid("Петров Пётр")); r.CurrentStatus != model.CurrentOnline {
		t.Fatalf("Петров: %+v", r)
	}
}

func TestReprocessIsIdempotent(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	d := dev("Иванов Иван", "a")
	join := c.deliver(c.m.joined(d, hm(9, 1)))
	c.send(c.m.left(d, hm(9, 40)))
	dup := c.deliver(c.m.left(d, hm(9, 40)))
	c.process()
	before := c.snapshot(c.lesson)
	for _, id := range []int64{join.ID, dup.ID} {
		if err := c.st.ResetEventForReprocess(c.ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	c.process()
	if got := c.snapshot(c.lesson); got != before {
		t.Fatalf("reprocessing changed the projection:\n%s\nwas:\n%s", got, before)
	}
	if ev := c.event(dup.ID); ev.ProcessingStatus != model.EventDuplicate {
		t.Fatalf("duplicate after reprocess: %s", ev.ProcessingStatus)
	}
	if ev := c.event(join.ID); ev.ProcessingStatus != model.EventProcessed {
		t.Fatalf("original after reprocess: %s", ev.ProcessingStatus)
	}
}

func TestBuildRawEvent(t *testing.T) {
	t.Parallel()
	received := time.Date(2026, 10, 6, 9, 1, 16, 500, time.UTC)
	m := newMeeting(testMeeting, mainUUID, hm(9, 0))
	long := strings.Repeat("Я", 400)

	cases := []struct {
		name        string
		body        []byte
		status      model.EventStatus
		typ         string
		meetingID   string
		uuid        string
		participant string
		occurred    *time.Time
		noteHas     string
	}{
		{name: "participant joined", body: m.joined(dev("Иванов  Иван", "a"), hms(9, 1, 15)),
			status: model.EventPending, typ: zoom.EventParticipantJoined, meetingID: testMeeting, uuid: mainUUID,
			participant: "Иванов  Иван", occurred: ptrTime(hms(9, 1, 15))},
		{name: "meeting started", body: m.started(hm(9, 0)), status: model.EventPending, typ: zoom.EventMeetingStarted,
			meetingID: testMeeting, uuid: mainUUID, occurred: ptrTime(hm(9, 0))},
		{name: "url validation", body: []byte(`{"payload":{"plainToken":"qgg8vlvZRS6UYooatFL8Aw"},"event_ts":1654503849680,"event":"endpoint.url_validation"}`),
			status: model.EventIgnored, typ: zoom.EventURLValidation, occurred: ptrTime(time.UnixMilli(1654503849680).UTC()),
			noteHas: "вебхука"},
		{name: "unsupported type", body: []byte(`{"event":"meeting.participant_admitted","event_ts":1,"payload":{"object":{"id":"1","uuid":"u","participant":{"user_name":"X"}}}}`),
			status: model.EventIgnored, typ: "meeting.participant_admitted", meetingID: "1", uuid: "u", participant: "X",
			occurred: ptrTime(time.UnixMilli(1).UTC()), noteHas: "не используется"},
		{name: "not json", body: []byte(`not json at all`), status: model.EventIgnored, typ: UnknownEventType, noteHas: "разобрать"},
		{name: "participant event without participant", body: []byte(`{"event":"meeting.participant_joined","payload":{"object":{"id":"1","uuid":"u"}}}`),
			status: model.EventIgnored, typ: zoom.EventParticipantJoined, noteHas: "разобрать"},
		{name: "no meeting id and no uuid", body: []byte(`{"event":"meeting.participant_left","event_ts":5,"payload":{"object":{"id":null,"participant":{"user_name":"A","leave_time":"2026-10-06T09:50:12Z"}}}}`),
			status: model.EventIgnored, typ: zoom.EventParticipantLeft, participant: "A", occurred: ptrTime(hms(9, 50, 12)),
			noteHas: "ни номера"},
		{name: "no meeting id, uuid present", body: []byte(`{"event":"meeting.participant_left","event_ts":5,"payload":{"object":{"id":null,"uuid":"qLa3","participant":{"user_name":"A","leave_time":"2026-10-06T09:50:12Z"}}}}`),
			status: model.EventPending, typ: zoom.EventParticipantLeft, uuid: "qLa3", participant: "A", occurred: ptrTime(hms(9, 50, 12))},
		{name: "meeting event without uuid", body: []byte(`{"event":"meeting.started","event_ts":5,"payload":{"object":{"id":"123456789"}}}`),
			status: model.EventIgnored, typ: zoom.EventMeetingStarted, meetingID: "123456789", occurred: ptrTime(time.UnixMilli(5).UTC()),
			noteHas: "uuid"},
		{name: "NUL and long name", body: m.joined(dev("Пе\u0000тров "+long, "a"), hm(9, 2)), status: model.EventPending,
			typ: zoom.EventParticipantJoined, meetingID: testMeeting, uuid: mainUUID,
			participant: "Петров " + strings.Repeat("Я", 300-7), occurred: ptrTime(hm(9, 2))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ev, perr := zoom.Parse(tc.body)
			got := BuildRawEvent(tc.body, ev, perr, received)
			if got.ProcessingStatus != tc.status || got.EventType != tc.typ || got.MeetingID != tc.meetingID ||
				got.MeetingUUID != tc.uuid || got.ParticipantName != tc.participant {
				t.Fatalf("got status=%s type=%q id=%q uuid=%q name=%q", got.ProcessingStatus, got.EventType, got.MeetingID,
					got.MeetingUUID, got.ParticipantName)
			}
			if tstr(got.OccurredAt) != tstr(tc.occurred) {
				t.Errorf("occurred %v, want %v", tstr(got.OccurredAt), tstr(tc.occurred))
			}
			if got.Payload != string(tc.body) || !got.ReceivedAt.Equal(received) {
				t.Error("payload or received_at changed")
			}
			wantKey := zoom.DedupeKey(ev, tc.body)
			if perr != nil {
				wantKey = zoom.DedupeKey(nil, tc.body)
			}
			if got.DedupeKey != wantKey || got.DedupeKey == "" {
				t.Errorf("dedupe key %q, want %q", got.DedupeKey, wantKey)
			}
			if tc.status == model.EventIgnored {
				if !strings.Contains(got.Error, tc.noteHas) {
					t.Errorf("note %q, want it to mention %q", got.Error, tc.noteHas)
				}
				if got.ProcessedAt == nil {
					t.Error("ignored event without processed_at")
				}
			} else if got.Error != "" || got.ProcessedAt != nil {
				t.Errorf("pending event with error %q / processed_at %v", got.Error, tstr(got.ProcessedAt))
			}
			if strings.ContainsRune(got.ParticipantName, 0) || strings.ContainsRune(got.EventType, 0) {
				t.Error("NUL kept")
			}
			if tc.name == "NUL and long name" && !strings.ContainsRune(ev.Participant.UserName, 0) {
				t.Error("BuildRawEvent modified the caller's event")
			}
		})
	}
}

func TestParticipantEventConversion(t *testing.T) {
	t.Parallel()
	m := newMeeting(testMeeting, mainUUID, hm(9, 0))
	conv := func(id int64, body []byte) attendance.ParticipantEvent {
		t.Helper()
		ev, err := zoom.Parse(body)
		if err != nil {
			t.Fatal(err)
		}
		raw := BuildRawEvent(body, ev, nil, hm(10, 0))
		raw.ID = id
		pe, ok := participantEvent(raw)
		if !ok {
			t.Fatal("not converted")
		}
		return pe
	}

	signedIn := device{name: "Мария", puuid: "PU-m", userID: "16777216", accountID: hostAccount}
	pe := conv(7, m.joined(signedIn, hm(9, 0)))
	if pe.EventID != 7 || pe.Kind != attendance.Join || pe.Breakout || pe.Key != "puuid:PU-m" ||
		pe.ParticipantID != hostAccount || pe.UserID != "16777216" || pe.MeetingUUID != mainUUID || !pe.At.Equal(hm(9, 0)) ||
		pe.EventTS != hm(9, 0).UnixMilli()+m.etsOffset || pe.JoinTime == nil || !pe.JoinTime.Equal(hm(9, 0)) {
		t.Fatalf("join: %+v", pe)
	}

	pe = conv(8, m.leftWith(dev("Иван", "a"), hm(9, 30), "Host ended the meeting."))
	if pe.Kind != attendance.Leave || pe.LeaveReason != "Host ended the meeting." || pe.JoinTime != nil || pe.ParticipantID != "" {
		t.Fatalf("leave: %+v", pe)
	}

	noUUID := device{name: "Иван", userID: "16778240"}
	pe = conv(9, m.breakout(true, noUUID, "room", hm(9, 20)))
	if !pe.Breakout || pe.Kind != attendance.Join || pe.Key != "uid:16778240" || pe.UserID != "16778240-room" {
		t.Fatalf("breakout join: %+v", pe)
	}
	pe = conv(10, m.breakout(false, dev("Иван", "a"), "room", hm(9, 35)))
	if !pe.Breakout || pe.Kind != attendance.Leave || pe.Key != "puuid:PU-a" {
		t.Fatalf("breakout leave: %+v", pe)
	}

	// Deprecated "id" is the fallback participant id (host rule on old payloads).
	old := []byte(`{"event":"meeting.participant_joined","event_ts":1,"payload":{"object":{"id":"1","uuid":"u",` +
		`"participant":{"user_name":"Old","id":"HOST","join_time":"2026-10-06T09:00:00Z"}}}}`)
	if pe := conv(11, old); pe.ParticipantID != "HOST" || pe.Key != "pid:HOST" {
		t.Fatalf("old payload: %+v", pe)
	}

	// Pending redeliveries are dropped by dedupe key; other rows are skipped.
	body := m.joined(dev("Иван", "a"), hm(9, 1))
	ev, _ := zoom.Parse(body)
	r1, r2 := BuildRawEvent(body, ev, nil, hm(9, 2)), BuildRawEvent(body, ev, nil, hm(9, 3))
	r1.ID, r2.ID = 1, 2
	admitted := []byte(`{"event":"meeting.participant_admitted","payload":{"object":{"id":"1","uuid":"u","participant":{"user_name":"X"}}}}`)
	r3 := BuildRawEvent(admitted, nil, nil, hm(9, 4))
	r3.ID = 3
	if got := participantEvents([]model.WebhookEvent{*r1, *r2, *r3}); len(got) != 1 || got[0].EventID != 1 {
		t.Fatalf("participantEvents: %+v", got)
	}
}

func TestRunWakesOnNotifyAndPolls(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	// Stored before the worker starts: handled by the first run at startup.
	early := c.deliver(c.m.joined(dev("Иванов Иван", "a"), hm(9, 1)))
	c.p.poll = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(c.ctx)
	done := make(chan error, 1)
	go func() { done <- c.p.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run returned %v", err)
		}
	}()

	waitStatus := func(id int64, want model.EventStatus) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			ev, err := c.st.GetWebhookEvent(c.ctx, id)
			if err == nil && ev.ProcessingStatus == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("event %d did not reach %s", id, want)
	}
	waitStatus(early.ID, model.EventProcessed)

	// Without Notify the poll ticker picks events up too.
	polled := c.deliver(c.m.joined(dev("Петров Пётр", "b"), hm(9, 2)))
	waitStatus(polled.ID, model.EventProcessed)

	// More than one batch is drained in one go.
	for i := range BatchSize + 20 {
		c.deliver(c.m.joined(dev(fmt.Sprintf("Гость %d", i), fmt.Sprint("g", i)), hm(9, 3)))
	}
	last := c.deliver(c.m.joined(dev("Сидорова Анна", "c"), hm(9, 4)))
	c.p.Notify()
	c.p.Notify() // coalesces, never blocks
	waitStatus(last.ID, model.EventProcessed)
}

// TestConcurrentRebuildsDuringProcessing: the worker processes 200 events while
// API-style RebuildLesson / RelinkLesson / LinkSession calls run in parallel. With
// SQLite's single writer a lock taken inside a transaction would deadlock; the
// test fails on a timeout or any error.
func TestConcurrentRebuildsDuringProcessing(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	lessonA := c.lesson                                                  // meeting testMeeting
	lessonB := c.env.lesson(c.g.id, "11122233344", hm(9, 0), hm(10, 30)) // another meeting id
	m2 := newMeeting("11122233344", "uuid-second", hm(9, 0))

	ctx, cancel := context.WithTimeout(c.ctx, 2*time.Minute)
	defer cancel()
	runCtx, stopRun := context.WithCancel(ctx)
	runDone := make(chan error, 1)
	go func() { runDone <- c.p.Run(runCtx) }()

	updates, unsubscribe := c.broker.Subscribe(realtime.LessonTopic(lessonA))
	defer unsubscribe()

	var stop atomic.Bool
	var wg sync.WaitGroup
	var calls atomic.Int64
	errs := make(chan error, 64)
	for w := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				var err error
				switch w {
				case 0, 1:
					err = c.p.RebuildLesson(ctx, lessonA)
				case 2:
					err = c.p.RelinkLesson(ctx, lessonB)
				case 3:
					err = c.p.RebuildLesson(ctx, lessonB)
				}
				if err != nil {
					errs <- err
					return
				}
				calls.Add(1)
				// API calls follow teacher actions; a short pause between them keeps
				// the test about lock ordering. Back-to-back calls (writer
				// starvation) are covered by TestIngestNotStarvedByProcessorLoad.
				time.Sleep(3 * time.Millisecond)
			}
		}()
	}

	// 200 joins/leaves: four students rejoining every few minutes; Александров is
	// in the second meeting (lesson B) until it gets linked to lesson A by hand.
	var ids []int64
	names := []string{"Иванов Иван", "Петров Пётр", "Сидорова Анна", "Александров Александр"}
	for i := range 200 {
		at := hm(9, 0).Add(time.Duration(i) * 10 * time.Second)
		n := i % len(names)
		round := i / len(names) // even rounds join, odd rounds leave the previous join
		m := c.m
		if n == 3 {
			m = m2
		}
		var body []byte
		if round%2 == 0 {
			body = m.joined(dev(names[n], fmt.Sprintf("%d-%d", n, round)), at)
		} else {
			body = m.left(dev(names[n], fmt.Sprintf("%d-%d", n, round-1)), at)
		}
		ids = append(ids, c.deliver(body).ID)
		if i%3 == 0 {
			c.p.Notify()
		}
		if i == 100 {
			s := waitSession(t, c.env, "uuid-second")
			if err := c.p.LinkSession(ctx, s.ID, &lessonA); err != nil {
				t.Fatal(err)
			}
		}
	}
	c.p.Notify()

	deadline := time.Now().Add(90 * time.Second)
	for {
		pending := 0
		for _, id := range ids {
			if c.event(id).ProcessingStatus == model.EventPending {
				pending++
			}
		}
		if pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d events still pending: deadlock or stalled worker", pending)
		}
		select {
		case err := <-errs:
			t.Fatalf("concurrent call failed: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	stop.Store(true)
	wg.Wait()
	stopRun()
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errs:
		t.Fatalf("concurrent call failed: %v", err)
	default:
	}
	if calls.Load() == 0 {
		t.Fatal("no concurrent API calls ran")
	}
	select {
	case <-updates:
	default:
		t.Fatal("no realtime update for the lesson")
	}
	for _, id := range ids {
		if st := c.event(id).ProcessingStatus; st != model.EventProcessed {
			t.Fatalf("event %d: %s", id, st)
		}
	}
	if s := c.session("uuid-second"); s.LessonID == nil || *s.LessonID != lessonA || s.LinkMode != model.LinkManual {
		t.Fatalf("manual link lost under concurrency: %+v", s)
	}

	// The incremental result equals a fresh rebuild from the raw events.
	for _, id := range []int64{lessonA, lessonB} {
		got := c.snapshot(id)
		c.rebuild(id)
		if want := c.snapshot(id); got != want {
			t.Fatalf("lesson %d differs from a fresh rebuild:\n%s\nwant:\n%s", id, got, want)
		}
	}
	row := c.row(lessonA, c.sid("Александров Александр"))
	if row.TotalDurationSeconds == 0 || c.row(lessonB, c.sid("Александров Александр")).TotalDurationSeconds != 0 {
		t.Fatalf("Александров's time did not move with the session: %+v", row)
	}
	if ctx.Err() != nil {
		t.Fatal("timed out")
	}
}

func waitSession(t *testing.T, e *env, uuid string) *model.ZoomMeeting {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if s, err := e.st.GetZoomMeetingByUUID(e.ctx, uuid); err == nil {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("session %s never appeared", uuid)
	return nil
}

// A backlog is handled in one transaction that rebuilds the lesson once, not
// once per BatchSize events; a failing event in a later page only costs the
// pages before it a one-by-one retry and never blocks the others.
func TestBacklogCoalescesRebuilds(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	c.p.txBudget = time.Minute // the event count decides here, not the machine's speed
	const guests = 2*BatchSize + 30
	for i := range guests {
		c.deliver(c.m.joined(dev(fmt.Sprintf("Гость %d", i), fmt.Sprint("g", i)), hm(9, 3)))
	}
	ivanov := c.deliver(c.m.joined(dev("Иванов Иван", "a"), hm(9, 4)))
	if n, err := c.p.ProcessPending(c.ctx); err != nil || n != guests+1 {
		t.Fatalf("ProcessPending = %d, %v; want the whole backlog (%d) in one transaction", n, err, guests+1)
	}
	if ev := c.event(ivanov.ID); ev.ProcessingStatus != model.EventProcessed {
		t.Fatalf("last event %s", ev.ProcessingStatus)
	}
	if r := c.row(c.lesson, c.sid("Иванов Иван")); r.CurrentStatus != model.CurrentOnline {
		t.Fatalf("the rebuild at the end of the transaction missed the last page: %+v", r)
	}
	if got := len(c.participants(c.lesson)); got != guests+1 {
		t.Fatalf("participants = %d, want %d", got, guests+1)
	}

	// A bad event on the second page (a fresh lesson keeps the one-by-one
	// retries of the first page cheap).
	c = newClass(t, hm(9, 0), hm(10, 30))
	c.p.txBudget = time.Minute
	bad := newMeeting(testMeeting, "uuid-bad", hm(9, 0))
	c.failSession("uuid-bad")
	var good []int64
	for i := range BatchSize + 10 {
		good = append(good, c.deliver(c.m.joined(dev(fmt.Sprintf("Гость %d", i), fmt.Sprint("g", i)), hm(9, 20))).ID)
	}
	badEv := c.deliver(bad.joined(dev("Петров Пётр", "p"), hm(9, 21)))
	for i := BatchSize + 10; i < BatchSize+20; i++ {
		good = append(good, c.deliver(c.m.joined(dev(fmt.Sprintf("Гость %d", i), fmt.Sprint("g", i)), hm(9, 22))).ID)
	}
	c.process()
	for _, id := range good {
		if ev := c.event(id); ev.ProcessingStatus != model.EventProcessed {
			t.Fatalf("event %d: %s, want processed", id, ev.ProcessingStatus)
		}
	}
	if ev := c.event(badEv.ID); ev.Attempts != 1 || ev.ProcessingStatus != model.EventPending {
		t.Fatalf("bad event: attempts=%d status=%s, want one recorded failure", ev.Attempts, ev.ProcessingStatus)
	}
	if got := len(c.participants(c.lesson)); got != BatchSize+20 {
		t.Fatalf("participants = %d, want %d", got, BatchSize+20)
	}
}
