// Package zoom handles Zoom Meeting webhooks: request signature verification,
// endpoint.url_validation answers, tolerant payload parsing and the idempotency
// (dedupe) key of an event. It does no I/O and reads no clock.
//
// Zoom facts this package relies on (researched 2026):
//   - x-zm-request-timestamp is unix seconds, event_ts is unix milliseconds;
//     join/leave/start/end times are ISO-8601 with second precision.
//   - The signature covers the raw body bytes; re-serialized JSON does not verify
//     (non-ASCII names, key order: real participant_left has "payload" before "event").
//   - Real payloads carry fields that are not in the schema (participant.public_ip,
//     private_ip), so decoding must ignore unknown fields.
//   - object.id is a string today; older payloads used a JSON number or null.
//   - participant_left has no join_time; participant_uuid is per join (changes on
//     rejoin) and stays the same inside breakout rooms, whose events carry the
//     main meeting's uuid in object.uuid plus object.breakout_room_uuid.
package zoom

import "errors"

// Sentinel errors. Callers match them with errors.Is; the wrapped text only adds
// a non-secret reason that is safe to log.
var (
	// ErrMissingHeaders: x-zm-signature or x-zm-request-timestamp is absent or malformed.
	ErrMissingHeaders = errors.New("zoom: missing or malformed signature headers")
	// ErrBadSignature: the HMAC does not match (wrong secret token or altered request).
	ErrBadSignature = errors.New("zoom: bad webhook signature")
	// ErrStale: the signed timestamp is outside the accepted window (replay or clock skew).
	ErrStale = errors.New("zoom: webhook timestamp outside the accepted window")
	// ErrBadPayload: the body is not a Zoom event envelope.
	ErrBadPayload = errors.New("zoom: bad webhook payload")
)

// Event types (the "event" field of the envelope).
const (
	EventURLValidation     = "endpoint.url_validation"
	EventMeetingStarted    = "meeting.started"
	EventMeetingEnded      = "meeting.ended"
	EventParticipantJoined = "meeting.participant_joined"
	EventParticipantLeft   = "meeting.participant_left"
	EventBreakoutJoined    = "meeting.participant_joined_breakout_room"
	EventBreakoutLeft      = "meeting.participant_left_breakout_room"
)

// IsParticipantEvent reports whether t is a supported participant presence event
// (main room or breakout room, join or leave).
func IsParticipantEvent(t string) bool {
	return IsJoinEvent(t) || IsLeaveEvent(t)
}

// IsJoinEvent reports whether t is a participant join (main room or breakout room).
func IsJoinEvent(t string) bool {
	return t == EventParticipantJoined || t == EventBreakoutJoined
}

// IsLeaveEvent reports whether t is a participant leave (main room or breakout room).
func IsLeaveEvent(t string) bool {
	return t == EventParticipantLeft || t == EventBreakoutLeft
}

// IsBreakoutEvent reports whether t is a breakout-room join or leave. Such events
// carry the main meeting's uuid and count as presence in the parent meeting.
func IsBreakoutEvent(t string) bool {
	return t == EventBreakoutJoined || t == EventBreakoutLeft
}

// IsSupported reports whether events of type t are processed by the attendance
// pipeline. Everything else (including endpoint.url_validation, which is answered
// synchronously) is stored with status "ignored".
func IsSupported(t string) bool {
	return t == EventMeetingStarted || t == EventMeetingEnded || IsParticipantEvent(t)
}
