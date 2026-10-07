package httpapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"
)

// Login rate limiting (docs/architecture.md §6, §8).
//
// A login attempt from a browser that has no proof of an earlier login of the
// account is counted three ways:
//   - per client network (ipLimiter, 60 / 15 min): stops spraying many
//     usernames. IPv6 clients are counted per /64, the block one host gets;
//   - per client network + account (loginLimiter, 10 / 15 min): stops guessing
//     one password;
//   - per account, whatever the address (accountLimiter, 30 / 15 min): stops a
//     botnet or many /64s from guessing one password.
//
// A browser that logged into the account before carries a known-device cookie
// (OWASP "device cookies"). Its attempts are counted per device + account only
// (10 / 15 min), so other people's failures — a student spraying logins from
// the school's NAT address, or a stranger trying a teacher's username — never
// lock out a teacher on a computer they used before. A stolen cookie gives no
// more guesses than one address has.

const (
	knownDeviceCookie = "zc_known_login"
	knownDevicePath   = "/api/auth/login"
	knownDeviceMaxAge = 90 * 24 * time.Hour
	// knownDeviceAccounts is how many accounts one browser remembers (a
	// shared computer in the teachers' room); the most recent logins win.
	knownDeviceAccounts = 5
)

// limiterIP is the per-address key of the login limiters: an IPv6 host
// normally controls a whole /64, so per-address keys would let it rotate past
// the limits. IPv4-mapped addresses (from X-Forwarded-For) count as IPv4.
func limiterIP(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	a = a.Unmap().WithZone("")
	if a.Is4() {
		return a.String()
	}
	p, err := a.Prefix(64)
	if err != nil {
		return a.String()
	}
	return p.String()
}

// loginGate is the set of limiter keys one login attempt counts under.
type loginGate struct {
	s       *Server
	now     time.Time
	device  string // "dev|<device>|<account tag>" for a known device, else ""
	ip      string // limiterIP of the client
	pair    string // ip + "|" + account
	account string
	// counted marks the limiters whose Allow admitted the attempt (so a
	// refused attempt can give back what it took elsewhere).
	countedIP, countedPair, countedAccount bool
}

func (s *Server) newLoginGate(r *http.Request, username string, now time.Time) *loginGate {
	account := s.loginLimitAccount(username)
	g := &loginGate{s: s, now: now, account: account}
	if dev := s.knownDevice(r, account, now); dev != "" {
		g.device = "dev|" + dev + "|" + s.accountTag(account)
		return g
	}
	g.ip = limiterIP(clientIP(r))
	g.pair = g.ip + "|" + account
	return g
}

// allow counts the attempt. When it is refused, everything it counted is
// given back (no password was checked) and wait/perIP describe the refusal:
// perIP is true when the address as a whole is blocked.
func (g *loginGate) allow() (ok bool, wait time.Duration, perIP bool) {
	s := g.s
	if g.device != "" {
		if s.loginLimiter.Allow(g.device, g.now) {
			return true, 0, false
		}
		return false, s.loginLimiter.RetryAfter(g.device, g.now), false
	}
	g.countedIP = s.ipLimiter.Allow(g.ip, g.now)
	g.countedPair = s.loginLimiter.Allow(g.pair, g.now)
	g.countedAccount = s.accountLimiter.Allow(g.account, g.now)
	if g.countedIP && g.countedPair && g.countedAccount {
		return true, 0, false
	}
	perIP = !g.countedIP
	g.release()
	wait = max(s.ipLimiter.RetryAfter(g.ip, g.now), s.loginLimiter.RetryAfter(g.pair, g.now),
		s.accountLimiter.RetryAfter(g.account, g.now))
	return false, wait, perIP
}

// failure confirms a wrong password.
func (g *loginGate) failure() {
	s := g.s
	if g.device != "" {
		s.loginLimiter.Failure(g.device, g.now)
		return
	}
	s.ipLimiter.Failure(g.ip, g.now)
	s.loginLimiter.Failure(g.pair, g.now)
	s.accountLimiter.Failure(g.account, g.now)
}

// success resets the account keys. The per-address budget only gets its own
// attempt back: one valid account must not clear the protection against
// trying many usernames, and a school behind one address must not be locked
// out by its successful logins.
func (g *loginGate) success() {
	s := g.s
	if g.device != "" {
		s.loginLimiter.Success(g.device)
		return
	}
	s.loginLimiter.Success(g.pair)
	s.accountLimiter.Success(g.account)
	s.ipLimiter.Release(g.ip, g.now)
}

// release gives back an admitted attempt whose password was never checked
// (the hashing pool was busy).
func (g *loginGate) release() {
	s := g.s
	if g.device != "" {
		s.loginLimiter.Release(g.device, g.now)
		return
	}
	if g.countedIP {
		s.ipLimiter.Release(g.ip, g.now)
	}
	if g.countedPair {
		s.loginLimiter.Release(g.pair, g.now)
	}
	if g.countedAccount {
		s.accountLimiter.Release(g.account, g.now)
	}
	g.countedIP, g.countedPair, g.countedAccount = false, false, false
}

// rateLimitMessage is the Russian 429 text: a blocked address is named as
// such, so a teacher who made no attempt understands why.
func (s *Server) rateLimitMessage(perIP bool) string {
	if !perIP {
		return "Слишком много попыток входа. Попробуйте через %d мин."
	}
	msg := "С этого адреса было слишком много неудачных попыток входа. Попробуйте через %d мин."
	if s.OIDC != nil {
		label := strings.TrimSpace(s.Config.OIDC.ButtonLabel)
		if label == "" {
			label = defaultOIDCLabel
		}
		msg = strings.TrimSuffix(msg, ".") + " или нажмите «" + strings.ReplaceAll(label, "%", "%%") + "»."
	}
	return msg
}

// knownDeviceState is the payload of the known-device cookie. Accounts are
// stored as keyed tags, so the cookie does not reveal who logged in.
type knownDeviceState struct {
	Device   string   `json:"d"`
	Accounts []string `json:"a"`
	Issued   int64    `json:"t"`
}

func (s *Server) accountTag(account string) string {
	m := hmac.New(sha256.New, s.deviceKey)
	m.Write([]byte("account|" + account))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:12])
}

func (s *Server) deviceMAC(payload string) []byte {
	m := hmac.New(sha256.New, s.deviceKey)
	m.Write([]byte(knownDeviceCookie + "|" + payload))
	return m.Sum(nil)
}

// readKnownDevice returns the verified cookie state (ok=false when missing,
// forged or expired).
func (s *Server) readKnownDevice(r *http.Request, now time.Time) (knownDeviceState, bool) {
	var st knownDeviceState
	c, err := r.Cookie(knownDeviceCookie)
	if err != nil || len(c.Value) > 2048 {
		return st, false
	}
	payload, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return st, false
	}
	mac, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(mac, s.deviceMAC(payload)) {
		return st, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || json.Unmarshal(raw, &st) != nil || st.Device == "" {
		return st, false
	}
	if age := now.Sub(time.Unix(st.Issued, 0)); age > knownDeviceMaxAge || age < -time.Hour {
		return st, false
	}
	return st, true
}

// knownDevice returns the device id when this browser proved an earlier
// successful login of account, "" otherwise.
func (s *Server) knownDevice(r *http.Request, account string, now time.Time) string {
	st, ok := s.readKnownDevice(r, now)
	if !ok || !slices.Contains(st.Accounts, s.accountTag(account)) {
		return ""
	}
	return st.Device
}

// rememberDevice (re)issues the known-device cookie after a successful
// password login of account.
func (s *Server) rememberDevice(w http.ResponseWriter, r *http.Request, account string, now time.Time) {
	st, ok := s.readKnownDevice(r, now)
	if !ok {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		st = knownDeviceState{Device: base64.RawURLEncoding.EncodeToString(b)}
	}
	tag := s.accountTag(account)
	accounts := []string{tag}
	for _, a := range st.Accounts {
		if a != tag && len(accounts) < knownDeviceAccounts {
			accounts = append(accounts, a)
		}
	}
	st.Accounts, st.Issued = accounts, now.Unix()
	raw, err := json.Marshal(st)
	if err != nil {
		return
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	http.SetCookie(w, &http.Cookie{
		Name:     knownDeviceCookie,
		Value:    payload + "." + base64.RawURLEncoding.EncodeToString(s.deviceMAC(payload)),
		Path:     knownDevicePath,
		MaxAge:   int(knownDeviceMaxAge / time.Second),
		HttpOnly: true,
		Secure:   s.Config.PublicHTTPS(),
		SameSite: http.SameSiteStrictMode,
	})
}
