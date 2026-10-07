package httpapi

import (
	"time"

	"zoom-checker/internal/model"
	"zoom-checker/internal/names"
	"zoom-checker/internal/store"
)

// JSON shapes of the API. They mirror web/src/lib/types.ts field for field
// (docs/architecture.md §6). Change both sides together.

// UserDTO is a user account.
type UserDTO struct {
	ID                 int64      `json:"id"`
	Username           string     `json:"username"`
	DisplayName        string     `json:"display_name"`
	Email              string     `json:"email"`
	Role               model.Role `json:"role"`
	AuthSource         string     `json:"auth_source"`
	MustChangePassword bool       `json:"must_change_password"`
	Disabled           bool       `json:"disabled"`
	LastLoginAt        *time.Time `json:"last_login_at"`
}

func toUserDTO(u *model.User) UserDTO {
	return UserDTO{
		ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Email: u.Email,
		Role: u.Role, AuthSource: string(u.AuthSource), MustChangePassword: u.MustChangePassword,
		Disabled: u.Disabled, LastLoginAt: utcPtr(u.LastLoginAt),
	}
}

// GroupSummaryDTO is a group in lists.
type GroupSummaryDTO struct {
	ID                     int64        `json:"id"`
	Name                   string       `json:"name"`
	Description            string       `json:"description"`
	Status                 model.Status `json:"status"`
	StudentCount           int          `json:"student_count"`
	DefaultDurationMinutes int          `json:"default_duration_minutes"`
	CreatedAt              time.Time    `json:"created_at"`
	LastLessonAt           *time.Time   `json:"last_lesson_at"`
	NextLessonAt           *time.Time   `json:"next_lesson_at"`
}

func toGroupSummaryDTO(g store.GroupSummary) GroupSummaryDTO {
	return GroupSummaryDTO{
		ID: g.ID, Name: g.Name, Description: g.Description, Status: g.Status,
		StudentCount: g.StudentCount, DefaultDurationMinutes: g.DefaultDurationMinutes,
		CreatedAt: g.CreatedAt.UTC(), LastLessonAt: utcPtr(g.LastLessonAt), NextLessonAt: utcPtr(g.NextLessonAt),
	}
}

// AliasDTO is an extra Zoom name of a student.
type AliasDTO struct {
	ID    int64  `json:"id"`
	Alias string `json:"alias"`
}

// StudentDTO is a student with aliases.
type StudentDTO struct {
	ID             int64        `json:"id"`
	GroupID        int64        `json:"group_id"`
	ExternalID     string       `json:"external_id"`
	FullName       string       `json:"full_name"`
	NormalizedName string       `json:"normalized_name"`
	Status         model.Status `json:"status"`
	Aliases        []AliasDTO   `json:"aliases"`
}

func toStudentDTO(st *model.Student) StudentDTO {
	d := StudentDTO{
		ID: st.ID, GroupID: st.GroupID, ExternalID: st.ExternalID, FullName: st.FullName,
		NormalizedName: st.NormalizedName, Status: st.Status, Aliases: make([]AliasDTO, 0, len(st.Aliases)),
	}
	for _, a := range st.Aliases {
		d.Aliases = append(d.Aliases, AliasDTO{ID: a.ID, Alias: a.Alias})
	}
	return d
}

// GroupDetailDTO is a group with its students.
type GroupDetailDTO struct {
	GroupSummaryDTO
	Students []StudentDTO `json:"students"`
}

// GroupRef is a short reference to a group.
type GroupRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// ZoomSessionDTO is one actual Zoom meeting instance.
type ZoomSessionDTO struct {
	ID           int64          `json:"id"`
	MeetingUUID  string         `json:"meeting_uuid"`
	MeetingID    string         `json:"meeting_id"`
	Topic        string         `json:"topic"`
	LessonID     *int64         `json:"lesson_id"`
	LinkMode     model.LinkMode `json:"link_mode"`
	StartedAt    *time.Time     `json:"started_at"`
	EndedAt      *time.Time     `json:"ended_at"`
	FirstEventAt time.Time      `json:"first_event_at"`
	// Lesson summarizes the linked lesson (null when unlinked), so session
	// lists need no request per row.
	Lesson *ZoomSessionLessonDTO `json:"lesson"`
}

// ZoomSessionLessonDTO is the short form of the lesson a session is linked to.
type ZoomSessionLessonDTO struct {
	ID             int64     `json:"id"`
	Title          string    `json:"title"`
	GroupName      string    `json:"group_name"`
	PlannedStartAt time.Time `json:"planned_start_at"`
}

// toZoomSessionDTO converts a session; lesson is its linked lesson's summary
// (nil when unlinked or unknown).
func toZoomSessionDTO(m *model.ZoomMeeting, lesson *ZoomSessionLessonDTO) ZoomSessionDTO {
	d := ZoomSessionDTO{
		ID: m.ID, MeetingUUID: m.MeetingUUID, MeetingID: m.MeetingID, Topic: m.Topic,
		LessonID: m.LessonID, LinkMode: m.LinkMode, StartedAt: utcPtr(m.StartedAt),
		EndedAt: utcPtr(m.EndedAt), FirstEventAt: m.FirstEventAt.UTC(),
	}
	if m.LessonID != nil && lesson != nil && lesson.ID == *m.LessonID {
		d.Lesson = lesson
	}
	return d
}

func toZoomSessionLessonDTO(r store.LessonRef) *ZoomSessionLessonDTO {
	return &ZoomSessionLessonDTO{ID: r.ID, Title: r.Title, GroupName: r.GroupName, PlannedStartAt: r.PlannedStartAt.UTC()}
}

// LessonDTO is a lesson with its Zoom sessions (GET /api/lessons/{id}).
type LessonDTO struct {
	ID                     int64              `json:"id"`
	Group                  GroupRef           `json:"group"`
	Title                  string             `json:"title"`
	ZoomMeetingID          string             `json:"zoom_meeting_id"`
	ZoomMeetingIDFormatted string             `json:"zoom_meeting_id_formatted"`
	ZoomJoinURL            string             `json:"zoom_join_url"`
	PlannedStartAt         time.Time          `json:"planned_start_at"`
	PlannedEndAt           time.Time          `json:"planned_end_at"`
	ActualStartAt          *time.Time         `json:"actual_start_at"`
	ActualEndAt            *time.Time         `json:"actual_end_at"`
	Status                 model.LessonStatus `json:"status"`
	ManualFinishedAt       *time.Time         `json:"manual_finished_at"`
	Sessions               []ZoomSessionDTO   `json:"sessions"`
}

func toLessonDTO(l *model.Lesson, groupName string, sessions []model.ZoomMeeting) LessonDTO {
	d := LessonDTO{
		ID: l.ID, Group: GroupRef{ID: l.GroupID, Name: groupName}, Title: l.Title,
		ZoomMeetingID: l.ZoomMeetingID, ZoomMeetingIDFormatted: names.FormatMeetingID(l.ZoomMeetingID),
		ZoomJoinURL: l.ZoomJoinURL, PlannedStartAt: l.PlannedStartAt.UTC(), PlannedEndAt: l.PlannedEndAt.UTC(),
		ActualStartAt: utcPtr(l.ActualStartAt), ActualEndAt: utcPtr(l.ActualEndAt), Status: l.Status,
		ManualFinishedAt: utcPtr(l.ManualFinishedAt), Sessions: make([]ZoomSessionDTO, 0, len(sessions)),
	}
	self := &ZoomSessionLessonDTO{ID: l.ID, Title: l.Title, GroupName: groupName, PlannedStartAt: l.PlannedStartAt.UTC()}
	for i := range sessions {
		d.Sessions = append(d.Sessions, toZoomSessionDTO(&sessions[i], self))
	}
	return d
}

// CountsDTO are the stat tiles of a lesson.
type CountsDTO struct {
	InGroup      int `json:"in_group"`
	Online       int `json:"online"`
	Seen         int `json:"seen"`
	NotSeen      int `json:"not_seen"`
	Unidentified int `json:"unidentified"`
	Present      int `json:"present"`
	Absent       int `json:"absent"`
}

func toCountsDTO(c store.Counts) CountsDTO {
	return CountsDTO{InGroup: c.InGroup, Online: c.Online, Seen: c.Seen, NotSeen: c.NotSeen,
		Unidentified: c.Unidentified, Present: c.Present, Absent: c.Absent}
}

// LessonSummaryDTO is a lesson in lists (no sessions, with counts).
type LessonSummaryDTO struct {
	ID                     int64              `json:"id"`
	Group                  GroupRef           `json:"group"`
	Title                  string             `json:"title"`
	ZoomMeetingID          string             `json:"zoom_meeting_id"`
	ZoomMeetingIDFormatted string             `json:"zoom_meeting_id_formatted"`
	ZoomJoinURL            string             `json:"zoom_join_url"`
	PlannedStartAt         time.Time          `json:"planned_start_at"`
	PlannedEndAt           time.Time          `json:"planned_end_at"`
	ActualStartAt          *time.Time         `json:"actual_start_at"`
	ActualEndAt            *time.Time         `json:"actual_end_at"`
	Status                 model.LessonStatus `json:"status"`
	ManualFinishedAt       *time.Time         `json:"manual_finished_at"`
	Counts                 CountsDTO          `json:"counts"`
}

func toLessonSummaryDTO(l *model.Lesson, groupName string, c store.Counts) LessonSummaryDTO {
	d := toLessonDTO(l, groupName, nil)
	return LessonSummaryDTO{
		ID: d.ID, Group: d.Group, Title: d.Title, ZoomMeetingID: d.ZoomMeetingID,
		ZoomMeetingIDFormatted: d.ZoomMeetingIDFormatted, ZoomJoinURL: d.ZoomJoinURL,
		PlannedStartAt: d.PlannedStartAt, PlannedEndAt: d.PlannedEndAt, ActualStartAt: d.ActualStartAt,
		ActualEndAt: d.ActualEndAt, Status: d.Status, ManualFinishedAt: d.ManualFinishedAt, Counts: toCountsDTO(c),
	}
}

// StudentRowDTO is one student on the live lesson screen.
type StudentRowDTO struct {
	StudentID         int64               `json:"student_id"`
	FullName          string              `json:"full_name"`
	ExternalID        string              `json:"external_id"`
	CurrentStatus     model.CurrentStatus `json:"current_status"`
	TotalSeconds      int64               `json:"total_seconds"` // as of LessonViewDTO.AsOf
	FirstJoinAt       *time.Time          `json:"first_join_at"`
	LastLeaveAt       *time.Time          `json:"last_leave_at"`
	LateSeconds       *int64              `json:"late_seconds"`
	EarlyLeaveSeconds *int64              `json:"early_leave_seconds"`
	AutoStatus        model.FinalStatus   `json:"auto_status"`
	ManualStatus      *model.FinalStatus  `json:"manual_status"`
	FinalStatus       model.FinalStatus   `json:"final_status"`
	Comment           string              `json:"comment"`
	ZoomNames         []string            `json:"zoom_names"`
	DevicesOnline     int                 `json:"devices_online"`
	Archived          bool                `json:"archived"`
}

// ParticipantDTO is one Zoom display name of a lesson.
type ParticipantDTO struct {
	ID                  int64               `json:"id"`
	DisplayName         string              `json:"display_name"`
	NormalizedName      string              `json:"normalized_name"`
	Resolution          model.Resolution    `json:"resolution"`
	IgnoreReason        model.IgnoreReason  `json:"ignore_reason"`
	StudentID           *int64              `json:"student_id"`
	StudentName         *string             `json:"student_name"`
	CandidateStudentIDs []int64             `json:"candidate_student_ids"`
	CurrentStatus       model.CurrentStatus `json:"current_status"`
	FirstJoinAt         *time.Time          `json:"first_join_at"`
	LastLeaveAt         *time.Time          `json:"last_leave_at"`
	TotalSeconds        int64               `json:"total_seconds"` // as of LessonViewDTO.AsOf
	Devices             int                 `json:"devices"`
	Manual              bool                `json:"manual"`
}

// LessonViewDTO is the live screen payload (GET /api/lessons/{id}/attendance and SSE snapshots).
// AsOf is the server time the view was built; totals of online rows include open
// intervals up to AsOf and clients add (now − receive time) while a row is online.
type LessonViewDTO struct {
	Lesson       LessonDTO        `json:"lesson"`
	Counts       CountsDTO        `json:"counts"`
	Students     []StudentRowDTO  `json:"students"`
	Participants []ParticipantDTO `json:"participants"`
	AsOf         time.Time        `json:"as_of"`
}

// IntervalDTO is one presence interval of one device.
type IntervalDTO struct {
	ID                  int64      `json:"id"`
	JoinAt              time.Time  `json:"join_at"`
	LeaveAt             *time.Time `json:"leave_at"`
	DurationSeconds     *int64     `json:"duration_seconds"`
	ZoomUserName        string     `json:"zoom_user_name"`
	ZoomParticipantUUID string     `json:"zoom_participant_uuid"`
	MeetingUUID         string     `json:"meeting_uuid"`
	LeaveReason         string     `json:"leave_reason"`
}

func toIntervalDTO(iv *model.Interval) IntervalDTO {
	return IntervalDTO{
		ID: iv.ID, JoinAt: iv.JoinAt.UTC(), LeaveAt: utcPtr(iv.LeaveAt), DurationSeconds: iv.DurationSeconds,
		ZoomUserName: iv.ZoomUserName, ZoomParticipantUUID: iv.ZoomParticipantUUID,
		MeetingUUID: iv.MeetingUUID, LeaveReason: iv.LeaveReason,
	}
}

// AuditDTO is one audit log entry.
type AuditDTO struct {
	ID            int64     `json:"id"`
	At            time.Time `json:"at"`
	UserName      string    `json:"user_name"`
	Action        string    `json:"action"`
	EntityType    string    `json:"entity_type"`
	EntityID      string    `json:"entity_id"`
	PreviousValue string    `json:"previous_value"`
	NewValue      string    `json:"new_value"`
	Reason        string    `json:"reason"`
}

func toAuditDTO(e *model.AuditEntry) AuditDTO {
	return AuditDTO{
		ID: e.ID, At: e.At.UTC(), UserName: e.UserName, Action: e.Action, EntityType: e.EntityType,
		EntityID: e.EntityID, PreviousValue: e.PreviousValue, NewValue: e.NewValue, Reason: e.Reason,
	}
}

// WebhookEventSummaryDTO is a raw webhook in the admin journal.
type WebhookEventSummaryDTO struct {
	ID               int64             `json:"id"`
	EventType        string            `json:"event_type"`
	MeetingID        string            `json:"meeting_id"`
	MeetingUUID      string            `json:"meeting_uuid"`
	ParticipantName  string            `json:"participant_name"`
	OccurredAt       *time.Time        `json:"occurred_at"`
	ReceivedAt       time.Time         `json:"received_at"`
	ProcessedAt      *time.Time        `json:"processed_at"`
	ProcessingStatus model.EventStatus `json:"processing_status"`
	Attempts         int               `json:"attempts"`
	Error            string            `json:"error"`
}

// WebhookEventDetailDTO adds the raw payload.
type WebhookEventDetailDTO struct {
	WebhookEventSummaryDTO
	EventTS       int64      `json:"event_ts"`
	DedupeKey     string     `json:"dedupe_key"`
	NextAttemptAt *time.Time `json:"next_attempt_at"`
	Payload       string     `json:"payload"`
}

func toWebhookEventSummaryDTO(e *model.WebhookEvent) WebhookEventSummaryDTO {
	return WebhookEventSummaryDTO{
		ID: e.ID, EventType: e.EventType, MeetingID: e.MeetingID, MeetingUUID: e.MeetingUUID,
		ParticipantName: e.ParticipantName, OccurredAt: utcPtr(e.OccurredAt), ReceivedAt: e.ReceivedAt.UTC(),
		ProcessedAt: utcPtr(e.ProcessedAt), ProcessingStatus: e.ProcessingStatus, Attempts: e.Attempts, Error: e.Error,
	}
}

func toWebhookEventDetailDTO(e *model.WebhookEvent) WebhookEventDetailDTO {
	return WebhookEventDetailDTO{
		WebhookEventSummaryDTO: toWebhookEventSummaryDTO(e),
		EventTS:                e.EventTS, DedupeKey: e.DedupeKey, NextAttemptAt: utcPtr(e.NextAttemptAt), Payload: e.Payload,
	}
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
