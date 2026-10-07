package attendance

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"zoom-checker/internal/model"
)

// largeInput builds a realistic busy lesson: a 30-student group (plus aliases), two
// sessions, students reconnecting with new participant UUIDs, second devices, breakout
// rooms, a few unknown names and some duplicated deliveries — n events in total,
// shuffled so Compute has to sort them.
func largeInput(n int) Input {
	rng := rand.New(rand.NewPCG(3, 4))
	const groupSize = 30
	inp := Input{
		Lesson: LessonInfo{ID: 1, PlannedStart: hm(18, 0), PlannedEnd: hm(19, 30)},
		Sessions: []Session{
			{MeetingUUID: sess1, HostID: host, StartedAt: tp(hm(17, 55)), EndedAt: tp(hm(18, 40)), FirstEventAt: hm(17, 55)},
			{MeetingUUID: sess2, HostID: host, StartedAt: tp(hm(18, 41)), FirstEventAt: hm(18, 41)},
		},
		Mappings:          map[string]Mapping{"гость 3": {Action: model.MappingStudent, StudentID: 7}},
		IgnoredNames:      map[string]bool{"iphone": true},
		IgnoreHost:        true,
		PresentMinPercent: 50,
		Now:               hm(19, 20),
	}
	for i := range groupSize {
		full := fmt.Sprintf("Студент%02d Фамилия", i)
		inp.Roster = append(inp.Roster, RosterStudent{
			StudentID: int64(i + 1), FullName: full, NormalizedName: fmt.Sprintf("студент%02d фамилия", i),
			NormalizedAliases: []string{fmt.Sprintf("с%02d", i)}, Active: i%10 != 9,
		})
	}
	userNames := func(i int) string {
		switch {
		case i < groupSize:
			return fmt.Sprintf("Студент%02d  ФАМИЛИЯ", i)
		case i < groupSize+5:
			return fmt.Sprintf("Гость %d", i-groupSize)
		default:
			return "iPhone"
		}
	}
	var id int64
	add := func(e ParticipantEvent) {
		id++
		e.EventID = id
		e.EventTS = e.At.UnixMilli() + rng.Int64N(900)
		inp.Events = append(inp.Events, e)
	}
	for len(inp.Events) < n {
		who := rng.IntN(groupSize + 6)
		uuid, lo, hi := sess1, hm(17, 55), hm(18, 40)
		if rng.IntN(2) == 0 {
			uuid, lo, hi = sess2, hm(18, 41), hm(19, 20)
		}
		start := lo.Add(time.Duration(rng.Int64N(int64(hi.Sub(lo)))))
		start = start.Truncate(time.Second)
		end := start.Add(time.Duration(rng.IntN(1200)) * time.Second)
		puuid := fmt.Sprintf("p-%d-%d", who, id)
		e := ParticipantEvent{
			Kind: Join, MeetingUUID: uuid, At: start, Key: "puuid:" + puuid, ParticipantUUID: puuid,
			UserName: userNames(who), Breakout: rng.IntN(8) == 0,
		}
		add(e)
		if rng.IntN(20) == 0 {
			add(e) // duplicate delivery
		}
		if rng.IntN(10) != 0 { // some devices are still online
			e.Kind, e.At = Leave, end
			add(e)
		}
	}
	inp.Events = inp.Events[:n]
	rng.Shuffle(len(inp.Events), func(a, b int) { inp.Events[a], inp.Events[b] = inp.Events[b], inp.Events[a] })
	return inp
}

func BenchmarkCompute5000Events(b *testing.B) {
	inp := largeInput(5000)
	b.ReportAllocs()
	for b.Loop() {
		Compute(inp)
	}
}

func BenchmarkUnionSeconds1000Spans(b *testing.B) {
	rng := rand.New(rand.NewPCG(5, 6))
	spans := make([]Span, 1000)
	for i := range spans {
		start := hm(18, 0).Add(time.Duration(rng.IntN(5400)) * time.Second)
		spans[i] = span(start, start.Add(time.Duration(rng.IntN(1800))*time.Second))
	}
	b.ReportAllocs()
	for b.Loop() {
		UnionSeconds(spans, hm(19, 30))
	}
}

// TestCompute5000EventsFast guards the "< 50 ms for 5,000 events" requirement
// (typically ~5 ms). On a loaded machine single runs can be preempted, so the test
// passes as soon as one of up to 20 runs is fast enough; the limit is relaxed under
// -race, which slows everything down by an order of magnitude.
func TestCompute5000EventsFast(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	inp := largeInput(5000)
	limit := 50 * time.Millisecond
	if raceEnabled {
		limit *= 10
	}
	best := time.Hour
	var out Output
	for range 20 {
		start := time.Now()
		out = Compute(inp)
		if best = min(best, time.Since(start)); best < limit {
			break
		}
	}
	if best > limit {
		t.Errorf("Compute(5000 events) took %v, want < %v", best, limit)
	}
	t.Logf("Compute(5000 events): %v, %d intervals, %d participants", best, len(out.Intervals), len(out.Participants))

	// Sanity: the synthetic lesson produced a meaningful projection.
	if len(out.Intervals) < 1000 || len(out.Participants) < 30 || len(out.Students) != 30 {
		t.Errorf("unexpected projection size: %d intervals, %d participants, %d students",
			len(out.Intervals), len(out.Participants), len(out.Students))
	}
}
