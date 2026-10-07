package zoom

import (
	"regexp"
	"testing"
	"time"
)

var hexKeyRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

func mustParseFixture(t *testing.T, name string) (*Event, []byte) {
	t.Helper()
	raw := readFixture(t, name)
	e, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse(%s): %v", name, err)
	}
	return e, raw
}

// clone copies an event deeply enough for the tests to mutate it.
func clone(e *Event) *Event {
	c := *e
	if e.Participant != nil {
		p := *e.Participant
		c.Participant = &p
	}
	return &c
}

func TestDedupeKeyFormat(t *testing.T) {
	t.Parallel()
	seen := map[string]string{}
	for _, tc := range fixtureCases {
		e, raw := mustParseFixture(t, tc.file)
		k := DedupeKey(e, raw)
		if !hexKeyRe.MatchString(k) {
			t.Errorf("%s: key %q is not 64 lower-case hex chars", tc.file, k)
		}
		if k2 := DedupeKey(e, raw); k2 != k {
			t.Errorf("%s: key is not deterministic", tc.file)
		}
		if other, dup := seen[k]; dup {
			t.Errorf("%s and %s share a key", tc.file, other)
		}
		seen[k] = tc.file
	}
}

func TestDedupeKeyParticipantEvents(t *testing.T) {
	t.Parallel()
	joined, joinedRaw := mustParseFixture(t, "participant_joined.json")
	left, leftRaw := mustParseFixture(t, "participant_left.json")
	boJoined, boRaw := mustParseFixture(t, "breakout_joined.json")
	base := DedupeKey(joined, joinedRaw)

	same := func(name string, e *Event, raw []byte) {
		t.Helper()
		if got := DedupeKey(e, raw); got != base {
			t.Errorf("%s: key changed, want the same key", name)
		}
	}
	differ := func(name string, e *Event, raw []byte) {
		t.Helper()
		if got := DedupeKey(e, raw); got == base {
			t.Errorf("%s: key unchanged, want a different key", name)
		}
	}

	// Redeliveries of the same join.
	r := clone(joined)
	r.EventTS += 300_000 // Zoom resend with a new event_ts
	same("new event_ts", r, joinedRaw)
	same("different raw body bytes", joined, append([]byte(" "), joinedRaw...))
	r = clone(joined)
	r.Participant.UserName = "Renamed"
	r.Participant.UserID = "999"
	same("display name and user_id are not part of a puuid key", r, joinedRaw)
	r = clone(joined)
	r.Participant.LeaveTime = tp("2026-10-06T09:44:55Z")
	same("leave_time is irrelevant for joins", r, joinedRaw)
	r = clone(joined)
	jt := r.Participant.JoinTime.In(time.FixedZone("MSK", 3*3600))
	r.Participant.JoinTime = &jt
	same("join_time zone does not matter", r, joinedRaw)

	// Different events.
	r = clone(joined)
	r.Participant.ParticipantUUID = "0F0F0F0F-1CEE-C0E7-EB63-A05B8F09F41C"
	differ("different participant_uuid (rejoin / second device)", r, joinedRaw)
	r = clone(joined)
	r.Participant.JoinTime = tp("2026-10-06T09:01:16Z")
	differ("different join_time", r, joinedRaw)
	r = clone(joined)
	r.MeetingUUID = "another-instance=="
	differ("different meeting instance", r, joinedRaw)
	differ("participant_left of the same participant", left, leftRaw)
	r = clone(joined)
	r.Type = EventParticipantLeft
	r.Participant.LeaveTime = r.Participant.JoinTime
	differ("leave at the same second as the join", r, joinedRaw)

	// Breakout events never collide with main-room ones, and rooms differ.
	r = clone(boJoined)
	r.Participant.JoinTime = joined.Participant.JoinTime
	differ("breakout join with the same puuid and time", r, boRaw)
	r2 := clone(r)
	r2.Participant.BreakoutRoomUUID = "otherRoom=="
	if DedupeKey(r, boRaw) == DedupeKey(r2, boRaw) {
		t.Error("breakout joins into different rooms at the same second share a key")
	}

	// participant_left: event_ts is ignored as long as leave_time is present...
	lk := DedupeKey(left, leftRaw)
	r = clone(left)
	r.EventTS++
	if DedupeKey(r, leftRaw) != lk {
		t.Error("participant_left: new event_ts changed the key")
	}
	// ...and becomes the time component when it is missing.
	noTime := clone(left)
	noTime.Participant.LeaveTime = nil
	noTimeKey := DedupeKey(noTime, leftRaw)
	if noTimeKey == lk {
		t.Error("participant_left without leave_time: same key as with it")
	}
	r = clone(noTime)
	if DedupeKey(r, leftRaw) != noTimeKey {
		t.Error("participant_left without leave_time: key not stable")
	}
	r.EventTS++
	if DedupeKey(r, leftRaw) == noTimeKey {
		t.Error("participant_left without leave_time: event_ts not used")
	}
}

func TestDedupeKeyMeetingEvents(t *testing.T) {
	t.Parallel()
	started, startedRaw := mustParseFixture(t, "meeting_started.json")
	ended, endedRaw := mustParseFixture(t, "meeting_ended.json")
	sk := DedupeKey(started, startedRaw)

	if DedupeKey(ended, endedRaw) == sk {
		t.Fatal("meeting.started and meeting.ended of one meeting share a key")
	}
	r := clone(started)
	r.EventTS += 1000
	r.StartTime = tp("2026-10-06T09:00:04Z")
	if DedupeKey(r, []byte(`{"event":"meeting.started"}`)) != sk {
		t.Error("meeting.started redelivery (new event_ts, body) changed the key")
	}
	r = clone(started)
	r.MeetingUUID = "nextOccurrence=="
	if DedupeKey(r, startedRaw) == sk {
		t.Error("meeting.started of another instance has the same key")
	}

	// Without a uuid the key falls back to the body, so unrelated events do not merge.
	a := &Event{Type: EventMeetingStarted, EventTS: 1}
	if DedupeKey(a, []byte(`{"a":1}`)) == DedupeKey(a, []byte(`{"a":2}`)) {
		t.Error("meeting.started without uuid: different bodies share a key")
	}
}

func TestDedupeKeyOtherEvents(t *testing.T) {
	t.Parallel()
	v, raw := mustParseFixture(t, "url_validation.json")
	k := DedupeKey(v, raw)
	if DedupeKey(v, raw) != k {
		t.Fatal("not deterministic")
	}
	other := []byte(`{"payload":{"plainToken":"another"},"event_ts":1654503849680,"event":"endpoint.url_validation"}`)
	if DedupeKey(v, other) == k {
		t.Error("different bodies share a key")
	}
	r := clone(v)
	r.EventTS++
	if DedupeKey(r, raw) == k {
		t.Error("event_ts is not part of the key")
	}
	r = clone(v)
	r.Type = "meeting.sharing_started"
	if DedupeKey(r, raw) == k {
		t.Error("type is not part of the key")
	}

	// Unsupported participant-like events are keyed by body (they are only stored).
	adm, admRaw := mustParseFixture(t, "participant_admitted.json")
	r = clone(adm)
	r.EventTS++
	if DedupeKey(adm, admRaw) == DedupeKey(r, admRaw) {
		t.Error("participant_admitted: event_ts should be part of the generic key")
	}

	// Participant event without participant (hand-built) uses the generic rule.
	bare := &Event{Type: EventParticipantJoined, MeetingUUID: "u", EventTS: 5}
	if DedupeKey(bare, []byte("a")) == DedupeKey(bare, []byte("b")) {
		t.Error("participant event without participant: body not used")
	}
}

func TestDedupeKeyNilEvent(t *testing.T) {
	t.Parallel()
	a := DedupeKey(nil, []byte("not json"))
	if !hexKeyRe.MatchString(a) {
		t.Fatalf("key %q", a)
	}
	if DedupeKey(nil, []byte("not json")) != a {
		t.Error("not deterministic")
	}
	if DedupeKey(nil, []byte("not json!")) == a {
		t.Error("different bodies share a key")
	}
	if DedupeKey(&Event{Type: "unparsed"}, []byte("not json")) == a {
		t.Error("nil event collides with a parsed event")
	}
}

// Values containing the "|" separator must not make two different events collide.
func TestDedupeKeyNoSeparatorInjection(t *testing.T) {
	t.Parallel()
	at := tp("2026-10-06T09:20:00Z")
	a := &Event{Type: EventBreakoutJoined, MeetingUUID: "A|B", Participant: &Participant{
		ParticipantUUID: "P", BreakoutRoomUUID: "C", JoinTime: at}}
	b := &Event{Type: EventBreakoutJoined, MeetingUUID: "A", Participant: &Participant{
		ParticipantUUID: "P", BreakoutRoomUUID: "B|C", JoinTime: at}}
	if DedupeKey(a, nil) == DedupeKey(b, nil) {
		t.Fatal("separator injection produced a collision")
	}
}
