package auth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-ldap/ldap/v3"

	"zoom-checker/internal/config"
)

const openLDAPFilter = "(&(objectClass=inetOrgPerson)(uid={username}))"

func testLDAPConfig(url string) config.LDAPConfig {
	return config.LDAPConfig{
		URL:             url,
		Preset:          config.PresetOpenLDAP,
		BindDN:          "uid=svc,dc=example,dc=test",
		BindPassword:    "svc",
		BaseDN:          "dc=example,dc=test",
		UserFilter:      openLDAPFilter,
		DisplayNameAttr: "cn",
		EmailAttr:       "mail",
		IDAttr:          "entryUUID",
		UsernameAttr:    "uid",
		TeacherGroups:   []string{"teachers"},
		Timeout:         2 * time.Second,
	}
}

// closedPort returns a loopback port nothing listens on.
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestNormalizeLogin(t *testing.T) {
	t.Parallel()
	ok := map[string]string{
		"alice":                    "alice",
		"  alice  ":                "alice",
		`CORP\alice`:               "alice",
		`corp.example\ t.ivanova `: "t.ivanova",
		"alice@corp.example.test":  "alice@corp.example.test",
		"Иванова":                  "Иванова",
	}
	for in, want := range ok {
		if got, err := normalizeLogin(in); err != nil || got != want {
			t.Errorf("normalizeLogin(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "   ", `CORP\`, "al\x00ice", "a\nb", "a\tb", "\x7f", "bad\xffutf8", strings.Repeat("a", 300)} {
		if got, err := normalizeLogin(in); err == nil {
			t.Errorf("normalizeLogin(%q) = %q, want error", in, got)
		}
	}
}

func TestUserFilterEscapesInjection(t *testing.T) {
	t.Parallel()
	l := &LDAP{cfg: testLDAPConfig("ldap://x")}
	cases := map[string]string{
		"alice":         "(&(objectClass=inetOrgPerson)(uid=alice))",
		"*":             `(&(objectClass=inetOrgPerson)(uid=\2a))`,
		"*)(uid=*":      `(&(objectClass=inetOrgPerson)(uid=\2a\29\28uid=\2a))`,
		"admin)(|(a=*)": `(&(objectClass=inetOrgPerson)(uid=admin\29\28|\28a=\2a\29))`,
		`a\2a`:          `(&(objectClass=inetOrgPerson)(uid=a\5c2a))`,
		"Иван":          `(&(objectClass=inetOrgPerson)(uid=\d0\98\d0\b2\d0\b0\d0\bd))`,
	}
	for in, want := range cases {
		got := l.userFilter(in)
		if got != want {
			t.Errorf("userFilter(%q) = %s, want %s", in, got, want)
		}
		// Still one well-formed equality filter, never a presence/substring match.
		if _, err := ldap.CompileFilter(got); err != nil {
			t.Errorf("userFilter(%q) does not compile: %v", in, err)
		}
	}
	ad := &LDAP{cfg: config.LDAPConfig{UserFilter: "(|(sAMAccountName={username})(userPrincipalName={username}))"}}
	if got := ad.userFilter("x*"); got != `(|(sAMAccountName=x\2a)(userPrincipalName=x\2a))` {
		t.Errorf("every placeholder must be substituted and escaped: %s", got)
	}
}

func TestAuthenticateRejectsBeforeDialing(t *testing.T) {
	t.Parallel()
	l, err := NewLDAP(testLDAPConfig("ldap://"+closedPort(t)), nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ user, pw string }{
		{"alice", ""},
		{"alice", " "},
		{"alice", "\t\n"},
		{"", "secret"},
		{"   ", "secret"},
		{"al\x00ice", "secret"},
		{`CORP\`, "secret"},
		{"alice", strings.Repeat("x", 2000)},
		{"alice", "\x00"},
		{"alice", "secret\x00tail"},
	}
	for _, c := range cases {
		start := time.Now()
		id, err := l.Authenticate(context.Background(), c.user, c.pw)
		if id != nil || !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("Authenticate(%q, %q) = %v, %v; want ErrInvalidCredentials", c.user, c.pw, id, err)
		}
		if time.Since(start) > 100*time.Millisecond {
			t.Errorf("Authenticate(%q, %q) took %v: must fail without network", c.user, c.pw, time.Since(start))
		}
	}
	if n := l.dials.Load(); n != 0 {
		t.Fatalf("%d connection attempts made; empty passwords must be rejected before dialing", n)
	}
}

func TestAuthenticateUnreachable(t *testing.T) {
	t.Parallel()
	l, err := NewLDAP(testLDAPConfig("ldap://"+closedPort(t)), nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = l.Authenticate(context.Background(), "alice", "secret")
	if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
	if time.Since(start) > 3*time.Second || l.dials.Load() != 1 {
		t.Fatalf("dials=%d after %v", l.dials.Load(), time.Since(start))
	}
	steps := l.Diagnose(context.Background(), "alice", "secret")
	if len(steps) != 1 || steps[0].OK || !strings.Contains(steps[0].Detail, "отклонено") {
		t.Fatalf("Diagnose on a closed port: %+v", steps)
	}
}

func TestFormatGUID(t *testing.T) {
	t.Parallel()
	// objectGUID a9e49734-7406-4a3b-9c1d-0123456789ab as stored by AD: the
	// first three groups little-endian, the rest as is.
	raw := []byte{0x34, 0x97, 0xe4, 0xa9, 0x06, 0x74, 0x3b, 0x4a, 0x9c, 0x1d, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab}
	if got := formatGUID(raw); got != "a9e49734-7406-4a3b-9c1d-0123456789ab" {
		t.Fatalf("formatGUID = %s", got)
	}
	if got := formatGUID([]byte{1, 2}); got != "0102" {
		t.Fatalf("formatGUID(short) = %s", got)
	}
}

func TestExternalIDAndIdentity(t *testing.T) {
	t.Parallel()
	guid := []byte{0x34, 0x97, 0xe4, 0xa9, 0x06, 0x74, 0x3b, 0x4a, 0x9c, 0x1d, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab}
	ad := &LDAP{cfg: config.LDAPConfig{IDAttr: "objectGUID", UsernameAttr: "sAMAccountName", DisplayNameAttr: "displayName", EmailAttr: "mail"}}
	e := &ldap.Entry{DN: "CN=Татьяна Иванова,OU=Users,DC=corp", Attributes: []*ldap.EntryAttribute{
		{Name: "objectGUID", ByteValues: [][]byte{guid}, Values: []string{string(guid)}},
		{Name: "sAMAccountName", Values: []string{"T.Ivanova"}, ByteValues: [][]byte{[]byte("T.Ivanova")}},
		{Name: "cn", Values: []string{"Татьяна Иванова"}, ByteValues: [][]byte{[]byte("Татьяна Иванова")}},
	}}
	id := ad.identity(e, "t.ivanova@corp", []ldapGroup{{DN: "CN=helpdesk,DC=corp", CN: "helpdesk"}})
	if id.ExternalID != "a9e49734-7406-4a3b-9c1d-0123456789ab" || id.Username != "t.ivanova" ||
		id.DisplayName != "Татьяна Иванова" || id.Groups[0] != "CN=helpdesk,DC=corp" || id.Source != "ldap" {
		t.Fatalf("unexpected identity: %+v", id)
	}

	ol := &LDAP{cfg: config.LDAPConfig{IDAttr: "entryUUID", UsernameAttr: "uid", DisplayNameAttr: "displayName"}}
	e2 := &ldap.Entry{DN: "uid=Bob,ou=People,dc=x", Attributes: []*ldap.EntryAttribute{
		{Name: "entryUUID", Values: []string{"5d3e2f9a-1111-2222-3333-444455556666"}, ByteValues: [][]byte{[]byte("5d3e2f9a-1111-2222-3333-444455556666")}},
	}}
	id2 := ol.identity(e2, "BOB", nil)
	if id2.ExternalID != "5d3e2f9a-1111-2222-3333-444455556666" || id2.Username != "bob" || id2.DisplayName != "bob" {
		t.Fatalf("fallbacks: %+v", id2)
	}
	e3 := &ldap.Entry{DN: "UID=Bob, OU=People, DC=x"}
	if got := ol.externalID(e3); got != "uid=bob,ou=people,dc=x" {
		t.Fatalf("missing id attribute must fall back to the normalized DN, got %q", got)
	}
	bin := &LDAP{cfg: config.LDAPConfig{IDAttr: "objectSid"}}
	e4 := &ldap.Entry{DN: "cn=x", Attributes: []*ldap.EntryAttribute{{Name: "objectSid", ByteValues: [][]byte{{1, 5, 0, 0xff}}}}}
	if got := bin.externalID(e4); got != "010500ff" {
		t.Fatalf("binary id must be hex, got %q", got)
	}
}

func TestBindErrorClassification(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	l := &LDAP{cfg: testLDAPConfig("ldap://x"), log: slog.New(slog.NewTextHandler(&buf, nil))}
	adMsg := func(code string) error {
		return ldap.NewError(ldap.LDAPResultInvalidCredentials,
			errors.New("80090308: LdapErr: DSID-0C09041C, comment: AcceptSecurityContext error, data "+code+", v4563"))
	}
	cases := []struct {
		err     error
		want    error
		logPart string
	}{
		{adMsg("52e"), ErrInvalidCredentials, "invalid credentials (AD 52e)"},
		{adMsg("533"), ErrInvalidCredentials, "account disabled (AD 533)"},
		{adMsg("775"), ErrInvalidCredentials, "account locked out (AD 775)"},
		{adMsg("532"), ErrInvalidCredentials, "password expired"},
		{adMsg("701"), ErrInvalidCredentials, "account expired"},
		{adMsg("773"), ErrInvalidCredentials, "must reset password"},
		{ldap.NewError(ldap.LDAPResultInvalidCredentials, errors.New("")), ErrInvalidCredentials, "invalid credentials"},
		{ldap.NewError(ldap.LDAPResultUnwillingToPerform, errors.New("unauthenticated bind")), ErrInvalidCredentials, "LDAP result 53"},
		{ldap.NewError(ldap.ErrorNetwork, errors.New("ldap: connection closed")), ErrUnavailable, ""},
		{ldap.NewError(ldap.LDAPResultServerDown, errors.New("down")), ErrUnavailable, ""},
		{ldap.NewError(ldap.LDAPResultStrongAuthRequired, errors.New("BindSimple: Transport encryption required.")), ErrUnavailable, "encrypted connection"},
		{errors.New("unable to send request"), ErrUnavailable, ""},
	}
	for _, c := range cases {
		buf.Reset()
		got := l.bindError(c.err, "ivanov")
		if !errors.Is(got, c.want) {
			t.Errorf("bindError(%v) = %v, want %v", c.err, got, c.want)
		}
		if c.want == ErrInvalidCredentials && got != ErrInvalidCredentials {
			t.Errorf("invalid credentials must be the bare sentinel (no details leak), got %v", got)
		}
		if !strings.Contains(buf.String(), c.logPart) {
			t.Errorf("log for %v = %q, want it to contain %q", c.err, buf.String(), c.logPart)
		}
	}
}

// TestGroupRoleMatching checks the LDAP side of MapRole: only DNs are matched,
// so a delegated admin's group whose CN merely ends in "/<rule>" grants nothing.
func TestGroupRoleMatching(t *testing.T) {
	t.Parallel()
	groups := []ldapGroup{
		{DN: "CN=Teachers/ZoomChecker-Admins,OU=Delegated,DC=corp", CN: "Teachers/ZoomChecker-Admins"},
		{DN: "CN=helpdesk,OU=G,DC=corp", CN: "zoomchecker-teachers"}, // multi-valued cn differing from the RDN
	}
	if v := groupValues(groups); len(v) != 2 || v[0] != groups[0].DN || v[1] != groups[1].DN {
		t.Fatalf("groupValues must return the DNs only: %q", v)
	}
	if role, ok := MapRole(groupValues(groups), []string{"ZoomChecker-Admins"}, []string{"ZoomChecker-Teachers"}); ok {
		t.Fatalf("got role %q, want access denied", role)
	}
	if role, ok := MapRole(groupValues(groups), nil, []string{"helpdesk"}); !ok || role != "teacher" {
		t.Fatalf("CN rule must match the RDN: %q %v", role, ok)
	}
}

func TestGroupSearchBase(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"OU=Teachers,DC=corp,DC=local":     "DC=corp,DC=local",
		"ou=People,dc=example,dc=test":     "dc=example,dc=test",
		"DC=corp,DC=local":                 "DC=corp,DC=local",
		"ou=People,o=company":              "ou=People,o=company",
		"cn=users,cn=accounts,dc=ipa,dc=x": "dc=ipa,dc=x",
		"not a dn":                         "not a dn",
	}
	for in, want := range cases {
		if got := groupSearchBase(in); got != want {
			t.Errorf("groupSearchBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewLDAPValidation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	caPEM, _, _ := testCA(t)
	caFile := filepath.Join(dir, "ca.pem")
	junk := filepath.Join(dir, "junk.pem")
	os.WriteFile(caFile, caPEM, 0o600)
	os.WriteFile(junk, []byte("not a certificate"), 0o600)

	base := testLDAPConfig("ldaps://dc1.corp.local:636")
	mod := func(f func(*config.LDAPConfig)) config.LDAPConfig { c := base; f(&c); return c }
	bad := map[string]config.LDAPConfig{
		"no url":            mod(func(c *config.LDAPConfig) { c.URL = "" }),
		"http url":          mod(func(c *config.LDAPConfig) { c.URL = "http://dc1" }),
		"starttls+ldaps":    mod(func(c *config.LDAPConfig) { c.StartTLS = true }),
		"no base":           mod(func(c *config.LDAPConfig) { c.BaseDN = " " }),
		"no placeholder":    mod(func(c *config.LDAPConfig) { c.UserFilter = "(uid=x)" }),
		"no bind dn":        mod(func(c *config.LDAPConfig) { c.BindDN = "" }),
		"ad without domain": mod(func(c *config.LDAPConfig) { c.BindDN = ""; c.Preset = config.PresetAD }),
		"missing ca":        mod(func(c *config.LDAPConfig) { c.CAFile = filepath.Join(dir, "missing.pem") }),
		"junk ca":           mod(func(c *config.LDAPConfig) { c.CAFile = junk }),
	}
	for name, cfg := range bad {
		if _, err := NewLDAP(cfg, nil); err == nil {
			t.Errorf("%s: NewLDAP must fail", name)
		}
	}
	good := []config.LDAPConfig{
		base,
		mod(func(c *config.LDAPConfig) { c.CAFile = caFile }),
		mod(func(c *config.LDAPConfig) { c.BindDN = ""; c.Preset = config.PresetAD; c.Domain = "corp.local" }),
		mod(func(c *config.LDAPConfig) {
			c.URL = "ldap://dc1:389"
			c.StartTLS = true
			c.Timeout = 0
			c.UsernameAttr = ""
		}),
	}
	for i, cfg := range good {
		l, err := NewLDAP(cfg, nil)
		if err != nil {
			t.Errorf("good[%d]: %v", i, err)
			continue
		}
		if l.cfg.Timeout <= 0 || l.cfg.UsernameAttr == "" || l.tlsConfig.ServerName == "" {
			t.Errorf("good[%d]: defaults not applied: %+v", i, l.cfg)
		}
	}
}

// testCA returns a CA certificate (PEM) and a server certificate for
// 127.0.0.1 signed by it.
func testCA(t *testing.T) ([]byte, tls.Certificate, *x509.Certificate) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "zc test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	srvKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srvTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		KeyUsage: x509.KeyUsageDigitalSignature,
	}
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTmpl, ca, &srvKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		tls.Certificate{Certificate: [][]byte{srvDER}, PrivateKey: srvKey}, ca
}

// TestDiagnoseTLS runs Diagnose against an in-process TLS listener that
// completes the handshake and hangs up: the connection step reflects
// certificate verification, the next step fails.
func TestDiagnoseTLS(t *testing.T) {
	t.Parallel()
	caPEM, cert, _ := testCA(t)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.(*tls.Conn).Handshake()
			c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port

	run := func(url, ca string) []DiagStep {
		cfg := testLDAPConfig(url)
		cfg.CAFile = ca
		l, err := NewLDAP(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		return l.Diagnose(context.Background(), "alice", "")
	}
	steps := run("ldaps://127.0.0.1:"+itoa(port), caFile)
	if len(steps) != 2 || !steps[0].OK || steps[1].OK || !strings.Contains(steps[0].Detail, "TLS") {
		t.Fatalf("with CA: %+v", steps)
	}
	steps = run("ldaps://127.0.0.1:"+itoa(port), "")
	if len(steps) != 1 || steps[0].OK || !strings.Contains(steps[0].Detail, "неизвестным центром сертификации") {
		t.Fatalf("without CA: %+v", steps)
	}
	steps = run("ldaps://localhost:"+itoa(port), caFile)
	if len(steps) != 1 || steps[0].OK || !strings.Contains(steps[0].Detail, "не совпадает") {
		t.Fatalf("wrong host name: %+v", steps)
	}
}

func itoa(n int) string { return big.NewInt(int64(n)).String() }
