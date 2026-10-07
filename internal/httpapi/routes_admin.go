package httpapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"zoom-checker/internal/auth"
	"zoom-checker/internal/model"
	"zoom-checker/internal/store"
)

const (
	msgUserNotFound  = "Пользователь не найден"
	msgLastAdmin     = "Нельзя убрать последнего администратора"
	maxDisplayName   = 200
	ldapTestTimeout  = 45 * time.Second
	userAuditEntity  = "user"
	settingsEntityID = "rules"
)

// usernamePattern is the allowed form of local logins (already lower-cased).
var usernamePattern = regexp.MustCompile(`^[a-z0-9._@-]{2,64}$`)

// lastAdminMu serializes the user changes that are checked against the
// last-admin rule. SQLite already serializes write transactions, but on
// PostgreSQL (READ COMMITTED) two parallel demotions of two different admins
// could each still see the other one and leave no administrator at all.
var lastAdminMu sync.Mutex

func (s *Server) registerAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/users", s.admin(s.handleListUsers))
	mux.HandleFunc("POST /api/users", s.admin(s.handleCreateUser))
	mux.HandleFunc("PATCH /api/users/{id}", s.admin(s.handleUpdateUser))
	mux.HandleFunc("DELETE /api/users/{id}", s.admin(s.handleDeleteUser))

	mux.HandleFunc("GET /api/settings", s.admin(s.handleGetSettings))
	mux.HandleFunc("PUT /api/settings", s.admin(s.handlePutSettings))

	mux.HandleFunc("GET /api/system", s.admin(s.handleSystemInfo))
	mux.HandleFunc("POST /api/system/test-ldap", s.admin(s.handleTestLDAP))
}

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) error {
	users, err := s.Store.ListUsers(r.Context())
	if err != nil {
		return errInternal(err)
	}
	out := make([]UserDTO, 0, len(users))
	for i := range users {
		out = append(out, toUserDTO(&users[i]))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type createUserRequest struct {
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name"`
	Password    string     `json:"password"`
	Role        model.Role `json:"role"`
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) error {
	var req createUserRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	username := strings.ToLower(strings.TrimSpace(req.Username))
	if !usernamePattern.MatchString(username) {
		return errValidation("Логин: от 2 до 64 символов — латинские буквы, цифры и знаки . _ @ -",
			map[string]any{"field": "username"})
	}
	display, err := validDisplayName(req.DisplayName)
	if err != nil {
		return err
	}
	if err := validRole(req.Role); err != nil {
		return err
	}
	if err := auth.ValidateNewPassword(req.Password); err != nil {
		return errValidation(err.Error(), map[string]any{"field": "password"})
	}
	ctx := r.Context()
	// A local account shadows a directory account with the same login (the
	// login form checks local accounts first), which is never what is meant.
	if _, err := s.Store.GetUserByUsername(ctx, model.AuthLDAP, username); err == nil {
		return errConflict("Этот логин уже используется для входа через LDAP", map[string]any{"field": "username"})
	} else if !errors.Is(err, store.ErrNotFound) {
		return errInternal(err)
	}
	hash, err := auth.HashPasswordContext(ctx, req.Password)
	if errors.Is(err, auth.ErrBusy) {
		return busyError(w)
	}
	if err != nil {
		return errInternal(err)
	}
	u := &model.User{
		Username: username, DisplayName: display, Role: req.Role, AuthSource: model.AuthLocal,
		PasswordHash: hash, MustChangePassword: true,
	}
	if err := s.Store.CreateUser(ctx, u); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return errConflict("Пользователь с таким логином уже есть", map[string]any{"field": "username"})
		}
		return errInternal(err)
	}
	s.audit(r, model.AuditEntry{Action: model.AuditUserCreate, EntityType: userAuditEntity,
		EntityID: strconv.FormatInt(u.ID, 10), NewValue: describeUser(u)})
	writeJSON(w, http.StatusCreated, toUserDTO(u))
	return nil
}

type updateUserRequest struct {
	Role        *model.Role `json:"role"`
	Disabled    *bool       `json:"disabled"`
	DisplayName *string     `json:"display_name"`
	Password    *string     `json:"password"`
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var req updateUserRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	var display string
	if req.DisplayName != nil {
		if display, err = validDisplayName(*req.DisplayName); err != nil {
			return err
		}
	}
	if req.Role != nil {
		if err := validRole(*req.Role); err != nil {
			return err
		}
	}
	var hash string
	if req.Password != nil {
		if err := auth.ValidateNewPassword(*req.Password); err != nil {
			return errValidation(err.Error(), map[string]any{"field": "password"})
		}
		hash, err = auth.HashPasswordContext(r.Context(), *req.Password)
		if errors.Is(err, auth.ErrBusy) {
			return busyError(w)
		}
		if err != nil {
			return errInternal(err)
		}
	}
	me := currentUser(r)
	if req.Disabled != nil && *req.Disabled && id == me.ID {
		return errValidation("Нельзя отключить собственную учётную запись", map[string]any{"field": "disabled"})
	}

	ctx := r.Context()
	var before, after model.User
	dropSessions := false
	lastAdminMu.Lock()
	defer lastAdminMu.Unlock()
	err = s.Store.InTx(ctx, func(tx *store.Store) error {
		u, err := tx.GetUser(ctx, id)
		if err != nil {
			return storeErr(err, msgUserNotFound)
		}
		before = *u
		if req.DisplayName != nil {
			u.DisplayName = display
		}
		if req.Role != nil {
			u.Role = *req.Role
		}
		if req.Disabled != nil {
			u.Disabled = *req.Disabled
			dropSessions = dropSessions || (u.Disabled && !before.Disabled)
		}
		if req.Password != nil {
			if u.AuthSource != model.AuthLocal {
				return errValidation("Пароль этой учётной записи меняется в корпоративной системе", map[string]any{"field": "password"})
			}
			u.PasswordHash = hash
			u.MustChangePassword = true
			dropSessions = true
		}
		if isActiveAdmin(&before) && !isActiveAdmin(u) {
			if err := ensureAnotherAdmin(ctx, tx, u.ID); err != nil {
				return err
			}
		}
		if err := tx.UpdateUser(ctx, u); err != nil {
			return storeErr(err, msgUserNotFound)
		}
		if dropSessions {
			// An admin resetting their own password keeps this browser signed in.
			keep := ""
			if u.ID == me.ID {
				keep = currentSession(r).IDHash
			}
			if _, err := tx.DeleteUserSessions(ctx, u.ID, keep); err != nil {
				return errInternal(err)
			}
		}
		after = *u
		return nil
	})
	if err != nil {
		return err
	}
	if req.Password != nil {
		// The bootstrap admin's generated password is no longer valid.
		s.removeInitialAdminPassword(&after, func(held string) bool {
			ok, _ := auth.VerifyPasswordContext(r.Context(), before.PasswordHash, held)
			return ok
		})
	}
	prev, next := describeUserChange(&before, &after, req.Password != nil)
	if next != "" {
		s.audit(r, model.AuditEntry{Action: model.AuditUserUpdate, EntityType: userAuditEntity,
			EntityID: strconv.FormatInt(after.ID, 10), PreviousValue: prev, NewValue: next})
	}
	writeJSON(w, http.StatusOK, toUserDTO(&after))
	return nil
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if id == currentUser(r).ID {
		return errValidation("Нельзя удалить собственную учётную запись", nil)
	}
	ctx := r.Context()
	var deleted model.User
	lastAdminMu.Lock()
	defer lastAdminMu.Unlock()
	err = s.Store.InTx(ctx, func(tx *store.Store) error {
		u, err := tx.GetUser(ctx, id)
		if err != nil {
			return storeErr(err, msgUserNotFound)
		}
		if isActiveAdmin(u) {
			if err := ensureAnotherAdmin(ctx, tx, u.ID); err != nil {
				return err
			}
		}
		if err := tx.DeleteUser(ctx, id); err != nil {
			return storeErr(err, msgUserNotFound)
		}
		deleted = *u
		return nil
	})
	if err != nil {
		return err
	}
	s.audit(r, model.AuditEntry{Action: model.AuditUserDelete, EntityType: userAuditEntity,
		EntityID: strconv.FormatInt(id, 10), PreviousValue: describeUser(&deleted)})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func isActiveAdmin(u *model.User) bool { return u.Role == model.RoleAdmin && !u.Disabled }

// ensureAnotherAdmin fails unless an enabled admin other than exceptID exists.
// It runs inside the transaction that changes the user, so two parallel
// demotions cannot both pass (SQLite serializes write transactions).
func ensureAnotherAdmin(ctx context.Context, tx *store.Store, exceptID int64) error {
	users, err := tx.ListUsers(ctx)
	if err != nil {
		return errInternal(err)
	}
	for i := range users {
		if users[i].ID != exceptID && isActiveAdmin(&users[i]) {
			return nil
		}
	}
	return errValidation(msgLastAdmin, nil)
}

func validDisplayName(raw string) (string, error) {
	d := strings.TrimSpace(raw)
	if utf8.RuneCountInString(d) > maxDisplayName {
		return "", errValidation("Имя слишком длинное: не больше 200 символов", map[string]any{"field": "display_name"})
	}
	if strings.ContainsFunc(d, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", errValidation("Имя содержит недопустимые символы", map[string]any{"field": "display_name"})
	}
	return d, nil
}

func validRole(r model.Role) error {
	if r != model.RoleAdmin && r != model.RoleTeacher {
		return errValidation("Выберите роль: администратор или преподаватель", map[string]any{"field": "role"})
	}
	return nil
}

func roleText(r model.Role) string {
	if r == model.RoleAdmin {
		return "администратор"
	}
	return "преподаватель"
}

// authSourceText names how an account signs in, for audit texts.
func authSourceText(src model.AuthSource) string {
	switch src {
	case model.AuthLDAP:
		return "вход через LDAP"
	case model.AuthOIDC:
		return "вход через Keycloak"
	default:
		return "вход по паролю Zoom Checker"
	}
}

func describeUser(u *model.User) string {
	s := u.Username + " (" + roleText(u.Role) + ", " + authSourceText(u.AuthSource) + ")"
	if u.Disabled {
		s += ", отключён"
	}
	return s
}

// describeUserChange renders the audit previous/new values of a user update
// (never the password itself). next is "" when nothing changed.
func describeUserChange(before, after *model.User, passwordReset bool) (prev, next string) {
	var p, n []string
	if before.DisplayName != after.DisplayName {
		p = append(p, "имя: "+before.DisplayName)
		n = append(n, "имя: "+after.DisplayName)
	}
	if before.Role != after.Role {
		p = append(p, "роль: "+roleText(before.Role))
		n = append(n, "роль: "+roleText(after.Role))
	}
	if before.Disabled != after.Disabled {
		state := map[bool]string{true: "отключён", false: "активен"}
		p = append(p, state[before.Disabled])
		n = append(n, state[after.Disabled])
	}
	if passwordReset {
		n = append(n, "пароль сброшен администратором")
	}
	return strings.Join(p, "; "), strings.Join(n, "; ")
}
