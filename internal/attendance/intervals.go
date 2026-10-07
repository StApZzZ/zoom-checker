package attendance

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"zoom-checker/internal/names"
)

// interval is the builder's working form of one device presence interval.
type interval struct {
	name        string // final normalized display name
	rawName     string // final raw display name
	renamedFrom string // raw join name when the leave reported another name
	joinName    string // normalized join name when the leave reported another name
	key         string // device key without the breakout namespace
	puuid       string
	meetingUUID string
	breakout    bool
	join        time.Time
	leave       time.Time // valid when closed
	closed      bool
	orphan      bool // built from a leave whose join was never seen
	host        bool // the device belongs to the session host
	reason      string
	prevInLane  int // previous interval of the same lane and session, -1 if none
}

// laneState tracks one device key (main room or breakout namespace) inside one session.
type laneState struct {
	open     int  // index of the open interval in builder.ivs, -1 if none
	last     int  // index of the latest interval of the lane (head of the prevInLane list), -1 if none
	host     bool // some event of the lane carried the session host's id
	lateJoin bool // a join after the session end was dropped
}

// nameHint is a candidate for ParticipantResult.DisplayName.
type nameHint struct {
	raw      string
	fromJoin bool
	at       time.Time
	ts, id   int64
}

// better reports whether h should replace cur: join names beat names seen only at
// leave, then the most recent event wins; the raw string breaks exact ties so the
// choice never depends on input order.
func (h nameHint) better(cur nameHint) bool {
	if h.fromJoin != cur.fromJoin {
		return h.fromJoin
	}
	if c := cmp.Or(h.at.Compare(cur.at), cmp.Compare(h.ts, cur.ts), cmp.Compare(h.id, cur.id)); c != 0 {
		return c > 0
	}
	return h.raw > cur.raw
}

type builder struct {
	ivs   []interval
	hints map[string]nameHint // normalized name -> display name
	norm  map[string]string   // raw -> normalized cache (names repeat a lot)
}

func newBuilder(events int) *builder {
	return &builder{
		ivs:   make([]interval, 0, events/2+1),
		hints: make(map[string]nameHint),
		norm:  make(map[string]string),
	}
}

func (b *builder) normalize(raw string) string {
	n, ok := b.norm[raw]
	if !ok {
		n = names.Normalize(raw)
		b.norm[raw] = n
	}
	return n
}

func (b *builder) hint(name string, ev *ParticipantEvent, fromJoin bool) {
	h := nameHint{raw: ev.UserName, fromJoin: fromJoin, at: ev.At, ts: ev.EventTS, id: ev.EventID}
	if cur, ok := b.hints[name]; !ok || h.better(cur) {
		b.hints[name] = h
	}
}

// eventKey returns the device key of an event, deriving it exactly like
// zoom.Participant.Key() (identifiers trimmed) when the caller did not set one, so
// events with and without a precomputed Key land in the same lane.
func eventKey(ev *ParticipantEvent, normName string) string {
	if ev.Key != "" {
		return ev.Key
	}
	if v := strings.TrimSpace(ev.ParticipantUUID); v != "" {
		return "puuid:" + v
	}
	if v := strings.TrimSpace(ev.UserID); v != "" {
		return "uid:" + v
	}
	if v := strings.TrimSpace(ev.ParticipantID); v != "" {
		return "pid:" + v
	}
	return "name:" + normName
}

// compareEvents orders events by (At, EventTS, Join<Leave, EventID) as the contract
// requires, then by every remaining field so that the order (and therefore the
// output) never depends on the order of the input slice.
func compareEvents(a, b *ParticipantEvent) int {
	if c := a.At.Compare(b.At); c != 0 {
		return c
	}
	if c := cmp.Compare(a.EventTS, b.EventTS); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Kind, b.Kind); c != 0 {
		return c
	}
	if c := cmp.Compare(a.EventID, b.EventID); c != 0 {
		return c
	}
	return cmp.Or(
		strings.Compare(a.Key, b.Key),
		compareBool(a.Breakout, b.Breakout),
		strings.Compare(a.UserName, b.UserName),
		strings.Compare(a.ParticipantUUID, b.ParticipantUUID),
		strings.Compare(a.ParticipantID, b.ParticipantID),
		strings.Compare(a.UserID, b.UserID),
		strings.Compare(a.LeaveReason, b.LeaveReason),
		compareTimePtr(a.JoinTime, b.JoinTime),
	)
}

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	}
	return 1
}

// compareTimePtr orders nil before any time.
func compareTimePtr(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	}
	return a.Compare(*b)
}

// runSession builds the intervals of one session from its events (rule 1).
// end is when the session's open intervals close (EndedAt, else the lesson's end),
// nil while it is still running; hostID is the session host's Zoom user id.
func (b *builder) runSession(meetingUUID, hostID string, evs []*ParticipantEvent, end *time.Time, endReason string) {
	slices.SortFunc(evs, compareEvents)
	lanes := make(map[string]*laneState, len(evs)/2+1)
	// Every event creates at most one lane, so slab pointers stay valid (no regrowth).
	slab := make([]laneState, 0, len(evs))
	for _, ev := range evs {
		name := b.normalize(ev.UserName)
		key := eventKey(ev, name)
		laneKey := key
		if ev.Breakout {
			// Separate namespace: breakout presence never closes or de-duplicates main-room presence.
			laneKey = "bo:" + key
		}
		st := lanes[laneKey]
		if st == nil {
			slab = append(slab, laneState{open: -1, last: -1})
			st = &slab[len(slab)-1]
			lanes[laneKey] = st
		}
		if hostID != "" && (ev.ParticipantID == hostID || ev.UserID == hostID) {
			st.host = true
		}
		switch ev.Kind {
		case Join:
			b.join(st, ev, name, key, meetingUUID, end)
		case Leave:
			b.leave(st, ev, name, key, meetingUUID, end)
		}
	}
	for i := range slab {
		st := &slab[i]
		if end != nil && st.open >= 0 {
			b.close(st, *end, endReason)
		}
		if st.host {
			for j := st.last; j >= 0; j = b.ivs[j].prevInLane {
				b.ivs[j].host = true
			}
		}
	}
}

func (b *builder) join(st *laneState, ev *ParticipantEvent, name, key, meetingUUID string, end *time.Time) {
	if end != nil && ev.At.After(*end) {
		st.lateJoin = true // nobody can join a session that is already over
		return
	}
	if st.open >= 0 {
		cur := &b.ivs[st.open]
		if cur.name == name {
			// Duplicate delivery or a lost leave: keep the earlier start.
			b.hint(name, ev, true)
			return
		}
		b.close(st, ev.At, ReasonRenamed)
	} else if st.last >= 0 {
		if last := &b.ivs[st.last]; last.orphan && !last.leave.Before(ev.At) {
			// The leave of this very visit sorted first (same second, event_ts out of
			// order after a redelivery): the join only supplies the start.
			if ev.At.Before(last.join) {
				last.join = ev.At
			}
			if last.name != name && name != "" && last.renamedFrom == "" {
				last.renamedFrom, last.joinName = ev.UserName, name
			}
			last.orphan = false
			b.hint(name, ev, true)
			return
		}
	}
	b.hint(name, ev, true)
	b.add(st, interval{
		name:        name,
		rawName:     ev.UserName,
		key:         key,
		puuid:       ev.ParticipantUUID,
		meetingUUID: meetingUUID,
		breakout:    ev.Breakout,
		join:        ev.At,
	})
}

func (b *builder) leave(st *laneState, ev *ParticipantEvent, name, key, meetingUUID string, end *time.Time) {
	at := ev.At
	if end != nil && at.After(*end) {
		at = *end // intervals are clipped to the session end
	}
	if st.open >= 0 {
		cur := &b.ivs[st.open]
		if name != "" && name != cur.name {
			// Zoom has no rename webhook: the leave carries the device's latest name,
			// so the whole interval belongs to that name.
			// Compute moves it back when only the join name is a student
			// (keepIdentifiedJoinNames).
			if cur.renamedFrom == "" {
				cur.renamedFrom, cur.joinName = cur.rawName, cur.name
			}
			cur.name, cur.rawName = name, ev.UserName
		}
		if name == cur.name {
			b.hint(name, ev, false)
		}
		b.close(st, at, ev.LeaveReason)
		return
	}

	// Orphan leave: the join was never received. Without a join, a leave after the end
	// still proves presence at the end (Zoom sends leaves for everyone still in the
	// meeting), unless it closes a visit that itself started after the end.
	joinTime := ev.JoinTime
	if joinTime != nil && joinTime.IsZero() {
		joinTime = nil // an unset time behind a non-nil pointer is not a join time
	}
	if end != nil && (st.lateJoin || (joinTime != nil && joinTime.After(*end))) {
		return
	}
	start := at
	if joinTime != nil && joinTime.Before(at) {
		start = *joinTime
	}
	for i := st.last; i >= 0; i = b.ivs[i].prevInLane { // all closed: nothing is open in this lane
		iv := &b.ivs[i]
		if iv.join.After(start) {
			continue
		}
		if !iv.leave.Before(at) {
			return // already covered by a known interval: repeated leave
		}
		if iv.leave.After(start) {
			start = iv.leave // keep device intervals disjoint
		}
	}
	b.hint(name, ev, false)
	b.add(st, interval{
		name:        name,
		rawName:     ev.UserName,
		key:         key,
		puuid:       ev.ParticipantUUID,
		meetingUUID: meetingUUID,
		breakout:    ev.Breakout,
		join:        start,
		leave:       at,
		closed:      true,
		orphan:      true,
		reason:      ev.LeaveReason,
	})
}

func (b *builder) close(st *laneState, at time.Time, reason string) {
	iv := &b.ivs[st.open]
	if at.Before(iv.join) {
		at = iv.join
	}
	iv.leave, iv.closed, iv.reason = at, true, reason
	st.open = -1
}

func (b *builder) add(st *laneState, iv interval) {
	iv.prevInLane = st.last
	b.ivs = append(b.ivs, iv)
	st.last = len(b.ivs) - 1
	if !iv.closed {
		st.open = st.last
	}
}
