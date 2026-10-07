package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// HTTP server limits (docs/architecture.md §11). WriteTimeout stays 0 because
// SSE responses live for hours; handlers that need a write deadline set it
// themselves through http.ResponseController.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 60 * time.Second
	idleTimeout       = 120 * time.Second
	maxHeaderBytes    = 64 << 10
)

// httpGroup runs the HTTP(S) servers of one process and shuts them down together.
type httpGroup struct {
	log     *slog.Logger
	servers []*boundServer
	errc    chan error
	serving sync.WaitGroup

	// baseCtx is the parent of every request context; cancelled only when a
	// graceful shutdown runs out of time.
	baseCtx    context.Context
	cancelBase context.CancelFunc
	// streamsCtx ends long-lived event streams (SSE) as soon as shutdown starts,
	// so they do not hold the graceful shutdown open until the deadline.
	streamsCtx  context.Context
	stopStreams context.CancelFunc
}

type boundServer struct {
	name string
	srv  *http.Server
	ln   net.Listener
	tls  bool
}

func newHTTPGroup(log *slog.Logger) *httpGroup {
	g := &httpGroup{log: log, errc: make(chan error, 4)}
	g.baseCtx, g.cancelBase = context.WithCancel(context.Background())
	g.streamsCtx, g.stopStreams = context.WithCancel(context.Background())
	return g
}

// newServer returns an http.Server with the service-wide limits.
func (g *httpGroup) newServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           g.wrapStreams(h),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      0,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		// TLS handshake failures from scanners and bots are noise at info level.
		ErrorLog:    slog.NewLogLogger(g.log.Handler(), slog.LevelDebug),
		BaseContext: func(net.Listener) context.Context { return g.baseCtx },
	}
}

// listen binds addr for srv. Binding happens before serving so that a busy or
// privileged port fails the start with a clear message.
func (g *httpGroup) listen(name, addr string, srv *http.Server, useTLS bool) (net.Addr, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		hint := ""
		if errors.Is(err, syscall.EACCES) || errors.Is(err, os.ErrPermission) {
			hint = " (ports below 1024 need root or CAP_NET_BIND_SERVICE; the systemd unit grants it)"
		} else if errors.Is(err, syscall.EADDRINUSE) {
			hint = " (another program already uses this port)"
		}
		return nil, fmt.Errorf("listen on %s for %s: %w%s", addr, name, err, hint)
	}
	g.servers = append(g.servers, &boundServer{name: name, srv: srv, ln: ln, tls: useTLS})
	return ln.Addr(), nil
}

// closeListeners releases bound ports when the start is aborted before serving.
func (g *httpGroup) closeListeners() {
	for _, s := range g.servers {
		_ = s.ln.Close()
	}
}

// serve starts every bound server. A server that stops with an error other than
// http.ErrServerClosed is reported on errc.
func (g *httpGroup) serve() {
	for _, s := range g.servers {
		g.serving.Go(func() {
			var err error
			if s.tls {
				err = s.srv.ServeTLS(s.ln, "", "")
			} else {
				err = s.srv.Serve(s.ln)
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				select {
				case g.errc <- fmt.Errorf("%s server on %s: %w", s.name, s.ln.Addr(), err):
				default:
				}
			}
		})
	}
}

// shutdown stops accepting connections, ends event streams, waits up to timeout
// for in-flight requests and then forcibly closes what is left.
func (g *httpGroup) shutdown(timeout time.Duration) {
	g.stopStreams()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, s := range g.servers {
		wg.Go(func() {
			if err := s.srv.Shutdown(ctx); err != nil {
				g.log.Warn("HTTP server did not stop in time; closing remaining connections", "server", s.name, "err", err)
				g.cancelBase()
				_ = s.srv.Close()
			}
		})
	}
	wg.Wait()
	g.cancelBase()
	g.serving.Wait()
}

// wrapStreams gives event-stream requests a context that ends when shutdown
// starts, so open lesson screens do not hold the graceful shutdown until its
// deadline. It also lifts the server-wide read deadline for them: current
// net/http already clears it before the handler runs on HTTP/1.1, but a stream
// lives for hours and must never depend on ReadTimeout.
func (g *httpGroup) wrapStreams(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isEventStream(r) {
			_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
			ctx, cancel := context.WithCancel(r.Context())
			stop := context.AfterFunc(g.streamsCtx, cancel)
			defer func() {
				stop()
				cancel()
			}()
			r = r.WithContext(ctx)
		}
		h.ServeHTTP(w, r)
	})
}

// isEventStream reports whether r opens a Server-Sent Events stream
// (GET /api/stream, GET /api/lessons/{id}/stream, or any EventSource request).
func isEventStream(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	return strings.HasSuffix(r.URL.Path, "/stream") ||
		strings.Contains(r.Header.Get("Accept"), "text/event-stream")
}

// healthz answers liveness probes on the plain-HTTP port in TLS auto mode
// (install.sh and the Docker HEALTHCHECK probe http://127.0.0.1/healthz).
func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte("ok"))
}

// redirectToHTTPS serves GET /healthz itself and permanently redirects every
// other request to https:// on the same host and path. The Host header is only
// trusted when it is one of the managed hosts; otherwise the redirect goes to
// defaultHost, so the port-80 listener cannot be used to bounce users elsewhere.
func redirectToHTTPS(hosts []string, defaultHost, httpsPort string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(hostOnly(r.Host))
		if !slices.Contains(hosts, host) {
			host = defaultHost
		}
		authority := host
		if httpsPort != "" && httpsPort != "443" {
			authority = net.JoinHostPort(host, httpsPort)
		} else if strings.Contains(host, ":") {
			authority = "[" + host + "]" // IPv6 literal
		}
		http.Redirect(w, r, "https://"+authority+r.URL.RequestURI(), http.StatusPermanentRedirect)
	})
	return mux
}

// hostOnly strips the port and IPv6 brackets from a Host header value.
func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]")
}

// certReloader serves a certificate from files (TLS_MODE=file) and picks up
// renewed files without a restart (checked at most every 30 s, on handshakes).
type certReloader struct {
	certFile, keyFile string
	log               *slog.Logger

	mu       sync.Mutex
	cert     *tls.Certificate
	certMod  time.Time
	keyMod   time.Time
	lastStat time.Time
}

func newCertReloader(certFile, keyFile string, log *slog.Logger) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile, log: log}
	if err := r.load(); err != nil {
		return nil, err
	}
	r.lastStat = time.Now()
	return r, nil
}

func (r *certReloader) load() error {
	cs, err := os.Stat(r.certFile)
	if err != nil {
		return fmt.Errorf("TLS_CERT_FILE: %w", err)
	}
	ks, err := os.Stat(r.keyFile)
	if err != nil {
		return fmt.Errorf("TLS_KEY_FILE: %w", err)
	}
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return fmt.Errorf("load TLS_CERT_FILE/TLS_KEY_FILE: %w", err)
	}
	r.cert, r.certMod, r.keyMod = &cert, cs.ModTime(), ks.ModTime()
	return nil
}

func (r *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.lastStat) >= 30*time.Second {
		r.lastStat = time.Now()
		cs, err1 := os.Stat(r.certFile)
		ks, err2 := os.Stat(r.keyFile)
		if err1 == nil && err2 == nil && (!cs.ModTime().Equal(r.certMod) || !ks.ModTime().Equal(r.keyMod)) {
			if err := r.load(); err != nil {
				r.log.Warn("could not reload the TLS certificate; keeping the previous one", "err", err)
			} else {
				r.log.Info("TLS certificate reloaded", "cert_file", r.certFile)
			}
		}
	}
	return r.cert, nil
}

func (r *certReloader) tlsConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: r.GetCertificate,
		NextProtos:     []string{"h2", "http/1.1"},
		MinVersion:     tls.VersionTLS12,
	}
}
