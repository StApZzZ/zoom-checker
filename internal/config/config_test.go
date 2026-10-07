package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// LoadEnvFile changes the process environment, so these tests do not run in
// parallel and register every key with t.Setenv for cleanup.
func TestLoadEnvFile(t *testing.T) {
	const p = "ZC_TEST_ENVFILE_"
	tests := []struct {
		name    string
		file    string
		preset  map[string]string // real environment before loading
		want    map[string]string // expected values; "<unset>" = must stay unset
		wantErr string
	}{
		{
			name: "dotenv syntax",
			file: "# comment\n\n" + p + "A=plain\nexport " + p + "B=exported\n" + p + "C=\"double # not a comment\"\n" +
				p + "D='single'\n" + p + "E=value   # inline comment\n" + p + "F=\n" + p + "G=a#b\n",
			want: map[string]string{p + "A": "plain", p + "B": "exported", p + "C": "double # not a comment",
				p + "D": "single", p + "E": "value", p + "F": "", p + "G": "a#b"},
		},
		{
			// A quoted value with a comment after it: the quotes must not stay in
			// the value (a quoted Zoom secret would fail every signature check).
			name: "quoted value followed by an inline comment",
			file: p + "A=\"s3cret #1\"   # from the Zoom app\n" + p + "B='single' # c\n" +
				p + "C=\"say \\\"hi\\\"\" # escaped quotes\n" + p + "D=\"a\"b # not quoted\n" +
				p + "E=\"open # never closed\n" + p + "F=\"x\"#no space\n",
			want: map[string]string{p + "A": "s3cret #1", p + "B": "single", p + "C": `say "hi"`,
				p + "D": `"a"b`, p + "E": `"open`, p + "F": `"x"#no space`},
		},
		{
			// Admins append corrections to the end of the file; systemd, Docker
			// and the shell use the last value, and so must check-config.
			name: "last occurrence wins",
			file: p + "TZ=Europe/Moscow\n" + p + "OTHER=x\n" + p + "TZ=Mars/Base\n",
			want: map[string]string{p + "TZ": "Mars/Base", p + "OTHER": "x"},
		},
		{
			name:   "real environment overrides the file",
			file:   p + "A=from-file\n" + p + "A=from-file-again\n" + p + "B=b\n",
			preset: map[string]string{p + "A": "from-env"},
			want:   map[string]string{p + "A": "from-env", p + "B": "b"},
		},
		{
			name:    "malformed line sets nothing",
			file:    p + "A=1\nBROKEN LINE\n" + p + "B=2\n",
			want:    map[string]string{p + "A": "<unset>", p + "B": "<unset>"},
			wantErr: ":2: ожидается строка вида КЛЮЧ=значение",
		},
		{
			name:    "empty key",
			file:    "=value\n",
			wantErr: ":1: ожидается строка вида КЛЮЧ=значение",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range []string{"A", "B", "C", "D", "E", "F", "G", "TZ", "OTHER"} {
				t.Setenv(p+k, "")
				os.Unsetenv(p + k)
			}
			for k, v := range tt.preset {
				t.Setenv(k, v)
			}
			path := filepath.Join(t.TempDir(), "zc.env")
			if err := os.WriteFile(path, []byte(tt.file), 0o600); err != nil {
				t.Fatal(err)
			}
			err := LoadEnvFile(path)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for k, want := range tt.want {
				got, ok := os.LookupEnv(k)
				if want == "<unset>" {
					if ok {
						t.Errorf("%s = %q, want unset", k, got)
					}
					continue
				}
				if !ok || got != want {
					t.Errorf("%s = %q (set %v), want %q", k, got, ok, want)
				}
			}
		})
	}
}

func TestLoadEnvFileMissing(t *testing.T) {
	t.Parallel()
	if err := LoadEnvFile(filepath.Join(t.TempDir(), "missing.env")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestSessionLifetimes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		env           map[string]string
		wantLocal     time.Duration
		wantExternal  time.Duration
		wantErrSubstr string
	}{
		{"defaults", nil, 168 * time.Hour, 24 * time.Hour, ""},
		{"custom", map[string]string{"SESSION_TTL": "12h", "EXTERNAL_SESSION_TTL": "8h"}, 12 * time.Hour, 8 * time.Hour, ""},
		{"bad external", map[string]string{"EXTERNAL_SESSION_TTL": "a day"}, 0, 0, "EXTERNAL_SESSION_TTL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, err := Load(func(k string) string { return tc.env[k] })
			if tc.wantErrSubstr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Fatalf("Load error = %v, want it to mention %s", err, tc.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.SessionTTL != tc.wantLocal || c.ExternalSessionTTL != tc.wantExternal {
				t.Errorf("SessionTTL=%v ExternalSessionTTL=%v, want %v %v", c.SessionTTL, c.ExternalSessionTTL, tc.wantLocal, tc.wantExternal)
			}
		})
	}
}
