// Package server wires the service together: database, bootstrap admin,
// authentication providers, webhook processor, HTTP(S) listeners (plain,
// certificate files or automatic Let's Encrypt via certmagic), background jobs
// and graceful shutdown (docs/architecture.md §2, §11).
package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"zoom-checker/internal/auth"
	"zoom-checker/internal/config"
	"zoom-checker/internal/db"
	"zoom-checker/internal/httpapi"
	"zoom-checker/internal/processor"
	"zoom-checker/internal/realtime"
	"zoom-checker/internal/store"
	"zoom-checker/internal/version"
	"zoom-checker/web"
)

const (
	// httpShutdownTimeout bounds the wait for in-flight requests; together with
	// workerShutdownTimeout it stays below systemd's TimeoutStopSec=20.
	httpShutdownTimeout   = 15 * time.Second
	workerShutdownTimeout = 3 * time.Second
)

// Option customizes Run (mainly for tests).
type Option func(*options)

type options struct {
	onListen func(name string, addr net.Addr)
	static   fs.FS
	now      func() time.Time
}

// WithListenHook calls fn for every bound listener ("http", "https",
// "http-redirect") before serving starts — tests use it with
// LISTEN_ADDR=127.0.0.1:0 to learn the port.
func WithListenHook(fn func(name string, addr net.Addr)) Option {
	return func(o *options) { o.onListen = fn }
}

// WithStatic replaces the embedded SPA (web.Dist()).
func WithStatic(fsys fs.FS) Option { return func(o *options) { o.static = fsys } }

// WithClock replaces time.Now for the HTTP layer, the store, the webhook
// processor and background jobs.
func WithClock(now func() time.Time) Option { return func(o *options) { o.now = now } }

// Run starts the service and blocks until ctx is cancelled (graceful shutdown,
// returns nil) or a listener fails (returns the error).
func Run(ctx context.Context, cfg *config.Config, log *slog.Logger, opts ...Option) (err error) {
	o := options{now: time.Now}
	for _, opt := range opts {
		opt(&o)
	}
	if o.static == nil {
		o.static = web.Dist()
	}
	if log == nil {
		log = slog.Default()
	}

	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return fmt.Errorf("create DATA_DIR %s: %w", cfg.DataDir, err)
	}
	d, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() {
		if cerr := d.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close database: %w", cerr)
		}
	}()
	if err := d.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	st := store.New(d, store.WithClock(o.now))

	if err := bootstrapAdmin(ctx, st, cfg, log); err != nil {
		return err
	}
	secret, err := serverSecret(ctx, st)
	if err != nil {
		return err
	}

	var ldapAuth *auth.LDAP
	if cfg.LDAP.Enabled() {
		// NewLDAP itself warns about LDAP_INSECURE_SKIP_VERIFY and unencrypted
		// connections (once).
		if ldapAuth, err = auth.NewLDAP(cfg.LDAP, log.With("component", "ldap")); err != nil {
			return fmt.Errorf("LDAP settings: %w", err)
		}
	}
	var oidcAuth *auth.OIDC
	if cfg.OIDC.Enabled() {
		key := auth.DeriveKey(secret, "oidc-flow-cookie")
		if oidcAuth, err = auth.NewOIDC(cfg.OIDC, cfg.PublicURL, key, log.With("component", "oidc")); err != nil {
			return fmt.Errorf("OIDC settings: %w", err)
		}
	}

	broker := realtime.NewBroker()
	proc := newProcessor(cfg, st, broker, log, o.now)
	api := httpapi.New(httpapi.Deps{
		Config:    cfg,
		Store:     st,
		Processor: proc,
		Broker:    broker,
		LDAP:      ldapAuth,
		OIDC:      oidcAuth,
		Log:       log,
		Static:    o.static,
		Now:       o.now,
		CookieKey: auth.DeriveKey(secret, "known-device-cookie"),
	})

	// Workers outlive the HTTP servers during shutdown (in-flight webhooks may
	// still call Notify), so they get their own cancellation.
	workCtx, stopWork := context.WithCancel(context.WithoutCancel(ctx))
	defer stopWork()

	group := newHTTPGroup(log)
	var acme *acmeManager
	addrs, err := bindListeners(cfg, log, api.Handler(), group, &acme, o.onListen)
	if acme != nil {
		defer acme.stop() // the certificate cache runs a maintenance goroutine
	}
	if err != nil {
		group.closeListeners()
		return err
	}

	var workers sync.WaitGroup
	workers.Go(func() {
		if err := proc.Run(workCtx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("webhook processor stopped", "err", err)
		}
	})
	j := &jobs{cfg: cfg, db: d, st: st, log: log.With("component", "jobs"), now: o.now}
	workers.Go(func() { j.run(workCtx) })
	workers.Go(func() { runMemTrimmer(workCtx, memTrimEvery) })

	group.serve()
	if acme != nil {
		if err := acme.manage(ctx); err != nil {
			log.Error("automatic HTTPS could not start", "err", err)
		}
	}
	logStartup(log, cfg, addrs, ldapAuth != nil, oidcAuth != nil, acme)

	var runErr error
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case runErr = <-group.errc:
		log.Error("HTTP server failed; shutting down", "err", runErr)
	}

	group.shutdown(httpShutdownTimeout)
	stopWork()
	// Lesson rebuilds the HTTP layer started in the background (after roster or
	// settings changes) and the workers stop together, before the deferred
	// database close; one shared deadline keeps the stop below systemd's limit.
	stopCtx, cancelStop := context.WithTimeout(context.Background(), workerShutdownTimeout)
	defer cancelStop()
	if err := api.Close(stopCtx); err != nil {
		log.Warn("background lesson rebuilds did not stop in time", "err", err)
	}
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-stopCtx.Done():
		log.Warn("background workers did not stop in time")
	}
	log.Info("stopped")
	return runErr
}

// newProcessor builds the webhook processor with the service clock and time
// zone (TIMEZONE is used in the texts it writes, e.g. the audit entry of an
// automatic reopen).
func newProcessor(cfg *config.Config, st *store.Store, broker *realtime.Broker, log *slog.Logger, now func() time.Time) *processor.Processor {
	return processor.New(st, broker, log.With("component", "processor"),
		processor.WithClock(now), processor.WithLocation(cfg.Timezone))
}

// bindListeners creates and binds the HTTP servers for the TLS mode. Ports are
// bound before anything is served so that configuration problems fail the start.
func bindListeners(cfg *config.Config, log *slog.Logger, h http.Handler, g *httpGroup,
	acmeOut **acmeManager, onListen func(string, net.Addr)) ([]string, error) {
	var addrs []string
	bind := func(name, addr string, srv *http.Server, useTLS bool) error {
		a, err := g.listen(name, addr, srv, useTLS)
		if err != nil {
			return err
		}
		addrs = append(addrs, name+"="+a.String())
		if onListen != nil {
			onListen(name, a)
		}
		return nil
	}

	switch cfg.TLSMode {
	case config.TLSFile:
		rl, err := newCertReloader(cfg.TLSCertFile, cfg.TLSKeyFile, log)
		if err != nil {
			return nil, err
		}
		srv := g.newServer(h)
		srv.TLSConfig = rl.tlsConfig()
		return addrs, bind("https", cfg.ListenAddr, srv, true)

	case config.TLSAuto:
		m := newACMEManager(cfg, log)
		*acmeOut = m
		httpsPort := cfg.PublicURL.Port()
		if httpsPort == "" {
			httpsPort = "443"
		}
		tlsSrv := g.newServer(h)
		tlsSrv.TLSConfig = m.tlsConfig()
		if err := bind("https", ":"+httpsPort, tlsSrv, true); err != nil {
			return nil, err
		}
		// Port 80 answers the ACME HTTP-01 challenge and /healthz; the rest is
		// redirected to HTTPS.
		redirect := redirectToHTTPS(m.hosts, strings.ToLower(cfg.PublicURL.Hostname()), httpsPort)
		httpSrv := g.newServer(m.issuer.HTTPChallengeHandler(redirect))
		return addrs, bind("http-redirect", ":80", httpSrv, false)

	default:
		return addrs, bind("http", cfg.ListenAddr, g.newServer(h), false)
	}
}

func logStartup(log *slog.Logger, cfg *config.Config, addrs []string, ldapOn, oidcOn bool, acme *acmeManager) {
	var methods []string
	if cfg.LocalAuthEnabled {
		methods = append(methods, "local")
	}
	if ldapOn {
		methods = append(methods, "ldap")
	}
	if oidcOn {
		methods = append(methods, "oidc")
	}
	attrs := []any{
		"version", version.String(),
		"url", cfg.PublicURL.String(),
		"webhook_url", cfg.WebhookURL(),
		"db", cfg.DBDriver(),
		"auth", strings.Join(methods, ","),
		"tls", cfg.TLSMode,
		"listen", strings.Join(addrs, " "),
		"data_dir", cfg.DataDir,
		"timezone", cfg.Timezone.String(),
	}
	if acme != nil {
		profile := acme.profile
		if profile == "" {
			profile = "default"
		}
		attrs = append(attrs, "tls_hosts", strings.Join(acme.hosts, ","), "acme_ca", acme.ca, "acme_profile", profile)
	}
	log.Info("Zoom Checker started", attrs...)
	if cfg.ZoomWebhookSecret == "" {
		log.Warn("ZOOM_WEBHOOK_SECRET_TOKEN is not set: Zoom webhooks are refused until it is configured")
	}
	if d := cfg.EventsRetentionDays; d > 0 && d < MinEventsRetentionDays {
		log.Warn("EVENTS_RETENTION_DAYS is too short; raw Zoom events are kept longer so that recent Zoom sessions can still be linked to lessons",
			"configured_days", d, "applied_days", MinEventsRetentionDays)
	}
	if cfg.TLSMode == config.TLSOff && cfg.PublicHTTPS() && len(cfg.TrustedProxies) == 0 {
		log.Warn(ProxyWithoutTrustMessage)
	}
}

// ProxyWithoutTrustMessage explains what an empty TRUSTED_PROXIES behind a
// reverse proxy costs (server start log and check-config).
const ProxyWithoutTrustMessage = "PUBLIC_URL is https but TLS_MODE=off, so a reverse proxy terminates HTTPS, and TRUSTED_PROXIES is empty: " +
	"every user appears with the proxy's address and all of them share one login-attempt limit " +
	"(anyone's failed logins can lock everybody out for 15 minutes). Set TRUSTED_PROXIES to the proxy's address"
