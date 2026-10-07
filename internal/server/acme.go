package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/caddyserver/certmagic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"zoom-checker/internal/config"
)

// acmeProfileShortlived is the Let's Encrypt profile required for IP address
// certificates (160-hour lifetime, renewed automatically via ARI).
const acmeProfileShortlived = "shortlived"

// acmeManager wraps the certmagic objects used by TLS_MODE=auto.
type acmeManager struct {
	magic   *certmagic.Config
	issuer  *certmagic.ACMEIssuer
	cache   *certmagic.Cache
	hosts   []string
	profile string
	ca      string
}

// managedHosts returns the names/IPs to obtain certificates for: the PUBLIC_URL
// host first, then TLS_EXTRA_HOSTS, lower-cased and de-duplicated.
func managedHosts(cfg *config.Config) []string {
	var out []string
	add := func(h string) {
		h = strings.ToLower(strings.TrimSpace(h))
		h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
		if h != "" && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	add(cfg.PublicURL.Hostname())
	for _, h := range cfg.TLSExtraHosts {
		if hh, _, err := net.SplitHostPort(h); err == nil {
			h = hh
		}
		add(h)
	}
	return out
}

// acmeProfile picks the ACME profile: the configured one, else "shortlived" when
// any managed host is an IP address (Let's Encrypt only issues IP certificates
// with it), else the CA default ("").
func acmeProfile(configured string, hosts []string) string {
	if p := strings.TrimSpace(configured); p != "" {
		return p
	}
	for _, h := range hosts {
		if net.ParseIP(h) != nil {
			return acmeProfileShortlived
		}
	}
	return ""
}

// acmeCA maps TLS_ACME_CA to a directory URL.
func acmeCA(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "production", "prod", "letsencrypt":
		return certmagic.LetsEncryptProductionCA
	case "staging", "test":
		return certmagic.LetsEncryptStagingCA
	default:
		return strings.TrimSpace(v)
	}
}

// newACMEManager prepares certificate management (no network I/O yet).
func newACMEManager(cfg *config.Config, log *slog.Logger) *acmeManager {
	log = log.With("component", "acme")
	zl := zap.New(&slogCore{log: log})
	hosts := managedHosts(cfg)
	m := &acmeManager{hosts: hosts, profile: acmeProfile(cfg.TLSACMEProfile, hosts), ca: acmeCA(cfg.TLSACMECA)}

	m.cache = certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) { return m.magic, nil },
		Logger:           zl,
	})
	m.magic = certmagic.New(m.cache, certmagic.Config{
		Storage: &certmagic.FileStorage{Path: filepath.Join(cfg.DataDir, "certmagic")},
		// Clients never send SNI for an IP address; without SNI certmagic would
		// pick a certificate by the local address, which breaks behind NAT.
		DefaultServerName: strings.ToLower(cfg.PublicURL.Hostname()),
		Logger:            zl,
		OnEvent:           acmeEventLogger(log),
	})
	m.issuer = certmagic.NewACMEIssuer(m.magic, certmagic.ACMEIssuer{
		CA:      m.ca,
		Email:   cfg.TLSEmail,
		Agreed:  true,
		Profile: m.profile,
		Logger:  zl,
	})
	m.magic.Issuers = []certmagic.Issuer{m.issuer}
	return m
}

// tlsConfig is the :443 TLS configuration: certmagic certificates, the
// acme-tls/1 protocol for the TLS-ALPN challenge, HTTP/2 and HTTP/1.1.
func (m *acmeManager) tlsConfig() *tls.Config {
	tc := m.magic.TLSConfig()
	tc.NextProtos = append([]string{"h2", "http/1.1"}, tc.NextProtos...)
	tc.MinVersion = tls.VersionTLS12
	return tc
}

// manage starts obtaining/renewing certificates in the background. It returns
// quickly: the server keeps running (and answering on :80) while issuance is
// pending; results are logged by the event hook.
func (m *acmeManager) manage(ctx context.Context) error {
	if err := m.magic.ManageAsync(ctx, m.hosts); err != nil {
		return fmt.Errorf("certificate management for %s: %w", strings.Join(m.hosts, ", "), err)
	}
	return nil
}

func (m *acmeManager) stop() { m.cache.Stop() }

// acmeEventLogger turns certmagic events into short log lines. Failures carry a
// hint, because the usual cause is a closed port 80/443 or a wrong DNS record.
func acmeEventLogger(log *slog.Logger) func(ctx context.Context, event string, data map[string]any) error {
	return func(ctx context.Context, event string, data map[string]any) error {
		switch event {
		case "cert_obtaining":
			log.Info("requesting TLS certificate", "identifier", data["identifier"])
		case "cert_obtained":
			log.Info("TLS certificate obtained", "identifier", data["identifier"],
				"renewal", data["renewal"], "issuer", data["issuer"])
		case "cert_failed":
			log.Error("could not obtain TLS certificate; check that ports 80 and 443 are reachable from the internet and PUBLIC_URL points to this server",
				"identifier", data["identifier"], "renewal", data["renewal"], "err", data["error"])
		case "cached_managed_cert":
			log.Debug("TLS certificate loaded from storage", "sans", data["sans"])
		case "tls_get_certificate":
			// Every handshake; too noisy even for debug.
		default:
			log.Debug("acme event", "event", event)
		}
		return nil
	}
}

// slogCore is a minimal zapcore.Core that forwards certmagic's zap logs to slog,
// so all output shares one format. zap info is mapped to debug: certmagic's
// routine maintenance messages are not interesting to operators; the important
// outcomes are logged at info by acmeEventLogger.
type slogCore struct {
	log    *slog.Logger
	fields []zapcore.Field
}

func zapToSlog(l zapcore.Level) slog.Level {
	switch {
	case l >= zapcore.ErrorLevel:
		return slog.LevelError
	case l == zapcore.WarnLevel:
		return slog.LevelWarn
	default:
		return slog.LevelDebug
	}
}

func (c *slogCore) Enabled(l zapcore.Level) bool {
	return c.log.Enabled(context.Background(), zapToSlog(l))
}

func (c *slogCore) With(fields []zapcore.Field) zapcore.Core {
	return &slogCore{log: c.log, fields: append(slices.Clip(c.fields), fields...)}
}

func (c *slogCore) Check(e zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(e.Level) {
		return ce.AddCore(e, c)
	}
	return ce
}

func (c *slogCore) Write(e zapcore.Entry, fields []zapcore.Field) error {
	enc := zapcore.NewMapObjectEncoder()
	for _, f := range c.fields {
		f.AddTo(enc)
	}
	for _, f := range fields {
		f.AddTo(enc)
	}
	keys := make([]string, 0, len(enc.Fields))
	for k := range enc.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	attrs := make([]any, 0, 2*len(keys)+2)
	if e.LoggerName != "" {
		attrs = append(attrs, "logger", e.LoggerName)
	}
	for _, k := range keys {
		attrs = append(attrs, k, enc.Fields[k])
	}
	c.log.Log(context.Background(), zapToSlog(e.Level), e.Message, attrs...)
	return nil
}

func (c *slogCore) Sync() error { return nil }
