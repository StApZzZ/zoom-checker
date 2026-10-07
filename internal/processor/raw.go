package processor

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"zoom-checker/internal/model"
	"zoom-checker/internal/zoom"
)

const (
	// UnknownEventType is stored when the body has no readable "event" field.
	UnknownEventType = "unknown"

	maxParticipantNameRunes = 300
	maxEventTypeRunes       = 100
)

// Explanations stored in the error column of ignored events (admin journal,
// Russian, plain words).
const (
	noteURLValidation  = "Проверка адреса вебхука от Zoom: ответ отправлен сразу, для учёта посещаемости это событие не нужно"
	noteUnsupported    = "Этот тип события Zoom не используется для учёта посещаемости"
	noteNoMeeting      = "В событии нет ни номера конференции, ни идентификатора сеанса (uuid) — его не к чему привязать"
	noteNoUUID         = "В событии нет идентификатора сеанса конференции (uuid) — его не к чему привязать"
	noteUnknownSession = "В событии нет номера конференции, а сеанс с таким uuid ещё не встречался (так Zoom присылает " +
		"события некоторых комнат для групповой работы) — событие нельзя связать с занятием"
	noteParsePrefix = "Не удалось разобрать содержимое события Zoom: "
)

// BuildRawEvent prepares the zoom_webhook_events row for a verified request body.
// ev/parseErr come from zoom.Parse(body). Unsupported or unparseable events get
// status "ignored" (with Error explaining why); supported ones "pending".
//
// The payload is kept byte-for-byte; the extracted columns (type, meeting, name)
// only feed the admin journal and the worker's queries. NUL characters are removed
// from them (PostgreSQL TEXT rejects NUL) and they are length-limited.
func BuildRawEvent(body []byte, ev *zoom.Event, parseErr error, receivedAt time.Time) *model.WebhookEvent {
	receivedAt = receivedAt.UTC()
	if parseErr != nil {
		ev = nil
	}
	e := &model.WebhookEvent{
		EventType:        UnknownEventType,
		DedupeKey:        zoom.DedupeKey(ev, body),
		Payload:          string(body),
		ReceivedAt:       receivedAt,
		ProcessingStatus: model.EventPending,
	}
	ignore := func(note string) *model.WebhookEvent {
		e.ProcessingStatus = model.EventIgnored
		e.Error = note
		e.ProcessedAt = &receivedAt
		return e
	}

	if ev == nil {
		if t := sniffEventType(body); t != "" {
			e.EventType = t
		}
		if parseErr == nil {
			return ignore(noteParsePrefix + "пустое событие")
		}
		return ignore(truncateRunes(noteParsePrefix+parseErr.Error(), maxErrorRunes))
	}

	ev = sanitizeEvent(ev)
	if t := cleanText(ev.Type, maxEventTypeRunes); t != "" {
		e.EventType = t
	}
	e.EventTS = ev.EventTS
	// Meeting id and uuid are stored exactly as the worker will see them after
	// sanitizeEvent: the projection rebuild selects raw rows by meeting_uuid.
	e.MeetingID = ev.MeetingID
	e.MeetingUUID = ev.MeetingUUID
	if ev.Participant != nil {
		e.ParticipantName = cleanText(ev.Participant.UserName, maxParticipantNameRunes)
	}
	if at := ev.OccurredAt(); !at.IsZero() {
		at = at.UTC()
		e.OccurredAt = &at
	}
	if note := unsupportedNote(ev); note != "" {
		return ignore(note)
	}
	return e
}

// parseEvent is zoom.Parse followed by sanitizeEvent; the worker and the rebuild
// use it so they see the same identifiers BuildRawEvent stored.
func parseEvent(body []byte) (*zoom.Event, error) {
	ev, err := zoom.Parse(body)
	if err != nil {
		return nil, err
	}
	return sanitizeEvent(ev), nil
}

// sanitizeEvent returns a copy of ev without NUL characters in the text fields we
// store or compare (PostgreSQL TEXT rejects NUL).
func sanitizeEvent(ev *zoom.Event) *zoom.Event {
	c := *ev
	c.Type = stripNUL(c.Type)
	c.MeetingID = stripNUL(c.MeetingID)
	c.MeetingUUID = stripNUL(c.MeetingUUID)
	c.HostID = stripNUL(c.HostID)
	c.Topic = stripNUL(c.Topic)
	if ev.Participant != nil {
		pc := *ev.Participant
		for _, f := range []*string{&pc.UserName, &pc.ParticipantUUID, &pc.ID, &pc.UserID, &pc.ParticipantUserID,
			&pc.Email, &pc.LeaveReason, &pc.BreakoutRoomUUID, &pc.ParentUserID} {
			*f = stripNUL(*f)
		}
		c.Participant = &pc
	}
	return &c
}

// unsupportedNote explains why a parsed event cannot take part in attendance, or
// returns "" when it can.
func unsupportedNote(ev *zoom.Event) string {
	switch {
	case ev.Type == zoom.EventURLValidation:
		return noteURLValidation
	case !zoom.IsSupported(ev.Type):
		return noteUnsupported
	case ev.MeetingUUID == "" && ev.MeetingID == "":
		return noteNoMeeting
	case ev.MeetingUUID == "":
		return noteNoUUID
	}
	return ""
}

func noteParseError(err error) string {
	return truncateRunes(noteParsePrefix+err.Error(), maxErrorRunes)
}

// sniffEventType reads the "event" string of a body zoom.Parse rejected (e.g. a
// participant event without a participant), so the journal still shows its type.
func sniffEventType(body []byte) string {
	var env struct {
		Event any `json:"event"`
	}
	if json.Unmarshal(body, &env) != nil {
		return ""
	}
	s, _ := env.Event.(string)
	return cleanText(s, maxEventTypeRunes)
}

// cleanText strips NUL characters and invalid UTF-8, trims and limits s to max runes.
func cleanText(s string, max int) string {
	s = strings.TrimSpace(strings.ToValidUTF8(stripNUL(s), "�"))
	return truncateRunes(s, max)
}

func stripNUL(s string) string {
	if strings.IndexByte(s, 0) < 0 {
		return s
	}
	return strings.ReplaceAll(s, "\x00", "")
}

// truncateRunes cuts s to at most n runes without splitting a character.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}
