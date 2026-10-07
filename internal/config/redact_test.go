package config

import (
	"strings"
	"testing"
)

func TestRedactDatabaseURL(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"postgres://u:p@h/db":                                               "postgres://u:xxxxx@h/db",
		"postgres://u@h/db":                                                 "postgres://u@h/db",
		"postgres://h/db":                                                   "postgres://h/db",
		"postgres://u:p@ss@h/d":                                             "postgres://u:xxxxx@h/d",
		"postgres://u:p/s?s@h/d":                                            "postgres://u:xxxxx@h/d",
		"postgres://zc:pa%ss@db/zc":                                         "postgres://zc:xxxxx@db/zc",
		"postgres://zc:pa/ss@db:5432/zc":                                    "postgres://zc:xxxxx@db:5432/zc",
		"postgres://h/db?user=u&password=s3cret&sslmode=disable":            "postgres://h/db?user=u&password=xxxxx&sslmode=disable",
		"postgresql://h/db?sslpassword=k3y":                                 "postgresql://h/db?sslpassword=xxxxx",
		"postgres://u:p@h/db?PASSWORD=x":                                    "postgres://u:xxxxx@h/db?PASSWORD=xxxxx",
		"postgres://zc@db:5432/zc?sslmode=require&password=S&sslpassword=K": "postgres://zc@db:5432/zc?sslmode=require&password=xxxxx&sslpassword=xxxxx",
		"mysql://root:secret@localhost/db":                                  "mysql://root:xxxxx@localhost/db",
		"sqlite:///var/lib/zoom-checker/zoom-checker.db":                    "sqlite:///var/lib/zoom-checker/zoom-checker.db",
	}
	for in, want := range tests {
		if got := RedactDatabaseURL(in); got != want {
			t.Errorf("RedactDatabaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRedactedHidesDatabasePasswords(t *testing.T) {
	t.Parallel()
	for _, dbURL := range []string{
		"postgres://zc@db:5432/zc?sslmode=require&password=SuperSecretPw&sslpassword=KeyPw",
		"postgres://zc:SuperSecretPw/x@db:5432/zc",
		"postgres://zc:Super%SecretPw@db:5432/zc",
	} {
		env := map[string]string{"DATABASE_URL": dbURL}
		c, err := Load(func(k string) string { return env[k] })
		if err != nil {
			t.Fatalf("Load(%q): %v", dbURL, err)
		}
		got := c.Redacted()["DATABASE_URL"]
		if strings.Contains(got, "SuperSecretPw") || strings.Contains(got, "KeyPw") || strings.Contains(got, "Secret") {
			t.Errorf("Redacted DATABASE_URL = %q leaks a password", got)
		}
	}
}
