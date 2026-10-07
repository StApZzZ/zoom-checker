// Command zoom-checker is the Zoom attendance service and its admin CLI
// (docs/architecture.md §9):
//
//	zoom-checker [--config FILE] [serve]      start the server (default)
//	zoom-checker migrate                      apply database migrations
//	zoom-checker check-config                 validate settings, print a redacted summary
//	zoom-checker version
//	zoom-checker healthcheck                  probe /healthz (Docker HEALTHCHECK)
//	zoom-checker user list|create|set-password
//	zoom-checker check-ldap [--user U] [--password-stdin]
//	zoom-checker check-oidc
//	zoom-checker backup FILE
//	zoom-checker simulate --meeting ID ...    send signed demo Zoom webhooks
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	_ "time/tzdata" // TIMEZONE must work in distroless images without zoneinfo

	"zoom-checker/internal/config"
	"zoom-checker/internal/server"
	"zoom-checker/internal/version"
)

// Locations used by install.sh and the systemd unit. A CLI command run by an
// administrator without --config picks the settings file up from there, and
// DATA_DIR (set by the unit, not by the file) defaults to the unit's value.
// Variables (not constants) so tests can point them elsewhere.
var (
	systemConfigFile = "/etc/zoom-checker/zoom-checker.env"
	systemDataDir    = "/var/lib/zoom-checker"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// app carries the I/O streams and global flags of one CLI invocation.
type app struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	configPath     string
	configSet      bool // --config or CONFIG_FILE given explicitly
}

// usageError is a command-line mistake (exit code 2). An empty message means
// the flag package has already printed the problem.
type usageError string

func (e usageError) Error() string { return string(e) }

func usageErr(format string, args ...any) error { return usageError(fmt.Sprintf(format, args...)) }

// errSilent means the command already printed its failure; exit code 1.
var errSilent = errors.New("failed")

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	a := &app{stdin: stdin, stdout: stdout, stderr: stderr}
	global := flag.NewFlagSet("zoom-checker", flag.ContinueOnError)
	global.SetOutput(stderr)
	global.StringVar(&a.configPath, "config", os.Getenv("CONFIG_FILE"), "`ФАЙЛ` настроек (строки КЛЮЧ=значение); или переменная CONFIG_FILE")
	global.Usage = func() { a.usage() }
	if err := global.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	a.configSet = a.configPath != ""
	rest := global.Args()
	cmd := "serve"
	if len(rest) > 0 {
		cmd, rest = rest[0], rest[1:]
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// After the first signal restore the default handling, so a second Ctrl+C
	// ends the process at once instead of waiting for the graceful shutdown.
	context.AfterFunc(ctx, stop)

	var err error
	switch cmd {
	case "serve":
		err = a.cmdServe(ctx, rest)
	case "migrate":
		err = a.cmdMigrate(ctx, rest)
	case "check-config":
		err = a.cmdCheckConfig(rest)
	case "version", "--version", "-version":
		fmt.Fprintln(stdout, version.String())
	case "healthcheck":
		err = a.cmdHealthcheck(ctx, rest)
	case "user", "users":
		err = a.cmdUser(ctx, rest)
	case "check-ldap":
		err = a.cmdCheckLDAP(ctx, rest)
	case "check-oidc":
		err = a.cmdCheckOIDC(ctx, rest)
	case "backup":
		err = a.cmdBackup(ctx, rest)
	case "simulate":
		err = a.cmdSimulate(ctx, rest)
	case "help", "-h", "--help":
		a.usage()
	default:
		fmt.Fprintf(stderr, "Неизвестная команда %q.\n\n", cmd)
		a.usage()
		return 2
	}
	var ue usageError
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return 0
	case errors.As(err, &ue):
		if ue != "" {
			fmt.Fprintln(stderr, string(ue))
		}
		fmt.Fprintln(stderr, "Справка: zoom-checker help")
		return 2
	case errors.Is(err, errSilent):
		return 1
	default:
		fmt.Fprintln(stderr, "Ошибка:", err)
		return 1
	}
}

func (a *app) usage() {
	fmt.Fprint(a.stderr, `Zoom Checker `+version.String()+` — учёт посещаемости Zoom-занятий

Использование: zoom-checker [--config FILE] <команда> [параметры]

Команды:
  serve                      запустить сервер (по умолчанию)
  migrate                    применить миграции базы данных и выйти
  check-config               проверить настройки и показать их (без секретов)
  version                    показать версию
  healthcheck                проверить, что сервер отвечает (для Docker HEALTHCHECK)
  user list                  список пользователей
  user create --username U --role admin|teacher [--display-name N] [--password-stdin | --generate]
  user set-password U [--password-stdin | --generate]
  check-ldap [--user U] [--password-stdin]   проверить подключение к LDAP / Active Directory
  check-oidc                 проверить настройки входа через Keycloak (OIDC)
  backup FILE                резервная копия базы SQLite в FILE
  simulate --meeting ID [--url URL] [--secret S] [--insecure] [--names FILE] [--duration 90m]
                             [--speed 30] [--instant] [--seed N] [--no-end] [--dry-run]
                             [--topic ТЕМА] [--host-name ИМЯ]
                             отправить подписанные демонстрационные webhook-события Zoom
                             (подробно: zoom-checker simulate --help)

--config FILE (или переменная CONFIG_FILE) — файл настроек KEY=VALUE; переменные окружения важнее файла.
`)
}

// newFlagSet creates a subcommand flag set that also accepts --config, so
// "zoom-checker check-config --config FILE" works as well. Its --help is in
// Russian, like the rest of the CLI.
func (a *app) newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("zoom-checker "+name, flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	fs.Func("config", "`ФАЙЛ` настроек (строки КЛЮЧ=значение)", func(v string) error {
		a.configPath, a.configSet = v, true
		return nil
	})
	fs.Usage = func() { printFlagUsage(fs) }
	return fs
}

// printFlagUsage is a Russian flag.PrintDefaults: "--name ЗНАЧЕНИЕ", the
// help text and "(по умолчанию: X)" instead of the flag package's English.
func printFlagUsage(fs *flag.FlagSet) {
	out := fs.Output()
	fmt.Fprintf(out, "Использование: %s [параметры]\n\nПараметры:\n", fs.Name())
	fs.VisitAll(func(f *flag.Flag) {
		placeholder, usage := flag.UnquoteUsage(f)
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			placeholder = ""
		} else if ru, ok := flagPlaceholders[placeholder]; ok {
			placeholder = ru // a type name from the flag package ("string", "duration"…)
		}
		line := "  --" + f.Name
		if placeholder != "" {
			line += " " + placeholder
		}
		fmt.Fprintln(out, line)
		if def := f.DefValue; def != "" && def != "0" && def != "false" && !strings.Contains(usage, "по умолчанию") {
			usage += " (по умолчанию: " + def + ")"
		}
		fmt.Fprintln(out, "        "+usage)
	})
}

// flagPlaceholders translate the placeholders flag.UnquoteUsage derives from
// a flag's type when its help has no `ИМЯ` in back quotes.
var flagPlaceholders = map[string]string{
	"string": "ТЕКСТ", "value": "ЗНАЧЕНИЕ", "duration": "ВРЕМЯ",
	"int": "ЧИСЛО", "uint": "ЧИСЛО", "float": "ЧИСЛО",
}

// parse parses subcommand flags; mistakes become a usageError (the flag
// package has printed them already).
func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError("")
	}
	return nil
}

// loadConfig reads the optional settings file and then the environment.
// forServer=false enables the administrator conveniences for the systemd
// layout (see systemConfigFile).
func (a *app) loadConfig(forServer bool) (*config.Config, error) {
	path := a.configPath
	if path == "" && !forServer {
		if fileReadable(systemConfigFile) {
			path = systemConfigFile
			fmt.Fprintf(a.stderr, "Настройки: %s\n", path)
		} else if _, err := os.Stat(systemConfigFile); err == nil {
			// Installed by install.sh (root:zoom-checker 0640) but run as another
			// user: without this hint the command would quietly work on ./data.
			fmt.Fprintf(a.stderr, "Внимание: файл настроек %s нельзя прочитать от этого пользователя — запустите команду через sudo или укажите --config.\n", systemConfigFile)
		}
	}
	if path != "" {
		if err := config.LoadEnvFile(path); err != nil {
			return nil, fmt.Errorf("не удалось прочитать файл настроек %s: %w", path, err)
		}
		if !forServer && os.Getenv("DATA_DIR") == "" && sameFile(path, systemConfigFile) && dirExists(systemDataDir) {
			_ = os.Setenv("DATA_DIR", systemDataDir)
		}
	}
	return config.Load(os.Getenv)
}

// loadValidConfig is loadConfig that prints validation errors as a list.
func (a *app) loadValidConfig(forServer bool) (*config.Config, error) {
	cfg, err := a.loadConfig(forServer)
	if err != nil {
		a.printConfigErrors(err)
		return nil, errSilent
	}
	return cfg, nil
}

func (a *app) printConfigErrors(err error) {
	fmt.Fprintln(a.stderr, "Настройки содержат ошибки / Configuration is invalid:")
	for _, line := range strings.Split(err.Error(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			fmt.Fprintln(a.stderr, "  ✘ "+line)
		}
	}
}

// cliLogger is the logger of administrative commands: warnings and errors only
// unless LOG_LEVEL=debug, so command output stays readable.
func (a *app) cliLogger(cfg *config.Config) *slog.Logger {
	level := "warn"
	if cfg != nil && cfg.LogLevel == "debug" {
		level = "debug"
	}
	format := "text"
	if cfg != nil {
		format = cfg.LogFormat
	}
	log := server.NewLogger(level, format, a.stderr)
	slog.SetDefault(log)
	return log
}

func (a *app) cmdServe(ctx context.Context, args []string) error {
	fs := a.newFlagSet("serve")
	if err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := a.loadValidConfig(true)
	if err != nil {
		return err
	}
	log := server.NewLogger(cfg.LogLevel, cfg.LogFormat, a.stderr)
	slog.SetDefault(log)
	limit := server.ApplySoftMemoryLimit()
	log.Debug("go runtime memory limit", "bytes", limit)
	if err := server.Run(ctx, cfg, log); err != nil {
		log.Error("server stopped with an error", "err", err)
		return errSilent
	}
	return nil
}

func fileReadable(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func sameFile(a, b string) bool {
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	if err1 != nil || err2 != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return os.SameFile(sa, sb)
}
