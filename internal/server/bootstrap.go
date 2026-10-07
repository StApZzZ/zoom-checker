package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"zoom-checker/internal/auth"
	"zoom-checker/internal/config"
	"zoom-checker/internal/model"
	"zoom-checker/internal/store"
)

// InitialPasswordFileName is the file in DATA_DIR that receives the generated
// password of the first administrator (install.sh prints it).
const InitialPasswordFileName = "initial-admin-password.txt"

// serverSecretKey is the settings row holding the random server secret from
// which purpose-specific keys (e.g. the OIDC flow cookie key) are derived.
const serverSecretKey = "server_secret"

// InitialPasswordPath returns where the generated first-admin password is written.
func InitialPasswordPath(dataDir string) string {
	return filepath.Join(dataDir, InitialPasswordFileName)
}

// bootstrapAdmin creates the first local administrator when the database has no
// users yet (docs/architecture.md §3, §8). With ADMIN_PASSWORD set it is used as
// is (the account must change it at first login when it fails the password
// policy); otherwise a random password is written to
// DATA_DIR/initial-admin-password.txt (0600) and the account must change it at
// first login. Only the file path is logged.
func bootstrapAdmin(ctx context.Context, st *store.Store, cfg *config.Config, log *slog.Logger) error {
	n, err := st.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("count users: %w", err)
	}
	if n > 0 {
		removeStaleInitialPassword(ctx, st, cfg, log)
		return nil
	}
	if !cfg.LocalAuthEnabled {
		log.Warn("no user accounts yet and local login is disabled: the first administrator has to sign in through OIDC/LDAP with an admin role")
		return nil
	}
	username := strings.ToLower(strings.TrimSpace(cfg.AdminUsername))
	if username == "" {
		username = "admin"
	}
	u := &model.User{
		Username:    username,
		DisplayName: "Администратор",
		Role:        model.RoleAdmin,
		AuthSource:  model.AuthLocal,
	}

	if cfg.AdminPassword != "" {
		// A policy-compliant ADMIN_PASSWORD is the administrator's own choice
		// and stays. A weak one (admin/admin, 123456) still works, so test and
		// dev setups start, but it is only good for the first login: an
		// internet-facing admin account must not keep it.
		if err := auth.ValidateNewPassword(cfg.AdminPassword); err != nil {
			u.MustChangePassword = true
			log.Warn("ADMIN_PASSWORD does not meet the password policy; the administrator will have to change it at first login",
				"username", username, "reason", err.Error())
		}
		if u.PasswordHash, err = auth.HashPassword(cfg.AdminPassword); err != nil {
			return fmt.Errorf("hash admin password: %w", err)
		}
		if err := st.CreateUser(ctx, u); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return nil // another instance sharing the database won the race
			}
			return fmt.Errorf("create admin: %w", err)
		}
		log.Info("created administrator account from ADMIN_PASSWORD", "username", username)
		return nil
	}

	pw := auth.GeneratePassword()
	if u.PasswordHash, err = auth.HashPassword(pw); err != nil {
		return fmt.Errorf("hash admin password: %w", err)
	}
	u.MustChangePassword = true
	path := InitialPasswordPath(cfg.DataDir)
	// Write the file first: an account whose password nobody can read would be
	// unreachable without the CLI.
	if err := writeFileAtomic(path, []byte(pw+"\n"), 0o600); err != nil {
		return fmt.Errorf("write initial admin password: %w", err)
	}
	if err := st.CreateUser(ctx, u); err != nil {
		_ = os.Remove(path)
		if errors.Is(err, store.ErrConflict) {
			return nil
		}
		return fmt.Errorf("create admin: %w", err)
	}
	log.Warn("created administrator account with a generated password; it must be changed at first login",
		"username", username, "password_file", path)
	return nil
}

// removeStaleInitialPassword deletes initial-admin-password.txt once the password
// in it no longer works (it was changed), so no stale secret stays on disk.
//
// Every local administrator that still has to change its password is checked,
// not only ADMIN_USERNAME: the variable may have been edited after the first
// start, and deleting the only copy of a still-valid first-login password would
// lock the administrator out. (Teachers are skipped: the file only ever holds
// the bootstrap admin's password, and each check costs an argon2id hash.)
func removeStaleInitialPassword(ctx context.Context, st *store.Store, cfg *config.Config, log *slog.Logger) {
	path := InitialPasswordPath(cfg.DataDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	pw := strings.TrimSpace(string(data))
	users, err := st.ListUsers(ctx)
	if err != nil {
		return // database trouble: decide at the next start
	}
	for _, u := range users {
		if u.AuthSource != model.AuthLocal || u.Role != model.RoleAdmin || !u.MustChangePassword || u.PasswordHash == "" {
			continue
		}
		if ok, _ := auth.VerifyPassword(u.PasswordHash, pw); ok {
			return // still the valid first-login password
		}
	}
	if err := os.Remove(path); err == nil {
		log.Info("removed the initial administrator password file: the password has been changed", "password_file", path)
	}
}

// RemoveInitialPasswordFile deletes DATA_DIR/initial-admin-password.txt (used by
// the CLI after an administrator password reset). Missing files are not an error.
func RemoveInitialPasswordFile(dataDir string) error {
	err := os.Remove(InitialPasswordPath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// serverSecret returns the random secret stored in settings, creating it on the
// first start. Concurrent first starts agree on one value (EnsureSetting).
func serverSecret(ctx context.Context, st *store.Store) ([]byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	v, err := st.EnsureSetting(ctx, serverSecretKey, hex.EncodeToString(b))
	if err != nil {
		return nil, fmt.Errorf("server secret: %w", err)
	}
	raw, err := hex.DecodeString(strings.TrimSpace(v))
	if err != nil || len(raw) < 16 {
		raw = []byte(v) // hand-edited value: still usable as key material
	}
	return raw, nil
}

// writeFileAtomic writes data to a temporary file next to path and renames it
// into place, so readers never see a half-written file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
