package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"zoom-checker/internal/auth"
	"zoom-checker/internal/model"
	"zoom-checker/internal/store"
)

// errExternalUserDisabled: the directory/IdP accepted the user, but an admin
// disabled the account here.
var errExternalUserDisabled = errors.New("httpapi: account disabled locally")

// errExternalIDTaken: a concurrent login created the row for this external id.
var errExternalIDTaken = errors.New("httpapi: external id created concurrently")

// maxUsernameSuffix bounds the "-2", "-3", ... attempts for a free username.
const maxUsernameSuffix = 100

// externalUserChange is what an LDAP/OIDC login changed in the account record.
// It feeds the audit log: who got access, and when the directory changed
// someone's role (e.g. made them an administrator).
type externalUserChange struct {
	created    bool
	prev, next string // describeUserChange of a sync; next == "" when nothing changed
}

// upsertExternalUser finds the account of an LDAP/OIDC identity strictly by
// (source, external id) and syncs display name, email and role from the
// provider (docs §8: re-synced at every login). A missing account is created;
// when its username is already used by another account of the same source
// (e.g. a re-created directory user with a new id) the new one gets a numeric
// suffix. Locally disabled accounts return errExternalUserDisabled.
func (s *Server) upsertExternalUser(ctx context.Context, id *auth.Identity) (*model.User, externalUserChange, error) {
	if id == nil || strings.TrimSpace(id.ExternalID) == "" {
		return nil, externalUserChange{}, errors.New("httpapi: identity without external id")
	}
	if id.Source != model.AuthLDAP && id.Source != model.AuthOIDC {
		return nil, externalUserChange{}, fmt.Errorf("httpapi: unexpected identity source %q", id.Source)
	}
	for range 3 {
		u, err := s.Store.GetUserByExternalID(ctx, id.Source, id.ExternalID)
		switch {
		case err == nil:
			if u.Disabled {
				return u, externalUserChange{}, errExternalUserDisabled
			}
			return s.syncExternalUser(ctx, u, id)
		case !errors.Is(err, store.ErrNotFound):
			return nil, externalUserChange{}, err
		}
		u, err = s.createExternalUser(ctx, id)
		if !errors.Is(err, errExternalIDTaken) {
			return u, externalUserChange{created: err == nil}, err
		}
		// Lost a race with a parallel login of the same person: load that row.
	}
	return nil, externalUserChange{}, errors.New("httpapi: could not create the user record")
}

func (s *Server) syncExternalUser(ctx context.Context, u *model.User, id *auth.Identity) (*model.User, externalUserChange, error) {
	before := *u
	changed := false
	if dn := strings.TrimSpace(id.DisplayName); dn != "" && dn != u.DisplayName {
		u.DisplayName, changed = dn, true
	}
	if email := strings.TrimSpace(id.Email); email != u.Email {
		u.Email, changed = email, true
	}
	if id.Role != "" && id.Role != u.Role {
		s.Log.Info("role synced from identity provider", "user_id", u.ID, "username", u.Username,
			"source", id.Source, "from", u.Role, "to", id.Role)
		u.Role, changed = id.Role, true
	}
	if !changed {
		return u, externalUserChange{}, nil
	}
	if err := s.Store.UpdateUser(ctx, u); err != nil {
		return nil, externalUserChange{}, fmt.Errorf("sync external user %d: %w", u.ID, err)
	}
	prev, next := describeUserChange(&before, u, false)
	return u, externalUserChange{prev: prev, next: next}, nil
}

func (s *Server) createExternalUser(ctx context.Context, id *auth.Identity) (*model.User, error) {
	base := strings.ToLower(strings.TrimSpace(id.Username))
	if base == "" {
		base = "user"
	}
	display := strings.TrimSpace(id.DisplayName)
	if display == "" {
		display = base
	}
	for n := 1; n <= maxUsernameSuffix; n++ {
		name := base
		if n > 1 {
			name = fmt.Sprintf("%s-%d", base, n)
		}
		_, err := s.Store.GetUserByUsername(ctx, id.Source, name)
		if err == nil {
			continue // taken by another account of this source
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		u := &model.User{
			Username: name, DisplayName: display, Email: strings.TrimSpace(id.Email),
			Role: id.Role, AuthSource: id.Source, ExternalID: id.ExternalID,
		}
		err = s.Store.CreateUser(ctx, u)
		if err == nil {
			if n > 1 {
				s.Log.Warn("external user created with a renamed username: the name is used by another account",
					"source", id.Source, "wanted", base, "username", name)
			}
			s.Log.Info("user created on first login", "user_id", u.ID, "username", u.Username,
				"source", u.AuthSource, "role", u.Role)
			return u, nil
		}
		if !errors.Is(err, store.ErrConflict) {
			return nil, err
		}
		if _, gerr := s.Store.GetUserByExternalID(ctx, id.Source, id.ExternalID); gerr == nil {
			return nil, errExternalIDTaken
		}
		// The name was taken in the meantime: try the next suffix.
	}
	return nil, fmt.Errorf("httpapi: no free username for %q", base)
}

// auditExternalUser records the creation of an account on its first LDAP/OIDC
// login and changes of its name or role synced from the directory. There is
// no logged-in user yet, so the entry is attributed to the account itself.
func (s *Server) auditExternalUser(r *http.Request, u *model.User, ch externalUserChange) {
	if u == nil || (!ch.created && ch.next == "") {
		return
	}
	id := u.ID
	name := u.DisplayName
	if name == "" {
		name = u.Username
	}
	e := model.AuditEntry{EntityType: userAuditEntity, EntityID: strconv.FormatInt(u.ID, 10), UserID: &id, UserName: name}
	if ch.created {
		e.Action, e.NewValue = model.AuditUserCreate, describeUser(u)
		e.Reason = "первый вход в систему (" + authSourceText(u.AuthSource) + ")"
	} else {
		e.Action, e.PreviousValue, e.NewValue = model.AuditUserUpdate, ch.prev, ch.next
		e.Reason = "данные обновлены при входе (" + authSourceText(u.AuthSource) + ")"
	}
	s.audit(r, e)
}
