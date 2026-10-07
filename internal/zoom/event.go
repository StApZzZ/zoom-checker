package zoom

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"

	"zoom-checker/internal/names"
)

// Event is a normalized Zoom webhook event. Only the fields the service uses are
// kept; the raw body is stored separately and stays the source of truth.
type Event struct {
	Type        string // "meeting.participant_joined", ...
	EventTS     int64  // unix ms (event_ts); orders same-second events
	MeetingID   string // object.id as text (digits; JSON string, number or null accepted), trimmed
	MeetingUUID string // object.uuid; for breakout events the MAIN meeting's uuid
	HostID      string
	Topic       string
	// StartTime and EndTime are object.start_time / object.end_time, set whenever
	// present. They are authoritative on meeting.started / meeting.ended; participant
	// events repeat the meeting's start_time.
	StartTime, EndTime *time.Time
	// Participant is object.participant; never nil for IsParticipantEvent types.
	Participant *Participant
	PlainToken  string // payload.plainToken of endpoint.url_validation
}

// Participant is object.participant of participant events. Identifiers are opaque
// strings and may be empty (guests that are not signed in have no ID, email or
// ParticipantUserID).
type Participant struct {
	UserName          string // display name as typed by the participant (untrusted)
	ParticipantUUID   string // per join; changes on rejoin, kept inside breakout rooms
	ID                string // deprecated by Zoom, often empty
	UserID            string // meeting-local, per join/device; differs inside breakout rooms
	ParticipantUserID string // Zoom account id when signed in
	Email             string
	LeaveReason       string     // raw leave_reason, see LeaveReasonText
	JoinTime          *time.Time // join_time; on non-leave events also date_time (Zoom's doc examples); participant_left normally has none
	LeaveTime         *time.Time // leave_time
	BreakoutRoomUUID  string     // object.breakout_room_uuid (breakout events)
	ParentUserID      string     // main-meeting user_id (breakout events)
}

// Parse decodes a webhook body. Decoding is tolerant: unknown fields are ignored,
// scalar fields accept strings, numbers or null, and unparseable times become nil.
// It fails with an error wrapping ErrBadPayload when the body is not a JSON object,
// has no string "event", or is a participant event without a participant object.
func Parse(body []byte) (*Event, error) {
	var env rawEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadPayload, err)
	}
	if !env.isObject {
		return nil, fmt.Errorf("%w: not a JSON object", ErrBadPayload)
	}
	var typ string
	if len(env.Event) == 0 || env.Event[0] != '"' || json.Unmarshal(env.Event, &typ) != nil {
		return nil, fmt.Errorf("%w: missing \"event\"", ErrBadPayload)
	}
	typ = strings.TrimSpace(typ)
	if typ == "" {
		return nil, fmt.Errorf("%w: missing \"event\"", ErrBadPayload)
	}

	e := &Event{Type: typ, EventTS: int64(env.EventTS)}
	pl := env.Payload.v
	if pl == nil {
		pl = &rawPayload{}
	}
	e.PlainToken = string(pl.PlainToken)
	if o := pl.Object.v; o != nil {
		e.MeetingID = strings.TrimSpace(string(o.ID))
		e.MeetingUUID = string(o.UUID)
		e.HostID = string(o.HostID)
		e.Topic = string(o.Topic)
		e.StartTime = o.StartTime.t
		e.EndTime = o.EndTime.t
		if p := o.Participant.v; p != nil {
			e.Participant = &Participant{
				UserName:          string(p.UserName),
				ParticipantUUID:   string(p.ParticipantUUID),
				ID:                string(p.ID),
				UserID:            string(p.UserID),
				ParticipantUserID: string(p.ParticipantUserID),
				Email:             string(p.Email),
				LeaveReason:       string(p.LeaveReason),
				JoinTime:          p.JoinTime.t,
				LeaveTime:         p.LeaveTime.t,
				BreakoutRoomUUID:  string(o.BreakoutRoomUUID),
				ParentUserID:      string(p.ParentUserID),
			}
			// date_time is the event's own time: on joins (Zoom's doc examples) it is
			// the join time. On leaves its meaning is undocumented (the doc examples
			// repeat the meeting's start_time there), and the attendance engine would
			// credit an orphan leave from JoinTime, so it is not used for leaves.
			if e.Participant.JoinTime == nil && !IsLeaveEvent(typ) {
				e.Participant.JoinTime = p.DateTime.t
			}
		}
	}
	if IsParticipantEvent(e.Type) && e.Participant == nil {
		return nil, fmt.Errorf("%w: %s without payload.object.participant", ErrBadPayload, e.Type)
	}
	return e, nil
}

// OccurredAt is when the event happened: join_time for joins, leave_time for
// leaves, start_time / end_time for meeting started / ended, falling back to
// event_ts when that time is missing (and for all other types). Zero if neither
// is known, or for a nil event. Always UTC.
func (e *Event) OccurredAt() time.Time {
	if e == nil {
		return time.Time{}
	}
	var t *time.Time
	switch {
	case IsJoinEvent(e.Type):
		if e.Participant != nil {
			t = e.Participant.JoinTime
		}
	case IsLeaveEvent(e.Type):
		if e.Participant != nil {
			t = e.Participant.LeaveTime
		}
	case e.Type == EventMeetingStarted:
		t = e.StartTime
	case e.Type == EventMeetingEnded:
		t = e.EndTime
	}
	if t != nil && !t.IsZero() {
		return t.UTC()
	}
	if e.EventTS > 0 {
		return time.UnixMilli(e.EventTS).UTC()
	}
	return time.Time{}
}

// Key identifies one participant connection (device + join) inside a meeting, by
// the most specific identifier present: "puuid:"+participant_uuid, "uid:"+user_id,
// "pid:"+id, else "name:"+normalized display name. Nil-safe ("" for nil).
func (p *Participant) Key() string {
	if p == nil {
		return ""
	}
	if v := strings.TrimSpace(p.ParticipantUUID); v != "" {
		return "puuid:" + v
	}
	if v := strings.TrimSpace(p.UserID); v != "" {
		return "uid:" + v
	}
	if v := strings.TrimSpace(p.ID); v != "" {
		return "pid:" + v
	}
	return "name:" + names.Normalize(p.UserName)
}

var (
	htmlTagRe      = regexp.MustCompile(`<[^>]*>`)
	reasonMarkerRe = regexp.MustCompile(`(?i)reason\s*:`)
)

// LeaveReasonText returns the human part of leave_reason, e.g. "Host ended the
// meeting." from "Jill Chill left the meeting.<br>Reason: Host ended the meeting."
// or "left the meeting" from "left the meeting. Reason : left the meeting". HTML
// tags are dropped and entities decoded; text without a "Reason:" marker is
// returned cleaned but otherwise as is. The last marker wins, so a display name
// containing "Reason:" cannot hijack the result. Nil-safe.
func (p *Participant) LeaveReasonText() string {
	if p == nil {
		return ""
	}
	s := htmlTagRe.ReplaceAllString(p.LeaveReason, " ")
	s = strings.Join(strings.Fields(html.UnescapeString(s)), " ")
	locs := reasonMarkerRe.FindAllStringIndex(s, -1)
	if len(locs) == 0 {
		return s
	}
	last := locs[len(locs)-1]
	if after := strings.TrimSpace(s[last[1]:]); after != "" {
		return after
	}
	// "... Reason:" with nothing after it: keep what precedes the marker.
	return strings.TrimSpace(s[:last[0]])
}

// --- tolerant decoding -------------------------------------------------------

type rawEnvelope struct {
	isObject bool
	Event    json.RawMessage     `json:"event"`
	EventTS  flexInt             `json:"event_ts"`
	Payload  lenient[rawPayload] `json:"payload"`
}

// UnmarshalJSON records whether the top level is an object (json.Unmarshal of
// "null" into a struct succeeds silently) and ignores any other top-level shape
// so Parse can report it.
func (r *rawEnvelope) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || b[0] != '{' {
		return nil
	}
	type plain rawEnvelope // drops this method to avoid recursion
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*r = rawEnvelope(p)
	r.isObject = true
	return nil
}

type rawPayload struct {
	PlainToken flexString         `json:"plainToken"`
	Object     lenient[rawObject] `json:"object"`
}

type rawObject struct {
	ID               flexString              `json:"id"`
	UUID             flexString              `json:"uuid"`
	HostID           flexString              `json:"host_id"`
	Topic            flexString              `json:"topic"`
	StartTime        flexTime                `json:"start_time"`
	EndTime          flexTime                `json:"end_time"`
	BreakoutRoomUUID flexString              `json:"breakout_room_uuid"`
	Participant      lenient[rawParticipant] `json:"participant"`
}

type rawParticipant struct {
	UserID            flexString `json:"user_id"`
	UserName          flexString `json:"user_name"`
	ID                flexString `json:"id"`
	ParticipantUUID   flexString `json:"participant_uuid"`
	ParticipantUserID flexString `json:"participant_user_id"`
	ParentUserID      flexString `json:"parent_user_id"`
	Email             flexString `json:"email"`
	JoinTime          flexTime   `json:"join_time"`
	DateTime          flexTime   `json:"date_time"`
	LeaveTime         flexTime   `json:"leave_time"`
	LeaveReason       flexString `json:"leave_reason"`
}

// lenient decodes a nested object and treats any other JSON value (null, string,
// array, ...) as absent instead of failing the whole event.
type lenient[T any] struct{ v *T }

func (l *lenient[T]) UnmarshalJSON(b []byte) error {
	l.v = nil
	if len(b) == 0 || b[0] != '{' {
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	l.v = &v
	return nil
}

// flexString accepts a JSON string, or a number kept as its exact literal text
// (no float rounding: 11+ digit meeting ids survive), and maps null / booleans /
// objects / arrays to "".
type flexString string

func (s *flexString) UnmarshalJSON(b []byte) error {
	*s = ""
	switch {
	case len(b) == 0:
	case b[0] == '"':
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*s = flexString(v)
	case b[0] == '-' || (b[0] >= '0' && b[0] <= '9'):
		*s = flexString(b)
	}
	return nil
}

// flexInt accepts an integer as a JSON number (also in float/exponent form) or a
// numeric string; anything else, or out of range, is 0.
type flexInt int64

func (n *flexInt) UnmarshalJSON(b []byte) error {
	*n = 0
	var lit string
	switch {
	case len(b) == 0:
		return nil
	case b[0] == '"':
		if err := json.Unmarshal(b, &lit); err != nil {
			return err
		}
		lit = strings.TrimSpace(lit)
	case b[0] == '-' || (b[0] >= '0' && b[0] <= '9'):
		lit = string(b)
	default:
		return nil
	}
	if v, err := strconv.ParseInt(lit, 10, 64); err == nil {
		*n = flexInt(v)
		return nil
	}
	// 2^63 as float64 is the first value that no longer fits in int64.
	if f, err := strconv.ParseFloat(lit, 64); err == nil && f >= -(1<<63) && f < 1<<63 {
		*n = flexInt(f)
	}
	return nil
}

// flexTime accepts an ISO-8601 / RFC 3339 string (fractional seconds optional,
// "Z" or an offset; no zone means UTC). Anything else is nil.
type flexTime struct{ t *time.Time }

func (ft *flexTime) UnmarshalJSON(b []byte) error {
	ft.t = nil
	if len(b) == 0 || b[0] != '"' {
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if t, ok := parseTime(s); ok {
		ft.t = &t
	}
	return nil
}

var timeLayouts = []string{
	time.RFC3339Nano, // also matches values without fractional seconds
	"2006-01-02T15:04:05.999999999Z0700",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
}

func parseTime(s string) (time.Time, bool) {
	// RFC 3339 allows lower-case "t" and "z"; the rest of a timestamp is digits and punctuation.
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
