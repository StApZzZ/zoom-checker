package httpapi

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

type ctxKey int

const (
	ctxClientIP ctxKey = iota
	ctxUser
	ctxSession
)

// clientIP returns the client address resolved by the realIP middleware.
func clientIP(r *http.Request) string {
	if v, ok := r.Context().Value(ctxClientIP).(string); ok {
		return v
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// realIP resolves the client IP, trusting X-Forwarded-For only from trusted proxies.
// The right-most untrusted address in X-Forwarded-For is the client. Repeated
// X-Forwarded-For header lines are one comma-separated list in arrival order
// (RFC 9110 §5.3): proxies such as HAProxy append their own line after the
// client's, so reading only the first line would trust what the client wrote.
func realIP(trusted []*net.IPNet, next http.Handler) http.Handler {
	isTrusted := func(ip net.IP) bool {
		for _, n := range trusted {
			if n.Contains(ip) {
				return true
			}
		}
		return false
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := host
		if len(trusted) > 0 && isTrusted(net.ParseIP(host)) {
			if xff := strings.Join(r.Header.Values("X-Forwarded-For"), ","); xff != "" {
				parts := strings.Split(xff, ",")
				for i := len(parts) - 1; i >= 0; i-- {
					cand := strings.TrimSpace(parts[i])
					pip := net.ParseIP(cand)
					if pip == nil {
						break
					}
					ip = cand
					if !isTrusted(pip) {
						break
					}
				}
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxClientIP, ip)))
	})
}

// statusRecorder captures the status code; Unwrap keeps http.ResponseController
// (Flush, SetWriteDeadline) working for SSE.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// logRequests logs one line per request. Health checks and static assets go to debug.
func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		level := slog.LevelInfo
		p := r.URL.Path
		if p == "/healthz" || p == "/readyz" || strings.HasPrefix(p, "/assets/") ||
			(r.Method == http.MethodGet && !strings.HasPrefix(p, "/api/") && !strings.HasPrefix(p, "/auth/")) {
			level = slog.LevelDebug
		}
		if rec.status >= 500 {
			level = slog.LevelError
		}
		// Never log query strings: OIDC callbacks carry codes/state there.
		log.Log(r.Context(), level, "http",
			"method", r.Method, "path", p, "status", rec.status,
			"bytes", rec.bytes, "dur_ms", time.Since(start).Milliseconds(), "ip", clientIP(r))
	})
}

// recoverPanics converts panics into 500 responses.
func recoverPanics(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				log.Error("panic", "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				writeError(w, r, log, errInternal(nil))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// securityHeaders sets defensive headers on every response (docs/architecture.md §6).
func securityHeaders(https bool, next http.Handler) http.Handler {
	const csp = "default-src 'self'; img-src 'self' data:; font-src 'self' data:; style-src 'self' 'unsafe-inline'; " +
		"script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if https {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}
