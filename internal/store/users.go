package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"zoom-checker/internal/db"
	"zoom-checker/internal/model"
)

const userCols = `id, username, display_name, email, role, auth_source, external_id, password_hash,
	must_change_password, disabled, created_at, updated_at, last_login_at`

const userColsU = `u.id, u.username, u.display_name, u.email, u.role, u.auth_source, u.external_id, u.password_hash,
	u.must_change_password, u.disabled, u.created_at, u.updated_at, u.last_login_at`

// scanUserInto reads the userCols columns; extra destinations (e.g. session columns
// selected before the user) can be prepended via pre.
func scanUserInto(sc scanner, u *model.User, pre ...any) error {
	var created, updated int64
	var lastLogin sql.NullInt64
	dest := append(pre, &u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Role, &u.AuthSource, &u.ExternalID,
		&u.PasswordHash, &u.MustChangePassword, &u.Disabled, &created, &updated, &lastLogin)
	if err := sc.Scan(dest...); err != nil {
		return err
	}
	u.CreatedAt = fromMS(created)
	u.UpdatedAt = fromMS(updated)
	u.LastLoginAt = fromNullMS(lastLogin)
	return nil
}

func scanUser(sc scanner) (model.User, error) {
	var u model.User
	err := scanUserInto(sc, &u)
	return u, err
}

// CountUsers returns the number of user accounts (used for first-start bootstrap).
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.queryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count users: %w", err)
	}
	return n, nil
}

// CreateUser inserts u and sets u.ID, u.CreatedAt and u.UpdatedAt (store clock).
// Username is stored as given — callers lower-case it. Returns ErrConflict when
// (auth_source, username) or a non-empty (auth_source, external_id) is taken.
func (s *Store) CreateUser(ctx context.Context, u *model.User) error {
	now := s.nowMS()
	err := s.queryRow(ctx, `INSERT INTO users (username, display_name, email, role, auth_source, external_id,
		password_hash, must_change_password, disabled, created_at, updated_at, last_login_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		u.Username, u.DisplayName, u.Email, string(u.Role), string(u.AuthSource), u.ExternalID,
		u.PasswordHash, u.MustChangePassword, u.Disabled, now, now, msArg(u.LastLoginAt)).Scan(&u.ID)
	if err != nil {
		if db.IsUniqueViolation(err) {
			return fmt.Errorf("%w: user %q (%s) already exists", ErrConflict, u.Username, u.AuthSource)
		}
		return fmt.Errorf("store: create user: %w", err)
	}
	u.CreatedAt = fromMS(now)
	u.UpdatedAt = u.CreatedAt
	return nil
}

// GetUser returns the user with the given id or ErrNotFound.
func (s *Store) GetUser(ctx context.Context, id int64) (*model.User, error) {
	return s.getUser(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id)
}

// GetUserByUsername looks a user up by (source, username) — exact match, so pass
// the lower-cased username. ErrNotFound when missing.
func (s *Store) GetUserByUsername(ctx context.Context, source model.AuthSource, username string) (*model.User, error) {
	return s.getUser(ctx, `SELECT `+userCols+` FROM users WHERE auth_source = ? AND username = ?`, string(source), username)
}

// GetUserByExternalID looks an OIDC/LDAP user up by its stable external id (OIDC
// "sub", LDAP id attribute). ErrNotFound when missing or extID is empty.
func (s *Store) GetUserByExternalID(ctx context.Context, source model.AuthSource, extID string) (*model.User, error) {
	if extID == "" {
		return nil, ErrNotFound
	}
	return s.getUser(ctx, `SELECT `+userCols+` FROM users WHERE auth_source = ? AND external_id = ?`, string(source), extID)
}

func (s *Store) getUser(ctx context.Context, q string, args ...any) (*model.User, error) {
	u, err := scanUser(s.queryRow(ctx, q, args...))
	if err != nil {
		if err = notFound(err); err == ErrNotFound {
			return nil, err
		}
		return nil, fmt.Errorf("store: get user: %w", err)
	}
	return &u, nil
}

// ListUsers returns all users ordered by username, then auth source and id.
func (s *Store) ListUsers(ctx context.Context) ([]model.User, error) {
	rows, err := s.query(ctx, `SELECT `+userCols+` FROM users ORDER BY username, auth_source, id`)
	if err != nil {
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	out, err := collect(rows, scanUser)
	if err != nil {
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	return out, nil
}

// UpdateUser writes the mutable columns of u: Username, DisplayName, Email, Role,
// PasswordHash, MustChangePassword and Disabled (ID, AuthSource, ExternalID,
// CreatedAt and LastLoginAt are not changed). Sets u.UpdatedAt. Typical use is
// read-modify-write: GetUser, change fields, UpdateUser. ErrNotFound when the user
// does not exist, ErrConflict when the new username is taken.
func (s *Store) UpdateUser(ctx context.Context, u *model.User) error {
	now := s.nowMS()
	err := s.execOne(ctx, `UPDATE users SET username = ?, display_name = ?, email = ?, role = ?, password_hash = ?,
		must_change_password = ?, disabled = ?, updated_at = ? WHERE id = ?`,
		u.Username, u.DisplayName, u.Email, string(u.Role), u.PasswordHash,
		u.MustChangePassword, u.Disabled, now, u.ID)
	if err != nil {
		if err == ErrNotFound {
			return err
		}
		if db.IsUniqueViolation(err) {
			return fmt.Errorf("%w: user %q already exists", ErrConflict, u.Username)
		}
		return fmt.Errorf("store: update user: %w", err)
	}
	u.UpdatedAt = fromMS(now)
	return nil
}

// DeleteUser removes the user and (by cascade) all of its sessions. Rows that only
// reference the user for history (audit, lessons.created_by, manual_by, ...) keep the
// id. ErrNotFound when missing.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	if err := s.execOne(ctx, `DELETE FROM users WHERE id = ?`, id); err != nil {
		if err == ErrNotFound {
			return err
		}
		return fmt.Errorf("store: delete user: %w", err)
	}
	return nil
}

// TouchUserLogin records a successful login at the given time. ErrNotFound when missing.
func (s *Store) TouchUserLogin(ctx context.Context, id int64, at time.Time) error {
	if err := s.execOne(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`, toMS(at), id); err != nil {
		if err == ErrNotFound {
			return err
		}
		return fmt.Errorf("store: touch user login: %w", err)
	}
	return nil
}

// ---- sessions ----

const sessionColsS = `s.id_hash, s.user_id, s.created_at, s.expires_at, s.last_seen_at, s.ip, s.user_agent,
	s.auth_source, s.oidc_id_token`

// CreateSession stores a new session. All times come from sess (CreatedAt,
// ExpiresAt, LastSeenAt; a zero CreatedAt/LastSeenAt is replaced by the store clock).
// ErrConflict if the id hash already exists.
func (s *Store) CreateSession(ctx context.Context, sess *model.Session) error {
	now := fromMS(s.nowMS())
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = now
	}
	if sess.LastSeenAt.IsZero() {
		sess.LastSeenAt = sess.CreatedAt
	}
	_, err := s.exec(ctx, `INSERT INTO sessions (id_hash, user_id, created_at, expires_at, last_seen_at, ip, user_agent,
		auth_source, oidc_id_token) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.IDHash, sess.UserID, toMS(sess.CreatedAt), toMS(sess.ExpiresAt), toMS(sess.LastSeenAt),
		sess.IP, sess.UserAgent, string(sess.AuthSource), sess.OIDCIDToken)
	if err != nil {
		if db.IsUniqueViolation(err) {
			return fmt.Errorf("%w: session already exists", ErrConflict)
		}
		return fmt.Errorf("store: create session: %w", err)
	}
	return nil
}

// GetSessionWithUser returns the session with the given id hash and its user in one
// query. It does not check expiry or the disabled flag — the caller does.
// ErrNotFound when missing.
func (s *Store) GetSessionWithUser(ctx context.Context, idHash string) (*model.Session, *model.User, error) {
	var sess model.Session
	var u model.User
	var created, expires, seen int64
	row := s.queryRow(ctx, `SELECT `+sessionColsS+`, `+userColsU+`
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id_hash = ?`, idHash)
	err := scanUserInto(row, &u, &sess.IDHash, &sess.UserID, &created, &expires, &seen, &sess.IP, &sess.UserAgent,
		&sess.AuthSource, &sess.OIDCIDToken)
	if err != nil {
		if err = notFound(err); err == ErrNotFound {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("store: get session: %w", err)
	}
	sess.CreatedAt = fromMS(created)
	sess.ExpiresAt = fromMS(expires)
	sess.LastSeenAt = fromMS(seen)
	return &sess, &u, nil
}

// TouchSession slides a session: sets last_seen_at and expires_at. ErrNotFound when
// the session no longer exists.
func (s *Store) TouchSession(ctx context.Context, idHash string, lastSeen, newExpiry time.Time) error {
	if err := s.execOne(ctx, `UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE id_hash = ?`,
		toMS(lastSeen), toMS(newExpiry), idHash); err != nil {
		if err == ErrNotFound {
			return err
		}
		return fmt.Errorf("store: touch session: %w", err)
	}
	return nil
}

// DeleteSession removes one session (logout). Deleting a missing session is not an error.
func (s *Store) DeleteSession(ctx context.Context, idHash string) error {
	if _, err := s.exec(ctx, `DELETE FROM sessions WHERE id_hash = ?`, idHash); err != nil {
		return fmt.Errorf("store: delete session: %w", err)
	}
	return nil
}

// DeleteUserSessions removes all sessions of a user except keepIDHash (pass "" to
// remove all, e.g. when an admin disables the user; pass the current session's hash
// after a password change). Returns the number of deleted sessions.
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64, keepIDHash string) (int64, error) {
	n, err := s.execAffected(ctx, `DELETE FROM sessions WHERE user_id = ? AND id_hash <> ?`, userID, keepIDHash)
	if err != nil {
		return 0, fmt.Errorf("store: delete user sessions: %w", err)
	}
	return n, nil
}

// DeleteExpiredSessions removes sessions with expires_at <= now and returns how many.
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	n, err := s.execAffected(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, toMS(now))
	if err != nil {
		return 0, fmt.Errorf("store: delete expired sessions: %w", err)
	}
	return n, nil
}
