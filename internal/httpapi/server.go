// Package httpapi is the HTTP layer: JSON API (docs/architecture.md §6), Zoom webhook
// endpoint, OIDC callbacks, SSE streams and the embedded SPA.
//
// Route groups live in routes_*.go files; each registers itself from (*Server).routes.
package httpapi

import (
	"context"
	"crypto/rand"
	"io/fs"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"zoom-checker/internal/auth"
	"zoom-checker/internal/config"
	"zoom-checker/internal/realtime"
	"zoom-checker/internal/store"
)

// Processor is the part of internal/processor the HTTP layer needs.
// It is an interface so handlers can be tested with a fake.
type Processor interface {
	// Notify wakes the webhook worker after a raw event was stored.
	Notify()
	// RebuildLesson recomputes the attendance projection of a lesson (docs §5.6)
	// and publishes realtime updates.
	RebuildLesson(ctx context.Context, lessonID int64) error
	// RelinkLesson must be called after a lesson was created or its meeting id /
	// planned time changed: links matching unlinked sessions and unlinks auto-linked
	// sessions that no longer match (docs §5.5 step 4), then rebuilds.
	RelinkLesson(ctx context.Context, lessonID int64) error
	// LinkSession manually links (lessonID != nil) or unlinks a Zoom session
	// (link_mode becomes manual) and rebuilds the affected lessons.
	LinkSession(ctx context.Context, sessionID int64, lessonID *int64) error
}

// Deps are the collaborators of the HTTP server.
type Deps struct {
	Config    *config.Config
	Store     *store.Store
	Processor Processor
	Broker    *realtime.Broker
	LDAP      *auth.LDAP // nil when LDAP is disabled
	OIDC      *auth.OIDC // nil when OIDC is disabled
	Log       *slog.Logger
	Static    fs.FS            // built SPA (web.Dist())
	Now       func() time.Time // defaults to time.Now
	// CookieKey signs the known-device cookie of the login limits (32 bytes,
	// derived from the server secret so it survives restarts). A random key
	// is used when it is empty.
	CookieKey []byte
}

// Server holds the HTTP handlers.
type Server struct {
	Deps
	loginLimiter   *auth.LoginLimiter // per IP+username, and per known device+username
	ipLimiter      *auth.LoginLimiter // per IP (stops spraying many usernames)
	accountLimiter *auth.LoginLimiter // per username, any address (stops distributed guessing)
	deviceKey      []byte             // HMAC key of the known-device cookie
	started        time.Time
	viewClock      atomic.Int64 // newest lesson view time handed out (unix ms); see lessonNow
	handler        http.Handler
	bg             *background   // work that outlives a request; see Close
	importSlots    chan struct{} // student list imports in progress (acquireImport)
	zoomClock      webhookClock  // server clock vs Zoom's request timestamps
}

// New builds the server and its routing table.
func New(d Deps) *Server {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	s := &Server{
		Deps:         d,
		loginLimiter: auth.NewLoginLimiter(10, 15*time.Minute),
		ipLimiter:    auth.NewLoginLimiter(60, 15*time.Minute),
		// Generous enough that one school's NAT never reaches it, while many
		// addresses together get about 120 guesses an hour per account. The
		// price: someone who knows a username can block its form login from
		// browsers that never logged into it, for up to 15 minutes.
		accountLimiter: auth.NewLoginLimiter(30, 15*time.Minute),
		deviceKey:      d.CookieKey,
		started:        d.Now(),
		bg:             newBackground(),
		importSlots:    make(chan struct{}, grpImportSlots),
	}
	if len(s.deviceKey) == 0 {
		s.deviceKey = make([]byte, 32)
		_, _ = rand.Read(s.deviceKey)
	}
	mux := http.NewServeMux()
	s.routes(mux)

	cop := http.NewCrossOriginProtection()
	// Zoom calls the webhook server-to-server; it is authenticated by HMAC instead.
	cop.AddInsecureBypassPattern("POST /api/webhooks/zoom")
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, s.Log, errForbidden("Запрос отклонён: он пришёл с другого сайта"))
	}))

	var h http.Handler = mux
	h = cop.Handler(h)
	h = logRequests(s.Log, h)
	h = securityHeaders(d.Config.PublicHTTPS(), h)
	h = realIP(d.Config.TrustedProxies, h)
	h = recoverPanics(s.Log, h)
	s.handler = h
	return s
}

// Handler returns the root handler.
func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := s.Store.DB().PingContext(ctx); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})

	s.registerAuth(mux)    // routes_auth.go
	s.registerAdmin(mux)   // routes_admin.go
	s.registerZoom(mux)    // routes_zoom.go
	s.registerGroups(mux)  // routes_groups.go
	s.registerLessons(mux) // routes_lessons.go

	// Unknown API paths must be JSON 404s, not the SPA.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, s.Log, errNotFound("Неизвестный адрес API"))
	})
	if s.Static != nil {
		mux.Handle("/", spaHandler(s.Static))
	}
}

// Route wrappers. Every handler returns an error that is rendered as the JSON envelope.

// public serves h without authentication.
func (s *Server) public(h handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			writeError(w, r, s.Log, err)
		}
	}
}

// authed requires a logged-in, enabled user (teacher or admin) whose password
// does not need to be changed.
func (s *Server) authed(h handlerFunc) http.HandlerFunc {
	return s.withSession(false, false, h)
}

// admin requires a logged-in administrator.
func (s *Server) admin(h handlerFunc) http.HandlerFunc {
	return s.withSession(true, false, h)
}

// authedAllowPasswordChange is like authed but also lets users that must change
// their password through (me, logout, change password).
func (s *Server) authedAllowPasswordChange(h handlerFunc) http.HandlerFunc {
	return s.withSession(false, true, h)
}

func (s *Server) now() time.Time { return s.Now().UTC() }
