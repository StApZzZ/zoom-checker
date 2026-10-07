// Package attendance is the pure attendance engine (docs/architecture.md §5.4).
//
// Compute turns the raw participant events of a lesson's Zoom sessions, the group
// roster, the teacher's manual mappings and the business settings into the full
// projection of the lesson: per-device presence intervals, one row per Zoom display
// name and one row per roster student. It does no I/O and never reads the clock
// (Input.Now is the only notion of "now"), so the processor can rebuild a lesson's
// projection from scratch on every event and get the same answer for the same input,
// whatever order the events arrived in.
package attendance

import (
	"fmt"
	"time"

	"zoom-checker/internal/model"
)

// EventKind tells whether a participant event starts or ends presence.
type EventKind int

const (
	// Join is meeting.participant_joined or meeting.participant_joined_breakout_room.
	Join EventKind = iota + 1
	// Leave is meeting.participant_left or meeting.participant_left_breakout_room.
	Leave
)

// String implements fmt.Stringer.
func (k EventKind) String() string {
	switch k {
	case Join:
		return "join"
	case Leave:
		return "leave"
	}
	return fmt.Sprintf("EventKind(%d)", int(k))
}

// Leave reasons assigned by the engine when it closes an interval itself.
// Intervals closed by a Zoom leave event keep Zoom's leave_reason text verbatim.
const (
	ReasonMeetingEnded   = "meeting_ended"   // open at meeting.ended, closed at the session's EndedAt
	ReasonLessonFinished = "lesson_finished" // open when the teacher finished the lesson (no meeting.ended)
	ReasonRenamed        = "renamed"         // the same device joined again under another name
)

// ParticipantEvent is one participant join/leave webhook of a session, already parsed.
type ParticipantEvent struct {
	EventID     int64     // raw webhook row id; last tie-breaker for ordering
	Kind        EventKind // Join or Leave; other values are ignored
	MeetingUUID string    // the (parent) session UUID, also for breakout-room events
	At          time.Time // join_time / leave_time (second precision); events with a zero At are ignored
	EventTS     int64     // Zoom event_ts in ms; orders events within the same second (0 = unknown)
	// Key identifies the device connection (zoom.Participant.Key()). When empty it is
	// derived the same way: "puuid:"+ParticipantUUID | "uid:"+UserID | "pid:"+ParticipantID | "name:"+normalized name.
	Key             string
	ParticipantUUID string
	ParticipantID   string // participant id / participant_user_id; compared with Session.HostID
	UserID          string // meeting-local user_id; also compared with Session.HostID
	UserName        string // raw Zoom display name
	LeaveReason     string
	JoinTime        *time.Time // participant_left may carry join_time; a zero value counts as absent
	Breakout        bool       // breakout-room variant; tracked apart from main-room presence
}

// Session is one actual Zoom meeting instance (meeting UUID) linked to the lesson.
type Session struct {
	MeetingUUID  string
	HostID       string
	StartedAt    *time.Time
	EndedAt      *time.Time
	FirstEventAt time.Time
}

// LessonInfo is the part of a lesson the engine needs.
type LessonInfo struct {
	ID               int64
	PlannedStart     time.Time
	PlannedEnd       time.Time
	ManualFinishedAt *time.Time // teacher pressed "finish"
}

// RosterStudent is a student of the lesson's group.
// Only Active students take part in automatic name matching; manual mappings may
// target any roster student (archived students keep their history).
type RosterStudent struct {
	StudentID         int64
	FullName          string
	NormalizedName    string
	NormalizedAliases []string
	Active            bool
}

// Mapping is a teacher decision about one normalized display name in this lesson.
type Mapping struct {
	Action    model.MappingAction
	StudentID int64 // for model.MappingStudent
}

// Input is everything Compute needs. Map keys are normalized display names
// (they are re-normalized defensively).
type Input struct {
	Lesson            LessonInfo
	Sessions          []Session
	Events            []ParticipantEvent
	Roster            []RosterStudent
	Mappings          map[string]Mapping
	IgnoredNames      map[string]bool
	IgnoreHost        bool
	PresentMinPercent int       // 0 = present if seen at all; clamped to [0, 100]
	Now               time.Time // open intervals are counted up to Now
}

// Output is the lesson projection "as of" Input.Now.
type Output struct {
	AsOf         time.Time // = Input.Now; live clients add (clientNow - AsOf) for online rows
	ActualStart  *time.Time
	ActualEnd    *time.Time
	Status       model.LessonStatus
	Participants []ParticipantResult // one per normalized display name that has an interval
	Students     []StudentResult     // one per roster student, in roster order
	Intervals    []IntervalResult    // per-device presence intervals with their resolution
}

// ParticipantResult is one normalized Zoom display name inside the lesson.
type ParticipantResult struct {
	NormalizedName      string
	DisplayName         string // raw name of the most recent join (of the latest event if it never joined under it)
	Resolution          model.Resolution
	StudentID           *int64 // for matched / manual
	IgnoreReason        model.IgnoreReason
	CandidateStudentIDs []int64             // for conflict, ascending
	CurrentStatus       model.CurrentStatus // online | left
	FirstJoinAt         *time.Time
	LastLeaveAt         *time.Time // latest leave among closed intervals; nil if none closed
	TotalSeconds        int64      // union of the name's intervals as of Now
	Devices             int        // distinct participant keys
	OnlineDevices       int        // distinct participant keys with an open interval
}

// StudentResult is the computed attendance of one roster student.
type StudentResult struct {
	StudentID     int64
	CurrentStatus model.CurrentStatus // online | left | not_seen
	FirstJoinAt   *time.Time
	LastLeaveAt   *time.Time
	TotalSeconds  int64  // union of all intervals of all names resolved to the student
	LateSeconds   *int64 // max(0, first join - planned start); nil if not seen
	// EarlyLeaveSeconds = max(0, end - last leave), only when the lesson is finished and
	// the student was seen. end is ActualEnd, but never later than the moment the last
	// Zoom session ended (TZ §20: staying until Zoom closed is never an early leave).
	EarlyLeaveSeconds *int64
	AutoStatus        model.FinalStatus
	ZoomNames         []string // display names resolved to the student, in participant order
	OnlineDevices     int
	Seen              bool
}

// IntervalResult is one presence interval of one device connection.
type IntervalResult struct {
	NormalizedName  string // final name (after a rename reported at leave)
	ParticipantKey  string // device key, without the breakout namespace
	ParticipantUUID string
	UserName        string // raw display name of the interval (final name)
	RenamedFrom     string // raw join name when the leave event reported a different name
	MeetingUUID     string
	JoinAt          time.Time
	LeaveAt         *time.Time // nil = still online
	DurationSeconds *int64     // nil while open
	LeaveReason     string
	StudentID       *int64 // for matched / manual names
	Resolution      model.Resolution
	Breakout        bool
}
