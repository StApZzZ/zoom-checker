package httpapi

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRealIP(t *testing.T) {
	t.Parallel()
	_, lo, _ := net.ParseCIDR("127.0.0.1/32")
	trusted := []*net.IPNet{lo}
	tests := []struct {
		name   string
		remote string
		xff    []string // header lines
		want   string
	}{
		{"two header lines: the proxy's line is last", "127.0.0.1:1234", []string{"1.2.3.4", "9.9.9.9"}, "9.9.9.9"},
		{"one comma-joined line", "127.0.0.1:1234", []string{"1.2.3.4, 9.9.9.9"}, "9.9.9.9"},
		{"trusted hop in the last line is skipped", "127.0.0.1:1234", []string{"1.2.3.4", "9.9.9.9, 127.0.0.1"}, "9.9.9.9"},
		{"untrusted peer: header ignored", "8.8.8.8:1", []string{"1.2.3.4"}, "8.8.8.8"},
		{"garbage right-most entry stops the walk", "127.0.0.1:1234", []string{"1.2.3.4", "not-an-ip"}, "127.0.0.1"},
		{"garbage behind a valid hop", "127.0.0.1:1234", []string{"garbage, 5.6.7.8"}, "5.6.7.8"},
		{"only trusted hops", "127.0.0.1:1234", []string{"127.0.0.1"}, "127.0.0.1"},
		{"no header", "127.0.0.1:1234", nil, "127.0.0.1"},
		{"IPv6 client", "127.0.0.1:1234", []string{"2001:db8::1"}, "2001:db8::1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got string
			h := realIP(trusted, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = clientIP(r) }))
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.remote
			for _, v := range tc.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			h.ServeHTTP(httptest.NewRecorder(), r)
			if got != tc.want {
				t.Errorf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLimiterIP(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"198.51.100.7":           "198.51.100.7",
		"::ffff:198.51.100.7":    "198.51.100.7",
		"2001:db8:1:2::1":        "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff::abc": "2001:db8:1:2::/64",
		"2001:db8:1:3::1":        "2001:db8:1:3::/64",
		"fe80::1%eth0":           "fe80::/64",
		"not-an-ip":              "not-an-ip",
	}
	for in, want := range tests {
		if got := limiterIP(in); got != want {
			t.Errorf("limiterIP(%q) = %q, want %q", in, got, want)
		}
	}
}
