package attendance

import (
	"slices"
	"time"
)

// Span is a time range; a nil End means "still open".
type Span struct {
	Start time.Time
	End   *time.Time
}

// UnionSeconds returns the length, in whole seconds, of the union of spans:
// open spans end at now, time covered by several spans (two devices, breakout room
// plus main room) is counted once, and spans whose end is not after their start add
// nothing. The result is never negative.
func UnionSeconds(spans []Span, now time.Time) int64 {
	segs := make([]segment, 0, len(spans))
	for _, s := range spans {
		end := now
		if s.End != nil {
			end = *s.End
		}
		segs = appendSegment(segs, s.Start, end)
	}
	return unionSegments(segs)
}

type segment struct{ start, end time.Time }

// appendSegment adds [start, end) unless it is empty or inverted.
func appendSegment(segs []segment, start, end time.Time) []segment {
	if !end.After(start) {
		return segs
	}
	return append(segs, segment{start, end})
}

// unionSegments sorts segs in place and returns the length of their union in whole
// seconds. Whole seconds and the sub-second remainder are accumulated separately so
// the result is the exact floor of the union length without int64 overflow.
func unionSegments(segs []segment) int64 {
	if len(segs) == 0 {
		return 0
	}
	slices.SortFunc(segs, func(a, b segment) int { return a.start.Compare(b.start) })
	var secs, nanos int64
	add := func(s segment) {
		d := s.end.Sub(s.start)
		secs += int64(d / time.Second)
		nanos += int64(d % time.Second)
	}
	cur := segs[0]
	for _, s := range segs[1:] {
		if s.start.After(cur.end) {
			add(cur)
			cur = s
			continue
		}
		if s.end.After(cur.end) {
			cur.end = s.end
		}
	}
	add(cur)
	return secs + nanos/int64(time.Second)
}

// clippedUnion is unionSegments of segs clipped to [from, to); segs is not modified.
func clippedUnion(segs []segment, from, to time.Time) int64 {
	clipped := make([]segment, 0, len(segs))
	for _, s := range segs {
		start, end := s.start, s.end
		if start.Before(from) {
			start = from
		}
		if end.After(to) {
			end = to
		}
		clipped = appendSegment(clipped, start, end)
	}
	return unionSegments(clipped)
}
