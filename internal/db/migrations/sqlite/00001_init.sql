-- +goose Up
-- All *_at columns are unix milliseconds UTC.

CREATE TABLE users (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    username             TEXT    NOT NULL,
    display_name         TEXT    NOT NULL DEFAULT '',
    email                TEXT    NOT NULL DEFAULT '',
    role                 TEXT    NOT NULL CHECK (role IN ('admin', 'teacher')),
    auth_source          TEXT    NOT NULL CHECK (auth_source IN ('local', 'oidc', 'ldap')),
    external_id          TEXT    NOT NULL DEFAULT '',
    password_hash        TEXT    NOT NULL DEFAULT '',
    must_change_password INTEGER NOT NULL DEFAULT 0,
    disabled             INTEGER NOT NULL DEFAULT 0,
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL,
    last_login_at        INTEGER
);
CREATE UNIQUE INDEX users_source_username ON users (auth_source, username);
CREATE UNIQUE INDEX users_source_external ON users (auth_source, external_id) WHERE external_id <> '';

CREATE TABLE sessions (
    id_hash       TEXT    PRIMARY KEY,
    user_id       INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    last_seen_at  INTEGER NOT NULL,
    ip            TEXT    NOT NULL DEFAULT '',
    user_agent    TEXT    NOT NULL DEFAULT '',
    auth_source   TEXT    NOT NULL,
    oidc_id_token TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user ON sessions (user_id);
CREATE INDEX sessions_expires ON sessions (expires_at);

CREATE TABLE student_groups (
    id                       INTEGER PRIMARY KEY AUTOINCREMENT,
    name                     TEXT    NOT NULL,
    normalized_name          TEXT    NOT NULL,
    description              TEXT    NOT NULL DEFAULT '',
    status                   TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    default_duration_minutes INTEGER NOT NULL DEFAULT 90,
    created_at               INTEGER NOT NULL,
    updated_at               INTEGER NOT NULL
);
CREATE UNIQUE INDEX student_groups_name ON student_groups (normalized_name);

CREATE TABLE students (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    group_id        INTEGER NOT NULL REFERENCES student_groups (id),
    external_id     TEXT    NOT NULL DEFAULT '',
    full_name       TEXT    NOT NULL,
    normalized_name TEXT    NOT NULL,
    status          TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);
CREATE INDEX students_group ON students (group_id);
CREATE UNIQUE INDEX students_group_name_active ON students (group_id, normalized_name) WHERE status = 'active';

CREATE TABLE student_aliases (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    student_id       INTEGER NOT NULL REFERENCES students (id) ON DELETE CASCADE,
    group_id         INTEGER NOT NULL,
    alias            TEXT    NOT NULL,
    normalized_alias TEXT    NOT NULL,
    created_by       INTEGER,
    created_at       INTEGER NOT NULL
);
CREATE UNIQUE INDEX student_aliases_group_alias ON student_aliases (group_id, normalized_alias);
CREATE INDEX student_aliases_student ON student_aliases (student_id);

CREATE TABLE lessons (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    group_id           INTEGER NOT NULL REFERENCES student_groups (id),
    title              TEXT    NOT NULL,
    zoom_meeting_id    TEXT    NOT NULL,
    zoom_join_url      TEXT    NOT NULL DEFAULT '',
    planned_start_at   INTEGER NOT NULL,
    planned_end_at     INTEGER NOT NULL,
    actual_start_at    INTEGER,
    actual_end_at      INTEGER,
    status             TEXT    NOT NULL DEFAULT 'planned' CHECK (status IN ('planned', 'active', 'finished')),
    manual_finished_at INTEGER,
    created_by         INTEGER,
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
);
CREATE INDEX lessons_meeting ON lessons (zoom_meeting_id, planned_start_at);
CREATE INDEX lessons_group ON lessons (group_id, planned_start_at);
CREATE INDEX lessons_planned_start ON lessons (planned_start_at);

CREATE TABLE zoom_meetings (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    meeting_uuid   TEXT    NOT NULL UNIQUE,
    meeting_id     TEXT    NOT NULL,
    topic          TEXT    NOT NULL DEFAULT '',
    host_id        TEXT    NOT NULL DEFAULT '',
    lesson_id      INTEGER REFERENCES lessons (id) ON DELETE SET NULL,
    link_mode      TEXT    NOT NULL DEFAULT 'auto' CHECK (link_mode IN ('auto', 'manual')),
    started_at     INTEGER,
    ended_at       INTEGER,
    first_event_at INTEGER NOT NULL,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);
CREATE INDEX zoom_meetings_lesson ON zoom_meetings (lesson_id);
CREATE INDEX zoom_meetings_meeting_id ON zoom_meetings (meeting_id, first_event_at);

CREATE TABLE zoom_webhook_events (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    event_type        TEXT    NOT NULL,
    event_ts          INTEGER NOT NULL DEFAULT 0,
    meeting_id        TEXT    NOT NULL DEFAULT '',
    meeting_uuid      TEXT    NOT NULL DEFAULT '',
    participant_name  TEXT    NOT NULL DEFAULT '',
    occurred_at       INTEGER,
    dedupe_key        TEXT    NOT NULL,
    payload           TEXT    NOT NULL,
    received_at       INTEGER NOT NULL,
    processed_at      INTEGER,
    processing_status TEXT    NOT NULL DEFAULT 'pending'
        CHECK (processing_status IN ('pending', 'processed', 'duplicate', 'ignored', 'failed')),
    attempts          INTEGER NOT NULL DEFAULT 0,
    next_attempt_at   INTEGER,
    error             TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX zwe_status ON zoom_webhook_events (processing_status, id);
CREATE INDEX zwe_dedupe ON zoom_webhook_events (dedupe_key);
CREATE INDEX zwe_meeting_uuid ON zoom_webhook_events (meeting_uuid, id);
CREATE INDEX zwe_received ON zoom_webhook_events (received_at);

CREATE TABLE zoom_participants (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    lesson_id             INTEGER NOT NULL REFERENCES lessons (id) ON DELETE CASCADE,
    normalized_name       TEXT    NOT NULL,
    display_name          TEXT    NOT NULL,
    resolution            TEXT    NOT NULL
        CHECK (resolution IN ('matched', 'manual', 'unidentified', 'conflict', 'ignored')),
    student_id            INTEGER,
    ignore_reason         TEXT    NOT NULL DEFAULT '',
    candidate_student_ids TEXT    NOT NULL DEFAULT '',
    current_status        TEXT    NOT NULL CHECK (current_status IN ('online', 'left')),
    first_join_at         INTEGER,
    last_leave_at         INTEGER,
    total_seconds         INTEGER NOT NULL DEFAULT 0,
    devices               INTEGER NOT NULL DEFAULT 0,
    computed_at           INTEGER NOT NULL,
    updated_at            INTEGER NOT NULL
);
CREATE UNIQUE INDEX zoom_participants_lesson_name ON zoom_participants (lesson_id, normalized_name);

CREATE TABLE attendance (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    lesson_id              INTEGER NOT NULL REFERENCES lessons (id) ON DELETE CASCADE,
    student_id             INTEGER NOT NULL REFERENCES students (id),
    first_join_at          INTEGER,
    last_leave_at          INTEGER,
    total_duration_seconds INTEGER NOT NULL DEFAULT 0,
    late_seconds           INTEGER,
    early_leave_seconds    INTEGER,
    current_status         TEXT    NOT NULL CHECK (current_status IN ('online', 'left', 'not_seen')),
    auto_status            TEXT    NOT NULL CHECK (auto_status IN ('present', 'absent')),
    manual_status          TEXT    CHECK (manual_status IN ('present', 'absent')),
    final_status           TEXT    NOT NULL CHECK (final_status IN ('present', 'absent')),
    comment                TEXT    NOT NULL DEFAULT '',
    manual_by              INTEGER,
    manual_at              INTEGER,
    computed_at            INTEGER NOT NULL,
    created_at             INTEGER NOT NULL,
    updated_at             INTEGER NOT NULL
);
CREATE UNIQUE INDEX attendance_lesson_student ON attendance (lesson_id, student_id);
CREATE INDEX attendance_student ON attendance (student_id);

CREATE TABLE attendance_intervals (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    lesson_id             INTEGER NOT NULL REFERENCES lessons (id) ON DELETE CASCADE,
    zoom_participant_id   INTEGER REFERENCES zoom_participants (id) ON DELETE CASCADE,
    attendance_id         INTEGER REFERENCES attendance (id) ON DELETE SET NULL,
    meeting_uuid          TEXT    NOT NULL,
    zoom_participant_uuid TEXT    NOT NULL DEFAULT '',
    zoom_user_name        TEXT    NOT NULL,
    join_at               INTEGER NOT NULL,
    leave_at              INTEGER,
    duration_seconds      INTEGER,
    leave_reason          TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX attendance_intervals_lesson ON attendance_intervals (lesson_id);
CREATE INDEX attendance_intervals_attendance ON attendance_intervals (attendance_id);

CREATE TABLE participant_mappings (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    lesson_id       INTEGER NOT NULL REFERENCES lessons (id) ON DELETE CASCADE,
    normalized_name TEXT    NOT NULL,
    action          TEXT    NOT NULL CHECK (action IN ('student', 'ignore', 'none')),
    student_id      INTEGER,
    created_by      INTEGER,
    created_at      INTEGER NOT NULL
);
CREATE UNIQUE INDEX participant_mappings_lesson_name ON participant_mappings (lesson_id, normalized_name);

CREATE TABLE audit_log (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    at             INTEGER NOT NULL,
    user_id        INTEGER,
    user_name      TEXT    NOT NULL DEFAULT '',
    action         TEXT    NOT NULL,
    entity_type    TEXT    NOT NULL,
    entity_id      TEXT    NOT NULL DEFAULT '',
    lesson_id      INTEGER,
    group_id       INTEGER,
    previous_value TEXT    NOT NULL DEFAULT '',
    new_value      TEXT    NOT NULL DEFAULT '',
    reason         TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX audit_log_lesson ON audit_log (lesson_id, at);
CREATE INDEX audit_log_at ON audit_log (at);

CREATE TABLE settings (
    key        TEXT    PRIMARY KEY,
    value      TEXT    NOT NULL,
    updated_at INTEGER NOT NULL,
    updated_by INTEGER
);

-- +goose Down
DROP TABLE settings;
DROP TABLE audit_log;
DROP TABLE participant_mappings;
DROP TABLE attendance_intervals;
DROP TABLE attendance;
DROP TABLE zoom_participants;
DROP TABLE zoom_webhook_events;
DROP TABLE zoom_meetings;
DROP TABLE lessons;
DROP TABLE student_aliases;
DROP TABLE students;
DROP TABLE student_groups;
DROP TABLE sessions;
DROP TABLE users;
