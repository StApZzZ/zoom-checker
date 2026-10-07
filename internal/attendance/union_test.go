package attendance

import (
	"encoding/binary"
	"math/rand/v2"
	"testing"
	"time"
)

func span(start, end time.Time) Span { return Span{Start: start, End: tp(end)} }

func openSpan(start time.Time) Span { return Span{Start: start} }

func TestUnionSeconds(t *testing.T) {
	t.Parallel()
	now := hm(19, 0)
	for _, tc := range []struct {
		name  string
		spans []Span
		want  int64
	}{
		{"nothing", nil, 0},
		{"one closed", []Span{span(hm(18, 0), hm(18, 30))}, minutes(30)},
		{"TZ §36 scenario 4", []Span{span(hm(18, 10), hm(18, 40)), span(hm(18, 50), hm(19, 30))}, minutes(70)},
		{"TZ §32 two devices", []Span{span(hm(18, 0), hm(19, 0)), span(hm(18, 30), hm(19, 30))}, minutes(90)},
		{"TZ §10 rejoin 38+45", []Span{span(hm(18, 2), hm(18, 40)), span(hm(18, 45), hm(19, 30))}, minutes(83)},
		{"contained", []Span{span(hm(18, 0), hm(19, 0)), span(hm(18, 10), hm(18, 20))}, minutes(60)},
		{"touching", []Span{span(hm(18, 0), hm(18, 30)), span(hm(18, 30), hm(19, 0))}, minutes(60)},
		{"identical", []Span{span(hm(18, 0), hm(18, 30)), span(hm(18, 0), hm(18, 30))}, minutes(30)},
		{"unsorted chain", []Span{span(hm(18, 40), hm(19, 0)), span(hm(18, 0), hm(18, 20)), span(hm(18, 15), hm(18, 45))}, minutes(60)},
		{"open ends at now", []Span{openSpan(hm(18, 15))}, minutes(45)},
		{"open overlapping closed", []Span{span(hm(18, 0), hm(18, 30)), openSpan(hm(18, 15))}, minutes(60)},
		{"open after now", []Span{openSpan(hm(19, 5))}, 0},
		{"closed after now still counts", []Span{span(hm(19, 10), hm(19, 20))}, minutes(10)},
		{"zero length", []Span{span(hm(18, 0), hm(18, 0))}, 0},
		{"inverted is zero", []Span{span(hm(18, 30), hm(18, 0))}, 0},
		{"inverted does not swallow others", []Span{span(hm(19, 0), hm(18, 0)), span(hm(18, 10), hm(18, 20))}, minutes(10)},
		{"sub-second parts add up", []Span{
			span(hms(18, 0, 0), hms(18, 0, 0).Add(600*time.Millisecond)),
			span(hms(18, 0, 1), hms(18, 0, 1).Add(600*time.Millisecond)),
		}, 1},
		{"truncates to whole seconds", []Span{span(hms(18, 0, 0), hms(18, 0, 59).Add(999*time.Millisecond))}, 59},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := UnionSeconds(tc.spans, now); got != tc.want {
				t.Errorf("UnionSeconds = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestUnionSecondsDoesNotModifyInput(t *testing.T) {
	t.Parallel()
	spans := []Span{span(hm(18, 40), hm(19, 0)), span(hm(18, 0), hm(18, 20))}
	UnionSeconds(spans, hm(19, 0))
	if !spans[0].Start.Equal(hm(18, 40)) {
		t.Error("input reordered")
	}
}

// checkUnionProperties verifies the invariants of UnionSeconds for one input:
// floor(longest span) <= result <= floor(sum of spans), result >= 0, and the result
// equals a brute-force count when all times are whole seconds.
func checkUnionProperties(t *testing.T, spans []Span, now time.Time) {
	t.Helper()
	got := UnionSeconds(spans, now)
	var sum, longest time.Duration
	for _, s := range spans {
		end := now
		if s.End != nil {
			end = *s.End
		}
		d := max(0, end.Sub(s.Start))
		sum += d
		longest = max(longest, d)
	}
	if got < 0 {
		t.Fatalf("negative union %d for %v", got, spans)
	}
	if got > int64(sum/time.Second) {
		t.Fatalf("union %d s > sum of spans %v for %v", got, sum, spans)
	}
	if got < int64(longest/time.Second) {
		t.Fatalf("union %d s < longest span %v for %v", got, longest, spans)
	}
}

// bruteUnion counts covered seconds one by one; times must be whole seconds within the lesson day.
func bruteUnion(spans []Span, now time.Time) int64 {
	var covered [24 * 3600]bool
	var n int64
	for _, s := range spans {
		end := now
		if s.End != nil {
			end = *s.End
		}
		for x := s.Start.Sub(day) / time.Second; x < end.Sub(day)/time.Second; x++ {
			if !covered[x] {
				covered[x] = true
				n++
			}
		}
	}
	return n
}

func TestUnionSecondsProperties(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(1, 2))
	now := hm(19, 0)
	for range 2000 {
		n := rng.IntN(8)
		spans := make([]Span, n)
		for i := range spans {
			start := hm(18, 0).Add(time.Duration(rng.IntN(7200)-600) * time.Second)
			if rng.IntN(5) == 0 {
				spans[i] = openSpan(start)
				continue
			}
			spans[i] = span(start, start.Add(time.Duration(rng.IntN(3600)-300)*time.Second))
		}
		checkUnionProperties(t, spans, now)
		if got, want := UnionSeconds(spans, now), bruteUnion(spans, now); got != want {
			t.Fatalf("UnionSeconds = %d, brute force = %d for %v", got, want, spans)
		}
	}
}

// FuzzUnionSeconds decodes arbitrary bytes into spans (with nanosecond offsets and
// open/inverted spans) and checks the union invariants.
func FuzzUnionSeconds(f *testing.F) {
	f.Add([]byte{}, int64(3600))
	f.Add([]byte("\x00\x00\x00\x10\x00\x00\x00\x20\x01\x00\x00\x00\x18\x00\x00\x00\x30\x00"), int64(100))
	f.Add([]byte("\xff\xff\xff\xff\x00\x00\x00\x01\x00\xff\xff\xff\xff\xff\xff\xff\xff\x01"), int64(-5))
	f.Fuzz(func(t *testing.T, data []byte, nowOffset int64) {
		base := hm(18, 0)
		now := base.Add(time.Duration(nowOffset % int64(48*time.Hour)))
		var spans []Span
		for len(data) >= 9 {
			start := base.Add(time.Duration(int32(binary.BigEndian.Uint32(data[0:4]))) * time.Millisecond)
			end := base.Add(time.Duration(int32(binary.BigEndian.Uint32(data[4:8]))) * time.Millisecond)
			if data[8]&1 == 1 {
				spans = append(spans, openSpan(start))
			} else {
				spans = append(spans, span(start, end))
			}
			data = data[9:]
		}
		checkUnionProperties(t, spans, now)
	})
}
