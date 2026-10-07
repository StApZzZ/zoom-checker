// Package model holds the domain types shared by all packages.
// It must stay free of logic and of dependencies on other internal packages.
package model

import "time"

// Role of an application user.
type Role string

const (
	RoleAdmin   Role = "admin"
	RoleTeacher Role = "teacher"
)

// AuthSource tells where a user account comes from.
type AuthSource string

const (
	AuthLocal AuthSource = "local"
	AuthOIDC  AuthSource = "oidc"
	AuthLDAP  AuthSource = "ldap"
)

// Status of groups and students.
type Status string

const (
	StatusActive   Status = "active"
	StatusArchived Status = "archived"
)

// LessonStatus is the lifecycle state of a lesson (TZ §4.3).
type LessonStatus string

const (
	LessonPlanned  LessonStatus = "planned"
	LessonActive   LessonStatus = "active"
	LessonFinished LessonStatus = "finished"
)

// CurrentStatus is the live status of a student during a lesson (TZ §13).
type CurrentStatus string

const (
	CurrentOnline  CurrentStatus = "online"
	CurrentLeft    CurrentStatus = "left"
	CurrentNotSeen CurrentStatus = "not_seen"
)

// FinalStatus is the attendance verdict (TZ §18).
type FinalStatus string

const (
	FinalPresent FinalStatus = "present"
	FinalAbsent  FinalStatus = "absent"
)

// Resolution says how a Zoom display name was resolved inside a lesson.
type Resolution string

const (
	ResolutionMatched      Resolution = "matched"      // exact normalized match with one student/alias
	ResolutionManual       Resolution = "manual"       // teacher mapped it to a student
	ResolutionUnidentified Resolution = "unidentified" // no match (or teacher forced "none")
	ResolutionConflict     Resolution = "conflict"     // matches several students
	ResolutionIgnored      Resolution = "ignored"      // host / ignore list / teacher said "not a student"
)

// IgnoreReason explains why a participant is ignored.
type IgnoreReason string

const (
	IgnoreNone   IgnoreReason = ""
	IgnoreHost   IgnoreReason = "host"
	IgnoreList   IgnoreReason = "list"
	IgnoreManual IgnoreReason = "manual"
)

// MappingAction is a teacher decision about a display name in a lesson.
type MappingAction string

const (
	MappingStudent MappingAction = "student" // name belongs to StudentID
	MappingIgnore  MappingAction = "ignore"  // not a student (teacher, guest, device)
	MappingNone    MappingAction = "none"    // do NOT auto-match this name (undo a wrong auto match)
)

// LinkMode of a Zoom session to a lesson.
type LinkMode string

const (
	LinkAuto   LinkMode = "auto"
	LinkManual LinkMode = "manual"
)

// EventStatus is the processing status of a raw webhook event (TZ §21).
type EventStatus string

const (
	EventPending   EventStatus = "pending"
	EventProcessed EventStatus = "processed"
	EventDuplicate EventStatus = "duplicate"
	EventIgnored   EventStatus = "ignored"
	EventFailed    EventStatus = "failed"
)

// User is an application account (teacher or admin).
type User struct {
	ID                 int64
	Username           string
	DisplayName        string
	Email              string
	Role               Role
	AuthSource         AuthSource
	ExternalID         string
	PasswordHash       string
	MustChangePassword bool
	Disabled           bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
	LastLoginAt        *time.Time
}

// Session is a logged-in browser session. IDHash is sha256(cookie token) in hex.
type Session struct {
	IDHash      string
	UserID      int64
	CreatedAt   time.Time
	ExpiresAt   time.Time
	LastSeenAt  time.Time
	IP          string
	UserAgent   string
	AuthSource  AuthSource
	OIDCIDToken string
}

// Group is a study group (table student_groups).
type Group struct {
	ID                     int64
	Name                   string
	NormalizedName         string
	Description            string
	Status                 Status
	DefaultDurationMinutes int
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// Student belongs to exactly one group.
type Student struct {
	ID             int64
	GroupID        int64
	ExternalID     string
	FullName       string
	NormalizedName string
	Status         Status
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Aliases        []Alias // filled by store when requested
}

// Alias is an extra exact display name of a student (e.g. remembered manual mapping).
type Alias struct {
	ID              int64
	StudentID       int64
	GroupID         int64
	Alias           string
	NormalizedAlias string
	CreatedBy       *int64
	CreatedAt       time.Time
}

// Lesson is one class session bound to a Zoom meeting ID.
type Lesson struct {
	ID               int64
	GroupID          int64
	Title            string
	ZoomMeetingID    string // digits only
	ZoomJoinURL      string
	PlannedStartAt   time.Time
	PlannedEndAt     time.Time
	ActualStartAt    *time.Time
	ActualEndAt      *time.Time
	Status           LessonStatus
	ManualFinishedAt *time.Time
	CreatedBy        *int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// ZoomMeeting is one actual Zoom meeting instance (one meeting UUID).
type ZoomMeeting struct {
	ID           int64
	MeetingUUID  string
	MeetingID    string
	Topic        string
	HostID       string
	LessonID     *int64
	LinkMode     LinkMode
	StartedAt    *time.Time
	EndedAt      *time.Time
	FirstEventAt time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// WebhookEvent is a raw Zoom webhook as received (table zoom_webhook_events).
type WebhookEvent struct {
	ID               int64
	EventType        string
	EventTS          int64 // ms, from payload
	MeetingID        string
	MeetingUUID      string
	ParticipantName  string
	OccurredAt       *time.Time
	DedupeKey        string
	Payload          string // raw body, byte-for-byte
	ReceivedAt       time.Time
	ProcessedAt      *time.Time
	ProcessingStatus EventStatus
	Attempts         int
	NextAttemptAt    *time.Time
	Error            string
}

// Participant is the projection of one display name inside a lesson (table zoom_participants).
type Participant struct {
	ID                  int64
	LessonID            int64
	NormalizedName      string
	DisplayName         string
	Resolution          Resolution
	StudentID           *int64
	IgnoreReason        IgnoreReason
	CandidateStudentIDs []int64
	CurrentStatus       CurrentStatus // online | left
	FirstJoinAt         *time.Time
	LastLeaveAt         *time.Time
	TotalSeconds        int64
	Devices             int
	ComputedAt          time.Time
	UpdatedAt           time.Time
}

// Attendance is the per-lesson, per-student result (table attendance).
type Attendance struct {
	ID                   int64
	LessonID             int64
	StudentID            int64
	FirstJoinAt          *time.Time
	LastLeaveAt          *time.Time
	TotalDurationSeconds int64
	LateSeconds          *int64
	EarlyLeaveSeconds    *int64
	CurrentStatus        CurrentStatus
	AutoStatus           FinalStatus
	ManualStatus         *FinalStatus
	FinalStatus          FinalStatus
	Comment              string
	ManualBy             *int64
	ManualAt             *time.Time
	ComputedAt           time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// Interval is one presence interval of one Zoom participant device (table attendance_intervals).
type Interval struct {
	ID                  int64
	LessonID            int64
	ZoomParticipantID   *int64
	AttendanceID        *int64
	MeetingUUID         string
	ZoomParticipantUUID string
	ZoomUserName        string
	JoinAt              time.Time
	LeaveAt             *time.Time // nil = still online
	DurationSeconds     *int64     // nil while open
	LeaveReason         string
}

// Mapping is a manual teacher decision about a display name in a lesson.
type Mapping struct {
	ID             int64
	LessonID       int64
	NormalizedName string
	Action         MappingAction
	StudentID      *int64
	CreatedBy      *int64
	CreatedAt      time.Time
}

// AuditEntry records manual changes (TZ §27).
type AuditEntry struct {
	ID            int64
	At            time.Time
	UserID        *int64
	UserName      string
	Action        string
	EntityType    string
	EntityID      string
	LessonID      *int64
	GroupID       *int64
	PreviousValue string
	NewValue      string
	Reason        string
}

// Settings are runtime business rules editable by admins.
type Settings struct {
	IgnoredNames      []string `json:"ignored_names"`
	PresentMinPercent int      `json:"present_min_percent"`
	LinkWindowHours   int      `json:"link_window_hours"`
	IgnoreHost        bool     `json:"ignore_host"`
}

// DefaultSettings returns the settings used when nothing is stored yet.
func DefaultSettings() Settings {
	return Settings{
		IgnoredNames:      []string{},
		PresentMinPercent: 0,
		// A session may start up to an hour before the planned start (the
		// meeting opened early); docs/architecture.md §5.5.
		LinkWindowHours: 1,
		IgnoreHost:      true,
	}
}

// Audit action names.
const (
	AuditAttendanceStatus  = "attendance.status"
	AuditAttendanceComment = "attendance.comment"
	AuditParticipantMap    = "participant.map"
	AuditParticipantUnmap  = "participant.unmap"
	AuditLessonCreate      = "lesson.create"
	AuditLessonUpdate      = "lesson.update"
	AuditLessonDelete      = "lesson.delete"
	AuditLessonFinish      = "lesson.finish"
	AuditLessonReopen      = "lesson.reopen"
	AuditSessionLink       = "session.link"
	AuditGroupCreate       = "group.create"
	AuditGroupUpdate       = "group.update"
	AuditGroupImport       = "group.import"
	AuditStudentCreate     = "student.create"
	AuditStudentUpdate     = "student.update"
	AuditAliasAdd          = "alias.add"
	AuditAliasDelete       = "alias.delete"
	AuditSettingsUpdate    = "settings.update"
	AuditUserCreate        = "user.create"
	AuditUserUpdate        = "user.update"
	AuditUserDelete        = "user.delete"
	AuditEventReprocess    = "event.reprocess"
	AuditLDAPTest          = "ldap.test"
)
