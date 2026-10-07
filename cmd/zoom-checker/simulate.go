package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"zoom-checker/internal/names"
	"zoom-checker/internal/timefmt"
	"zoom-checker/internal/zoom"
)

// defaultSimNames is the built-in class list. Roles are assigned in this order
// (see buildScenario), so "Иванов Иван" is the one joining as "  ИВАНОВ   Иван "
// (TZ §36 scenario 2).
var defaultSimNames = []string{
	"Иванов Иван",
	"Петрова Анна",
	"Сидоров Алексей",
	"Смирнова Мария",
	"Кузнецов Дмитрий",
	"Попова Екатерина",
	"Васильев Михаил",
	"Новикова Ольга",
	"Фёдоров Артём",
	"Морозова Дарья",
	"Волков Никита",
	"Соколова Алёна",
}

// Unidentified participants of the scenario (TZ §36 scenario 3).
const (
	simDeviceName   = "iPhone"
	simNicknameName = "Ваня"
)

// Leave reasons as Zoom words them.
const (
	reasonLeft          = "left the meeting. Reason : left the meeting"
	reasonJoinBreakout  = "left the meeting. Reason : join breakout room"
	reasonLeaveBreakout = "left the meeting. Reason : leave breakout room"
	reasonHostEnded     = "left the meeting. Reason : Host ended the meeting."
	reasonConnection    = "left the meeting. Reason : Connection lost"
)

type simOptions struct {
	URL       string
	Secret    string
	MeetingID string
	Topic     string
	HostName  string
	Timezone  string
	Names     []string
	Duration  time.Duration
	Speed     float64
	Instant   bool
	Seed      uint64
	NoEnd     bool
	DryRun    bool
	Insecure  bool
}

// simConn is one connection of a participant (one device, one join): Zoom gives
// every join its own participant_uuid and user_id.
type simConn struct {
	Name     string
	UUID     string
	UserID   string
	ID       string // deprecated "id": set for the signed-in host only
	PUserID  string // participant_user_id (Zoom account) — host only
	Email    string
	PublicIP string
}

type simEvent struct {
	Offset time.Duration // simulated time since the meeting start
	Type   string
	Conn   *simConn // participant events
	Reason string   // leave_reason for leaves
	// Breakout room events: room uuid and the room-local user id.
	BreakoutUUID   string
	BreakoutUserID string
	Note           string    // shown on the console
	DupOf          *simEvent // duplicate delivery of that event (same bytes)

	At   time.Time // simulated timestamp (render)
	Body []byte    // signed request body (render)
}

type simScenario struct {
	MeetingID   string
	MeetingUUID string
	HostID      string
	AccountID   string
	Topic       string
	Timezone    string
	Duration    time.Duration
	Events      []*simEvent
	Legend      []string
}

// simBuilder assembles the timeline. Times are given in minutes of a 90-minute
// template lesson and scaled to --duration.
type simBuilder struct {
	rng      *rand.Rand
	dur      time.Duration
	nextUser int
	events   []*simEvent
	open     map[*simConn]bool // main-room connection state at the end
	order    []*simConn        // connections in creation order (deterministic iteration)
}

func (b *simBuilder) at(min float64) time.Duration {
	return time.Duration(min / 90 * float64(b.dur))
}

// between returns a random time in [lo, hi) template minutes.
func (b *simBuilder) between(lo, hi float64) time.Duration {
	return b.at(lo + b.rng.Float64()*(hi-lo))
}

func (b *simBuilder) conn(name string) *simConn {
	b.nextUser++
	c := &simConn{
		Name:     name,
		UUID:     simUUID(b.rng),
		UserID:   fmt.Sprint(16777216 + 1024*b.nextUser),
		PublicIP: fmt.Sprintf("203.0.113.%d", 10+b.rng.IntN(240)),
	}
	b.order = append(b.order, c)
	return c
}

func (b *simBuilder) add(e *simEvent) *simEvent {
	b.events = append(b.events, e)
	return e
}

func (b *simBuilder) join(at time.Duration, c *simConn, note string) *simEvent {
	b.open[c] = true
	return b.add(&simEvent{Offset: at, Type: zoom.EventParticipantJoined, Conn: c, Note: note})
}

func (b *simBuilder) leave(at time.Duration, c *simConn, reason, note string) *simEvent {
	b.open[c] = false
	return b.add(&simEvent{Offset: at, Type: zoom.EventParticipantLeft, Conn: c, Reason: reason, Note: note})
}

// messyName turns "Иванов Иван" into "  ИВАНОВ   Иван ": the surname in
// capitals, extra spaces inside and around (TZ §36 scenario 2).
func messyName(name string) string {
	w := strings.Fields(name)
	if len(w) == 0 {
		return name
	}
	w[0] = strings.ToUpper(w[0])
	return "  " + strings.Join(w, "   ") + " "
}

// buildScenario creates the demo lesson. Roles go to the names in order:
//  1. joins with messy spacing/case;   2. leaves and rejoins twice;
//  3. two devices at once;             4. goes to a breakout room and back;
//  5. never joins;                     6–7. late by 10–20 minutes;
//  8. leaves early;                    the rest join within the first minutes.
//
// Plus the host, two unidentified participants ("iPhone", "Ваня"), one
// duplicate delivery and, unless NoEnd, the end of the meeting.
func buildScenario(o simOptions, rng *rand.Rand) *simScenario {
	b := &simBuilder{rng: rng, dur: o.Duration, open: map[*simConn]bool{}}
	sc := &simScenario{
		MeetingID:   o.MeetingID,
		MeetingUUID: simBase64(rng, 16),
		HostID:      simToken(rng, 22),
		AccountID:   simToken(rng, 22),
		Topic:       o.Topic,
		Timezone:    o.Timezone,
		Duration:    o.Duration,
	}
	legend := func(format string, args ...any) { sc.Legend = append(sc.Legend, fmt.Sprintf(format, args...)) }

	b.add(&simEvent{Offset: 0, Type: zoom.EventMeetingStarted, Note: "конференция началась"})

	host := b.conn(o.HostName)
	host.ID, host.PUserID, host.Email = sc.HostID, sc.HostID, "teacher@school.example"
	b.join(b.between(0.2, 0.6), host, "ведущий")
	legend("%s — ведущий конференции (скрывается, если включено «не учитывать ведущего»)", o.HostName)

	roster := o.Names
	role := 0
	next := func() (string, bool) {
		if role >= len(roster) {
			return "", false
		}
		role++
		return roster[role-1], true
	}
	var dupCandidate *simEvent

	if n, ok := next(); ok {
		c := b.conn(messyName(n))
		dupCandidate = b.join(b.between(0.5, 4), c, "имя с лишними пробелами и заглавными буквами")
		legend("%s — входит как %q (должен распознаться)", n, c.Name)
	}
	if n, ok := next(); ok {
		c1 := b.conn(n)
		b.join(b.between(1, 4), c1, "")
		b.leave(b.between(23, 27), c1, reasonConnection, "вышел (1-й раз)")
		c2 := b.conn(n)
		b.join(b.between(30, 32), c2, "вернулся (1-й раз)")
		b.leave(b.between(48, 52), c2, reasonLeft, "вышел (2-й раз)")
		c3 := b.conn(n)
		b.join(b.between(53, 55), c3, "вернулся (2-й раз)")
		legend("%s — выходит и возвращается дважды (время складывается)", n)
	}
	if n, ok := next(); ok {
		pc := b.conn(n)
		b.join(b.between(1, 4), pc, "компьютер")
		phone := b.conn(n)
		phone.PublicIP = fmt.Sprintf("198.51.100.%d", 10+rng.IntN(240))
		b.join(b.between(19, 21), phone, "второе устройство (телефон)")
		b.leave(b.between(43, 47), phone, reasonLeft, "телефон отключился, компьютер остался")
		legend("%s — сидит с двух устройств одновременно (время не удваивается)", n)
	}
	if n, ok := next(); ok {
		c := b.conn(n)
		b.join(b.between(1, 4), c, "")
		out := b.between(29, 31)
		back := b.between(44, 46)
		room := simBase64(rng, 16)
		b.leave(out, c, reasonJoinBreakout, "ушёл в сессионный зал")
		bo := b.add(&simEvent{Offset: out + 500*time.Millisecond, Type: zoom.EventBreakoutJoined, Conn: c,
			BreakoutUUID: room, BreakoutUserID: fmt.Sprint(16777216 + 1024*(100+b.nextUser)), Note: "в сессионном зале"})
		b.add(&simEvent{Offset: back, Type: zoom.EventBreakoutLeft, Conn: c, Reason: reasonLeaveBreakout,
			BreakoutUUID: room, BreakoutUserID: bo.BreakoutUserID, Note: "вышел из сессионного зала"})
		// Back in the main room: a new user_id, the same participant_uuid.
		b.nextUser++
		backConn := *c
		backConn.UserID = fmt.Sprint(16777216 + 1024*b.nextUser)
		b.order = append(b.order, &backConn)
		b.join(back+500*time.Millisecond, &backConn, "вернулся в общий зал")
		legend("%s — уходит в сессионный зал и возвращается (остаётся присутствующим)", n)
	}
	if n, ok := next(); ok {
		legend("%s — так и не подключается", n)
	}
	for i := 0; i < 2; i++ {
		if n, ok := next(); ok {
			at := b.between(10, 20)
			b.join(at, b.conn(n), fmt.Sprintf("опоздание ~%.0f мин (по времени занятия)", at.Minutes()*90/o.Duration.Minutes()))
			legend("%s — опаздывает на 10–20 минут", n)
		}
	}
	if n, ok := next(); ok {
		c := b.conn(n)
		b.join(b.between(0.5, 5), c, "")
		b.leave(b.between(72, 78), c, reasonLeft, "ушёл раньше конца")
		legend("%s — уходит раньше конца занятия", n)
	}
	var regular []string
	for {
		n, ok := next()
		if !ok {
			break
		}
		e := b.join(b.between(0.3, 5), b.conn(n), "")
		if dupCandidate == nil || len(regular) == 0 {
			dupCandidate = e
		}
		regular = append(regular, n)
	}
	if len(regular) > 0 {
		legend("%s — подключаются вовремя и сидят до конца", strings.Join(regular, ", "))
	}

	phoneGuest := b.conn(simDeviceName)
	b.join(b.between(3, 8), phoneGuest, "не идентифицирован")
	nick := b.conn(simNicknameName)
	b.join(b.between(12, 16), nick, "не идентифицирован")
	b.leave(b.between(55, 65), nick, reasonLeft, "")
	legend("«%s» и «%s» — не идентифицированы (их нужно сопоставить вручную)", simDeviceName, simNicknameName)

	if dupCandidate != nil {
		b.add(&simEvent{Offset: dupCandidate.Offset + b.at(2), Type: dupCandidate.Type, Conn: dupCandidate.Conn,
			DupOf: dupCandidate, Note: "повторная доставка того же события (не должна ничего изменить)"})
		legend("одно событие приходит дважды — повтор не должен создать второй вход")
	}

	if !o.NoEnd {
		end := b.dur
		// Zoom sends a leave for most participants when the host ends the
		// meeting; the rest are closed by meeting.ended itself (TZ §36 scenario 7).
		k := 0
		for _, c := range b.order {
			if !b.open[c] {
				continue
			}
			if c == host || (c != phoneGuest && rng.IntN(2) == 0) {
				k++
				b.leave(end-time.Duration(k)*b.at(0.02), c, reasonHostEnded, "")
			}
		}
		b.add(&simEvent{Offset: end, Type: zoom.EventMeetingEnded, Note: "конференция завершена"})
		legend("в конце ведущий завершает конференцию; кто остался — закрывается временем окончания")
	} else {
		legend("конференция не завершается (--no-end): занятие остаётся «идёт»")
	}

	sort.SliceStable(b.events, func(i, j int) bool { return b.events[i].Offset < b.events[j].Offset })
	sc.Events = b.events
	return sc
}

// Zoom webhook JSON shapes (see internal/zoom/testdata).
type zEnvelope struct {
	Event   string   `json:"event"`
	EventTS int64    `json:"event_ts"`
	Payload zPayload `json:"payload"`
}

type zPayload struct {
	AccountID string  `json:"account_id"`
	Object    zObject `json:"object"`
}

type zObject struct {
	ID               string        `json:"id"`
	UUID             string        `json:"uuid"`
	BreakoutRoomUUID string        `json:"breakout_room_uuid,omitempty"`
	HostID           string        `json:"host_id"`
	HostEmail        string        `json:"host_email,omitempty"`
	Topic            string        `json:"topic"`
	Type             int           `json:"type"`
	StartTime        string        `json:"start_time"`
	Timezone         string        `json:"timezone"`
	Duration         int           `json:"duration"`
	EndTime          string        `json:"end_time,omitempty"`
	Participant      *zParticipant `json:"participant,omitempty"`
}

type zParticipant struct {
	UserID            string `json:"user_id"`
	ParentUserID      string `json:"parent_user_id,omitempty"`
	UserName          string `json:"user_name"`
	ID                string `json:"id"`
	ParticipantUUID   string `json:"participant_uuid"`
	JoinTime          string `json:"join_time,omitempty"`
	LeaveTime         string `json:"leave_time,omitempty"`
	LeaveReason       string `json:"leave_reason,omitempty"`
	Email             string `json:"email"`
	RegistrantID      string `json:"registrant_id"`
	ParticipantUserID string `json:"participant_user_id"`
	PublicIP          string `json:"public_ip,omitempty"`
}

func zoomTime(t time.Time) string { return t.UTC().Truncate(time.Second).Format(time.RFC3339) }

// render fills At and Body of every event; clock maps a scenario offset to the
// timestamp written into the payload.
func (sc *simScenario) render(clock func(time.Duration) time.Time) error {
	start := zoomTime(clock(0))
	for _, e := range sc.Events {
		e.At = clock(e.Offset)
		if e.DupOf != nil {
			e.Body = e.DupOf.Body // identical bytes, as a Zoom retry
			continue
		}
		obj := zObject{
			ID:        sc.MeetingID,
			UUID:      sc.MeetingUUID,
			HostID:    sc.HostID,
			Topic:     sc.Topic,
			Type:      8,
			StartTime: start,
			Timezone:  sc.Timezone,
			Duration:  int(sc.Duration.Minutes()),
		}
		switch e.Type {
		case zoom.EventMeetingStarted:
			obj.HostEmail = "teacher@school.example"
		case zoom.EventMeetingEnded:
			obj.HostEmail = "teacher@school.example"
			obj.EndTime = zoomTime(e.At)
		default:
			c := e.Conn
			p := &zParticipant{
				UserID:            c.UserID,
				UserName:          c.Name,
				ID:                c.ID,
				ParticipantUUID:   c.UUID,
				Email:             c.Email,
				ParticipantUserID: c.PUserID,
				PublicIP:          c.PublicIP,
			}
			if e.BreakoutUUID != "" {
				obj.BreakoutRoomUUID = e.BreakoutUUID
				p.UserID, p.ParentUserID = e.BreakoutUserID, c.UserID
			}
			if zoom.IsLeaveEvent(e.Type) {
				// Real participant_left payloads carry no join_time.
				p.LeaveTime, p.LeaveReason = zoomTime(e.At), e.Reason
			} else {
				p.JoinTime = zoomTime(e.At)
			}
			obj.Participant = p
		}
		body, err := json.Marshal(zEnvelope{
			Event:   e.Type,
			EventTS: e.At.UnixMilli(),
			Payload: zPayload{AccountID: sc.AccountID, Object: obj},
		})
		if err != nil {
			return err
		}
		e.Body = body
	}
	return nil
}

func (a *app) cmdSimulate(ctx context.Context, args []string) error {
	fs := a.newFlagSet("simulate")
	urlFlag := fs.String("url", "", "`URL` для событий Zoom (по умолчанию PUBLIC_URL + /api/webhooks/zoom)")
	secret := fs.String("secret", "", "`ТОКЕН` — Secret Token приложения Zoom (по умолчанию ZOOM_WEBHOOK_SECRET_TOKEN)")
	meeting := fs.String("meeting", "", "`ID` — номер конференции Zoom или ссылка на неё (обязательно)")
	namesFile := fs.String("names", "", "`ФАЙЛ` с именами студентов, по одному в строке (по умолчанию 12 встроенных имён)")
	duration := fs.Duration("duration", 90*time.Minute, "длительность занятия, например 90m")
	speed := fs.Float64("speed", 30, "ускорение: 30 проигрывает 90-минутное занятие за 3 минуты")
	instant := fs.Bool("instant", false, "отправить всё сразу, распределив время событий по прошедшему --duration")
	seed := fs.Uint64("seed", 0, "начальное число для случайного выбора (по умолчанию случайное; печатается, чтобы повторить прогон)")
	noEnd := fs.Bool("no-end", false, "не завершать конференцию (занятие останется идущим)")
	dryRun := fs.Bool("dry-run", false, "напечатать JSON событий, ничего не отправляя")
	topic := fs.String("topic", "Демо-занятие Zoom Checker", "тема конференции")
	hostName := fs.String("host-name", "Преподаватель", "`ИМЯ` организатора конференции в Zoom")
	insecure := fs.Bool("insecure", false, "не проверять TLS-сертификат сервера (для тестовых серверов)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageErr("Лишние аргументы: %s", strings.Join(fs.Args(), " "))
	}
	if *meeting == "" {
		return usageErr("Укажите конференцию: --meeting 85746065432 (или ссылку на неё)")
	}
	mid, err := names.ParseMeetingID(*meeting)
	if err != nil {
		return usageErr("--meeting: %v", err)
	}
	if *duration < time.Minute {
		return usageErr("--duration должен быть не меньше 1m")
	}
	if !(*speed > 0) {
		return usageErr("--speed должен быть больше 0")
	}

	o := simOptions{
		URL: *urlFlag, Secret: *secret, MeetingID: mid, Topic: *topic, HostName: *hostName,
		Duration: *duration, Speed: *speed, Instant: *instant, NoEnd: *noEnd, DryRun: *dryRun,
		Insecure: *insecure, Names: defaultSimNames, Timezone: "Europe/Moscow",
	}
	if o.URL == "" || o.Secret == "" {
		cfg, err := a.loadConfig(false)
		switch {
		case err == nil:
			if o.URL == "" {
				o.URL = cfg.WebhookURL()
			}
			if o.Secret == "" {
				o.Secret = cfg.ZoomWebhookSecret
			}
			o.Timezone = cfg.Timezone.String()
		case !o.DryRun:
			a.printConfigErrors(err)
			fmt.Fprintln(a.stderr, "Передайте --url и --secret явно или исправьте настройки.")
			return errSilent
		}
	}
	if o.Secret == "" && !o.DryRun {
		return usageErr("Нет секрета: передайте --secret или задайте ZOOM_WEBHOOK_SECRET_TOKEN")
	}
	if *namesFile != "" {
		if o.Names, err = readNamesFile(*namesFile); err != nil {
			return err
		}
	}
	o.Seed = *seed
	if o.Seed == 0 {
		o.Seed = uint64(time.Now().UnixNano())
	}

	sc := buildScenario(o, rand.New(rand.NewPCG(o.Seed, o.Seed^0x9e3779b97f4a7c15)))
	now := time.Now()
	clock := simClock(o, now)
	if err := sc.render(clock); err != nil {
		return err
	}
	if o.DryRun {
		a.printSimLegend(a.stderr, sc, o, now)
		for _, e := range sc.Events {
			fmt.Fprintln(a.stdout, string(e.Body))
		}
		return nil
	}
	a.printSimLegend(a.stdout, sc, o, now)

	tr := http.DefaultTransport.(*http.Transport).Clone()
	if o.Insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit test flag
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: tr}
	sent, failed := sendScenario(ctx, a.stdout, client, sc, o, now)
	fmt.Fprintf(a.stdout, "\nОтправлено событий: %d, с ошибкой: %d.\n", sent, failed)
	if ctx.Err() != nil {
		fmt.Fprintln(a.stdout, "Прервано.")
		return errSilent
	}
	if failed > 0 {
		return errSilent
	}
	return nil
}

// simClock maps scenario offsets to payload timestamps: compressed real time
// from now (default), or the past --duration ending now (--instant).
func simClock(o simOptions, now time.Time) func(time.Duration) time.Time {
	if o.Instant {
		base := now.Add(-o.Duration)
		return func(off time.Duration) time.Time { return base.Add(off) }
	}
	return func(off time.Duration) time.Time {
		return now.Add(time.Duration(float64(off) / o.Speed))
	}
}

func (a *app) printSimLegend(w io.Writer, sc *simScenario, o simOptions, now time.Time) {
	mode := fmt.Sprintf("ускорение ×%g — займёт около %s", o.Speed,
		time.Duration(float64(o.Duration)/o.Speed).Round(time.Second))
	if o.Instant {
		mode = "всё сразу, время событий — последние " + o.Duration.String()
	}
	fmt.Fprintf(w, "Симуляция занятия: конференция %s, событий: %d, длительность %s (%s)\n",
		names.FormatMeetingID(sc.MeetingID), len(sc.Events), o.Duration, mode)
	if !o.DryRun {
		fmt.Fprintf(w, "Адрес: %s\n", o.URL)
	}
	fmt.Fprintf(w, "Seed: %d (повторить: --seed %d)\n", o.Seed, o.Seed)
	if o.Instant {
		// The meeting "started" --duration ago: a lesson is linked only when the meeting
		// started at most link_window_hours (default 1 h) before its planned start.
		loc := timefmt.LoadZone(o.Timezone)
		fmt.Fprintf(w, "Чтобы события попали в занятие, создайте занятие с конференцией %s, которое начинается в %s — когда началась конференция (можно на час позже).\n",
			names.FormatMeetingID(sc.MeetingID), timefmt.DateTime(now.Add(-o.Duration), loc))
	} else {
		fmt.Fprintf(w, "Чтобы события попали в занятие, создайте занятие с конференцией %s на текущее время.\n",
			names.FormatMeetingID(sc.MeetingID))
	}
	if !o.Instant && o.Speed != 1 {
		fmt.Fprintf(w, "Время сжато в %g раз: минуты сценария ниже пройдут быстрее, и в интерфейсе время присутствия и опоздания будут во столько же раз меньше (для настоящих минут используйте --instant).\n", o.Speed)
	}
	fmt.Fprintln(w, "Сценарий:")
	for _, l := range sc.Legend {
		fmt.Fprintln(w, "  • "+l)
	}
	fmt.Fprintln(w)
}

// sendScenario posts every event (signed like Zoom) at its time and prints one
// line per request. It returns how many were sent and how many failed.
func sendScenario(ctx context.Context, w io.Writer, client *http.Client, sc *simScenario, o simOptions, start time.Time) (sent, failed int) {
	for _, e := range sc.Events {
		if !o.Instant {
			due := start.Add(time.Duration(float64(e.Offset) / o.Speed))
			if d := time.Until(due); d > 0 {
				t := time.NewTimer(d)
				select {
				case <-ctx.Done():
					t.Stop()
					return sent, failed
				case <-t.C:
				}
			}
		}
		if ctx.Err() != nil {
			return sent, failed
		}
		status, err := postSigned(ctx, client, o.URL, o.Secret, e.Body)
		sent++
		who := ""
		if e.Conn != nil {
			who = fmt.Sprintf("%q", e.Conn.Name)
		}
		note := ""
		if e.Note != "" {
			note = " — " + e.Note
		}
		code := fmt.Sprint(status)
		if err != nil {
			code = "ERR"
			note += " (" + err.Error() + ")"
		}
		if err != nil || status < 200 || status > 299 {
			failed++
		}
		fmt.Fprintf(w, "%s  %-3s  %-41s %s%s\n", time.Now().Format("15:04:05"), code, e.Type, who, note)
	}
	return sent, failed
}

// postSigned sends body with Zoom's signature headers and returns the status.
func postSigned(ctx context.Context, client *http.Client, url, secret string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	ts, sig := zoom.Sign(secret, time.Now(), body)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("User-Agent", "Zoom Marketplace/1.0a (zoom-checker simulate)")
	req.Header.Set(zoom.HeaderTimestamp, ts)
	req.Header.Set(zoom.HeaderSignature, sig)
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, nil
}

// readNamesFile reads one name per line; blank lines and "#" comments are skipped.
func readNamesFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("--names: %w", err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("--names: %w", err)
	}
	if len(out) == 0 {
		return nil, errors.New("--names: в файле нет ни одного имени")
	}
	return out, nil
}

// Random identifiers shaped like Zoom's.

const simAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

func simToken(rng *rand.Rand, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = simAlphabet[rng.IntN(len(simAlphabet))]
	}
	return string(b)
}

func simBase64(rng *rand.Rand, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rng.UintN(256))
	}
	return base64.StdEncoding.EncodeToString(b)
}

// simUUID returns an upper-case UUID like Zoom's participant_uuid.
func simUUID(rng *rand.Rand) string {
	b := make([]byte, 16)
	for i := range b {
		b[i] = byte(rng.UintN(256))
	}
	return strings.ToUpper(fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]))
}
