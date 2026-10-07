package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"unicode"

	"zoom-checker/internal/auth"
	"zoom-checker/internal/config"
	"zoom-checker/internal/model"
	"zoom-checker/internal/server"
	"zoom-checker/internal/store"
)

// cliAuditName is the audit author of changes made from the command line.
const cliAuditName = "командная строка"

func (a *app) cmdUser(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageErr("Укажите действие: user list | user create ... | user set-password LOGIN ...")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list", "ls":
		return a.userList(ctx, rest)
	case "create", "add":
		return a.userCreate(ctx, rest)
	case "set-password", "passwd":
		return a.userSetPassword(ctx, rest)
	default:
		return usageErr("Неизвестное действие %q: используйте list, create или set-password", sub)
	}
}

// openStore loads the configuration, opens and migrates the database. With
// mustExist a missing SQLite file is an error instead of a new empty database
// (listing users or resetting a password of a database that is not there
// means the settings point to the wrong place).
func (a *app) openStore(ctx context.Context, mustExist bool) (*store.Store, *config.Config, func(), error) {
	cfg, err := a.loadValidConfig(false)
	if err != nil {
		return nil, nil, nil, err
	}
	a.cliLogger(cfg)
	if mustExist {
		if err := requireExistingDB(cfg); err != nil {
			return nil, nil, nil, err
		}
	}
	d, err := openDB(ctx, cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := d.Migrate(ctx); err != nil {
		closeDB(d)
		return nil, nil, nil, fmt.Errorf("миграции не применены: %w", err)
	}
	return store.New(d), cfg, func() { closeDB(d) }, nil
}

func (a *app) userList(ctx context.Context, args []string) error {
	fs := a.newFlagSet("user list")
	if err := parse(fs, args); err != nil {
		return err
	}
	st, cfg, done, err := a.openStore(ctx, true)
	if err != nil {
		return err
	}
	defer done()
	users, err := st.ListUsers(ctx)
	if err != nil {
		return err
	}
	if len(users) == 0 {
		fmt.Fprintln(a.stdout, "Пользователей пока нет.")
		return nil
	}
	tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tЛОГИН\tРОЛЬ\tВХОД ЧЕРЕЗ\tСОСТОЯНИЕ\tПОСЛЕДНИЙ ВХОД\tИМЯ")
	for _, u := range users {
		state := "активен"
		switch {
		case u.Disabled:
			state = "отключён"
		case u.MustChangePassword:
			state = "нужно сменить пароль"
		}
		last := "—"
		if u.LastLoginAt != nil {
			last = u.LastLoginAt.In(cfg.Timezone).Format("2006-01-02 15:04")
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", u.ID, u.Username, u.Role, u.AuthSource, state, last, u.DisplayName)
	}
	return tw.Flush()
}

// passwordSource handles the mutually exclusive --password-stdin / --generate flags.
type passwordSource struct {
	stdin    *bool
	generate *bool
}

func addPasswordFlags(fs *flag.FlagSet) passwordSource {
	return passwordSource{
		stdin:    fs.Bool("password-stdin", false, "прочитать пароль из первой строки стандартного ввода"),
		generate: fs.Bool("generate", false, "придумать случайный пароль и показать его один раз (так по умолчанию)"),
	}
}

// get returns the password and whether it was generated. Without flags a
// password is generated: typing one on the command line would leave it in the
// shell history.
func (p passwordSource) get(r io.Reader) (string, bool, error) {
	if *p.stdin && *p.generate {
		return "", false, usageErr("Используйте либо --password-stdin, либо --generate")
	}
	if *p.stdin {
		pw, err := readPasswordLine(r)
		if err != nil {
			return "", false, err
		}
		if err := auth.ValidateNewPassword(pw); err != nil {
			return "", false, err
		}
		return pw, false, nil
	}
	return auth.GeneratePassword(), true, nil
}

// newUsernamePattern is the form of new local logins, the same rule as the
// admin API (POST /api/users), so accounts made here look like the others.
var newUsernamePattern = regexp.MustCompile(`^[a-z0-9._@-]{2,64}$`)

// validateNewUsername normalizes and checks the login of a new local account.
func validateNewUsername(s string) (string, error) {
	u, err := normalizeUsername(s)
	if err != nil {
		return "", err
	}
	if !newUsernamePattern.MatchString(u) {
		return "", usageErr("Логин: от 2 до 64 символов — латинские буквы, цифры и знаки . _ @ -")
	}
	return u, nil
}

// normalizeUsername lower-cases and sanity-checks a local login name. It is
// lenient on purpose: set-password must also reach accounts created before the
// login rules existed (for example ADMIN_USERNAME).
func normalizeUsername(s string) (string, error) {
	u := strings.ToLower(strings.TrimSpace(s))
	if u == "" {
		return "", usageErr("Укажите логин: --username LOGIN")
	}
	if len([]rune(u)) > 64 {
		return "", errors.New("логин слишком длинный: не больше 64 символов")
	}
	for _, r := range u {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", errors.New("логин не должен содержать пробелов")
		}
	}
	return u, nil
}

func (a *app) userCreate(ctx context.Context, args []string) error {
	fs := a.newFlagSet("user create")
	username := fs.String("username", "", "`ЛОГИН` пользователя (обязательно)")
	role := fs.String("role", "", "`РОЛЬ` — admin или teacher (обязательно)")
	display := fs.String("display-name", "", "`ФИО`, которое видно в интерфейсе")
	mustChange := fs.Bool("must-change", false, "потребовать сменить пароль при первом входе (с придуманным паролем — всегда)")
	pw := addPasswordFlags(fs)
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageErr("Лишние аргументы: %s", strings.Join(fs.Args(), " "))
	}
	name, err := validateNewUsername(*username)
	if err != nil {
		return err
	}
	r := model.Role(strings.ToLower(strings.TrimSpace(*role)))
	if r != model.RoleAdmin && r != model.RoleTeacher {
		return usageErr("Укажите роль: --role admin или --role teacher")
	}
	password, generated, err := pw.get(a.stdin)
	if err != nil {
		return err
	}
	st, cfg, done, err := a.openStore(ctx, false)
	if err != nil {
		return err
	}
	defer done()

	// A local account shadows a directory account with the same login (the
	// login form checks local accounts first), as the admin API refuses too.
	if _, err := st.GetUserByUsername(ctx, model.AuthLDAP, name); err == nil {
		return fmt.Errorf("логин %q уже используется для входа через LDAP; выберите другой", name)
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	u := &model.User{
		Username:           name,
		DisplayName:        strings.TrimSpace(*display),
		Role:               r,
		AuthSource:         model.AuthLocal,
		PasswordHash:       hash,
		MustChangePassword: generated || *mustChange,
	}
	err = st.InTx(ctx, func(tx *store.Store) error {
		if err := tx.CreateUser(ctx, u); err != nil {
			return err
		}
		return tx.InsertAudit(ctx, &model.AuditEntry{
			UserName: cliAuditName, Action: model.AuditUserCreate, EntityType: "user",
			EntityID: strconv.FormatInt(u.ID, 10), NewValue: describeUser(u),
		})
	})
	if errors.Is(err, store.ErrConflict) {
		return fmt.Errorf("пользователь с логином %q уже существует", name)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "Пользователь %s создан (роль: %s).\n", name, roleRU(r))
	if generated {
		fmt.Fprintf(a.stdout, "Пароль: %s\n(при первом входе будет предложено сменить пароль; сохраните его сейчас — он больше не будет показан)\n", password)
	}
	if !cfg.LocalAuthEnabled {
		fmt.Fprintln(a.stdout, "Внимание: вход по логину и паролю выключен (AUTH_LOCAL_ENABLED=false) — этот пользователь не сможет войти, пока его не включат.")
	}
	return nil
}

func (a *app) userSetPassword(ctx context.Context, args []string) error {
	// Accept the login before or after the flags.
	var login string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		login, args = args[0], args[1:]
	}
	fs := a.newFlagSet("user set-password")
	mustChange := fs.Bool("must-change", false, "потребовать сменить пароль при следующем входе (с придуманным паролем — всегда)")
	pw := addPasswordFlags(fs)
	if err := parse(fs, args); err != nil {
		return err
	}
	if login == "" && fs.NArg() > 0 {
		login = fs.Arg(0)
	}
	name, err := normalizeUsername(login)
	if err != nil {
		return usageErr("Укажите логин: zoom-checker user set-password LOGIN [--password-stdin | --generate]")
	}
	password, generated, err := pw.get(a.stdin)
	if err != nil {
		return err
	}
	st, cfg, done, err := a.openStore(ctx, true)
	if err != nil {
		return err
	}
	defer done()

	u, err := st.GetUserByUsername(ctx, model.AuthLocal, name)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("локальный пользователь %q не найден (пароли пользователей LDAP и Keycloak меняются там)", name)
	}
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	u.PasswordHash = hash
	u.MustChangePassword = generated || *mustChange
	err = st.InTx(ctx, func(tx *store.Store) error {
		if err := tx.UpdateUser(ctx, u); err != nil {
			return err
		}
		// Sessions opened with the old password end now.
		if _, err := tx.DeleteUserSessions(ctx, u.ID, ""); err != nil {
			return err
		}
		return tx.InsertAudit(ctx, &model.AuditEntry{
			UserName: cliAuditName, Action: model.AuditUserUpdate, EntityType: "user",
			EntityID: strconv.FormatInt(u.ID, 10), NewValue: "пароль задан из командной строки",
		})
	})
	if err != nil {
		return err
	}
	if name == strings.ToLower(cfg.AdminUsername) {
		// The bootstrap password file no longer matches the account.
		if err := server.RemoveInitialPasswordFile(cfg.DataDir); err != nil {
			slog.Warn("could not remove the initial password file", "err", err)
		}
	}
	fmt.Fprintf(a.stdout, "Пароль пользователя %s изменён; его открытые сеансы завершены.\n", name)
	if generated {
		fmt.Fprintf(a.stdout, "Новый пароль: %s\n(при входе будет предложено сменить пароль; сохраните его сейчас — он больше не будет показан)\n", password)
	}
	if u.Disabled {
		fmt.Fprintln(a.stdout, "Внимание: учётная запись отключена; включите её в разделе «Пользователи».")
	}
	return nil
}

func roleRU(r model.Role) string {
	if r == model.RoleAdmin {
		return "администратор"
	}
	return "преподаватель"
}

func describeUser(u *model.User) string {
	s := fmt.Sprintf("%s (%s, %s)", u.Username, roleRU(u.Role), u.AuthSource)
	if u.DisplayName != "" {
		s = u.DisplayName + " — " + s
	}
	return s
}

// discardLogger is used for validation-only constructors in check-config.
func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }
