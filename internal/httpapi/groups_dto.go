package httpapi

import (
	"time"

	"zoom-checker/internal/importer"
	"zoom-checker/internal/model"
)

// JSON shapes of the group endpoints that are not in dto.go. They mirror
// web/src/lib/types.ts (ImportPreview, ImportResult, GroupReport); fields
// marked "additive" are not in types.ts yet and are ignored by older clients.

// ImportRowDTO is one row of an import preview (types.ts ImportRow).
type ImportRowDTO struct {
	Line           int    `json:"line"` // 0 for "archive" rows (not in the file)
	ExternalID     string `json:"external_id"`
	FullName       string `json:"full_name"`
	NormalizedName string `json:"normalized_name"`
	// Status: new | existing | error | duplicate | archive.
	Status    string `json:"status"`
	Message   string `json:"message"`
	StudentID *int64 `json:"student_id"`
	// Restore (additive): a "new" row that brings back the archived student
	// StudentID instead of creating a second record.
	Restore bool `json:"restore"`
	// Rename (additive): the row changes the stored name of student StudentID.
	Rename bool `json:"rename"`
}

// ImportCountsDTO counts preview rows per status (types.ts ImportCounts).
type ImportCountsDTO struct {
	New       int `json:"new"`
	Existing  int `json:"existing"`
	Error     int `json:"error"`
	Duplicate int `json:"duplicate"`
	Archive   int `json:"archive"`
	Restore   int `json:"restore"` // additive: "new" rows that restore an archived student
	Rename    int `json:"rename"`  // additive: "existing" rows that change a student's name
}

// ImportDetectedDTO tells how the list was read (types.ts ImportDetected).
type ImportDetectedDTO struct {
	Format     string `json:"format"`   // csv | xlsx | text
	Encoding   string `json:"encoding"` // utf-8, windows-1251, ...
	Delimiter  string `json:"delimiter"`
	NameColumn string `json:"name_column"`
}

// ImportPreviewDTO is the answer of POST /api/groups/{id}/import/preview and
// details.preview of a rejected commit (types.ts ImportPreview).
type ImportPreviewDTO struct {
	Mode           importer.Mode       `json:"mode"`
	NameFormat     importer.NameFormat `json:"name_format"`     // additive
	HasPatronymics bool                `json:"has_patronymics"` // additive: offer name_format=last_first
	Rows           []ImportRowDTO      `json:"rows"`
	Counts         ImportCountsDTO     `json:"counts"`
	Detected       ImportDetectedDTO   `json:"detected"`
}

// ImportResultDTO is the answer of POST /api/groups/{id}/import/commit
// (types.ts ImportResult). The counts do not overlap: every row of the list
// is exactly one of created, existing, updated or restored.
type ImportResultDTO struct {
	Created  int `json:"created"`
	Existing int `json:"existing"` // rows that matched a student and changed nothing ("без изменений")
	Updated  int `json:"updated"`  // rows that matched a student whose name or ID changed
	Archived int `json:"archived"` // students missing from a replace list, moved to the archive
	Restored int `json:"restored"` // archived students brought back by the list
}

// GroupReportCellDTO is one student × lesson cell (types.ts GroupReportCell).
type GroupReportCellDTO struct {
	LessonID int64 `json:"lesson_id"`
	// FinalStatus is null when there is no attendance row, or the lesson has
	// not been held (status planned: no Zoom session, so only provisional
	// rows) and no teacher decision was recorded for the student.
	FinalStatus  *model.FinalStatus `json:"final_status"`
	Manual       bool               `json:"manual"`
	TotalSeconds int64              `json:"total_seconds"`
	LateSeconds  *int64             `json:"late_seconds"`
}

// GroupReportLessonDTO is one column of the report (types.ts GroupReportLesson).
type GroupReportLessonDTO struct {
	ID             int64              `json:"id"`
	Title          string             `json:"title"`
	PlannedStartAt time.Time          `json:"planned_start_at"`
	Status         model.LessonStatus `json:"status"`
}

// GroupReportStudentDTO is one row of the report (types.ts GroupReportStudent).
type GroupReportStudentDTO struct {
	StudentID    int64                `json:"student_id"`
	FullName     string               `json:"full_name"`
	ExternalID   string               `json:"external_id"`
	Archived     bool                 `json:"archived"`
	Cells        []GroupReportCellDTO `json:"cells"` // same order as GroupReportDTO.Lessons
	PresentCount int                  `json:"present_count"`
	AbsentCount  int                  `json:"absent_count"`
}

// GroupReportDTO is the attendance matrix of GET /api/groups/{id}/report
// (types.ts GroupReport). From and To are the inclusive bounds that were
// used; null means the period is open on that side (no from / to given).
type GroupReportDTO struct {
	Group    GroupRef                `json:"group"`
	From     *time.Time              `json:"from"`
	To       *time.Time              `json:"to"`
	Lessons  []GroupReportLessonDTO  `json:"lessons"`
	Students []GroupReportStudentDTO `json:"students"`
}
