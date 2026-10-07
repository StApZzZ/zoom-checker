package zoom

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tp(s string) *time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	t = t.UTC()
	return &t
}

func timesEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return "<nil>"
	}
	return t.Format(time.RFC3339Nano)
}

// checkEvent compares field by field (times with Equal) and requires UTC times.
func checkEvent(t *testing.T, got, want *Event) {
	t.Helper()
	str := func(field, g, w string) {
		if g != w {
			t.Errorf("%s = %q, want %q", field, g, w)
		}
	}
	tm := func(field string, g, w *time.Time) {
		if !timesEqual(g, w) {
			t.Errorf("%s = %s, want %s", field, fmtTime(g), fmtTime(w))
		}
		if g != nil && g.Location() != time.UTC {
			t.Errorf("%s location = %v, want UTC", field, g.Location())
		}
	}
	str("Type", got.Type, want.Type)
	if got.EventTS != want.EventTS {
		t.Errorf("EventTS = %d, want %d", got.EventTS, want.EventTS)
	}
	str("MeetingID", got.MeetingID, want.MeetingID)
	str("MeetingUUID", got.MeetingUUID, want.MeetingUUID)
	str("HostID", got.HostID, want.HostID)
	str("Topic", got.Topic, want.Topic)
	str("PlainToken", got.PlainToken, want.PlainToken)
	tm("StartTime", got.StartTime, want.StartTime)
	tm("EndTime", got.EndTime, want.EndTime)
	if (got.Participant == nil) != (want.Participant == nil) {
		t.Fatalf("Participant = %+v, want %+v", got.Participant, want.Participant)
	}
	if want.Participant == nil {
		return
	}
	g, w := got.Participant, want.Participant
	str("UserName", g.UserName, w.UserName)
	str("ParticipantUUID", g.ParticipantUUID, w.ParticipantUUID)
	str("ID", g.ID, w.ID)
	str("UserID", g.UserID, w.UserID)
	str("ParticipantUserID", g.ParticipantUserID, w.ParticipantUserID)
	str("Email", g.Email, w.Email)
	str("LeaveReason", g.LeaveReason, w.LeaveReason)
	str("BreakoutRoomUUID", g.BreakoutRoomUUID, w.BreakoutRoomUUID)
	str("ParentUserID", g.ParentUserID, w.ParentUserID)
	tm("JoinTime", g.JoinTime, w.JoinTime)
	tm("LeaveTime", g.LeaveTime, w.LeaveTime)
}

const (
	lessonUUID  = "/kV8nE2xQ+O0hR3dWm1x9A=="
	lessonID    = "85746065432"
	lessonHost  = "z8yCxAbCDEfg23HiJKl4mN"
	lessonTopic = "Математика — 10А"
	ivanUUID    = "D2FF531A-1CEE-C0E7-EB63-A05B8F09F41C"
)

type fixtureCase struct {
	file       string
	want       Event
	occurredAt time.Time
	key        string // participant key ("" when no participant)
	reasonText string
}

var fixtureCases = []fixtureCase{
	{
		file:       "url_validation.json", // "payload" before "event"
		want:       Event{Type: EventURLValidation, EventTS: 1654503849680, PlainToken: "qgg8vlvZRS6UYooatFL8Aw"},
		occurredAt: time.UnixMilli(1654503849680).UTC(),
	},
	{
		file: "meeting_started.json",
		want: Event{
			Type: EventMeetingStarted, EventTS: 1791277203512, MeetingID: lessonID, MeetingUUID: lessonUUID,
			HostID: lessonHost, Topic: lessonTopic, StartTime: tp("2026-10-06T09:00:03Z"),
		},
		occurredAt: *tp("2026-10-06T09:00:03Z"),
	},
	{
		file: "meeting_ended.json", // object.id as a JSON number
		want: Event{
			Type: EventMeetingEnded, EventTS: 1791282708301, MeetingID: lessonID, MeetingUUID: lessonUUID,
			HostID: lessonHost, Topic: lessonTopic, StartTime: tp("2026-10-06T09:00:03Z"), EndTime: tp("2026-10-06T10:31:47Z"),
		},
		occurredAt: *tp("2026-10-06T10:31:47Z"),
	},
	{
		file: "participant_joined.json",
		want: Event{
			Type: EventParticipantJoined, EventTS: 1791277275841, MeetingID: lessonID, MeetingUUID: lessonUUID,
			HostID: lessonHost, Topic: lessonTopic, StartTime: tp("2026-10-06T09:00:03Z"),
			Participant: &Participant{
				UserName: "Иванов  Иван", ParticipantUUID: ivanUUID, UserID: "16778240",
				JoinTime: tp("2026-10-06T09:01:15Z"),
			},
		},
		occurredAt: *tp("2026-10-06T09:01:15Z"),
		key:        "puuid:" + ivanUUID,
	},
	{
		file: "participant_joined_emoji.json", // number id, fractional seconds, +03:00 offset
		want: Event{
			Type: EventParticipantJoined, EventTS: 1791277510377, MeetingID: lessonID, MeetingUUID: lessonUUID,
			HostID: lessonHost, Topic: lessonTopic, StartTime: tp("2026-10-06T09:00:03Z"),
			Participant: &Participant{
				UserName: "🦊 Фёдорова Алёна", ParticipantUUID: "1THEbw9LTtSQ-d_-tSS0DQ", UserID: "16779264",
				JoinTime: tp("2026-10-06T09:05:10.123Z"),
			},
		},
		occurredAt: *tp("2026-10-06T09:05:10.123Z"),
		key:        "puuid:1THEbw9LTtSQ-d_-tSS0DQ",
	},
	{
		file: "participant_joined_doc_example.json", // Zoom docs example: date_time instead of join_time
		want: Event{
			Type: EventParticipantJoined, EventTS: 1626230691572, MeetingID: "1234567890",
			MeetingUUID: "4444AAAiAAAAAiAiAiiAii==", HostID: "x1yCzABCDEfg23HiJKl4mN", Topic: "My Meeting",
			StartTime: tp("2021-07-13T21:44:51Z"),
			Participant: &Participant{
				UserName: "Jill Chill", ParticipantUUID: "55555AAAiAAAAAiAiAiiAii", ID: "iFxeBPYun6SAiWUzBcEkX",
				UserID: "1234567890", ParticipantUserID: "rstuvwxyza789-cde", Email: "jchill@example.com",
				JoinTime: tp("2021-07-13T21:44:51Z"),
			},
		},
		occurredAt: *tp("2021-07-13T21:44:51Z"),
		key:        "puuid:55555AAAiAAAAAiAiAiiAii",
	},
	{
		file: "participant_left.json", // real 2026 shape: payload first, no join_time, public/private ip
		want: Event{
			Type: EventParticipantLeft, EventTS: 1791279896354, MeetingID: lessonID, MeetingUUID: lessonUUID,
			HostID: lessonHost, Topic: lessonTopic, StartTime: tp("2026-10-06T09:00:03Z"),
			Participant: &Participant{
				UserName: "Иванов  Иван", ParticipantUUID: ivanUUID, UserID: "16778240",
				LeaveReason: "left the meeting. Reason : left the meeting", LeaveTime: tp("2026-10-06T09:44:55Z"),
			},
		},
		occurredAt: *tp("2026-10-06T09:44:55Z"),
		key:        "puuid:" + ivanUUID,
		reasonText: "left the meeting",
	},
	{
		file: "participant_left_doc_example.json",
		want: Event{
			Type: EventParticipantLeft, EventTS: 1626230691572, MeetingID: "1234567890",
			MeetingUUID: "4444AAAiAAAAAiAiAiiAii==", HostID: "x1yCzABCDEfg23HiJKl4mN", Topic: "My Meeting",
			StartTime: tp("2021-07-13T21:44:51Z"),
			Participant: &Participant{
				UserName: "Jill Chill", ParticipantUUID: "55555AAAiAAAAAiAiAiiAii", ID: "iFxeBPYun6SAiWUzBcEkX",
				UserID: "1234567890", ParticipantUserID: "rstuvwxyza789-cde", Email: "jchill@example.com",
				LeaveReason: "Jill Chill left the meeting.<br>Reason: Host ended the meeting.",
				// date_time is not taken as JoinTime on leaves (it repeats the meeting start here).
				LeaveTime: tp("2021-07-13T22:50:51Z"),
			},
		},
		occurredAt: *tp("2021-07-13T22:50:51Z"),
		key:        "puuid:55555AAAiAAAAAiAiAiiAii",
		reasonText: "Host ended the meeting.",
	},
	{
		file: "participant_left_null_id.json", // 2021-style: id/topic null, no participant_uuid
		want: Event{
			Type: EventParticipantLeft, EventTS: 1791280212604, MeetingUUID: "qLa3t1fZTxe0n8Yw6Vb+5g==",
			HostID: lessonHost, StartTime: tp("2026-10-06T09:20:00Z"),
			Participant: &Participant{
				UserName: "Сидорова Анна", UserID: "16780288", LeaveTime: tp("2026-10-06T09:50:12Z"),
				LeaveReason: "left the meeting. Reason : left the meeting with Unknown Failover Reason",
			},
		},
		occurredAt: *tp("2026-10-06T09:50:12Z"),
		key:        "uid:16780288",
		reasonText: "left the meeting with Unknown Failover Reason",
	},
	{
		file: "participant_left_number_id.json", // 2020-style: numeric object.id and user_id
		want: Event{
			Type: EventParticipantLeft, EventTS: 1791279300250, MeetingID: "251361636",
			MeetingUUID: "lQ7mYt2+RkeT3zKd0aWq8w==", HostID: lessonHost, Topic: "Physics consultation",
			StartTime: tp("2026-10-06T09:00:00Z"),
			Participant: &Participant{
				UserName: "John Smith", UserID: "16778241", Email: "john.smith@example.com",
				ParticipantUserID: "Kq9nV2xTRb6aW0sZ1pLmCg", LeaveTime: tp("2026-10-06T09:35:00Z"),
				LeaveReason: "left the meeting. Reason : Removed by host.",
			},
		},
		occurredAt: *tp("2026-10-06T09:35:00Z"),
		key:        "uid:16778241",
		reasonText: "Removed by host.",
	},
	{
		file: "breakout_joined.json", // object.uuid is the MAIN meeting
		want: Event{
			Type: EventBreakoutJoined, EventTS: 1791278400218, MeetingID: lessonID, MeetingUUID: lessonUUID,
			HostID: lessonHost, Topic: lessonTopic, StartTime: tp("2026-10-06T09:00:03Z"),
			Participant: &Participant{
				UserName: "Иванов  Иван", ParticipantUUID: ivanUUID, UserID: "16780288",
				BreakoutRoomUUID: "FkQbpP2UR028mDrwzEahqw==", ParentUserID: "16778240",
				JoinTime: tp("2026-10-06T09:20:00Z"),
			},
		},
		occurredAt: *tp("2026-10-06T09:20:00Z"),
		key:        "puuid:" + ivanUUID,
	},
	{
		file: "breakout_left.json",
		want: Event{
			Type: EventBreakoutLeft, EventTS: 1791279300731, MeetingID: lessonID, MeetingUUID: lessonUUID,
			HostID: lessonHost, Topic: lessonTopic, StartTime: tp("2026-10-06T09:00:03Z"),
			Participant: &Participant{
				UserName: "Иванов  Иван", ParticipantUUID: ivanUUID, UserID: "16780288",
				BreakoutRoomUUID: "FkQbpP2UR028mDrwzEahqw==", ParentUserID: "16778240",
				LeaveTime: tp("2026-10-06T09:35:00Z"), LeaveReason: "left the meeting. Reason : Host closed the meeting.",
			},
		},
		occurredAt: *tp("2026-10-06T09:35:00Z"),
		key:        "puuid:" + ivanUUID,
		reasonText: "Host closed the meeting.",
	},
	{
		file: "participant_admitted.json", // unsupported type: parsed, OccurredAt = event_ts
		want: Event{
			Type: "meeting.participant_admitted", EventTS: 1791277180466, MeetingID: lessonID, MeetingUUID: lessonUUID,
			HostID: lessonHost, Topic: lessonTopic, StartTime: tp("2026-10-06T09:00:03Z"),
			Participant: &Participant{
				UserName: "Петров Пётр", ParticipantUUID: "8F3C2B1A-7D6E-4F5A-9B8C-0D1E2F3A4B5C", UserID: "16777216",
				JoinTime: tp("2026-10-06T08:59:40Z"),
			},
		},
		occurredAt: time.UnixMilli(1791277180466).UTC(),
		key:        "puuid:8F3C2B1A-7D6E-4F5A-9B8C-0D1E2F3A4B5C",
	},
}

func TestParseFixtures(t *testing.T) {
	t.Parallel()
	for _, tc := range fixtureCases {
		t.Run(tc.file, func(t *testing.T) {
			t.Parallel()
			e, err := Parse(readFixture(t, tc.file))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			checkEvent(t, e, &tc.want)
			if got := e.OccurredAt(); !got.Equal(tc.occurredAt) || got.Location() != time.UTC {
				t.Errorf("OccurredAt = %v, want %v (UTC)", got, tc.occurredAt)
			}
			if got := e.Participant.Key(); got != tc.key {
				t.Errorf("Key = %q, want %q", got, tc.key)
			}
			if got := e.Participant.LeaveReasonText(); got != tc.reasonText {
				t.Errorf("LeaveReasonText = %q, want %q", got, tc.reasonText)
			}
		})
	}
}

func TestEveryFixtureIsCovered(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("testdata/*.json")
	if err != nil {
		t.Fatal(err)
	}
	covered := map[string]bool{}
	for _, tc := range fixtureCases {
		covered[tc.file] = true
	}
	for _, f := range files {
		if !covered[filepath.Base(f)] {
			t.Errorf("fixture %s has no expectation in fixtureCases", f)
		}
	}
	if len(files) != len(fixtureCases) {
		t.Errorf("%d fixtures, %d cases", len(files), len(fixtureCases))
	}
}

func TestParseMeetingID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, id string // id is the raw JSON value; "" omits the field
		want     string
	}{
		{"string", `"85746065432"`, "85746065432"},
		{"string with surrounding whitespace", `" 85746065432\n"`, "85746065432"},
		{"10-digit number", `1234567890`, "1234567890"},
		{"11-digit number (beyond int32)", `98765432101`, "98765432101"},
		{"19-digit number (beyond float64 precision)", `1234567890123456789`, "1234567890123456789"},
		{"null", `null`, ""},
		{"missing", ``, ""},
		{"empty string", `""`, ""},
		{"boolean", `true`, ""},
		{"object", `{"x":1}`, ""},
		{"array", `[1,2]`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			obj := `"uuid":"u"`
			if tt.id != "" {
				obj = `"id":` + tt.id + `,` + obj
			}
			e, err := Parse([]byte(`{"event":"meeting.started","event_ts":1,"payload":{"object":{` + obj + `}}}`))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if e.MeetingID != tt.want {
				t.Fatalf("MeetingID = %q, want %q", e.MeetingID, tt.want)
			}
		})
	}
}

func TestParseTimes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		raw  string // JSON value of join_time
		want *time.Time
	}{
		{`"2026-10-06T09:01:15Z"`, tp("2026-10-06T09:01:15Z")},
		{`"2026-10-06T09:01:15.5Z"`, tp("2026-10-06T09:01:15.5Z")},
		{`"2026-10-06T09:01:15.123456789Z"`, tp("2026-10-06T09:01:15.123456789Z")},
		{`"2026-10-06T12:01:15+03:00"`, tp("2026-10-06T09:01:15Z")},
		{`"2026-10-06T12:01:15.250+03:00"`, tp("2026-10-06T09:01:15.25Z")},
		{`"2026-10-06T04:01:15-05:00"`, tp("2026-10-06T09:01:15Z")},
		{`"2026-10-06T12:01:15+0300"`, tp("2026-10-06T09:01:15Z")},
		{`"2026-10-06t09:01:15z"`, tp("2026-10-06T09:01:15Z")},
		{`"2026-10-06T09:01:15"`, tp("2026-10-06T09:01:15Z")},
		{`"2026-10-06 09:01:15"`, tp("2026-10-06T09:01:15Z")},
		{`" 2026-10-06T09:01:15Z "`, tp("2026-10-06T09:01:15Z")},
		{`""`, nil},
		{`"not a time"`, nil},
		{`"2026-13-45T99:00:00Z"`, nil},
		{`null`, nil},
		{`1791277275`, nil},
		{`{}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			t.Parallel()
			body := `{"event":"meeting.participant_joined","event_ts":1791277275000,"payload":{"object":{"uuid":"u","participant":{"user_name":"A","join_time":` + tt.raw + `}}}}`
			e, err := Parse([]byte(body))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			got := e.Participant.JoinTime
			if !timesEqual(got, tt.want) {
				t.Fatalf("JoinTime = %s, want %s", fmtTime(got), fmtTime(tt.want))
			}
			if got != nil && got.Location() != time.UTC {
				t.Fatalf("JoinTime location = %v, want UTC", got.Location())
			}
		})
	}
}

func TestParseJoinTimePrefersJoinTimeOverDateTime(t *testing.T) {
	t.Parallel()
	body := `{"event":"meeting.participant_joined","payload":{"object":{"uuid":"u","participant":{"date_time":"2026-10-06T09:00:00Z","join_time":"2026-10-06T09:01:00Z"}}}}`
	e, err := Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if want := tp("2026-10-06T09:01:00Z"); !timesEqual(e.Participant.JoinTime, want) {
		t.Fatalf("JoinTime = %s, want %s", fmtTime(e.Participant.JoinTime), fmtTime(want))
	}
}

// date_time stands in for join_time on joins (Zoom's doc examples) but is not a
// join time on leaves: an orphan leave would otherwise be credited from it.
func TestParseDateTimeFallback(t *testing.T) {
	t.Parallel()
	const (
		dateTime = `"date_time":"2021-07-13T21:44:51Z"`
		joinTime = `"join_time":"2021-07-13T22:00:00Z"`
		leave    = `"leave_time":"2021-07-13T22:50:51Z"`
	)
	tests := []struct {
		name, typ, fields string
		wantJoin          *time.Time
	}{
		{"joined: date_time", EventParticipantJoined, dateTime, tp("2021-07-13T21:44:51Z")},
		{"breakout joined: date_time", EventBreakoutJoined, dateTime, tp("2021-07-13T21:44:51Z")},
		{"admitted: date_time", "meeting.participant_admitted", dateTime, tp("2021-07-13T21:44:51Z")},
		{"left: date_time ignored", EventParticipantLeft, dateTime + `,` + leave, nil},
		{"breakout left: date_time ignored", EventBreakoutLeft, dateTime + `,` + leave, nil},
		{"left: explicit join_time kept", EventParticipantLeft, dateTime + `,` + joinTime + `,` + leave, tp("2021-07-13T22:00:00Z")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := `{"event":"` + tt.typ + `","payload":{"object":{"uuid":"u","participant":{"participant_uuid":"p",` + tt.fields + `}}}}`
			e, err := Parse([]byte(body))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := e.Participant.JoinTime; !timesEqual(got, tt.wantJoin) {
				t.Fatalf("JoinTime = %s, want %s", fmtTime(got), fmtTime(tt.wantJoin))
			}
		})
	}
}

func TestParseEventTS(t *testing.T) {
	t.Parallel()
	tests := []struct {
		raw  string
		want int64
	}{
		{`1791277203512`, 1791277203512},
		{`"1791277203512"`, 1791277203512},
		{`1.791277203512e12`, 1791277203512},
		{`1791277203512.0`, 1791277203512},
		{``, 0},
		{`null`, 0},
		{`"abc"`, 0},
		{`true`, 0},
		{`1e300`, 0},
		{`"NaN"`, 0},
		{`-5`, -5},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			t.Parallel()
			body := `{"event":"meeting.ended"`
			if tt.raw != "" {
				body += `,"event_ts":` + tt.raw
			}
			e, err := Parse([]byte(body + `}`))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if e.EventTS != tt.want {
				t.Fatalf("EventTS = %d, want %d", e.EventTS, tt.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, body string }{
		{"empty", ``},
		{"whitespace", "  \n"},
		{"not JSON", `event=meeting.started`},
		{"truncated", `{"event":"meeting.started","payload":{`},
		{"trailing garbage", `{"event":"meeting.started"} x`},
		{"null", `null`},
		{"array", `[{"event":"meeting.started"}]`},
		{"string", `"meeting.started"`},
		{"number", `42`},
		{"empty object", `{}`},
		{"empty event", `{"event":""}`},
		{"blank event", `{"event":"   "}`},
		{"numeric event", `{"event":5}`},
		{"null event", `{"event":null}`},
		{"object event", `{"event":{"type":"meeting.started"}}`},
		{"joined without participant", `{"event":"meeting.participant_joined","payload":{"object":{"uuid":"u"}}}`},
		{"left with participant null", `{"event":"meeting.participant_left","payload":{"object":{"uuid":"u","participant":null}}}`},
		{"breakout with participant string", `{"event":"meeting.participant_joined_breakout_room","payload":{"object":{"participant":"x"}}}`},
		{"joined without payload", `{"event":"meeting.participant_joined"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e, err := Parse([]byte(tt.body))
			if !errors.Is(err, ErrBadPayload) {
				t.Fatalf("Parse = (%+v, %v), want ErrBadPayload", e, err)
			}
			if e != nil {
				t.Fatalf("Parse returned an event with an error: %+v", e)
			}
		})
	}
}

func TestParseTolerant(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body string
		want       Event
	}{
		{
			"unknown fields everywhere",
			`{"event":"meeting.participant_joined","x":[1,{"y":null}],"payload":{"account_id":"a","new":true,
			  "object":{"uuid":"u","future":{"a":1},"participant":{"user_name":"Анна","participant_uuid":"p","public_ip":"1.2.3.4","private_ip":"10.0.0.1","extra":[]}}}}`,
			Event{Type: EventParticipantJoined, MeetingUUID: "u", Participant: &Participant{UserName: "Анна", ParticipantUUID: "p"}},
		},
		{
			"payload is not an object",
			`{"event":"meeting.sharing_started","event_ts":7,"payload":"oops"}`,
			Event{Type: "meeting.sharing_started", EventTS: 7},
		},
		{
			"payload null",
			`{"event":"meeting.started","payload":null}`,
			Event{Type: EventMeetingStarted},
		},
		{
			"object is an array",
			`{"event":"meeting.started","payload":{"object":[1,2]}}`,
			Event{Type: EventMeetingStarted},
		},
		{
			"participant ignored shape on a non-participant event",
			`{"event":"meeting.started","payload":{"object":{"uuid":"u","participant":"x"}}}`,
			Event{Type: EventMeetingStarted, MeetingUUID: "u"},
		},
		{
			"scalar fields as numbers, booleans and null",
			`{"event":"meeting.participant_left","payload":{"object":{"uuid":null,"host_id":12,"topic":false,
			  "participant":{"user_id":16778240,"id":251361636,"user_name":null,"participant_uuid":null,"leave_reason":["x"]}}}}`,
			Event{Type: EventParticipantLeft, HostID: "12", Participant: &Participant{UserID: "16778240", ID: "251361636"}},
		},
		{
			"event type with surrounding spaces",
			`{"event":" meeting.ended ","payload":{"object":{"uuid":"u","end_time":"2026-10-06T10:00:00Z"}}}`,
			Event{Type: EventMeetingEnded, MeetingUUID: "u", EndTime: tp("2026-10-06T10:00:00Z")},
		},
		{
			"duplicate keys: last wins",
			`{"event":"meeting.started","event":"meeting.ended","payload":{"object":{"uuid":"a","uuid":"b"}}}`,
			Event{Type: EventMeetingEnded, MeetingUUID: "b"},
		},
		{
			"url_validation with numeric token",
			`{"event":"endpoint.url_validation","payload":{"plainToken":12345}}`,
			Event{Type: EventURLValidation, PlainToken: "12345"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e, err := Parse([]byte(tt.body))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			checkEvent(t, e, &tt.want)
		})
	}
}

func TestOccurredAt(t *testing.T) {
	t.Parallel()
	join := tp("2026-10-06T09:01:15Z")
	leave := tp("2026-10-06T09:44:55Z")
	start := tp("2026-10-06T09:00:03Z")
	end := tp("2026-10-06T10:31:47Z")
	const ts = int64(1791277999123)
	tsTime := time.UnixMilli(ts).UTC()
	moscow := time.FixedZone("MSK", 3*3600)
	joinMSK := join.In(moscow)

	tests := []struct {
		name string
		e    Event
		want time.Time
	}{
		{"joined uses join_time", Event{Type: EventParticipantJoined, EventTS: ts, StartTime: start, Participant: &Participant{JoinTime: join, LeaveTime: leave}}, *join},
		{"joined without join_time falls back to event_ts", Event{Type: EventParticipantJoined, EventTS: ts, StartTime: start, Participant: &Participant{LeaveTime: leave}}, tsTime},
		{"joined without participant falls back to event_ts", Event{Type: EventParticipantJoined, EventTS: ts}, tsTime},
		{"left uses leave_time", Event{Type: EventParticipantLeft, EventTS: ts, Participant: &Participant{JoinTime: join, LeaveTime: leave}}, *leave},
		{"left without leave_time ignores join_time", Event{Type: EventParticipantLeft, EventTS: ts, Participant: &Participant{JoinTime: join}}, tsTime},
		{"breakout joined uses join_time", Event{Type: EventBreakoutJoined, EventTS: ts, Participant: &Participant{JoinTime: join}}, *join},
		{"breakout left uses leave_time", Event{Type: EventBreakoutLeft, EventTS: ts, Participant: &Participant{LeaveTime: leave}}, *leave},
		{"started uses start_time", Event{Type: EventMeetingStarted, EventTS: ts, StartTime: start, EndTime: end}, *start},
		{"started without start_time", Event{Type: EventMeetingStarted, EventTS: ts, EndTime: end}, tsTime},
		{"ended uses end_time", Event{Type: EventMeetingEnded, EventTS: ts, StartTime: start, EndTime: end}, *end},
		{"ended without end_time", Event{Type: EventMeetingEnded, EventTS: ts, StartTime: start}, tsTime},
		{"zero time counts as missing", Event{Type: EventMeetingEnded, EventTS: ts, EndTime: &time.Time{}}, tsTime},
		{"other types use event_ts", Event{Type: "meeting.participant_admitted", EventTS: ts, StartTime: start, Participant: &Participant{JoinTime: join}}, tsTime},
		{"url_validation uses event_ts", Event{Type: EventURLValidation, EventTS: ts}, tsTime},
		{"nothing known", Event{Type: EventParticipantJoined, Participant: &Participant{}}, time.Time{}},
		{"negative event_ts is unknown", Event{Type: "x", EventTS: -1}, time.Time{}},
		{"result is UTC", Event{Type: EventParticipantJoined, Participant: &Participant{JoinTime: &joinMSK}}, *join},
	}
	if got := (*Event)(nil).OccurredAt(); !got.IsZero() {
		t.Fatalf("nil event: OccurredAt = %v, want zero", got)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.e.OccurredAt()
			if !got.Equal(tt.want) {
				t.Fatalf("OccurredAt = %v, want %v", got, tt.want)
			}
			if !got.IsZero() && got.Location() != time.UTC {
				t.Fatalf("OccurredAt location = %v, want UTC", got.Location())
			}
		})
	}
}

func TestParticipantKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		p    *Participant
		want string
	}{
		{"participant_uuid first", &Participant{ParticipantUUID: "P", UserID: "U", ID: "I", UserName: "N"}, "puuid:P"},
		{"user_id second", &Participant{UserID: "U", ID: "I", UserName: "N"}, "uid:U"},
		{"id third", &Participant{ID: "I", UserName: "N"}, "pid:I"},
		{"normalized name last", &Participant{UserName: "  ИВАНОВ   Иван "}, "name:иванов иван"},
		{"name with ё and emoji", &Participant{UserName: "🦊 Фёдорова\u00a0Алёна"}, "name:🦊 федорова алена"},
		{"blank identifiers are skipped", &Participant{ParticipantUUID: "  ", UserID: "\t", ID: " ", UserName: "Анна"}, "name:анна"},
		{"identifiers are trimmed", &Participant{ParticipantUUID: " P "}, "puuid:P"},
		{"ParticipantUserID is not a key", &Participant{ParticipantUserID: "acct", UserName: "Анна"}, "name:анна"},
		{"nothing at all", &Participant{}, "name:"},
		{"nil participant", nil, ""},
	}
	for _, tt := range tests {
		if got := tt.p.Key(); got != tt.want {
			t.Errorf("%s: Key = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestLeaveReasonText(t *testing.T) {
	t.Parallel()
	tests := []struct{ raw, want string }{
		// Real 2021-2026 strings: no name, "Reason :" with a space.
		{"left the meeting. Reason : left the meeting", "left the meeting"},
		{"left the meeting. Reason : Host ended the meeting.", "Host ended the meeting."},
		{"left the meeting. Reason : Host closed the meeting.", "Host closed the meeting."},
		{"left the meeting. Reason : Removed by host.", "Removed by host."},
		{"left the meeting. Reason : left the meeting with Unknown Failover Reason", "left the meeting with Unknown Failover Reason"},
		// Documented form with the name and HTML.
		{"Jill Chill left the meeting.<br>Reason: Host ended the meeting.", "Host ended the meeting."},
		{"Иван got disconnected from the meeting.<br/>REASON:Network connection error.", "Network connection error."},
		{"x<br />reason :   Host did not join.  ", "Host did not join."},
		{"Tom &amp; Jerry left the meeting.<br>Reason: Unknown reason.", "Unknown reason."},
		{"A left.<BR>Reason:<b>Exceeded</b> free meeting minutes limit.", "Exceeded free meeting minutes limit."},
		// A display name containing the marker does not hijack the result.
		{"Reason: Removed by host left the meeting.<br>Reason: left the meeting", "left the meeting"},
		// No marker: cleaned raw text.
		{"Exceeded free meeting minutes limit.", "Exceeded free meeting minutes limit."},
		{"Host ended the meeting.<br>", "Host ended the meeting."},
		{"  Leave   waiting room. ", "Leave waiting room."},
		// Marker with nothing after it.
		{"left the meeting. Reason :", "left the meeting."},
		{"Reason:", ""},
		{"", ""},
	}
	for _, tt := range tests {
		p := &Participant{LeaveReason: tt.raw}
		if got := p.LeaveReasonText(); got != tt.want {
			t.Errorf("LeaveReasonText(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
	if got := (*Participant)(nil).LeaveReasonText(); got != "" {
		t.Errorf("nil participant: %q", got)
	}
}

func TestEventTypePredicates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		typ                                      string
		participant, join, leave, breakout, supp bool
	}{
		{EventMeetingStarted, false, false, false, false, true},
		{EventMeetingEnded, false, false, false, false, true},
		{EventParticipantJoined, true, true, false, false, true},
		{EventParticipantLeft, true, false, true, false, true},
		{EventBreakoutJoined, true, true, false, true, true},
		{EventBreakoutLeft, true, false, true, true, true},
		{EventURLValidation, false, false, false, false, false},
		{"meeting.participant_admitted", false, false, false, false, false},
		{"meeting.participant_joined_waiting_room", false, false, false, false, false},
		{"", false, false, false, false, false},
		{strings.ToUpper(EventParticipantJoined), false, false, false, false, false},
	}
	for _, tt := range tests {
		if got := IsParticipantEvent(tt.typ); got != tt.participant {
			t.Errorf("IsParticipantEvent(%q) = %v", tt.typ, got)
		}
		if got := IsJoinEvent(tt.typ); got != tt.join {
			t.Errorf("IsJoinEvent(%q) = %v", tt.typ, got)
		}
		if got := IsLeaveEvent(tt.typ); got != tt.leave {
			t.Errorf("IsLeaveEvent(%q) = %v", tt.typ, got)
		}
		if got := IsBreakoutEvent(tt.typ); got != tt.breakout {
			t.Errorf("IsBreakoutEvent(%q) = %v", tt.typ, got)
		}
		if got := IsSupported(tt.typ); got != tt.supp {
			t.Errorf("IsSupported(%q) = %v", tt.typ, got)
		}
	}
}
