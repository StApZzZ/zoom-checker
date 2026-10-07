package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"zoom-checker/internal/names"
	"zoom-checker/internal/zoom"
)

func testSimOptions() simOptions {
	return simOptions{
		MeetingID: "85746065432",
		Topic:     "Демо",
		HostName:  "Преподаватель",
		Timezone:  "Europe/Moscow",
		Names:     defaultSimNames,
		Duration:  90 * time.Minute,
		Speed:     30,
		Instant:   true,
		Seed:      42,
	}
}

func renderTestScenario(t *testing.T, o simOptions, now time.Time) *simScenario {
	t.Helper()
	sc := buildScenario(o, rand.New(rand.NewPCG(o.Seed, 7)))
	if err := sc.render(simClock(o, now)); err != nil {
		t.Fatal(err)
	}
	return sc
}

type parsedEvent struct {
	raw []byte
	ev  *zoom.Event
	obj map[string]any // payload.object as decoded JSON
}

func parseAll(t *testing.T, sc *simScenario) []parsedEvent {
	t.Helper()
	var out []parsedEvent
	for i, e := range sc.Events {
		ev, err := zoom.Parse(e.Body)
		if err != nil {
			t.Fatalf("event %d (%s): parse: %v\n%s", i, e.Type, err, e.Body)
		}
		var env struct {
			Payload struct {
				Object map[string]any `json:"object"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(e.Body, &env); err != nil {
			t.Fatal(err)
		}
		out = append(out, parsedEvent{raw: e.Body, ev: ev, obj: env.Payload.Object})
	}
	return out
}

func TestSimulateScenarioCoversEveryCase(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	o := testSimOptions()
	sc := renderTestScenario(t, o, now)
	evs := parseAll(t, sc)

	if got := evs[0].ev.Type; got != zoom.EventMeetingStarted {
		t.Fatalf("first event = %s, want meeting.started", got)
	}
	if got := evs[len(evs)-1].ev.Type; got != zoom.EventMeetingEnded {
		t.Fatalf("last event = %s, want meeting.ended", got)
	}

	type nameStats struct {
		joins, leaves, boJoins, boLeaves int
		uuids                            map[string]bool
		firstJoin                        time.Time
	}
	stats := map[string]*nameStats{}
	get := func(n string) *nameStats {
		if stats[n] == nil {
			stats[n] = &nameStats{uuids: map[string]bool{}}
		}
		return stats[n]
	}
	start := *evs[0].ev.StartTime
	bodies := map[string]int{}
	prevTS := int64(0)
	for i, pe := range evs {
		ev := pe.ev
		bodies[string(pe.raw)]++
		if bodies[string(pe.raw)] > 1 {
			continue // the duplicate delivery repeats an earlier event verbatim
		}
		// Shape checks: string meeting id, same uuid everywhere, event_ts ordered.
		if id, ok := pe.obj["id"].(string); !ok || id != o.MeetingID {
			t.Errorf("event %d: object.id = %#v, want string %q", i, pe.obj["id"], o.MeetingID)
		}
		if ev.MeetingUUID != sc.MeetingUUID {
			t.Errorf("event %d: uuid %q != %q", i, ev.MeetingUUID, sc.MeetingUUID)
		}
		if ev.EventTS < prevTS {
			t.Errorf("event %d: event_ts goes backwards", i)
		}
		prevTS = ev.EventTS
		if !zoom.IsSupported(ev.Type) {
			t.Errorf("event %d: unsupported type %s", i, ev.Type)
		}
		if ev.Participant == nil {
			continue
		}
		p := ev.Participant
		part := pe.obj["participant"].(map[string]any)
		n := names.Normalize(p.UserName)
		s := get(n)
		if p.ParticipantUUID == "" {
			t.Errorf("event %d: empty participant_uuid", i)
		}
		switch ev.Type {
		case zoom.EventParticipantJoined:
			s.joins++
			s.uuids[p.ParticipantUUID] = true
			if p.JoinTime == nil {
				t.Errorf("event %d: join without join_time", i)
			} else if s.firstJoin.IsZero() || p.JoinTime.Before(s.firstJoin) {
				s.firstJoin = *p.JoinTime
			}
		case zoom.EventParticipantLeft:
			s.leaves++
			if _, has := part["join_time"]; has {
				t.Errorf("event %d: participant_left must not carry join_time", i)
			}
			if p.LeaveTime == nil {
				t.Errorf("event %d: leave without leave_time", i)
			}
		case zoom.EventBreakoutJoined:
			s.boJoins++
			if pe.obj["breakout_room_uuid"] == "" || p.ParentUserID == "" {
				t.Errorf("event %d: breakout join without room uuid / parent user id", i)
			}
		case zoom.EventBreakoutLeft:
			s.boLeaves++
		}
	}

	// Host.
	if s := stats[names.Normalize("Преподаватель")]; s == nil || s.joins == 0 {
		t.Error("host never joins")
	}
	// Messy spacing/case of the first name.
	foundMessy := false
	for _, e := range sc.Events {
		if e.Conn != nil && e.Conn.Name == "  ИВАНОВ   Иван " {
			foundMessy = true
		}
	}
	if !foundMessy {
		t.Error(`no participant joins as "  ИВАНОВ   Иван "`)
	}
	// Leaves and rejoins twice: 3 joins, at least 2 leaves, 3 distinct participant uuids.
	if s := stats[names.Normalize("Петрова Анна")]; s == nil || s.joins != 3 || s.leaves < 2 || len(s.uuids) != 3 {
		t.Errorf("rejoin case: %+v", s)
	}
	// Two devices at once: two uuids, the second join happens before the first leaves.
	if s := stats[names.Normalize("Сидоров Алексей")]; s == nil || len(s.uuids) != 2 {
		t.Errorf("two devices case: %+v", s)
	} else {
		var joins, leaves []time.Time
		for _, pe := range evs {
			if pe.ev.Participant == nil || names.Normalize(pe.ev.Participant.UserName) != names.Normalize("Сидоров Алексей") {
				continue
			}
			if pe.ev.Type == zoom.EventParticipantJoined {
				joins = append(joins, *pe.ev.Participant.JoinTime)
			} else if pe.ev.Type == zoom.EventParticipantLeft {
				leaves = append(leaves, *pe.ev.Participant.LeaveTime)
			}
		}
		if len(joins) != 2 || len(leaves) == 0 || !joins[1].Before(leaves[0]) {
			t.Errorf("devices do not overlap: joins %v leaves %v", joins, leaves)
		}
	}
	// Breakout room and back.
	if s := stats[names.Normalize("Смирнова Мария")]; s == nil || s.boJoins != 1 || s.boLeaves != 1 || s.joins != 2 {
		t.Errorf("breakout case: %+v", s)
	}
	// Never joins.
	if s := stats[names.Normalize("Кузнецов Дмитрий")]; s != nil {
		t.Errorf("Кузнецов Дмитрий should never join, got %+v", s)
	}
	// Late by 10–20 minutes.
	for _, n := range []string{"Попова Екатерина", "Васильев Михаил"} {
		s := stats[names.Normalize(n)]
		if s == nil {
			t.Errorf("%s never joins", n)
			continue
		}
		late := s.firstJoin.Sub(start)
		if late < 10*time.Minute-time.Second || late > 20*time.Minute+time.Second {
			t.Errorf("%s late by %s, want 10–20 min", n, late)
		}
	}
	// Unidentified participants.
	for _, n := range []string{"iPhone", "Ваня"} {
		if s := stats[names.Normalize(n)]; s == nil || s.joins == 0 {
			t.Errorf("unidentified %q missing", n)
		}
	}
	// Exactly one duplicate delivery (identical bytes).
	dups := 0
	for _, c := range bodies {
		if c > 1 {
			dups += c - 1
		}
	}
	if dups != 1 {
		t.Errorf("duplicate deliveries = %d, want 1", dups)
	}
	// Every default name except the one who never joins appears.
	for i, n := range defaultSimNames {
		if i == 4 {
			continue
		}
		if stats[names.Normalize(n)] == nil {
			t.Errorf("%s never appears", n)
		}
	}
	// Someone is still online when meeting.ended arrives (closed by the end itself).
	open := map[string]bool{}
	for _, pe := range evs {
		p := pe.ev.Participant
		if p == nil || zoom.IsBreakoutEvent(pe.ev.Type) {
			continue
		}
		open[p.ParticipantUUID] = pe.ev.Type == zoom.EventParticipantJoined
	}
	stillOpen := 0
	for _, v := range open {
		if v {
			stillOpen++
		}
	}
	if stillOpen == 0 {
		t.Error("nobody is left for meeting.ended to close")
	}
	// Instant mode: timestamps lie in the past duration, ending now.
	if end := evs[len(evs)-1].ev.EndTime; end == nil || end.After(now) || now.Sub(*end) > time.Second {
		t.Errorf("end_time %v, want ≈ %v", end, now)
	}
	if !start.Equal(now.Add(-90 * time.Minute)) {
		t.Errorf("start %v, want %v", start, now.Add(-90*time.Minute))
	}
}

func TestSimulateDeterministicBySeed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	a := renderTestScenario(t, testSimOptions(), now)
	b := renderTestScenario(t, testSimOptions(), now)
	if len(a.Events) != len(b.Events) {
		t.Fatal("different event counts for the same seed")
	}
	for i := range a.Events {
		if !bytes.Equal(a.Events[i].Body, b.Events[i].Body) {
			t.Fatalf("event %d differs for the same seed", i)
		}
	}
}

func TestSimulateFewNamesAndNoEnd(t *testing.T) {
	t.Parallel()
	o := testSimOptions()
	o.Names = []string{"Иванов Иван", "Петрова Анна"}
	o.NoEnd = true
	o.Duration = 10 * time.Minute
	sc := renderTestScenario(t, o, time.Now())
	evs := parseAll(t, sc)
	for _, pe := range evs {
		if pe.ev.Type == zoom.EventMeetingEnded {
			t.Fatal("meeting.ended sent with --no-end")
		}
	}
	for i := 1; i < len(sc.Events); i++ {
		if sc.Events[i].Offset < sc.Events[i-1].Offset {
			t.Fatal("events not sorted by time")
		}
		if sc.Events[i].Offset > o.Duration {
			t.Fatalf("event after the end: %s", sc.Events[i].Offset)
		}
	}
}

func TestSimulateCompressedClock(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	o := testSimOptions()
	o.Instant = false
	sc := renderTestScenario(t, o, now)
	last := sc.Events[len(sc.Events)-1]
	if want := now.Add(3 * time.Minute); !last.At.Equal(want) {
		t.Fatalf("90 min at speed 30 should end at %v, got %v", want, last.At)
	}
}

func TestSimulateSendsSignedRequests(t *testing.T) {
	t.Parallel()
	const secret = "sim-secret"
	var mu sync.Mutex
	var got [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := zoom.Verify(secret, r.Header, body, time.Now(), 0); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		if _, err := zoom.Parse(body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		got = append(got, body)
		mu.Unlock()
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	o := testSimOptions()
	o.URL, o.Secret = srv.URL, secret
	now := time.Now()
	sc := renderTestScenario(t, o, now)
	var out bytes.Buffer
	sent, failed := sendScenario(context.Background(), &out, srv.Client(), sc, o, now)
	if failed != 0 || sent != len(sc.Events) {
		t.Fatalf("sent %d failed %d\n%s", sent, failed, out.String())
	}
	if len(got) != len(sc.Events) {
		t.Fatalf("server got %d events, want %d", len(got), len(sc.Events))
	}
	if lines := strings.Count(out.String(), "\n"); lines != len(sc.Events) {
		t.Errorf("printed %d lines, want one per event", lines)
	}
	if !strings.Contains(out.String(), " 200 ") {
		t.Errorf("status not printed:\n%s", out.String())
	}

	// A wrong secret is reported as a failure, not a crash.
	o.Secret = "wrong"
	out.Reset()
	if _, failed := sendScenario(context.Background(), &out, srv.Client(), sc, o, now); failed != len(sc.Events) {
		t.Errorf("wrong secret: failed = %d, want %d", failed, len(sc.Events))
	}
}

func TestSimulateCLIDryRun(t *testing.T) {
	t.Setenv("CONFIG_FILE", "")
	var stdout, stderr bytes.Buffer
	code := run([]string{"simulate", "--meeting", "https://us02web.zoom.us/j/85746065432?pwd=x", "--dry-run", "--instant", "--seed", "5"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 30 {
		t.Fatalf("only %d events printed", len(lines))
	}
	for _, l := range lines {
		if _, err := zoom.Parse([]byte(l)); err != nil {
			t.Fatalf("dry-run line is not a Zoom event: %v\n%s", err, l)
		}
	}
	if !strings.Contains(stderr.String(), "Сценарий") {
		t.Errorf("legend missing on stderr: %s", stderr.String())
	}
}

func TestMessyName(t *testing.T) {
	t.Parallel()
	if got := messyName("Иванов Иван"); got != "  ИВАНОВ   Иван " {
		t.Fatalf("messyName = %q", got)
	}
	if names.Normalize(messyName("Иванов Иван")) != names.Normalize("Иванов Иван") {
		t.Fatal("messy name must normalize to the original")
	}
}
