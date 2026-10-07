package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"zoom-checker/internal/auth"
	"zoom-checker/internal/config"
	"zoom-checker/internal/db"
	"zoom-checker/internal/model"
	"zoom-checker/internal/store"
)

func testConfig(t *testing.T, env map[string]string) *config.Config {
	t.Helper()
	base := map[string]string{
		"LISTEN_ADDR": "127.0.0.1:0",
		"DATA_DIR":    t.TempDir(),
		"TIMEZONE":    "Europe/Moscow",
	}
	for k, v := range env {
		base[k] = v
	}
	cfg, err := config.Load(func(k string) string { return base[k] })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type running struct {
	addrs  map[string]net.Addr
	cancel context.CancelFunc
	done   chan error
	logs   *syncBuffer
}

// start runs the server in the background and waits until it listens.
func start(t *testing.T, cfg *config.Config) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{addrs: map[string]net.Addr{}, cancel: cancel, done: make(chan error, 1), logs: &syncBuffer{}}
	var mu sync.Mutex
	listening := make(chan struct{}, 4)
	log := slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	static := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>test</title>")}}
	go func() {
		r.done <- Run(ctx, cfg, log, WithStatic(static), WithListenHook(func(name string, a net.Addr) {
			mu.Lock()
			r.addrs[name] = a
			mu.Unlock()
			listening <- struct{}{}
		}))
	}()
	select {
	case <-listening:
	case err := <-r.done:
		t.Fatalf("Run returned before listening: %v\nlogs:\n%s", err, r.logs.String())
	case <-time.After(20 * time.Second):
		t.Fatalf("server did not start\nlogs:\n%s", r.logs.String())
	}
	t.Cleanup(func() { r.stop(t) })
	return r
}

func (r *running) stop(t *testing.T) {
	t.Helper()
	if r.cancel == nil {
		return
	}
	r.cancel()
	r.cancel = nil
	select {
	case err := <-r.done:
		if err != nil {
			t.Errorf("Run returned %v", err)
		}
	case <-time.After(25 * time.Second):
		t.Fatalf("graceful shutdown did not finish\nlogs:\n%s", r.logs.String())
	}
}

func get(t *testing.T, client *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func openStore(t *testing.T, cfg *config.Config) *store.Store {
	t.Helper()
	d, err := db.Open(context.Background(), cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return store.New(d)
}

func TestRunSmokeTLSOff(t *testing.T) {
	cfg := testConfig(t, nil)
	r := start(t, cfg)
	base := "http://" + r.addrs["http"].String()

	if code, body := get(t, http.DefaultClient, base+"/healthz"); code != 200 || body != "ok" {
		t.Fatalf("/healthz = %d %q", code, body)
	}
	if code, _ := get(t, http.DefaultClient, base+"/readyz"); code != 200 {
		t.Fatalf("/readyz = %d", code)
	}
	if code, body := get(t, http.DefaultClient, base+"/lessons/1"); code != 200 || !strings.Contains(body, "<title>test</title>") {
		t.Fatalf("SPA fallback = %d %q", code, body)
	}

	// Bootstrap: generated password in a 0600 file, only the path is logged.
	pwFile := InitialPasswordPath(cfg.DataDir)
	fi, err := os.Stat(pwFile)
	if err != nil {
		t.Fatalf("initial password file: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("password file mode %v, want 0600", fi.Mode().Perm())
	}
	pwData, _ := os.ReadFile(pwFile)
	password := strings.TrimSpace(string(pwData))
	if len(password) < 12 {
		t.Fatalf("generated password too short: %q", password)
	}
	if strings.Contains(r.logs.String(), password) {
		t.Fatal("the generated password was logged")
	}
	if !strings.Contains(r.logs.String(), pwFile) {
		t.Error("the password file path was not logged")
	}

	// A startup backup is made (no backup yet).
	deadline := time.Now().Add(10 * time.Second)
	for {
		names, _ := listBackups(filepath.Join(cfg.DataDir, "backups"))
		if len(names) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no startup backup; logs:\n%s", r.logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.stop(t)

	st := openStore(t, cfg)
	ctx := context.Background()
	users, err := st.ListUsers(ctx)
	if err != nil || len(users) != 1 {
		t.Fatalf("users after first start: %v %d", err, len(users))
	}
	admin := users[0]
	if admin.Username != "admin" || admin.Role != model.RoleAdmin || admin.AuthSource != model.AuthLocal || !admin.MustChangePassword {
		t.Errorf("bootstrap admin: %+v", admin)
	}
	if ok, _ := auth.VerifyPassword(admin.PasswordHash, password); !ok {
		t.Error("password in the file does not match the admin account")
	}
	secret1, err := st.GetSetting(ctx, "server_secret")
	if err != nil || len(secret1) != 64 {
		t.Fatalf("server secret: %v %q", err, secret1)
	}

	// Second start: no new admin, same secret, password file kept (still valid).
	r2 := start(t, cfg)
	if code, _ := get(t, http.DefaultClient, "http://"+r2.addrs["http"].String()+"/healthz"); code != 200 {
		t.Fatal("second start not healthy")
	}
	r2.stop(t)
	if n, _ := st.CountUsers(ctx); n != 1 {
		t.Fatalf("users after second start: %d", n)
	}
	if s, _ := st.GetSetting(ctx, "server_secret"); s != secret1 {
		t.Error("server secret changed between starts")
	}
	if _, err := os.Stat(pwFile); err != nil {
		t.Error("password file removed although the password is still valid")
	}

	// After the admin changed the password the stale file is removed at start.
	hash, _ := auth.HashPassword("a-new-password-123")
	admin.PasswordHash, admin.MustChangePassword = hash, false
	if err := st.UpdateUser(ctx, &admin); err != nil {
		t.Fatal(err)
	}
	r3 := start(t, cfg)
	r3.stop(t)
	if _, err := os.Stat(pwFile); !os.IsNotExist(err) {
		t.Errorf("stale password file not removed: %v", err)
	}
}

func TestRunWithAdminPassword(t *testing.T) {
	cfg := testConfig(t, map[string]string{"ADMIN_USERNAME": "Boss", "ADMIN_PASSWORD": "Strong-Pass-2026", "BACKUP_KEEP": "0"})
	r := start(t, cfg)
	r.stop(t)
	if _, err := os.Stat(InitialPasswordPath(cfg.DataDir)); !os.IsNotExist(err) {
		t.Error("password file written although ADMIN_PASSWORD is set")
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "backups")); !os.IsNotExist(err) {
		t.Error("backup made although BACKUP_KEEP=0")
	}
	st := openStore(t, cfg)
	u, err := st.GetUserByUsername(context.Background(), model.AuthLocal, "boss")
	if err != nil {
		t.Fatal(err)
	}
	if u.MustChangePassword {
		t.Error("admin from ADMIN_PASSWORD must not be forced to change it")
	}
	if ok, _ := auth.VerifyPassword(u.PasswordHash, "Strong-Pass-2026"); !ok {
		t.Error("ADMIN_PASSWORD does not verify")
	}
}

func TestRunListenError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cfg := testConfig(t, map[string]string{"LISTEN_ADDR": ln.Addr().String(), "BACKUP_KEEP": "0"})
	err = Run(context.Background(), cfg, slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("Run on a busy port = %v", err)
	}
}

func TestRunTLSFile(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	writeSelfSigned(t, certFile, keyFile, "first")
	cfg := testConfig(t, map[string]string{
		"PUBLIC_URL": "https://localhost", "TLS_MODE": "file",
		"TLS_CERT_FILE": certFile, "TLS_KEY_FILE": keyFile, "BACKUP_KEEP": "0",
	})
	r := start(t, cfg)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := client.Get("https://" + r.addrs["https"].String() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if resp.Header.Get("Strict-Transport-Security") == "" {
		t.Error("HSTS missing for https PUBLIC_URL")
	}
}

func writeSelfSigned(t *testing.T, certFile, keyFile, cn string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCertReloader(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	writeSelfSigned(t, certFile, keyFile, "first")
	rl, err := newCertReloader(certFile, keyFile, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	cn := func() string {
		c, err := rl.GetCertificate(nil)
		if err != nil {
			t.Fatal(err)
		}
		leaf, _ := x509.ParseCertificate(c.Certificate[0])
		return leaf.Subject.CommonName
	}
	if cn() != "first" {
		t.Fatal("initial certificate not served")
	}
	writeSelfSigned(t, certFile, keyFile, "second")
	future := time.Now().Add(time.Minute)
	os.Chtimes(certFile, future, future)
	rl.mu.Lock()
	rl.lastStat = time.Time{}
	rl.mu.Unlock()
	if cn() != "second" {
		t.Fatal("renewed certificate not picked up")
	}
	// A broken file keeps the previous certificate.
	os.WriteFile(certFile, []byte("garbage"), 0o600)
	os.Chtimes(certFile, future.Add(time.Minute), future.Add(time.Minute))
	rl.mu.Lock()
	rl.lastStat = time.Time{}
	rl.mu.Unlock()
	if cn() != "second" {
		t.Fatal("broken file replaced the working certificate")
	}
	if _, err := newCertReloader(filepath.Join(dir, "none"), keyFile, slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("missing certificate accepted")
	}
}

func TestShutdownEndsStreamsAndDrainsRequests(t *testing.T) {
	t.Parallel()
	g := newHTTPGroup(slog.New(slog.DiscardHandler))
	streamEnded := make(chan struct{})
	streamStarted := make(chan struct{})
	slowStarted := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		http.NewResponseController(w).Flush()
		close(streamStarted)
		<-r.Context().Done()
		close(streamEnded)
	})
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		close(slowStarted)
		time.Sleep(300 * time.Millisecond)
		if r.Context().Err() != nil {
			http.Error(w, "cancelled", 500)
			return
		}
		w.Write([]byte("done"))
	})
	srv := g.newServer(mux)
	addr, err := g.listen("http", "127.0.0.1:0", srv, false)
	if err != nil {
		t.Fatal(err)
	}
	g.serve()
	base := "http://" + addr.String()

	go func() {
		resp, err := http.Get(base + "/api/stream")
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	<-streamStarted
	slowResult := make(chan string, 1)
	go func() {
		resp, err := http.Get(base + "/slow")
		if err != nil {
			slowResult <- err.Error()
			return
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		slowResult <- string(b)
	}()
	<-slowStarted

	began := time.Now()
	g.shutdown(10 * time.Second)
	if d := time.Since(began); d > 3*time.Second {
		t.Fatalf("shutdown took %s: the stream kept it open", d)
	}
	select {
	case <-streamEnded:
	default:
		t.Fatal("stream handler context was not cancelled")
	}
	if got := <-slowResult; got != "done" {
		t.Fatalf("in-flight request = %q, want it to finish normally", got)
	}
}

// TestStreamsOutliveReadTimeout: an SSE stream must stay open (its request
// context alive, its events delivered) long after the server's ReadTimeout,
// which is 60 s in production while lesson screens stay open for hours.
func TestStreamsOutliveReadTimeout(t *testing.T) {
	t.Parallel()
	const readTimeout = 200 * time.Millisecond
	g := newHTTPGroup(slog.New(slog.DiscardHandler))
	result := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		rc := http.NewResponseController(w)
		_ = rc.Flush()
		select {
		case <-r.Context().Done():
			result <- "cancelled"
			return
		case <-time.After(5 * readTimeout):
		}
		_, _ = w.Write([]byte("event: late\n\n"))
		_ = rc.Flush()
		result <- "alive"
	})
	srv := g.newServer(mux)
	srv.ReadTimeout = readTimeout
	addr, err := g.listen("http", "127.0.0.1:0", srv, false)
	if err != nil {
		t.Fatal(err)
	}
	g.serve()
	defer g.shutdown(5 * time.Second)

	resp, err := http.Get("http://" + addr.String() + "/api/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := <-result; got != "alive" {
		t.Fatalf("stream after ReadTimeout: %s", got)
	}
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "event: late\n" {
		t.Fatalf("event after ReadTimeout not delivered: %q %v", line, err)
	}
}

func TestIsEventStream(t *testing.T) {
	t.Parallel()
	cases := []struct {
		method, path, accept string
		want                 bool
	}{
		{"GET", "/api/stream", "", true},
		{"GET", "/api/lessons/5/stream", "", true},
		{"GET", "/api/lessons/5", "text/event-stream", true},
		{"GET", "/api/lessons/5", "application/json", false},
		{"POST", "/api/stream", "", false},
	}
	for _, c := range cases {
		r, _ := http.NewRequest(c.method, c.path, nil)
		if c.accept != "" {
			r.Header.Set("Accept", c.accept)
		}
		if got := isEventStream(r); got != c.want {
			t.Errorf("%s %s %q = %v", c.method, c.path, c.accept, got)
		}
	}
}

// LDAP_INSECURE_SKIP_VERIFY is warned about once at start, not twice.
func TestRunWarnsOnceAboutInsecureLDAP(t *testing.T) {
	cfg := testConfig(t, map[string]string{"BACKUP_KEEP": "0",
		"LDAP_URL": "ldaps://127.0.0.1:1", "LDAP_INSECURE_SKIP_VERIFY": "true", "LDAP_PRESET": "openldap",
		"LDAP_BASE_DN": "dc=example,dc=org", "LDAP_BIND_DN": "cn=reader,dc=example,dc=org", "LDAP_BIND_PASSWORD": "x"})
	r := start(t, cfg)
	r.stop(t)
	if n := strings.Count(r.logs.String(), "LDAP_INSECURE_SKIP_VERIFY"); n != 1 {
		t.Fatalf("warned %d times about LDAP_INSECURE_SKIP_VERIFY:\n%s", n, r.logs.String())
	}
}
