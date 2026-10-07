package zoom

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"strconv"
	"time"
)

// DedupeKey returns the idempotency key (hex SHA-256) of an event (TZ §22):
//
//   - participant events: type | meeting_uuid | breakout_room_uuid | participant.Key() |
//     join_time (joins) or leave_time (leaves) in RFC 3339 nano UTC, or "ets:"+event_ts
//     when that time is missing;
//   - meeting.started / meeting.ended with a uuid: type | meeting_uuid;
//   - everything else: type | event_ts | sha256(rawBody).
//
// event_ts is deliberately left out of participant and meeting keys: Zoom normally
// keeps it on retries but may redeliver an event with a new one. Components are
// length-prefixed before hashing, so a "|" inside a value cannot make two
// different events collide. e may be nil (unparseable body): the key then only
// depends on rawBody.
func DedupeKey(e *Event, rawBody []byte) string {
	h := sha256.New()
	switch {
	case e == nil:
		bodySum := sha256.Sum256(rawBody)
		writeParts(h, "unparsed", hex.EncodeToString(bodySum[:]))
	case IsParticipantEvent(e.Type) && e.Participant != nil:
		p := e.Participant
		at := p.LeaveTime
		if IsJoinEvent(e.Type) {
			at = p.JoinTime
		}
		var when string
		if at != nil && !at.IsZero() {
			when = at.UTC().Format(time.RFC3339Nano)
		} else {
			when = "ets:" + strconv.FormatInt(e.EventTS, 10)
		}
		writeParts(h, e.Type, e.MeetingUUID, p.BreakoutRoomUUID, p.Key(), when)
	case (e.Type == EventMeetingStarted || e.Type == EventMeetingEnded) && e.MeetingUUID != "":
		writeParts(h, e.Type, e.MeetingUUID)
	default:
		bodySum := sha256.Sum256(rawBody)
		writeParts(h, e.Type, strconv.FormatInt(e.EventTS, 10), hex.EncodeToString(bodySum[:]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeParts(h hash.Hash, parts ...string) {
	for _, p := range parts {
		h.Write([]byte(strconv.Itoa(len(p))))
		h.Write([]byte{':'})
		h.Write([]byte(p))
		h.Write([]byte{'|'})
	}
}
