package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"zoom-checker/internal/auth"
	"zoom-checker/internal/config"
	"zoom-checker/internal/db"
	"zoom-checker/internal/model"
	"zoom-checker/internal/store"
)

func TestMain(m *testing.M) {
	// Never pick up a real installation's settings while testing.
	systemConfigFile = filepath.Join(os.TempDir(), "zoom-checker-test-no-such-file.env")
	systemDataDir = filepath.Join(os.TempDir(), "zoom-checker-test-no-such-dir")
	os.Exit(m.Run())
}

// cliEnv isolates the configuration of one CLI test: every variable the CLI
// may read is cleared (and restored after the test), then vars are set.
func cliEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "CONFIG_FILE", k == "DATA_DIR", k == "DATABASE_URL", k == "PUBLIC_URL", k == "LISTEN_ADDR",
			strings.HasPrefix(k, "TLS_"), strings.HasPrefix(k, "ZOOM_"), strings.HasPrefix(k, "ADMIN_"),
			strings.HasPrefix(k, "LDAP_"), strings.HasPrefix(k, "OIDC_"), strings.HasPrefix(k, "LOG_"),
			k == "AUTH_LOCAL_ENABLED", k == "TIMEZONE", k == "BACKUP_KEEP":
			t.Setenv(k, "")
			os.Unsetenv(k)
		}
	}
	for k, v := range vars {
		t.Setenv(k, v)
	}
}

func runCLI(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestVersion(t *testing.T) {
	code, out, _ := runCLI(t, "", "version")
	if code != 0 || strings.TrimSpace(out) == "" {
		t.Fatalf("version: code %d out %q", code, out)
	}
}

func TestUnknownCommand(t *testing.T) {
	code, _, errOut := runCLI(t, "", "frobnicate")
	if code != 2 || !strings.Contains(errOut, "frobnicate") {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
}

// TestSubcommandHelpInRussian: --help of every subcommand is Russian, with the
// flags written as --name and no English left from the flag package.
func TestSubcommandHelpInRussian(t *testing.T) {
	for _, args := range [][]string{
		{"simulate", "--help"}, {"user", "create", "--help"}, {"user", "set-password", "-h"},
		{"check-ldap", "--help"}, {"healthcheck", "--help"},
	} {
		code, _, errOut := runCLI(t, "", args...)
		if code != 0 || !strings.Contains(errOut, "Использование: zoom-checker "+args[0]) || !strings.Contains(errOut, "--config ФАЙЛ") {
			t.Errorf("%v: exit %d\n%s", args, code, errOut)
		}
		for _, english := range []string{"Usage of", "(default", "read the", "print the", "required"} {
			if strings.Contains(errOut, english) {
				t.Errorf("%v: English %q left:\n%s", args, english, errOut)
			}
		}
	}
	_, _, errOut := runCLI(t, "", "simulate", "--help")
	for _, want := range []string{"--insecure", "--meeting ID", "--topic ТЕКСТ", "(по умолчанию: 30)"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("simulate --help lacks %q:\n%s", want, errOut)
		}
	}
	_, _, errOut = runCLI(t, "", "help")
	for _, want := range []string{"--insecure", "--topic", "--host-name"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("help lacks %q", want)
		}
	}
}

func TestCheckConfig(t *testing.T) {
	dir := t.TempDir()
	cliEnv(t, map[string]string{"DATA_DIR": dir, "ZOOM_WEBHOOK_SECRET_TOKEN": "s3cret-value"})

	code, out, errOut := runCLI(t, "", "check-config")
	if code != 0 {
		t.Fatalf("valid config: exit %d\n%s%s", code, out, errOut)
	}
	if !strings.Contains(out, "DATA_DIR") || !strings.Contains(out, "ZOOM_WEBHOOK_SECRET_TOKEN") {
		t.Errorf("summary missing keys:\n%s", out)
	}
	if strings.Contains(out, "s3cret-value") {
		t.Error("check-config printed a secret")
	}
	// Keys are sorted.
	if strings.Index(out, "AUTH_LOCAL_ENABLED") > strings.Index(out, "WEBHOOK_URL") {
		t.Error("keys are not sorted")
	}

	t.Setenv("TLS_MODE", "sometimes")
	code, _, errOut = runCLI(t, "", "check-config")
	if code != 1 || !strings.Contains(errOut, "Configuration is invalid") || !strings.Contains(errOut, "TLS_MODE") {
		t.Fatalf("invalid config: exit %d, stderr %q", code, errOut)
	}
}

func TestCheckConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	cliEnv(t, nil)
	file := filepath.Join(dir, "zc.env")
	os.WriteFile(file, []byte("# test\nPUBLIC_URL=https://att.example.com\nTLS_MODE=off\nDATA_DIR="+dir+"\nZOOM_WEBHOOK_SECRET_TOKEN=x\n"), 0o600)
	// LoadEnvFile sets process variables: register them for cleanup.
	for _, k := range []string{"PUBLIC_URL", "TLS_MODE", "DATA_DIR", "ZOOM_WEBHOOK_SECRET_TOKEN"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	code, out, errOut := runCLI(t, "", "--config", file, "check-config")
	if code != 0 || !strings.Contains(out, "https://att.example.com") {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	// The flag is accepted after the subcommand too.
	code, _, errOut = runCLI(t, "", "check-config", "--config", filepath.Join(dir, "missing.env"))
	if code != 1 || !strings.Contains(errOut, "missing.env") {
		t.Fatalf("missing file: exit %d, %q", code, errOut)
	}
}

func TestHealthURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mode, listen, want string
	}{
		{config.TLSOff, ":8080", "http://127.0.0.1:8080/healthz"},
		{config.TLSOff, "0.0.0.0:9000", "http://127.0.0.1:9000/healthz"},
		{config.TLSOff, "10.1.2.3:8080", "http://10.1.2.3:8080/healthz"},
		{config.TLSOff, "[::]:8080", "http://127.0.0.1:8080/healthz"},
		{config.TLSOff, "8081", "http://127.0.0.1:8081/healthz"},
		{config.TLSFile, ":8443", "https://127.0.0.1:8443/healthz"},
		{config.TLSAuto, ":8080", "http://127.0.0.1/healthz"},
	}
	for _, c := range cases {
		got := healthURL(&config.Config{TLSMode: c.mode, ListenAddr: c.listen})
		if got != c.want {
			t.Errorf("%s %s: got %s want %s", c.mode, c.listen, got, c.want)
		}
	}
}

func TestHealthcheck(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.Write([]byte("ok"))
			return
		}
		http.NotFound(w, r)
	}))
	defer ok.Close()
	cliEnv(t, map[string]string{"LISTEN_ADDR": strings.TrimPrefix(ok.URL, "http://")})
	if code, _, errOut := runCLI(t, "", "healthcheck"); code != 0 {
		t.Fatalf("healthy server: exit %d %s", code, errOut)
	}
	if code, _, _ := runCLI(t, "", "healthcheck", "--url", ok.URL+"/nope"); code != 1 {
		t.Fatalf("404 must be unhealthy, exit %d", code)
	}
	ok.Close()
	if code, _, _ := runCLI(t, "", "healthcheck"); code != 1 {
		t.Fatalf("closed server must be unhealthy, exit %d", code)
	}
}

func openTestStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	d, err := db.Open(context.Background(), "sqlite://"+filepath.ToSlash(filepath.Join(dir, "zoom-checker.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return store.New(d)
}

func TestUserCommands(t *testing.T) {
	dir := t.TempDir()
	cliEnv(t, map[string]string{"DATA_DIR": dir})

	code, out, errOut := runCLI(t, "", "user", "create", "--username", " Teacher1 ", "--role", "teacher", "--display-name", "Мария Ивановна")
	if code != 0 {
		t.Fatalf("create: exit %d %s", code, errOut)
	}
	_, pwLine, ok := strings.Cut(out, "Пароль: ")
	if !ok {
		t.Fatalf("generated password not printed:\n%s", out)
	}
	generated := strings.TrimSpace(strings.SplitN(pwLine, "\n", 2)[0])

	st := openTestStore(t, dir)
	ctx := context.Background()
	u, err := st.GetUserByUsername(ctx, model.AuthLocal, "teacher1")
	if err != nil {
		t.Fatal(err)
	}
	if u.Role != model.RoleTeacher || !u.MustChangePassword || u.DisplayName != "Мария Ивановна" {
		t.Errorf("created user: %+v", u)
	}
	if ok, _ := auth.VerifyPassword(u.PasswordHash, generated); !ok {
		t.Error("printed password does not verify")
	}

	if code, _, errOut := runCLI(t, "", "user", "create", "--username", "teacher1", "--role", "teacher"); code != 1 || !strings.Contains(errOut, "уже существует") {
		t.Errorf("duplicate: exit %d %q", code, errOut)
	}
	if code, _, _ := runCLI(t, "", "user", "create", "--username", "role-test", "--role", "boss"); code != 2 {
		t.Errorf("bad role: exit %d", code)
	}
	if code, _, _ := runCLI(t, "short\n", "user", "create", "--username", "weak-user", "--role", "admin", "--password-stdin"); code != 1 {
		t.Errorf("weak password must be refused, exit %d", code)
	}

	code, _, errOut = runCLI(t, "Correct-Horse-42\n", "user", "set-password", "teacher1", "--password-stdin")
	if code != 0 {
		t.Fatalf("set-password: exit %d %s", code, errOut)
	}
	u, _ = st.GetUserByUsername(ctx, model.AuthLocal, "teacher1")
	if ok, _ := auth.VerifyPassword(u.PasswordHash, "Correct-Horse-42"); !ok || u.MustChangePassword {
		t.Errorf("after set-password: verify=%v must_change=%v", ok, u.MustChangePassword)
	}
	if code, _, _ := runCLI(t, "", "user", "set-password", "nobody"); code != 1 {
		t.Errorf("unknown user: exit %d", code)
	}

	code, out, _ = runCLI(t, "", "user", "list")
	if code != 0 || !strings.Contains(out, "teacher1") || !strings.Contains(out, "Мария Ивановна") {
		t.Fatalf("list: exit %d\n%s", code, out)
	}

	audit, err := st.ListAudit(ctx, store.AuditFilter{EntityType: "user"})
	if err != nil || len(audit) < 2 {
		t.Fatalf("audit entries: %v %d", err, len(audit))
	}

	// backup + migrate on the same database.
	dest := filepath.Join(dir, "copy.db")
	if code, _, errOut := runCLI(t, "", "backup", dest); code != 0 {
		t.Fatalf("backup: exit %d %s", code, errOut)
	}
	if fi, err := os.Stat(dest); err != nil || fi.Size() == 0 {
		t.Fatalf("backup file: %v", err)
	}
	if code, out, errOut := runCLI(t, "", "migrate"); code != 0 || !strings.Contains(out, "версия схемы") {
		t.Fatalf("migrate: exit %d %s %s", code, out, errOut)
	}
}

func TestUserCreateLoginRules(t *testing.T) {
	dir := t.TempDir()
	cliEnv(t, map[string]string{"DATA_DIR": dir})

	// The same login rule as the admin API.
	for _, login := range []string{"иванов", "a", "bad/name"} {
		if code, _, errOut := runCLI(t, "", "user", "create", "--username", login, "--role", "teacher"); code != 2 || !strings.Contains(errOut, "латинские буквы") {
			t.Errorf("login %q: exit %d %q", login, code, errOut)
		}
	}
	if code, _, _ := runCLI(t, "", "user", "create", "--username", "has space", "--role", "teacher"); code == 0 {
		t.Error("login with a space accepted")
	}

	// A local account must not shadow a directory (LDAP) account.
	if code, _, errOut := runCLI(t, "", "migrate"); code != 0 {
		t.Fatalf("migrate: %s", errOut)
	}
	st := openTestStore(t, dir)
	ctx := context.Background()
	if err := st.CreateUser(ctx, &model.User{Username: "ivanov", Role: model.RoleTeacher, AuthSource: model.AuthLDAP, ExternalID: "guid-1"}); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runCLI(t, "", "user", "create", "--username", "Ivanov", "--role", "admin")
	if code != 1 || !strings.Contains(errOut, "LDAP") {
		t.Fatalf("LDAP login shadowed: exit %d %q", code, errOut)
	}
	if _, err := st.GetUserByUsername(ctx, model.AuthLocal, "ivanov"); err == nil {
		t.Fatal("local account created over the LDAP one")
	}
	// set-password stays lenient: it must reach any existing local account.
	if err := st.CreateUser(ctx, &model.User{Username: "админ", Role: model.RoleAdmin, AuthSource: model.AuthLocal, PasswordHash: "x"}); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runCLI(t, "", "user", "set-password", "Админ", "--generate"); code != 0 {
		t.Fatalf("set-password for a legacy login: exit %d %s", code, errOut)
	}
}

func TestCheckConfigWarnsShortRetention(t *testing.T) {
	cliEnv(t, map[string]string{"DATA_DIR": t.TempDir(), "ZOOM_WEBHOOK_SECRET_TOKEN": "x", "EVENTS_RETENTION_DAYS": "7"})
	code, _, errOut := runCLI(t, "", "check-config")
	if code != 0 || !strings.Contains(errOut, "EVENTS_RETENTION_DAYS=7") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
}

func TestUserCommandsNeedExistingDatabase(t *testing.T) {
	dir := t.TempDir()
	cliEnv(t, map[string]string{"DATA_DIR": filepath.Join(dir, "wrong-place")})
	for _, args := range [][]string{{"user", "list"}, {"user", "set-password", "admin", "--generate"}} {
		code, out, errOut := runCLI(t, "", args...)
		if code != 1 || !strings.Contains(errOut, "не найдена") || strings.Contains(out, "Пароль") {
			t.Errorf("%v on a missing database: exit %d, out %q, stderr %q", args, code, out, errOut)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "wrong-place")); err == nil {
		t.Error("an empty database was created in the wrong place")
	}
}

func TestUnreadableSystemConfigHint(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read any file")
	}
	file := filepath.Join(t.TempDir(), "zoom-checker.env")
	if err := os.WriteFile(file, []byte("PUBLIC_URL=https://att.example.com\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	prev := systemConfigFile
	systemConfigFile = file
	t.Cleanup(func() { systemConfigFile = prev })
	cliEnv(t, map[string]string{"DATA_DIR": t.TempDir()})
	_, _, errOut := runCLI(t, "", "check-config")
	if !strings.Contains(errOut, "нельзя прочитать") || !strings.Contains(errOut, "sudo") {
		t.Fatalf("no hint about the unreadable settings file: %q", errOut)
	}
}

func TestBackupRefusesMissingDatabase(t *testing.T) {
	dir := t.TempDir()
	cliEnv(t, map[string]string{"DATA_DIR": filepath.Join(dir, "nothing-here")})
	code, _, errOut := runCLI(t, "", "backup", filepath.Join(dir, "x.db"))
	if code != 1 || !strings.Contains(errOut, "не найдена") {
		t.Fatalf("exit %d %q", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(dir, "nothing-here")); err == nil {
		t.Error("backup created a database directory")
	}
}

func TestCheckLDAPDisabled(t *testing.T) {
	cliEnv(t, map[string]string{"DATA_DIR": t.TempDir()})
	if code, _, errOut := runCLI(t, "", "check-ldap"); code != 1 || !strings.Contains(errOut, "LDAP") {
		t.Fatalf("exit %d %q", code, errOut)
	}
	if code, _, errOut := runCLI(t, "", "check-oidc"); code != 1 || !strings.Contains(errOut, "OIDC") {
		t.Fatalf("exit %d %q", code, errOut)
	}
}
