package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"zoom-checker/internal/auth"
	"zoom-checker/internal/config"
	"zoom-checker/internal/db"
	"zoom-checker/internal/server"
)

func (a *app) cmdCheckConfig(args []string) error {
	fs := a.newFlagSet("check-config")
	if err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := a.loadConfig(false)
	if err != nil {
		a.printConfigErrors(err)
		return errSilent
	}
	red := cfg.Redacted()
	keys := make([]string, 0, len(red))
	for k := range red {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
	for _, k := range keys {
		fmt.Fprintf(tw, "%s\t%s\n", k, red[k])
	}
	_ = tw.Flush()

	problems, warnings := checkEnvironment(cfg)
	for _, w := range warnings {
		fmt.Fprintln(a.stderr, "  ! "+w)
	}
	if len(problems) > 0 {
		fmt.Fprintln(a.stderr, "Настройки содержат ошибки / Configuration is invalid:")
		for _, p := range problems {
			fmt.Fprintln(a.stderr, "  ✘ "+p)
		}
		return errSilent
	}
	fmt.Fprintln(a.stdout, "Настройки в порядке / Configuration is valid.")
	return nil
}

// checkEnvironment performs checks that need the file system but no network:
// DATA_DIR writable, certificate files loadable, CA bundles readable.
func checkEnvironment(cfg *config.Config) (problems, warnings []string) {
	if st, err := os.Stat(cfg.DataDir); err == nil {
		if !st.IsDir() {
			problems = append(problems, fmt.Sprintf("DATA_DIR: %s — это не папка", cfg.DataDir))
		} else if f, err := os.CreateTemp(cfg.DataDir, ".write-test-*"); err != nil {
			problems = append(problems, fmt.Sprintf("DATA_DIR: в папку %s нельзя записывать от этого пользователя: %v", cfg.DataDir, err))
		} else {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	} else {
		warnings = append(warnings, fmt.Sprintf("DATA_DIR: папки %s пока нет — сервис создаст её при запуске", cfg.DataDir))
	}
	if cfg.TLSMode == config.TLSFile {
		if _, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile); err != nil {
			problems = append(problems, fmt.Sprintf("TLS_CERT_FILE/TLS_KEY_FILE: не удалось прочитать сертификат или ключ: %v", err))
		}
	}
	if cfg.LDAP.Enabled() {
		if _, err := auth.NewLDAP(cfg.LDAP, discardLogger()); err != nil {
			problems = append(problems, fmt.Sprintf("LDAP: %v", err))
		}
	}
	if cfg.OIDC.Enabled() {
		if _, err := auth.NewOIDC(cfg.OIDC, cfg.PublicURL, make([]byte, 32), discardLogger()); err != nil {
			problems = append(problems, fmt.Sprintf("OIDC: %v", err))
		}
	}
	if cfg.ZoomWebhookSecret == "" {
		warnings = append(warnings, "ZOOM_WEBHOOK_SECRET_TOKEN не задан: события от Zoom не принимаются, пока его не добавить")
	}
	if d := cfg.EventsRetentionDays; d > 0 && d < server.MinEventsRetentionDays {
		warnings = append(warnings, fmt.Sprintf("EVENTS_RETENTION_DAYS=%d — слишком мало: события Zoom хранятся не меньше %d дней, чтобы недавние конференции можно было привязать к занятиям",
			d, server.MinEventsRetentionDays))
	}
	if !cfg.PublicHTTPS() && !isLocalHost(cfg.PublicURL.Hostname()) {
		warnings = append(warnings, "PUBLIC_URL не https: Zoom присылает события только на адреса https://")
	} else if ip := net.ParseIP(cfg.PublicURL.Hostname()); ip != nil && !ip.IsLoopback() {
		warnings = append(warnings, "PUBLIC_URL — IP-адрес: для проверки сервиса этого хватит, но Zoom принимает адрес для событий только с доменным именем (например https://attendance.example.com). Заведите DNS-запись и укажите домен в PUBLIC_URL")
	}
	if cfg.AdminPassword != "" && cfg.LocalAuthEnabled {
		if err := auth.ValidateNewPassword(cfg.AdminPassword); err != nil {
			warnings = append(warnings, "ADMIN_PASSWORD слишком простой ("+strings.TrimSuffix(err.Error(), ".")+
				") — при первом входе сервис попросит его сменить. После первого входа уберите ADMIN_PASSWORD из файла настроек.")
		}
	}
	if w := proxyWithoutTrustWarning(cfg); w != "" {
		warnings = append(warnings, w)
	}
	if cfg.LDAP.Enabled() {
		warnings = append(warnings, bareGroupWarnings(cfg.LDAP.AdminGroups)...)
	}
	if w := filePortWarning(cfg); w != "" {
		warnings = append(warnings, w)
	}
	return problems, warnings
}

// filePortWarning: with TLS_MODE=file the service itself answers HTTPS on
// LISTEN_ADDR (default :8080), while PUBLIC_URL usually has no port (443).
// Without a port forward in between, browsers and Zoom then reach nothing.
func filePortWarning(cfg *config.Config) string {
	if cfg.TLSMode != config.TLSFile {
		return ""
	}
	_, listen, err := net.SplitHostPort(cfg.ListenAddr)
	if err != nil {
		return ""
	}
	public := cfg.PublicURL.Port()
	if public == "" {
		public = "443"
		if cfg.PublicURL.Scheme == "http" {
			public = "80"
		}
	}
	if listen == public {
		return ""
	}
	return fmt.Sprintf("TLS_MODE=file: сервис слушает порт %s (LISTEN_ADDR), а адрес в PUBLIC_URL ведёт на порт %s. "+
		"Если между ними нет перенаправления портов, укажите LISTEN_ADDR=:%s", listen, public, public)
}

// bareGroupWarnings flags LDAP_ADMIN_GROUPS entries that are a bare group
// name: such a rule matches a group with that name anywhere in the
// directory, so whoever may create groups in any OU could make themselves an
// administrator. A full DN pins the one group meant.
func bareGroupWarnings(groups []string) []string {
	var out []string
	for _, g := range groups {
		if g = strings.TrimSpace(g); g != "" && !strings.Contains(g, "=") {
			out = append(out, fmt.Sprintf("LDAP_ADMIN_GROUPS: «%s» — только имя группы, поэтому подойдёт любая группа с таким именем в любом месте каталога. "+
				"Надёжнее указать полный DN, например CN=%s,OU=Groups,DC=corp,DC=example,DC=com (его показывает check-ldap --user ЛОГИН)", g, g))
		}
	}
	return out
}

// proxyWithoutTrustWarning warns when HTTPS is evidently terminated by a
// reverse proxy (PUBLIC_URL https, TLS_MODE=off) but TRUSTED_PROXIES is empty:
// every user then has the proxy's address, shares one login-attempt budget
// and can be locked out by anybody else's failed logins.
func proxyWithoutTrustWarning(cfg *config.Config) string {
	if cfg.TLSMode != config.TLSOff || !cfg.PublicHTTPS() || len(cfg.TrustedProxies) > 0 {
		return ""
	}
	return server.ProxyWithoutTrustMessage
}

func isLocalHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func (a *app) cmdMigrate(ctx context.Context, args []string) error {
	fs := a.newFlagSet("migrate")
	if err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := a.loadValidConfig(false)
	if err != nil {
		return err
	}
	a.cliLogger(cfg)
	d, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeDB(d)
	if err := d.Migrate(ctx); err != nil {
		return fmt.Errorf("миграции не применены: %w", err)
	}
	v, err := d.MigrationVersion(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "База данных (%s) обновлена, версия схемы: %d\n", d.Dialect, v)
	return nil
}

// openDB opens the configured database (a missing SQLite file is created).
func openDB(ctx context.Context, cfg *config.Config) (*db.DB, error) {
	d, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть базу данных: %w", err)
	}
	return d, nil
}

// closeDB closes d and, when the command ran as root, hands files SQLite
// created back to the owner of the data directory (the service user).
func closeDB(d *db.DB) {
	_ = d.Close()
	fixOwnership(d.SQLitePath())
}

// sqliteFile returns the file path of a file-backed SQLite DATABASE_URL ("" otherwise).
func sqliteFile(cfg *config.Config) string {
	if cfg.DBDriver() != "sqlite" {
		return ""
	}
	rest := strings.TrimPrefix(cfg.DatabaseURL, "sqlite:")
	if rest == ":memory:" || rest == "//:memory:" {
		return ""
	}
	return filepath.FromSlash(strings.TrimPrefix(rest, "//"))
}

// requireExistingDB fails when the SQLite file does not exist yet (a command
// that only reads would otherwise silently create an empty database).
func requireExistingDB(cfg *config.Config) error {
	p := sqliteFile(cfg)
	if p == "" {
		return nil
	}
	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("база данных %s не найдена (проверьте DATA_DIR / DATABASE_URL или укажите --config)", p)
	}
	return nil
}

func (a *app) cmdBackup(ctx context.Context, args []string) error {
	fs := a.newFlagSet("backup")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return usageErr("Укажите файл для резервной копии: zoom-checker backup FILE")
	}
	dest := fs.Arg(0)
	cfg, err := a.loadValidConfig(false)
	if err != nil {
		return err
	}
	a.cliLogger(cfg)
	if cfg.DBDriver() == "postgres" {
		fmt.Fprintln(a.stderr, "Для PostgreSQL резервную копию делает pg_dump, например:")
		fmt.Fprintf(a.stderr, "  pg_dump --format=custom --file=%s '%s'\n", dest, cfg.Redacted()["DATABASE_URL"])
		return errSilent
	}
	if err := requireExistingDB(cfg); err != nil {
		return err
	}
	d, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeDB(d)
	if err := d.Backup(ctx, dest); err != nil {
		if errors.Is(err, db.ErrBackupUnsupported) {
			return errors.New("для этой базы данных резервное копирование изнутри не поддерживается; используйте pg_dump")
		}
		return fmt.Errorf("резервная копия не создана: %w", err)
	}
	fmt.Fprintf(a.stdout, "Резервная копия сохранена: %s\n", dest)
	return nil
}

// healthURL derives the local /healthz address from the TLS mode: port 80 in
// auto mode (it serves /healthz itself), LISTEN_ADDR otherwise (https in file mode).
func healthURL(cfg *config.Config) string {
	if cfg.TLSMode == config.TLSAuto {
		return "http://127.0.0.1/healthz"
	}
	host, port, err := net.SplitHostPort(cfg.ListenAddr)
	if err != nil {
		host, port = "", strings.TrimPrefix(cfg.ListenAddr, ":")
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	if port == "" {
		port = "8080"
	}
	scheme := "http"
	if cfg.TLSMode == config.TLSFile {
		scheme = "https"
	}
	return (&url.URL{Scheme: scheme, Host: net.JoinHostPort(host, port), Path: "/healthz"}).String()
}

func (a *app) cmdHealthcheck(ctx context.Context, args []string) error {
	fs := a.newFlagSet("healthcheck")
	timeout := fs.Duration("timeout", 5*time.Second, "сколько ждать ответа")
	urlFlag := fs.String("url", "", "`URL` для проверки (по умолчанию — по TLS_MODE и LISTEN_ADDR)")
	if err := parse(fs, args); err != nil {
		return err
	}
	target := *urlFlag
	if target == "" {
		cfg, err := a.loadConfig(true)
		if err != nil {
			a.printConfigErrors(err)
			return errSilent
		}
		target = healthURL(cfg)
	}
	client := &http.Client{
		Timeout: *timeout,
		Transport: &http.Transport{
			Proxy: nil,
			// Loopback probe of our own listener; in TLS file mode the
			// certificate is issued for the public name, not 127.0.0.1.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(a.stderr, "unhealthy: %s: %v\n", target, err)
		return errSilent
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(a.stderr, "unhealthy: %s answered %s\n", target, resp.Status)
		return errSilent
	}
	fmt.Fprintln(a.stdout, "ok")
	return nil
}

func (a *app) cmdCheckLDAP(ctx context.Context, args []string) error {
	fs := a.newFlagSet("check-ldap")
	user := fs.String("user", "", "`ЛОГИН` для поиска в каталоге (с --password-stdin — и для проверки пароля)")
	pwStdin := fs.Bool("password-stdin", false, "прочитать пароль пользователя из первой строки стандартного ввода")
	if err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := a.loadValidConfig(false)
	if err != nil {
		return err
	}
	log := a.cliLogger(cfg)
	if !cfg.LDAP.Enabled() {
		fmt.Fprintln(a.stderr, "Вход через LDAP не настроен: задайте LDAP_URL и другие параметры LDAP_*.")
		return errSilent
	}
	password := ""
	if *pwStdin {
		if *user == "" {
			return usageErr("--password-stdin требует --user")
		}
		if password, err = readPasswordLine(a.stdin); err != nil {
			return err
		}
	}
	l, err := auth.NewLDAP(cfg.LDAP, log)
	if err != nil {
		return fmt.Errorf("настройки LDAP: %w", err)
	}
	fmt.Fprintf(a.stdout, "Проверка LDAP: %s\n", cfg.LDAP.URL)
	ctx, cancel := context.WithTimeout(ctx, 4*cfg.LDAP.Timeout+10*time.Second)
	defer cancel()
	err = a.printDiag(l.Diagnose(ctx, *user, password))
	if *user != "" && !*pwStdin {
		// Without this line a green run reads as "the login works", although
		// only the account lookup was checked.
		fmt.Fprintf(a.stdout, "ℹ Пароль не проверялся. Чтобы проверить и его, запустите "+
			"zoom-checker check-ldap --user %s --password-stdin и передайте пароль одной строкой на стандартный ввод.\n", *user)
	}
	return err
}

func (a *app) cmdCheckOIDC(ctx context.Context, args []string) error {
	fs := a.newFlagSet("check-oidc")
	if err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := a.loadValidConfig(false)
	if err != nil {
		return err
	}
	log := a.cliLogger(cfg)
	if !cfg.OIDC.Enabled() {
		fmt.Fprintln(a.stderr, "Вход через Keycloak (OIDC) не настроен: задайте OIDC_ISSUER_URL, OIDC_CLIENT_ID и OIDC_CLIENT_SECRET.")
		return errSilent
	}
	// The flow-cookie key is irrelevant for diagnostics; use a throwaway one
	// instead of opening the database.
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	o, err := auth.NewOIDC(cfg.OIDC, cfg.PublicURL, key, log)
	if err != nil {
		return fmt.Errorf("настройки OIDC: %w", err)
	}
	fmt.Fprintf(a.stdout, "Проверка OIDC: %s\nАдрес возврата (Valid redirect URI в Keycloak): %s\n", cfg.OIDC.IssuerURL, o.RedirectURL())
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return a.printDiag(o.Diagnose(ctx))
}

// printDiag prints diagnostic steps with ✔/✘ and fails when any step failed.
func (a *app) printDiag(steps []auth.DiagStep) error {
	failed := false
	for _, s := range steps {
		mark := "✔"
		if !s.OK {
			mark, failed = "✘", true
		}
		line := mark + " " + s.Name
		if s.Detail != "" {
			line += " — " + s.Detail
		}
		fmt.Fprintln(a.stdout, line)
	}
	if failed {
		return errSilent
	}
	return nil
}

// readPasswordLine reads one line from r (for --password-stdin) without the
// line ending. Leading/trailing spaces are part of the password.
func readPasswordLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("не удалось прочитать пароль: %w", err)
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", errors.New("пароль не передан: ожидалась строка с паролем на стандартном вводе")
	}
	return line, nil
}
