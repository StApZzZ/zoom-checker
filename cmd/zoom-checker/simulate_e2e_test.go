package main

import (
	"bytes"
	"context"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"zoom-checker/internal/config"
	"zoom-checker/internal/db"
	"zoom-checker/internal/model"
	"zoom-checker/internal/names"
	"zoom-checker/internal/server"
	"zoom-checker/internal/store"
)

// TestSimulateEndToEnd plays the demo lesson (--instant) against the real
// service — webhook endpoint, processor, attendance engine — and checks that
// every scenario case ends up the way the legend promises. It guards the
// simulator's realism as much as the pipeline.
func TestSimulateEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the whole service")
	}
	t.Parallel()
	const (
		secret  = "e2e-webhook-secret"
		meeting = "85746065432"
	)
	dir := t.TempDir()
	env := map[string]string{
		"LISTEN_ADDR":               "127.0.0.1:0",
		"DATA_DIR":                  dir,
		"ZOOM_WEBHOOK_SECRET_TOKEN": secret,
		"ADMIN_PASSWORD":            "E2e-Admin-Pass-2026",
		"BACKUP_KEEP":               "0",
		"TIMEZONE":                  "Europe/Moscow",
	}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	const dur = 90 * time.Minute

	// Seed the group (the built-in names) and the lesson the meeting belongs to.
	d, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	st := store.New(d)
	g := &model.Group{Name: "Демо-группа"}
	if err := st.CreateGroup(ctx, g); err != nil {
		t.Fatal(err)
	}
	studentID := map[string]int64{}
	for _, n := range defaultSimNames {
		s := &model.Student{GroupID: g.ID, FullName: n}
		if err := st.CreateStudent(ctx, s); err != nil {
			t.Fatal(err)
		}
		studentID[n] = s.ID
	}
	lesson := &model.Lesson{GroupID: g.ID, Title: "Демо", ZoomMeetingID: meeting,
		PlannedStartAt: now.Add(-dur), PlannedEndAt: now}
	if err := st.CreateLesson(ctx, lesson); err != nil {
		t.Fatal(err)
	}

	// Start the service.
	runCtx, stop := context.WithCancel(ctx)
	logs := &syncWriter{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	addrc := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() {
		done <- server.Run(runCtx, cfg, log,
			server.WithStatic(fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}),
			server.WithListenHook(func(_ string, a net.Addr) { addrc <- a }))
	}()
	var addr net.Addr
	select {
	case addr = <-addrc:
	case err := <-done:
		t.Fatalf("server stopped: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("server did not start")
	}
	t.Cleanup(func() {
		stop()
		select {
		case <-done:
		case <-time.After(25 * time.Second):
			t.Error("server did not shut down")
		}
	})

	// Send the scenario.
	o := simOptions{
		URL: "http://" + addr.String() + "/api/webhooks/zoom", Secret: secret, MeetingID: meeting,
		Topic: "Демо", HostName: "Преподаватель", Timezone: "Europe/Moscow", Names: defaultSimNames,
		Duration: dur, Speed: 30, Instant: true, Seed: 2026,
	}
	sc := buildScenario(o, rand.New(rand.NewPCG(o.Seed, 1)))
	if err := sc.render(simClock(o, now)); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	client := &http.Client{Timeout: 10 * time.Second}
	if sent, failed := sendScenario(ctx, &out, client, sc, o, now); failed != 0 || sent != len(sc.Events) {
		t.Fatalf("sent %d, failed %d\n%s", sent, failed, out.String())
	}

	// Wait for the processor to drain the queue and finish the lesson.
	deadline := time.Now().Add(20 * time.Second)
	for {
		pending, err := st.ListWebhookEvents(ctx, store.EventFilter{Status: model.EventPending})
		if err != nil {
			t.Fatal(err)
		}
		l, err := st.GetLesson(ctx, lesson.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) == 0 && l.Status == model.LessonFinished {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not processed in time: %d pending, lesson %s\nlogs:\n%s", len(pending), l.Status, logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Raw events: all stored, exactly the one duplicate delivery detected.
	events, err := st.ListWebhookEvents(ctx, store.EventFilter{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != len(sc.Events) {
		t.Errorf("stored %d events, sent %d", len(events), len(sc.Events))
	}
	byStatus := map[model.EventStatus]int{}
	for _, e := range events {
		byStatus[e.ProcessingStatus]++
		if e.ProcessingStatus == model.EventFailed {
			t.Errorf("event %d (%s) failed: %s", e.ID, e.EventType, e.Error)
		}
	}
	if byStatus[model.EventDuplicate] != 1 {
		t.Errorf("duplicates = %d, want 1 (statuses %v)", byStatus[model.EventDuplicate], byStatus)
	}

	rows, err := st.ListAttendance(ctx, lesson.ID)
	if err != nil {
		t.Fatal(err)
	}
	att := map[int64]model.Attendance{}
	for _, r := range rows {
		att[r.StudentID] = r
	}
	row := func(name string) model.Attendance {
		t.Helper()
		r, ok := att[studentID[name]]
		if !ok {
			t.Fatalf("no attendance row for %s", name)
		}
		return r
	}
	minutes := func(r model.Attendance) float64 { return float64(r.TotalDurationSeconds) / 60 }

	// 1. Messy spacing/case is recognised.
	if r := row("Иванов Иван"); r.FinalStatus != model.FinalPresent || minutes(r) < 80 {
		t.Errorf("Иванов (messy name): %s, %.1f min", r.FinalStatus, minutes(r))
	}
	// 2. Two rejoins: time adds up, gaps are not counted.
	if r := row("Петрова Анна"); r.FinalStatus != model.FinalPresent || minutes(r) < 60 || minutes(r) > 86 {
		t.Errorf("Петрова (rejoins): %s, %.1f min", r.FinalStatus, minutes(r))
	}
	// 3. Two devices: overlap counted once.
	if r := row("Сидоров Алексей"); minutes(r) < 80 || minutes(r) > 90 {
		t.Errorf("Сидоров (two devices): %.1f min, want 80–90", minutes(r))
	}
	// 4. Breakout room: stays present for (almost) the whole lesson.
	if r := row("Смирнова Мария"); r.FinalStatus != model.FinalPresent || minutes(r) < 80 {
		t.Errorf("Смирнова (breakout): %s, %.1f min", r.FinalStatus, minutes(r))
	}
	// 5. Never joins.
	if r := row("Кузнецов Дмитрий"); r.CurrentStatus != model.CurrentNotSeen || r.FinalStatus != model.FinalAbsent || r.TotalDurationSeconds != 0 {
		t.Errorf("Кузнецов (never joins): %s %s %d s", r.CurrentStatus, r.FinalStatus, r.TotalDurationSeconds)
	}
	// 6–7. Late by 10–20 minutes.
	for _, n := range []string{"Попова Екатерина", "Васильев Михаил"} {
		r := row(n)
		if r.LateSeconds == nil || *r.LateSeconds < 599 || *r.LateSeconds > 1201 {
			t.Errorf("%s (late): late_seconds %v", n, r.LateSeconds)
		}
	}
	// 8. Leaves early.
	if r := row("Новикова Ольга"); r.EarlyLeaveSeconds == nil || *r.EarlyLeaveSeconds < 600 {
		t.Errorf("Новикова (early leave): early_leave_seconds %v", r.EarlyLeaveSeconds)
	}
	// Everybody is closed by the end of the meeting.
	for _, r := range rows {
		if r.CurrentStatus == model.CurrentOnline {
			t.Errorf("student %d still online after meeting.ended", r.StudentID)
		}
	}

	// Unidentified participants.
	parts, err := st.ListParticipants(ctx, lesson.ID)
	if err != nil {
		t.Fatal(err)
	}
	res := map[string]model.Resolution{}
	for _, p := range parts {
		res[p.NormalizedName] = p.Resolution
	}
	for _, n := range []string{simDeviceName, simNicknameName} {
		if got := res[names.Normalize(n)]; got != model.ResolutionUnidentified {
			t.Errorf("participant %q resolution %q, want unidentified", n, got)
		}
	}
	if got := res[names.Normalize("  ИВАНОВ   Иван ")]; got != model.ResolutionMatched {
		t.Errorf("messy name resolution %q, want matched", got)
	}
}

// syncWriter is a goroutine-safe log sink for the server under test.
type syncWriter struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncWriter) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
