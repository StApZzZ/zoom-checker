package main

import (
	"net"
	"net/url"
	"strings"
	"testing"

	"zoom-checker/internal/config"
)

// check-config and the pg_dump hint of backup never print a database password,
// wherever the URL carries it.
func TestCheckConfigRedactsDatabasePasswords(t *testing.T) {
	for _, dbURL := range []string{
		"postgres://zc@db:5432/zc?sslmode=require&password=SuperSecretPw&sslpassword=KeyPw",
		"postgres://zc:SuperSecretPw/x@db:5432/zc",
		"postgres://zc:Super%SecretPw@db:5432/zc",
	} {
		cliEnv(t, map[string]string{"DATA_DIR": t.TempDir(), "ZOOM_WEBHOOK_SECRET_TOKEN": "x", "DATABASE_URL": dbURL})
		code, out, errOut := runCLI(t, "", "check-config")
		if code != 0 {
			t.Fatalf("%s: exit %d\n%s%s", dbURL, code, out, errOut)
		}
		_, _, hint := runCLI(t, "", "backup", t.TempDir()+"/x.dump")
		for _, text := range []string{out, errOut, hint} {
			if strings.Contains(text, "SuperSecretPw") || strings.Contains(text, "KeyPw") || strings.Contains(text, "Secret") {
				t.Errorf("%s: a password was printed:\n%s", dbURL, text)
			}
		}
		if !strings.Contains(hint, "pg_dump") {
			t.Errorf("backup hint missing: %q", hint)
		}
	}
}

func TestCheckConfigWarnings(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string // "" = no such warning
		not  string
	}{
		{"weak ADMIN_PASSWORD", map[string]string{"ADMIN_PASSWORD": "admin"}, "ADMIN_PASSWORD слишком простой", ""},
		{"strong ADMIN_PASSWORD", map[string]string{"ADMIN_PASSWORD": "Strong-Pass-2026"}, "", "ADMIN_PASSWORD"},
		{"https behind a proxy without TRUSTED_PROXIES",
			map[string]string{"PUBLIC_URL": "https://att.example.com", "TLS_MODE": "off"}, "TRUSTED_PROXIES", ""},
		{"https behind a trusted proxy",
			map[string]string{"PUBLIC_URL": "https://att.example.com", "TLS_MODE": "off", "TRUSTED_PROXIES": "10.0.0.1/32"}, "", "TRUSTED_PROXIES"},
		{"IP address in PUBLIC_URL",
			map[string]string{"PUBLIC_URL": "https://203.0.113.10", "TLS_MODE": "off", "TRUSTED_PROXIES": "10.0.0.1/32"}, "PUBLIC_URL — IP-адрес", ""},
		{"domain in PUBLIC_URL",
			map[string]string{"PUBLIC_URL": "https://att.example.com", "TLS_MODE": "off", "TRUSTED_PROXIES": "10.0.0.1/32"}, "", "IP-адрес"},
		{"bare group name in LDAP_ADMIN_GROUPS", ldapEnv("ZoomAdmins;CN=Teachers,OU=Groups,DC=example,DC=com"),
			"LDAP_ADMIN_GROUPS: «ZoomAdmins» — только имя группы", "«CN=Teachers"},
		{"full DN in LDAP_ADMIN_GROUPS", ldapEnv("CN=ZoomAdmins,OU=Groups,DC=example,DC=com"), "", "только имя группы"},
		{"English is gone", map[string]string{"ZOOM_WEBHOOK_SECRET_TOKEN": ""}, "ZOOM_WEBHOOK_SECRET_TOKEN не задан", "is not set"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"DATA_DIR": t.TempDir(), "ZOOM_WEBHOOK_SECRET_TOKEN": "x", "TRUSTED_PROXIES": ""}
			for k, v := range tc.env {
				env[k] = v
			}
			cliEnv(t, env)
			code, out, errOut := runCLI(t, "", "check-config")
			if code != 0 {
				t.Fatalf("exit %d (a warning must not fail the check)\n%s%s", code, out, errOut)
			}
			if tc.want != "" && !strings.Contains(errOut, tc.want) {
				t.Errorf("warning %q missing:\n%s", tc.want, errOut)
			}
			if tc.not != "" && strings.Contains(errOut, tc.not) {
				t.Errorf("unexpected warning about %q:\n%s", tc.not, errOut)
			}
			if strings.Contains(errOut+out, "Strong-Pass-2026") {
				t.Error("ADMIN_PASSWORD printed")
			}
		})
	}
}

func ldapEnv(adminGroups string) map[string]string {
	return map[string]string{
		"LDAP_URL": "ldaps://dc.example.com:636", "LDAP_BASE_DN": "DC=example,DC=com",
		"LDAP_BIND_DN": "CN=svc,DC=example,DC=com", "LDAP_ADMIN_GROUPS": adminGroups,
	}
}

// check-ldap --user without --password-stdin says that the password was not
// checked, so a run that only found the account is not mistaken for a login test.
func TestCheckLDAPWithoutPassword(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens: the connection step fails at once
	cliEnv(t, map[string]string{"DATA_DIR": t.TempDir(), "LDAP_URL": "ldap://" + addr, "LDAP_BASE_DN": "dc=example,dc=org",
		"LDAP_PRESET": "openldap", "LDAP_BIND_DN": "cn=reader,dc=example,dc=org", "LDAP_BIND_PASSWORD": "x", "LDAP_TIMEOUT": "2s"})
	_, out, _ := runCLI(t, "", "check-ldap", "--user", "ivanov")
	if !strings.Contains(out, "Пароль не проверялся") || !strings.Contains(out, "--password-stdin") {
		t.Errorf("no note about the unchecked password:\n%s", out)
	}
	_, out, _ = runCLI(t, "", "check-ldap")
	if strings.Contains(out, "Пароль не проверялся") {
		t.Errorf("note printed without --user:\n%s", out)
	}
}

func TestFilePortWarning(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode, listen, public string
		want                 string // "" = no warning
	}{
		{config.TLSFile, ":8080", "https://att.example.com", "LISTEN_ADDR=:443"},
		{config.TLSFile, ":443", "https://att.example.com", ""},
		{config.TLSFile, "0.0.0.0:8443", "https://att.example.com:8443", ""},
		{config.TLSFile, ":443", "https://att.example.com:8443", "LISTEN_ADDR=:8443"},
		{config.TLSAuto, ":443", "https://att.example.com", ""},
		{config.TLSOff, ":8080", "https://att.example.com", ""},
	} {
		u, err := url.Parse(tc.public)
		if err != nil {
			t.Fatal(err)
		}
		got := filePortWarning(&config.Config{TLSMode: tc.mode, ListenAddr: tc.listen, PublicURL: u})
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s %s %s: %q, want %q", tc.mode, tc.listen, tc.public, got, tc.want)
		}
	}
}
