package httpapi

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	ber "github.com/go-asn1-ber/asn1-ber"
	"github.com/go-ldap/ldap/v3"

	"zoom-checker/internal/auth"
	"zoom-checker/internal/config"
	"zoom-checker/internal/model"
)

const (
	azLDAPBase  = "dc=example,dc=test"
	azLDAPSvcDN = "uid=svc,dc=example,dc=test"
	azLDAPSvcPW = "svc-bind-password"
)

// azLDAPUser is one inetOrgPerson of the fake directory.
type azLDAPUser struct {
	UID, Password, UUID, CN, Mail string
	MemberOf                      []string
}

func (u azLDAPUser) dn() string { return "uid=" + u.UID + ",ou=people," + azLDAPBase }

// azFakeLDAP is a tiny LDAPv3 server: simple binds, the user search of the
// OpenLDAP preset (uid=...) and an empty group search (groups come from
// memberOf). Enough for auth.LDAP.Authenticate and Diagnose.
type azFakeLDAP struct {
	ln    net.Listener
	mu    sync.Mutex
	users map[string]azLDAPUser
}

func newAZFakeLDAP(t *testing.T, users ...azLDAPUser) *azFakeLDAP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &azFakeLDAP{ln: ln, users: map[string]azLDAPUser{}}
	for _, u := range users {
		f.users[u.UID] = u
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *azFakeLDAP) set(u azLDAPUser) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[u.UID] = u
}

func (f *azFakeLDAP) url() string { return "ldap://" + f.ln.Addr().String() }

func (f *azFakeLDAP) serve(c net.Conn) {
	defer c.Close()
	for {
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		p, err := ber.ReadPacket(c)
		if err != nil || len(p.Children) < 2 {
			return
		}
		msgID, _ := p.Children[0].Value.(int64)
		op := p.Children[1]
		var out []*ber.Packet
		switch op.Tag {
		case ldap.ApplicationBindRequest:
			name, _ := op.Children[1].Value.(string)
			pw := op.Children[2].Data.String()
			out = append(out, azLDAPResult(msgID, ldap.ApplicationBindResponse, f.bind(name, pw)))
		case ldap.ApplicationSearchRequest:
			filter, _ := ldap.DecompileFilter(op.Children[6])
			for _, u := range f.search(filter) {
				out = append(out, azLDAPEntry(msgID, u))
			}
			out = append(out, azLDAPResult(msgID, ldap.ApplicationSearchResultDone, ldap.LDAPResultSuccess))
		case ldap.ApplicationUnbindRequest:
			return
		default:
			continue // abandon etc.
		}
		for _, pkt := range out {
			if _, err := c.Write(pkt.Bytes()); err != nil {
				return
			}
		}
	}
}

func (f *azFakeLDAP) bind(dn, pw string) uint16 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if dn == azLDAPSvcDN && pw == azLDAPSvcPW {
		return ldap.LDAPResultSuccess
	}
	for _, u := range f.users {
		if strings.EqualFold(dn, u.dn()) && pw == u.Password && pw != "" {
			return ldap.LDAPResultSuccess
		}
	}
	return ldap.LDAPResultInvalidCredentials
}

// search answers "(&(objectClass=inetOrgPerson)(uid=X))"; group searches find nothing.
func (f *azFakeLDAP) search(filter string) []azLDAPUser {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, rest, ok := strings.Cut(filter, "(uid=")
	if !ok || strings.Contains(filter, "groupOf") {
		return nil
	}
	uid, _, _ := strings.Cut(rest, ")")
	if u, ok := f.users[strings.ToLower(uid)]; ok {
		return []azLDAPUser{u}
	}
	return nil
}

func azLDAPEnvelope(msgID int64, op *ber.Packet) *ber.Packet {
	env := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSequence, nil, "LDAPMessage")
	env.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagInteger, msgID, "messageID"))
	env.AppendChild(op)
	return env
}

func azLDAPResult(msgID int64, tag ber.Tag, code uint16) *ber.Packet {
	op := ber.Encode(ber.ClassApplication, ber.TypeConstructed, tag, nil, "result")
	op.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagEnumerated, int64(code), "resultCode"))
	op.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, "", "matchedDN"))
	op.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, "", "diagnosticMessage"))
	return azLDAPEnvelope(msgID, op)
}

func azLDAPEntry(msgID int64, u azLDAPUser) *ber.Packet {
	op := ber.Encode(ber.ClassApplication, ber.TypeConstructed, ldap.ApplicationSearchResultEntry, nil, "entry")
	op.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, u.dn(), "objectName"))
	attrs := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSequence, nil, "attributes")
	add := func(name string, vals ...string) {
		a := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSequence, nil, "attribute")
		a.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, name, "type"))
		set := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSet, nil, "vals")
		for _, v := range vals {
			set.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, v, "value"))
		}
		a.AppendChild(set)
		attrs.AppendChild(a)
	}
	add("uid", u.UID)
	add("cn", u.CN)
	add("mail", u.Mail)
	add("entryUUID", u.UUID)
	if len(u.MemberOf) > 0 {
		add("memberOf", u.MemberOf...)
	}
	op.AppendChild(attrs)
	return azLDAPEnvelope(msgID, op)
}

func azLDAPConfig(url string) config.LDAPConfig {
	return config.LDAPConfig{
		URL: url, Preset: config.PresetOpenLDAP, BindDN: azLDAPSvcDN, BindPassword: azLDAPSvcPW,
		BaseDN: azLDAPBase, UserFilter: "(&(objectClass=inetOrgPerson)(uid={username}))",
		DisplayNameAttr: "cn", EmailAttr: "mail", IDAttr: "entryUUID", UsernameAttr: "uid",
		AdminGroups: []string{"admins"}, TeacherGroups: []string{"teachers"}, Timeout: 3 * time.Second,
	}
}

func newAZLDAPEnv(t *testing.T, dir *azFakeLDAP) *azEnv {
	t.Helper()
	cfg := azLDAPConfig(dir.url())
	l, err := auth.NewLDAP(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	return newAZEnv(t, func(s *azSetup) {
		s.cfg.LDAP = cfg
		s.deps.LDAP = l
	})
}

const (
	azTeachersDN = "cn=teachers,ou=groups,dc=example,dc=test"
	azAdminsDN   = "cn=admins,ou=groups,dc=example,dc=test"
)

func TestLoginLDAP(t *testing.T) {
	t.Parallel()
	dir := newAZFakeLDAP(t, azLDAPUser{UID: "alice", Password: "alice-pw", UUID: "uuid-alice", CN: "Алиса Петрова",
		Mail: "alice@example.test", MemberOf: []string{azTeachersDN}})
	e := newAZLDAPEnv(t, dir)

	rec, cookie := e.login("Alice", "alice-pw")
	azWantStatus(t, rec, http.StatusOK)
	u := azDecode[loginResponse](t, rec).User
	if cookie == nil || u.AuthSource != "ldap" || u.Username != "alice" || u.DisplayName != "Алиса Петрова" ||
		u.Email != "alice@example.test" || u.Role != model.RoleTeacher || u.MustChangePassword {
		t.Fatalf("ldap user = %+v", u)
	}
	stored, err := e.st.GetUserByExternalID(context.Background(), model.AuthLDAP, "uuid-alice")
	if err != nil || stored.ID != u.ID {
		t.Fatalf("stored by external id: %+v %v", stored, err)
	}

	// Directory changes are synced at the next login, on the same row.
	dir.set(azLDAPUser{UID: "alice", Password: "alice-pw", UUID: "uuid-alice", CN: "Алиса Иванова",
		Mail: "alice@example.test", MemberOf: []string{azAdminsDN}})
	rec, _ = e.login("alice", "alice-pw")
	azWantStatus(t, rec, http.StatusOK)
	u2 := azDecode[loginResponse](t, rec).User
	if u2.ID != u.ID || u2.DisplayName != "Алиса Иванова" || u2.Role != model.RoleAdmin {
		t.Fatalf("synced user = %+v", u2)
	}

	// Wrong / unknown → the generic 422.
	rec, _ = e.login("alice", "wrong")
	azWantError(t, rec, http.StatusUnprocessableEntity, CodeInvalidCredentials)
	rec, _ = e.login("mallory", "whatever")
	azWantError(t, rec, http.StatusUnprocessableEntity, CodeInvalidCredentials)

	// Changing the password here is refused for directory accounts.
	msg := azWantError(t, e.do("POST", "/api/auth/password",
		map[string]string{"current_password": "alice-pw", "new_password": "new password 1"}, cookie),
		http.StatusUnprocessableEntity, CodeValidation)
	if msg != "Пароль меняется в корпоративной системе" {
		t.Fatalf("message %q", msg)
	}
}

func TestLoginLDAPDeniedAndDisabled(t *testing.T) {
	t.Parallel()
	dir := newAZFakeLDAP(t,
		azLDAPUser{UID: "eve", Password: "eve-pw", UUID: "uuid-eve", CN: "Ева", MemberOf: []string{"cn=students,ou=groups,dc=example,dc=test"}},
		azLDAPUser{UID: "dan", Password: "dan-pw", UUID: "uuid-dan", CN: "Дан", MemberOf: []string{azTeachersDN}},
	)
	e := newAZLDAPEnv(t, dir)

	rec, cookie := e.login("eve", "eve-pw")
	msg := azWantError(t, rec, http.StatusForbidden, CodeAccessDenied)
	if !strings.Contains(msg, "teachers") || cookie != nil {
		t.Fatalf("access denied message %q", msg)
	}

	e.createUser("dan", "", model.RoleTeacher, func(u *model.User) {
		u.AuthSource, u.ExternalID, u.Disabled = model.AuthLDAP, "uuid-dan", true
	})
	rec, cookie = e.login("dan", "dan-pw")
	azWantError(t, rec, http.StatusForbidden, CodeAccountDisabled)
	if cookie != nil {
		t.Fatal("disabled user got a session")
	}
}

func TestLoginLocalAccountWinsOverLDAP(t *testing.T) {
	t.Parallel()
	dir := newAZFakeLDAP(t, azLDAPUser{UID: "carol", Password: "ldap-pw", UUID: "uuid-carol", CN: "Кэрол", MemberOf: []string{azTeachersDN}})
	e := newAZLDAPEnv(t, dir)
	e.createUser("carol", "local password", model.RoleTeacher, nil)

	rec, _ := e.login("carol", "ldap-pw")
	azWantError(t, rec, http.StatusUnprocessableEntity, CodeInvalidCredentials)
	rec, _ = e.login("carol", "local password")
	azWantStatus(t, rec, http.StatusOK)
	if u := azDecode[loginResponse](t, rec).User; u.AuthSource != "local" {
		t.Fatalf("user = %+v", u)
	}
}

func TestLoginLDAPUnavailable(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	cfg := azLDAPConfig("ldap://" + addr)
	l, err := auth.NewLDAP(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := newAZEnv(t, func(s *azSetup) { s.cfg.LDAP, s.deps.LDAP = cfg, l })
	rec, _ := e.login("alice", "alice-pw")
	msg := azWantError(t, rec, http.StatusServiceUnavailable, CodeUnavailable)
	if msg != auth.UserMessage(auth.ErrUnavailable) {
		t.Fatalf("message %q", msg)
	}
}

func TestTestLDAPEndpoint(t *testing.T) {
	t.Parallel()
	dir := newAZFakeLDAP(t, azLDAPUser{UID: "alice", Password: "alice-pw", UUID: "uuid-alice", CN: "Алиса", MemberOf: []string{azTeachersDN}})
	e := newAZLDAPEnv(t, dir)
	_, admin := e.adminCookie()

	rec := e.do("POST", "/api/system/test-ldap", map[string]string{"username": "alice", "password": "alice-pw"}, admin)
	azWantStatus(t, rec, http.StatusOK)
	res := azDecode[testLDAPResponse](t, rec)
	if !res.OK || len(res.Steps) < 3 {
		t.Fatalf("diagnostics = %+v", res)
	}
	if strings.Contains(rec.Body.String(), "alice-pw") || strings.Contains(rec.Body.String(), azLDAPSvcPW) {
		t.Fatalf("secret in diagnostics: %s", rec.Body.String())
	}
	rec = e.do("POST", "/api/system/test-ldap", map[string]string{"username": "alice", "password": "bad"}, admin)
	if res := azDecode[testLDAPResponse](t, rec); res.OK || len(res.Steps) == 0 || res.Steps[len(res.Steps)-1].OK {
		t.Fatalf("wrong password diagnostics = %+v", res)
	}
	// Empty body is fine too (connection + service account only).
	azWantStatus(t, e.do("POST", "/api/system/test-ldap", nil, admin), http.StatusOK)

	plain := newAZEnv(t)
	_, admin2 := plain.adminCookie()
	azWantError(t, plain.do("POST", "/api/system/test-ldap", map[string]string{}, admin2), http.StatusUnprocessableEntity, CodeValidation)
}

// Password checks through the LDAP test are limited per tested account (they
// would otherwise be an unlimited password oracle) and audited; runs without a
// password are not limited.
func TestTestLDAPEndpointLimitsPasswordChecks(t *testing.T) {
	t.Parallel()
	dir := newAZFakeLDAP(t, azLDAPUser{UID: "alice", Password: "alice-pw", UUID: "uuid-alice", CN: "Алиса", MemberOf: []string{azTeachersDN}})
	e := newAZLDAPEnv(t, dir)
	_, admin := e.adminCookie()
	other := e.createUser("root2", "", model.RoleAdmin, nil)
	admin2 := e.sessionFor(other, model.AuthLocal, "")

	for i := range 10 {
		pw := fmt.Sprint("guess", i)
		if i == 9 {
			pw = "alice-pw" // a correct password does not reset the budget
		}
		azWantStatus(t, e.do("POST", "/api/system/test-ldap", map[string]string{"username": "alice", "password": pw}, admin), http.StatusOK)
	}
	for _, c := range []*http.Cookie{admin, admin2} { // another admin shares the account's budget
		rec := e.do("POST", "/api/system/test-ldap", map[string]string{"username": "ALICE", "password": "alice-pw"}, c)
		azWantError(t, rec, http.StatusTooManyRequests, CodeRateLimited)
		if rec.Header().Get("Retry-After") == "" {
			t.Fatal("no Retry-After")
		}
	}
	// Without a password nothing is limited.
	azWantStatus(t, e.do("POST", "/api/system/test-ldap", map[string]string{"username": "alice"}, admin), http.StatusOK)
	azWantStatus(t, e.do("POST", "/api/system/test-ldap", nil, admin), http.StatusOK)

	entries := e.azAudit(model.AuditLDAPTest)
	if len(entries) != 12 {
		t.Fatalf("%d audit entries, want 12 (refused runs are not audited)", len(entries))
	}
	for _, a := range entries {
		if strings.Contains(a.NewValue, "alice-pw") || strings.Contains(a.NewValue, "guess") || a.UserID == nil {
			t.Fatalf("audit entry %+v", a)
		}
	}
	if !strings.Contains(entries[0].NewValue+entries[len(entries)-1].NewValue, "с паролем") {
		t.Errorf("audit does not say a password was checked: %+v", entries)
	}
}
