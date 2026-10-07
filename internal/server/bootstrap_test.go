package server

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"zoom-checker/internal/auth"
	"zoom-checker/internal/db"
	"zoom-checker/internal/model"
	"zoom-checker/internal/store"
)

// ADMIN_PASSWORD creates the first admin; a password that fails the policy
// still works but has to be changed at the first login.
func TestBootstrapAdminPasswordPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, password string
		mustChange     bool
	}{
		{"strong password is kept", "Strong-Pass-2026", false},
		{"too short", "admin", true},
		{"one repeated character", "aaaaaaaaaa", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := testConfig(t, map[string]string{"ADMIN_PASSWORD": tc.password})
			d, err := db.Open(context.Background(), cfg.DatabaseURL)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = d.Close() })
			if err := d.Migrate(context.Background()); err != nil {
				t.Fatal(err)
			}
			st := store.New(d)
			var logs bytes.Buffer
			if err := bootstrapAdmin(context.Background(), st, cfg, slog.New(slog.NewTextHandler(&logs, nil))); err != nil {
				t.Fatal(err)
			}
			u, err := st.GetUserByUsername(context.Background(), model.AuthLocal, "admin")
			if err != nil {
				t.Fatal(err)
			}
			if u.MustChangePassword != tc.mustChange || u.Role != model.RoleAdmin {
				t.Errorf("admin = role %s, must_change_password %v; want %v", u.Role, u.MustChangePassword, tc.mustChange)
			}
			if ok, _ := auth.VerifyPassword(u.PasswordHash, tc.password); !ok {
				t.Error("ADMIN_PASSWORD does not verify")
			}
			if warned := strings.Contains(logs.String(), "will have to change it at first login"); warned != tc.mustChange {
				t.Errorf("warning logged = %v, want %v; logs:\n%s", warned, tc.mustChange, logs.String())
			}
			if strings.Contains(logs.String(), tc.password) && len(tc.password) > 6 {
				t.Error("the password itself was logged")
			}
			if _, err := os.Stat(InitialPasswordPath(cfg.DataDir)); !os.IsNotExist(err) {
				t.Error("password file written although ADMIN_PASSWORD is set")
			}
		})
	}
}
