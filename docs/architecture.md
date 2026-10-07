# Zoom Checker — architecture & implementation contract

This document is the binding contract for everyone implementing the service.
Source requirements: the original Russian specification (the TZ; not published in this
repository — "TZ §N" references point to its sections). Where this document is more
specific than the TZ, this document wins; where it deviates, the deviation is listed in
§13.

## 0. Goals that drive every decision

1. **Easy to install** — one static binary (or one small Docker image), zero mandatory
   external services. SQLite by default, PostgreSQL optional. Built-in HTTPS (ACME).
   One `install.sh` for a bare Linux server.
2. **Lightweight** — Go, ~20–40 MB RSS, no runtime dependencies, no Redis/queues.
3. **Corporate auth** — local accounts + Keycloak (OIDC) + LDAP / Active Directory,
   any combination enabled at once.
4. **Usable by non-technical staff** — Russian UI, plain words, big status badges,
   guided import, copy-paste friendly inputs (Zoom link *or* full invitation text, list of
   names pasted from Excel), no jargon.

## 1. Stack

| Layer | Choice | Why |
|---|---|---|
| Language | Go 1.27 (`go 1.26` minimum in go.mod) | single static binary, tiny RAM, stdlib HTTP router & CSRF protection |
| HTTP | `net/http` ServeMux (method+path patterns), `http.CrossOriginProtection` | no framework needed |
| DB | SQLite via `modernc.org/sqlite` (pure Go, CGO off) **or** PostgreSQL via `pgx/v5/stdlib` | zero-config default, enterprise option |
| Migrations | `pressly/goose/v3` Provider, embedded SQL, one dir per dialect | |
| OIDC | `coreos/go-oidc/v3` + `golang.org/x/oauth2` (Auth Code + PKCE S256 + nonce) | |
| LDAP | `go-ldap/ldap/v3` | |
| XLSX | `xuri/excelize/v2` | import & export |
| TLS | `caddyserver/certmagic` (optional, `TLS_MODE=auto`) | built-in Let's Encrypt |
| Logging | `log/slog` | |
| Frontend | React 19 + TypeScript + Vite + Tailwind CSS v4 + shadcn/ui + TanStack Query + React Router | embedded into the binary via `embed.FS` |
| Realtime | Server-Sent Events | one-way, proxy friendly, auto-reconnect |
| Tests | `go test`, Playwright e2e | |

## 2. Repository layout

```
cmd/zoom-checker/            main.go — CLI: serve (default), migrate, user, check-ldap, check-oidc, simulate, backup, version
internal/version/            Version/Commit (ldflags)
internal/config/             env loading + validation (Config struct)
internal/names/              Normalize(), ParseMeetingID(), FormatMeetingID()
internal/timefmt/            user-facing time strings with the UTC offset ("18:00 (UTC+3)"), LoadZone() for `tz`
internal/model/              shared domain structs & enums (NO logic, NO deps)
internal/db/                 Open(), dialect, Rebind(), migrations (embedded)
internal/db/migrations/{sqlite,postgres}/*.sql
internal/store/              all SQL; one Store type; tx support
internal/zoom/               webhook signature, url_validation, payload parsing, dedupe key
internal/attendance/         PURE engine: events + roster + mappings -> projection
internal/processor/          async worker: raw event -> session link -> rebuild projection -> publish
internal/realtime/           in-process pub/sub broker for SSE
internal/auth/               passwords, sessions, local, LDAP, OIDC, role mapping, middleware, rate limit
internal/importer/           CSV/XLSX/plain-text student list parsing + validation
internal/export/             CSV/XLSX export of lesson attendance and group report
internal/httpapi/            router, handlers (routes_*.go), SSE, SPA serving, middleware; Server.Close cancels and
                             waits for rebuilds that outlive a request (called by internal/server before the DB closes)
internal/server/             wiring + TLS (certmagic) + graceful shutdown + background jobs (backup, retention)
web/                         React app (Vite). web/embed.go embeds web/dist
deploy/                      install.sh, zoom-checker.service, docker-compose.yml, .env.example, dev stands (keycloak, ldap)
docs/                        Russian user/admin docs + this file
```

## 3. Configuration (environment variables)

All configuration is env vars (12-factor). `zoom-checker --config /etc/zoom-checker/zoom-checker.env`
(or `CONFIG_FILE`) loads a dotenv-style file first; real env vars override it. File syntax (`config.LoadEnvFile`,
mirrored by `get_env` in `deploy/install.sh`): `#` comment lines, optional `export `, the **last** line of a key
wins, `"…"` (escapes `\n \" \\`) or `'…'` quotes, an unquoted ` # comment` tail is dropped, and so is one after
a closing quote (`KEY="value"  # comment` → `value`); a malformed line sets nothing.
Secrets are never logged (config has `Redacted()` for diagnostics).

| Var | Default | Meaning |
|---|---|---|
| `PUBLIC_URL` | `http://localhost:8080` | external base URL; used for webhook URL display, OIDC redirect, secure cookies |
| `LISTEN_ADDR` | `:8080` | HTTP listen address (ignored when `TLS_MODE=auto`) |
| `TLS_MODE` | `off` | `off` (plain HTTP, e.g. behind proxy) · `auto` (ACME via certmagic on :443 + :80 redirect/challenge) · `file` (`TLS_CERT_FILE`/`TLS_KEY_FILE`, listens on `LISTEN_ADDR`) |
| `TLS_EMAIL` | empty | ACME account email (optional) |
| `TLS_ACME_CA` | `production` | `production` · `staging` · ACME directory URL |
| `TLS_ACME_PROFILE` | empty | empty = auto: `shortlived` when a managed host is an IP address (Let's Encrypt issues IP certs only with that profile, 160 h, renewed automatically via ARI), CA default otherwise |
| `TLS_EXTRA_HOSTS` | empty | extra comma-separated hostnames/IPs to manage certificates for |
| `TLS_CERT_FILE`, `TLS_KEY_FILE` | | for `TLS_MODE=file` |
| `DATA_DIR` | `./data` | SQLite DB, ACME certs, backups, initial admin password file |
| `DATABASE_URL` | `sqlite://$DATA_DIR/zoom-checker.db` | or `postgres://user:pass@host:5432/db?sslmode=...` |
| `TIMEZONE` | `Europe/Moscow` | used for exports (unless the browser passes its own zone as `tz`, §6), server-side date strings (always with the UTC offset, e.g. `06.10.2026 18:00 (UTC+3)`), "today" |
| `ZOOM_WEBHOOK_SECRET_TOKEN` | empty | Zoom app Secret Token. Empty → webhook endpoint returns 503 and UI shows "not configured" |
| `ZOOM_WEBHOOK_MAX_AGE` | `2h` | reject signed requests whose `x-zm-request-timestamp` is older than this (or more than 24 h in the future: a request from the "future" only means the server clock is behind). Must cover Zoom's retry schedule (+5, +25, +85 min); a 4xx stops retries forever. Replays inside the window are harmless (idempotent processing) |
| `ADMIN_USERNAME` | `admin` | bootstrap local admin username |
| `ADMIN_PASSWORD` | empty | if set and no users exist → create admin with it (`must_change_password=1` when it fails the password policy, e.g. shorter than 8 characters; docs tell admins to change it after the first login and drop the line). If empty and no users exist → generate random password, write to `$DATA_DIR/initial-admin-password.txt` (0600) and log the file path (never the password) |
| `SESSION_TTL` | `168h` | **absolute** lifetime of a local account's session, counted from the login (activity never extends it; the cookie `Max-Age` is the same) |
| `EXTERNAL_SESSION_TTL` | `24h` | absolute lifetime of an LDAP/OIDC session (database row and cookie): afterwards the user logs in again, which re-syncs role and disabled state from the directory/IdP. Disabling a user in Админ → Пользователи ends their sessions at once |
| `TRUSTED_PROXIES` | empty | comma-separated CIDRs whose `X-Forwarded-For`/`X-Forwarded-Proto` are trusted |
| `LOG_LEVEL` | `info` | debug/info/warn/error |
| `LOG_FORMAT` | `text` | text/json |
| `BACKUP_KEEP` | `14` | SQLite only: daily `VACUUM INTO $DATA_DIR/backups/zoom-checker-YYYY-MM-DD.db`, keep N (0 = disabled) |
| `EVENTS_RETENTION_DAYS` | `365` | daily (03:30 `TIMEZONE`) delete raw webhook events received more than N days ago that nothing needs any more (0 = keep forever; 1–89 are raised to 90 with a warning). Events of Zoom sessions **linked to a lesson** are never deleted — they are that lesson's source of truth (every rebuild recomputes its attendance from them) — and neither are `pending` events (§5.9) |
| `AUTH_LOCAL_ENABLED` | `true` | local username/password accounts |
| `OIDC_ISSUER_URL` | empty | e.g. `https://sso.example.com/realms/edu`. Empty → OIDC disabled |
| `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET` | | confidential client |
| `OIDC_SCOPES` | `openid profile email` | |
| `OIDC_BUTTON_LABEL` | `Войти через Keycloak` | login button text |
| `OIDC_ADMIN_ROLES` | empty | comma list; matched against realm roles, client roles (`resource_access.<client_id>.roles`), `groups` (with or without leading `/`), `roles` claim, and `OIDC_ROLES_CLAIM`. A rule without `/` also matches the last segment of a group path (`teachers` ↔ `/staff/teachers`), a rule with `/` only that full path. Keycloak sends `groups` only with a Group Membership mapper (admin guide §8); group-based admin rules should be full paths |
| `OIDC_TEACHER_ROLES` | empty | comma list; `*` = any authenticated user. **Empty = nobody** (secure default; realms often contain students) |
| `OIDC_ROLES_CLAIM` | empty | extra dot-path claim with roles, e.g. `attendance.roles` |
| `OIDC_CA_FILE` | empty | extra CA bundle for the IdP |
| `LDAP_URL` | empty | `ldaps://dc1.corp.local:636` or `ldap://...:389`. Empty → LDAP disabled |
| `LDAP_START_TLS` | `false` | StartTLS on ldap:// |
| `LDAP_CA_FILE` | empty | CA bundle |
| `LDAP_INSECURE_SKIP_VERIFY` | `false` | testing only; logged as warning |
| `LDAP_PRESET` | `ad` | `ad` · `openldap` · `freeipa` — sets defaults for the vars below |
| `LDAP_BIND_DN`, `LDAP_BIND_PASSWORD` | | service account (search). If empty with `ad` preset, direct bind as `username@LDAP_DOMAIN` is used |
| `LDAP_DOMAIN` | empty | AD UPN suffix for direct bind / `DOMAIN\user` stripping |
| `LDAP_BASE_DN` | | search base |
| `LDAP_USER_FILTER` | preset | `{username}` placeholder (escaped with `ldap.EscapeFilter`) |
| `LDAP_DISPLAY_NAME_ATTR`, `LDAP_EMAIL_ATTR`, `LDAP_ID_ATTR` | preset | AD: `displayName`,`mail`,`objectGUID`; OpenLDAP: `cn`,`mail`,`entryUUID`; FreeIPA: `displayName`,`mail`,`ipaUniqueID` |
| `LDAP_ADMIN_GROUPS`, `LDAP_TEACHER_GROUPS` | empty | **semicolon**-separated group DNs or bare CNs (case-insensitive). A bare CN matches a group of that CN in **any** OU (memberOf is domain-wide), so docs and examples give admin groups as full DNs; teacher groups may stay bare. `*` in teacher groups = any user that can bind. Empty teacher groups = nobody |
| `LDAP_NESTED_GROUPS` | `true` for `ad` | AD: resolve nested membership via `LDAP_MATCHING_RULE_IN_CHAIN` |
| `LDAP_TIMEOUT` | `10s` | dial/op timeout |

Runtime business settings are stored in DB (`settings` table) and editable by admins in UI
(§7): ignored names list, present-min-percent rule, link window, ignore-host flag.

## 4. Data model

All timestamps are stored as **INTEGER/BIGINT unix milliseconds UTC** (portable between
SQLite and PostgreSQL, unambiguous). Columns named `*_at`. Go code uses `time.Time` (UTC)
and `*time.Time` for nullable; conversion lives only in `internal/store`.
Raw webhook bodies are stored as TEXT byte-for-byte (not JSONB, which would reformat).
Booleans are INTEGER 0/1 in SQLite and BOOLEAN in PostgreSQL (store maps them).

Tables (SQLite DDL is authoritative; PostgreSQL mirrors it with `BIGINT GENERATED BY DEFAULT AS IDENTITY`, `BIGINT` times, `BOOLEAN`):

```sql
users(id PK, username TEXT NOT NULL, display_name TEXT NOT NULL DEFAULT '', email TEXT NOT NULL DEFAULT '',
      role TEXT NOT NULL CHECK(role IN ('admin','teacher')),
      auth_source TEXT NOT NULL CHECK(auth_source IN ('local','oidc','ldap')),
      external_id TEXT NOT NULL DEFAULT '', password_hash TEXT NOT NULL DEFAULT '',
      must_change_password INTEGER NOT NULL DEFAULT 0, disabled INTEGER NOT NULL DEFAULT 0,
      created_at, updated_at INTEGER NOT NULL, last_login_at INTEGER)
  UNIQUE(auth_source, username); UNIQUE(auth_source, external_id) WHERE external_id <> ''

sessions(id_hash TEXT PK /* hex sha256 of cookie token */, user_id FK users ON DELETE CASCADE,
         created_at, expires_at, last_seen_at INTEGER NOT NULL, ip TEXT, user_agent TEXT,
         auth_source TEXT NOT NULL, oidc_id_token TEXT NOT NULL DEFAULT '')

student_groups(id PK, name TEXT NOT NULL, normalized_name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
               status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','archived')),
               default_duration_minutes INTEGER NOT NULL DEFAULT 90,
               created_at, updated_at INTEGER NOT NULL)
  UNIQUE(normalized_name)

students(id PK, group_id FK student_groups, external_id TEXT NOT NULL DEFAULT '',
         full_name TEXT NOT NULL, normalized_name TEXT NOT NULL,
         status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','archived')),
         created_at, updated_at INTEGER NOT NULL)
  UNIQUE(group_id, normalized_name) WHERE status='active'

student_aliases(id PK, student_id FK students ON DELETE CASCADE, group_id INTEGER NOT NULL,
                alias TEXT NOT NULL, normalized_alias TEXT NOT NULL, created_by INTEGER, created_at INTEGER NOT NULL)
  UNIQUE(group_id, normalized_alias)

lessons(id PK, group_id FK student_groups, title TEXT NOT NULL,
        zoom_meeting_id TEXT NOT NULL /* digits only */, zoom_join_url TEXT NOT NULL DEFAULT '',
        planned_start_at INTEGER NOT NULL, planned_end_at INTEGER NOT NULL,
        actual_start_at INTEGER, actual_end_at INTEGER,
        status TEXT NOT NULL DEFAULT 'planned' CHECK(status IN ('planned','active','finished')),
        manual_finished_at INTEGER /* teacher pressed "finish" */,
        created_by INTEGER, created_at, updated_at INTEGER NOT NULL)
  INDEX(zoom_meeting_id, planned_start_at); INDEX(group_id, planned_start_at); INDEX(planned_start_at)

zoom_meetings(id PK /* one row per actual Zoom session = meeting UUID */, meeting_uuid TEXT NOT NULL UNIQUE,
              meeting_id TEXT NOT NULL, topic TEXT NOT NULL DEFAULT '', host_id TEXT NOT NULL DEFAULT '',
              lesson_id INTEGER FK lessons ON DELETE SET NULL,
              link_mode TEXT NOT NULL DEFAULT 'auto' CHECK(link_mode IN ('auto','manual')),
              started_at INTEGER, ended_at INTEGER, first_event_at INTEGER NOT NULL,
              created_at, updated_at INTEGER NOT NULL)
  INDEX(lesson_id); INDEX(meeting_id, first_event_at)

zoom_webhook_events(id PK, event_type TEXT NOT NULL, event_ts INTEGER NOT NULL DEFAULT 0,
                    meeting_id TEXT NOT NULL DEFAULT '', meeting_uuid TEXT NOT NULL DEFAULT '',
                    participant_name TEXT NOT NULL DEFAULT '', occurred_at INTEGER,
                    dedupe_key TEXT NOT NULL, payload TEXT NOT NULL,
                    received_at INTEGER NOT NULL, processed_at INTEGER,
                    processing_status TEXT NOT NULL DEFAULT 'pending'
                       CHECK(processing_status IN ('pending','processed','duplicate','ignored','failed')),
                    attempts INTEGER NOT NULL DEFAULT 0, next_attempt_at INTEGER, error TEXT NOT NULL DEFAULT '')
  INDEX(processing_status, id); INDEX(dedupe_key); INDEX(meeting_uuid, id); INDEX(received_at)

-- projections (rebuilt by processor; never edited by hand except manual columns)
zoom_participants(id PK, lesson_id FK lessons ON DELETE CASCADE, normalized_name TEXT NOT NULL,
                  display_name TEXT NOT NULL,
                  resolution TEXT NOT NULL CHECK(resolution IN ('matched','manual','unidentified','conflict','ignored')),
                  student_id INTEGER, ignore_reason TEXT NOT NULL DEFAULT '' /* host|list|manual */,
                  candidate_student_ids TEXT NOT NULL DEFAULT '' /* comma list for conflict */,
                  current_status TEXT NOT NULL CHECK(current_status IN ('online','left')),
                  first_join_at INTEGER, last_leave_at INTEGER,
                  total_seconds INTEGER NOT NULL DEFAULT 0, devices INTEGER NOT NULL DEFAULT 0,
                  computed_at, updated_at INTEGER NOT NULL)
  UNIQUE(lesson_id, normalized_name)

attendance(id PK, lesson_id FK lessons ON DELETE CASCADE, student_id FK students,
           first_join_at INTEGER, last_leave_at INTEGER, total_duration_seconds INTEGER NOT NULL DEFAULT 0,
           late_seconds INTEGER, early_leave_seconds INTEGER,
           current_status TEXT NOT NULL CHECK(current_status IN ('online','left','not_seen')),
           auto_status TEXT NOT NULL CHECK(auto_status IN ('present','absent')),
           manual_status TEXT CHECK(manual_status IN ('present','absent')),
           final_status TEXT NOT NULL CHECK(final_status IN ('present','absent')),
           comment TEXT NOT NULL DEFAULT '', manual_by INTEGER, manual_at INTEGER,
           computed_at, created_at, updated_at INTEGER NOT NULL)
  UNIQUE(lesson_id, student_id)

attendance_intervals(id PK, lesson_id FK lessons ON DELETE CASCADE,
                     zoom_participant_id INTEGER FK zoom_participants ON DELETE CASCADE,
                     attendance_id INTEGER FK attendance ON DELETE SET NULL /* NULL when not a student */,
                     meeting_uuid TEXT NOT NULL, zoom_participant_uuid TEXT NOT NULL DEFAULT '',
                     zoom_user_name TEXT NOT NULL, join_at INTEGER NOT NULL, leave_at INTEGER /* NULL = open */,
                     duration_seconds INTEGER /* NULL while open */, leave_reason TEXT NOT NULL DEFAULT '')
  INDEX(lesson_id); INDEX(attendance_id)

participant_mappings(id PK, lesson_id FK lessons ON DELETE CASCADE, normalized_name TEXT NOT NULL,
                     action TEXT NOT NULL CHECK(action IN ('student','ignore','none')),
                     student_id INTEGER, created_by INTEGER, created_at INTEGER NOT NULL)
  UNIQUE(lesson_id, normalized_name)

audit_log(id PK, at INTEGER NOT NULL, user_id INTEGER, user_name TEXT NOT NULL DEFAULT '',
          action TEXT NOT NULL, entity_type TEXT NOT NULL, entity_id TEXT NOT NULL DEFAULT '',
          lesson_id INTEGER, group_id INTEGER, previous_value TEXT NOT NULL DEFAULT '',
          new_value TEXT NOT NULL DEFAULT '', reason TEXT NOT NULL DEFAULT '')
  INDEX(lesson_id, at); INDEX(at)

settings(key TEXT PK, value TEXT NOT NULL /* JSON */, updated_at INTEGER NOT NULL, updated_by INTEGER)
```

Uniqueness of names inside a group spans **both** `students.normalized_name` (active) and
`student_aliases.normalized_alias`; the DB enforces each table separately and the store
enforces the cross-table rule inside the same transaction (returns `store.ErrNameTaken`).

## 5. Core algorithms

### 5.1 Name normalization — `names.Normalize(s string) string`

Order matters:
1. Unicode NFKC (`golang.org/x/text/unicode/norm`) — composes `е`+U+0308 → `ё`, maps NBSP/full-width to plain.
2. Drop format/invisible runes (Unicode category `Cf`: zero-width space/joiner, BOM, LRM/RLM, soft hyphen).
3. Lower-case (`strings.ToLower`, Unicode aware).
4. `ё` → `е`.
5. Every run of Unicode whitespace (`unicode.IsSpace`) → single ASCII space; trim.

`"  ИВАНОВ   Иван "` → `"иванов иван"`. `"Фёдоров"` → `"федоров"`. No fuzzy matching,
no transliteration, no initials logic (TZ §7). Empty result = "empty name" (never matches).

### 5.2 Meeting ID extraction — `names.ParseMeetingID(input string) (string, error)`

Accepts: bare digits with spaces/dashes (`123 456 7890`, `123-456-7890`); URLs
`https://zoom.us/j/<id>`, `https://<sub>.zoom.us/j/<id>?pwd=…`, `/s/<id>`, `/w/<id>`,
`/wc/join/<id>`, `/wc/<id>/join`, `zoommtg://zoom.us/join?confno=<id>`; whole invitation
text (first zoom URL wins; otherwise `Meeting ID: 857 4606 5432` / `Идентификатор конференции: …`).
Valid IDs: 9–11 digits. Personal links `/my/<name>` → error
`"Ссылка вида zoom.us/my/... не содержит номер конференции. Укажите числовой идентификатор (Meeting ID)."`.
`names.FormatMeetingID("1234567890")` → `"123 456 7890"` (Zoom grouping: 3-3-4 for 10, 3-4-4 for 11, 3-3-3 for 9).

### 5.3 Zoom webhook — `internal/zoom`

* `Verify(secret string, header http.Header, body []byte, now time.Time, maxAge time.Duration) error`
  * `x-zm-request-timestamp` (unix **seconds**), reject if older than `ZOOM_WEBHOOK_MAX_AGE` (default 2 h) or more than
    24 h in the future (`MaxFutureSkew`) → `ErrStale`. `RequestTime` exposes the parsed timestamp: the HTTP layer
    records the skew of every correctly signed request (largest value of the last 1–2 h, so Zoom's retries, which keep
    their original timestamp, do not hide it) and the time of the last `ErrStale` refusal, logs a hint to enable NTP on
    a refusal, and reports both in `GET /api/zoom/status` (§6). Refused requests are not stored (the status card must
    not turn green).
  * message `"v0:" + ts + ":" + string(body)`; `"v0=" + hex(HMAC-SHA256(secret, message))`;
    compare to `x-zm-signature` with `hmac.Equal` → `ErrBadSignature`.
  * Missing headers → `ErrMissingHeaders`. Applies to **all** requests incl. `endpoint.url_validation`.
* `URLValidation(secret, plainToken string) URLValidationResponse{PlainToken, EncryptedToken}` where
  `EncryptedToken = hex(HMAC-SHA256(secret, plainToken))`.
* `Parse(body []byte) (*Event, error)` → normalized struct:
  ```go
  type Event struct {
      Type        string            // "meeting.participant_joined", ...
      EventTS     int64             // ms
      MeetingID   string            // object.id as digit string (accept JSON number or string)
      MeetingUUID string
      HostID, Topic string
      StartTime, EndTime *time.Time // meeting.started / meeting.ended
      Participant *Participant      // participant_* events
      PlainToken  string            // url_validation
  }
  type Participant struct {
      UserName, ParticipantUUID, ID, UserID, ParticipantUserID, Email, LeaveReason string
      JoinTime, LeaveTime *time.Time
      BreakoutRoomUUID, ParentUserID string
  }
  func (e *Event) OccurredAt() time.Time // join_time / leave_time / start / end / event_ts fallback
  func (p *Participant) Key() string      // "puuid:"+ParticipantUUID | "uid:"+UserID | "pid:"+ID | "name:"+Normalize(UserName)
  // Zoom facts (researched 2026): participant_uuid is per join (changes on every rejoin, differs per
  // device) and stays the same inside breakout rooms; user_id is meeting-local per join; `id` is deprecated;
  // participant_left carries NO join_time; there is NO rename webhook; object.id is a string (older
  // payloads: number or null); join/leave times have second precision, event_ts has ms and orders events.
  ```
* `DedupeKey(e *Event, rawBody []byte) string` = hex sha256 (length-prefixed parts; breakout events also include `breakout_room_uuid`) of `type|meeting_uuid|participant.Key()|join_time or leave_time (RFC3339Nano)` for participant
  events, `type|meeting_uuid` for started/ended; anything else `type|event_ts|sha256(body)`.
  `event_ts` is deliberately excluded for participant/meeting events so Zoom redeliveries (which may
  carry a new event_ts) dedupe too.
* Supported types: `meeting.started`, `meeting.ended`, `meeting.participant_joined`,
  `meeting.participant_left`, plus breakout-room variants
  `meeting.participant_joined_breakout_room`, `meeting.participant_left_breakout_room` (treated
  as presence in the parent meeting, see §5.4). Everything else is stored with status `ignored`.
  `participant.date_time` is used as join time only for non-leave events (doc examples set it to the meeting
  start on leave events). `zoom_meetings.started_at` comes only from `meeting.started` (or `meeting.ended`'s
  start_time), never from participant payloads. NUL characters are stripped from decoded strings before storage
  (PostgreSQL TEXT rejects them).
  Participant events with an empty meeting id whose UUID is not a known session (old Zoom behaviour for
  breakout rooms) are stored as `ignored` (error text explains why) — they cannot be linked to a lesson.

### 5.4 Attendance engine — `internal/attendance` (pure, no I/O, no clock)

```go
type Input struct {
    Lesson       LessonInfo        // ID, PlannedStart, PlannedEnd, ManualFinishedAt *time.Time
    Sessions     []Session         // MeetingUUID, HostID, StartedAt, EndedAt *time.Time, FirstEventAt time.Time
    Events       []ParticipantEvent// all non-duplicate participant events of these sessions
    Roster       []RosterStudent   // StudentID, FullName, NormalizedName, NormalizedAliases []string
    Mappings     map[string]Mapping// normalized display name -> {Action: student|ignore|none, StudentID}
    IgnoredNames map[string]bool   // normalized, from settings
    IgnoreHost   bool
    PresentMinPercent int          // 0 = present if seen at all
    Now          time.Time
}
type ParticipantEvent struct {
    EventID int64; Kind EventKind /* Join|Leave */; MeetingUUID string; At time.Time
    Key, ParticipantUUID, ParticipantID, UserID, UserName, LeaveReason string
    JoinTime *time.Time // participant_left may carry join_time
}
type Output struct {
    ActualStart, ActualEnd *time.Time
    Status       model.LessonStatus
    Participants []ParticipantResult // one per normalized display name
    Students     []StudentResult     // one per roster student (and per mapped non-roster student)
    Intervals    []IntervalResult    // raw device intervals with resolution
}
func Compute(in Input) Output
func UnionSeconds(intervals []Span, now time.Time) int64 // open spans end at now; overlap counted once
```

Rules:
1. **Interval building, per session**, events sorted by `(At, EventTS, Kind Join<Leave, EventID)`
   (`At` has second precision; Zoom's `event_ts` in ms orders same-second events, e.g. main-room leave
   followed by a breakout join):
   * `Join(key)`: if an interval for `key` is open with the **same** normalized name → ignore (duplicate / lost leave).
     If open with a **different** name → close it at `At` (rename) and open a new one. Else open new.
   * `Leave(key)`: close the open interval for `key`. No open interval → if `JoinTime` present use `[JoinTime, At]`,
     otherwise record a zero-length interval `[At, At]` (participant was seen, duration unknown).
     **Rename at leave**: Zoom has no rename webhook; if the leave event's normalized `UserName` is non-empty and
     differs from the interval's join name, the interval is attributed to the leave name (the latest name of that
     device) and remembers `RenamedFrom` = join name. Resolution (rule 2) uses the interval's final name.
     Exception (`keepIdentifiedJoinNames`): when the join name resolves to a roster student and the leave name does
     not resolve to that same student (unidentified, ignored, a conflict or another student), the interval stays with
     the join name — a student identified once stays seen (TZ §18) and the result never contradicts what the teacher
     saw live. The opposite direction (unidentified at join → a student at leave) still moves the interval. The result
     does not depend on event order (the reordered orphan-leave path keeps the join name too).
   * Breakout events: `joined_breakout_room` = Join, `left_breakout_room` = Leave, in the parent session
     (their `object.uuid` is the main meeting UUID), keyed in a **separate namespace** `"bo:"+key` so they never
     close or de-duplicate main-room intervals. Union (rule 3) removes the overlap, so the student stays present
     while in a breakout room even though Zoom may report a main-room leave.
   * A session's intervals end at `min(EndedAt, lesson end)`, where the lesson end is `ManualFinishedAt` (the
     teacher's — or the upkeep's, §5.6 — finish). A join after that moment is ignored, a leave is clipped to it, and
     every interval still open closes there (TZ §17, scenario 7). A manual finish therefore stays final even when
     `meeting.ended` arrives later: it cannot extend intervals or count latecomers.
   * Leave reasons: an interval closed by a Zoom leave event keeps Zoom's `leave_reason` text; intervals the
     engine closes itself get a code: `meeting_ended` (open at `meeting.ended`), `lesson_finished` (open when
     the teacher finished the lesson), `renamed` (the same device joined again under another name). The
     processor stores `attendance_intervals.leave_reason` as that text/code, and for an interval renamed at
     leave (`RenamedFrom`) as `renamed from: <old name>` or `<Zoom reason>; renamed from: <old name>`.
2. **Resolution** of each normalized display name `n` (first rule that applies; an interval renamed at leave
   uses its final name):
   1. `Mappings[n]` (teacher decision): `student` → `manual` (when the student is part of the lesson roster,
      otherwise `unidentified`); `ignore` → `ignored` (reason `manual`); `none` → `unidentified`.
   2. Empty normalized name → `unidentified`.
   3. Exact match against the names **and** aliases of the roster students that take part in automatic
      matching (active students, archived students that already have a row in this lesson, kept matches of a
      finished lesson — §5.6): exactly 1 student → `matched`; >1 → `conflict` (`candidate_student_ids`);
      0 → continue.
   4. `IgnoredNames[n]` → `ignored` (reason `list`). The ignore list is global, but because rule 3 runs first
      it never hides a name that a student of **this lesson's group** uses as name or alias — "always hide
      «Александр»" in one group cannot hide the student whose alias is «Александр» in another.
   5. `IgnoreHost` and a device with this name is a session host (participant `ID`/`UserID` equals the
      session `HostID`) → `ignored` (reason `host`).
   6. → `unidentified`.
3. **Per student**: all intervals of all names resolved to the student, across sessions →
   `total = UnionSeconds(...)` (overlaps from two devices / breakout counted once — scenario 5).
   `current_status`: `online` if any open interval; `left` if any interval; else `not_seen`.
   `first_join_at` = min join; `last_leave_at` = max leave among closed intervals (nil if none).
   `late_seconds` = max(0, first_join − PlannedStart) (nil if not seen).
   `early_leave_seconds` (only when lesson finished and student seen) = max(0, end − last_leave), where
   end = min(ActualEnd, the latest end of the lesson's Zoom sessions): when the teacher presses "finish" after
   Zoom already closed, staying until Zoom closed is not an early leave (TZ §20).
   `auto_status` = `present` iff seen **and** (PresentMinPercent == 0 or the student's time inside the percent
   window ≥ PresentMinPercent% × the window's length). The window (`percentWindow`) is the planned
   `[PlannedStart, PlannedEnd]` while the lesson runs; once finished it is the overlap of the planned window with
   `[ActualStart, ActualEnd]` (a lesson that ran shorter is honoured, the host's Zoom time before and after the lesson
   is not), or the actual time when the lesson ran entirely outside the plan. The student's union is clipped to the
   window for this rule only (`clippedUnion`); `total_seconds` shown to users stays unclipped.
   `final_status` = manual_status if set else auto_status (applied by store, manual columns are not in Output).
4. **Per participant name**: union total, online/left, first join, last leave, devices = distinct keys
   (Zoom gives every join a new `participant_uuid`, so this counts connections, not physical devices: the UI
   words it «N подключения»; the student card numbers «устройство N» only for connections that overlap).
5. **Lesson**: ActualStart = min(StartedAt ?? FirstEventAt) over sessions; ActualEnd = ManualFinishedAt if set,
   else max(EndedAt) when every session has EndedAt; Status = `finished` if ActualEnd set, `active` if any session,
   else `planned`.
6. Totals in Output are "as of `Now`". Live clients add `(clientNow − asOf)` for `online` rows.

### 5.5 Session ↔ lesson linking — `internal/processor`

On every event with a meeting UUID:
1. Upsert `zoom_meetings` by `meeting_uuid` (meeting_id, topic, host_id, started_at from `meeting.started`,
   ended_at from `meeting.ended`, first_event_at = min occurred_at).
2. If `lesson_id` already set → that lesson. If `link_mode='manual'` and `lesson_id IS NULL` → unlinked by a human, stop.
3. Else auto-link (`bestCandidate`): `ref = started_at ?? first_event_at`; candidates
   (`store.FindLessonsByMeeting`) = lessons with the same `zoom_meeting_id` and
   `planned_start_at − W ≤ ref ≤ planned_end_at + 30 min` (W = setting `link_window_hours`, default **1**: how long
   before the planned start a meeting may be opened; the 30 minutes after the planned end are fixed). A lesson the
   session **ended** more than 10 min before the planned start of is not a candidate (a test call or the previous
   meeting on a reused personal meeting ID must not finish a lesson before it began). Candidates are ranked by tier:
   1. **running** — `ref` inside the planned `[start, end]`;
   2. **overrun restart** — `ref` after the planned end and another session of that lesson ended after the planned
      end within 10 min of `ref` (the lesson was running over and the meeting was restarted);
   3. **upcoming** — `ref` before the planned start (the meeting was opened early);
   4. **past** — `ref` after the planned end (the lesson started late or ran over).

   Within a tier the closer lesson wins (distance to the planned start for running/upcoming, to the planned end
   otherwise), then the lower id. Recurring meetings therefore attach each occurrence (new UUID) to the right dated
   lesson; a restart in the second half of a lesson stays with it instead of going to the next lesson on the same
   meeting; a meeting opened in the break goes to the lesson about to start; an unrelated meeting hours later on the
   same personal meeting ID stays unlinked (it shows on the Zoom page) instead of reopening and stretching a
   finished lesson. Multiple sessions per lesson are unioned. If a manually finished lesson receives a new session
   that started after `manual_finished_at`, clear `manual_finished_at` (audit «Занятие возобновлено автоматически»).
   An auto-linked session whose `meeting.ended` shows it ended more than 10 min before its lesson's planned start
   is moved to its best candidate or unlinked (mode `auto`).
4. On lesson create/update (`RelinkLesson`): link unlinked auto-mode sessions with that meeting id whose best
   candidate is this lesson; move or unlink auto-linked sessions of this lesson whose best candidate is no
   longer this lesson; claim auto-linked sessions of nearby lessons (same meeting id, planned time within
   `[start − (W + 30 min), end + (W + 30 min)]`, `NearbyRange`) for which this lesson is now the best candidate
   (a better-fitting lesson created late). Manual links and manual unlinks are never touched. Every path (event,
   relink, claim, delete) uses the same `bestCandidate` rules.
5. On lesson delete: `store.DeleteLesson` detaches its sessions (`lesson_id = NULL`, mode `auto`); the handler
   then runs `RelinkLesson` on every lesson that could be a candidate of them — same meeting id within
   `NearbyRange` of the deleted lesson, plus the candidates of each detached session at its own reference time
   (`CandidateRange`; it may have been linked by hand from another meeting id or another day) — so a remaining
   lesson (e.g. the twin of a lesson created twice by mistake) takes them over even when no Zoom event will come
   any more.
6. Rebuild the lesson projection (§5.6), commit, publish `lesson:<id>` and `lessons`.

**Known limitation (deferred, no schema change in the MVP):** `zoom_meetings.lesson_id` is a single column, so
one Zoom session (one meeting UUID) feeds at most one lesson. A joint lecture of several groups on one meeting,
or back-to-back lessons on one meeting that keeps running, leave every other lesson `planned` with all students
`not_seen` (the other groups' students show as unidentified in the linked lesson). Documented workarounds
(admin-guide §6 «Одна конференция Zoom — одно занятие», teacher-guide): a combined group for a joint lecture or
a manual link to the lesson that matters; separate meetings, or ending and restarting the meeting between
back-to-back lessons (a meeting restarted inside the next lesson's planned time links to that lesson by rule 3);
one lesson for a double lesson of one
group. The later fix replaces the column with a link table (`zoom_meeting_lessons`), links a session to every
lesson whose window overlaps its span, and clips each lesson's intervals to its own window.

### 5.6 Projection rebuild — `processor.RebuildLesson(ctx, lessonID)`

Inside one transaction: load lesson, linked sessions, participant events (`processing_status <> 'duplicate'`,
types participant_*), roster, aliases, mappings, settings → `attendance.Compute` → write. All processor
transactions run under **one** in-process mutex taken before BEGIN (never inside a transaction: SQLite has a
single writer); the webhook worker processes a batch of up to 100 events in one transaction, falling back to
one event per transaction when the batch fails. Under a backlog one transaction keeps taking the next batch while
it lasts (at most 2000 events or 200 ms) and rebuilds every affected lesson once at the end; per-event status and
dedupe are unchanged, and if the transaction fails the first batch is retried one event at a time (later events
stay pending). Never call processor methods while holding a store transaction.

Roster of a rebuild:
* Lesson **not finished**: active students of the group, students that already have an attendance row in this
  lesson (archived students keep their history) and targets of this lesson's manual mappings.
* Lesson **finished** (and rows exist): the roster is frozen — students with a row plus mapping targets.
  Students who joined the group later do not appear in a past lesson. The roster also stays frozen while a lesson
  that was held (stored `finished`) or whose planned time is over has **no linked session**
  (`heldWithoutSessions`: a wrong Zoom link pasted and fixed, an unlink and relink); its previous automatic matches
  are carried as participants without time (`carriedMatches`), so `keepMatches` still applies when the session
  comes back.
* Automatic name matching (§5.4 rule 2.3) covers active students **and archived students that already have a row
  in this lesson** (they were in the group when the lesson ran; a later rebuild must not turn their attended time
  into "not seen"). A name an active student also uses belongs to the active student. Archived students without a
  row (mapping targets only) are never auto-matched.
* **Finished lessons keep their matches** (TZ §17, `keepMatches`): a Zoom name the previous projection matched to a
  roster student stays with that student even when the current names no longer produce the match (student renamed,
  alias removed). Teacher mappings still win; once a rebuild does not match the name (a mapping overrode it, or the
  lesson was reopened), nothing is kept. Deleting a wrong alias therefore does not un-match finished lessons — the
  teacher fixes such a lesson with "Не студент" or a mapping.

Writes:
* `zoom_participants`: upsert by (lesson_id, normalized_name), delete names no longer present (stable ids).
* `attendance`: upsert by (lesson_id, student_id) computed columns only; `final_status = COALESCE(manual_status, auto_status)`.
  While not finished: rows for every active student; rows of archived students are kept when the student was seen or
  has a manual status/comment, otherwise deleted. Finished: existing rows, plus rows of manually mapped students seen
  in the lesson.
* `attendance_intervals`: delete + insert for the lesson.
* `lessons`: actual_start_at, actual_end_at, status.

**Upkeep** (`processor/upkeep.go`, at start and then every minute):
* with `present_min_percent > 0`, running lessons that have an online student whose stored status is still
  `absent` (`store.ListLessonsOnlineAbsent`) are rebuilt, so the stored status, the live view, lists and exports agree
  once enough time has passed without any new event;
* a running lesson whose planned end and every open session have been silent for **6 hours**
  (`ListActiveLessonsEndedBefore`, `LatestEventAt`) is finished automatically — Zoom's `meeting.ended` was lost.
  The finish time is the latest known moment (planned end, a session start or the last event), stored as
  `manual_finished_at` with an audit entry by «Система» («Завершено автоматически: …»), so «Возобновить» and a late
  `meeting.ended` behave as for a teacher's finish. The web shows an overdue banner from 1 h after the planned end.

Who triggers rebuilds besides Zoom events: teacher actions on the lesson (finish, reopen, recompute, map/unmap,
status), lesson create/edit/delete (relink, §5.5), session link/unlink, a settings change (every lesson planned
within the last 60 days or later, in the background), and roster/alias changes of a group (every lesson of the
group that is not finished). Both automatic refreshes skip **stale planned lessons** — still `planned` (no Zoom
session ever came) and planned end more than 24 h ago: a cancelled lesson must not gain students who joined the
group later, and without participants new settings change nothing there. The live screen of such a lesson does
not add rows for students without one either (like a finished lesson; a lesson never rebuilt shows every active
student), so it agrees with the lesson list counts. "Пересчитать" still rebuilds it with the current roster.

### 5.7 Webhook pipeline (TZ §23)

```
POST /api/webhooks/zoom
  read body (limit 1 MiB) → zoom.Verify → (bad → 401, log reason, no body echo)
  → zoom.Parse (unparseable but signed → store as 'ignored' with error, 200)
  → url_validation → 200 {plainToken, encryptedToken}   (also stored, status 'ignored')
  → store raw row (status pending, dedupe_key) → 200 {"status":"ok"} → processor.Notify()
processor worker (single goroutine; also runs every 5 s and once at startup):
  SELECT pending ORDER BY id LIMIT 100 (next_attempt_at <= now)
  per event: if earlier non-duplicate row with same dedupe_key exists → 'duplicate'
             else tx{ upsert session, link, rebuild } → 'processed'
             error → attempts++, next_attempt_at = now + 2^attempts s, status stays 'pending'; attempts ≥ 6 → 'failed'
```
Ingest must answer Zoom in < 1 s typical (Zoom requires < 3 s). Business errors never lose the raw row.
Webhook answers: 401 `invalid_signature` (bad/stale signature), 503 when `ZOOM_WEBHOOK_SECRET_TOKEN` is not set.

### 5.8 Realtime

`internal/realtime.Broker`: `Subscribe(topic string) (ch <-chan struct{}, cancel func())`,
`Publish(topic string)` (non-blocking, coalescing: channel buffer 1).
Topics: `lesson:<id>`, `lessons` (any lesson list change).
`GET /api/lessons/{id}/stream` (SSE): sends `event: snapshot` + LessonView JSON immediately,
then on every publish (debounced 150 ms), `: ping` every 20 s; headers `Cache-Control: no-cache`,
`X-Accel-Buffering: no`; write deadline disabled via `http.ResponseController`.
`GET /api/stream` (SSE): `event: lessons` (no data) for dashboards.
Client: `EventSource`; on error falls back to polling every 5 s until reconnect. Target: UI update
< 1 s after webhook receipt (TZ requires ≤ 5 s).
`LessonView.as_of` never goes back by a small step within one server process (`Server.lessonNow`): clients keep the
newest view by `as_of`, so after a backward step of the system clock (NTP, VM time sync) every snapshot would
otherwise be dropped until the clock caught up. A backward step larger than 2 minutes is a real clock correction:
`as_of` follows the new time (logged as a warning), otherwise the durations of everyone online would stay inflated
by the size of the step; clients treat a snapshot more than 2 minutes older than the cached one as newer (same rule).

### 5.9 Raw event retention — `store.PurgeEventsBefore`

Daily at 03:30 (`TIMEZONE`) the server deletes raw webhook events received more than `EVENTS_RETENTION_DAYS`
ago (effective minimum 90 days, 0 = never), one DELETE per window of 2000 event ids (each statement scans at most
that many rows, however many old events are kept, and the SQLite write lock is released in between). Never
deleted, whatever their age:
* events whose `meeting_uuid` belongs to a `zoom_meetings` row with `lesson_id IS NOT NULL` — the lesson's
  attendance is recomputed from them on every rebuild (recompute, manual mapping, settings change, relink,
  lesson edit), so purging them would wipe it;
* `pending` events (not processed yet).

Deleted: events of sessions that were never linked or were unlinked (including after their lesson was deleted)
and events without a session (url_validation, unsupported types). The 90-day floor keeps unlinked sessions'
events long enough to link such a session by hand on the Zoom page.

## 6. HTTP API

JSON, snake_case. Times are RFC 3339 UTC strings (`time.Time`), nullable → `null`.
Errors: `{"error":{"code":"...","message":"<Russian, human readable>","details":{...}}}`. Codes: `validation` (422,
`details.field` names the field), `not_found` (404), `conflict` (409), `unauthorized` (401), `forbidden` (403),
`bad_request` (400/413/415), `rate_limited` (429, with a `Retry-After` header and `details.retry_after_seconds`),
`unavailable` (503, directory/IdP down), `internal` (500), and the specific `invalid_credentials` (422, wrong login
or password), `access_denied` (403, authenticated but no configured role/group), `account_disabled` (403, a
directory account disabled here), `password_change_required` (403 on every route except me/logout/password while
the user must change a temporary password), `invalid_signature` (401, Zoom webhook).
Auth: session cookie `zc_session` (HttpOnly, SameSite=Lax, Secure when PUBLIC_URL is https).
CSRF: `http.CrossOriginProtection` on all non-GET routes except `/api/webhooks/zoom`; JSON bodies only.

Public:
* `GET /healthz` → `ok`; `GET /readyz` → DB ping.
* `GET /api/auth/config` → `AuthConfig {local_enabled, ldap_enabled, oidc_enabled, oidc_label, version, public_url,
  timezone}` (`public_url` = PUBLIC_URL without a trailing `/`, named by the login page after `error=oidc_host`;
  `timezone` = IANA name of `TIMEZONE`, the web notes when the browser's zone differs)
* `POST /api/auth/login` `{username,password}` → `{"user": User}`. Wrong login or password → 422
  `invalid_credentials` («Неверный логин или пароль», also for disabled local accounts); LDAP user without a
  configured group → 403 `access_denied`; directory account disabled here → 403 `account_disabled`; directory
  down → 503 `unavailable`. Rate limits (sliding 15 min; "IP" is the client network: an IPv6 address counts per
  /64, an IPv4-mapped one as IPv4): 10 attempts per IP + account (domain spellings of one account share the key;
  reset by a successful login), 60 attempts per IP (failed or unfinished attempts only: a successful login gives its
  attempt back without resetting the counter) and 30 per account from any address → 429 `rate_limited` (the
  Russian message names the address and, when OIDC is on, the Keycloak button). A successful login sets the
  known-device cookie `zc_known_login` (HMAC-signed, HttpOnly, SameSite=Strict, path `/api/auth/login`, 90 days,
  up to 5 accounts); attempts from such a browser are counted per device + account only (10 / 15 min), so other
  people's failures from a shared NAT address never lock out a teacher on a computer they used before. Password
  hashing runs in a small shared pool (2–4 argon2 slots): when every slot is busy the login answers 503
  `unavailable` with `Retry-After: 3` before allocating any memory, and the attempt is not counted.
* `GET /auth/oidc/login?next=/path&prompt=login|select_account` → 302 to the IdP. `prompt` is passed to the
  authorization request (any other value → `/login?error=oidc_failed`), so the login page can offer
  «Войти под другой учётной записью» after a denial (Keycloak would otherwise send the same account back).
  When the refused account's ID token is still in the `zc_oidc_denied` cookie (§8), a `prompt` request first
  ends that Keycloak session instead: 302 to `end_session_endpoint?id_token_hint=…&state=switch_account`, back to
  `/login?state=switch_account` (the page then asks to sign in with the other account).
  Before that provider logout the switch flow keeps `next` in the sealed, HttpOnly, 10-minute cookie `zc_oidc_next`
  (path `/auth/oidc`), because the post-logout redirect URI must match exactly; the next login takes it back unless an
  explicit `next` is given.
* `GET /auth/oidc/callback` → 302 to `next`, or on failure to `/login?error=oidc_failed|disabled|access_denied|oidc_host`;
  `oidc_failed`, `disabled` and `access_denied` keep `&next=` (the path the user was going to); `access_denied` adds
  `&required=<URL-encoded comma-separated roles/groups that grant access>` (teacher rules first, then admin; at most
  20 values / 1000 bytes; omitted when nothing is configured). `oidc_host`: the callback arrived without the flow
  cookie — the login was started on another address than PUBLIC_URL (logged at WARN with PUBLIC_URL); the login page
  names `public_url` from the auth config.
* `POST /api/webhooks/zoom`

Authenticated (teacher or admin):
* `GET /api/auth/me` → `{"user": User}`; `POST /api/auth/logout` → `{redirect_url}`;
  `POST /api/auth/password` `{current_password,new_password}` (local) → `{"user": User}` (wrong current password →
  422 with `details.field = "current_password"`; other sessions of the user are signed out)
* `GET /api/groups?status=active|archived|all` → `[GroupSummary]`
* `POST /api/groups` `{name, description?, default_duration_minutes?}`; `GET /api/groups/{id}` → `GroupDetail` (students + aliases)
* `PATCH /api/groups/{id}` `{name?, description?, status?, default_duration_minutes?}`
* `POST /api/groups/{id}/students` `{full_name, external_id?}`; `PATCH /api/students/{id}` `{full_name?, external_id?, status?}`
* `POST /api/students/{id}/aliases` `{alias}`; `DELETE /api/students/{id}/aliases/{alias_id}`
* `POST /api/groups/{id}/import/preview` — multipart `file` (csv/xlsx/txt) **or** JSON `{text}`; query
  `mode=add|replace`, `name_format=as_is|last_first` → `ImportPreview`. Rows whose names repeat get status
  `duplicate` with the TZ §8 sentence plus the other line number(s), «Если это один и тот же студент — удалите лишнюю
  строку» and a hint with a letter of their own («Петрова Анна А.» / «… Б.»). A row matched to a student only by
  `external_id` must share at least one word (initials and punctuation aside, hyphenated parts split) with that
  student's name or aliases, otherwise it is an `error` (the ID belongs to another person); an archived student matched
  only by ID is not restored. An XLSX file is checked before parsing (zip directory and every part: at most 32 MiB
  uncompressed in total, 5000 entries, 4 MiB per non-worksheet part, 100k elements per part, 200k shared strings and
  200k elements in total); one workbook is parsed at a time and the server allows two imports at once — when busy, 503
  `unavailable` with `Retry-After`.
* `POST /api/groups/{id}/import/commit` `{mode, name_format?, rows:[{external_id, full_name, line?}]}` → `ImportResult`
  (server re-validates; any error → 422 with `details.preview`; the roster changed meanwhile → 409)
* `GET /api/groups/{id}/report?from=&to=` → `GroupReport`; `GET /api/groups/{id}/report/export?format=xlsx|csv&from=&to=&tz=`.
  `from`/`to` are `YYYY-MM-DD` in the zone (`tz` when given, else `TIMEZONE`; `to` inclusive: the whole day) or RFC
  3339 instants. `tz` is an optional IANA zone (the web sends the browser's): lesson times and the dates of the file
  name use it; an empty, unknown or `Local` value falls back to `TIMEZONE`. The XLSX legend and file properties name
  the UTC offset. There is **no
  default window**: a missing bound leaves the period open on that side (`from`/`to` are `null` in the answer),
  so "Всё время" covers every lesson (the web client sends both bounds, "Всё время" ending today). Cells of
  lessons still `planned` are `null` unless a teacher set the status by hand.
* `GET /api/zoom/parse?input=` → `{meeting_id, formatted}` or 422
* `GET /api/lessons?from=&to=&group_id=&status=` → `[LessonSummary]` (with live counts)
* `POST /api/lessons` `{group_id, title, zoom, planned_start_at, planned_end_at, repeat_weekly?: 1..52}` → `[Lesson]`
* `GET /api/lessons/{id}` → `Lesson` (incl. group, sessions)
* `PATCH /api/lessons/{id}` `{title?, zoom?, planned_start_at?, planned_end_at?}`; `DELETE /api/lessons/{id}`
* `POST /api/lessons/{id}/finish` `{at?}`; `POST /api/lessons/{id}/reopen`; `POST /api/lessons/{id}/recompute`
* `GET /api/lessons/{id}/attendance` → `LessonView` (lesson + counts + students + participants + as_of)
* `GET /api/lessons/{id}/unidentified` → `[Participant]` (resolution unidentified|conflict)
* `GET /api/lessons/{id}/participants` → `[Participant]` (all, incl. ignored)
* `POST /api/lessons/{id}/participants/{participant_id}/map` `{student_id?, action?: "student"|"ignore"|"none", remember_alias?: bool, ignore_always?: bool, reason?}`
* `DELETE /api/lessons/{id}/participants/{participant_id}/map` → `LessonView`: removes the manual decision. When it
  mapped the name to a student, that student's alias whose normalized form equals the participant's normalized
  name (what «Запомнить это имя» stored) is deleted in the same transaction — otherwise the name would match the
  student again automatically; the audit entry says so (plus an `alias.delete` entry) and the group's other
  unfinished lessons are rebuilt in the background
* `PATCH /api/lessons/{id}/attendance/{student_id}` `{status?: "present"|"absent"|null /* null = back to auto */, comment?, reason?}`
* `GET /api/lessons/{id}/students/{student_id}/history` → `{intervals:[...], audit:[...]}`
* `GET /api/lessons/{id}/audit` → `[AuditEntry]`
* `GET /api/lessons/{id}/export?format=csv|xlsx&tz=` — `tz` as for the report export: times are written in that zone
  and the clock columns name it («Первый вход (UTC+5)», «Последний выход (UTC+5)», also on the «Не
  идентифицированы» sheet). Every minute figure, on screen and in exports, is whole minutes **rounded down**
  (`export.Minutes` = seconds / 60, the same rule as `web/src/lib/format.ts`), so 59 s is 0 min and 90 s is 1 min.
* `GET /api/lessons/{id}/stream` (SSE), `GET /api/stream` (SSE)
* `GET /api/zoom/sessions?unlinked=1&limit=` → `[ZoomSession]` newest first (`limit` default 100, at most 500; larger
  values are capped), each with the summary of its linked lesson (`lesson`, `null` when unlinked);
  `POST /api/zoom/sessions/{id}/link` `{lesson_id: number|null}` → `ZoomSession` (`null` unlinks for good: mode manual)
* `GET /api/zoom/status` → `ZoomStatus {webhook_url, secret_configured, last_event_at, last_event_type, events_24h,
  failed_count, pending_count, clock_skew_seconds, last_rejected_at}`. `clock_skew_seconds` = Zoom's request time minus
  the server clock, measured on recent correctly signed webhooks (positive: the server clock is behind; `null` before the
  first one since the server started); `last_rejected_at` = the last signed webhook refused because its timestamp was too
  far from the server clock (that event is lost; `null` when none). The web warns (icon + text) when
  `|clock_skew_seconds| > 120` or a refusal happened in the last 24 h.

Admin only:
* `GET/PUT /api/settings` → `{ignored_names: string[], present_min_percent, link_window_hours, ignore_host}`
  (`link_window_hours` 1..72, default 1: how long before a lesson's planned start its meeting may start, §5.5)
* `GET /api/users`; `POST /api/users` `{username, display_name, password, role}` (local; 409 with
  `details.field = "username"` for a taken login);
  `PATCH /api/users/{id}` `{role?, disabled?, display_name?, password?}` → `User` (a password reset sets
  `must_change_password` and signs the user out elsewhere; resetting the bootstrap admin's password — local
  account named `ADMIN_USERNAME` — removes `DATA_DIR/initial-admin-password.txt`, as does any change that replaces
  the password the file holds); `DELETE /api/users/{id}` (not self). The last active admin cannot be demoted,
  disabled or deleted (422).
* `GET /api/zoom/events?status=&type=&q=&before_id=&limit=` → `WebhookEventsPage {items:[WebhookEventSummary], next_before_id}`
  (`limit` default 50, at most 200); `GET /api/zoom/events/{id}` → `WebhookEventDetail` (with payload);
  `POST /api/zoom/events/{id}/reprocess` → `WebhookEventDetail`; `POST /api/zoom/events/reprocess-failed` → `{count}`
* `GET /api/system` → `SystemInfo` (no secrets)
* `POST /api/system/test-ldap` `{username?, password?}` → `TestLdapResult` (step-by-step diagnostics, Russian, no
  secrets; the «Группы» step lists full DNs). Runs that include a password are limited to 10 per 15 minutes per tested
  account and 60 per admin (a passed check does not reset the budget) → 429; every run writes an audit entry
  `ldap.test` without the password.

Shapes (TypeScript, mirrored by Go structs in `internal/httpapi/dto.go`):

```ts
type Role = 'admin' | 'teacher'
interface User { id: number; username: string; display_name: string; email: string; role: Role; auth_source: 'local'|'oidc'|'ldap'; must_change_password: boolean; disabled: boolean; last_login_at: string|null }
interface GroupSummary { id: number; name: string; description: string; status: 'active'|'archived'; student_count: number; default_duration_minutes: number; created_at: string; last_lesson_at: string|null; next_lesson_at: string|null }
interface Student { id: number; group_id: number; external_id: string; full_name: string; normalized_name: string; status: 'active'|'archived'; aliases: { id: number; alias: string }[] }
interface GroupDetail extends GroupSummary { students: Student[] }
interface ImportRow { line: number /* 0 for archive rows */; external_id: string; full_name: string; normalized_name: string; status: 'new'|'existing'|'error'|'duplicate'|'archive'; message: string; student_id: number|null; restore: boolean /* a "new" row that brings back archived student student_id */; rename: boolean /* an "existing" row that changes student student_id's stored name */ }
interface ImportCounts { new: number; existing: number; error: number; duplicate: number; archive: number; restore: number /* "new" rows that restore, also counted in new */; rename: number /* "existing" rows that rename, also counted in existing (UI: «Будут переименованы: N») */ }
interface ImportPreview { mode: 'add'|'replace'; name_format: 'as_is'|'last_first'; has_patronymics: boolean /* offer name_format=last_first */; rows: ImportRow[]; counts: ImportCounts; detected: { format: 'csv'|'xlsx'|'text'; encoding: string; delimiter: string; name_column: string } }
interface ImportCommitRow { external_id: string; full_name: string; line?: number /* echoed in details.preview of a rejected commit */ }
interface ImportResult { created: number; existing: number /* matched, nothing changed */; updated: number /* name or ID changed */; archived: number; restored: number } // counts do not overlap
interface Lesson { id: number; group: { id: number; name: string }; title: string; zoom_meeting_id: string; zoom_meeting_id_formatted: string; zoom_join_url: string; planned_start_at: string; planned_end_at: string; actual_start_at: string|null; actual_end_at: string|null; status: 'planned'|'active'|'finished'; manual_finished_at: string|null; sessions: ZoomSession[] }
interface Counts { in_group: number; online: number; seen: number; not_seen: number; unidentified: number; present: number; absent: number }
interface LessonSummary extends Omit<Lesson,'sessions'> { counts: Counts }
interface StudentRow { student_id: number; full_name: string; external_id: string; current_status: 'online'|'left'|'not_seen'; total_seconds: number; first_join_at: string|null; last_leave_at: string|null; late_seconds: number|null; early_leave_seconds: number|null; auto_status: 'present'|'absent'; manual_status: 'present'|'absent'|null; final_status: 'present'|'absent'; comment: string; zoom_names: string[]; devices_online: number; archived: boolean }
interface Participant { id: number; display_name: string; normalized_name: string; resolution: 'matched'|'manual'|'unidentified'|'conflict'|'ignored'; ignore_reason: ''|'host'|'list'|'manual'; student_id: number|null; student_name: string|null; candidate_student_ids: number[]; current_status: 'online'|'left'; first_join_at: string|null; last_leave_at: string|null; total_seconds: number; devices: number; manual: boolean }
interface LessonView { lesson: Lesson; counts: Counts; students: StudentRow[]; participants: Participant[]; as_of: string }
interface Interval { id: number; join_at: string; leave_at: string|null; duration_seconds: number|null; zoom_user_name: string; zoom_participant_uuid: string; meeting_uuid: string; leave_reason: string /* Zoom text | meeting_ended | lesson_finished | renamed | "renamed from: <old name>" | "<Zoom text>; renamed from: <old name>" (§5.4) */ }
interface AuditEntry { id: number; at: string; user_name: string; action: string; entity_type: string; entity_id: string; previous_value: string; new_value: string; reason: string }
interface ZoomSessionLesson { id: number; title: string; group_name: string; planned_start_at: string }
interface ZoomSession { id: number; meeting_uuid: string; meeting_id: string; topic: string; lesson_id: number|null; lesson: ZoomSessionLesson|null; link_mode: 'auto'|'manual'; started_at: string|null; ended_at: string|null; first_event_at: string }
interface Settings { ignored_names: string[]; present_min_percent: number /* 0..100 */; link_window_hours: number /* 1..72 */; ignore_host: boolean }
interface AuthConfig { local_enabled: boolean; ldap_enabled: boolean; oidc_enabled: boolean; oidc_label: string; version: string; public_url: string; timezone: string /* IANA name of TIMEZONE */ }
interface LoginResponse { user: User } // login, me, password change
interface ZoomStatus { webhook_url: string; secret_configured: boolean; last_event_at: string|null; last_event_type: string; events_24h: number; failed_count: number; pending_count: number; clock_skew_seconds: number|null; last_rejected_at: string|null }
interface GroupReportCell { lesson_id: number; final_status: 'present'|'absent'|null /* null: no row, or planned lesson without a manual status */; manual: boolean; total_seconds: number; late_seconds: number|null }
interface GroupReportLesson { id: number; title: string; planned_start_at: string; status: 'planned'|'active'|'finished' }
interface GroupReportStudent { student_id: number; full_name: string; external_id: string; archived: boolean; cells: GroupReportCell[] /* same order as lessons */; present_count: number; absent_count: number }
interface GroupReport { group: { id: number; name: string }; from: string|null; to: string|null /* inclusive bounds used; null = open */; lessons: GroupReportLesson[]; students: GroupReportStudent[] }
interface WebhookEventSummary { id: number; event_type: string; meeting_id: string; meeting_uuid: string; participant_name: string; occurred_at: string|null; received_at: string; processed_at: string|null; processing_status: 'pending'|'processed'|'duplicate'|'ignored'|'failed'; attempts: number; error: string }
interface WebhookEventDetail extends WebhookEventSummary { event_ts: number; dedupe_key: string; next_attempt_at: string|null; payload: string /* raw body */ }
interface WebhookEventsPage { items: WebhookEventSummary[]; next_before_id: number|null }
interface AuthProviderStatus { enabled: boolean; detail: string /* Russian, no secrets */ }
interface SystemInfo { version: string; commit: string; db_driver: 'sqlite'|'postgres'; tls_mode: 'off'|'auto'|'file'; public_url: string; data_dir: string; timezone: string; uptime_seconds: number; auth: { local: AuthProviderStatus; ldap: AuthProviderStatus; oidc: AuthProviderStatus } }
interface DiagnosticStep { name: string; ok: boolean; detail: string } // Go auth.DiagStep
interface TestLdapResult { ok: boolean /* every step ok */; steps: DiagnosticStep[] }
```

`GET /` and any non-`/api`, non-`/auth` path serves the SPA (`index.html` fallback; hashed assets
`Cache-Control: public, max-age=31536000, immutable`).

Security headers on every response: `Content-Security-Policy: default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'`,
`X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin`, `Strict-Transport-Security` when https.
User-supplied strings are rendered only through React text nodes (auto-escaped); exports prefix cells
starting with `= + - @ \t \r` with `'` (CSV/XLSX formula injection).

## 7. UI (Russian)

Routes: `/login`, `/` (Занятия: today / upcoming / past, big "Новое занятие" button),
`/lessons/:id` (live screen, TZ §14), `/groups`, `/groups/:id` (students, import wizard, aliases, памятка),
`/groups/:id/report`, `/zoom` (Zoom connection status + setup guide + unlinked sessions),
`/admin/users`, `/admin/settings`, `/admin/events` (raw webhook journal), `/help`.

Live lesson screen: header (title, group, date/time, Zoom ID with copy, status, live indicator),
5 stat tiles (В группе / Сейчас в Zoom / Были хотя бы раз / Не подключались / Не идентифицированы),
student table (ФИО · статус badge 🟢 В Zoom / 🟡 Вышел / 🔴 Не был · время (ticks live) · первый вход
(+опоздание) · итог Присутствовал/Отсутствовал toggle with "вручную" marker · комментарий),
row → side sheet with interval history, Zoom names, audit, manual actions.
"Не идентифицированы" block: name · status · time · "Это студент…" (searchable select +
"Запомнить это имя для следующих занятий") · "Не студент" (+ "всегда скрывать это имя").
Collapsed "Скрытые участники" (host / list / manual) with "Вернуть". Export buttons (Excel, CSV).
"Завершить занятие" / "Возобновить". Empty-state hints explain what to do.
Banners: waiting (5 min after the planned start it lists running conferences without a lesson under another number
with «Привязать к этому занятию»), overdue (status `active` more than 1 h after the planned end with an open session:
«Zoom не сообщил, что конференция закончилась» + «Завершить занятие»; the lesson list card shows the same hint instead
of live numbers), finished (exports; says so when the service finished the lesson itself, §5.6). A «Конференции Zoom»
card lists the linked sessions (number, topic, time, auto/manual) with «Это другая конференция» (unlink, mode manual).
When the browser's zone differs from `TIMEZONE` a note with a globe icon names both offsets (date/time fields, the
finish dialog, the page footer); downloads pass the browser's zone as `tz`.

## 8. Auth details

* Passwords: argon2id (m=19456 KiB, t=2, p=1, 16-byte salt, 32-byte key), PHC string format.
* Sessions: 32 random bytes → base64url cookie; DB stores sha256 hex. **Absolute** lifetime counted from the login:
  `SESSION_TTL` for local accounts, `EXTERNAL_SESSION_TTL` (default 24 h) for LDAP/OIDC, in the DB row and the cookie
  `Max-Age`; requests and SSE re-checks never extend it (`validateSession` only reads; `loadSession` updates
  `last_seen_at` at most once per minute). A disabled user's sessions stop working on the next request (open SSE
  streams within 60 s). Logout deletes row.
* Login form: if a **local** user with that username exists → local password check only.
  Otherwise, if LDAP enabled → LDAP. Unknown → generic "Неверный логин или пароль".
* LDAP: dial with timeout; ldaps or StartTLS; bind service account (or direct UPN bind for AD without
  service account); search `LDAP_USER_FILTER` with escaped username (strip `DOMAIN\` prefix; for AD accept UPN);
  exactly one entry; **reject empty password before any bind**; bind as user DN; collect groups
  (`memberOf`; AD nested: search `(member:1.2.840.113556.1.4.1941:=<userDN>)`; OpenLDAP without memberOf:
  `(|(member=<dn>)(uniqueMember=<dn>)(memberUid=<uid>))`); map roles; upsert user by (ldap, id attr).
* OIDC: discovery; state + nonce + PKCE verifier in an encrypted, HttpOnly, 10-minute cookie
  (`zc_oidc`, AES-GCM with a key derived from a random server secret stored in DB `settings.server_secret`);
  verify ID token; roles from ID token claims, falling back to UserInfo and to the access token
  claims (Keycloak puts `realm_access`/`resource_access` in the access token by default); upsert user by (oidc, sub);
  logout → `end_session_endpoint?id_token_hint=…&post_logout_redirect_uri=PUBLIC_URL/login`.
* Role mapping: admin if any admin role/group matches; else teacher if any teacher role matches (or `*`);
  else access denied page that names the roles/groups that are required (OIDC: the callback redirects to
  `/login?error=access_denied&required=…`, §6; the page offers «Войти под другой учётной записью»). Keycloak 26
  answers `prompt=login` with a password form locked to the same account and ignores `select_account`, so the
  callback keeps the ID token of an account it refuses (no role, or disabled here → `error=disabled`, which offers
  the same link) in a sealed, HttpOnly, 10-minute cookie `zc_oidc_denied` (path `/auth/oidc`). The link then ends
  that Keycloak session first (RP-initiated logout with `id_token_hint`, no confirmation page; this also signs the
  account out of other apps of the realm) and the next login shows Keycloak's empty form; without the cookie it
  falls back to `prompt=login`. A successful login deletes the cookie. Role is re-synced at every login for external users; local users' role is set
  by admins (an admin's change to a directory user's role lasts until that user's next login).
* Passwords are hashed in a shared pool of 2–4 argon2 slots (`auth.HashPasswordContext`/`VerifyPasswordContext`,
  `ErrBusy` before any memory is allocated); `cmd/zoom-checker` sets a soft memory limit of 192 MiB when `GOMEMLIMIT`
  is unset. The CLI and the bootstrap wait for a slot without a time limit.
* Login rate limits (`auth.LoginLimiter`, §6): per IP + account, per IP, per account, and per known device. `Allow` counts an attempt before the
  check; a failure confirms it; a success resets the per-account key and only gives back its own attempt on the
  per-IP key (`Release`), so one valid account cannot clear the protection against username spraying, while a
  school behind one NAT address is not locked out by its successful logins. Behind a reverse proxy set
  `TRUSTED_PROXIES`, otherwise every client shares the proxy's address.
* Bootstrap: first start with empty `users` → create local admin (§3), `must_change_password=1` when generated
  or when `ADMIN_PASSWORD` fails the password policy.
  `DATA_DIR/initial-admin-password.txt` is removed once the password it holds is no longer valid: when the
  bootstrap admin's password changes (own change or an admin's reset) or the replaced password is the one in the
  file; at start a stale file (no local admin with `must_change_password` verifies against it) is removed.

## 9. CLI

```
zoom-checker [serve]                       start server (default)
zoom-checker migrate                       apply migrations and exit
zoom-checker user create|set-password|list ...   manage local users
zoom-checker check-config                  validate config, print redacted summary; warns (Russian) about
                                           a missing Zoom secret, a non-https or IP-address PUBLIC_URL, a weak
                                           ADMIN_PASSWORD, https behind a proxy without TRUSTED_PROXIES, a bare
                                           group name in LDAP_ADMIN_GROUPS and a TLS_MODE=file port mismatch
zoom-checker check-ldap [--user U] [--password-stdin]
                                           connectivity + bind + search diagnostics (the password is
                                           checked only when given on stdin)
zoom-checker check-oidc                    discovery + client credentials sanity
zoom-checker simulate --meeting ID [--url URL] [--secret S] [--insecure] [--names file] [--duration D]
                      [--speed N] [--instant] [--seed N] [--no-end] [--dry-run] [--topic T] [--host-name N]
                                           send signed realistic Zoom webhooks (demo/testing); URL and
                                           secret default to PUBLIC_URL + /api/webhooks/zoom and
                                           ZOOM_WEBHOOK_SECRET_TOKEN. With --instant the meeting starts
                                           --duration ago; the legend names the lesson start that links
zoom-checker backup FILE                   online backup (SQLite VACUUM INTO / pg: prints pg_dump hint)
zoom-checker version
```

Validation messages, warnings and every `--help` are Russian (the flag help is printed by `printFlagUsage`, not
the flag package); setting names, paths and flags stay literal.

## 10. Logging (TZ §29)

slog with fields: `event_type, meeting_id, meeting_uuid, participant` (display name), `lesson_id`,
`resolution`, `student_id`, `err`. Logged: webhook received, signature failures (reason only),
match result, attendance changes, processing errors. Never logged: secrets, tokens, passwords,
cookies, Authorization headers, full payloads at info level.

## 11. Deployment

* `deploy/install.sh` (bash, idempotent, RHEL/OL/Alma/Rocky/Debian/Ubuntu): installs binary to
  `/usr/local/bin/zoom-checker`, creates system user, `/var/lib/zoom-checker`, `/etc/zoom-checker/zoom-checker.env`
  (only if missing), systemd unit (hardened, `AmbientCapabilities=CAP_NET_BIND_SERVICE`, system-call filter),
  opens firewall (firewalld/ufw) for 80/443 (or the port), validates with `check-config` as the service user
  (`env -i`) before every restart, starts, waits for `zoom-checker healthcheck`, prints URL + the initial admin
  password. The unit runs `zoom-checker --config /etc/zoom-checker/zoom-checker.env serve` (not systemd's
  `EnvironmentFile=`, whose rules differ), so validation and runtime read the file with the same parser (§3).
  Docs call the CLI by its full path (RHEL-family `sudo` `secure_path` omits `/usr/local/bin`) and run the
  `check-*` commands as the service user (`sudo -u zoom-checker …`), so a root-only readable key or CA file is
  reported before the restart instead of crash-looping the service.
* Docker: multi-stage → `gcr.io/distroless/static-debian12:nonroot`, `time/tzdata` embedded,
  volume `/data`. `deploy/docker-compose.yml` (SQLite) + optional `postgres` profile.
* Release: `make release` → `dist/`: `zoom-checker-linux-{amd64,arm64}`, `install.sh`, `zoom-checker.service`,
  `.env.example`, `SHA256SUMS` (`make build` → `bin/zoom-checker` for this machine).

## 12. Testing

* Unit: names, zoom (signature vectors, url_validation, parsing of real payload samples, dedupe), attendance
  engine (all TZ §36 scenarios + edge cases: out-of-order, duplicate, orphan leave, rename, two devices,
  breakout, multi-session lesson, manual mapping transfer, present-percent rule), importer, export.
* Store: same suite on SQLite (always) and PostgreSQL (`TEST_POSTGRES_URL`).
* HTTP integration: httptest server + signed webhooks → API/SSE assertions (latency < 1 s).
* Auth integration: OpenLDAP / Samba AD and Keycloak containers (`deploy/dev/*`), skipped when env not set.
* E2E: Playwright against the real binary (`web/e2e/README.md`): TZ §36 S1–S9 (S1 measures webhook → «В Zoom»
  latency, ≤ 5 s), teacher and admin flows, `simulate`, kill -9/restart of a live page, tablet/laptop layout;
  project `auth` (Keycloak, OpenLDAP, Samba AD via `deploy/dev/auth-stand`, `E2E_AUTH_STAND=1`);
  `E2E_DATABASE_URL` runs it on PostgreSQL.

## 13. Deliberate deviations from TZ

* Student ↔ group is one-to-many (`students.group_id`) instead of `group_students` M:N: a person in two
  groups is two student records; this keeps import/uniqueness semantics simple and per-group.
* Table `groups` is named `student_groups` (`GROUPS` is an SQL keyword in SQLite/PostgreSQL window syntax).
* Aliases (`student_aliases`) are an addition: still exact match after normalization (no fuzzy), lets a teacher
  "remember" a manual mapping for future lessons.
* "Two Zoom participants with the same display name" is treated as one student on two devices (TZ §32 device rules);
  "conflict" arises when one display name matches several students/aliases of the group.
