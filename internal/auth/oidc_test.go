package auth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"zoom-checker/internal/config"
	"zoom-checker/internal/model"
)

const (
	testClientID     = "zoom-checker"
	testClientSecret = "s3cret"
)

var (
	testKey  = sync.OnceValue(func() *rsa.PrivateKey { k, _ := rsa.GenerateKey(rand.Reader, 2048); return k })
	otherKey = sync.OnceValue(func() *rsa.PrivateKey { k, _ := rsa.GenerateKey(rand.Reader, 2048); return k })
)

// grant describes what the fake token endpoint issues for one code.
type grant struct {
	nonce, challenge string
	id, at           map[string]any // claims added to / overriding the defaults
	atKey            *rsa.PrivateKey
	opaqueAT         bool
	badAtHash        bool
}

// fakeIdP is a minimal OpenID provider: discovery, JWKS, token, userinfo,
// authorization (for Diagnose) and end_session endpoints.
type fakeIdP struct {
	t   *testing.T
	srv *httptest.Server

	mu            sync.Mutex
	codes         map[string]*grant
	tokenCalls    int
	discoveryDown bool
	noEndSession  bool
	issuer        string // override the advertised issuer
	userinfo      map[string]any
}

func newFakeIdP(t *testing.T) *fakeIdP {
	f := &fakeIdP{t: t, codes: map[string]*grant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", f.discovery)
	mux.HandleFunc("GET /jwks", f.jwks)
	mux.HandleFunc("POST /token", f.token)
	mux.HandleFunc("GET /userinfo", f.userInfo)
	mux.HandleFunc("GET /auth", f.authorize)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) url() string { return f.srv.URL }

func (f *fakeIdP) discovery(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.discoveryDown {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	iss := f.srv.URL
	if f.issuer != "" {
		iss = f.issuer
	}
	m := map[string]any{
		"issuer":                                iss,
		"authorization_endpoint":                f.srv.URL + "/auth",
		"token_endpoint":                        f.srv.URL + "/token",
		"jwks_uri":                              f.srv.URL + "/jwks",
		"userinfo_endpoint":                     f.srv.URL + "/userinfo",
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"plain", "S256"},
	}
	if !f.noEndSession {
		m["end_session_endpoint"] = f.srv.URL + "/logout?ui_locales=ru"
	}
	writeJSON(w, http.StatusOK, m)
}

func (f *fakeIdP) jwks(w http.ResponseWriter, _ *http.Request) {
	pub := testKey().PublicKey
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
		"n": b64url(pub.N.Bytes()), "e": b64url(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}

func (f *fakeIdP) clientOK(r *http.Request) bool {
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	} else {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	}
	return id == testClientID && secret == testClientSecret
}

func (f *fakeIdP) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenCalls++
	if !f.clientOK(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized_client", "error_description": "Invalid client or Invalid client credentials"})
		return
	}
	if r.PostForm.Get("grant_type") == "client_credentials" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized_client", "error_description": "Client not enabled to retrieve service account"})
		return
	}
	g := f.codes[r.PostForm.Get("code")]
	delete(f.codes, r.PostForm.Get("code")) // one-time codes
	if g == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "Code not valid"})
		return
	}
	if oauth2.S256ChallengeFromVerifier(r.PostForm.Get("code_verifier")) != g.challenge {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "PKCE verification failed"})
		return
	}
	if r.PostForm.Get("redirect_uri") != "http://app.test/auth/oidc/callback" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "Incorrect redirect_uri"})
		return
	}
	now := time.Now().Unix()
	base := map[string]any{"iss": f.srv.URL, "sub": "user-123", "iat": now, "exp": now + 300}

	var at string
	if g.opaqueAT {
		at = "opaque-" + randomString(16)
	} else {
		claims := maps.Clone(base)
		maps.Copy(claims, map[string]any{"aud": "account", "azp": testClientID, "typ": "Bearer", "scope": "openid profile email"})
		maps.Copy(claims, g.at)
		key := g.atKey
		if key == nil {
			key = testKey()
		}
		at = signJWT(key, claims)
	}
	id := maps.Clone(base)
	maps.Copy(id, map[string]any{
		"aud": testClientID, "azp": testClientID, "nonce": g.nonce, "at_hash": atHash(at),
		"preferred_username": "Ivanova", "email": "ivanova@example.test",
		"given_name": "Мария", "family_name": "Иванова",
	})
	if g.badAtHash {
		id["at_hash"] = atHash("something else")
	}
	maps.Copy(id, g.id)
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": at, "token_type": "Bearer", "expires_in": 300, "id_token": signJWT(testKey(), id),
	})
}

func (f *fakeIdP) userInfo(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	claims := map[string]any{"sub": "user-123"}
	maps.Copy(claims, f.userinfo)
	writeJSON(w, http.StatusOK, claims)
}

func (f *fakeIdP) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != testClientID || q.Get("redirect_uri") != "http://app.test/auth/oidc/callback" {
		http.Error(w, "Invalid parameter: redirect_uri", http.StatusBadRequest)
		return
	}
	_, _ = w.Write([]byte("<html>login form</html>"))
}

func (f *fakeIdP) addCode(g *grant) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	code := randomString(12)
	f.codes[code] = g
	return code
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func signJWT(key *rsa.PrivateKey, claims map[string]any) string {
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "k1"})
	c, _ := json.Marshal(claims)
	in := b64url(h) + "." + b64url(c)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return in + "." + b64url(sig)
}

func atHash(at string) string {
	sum := sha256.Sum256([]byte(at))
	return b64url(sum[:16])
}

func newTestOIDC(t *testing.T, idp *fakeIdP, mod func(*config.OIDCConfig)) *OIDC {
	t.Helper()
	cfg := config.OIDCConfig{
		IssuerURL:    idp.url(),
		ClientID:     testClientID,
		ClientSecret: testClientSecret,
		Scopes:       []string{"profile", "email"},
		AdminRoles:   []string{"zoom-checker-admin"},
		TeacherRoles: []string{"zoom-checker-teacher"},
	}
	if mod != nil {
		mod(&cfg)
	}
	pub, _ := url.Parse("http://app.test")
	o, err := NewOIDC(cfg, pub, DeriveKey([]byte("server secret"), "oidc-flow-cookie"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

type callbackFn func(q url.Values, cookies []*http.Cookie) (url.Values, []*http.Cookie)

// login runs StartLogin, lets the fake IdP issue a code for g and calls
// HandleCallback, optionally tampering with the callback request.
func login(t *testing.T, o *OIDC, idp *fakeIdP, g *grant, tamper callbackFn) (*Identity, string, string, error, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	authURL, err := o.StartLogin(rec, httptest.NewRequest("GET", "http://app.test/auth/oidc/login", nil), "/lessons/7")
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	u, _ := url.Parse(authURL)
	aq := u.Query()
	for k, want := range map[string]string{"response_type": "code", "client_id": testClientID,
		"redirect_uri": "http://app.test/auth/oidc/callback", "code_challenge_method": "S256"} {
		if aq.Get(k) != want {
			t.Fatalf("auth URL %s=%q, want %q", k, aq.Get(k), want)
		}
	}
	if !strings.HasPrefix(aq.Get("scope"), "openid ") || len(aq.Get("state")) < 40 || len(aq.Get("nonce")) < 40 || aq.Get("code_challenge") == "" {
		t.Fatalf("auth URL lacks scope/state/nonce/PKCE: %s", authURL)
	}
	g.nonce, g.challenge = cmpOr(g.nonce, aq.Get("nonce")), aq.Get("code_challenge")
	q := url.Values{"code": {idp.addCode(g)}, "state": {aq.Get("state")}, "session_state": {"x"}, "iss": {idp.url()}}
	cookies := rec.Result().Cookies()
	if tamper != nil {
		q, cookies = tamper(q, cookies)
	}
	req := httptest.NewRequest("GET", "http://app.test/auth/oidc/callback?"+q.Encode(), nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec2 := httptest.NewRecorder()
	id, raw, next, err := o.HandleCallback(rec2, req)
	return id, raw, next, err, rec2
}

func realmRoles(roles ...string) map[string]any {
	return map[string]any{"realm_access": map[string]any{"roles": anySlice(roles)}}
}

func anySlice(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

func TestOIDCLoginRolesFromAccessToken(t *testing.T) {
	t.Parallel()
	idp := newFakeIdP(t)
	o := newTestOIDC(t, idp, nil)

	// The flow cookie set by StartLogin.
	rec := httptest.NewRecorder()
	if _, err := o.StartLogin(rec, httptest.NewRequest("GET", "/", nil), "//evil.com"); err != nil {
		t.Fatal(err)
	}
	c := rec.Result().Cookies()[0]
	if c.Name != "zc_oidc" || c.Path != "/auth/oidc" || !c.HttpOnly || c.Secure || c.SameSite != http.SameSiteLaxMode || c.MaxAge != 600 {
		t.Fatalf("flow cookie attributes: %+v", c)
	}
	if f, err := o.open(c.Value); err != nil || f.Next != "/" {
		t.Fatalf("next must be sanitized to /: %+v %v", f, err)
	}

	id, raw, next, err, rec2 := login(t, o, idp, &grant{at: realmRoles("offline_access", "zoom-checker-teacher")}, nil)
	if err != nil {
		t.Fatalf("HandleCallback: %v", err)
	}
	want := Identity{Source: model.AuthOIDC, ExternalID: "user-123", Username: "ivanova", DisplayName: "Мария Иванова",
		Email: "ivanova@example.test", Role: model.RoleTeacher}
	got := *id
	got.Groups = nil
	if got.Source != want.Source || got.ExternalID != want.ExternalID || got.Username != want.Username ||
		got.DisplayName != want.DisplayName || got.Email != want.Email || got.Role != want.Role {
		t.Fatalf("identity = %+v, want %+v", got, want)
	}
	if next != "/lessons/7" || strings.Count(raw, ".") != 2 {
		t.Fatalf("next=%q raw id token=%q", next, raw)
	}
	cleared := rec2.Result().Cookies()
	if len(cleared) != 1 || cleared[0].Name != "zc_oidc" || cleared[0].MaxAge >= 0 || cleared[0].Path != "/auth/oidc" {
		t.Fatalf("flow cookie must be deleted (one-shot): %+v", cleared)
	}
}

func TestOIDCRoleSources(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		cfg      func(*config.OIDCConfig)
		g        grant
		userinfo map[string]any
		want     model.Role
	}{
		{"client roles in access token", func(c *config.OIDCConfig) { c.AdminRoles = []string{"admin"} },
			grant{at: map[string]any{"resource_access": map[string]any{testClientID: map[string]any{"roles": []any{"admin"}}}}}, nil, model.RoleAdmin},
		{"other client's roles do not count", func(c *config.OIDCConfig) { c.AdminRoles = []string{"admin"} },
			grant{at: map[string]any{"resource_access": map[string]any{"account": map[string]any{"roles": []any{"admin"}}}}}, nil, ""},
		{"group path in id token", func(c *config.OIDCConfig) { c.TeacherRoles = []string{"zoom-teachers"} },
			grant{id: map[string]any{"groups": []any{"/staff/zoom-teachers"}}}, nil, model.RoleTeacher},
		{"realm_roles in id token", nil, grant{id: map[string]any{"realm_roles": []any{"zoom-checker-admin"}}}, nil, model.RoleAdmin},
		{"roles claim", nil, grant{id: map[string]any{"roles": "zoom-checker-teacher"}}, nil, model.RoleTeacher},
		{"userinfo groups", func(c *config.OIDCConfig) { c.TeacherRoles = []string{"teachers"} },
			grant{}, map[string]any{"groups": []any{"teachers"}}, model.RoleTeacher},
		{"userinfo with another subject is ignored", func(c *config.OIDCConfig) { c.TeacherRoles = []string{"teachers"} },
			grant{}, map[string]any{"sub": "intruder", "groups": []any{"teachers"}}, ""},
		{"extra dot-path claim", func(c *config.OIDCConfig) { c.RolesClaim = "attendance.roles"; c.TeacherRoles = []string{"teacher"} },
			grant{id: map[string]any{"attendance": map[string]any{"roles": []any{"teacher"}}}}, nil, model.RoleTeacher},
		{"namespaced claim", func(c *config.OIDCConfig) {
			c.RolesClaim = "https://zc.example/roles"
			c.TeacherRoles = []string{"teacher"}
		},
			grant{id: map[string]any{"https://zc.example/roles": []any{"teacher"}}}, nil, model.RoleTeacher},
		{"star teacher", func(c *config.OIDCConfig) { c.TeacherRoles = []string{"*"} }, grant{}, nil, model.RoleTeacher},
		{"several audiences with our azp", nil,
			grant{id: map[string]any{"aud": []any{testClientID, "account"}, "realm_roles": []any{"zoom-checker-teacher"}}}, nil, model.RoleTeacher},
		{"id token without azp", nil,
			grant{id: map[string]any{"azp": "", "realm_roles": []any{"zoom-checker-teacher"}}}, nil, model.RoleTeacher},
		{"opaque access token, roles in id token", nil,
			grant{opaqueAT: true, id: map[string]any{"realm_roles": []any{"zoom-checker-teacher"}}}, nil, model.RoleTeacher},
		{"access token azp mismatch", nil,
			grant{at: map[string]any{"azp": "other-client", "realm_access": map[string]any{"roles": []any{"zoom-checker-admin"}}}}, nil, ""},
		{"access token for another subject", nil,
			grant{at: map[string]any{"sub": "someone-else", "realm_access": map[string]any{"roles": []any{"zoom-checker-admin"}}}}, nil, ""},
		{"forged access token", nil, grant{atKey: otherKey(), at: realmRoles("zoom-checker-admin")}, nil, ""},
		{"expired access token", nil, grant{at: map[string]any{"exp": time.Now().Add(-time.Hour).Unix(), "realm_access": map[string]any{"roles": []any{"zoom-checker-admin"}}}}, nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			idp := newFakeIdP(t)
			idp.userinfo = c.userinfo
			o := newTestOIDC(t, idp, c.cfg)
			g := c.g
			id, raw, _, err, _ := login(t, o, idp, &g, nil)
			if c.want == "" {
				var denied *AccessDeniedError
				if !errors.As(err, &denied) || !errors.Is(err, ErrAccessDenied) {
					t.Fatalf("want AccessDeniedError, got %v (%+v)", err, id)
				}
				if raw == "" {
					t.Fatal("raw id token must be returned with AccessDeniedError (for IdP logout)")
				}
				return
			}
			if err != nil || id.Role != c.want {
				t.Fatalf("got %+v, %v; want role %q", id, err, c.want)
			}
		})
	}
}

func TestOIDCMissingRoles(t *testing.T) {
	t.Parallel()
	idp := newFakeIdP(t)
	o := newTestOIDC(t, idp, nil)
	id, raw, next, err, _ := login(t, o, idp, &grant{at: realmRoles("offline_access", "uma_authorization")}, nil)
	var denied *AccessDeniedError
	if id != nil || !errors.As(err, &denied) {
		t.Fatalf("want AccessDeniedError, got %+v %v", id, err)
	}
	if strings.Join(denied.Required, ",") != "zoom-checker-teacher,zoom-checker-admin" || denied.Username != "ivanova" ||
		!strings.Contains(strings.Join(denied.Found, ","), "uma_authorization") {
		t.Fatalf("AccessDeniedError = %+v", denied)
	}
	if raw == "" || next != "/lessons/7" {
		t.Fatalf("raw=%q next=%q", raw, next)
	}
	if !strings.Contains(o.LogoutURL(raw), "id_token_hint=") {
		t.Fatal("denied users can be logged out at the IdP")
	}
}

func TestOIDCFlowFailures(t *testing.T) {
	t.Parallel()
	setQ := func(k, v string) callbackFn {
		return func(q url.Values, c []*http.Cookie) (url.Values, []*http.Cookie) { q.Set(k, v); return q, c }
	}
	cases := []struct {
		name          string
		g             grant
		tamper        callbackFn
		advance       time.Duration
		wantTokenCall bool
	}{
		{"nonce mismatch", grant{nonce: "attacker-nonce"}, nil, 0, true},
		{"state mismatch", grant{}, setQ("state", "forged"), 0, false},
		{"empty state", grant{}, setQ("state", ""), 0, false},
		{"missing cookie", grant{}, func(q url.Values, _ []*http.Cookie) (url.Values, []*http.Cookie) { return q, nil }, 0, false},
		{"tampered cookie", grant{}, func(q url.Values, c []*http.Cookie) (url.Values, []*http.Cookie) {
			v := []byte(c[0].Value)
			v[len(v)/2] ^= 1
			return q, []*http.Cookie{{Name: c[0].Name, Value: string(v)}}
		}, 0, false},
		{"expired cookie", grant{}, nil, 11 * time.Minute, false},
		{"provider error", grant{}, setQ("error", "access_denied"), 0, false},
		{"issuer mix-up", grant{}, setQ("iss", "https://evil.example"), 0, false},
		{"missing code", grant{}, setQ("code", ""), 0, false},
		{"unknown code", grant{}, setQ("code", "nope"), 0, true},
		{"at_hash mismatch", grant{badAtHash: true}, nil, 0, true},
		{"id token for another client", grant{id: map[string]any{"aud": "other"}}, nil, 0, true},
		{"id token azp names another client", grant{id: map[string]any{"azp": "other"}}, nil, 0, true},
		{"several audiences without azp", grant{id: map[string]any{"aud": []any{testClientID, "other"}, "azp": ""}}, nil, 0, true},
		{"id token from another issuer", grant{id: map[string]any{"iss": "https://evil.example"}}, nil, 0, true},
		{"expired id token", grant{id: map[string]any{"exp": time.Now().Add(-time.Minute).Unix()}}, nil, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			idp := newFakeIdP(t)
			o := newTestOIDC(t, idp, func(cfg *config.OIDCConfig) { cfg.TeacherRoles = []string{"*"} })
			if c.advance != 0 {
				// The browser comes back after StartLogin: move the clock then.
				c.tamper = func(q url.Values, ck []*http.Cookie) (url.Values, []*http.Cookie) {
					o.now = func() time.Time { return time.Now().Add(c.advance) }
					return q, ck
				}
			}
			g := c.g
			id, _, _, err, rec := login(t, o, idp, &g, c.tamper)
			if id != nil || !errors.Is(err, ErrOIDCFlow) {
				t.Fatalf("want ErrOIDCFlow, got %+v, %v", id, err)
			}
			if UserMessage(err) != "Не удалось завершить вход. Попробуйте ещё раз." {
				t.Fatalf("UserMessage = %q", UserMessage(err))
			}
			idp.mu.Lock()
			calls := idp.tokenCalls
			idp.mu.Unlock()
			if (calls > 0) != c.wantTokenCall {
				t.Fatalf("token endpoint calls = %d, want called=%v", calls, c.wantTokenCall)
			}
			if ck := rec.Result().Cookies(); len(ck) != 1 || ck[0].MaxAge >= 0 {
				t.Fatalf("flow cookie must be deleted even on failure: %+v", ck)
			}
		})
	}
}

func TestOIDCCodeReplay(t *testing.T) {
	t.Parallel()
	idp := newFakeIdP(t)
	o := newTestOIDC(t, idp, func(c *config.OIDCConfig) { c.TeacherRoles = []string{"*"} })
	var saved url.Values
	var savedCookies []*http.Cookie
	_, _, _, err, _ := login(t, o, idp, &grant{}, func(q url.Values, c []*http.Cookie) (url.Values, []*http.Cookie) {
		saved, savedCookies = q, c
		return q, c
	})
	if err != nil {
		t.Fatal(err)
	}
	// An attacker replays the captured callback (same cookie and code).
	req := httptest.NewRequest("GET", "http://app.test/auth/oidc/callback?"+saved.Encode(), nil)
	for _, c := range savedCookies {
		req.AddCookie(c)
	}
	if _, _, _, err := o.HandleCallback(httptest.NewRecorder(), req); !errors.Is(err, ErrOIDCFlow) {
		t.Fatalf("replayed code must fail, got %v", err)
	}
}

func TestOIDCWrongClientSecret(t *testing.T) {
	t.Parallel()
	idp := newFakeIdP(t)
	o := newTestOIDC(t, idp, func(c *config.OIDCConfig) { c.ClientSecret = "wrong"; c.TeacherRoles = []string{"*"} })
	if _, _, _, err, _ := login(t, o, idp, &grant{}, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("misconfigured secret must be ErrUnavailable, got %v", err)
	}
}

func TestOIDCLazyDiscovery(t *testing.T) {
	t.Parallel()
	idp := newFakeIdP(t)
	idp.discoveryDown = true
	o := newTestOIDC(t, idp, nil)
	clock := time.Now()
	o.now = func() time.Time { return clock }
	start := func() error {
		_, err := o.StartLogin(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil), "/")
		return err
	}
	if err := start(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("IdP down: want ErrUnavailable, got %v", err)
	}
	if o.LogoutURL("x") != "" {
		t.Fatal("LogoutURL without provider must be empty")
	}
	idp.mu.Lock()
	idp.discoveryDown = false
	idp.mu.Unlock()
	if err := start(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("within the back-off the cached failure is returned, got %v", err)
	}
	clock = clock.Add(discoveryBackoff + time.Second)
	if err := start(); err != nil {
		t.Fatalf("after the back-off discovery is retried: %v", err)
	}
	idp.mu.Lock()
	idp.discoveryDown = true
	idp.mu.Unlock()
	if err := start(); err != nil {
		t.Fatalf("the discovered provider is cached: %v", err)
	}
}

func TestOIDCIssuerTrailingSlash(t *testing.T) {
	t.Parallel()
	idp := newFakeIdP(t)
	idp.issuer = idp.url() + "/"
	o := newTestOIDC(t, idp, nil)
	p, err := o.provider(t.Context())
	if err != nil || p.issuer != idp.url()+"/" {
		t.Fatalf("issuer differing only by a trailing slash must be accepted: %v", err)
	}
	idp2 := newFakeIdP(t)
	idp2.issuer = "https://other.example/realms/x"
	if _, err := newTestOIDC(t, idp2, nil).provider(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("issuer mismatch must fail, got %v", err)
	}
}

func TestOIDCLogoutURL(t *testing.T) {
	t.Parallel()
	idp := newFakeIdP(t)
	o := newTestOIDC(t, idp, nil)
	u, err := url.Parse(o.LogoutURL("raw.id.token"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Path != "/logout" || q.Get("id_token_hint") != "raw.id.token" || q.Get("client_id") != testClientID ||
		q.Get("post_logout_redirect_uri") != "http://app.test/login" || q.Get("ui_locales") != "ru" {
		t.Fatalf("LogoutURL = %s", u)
	}
	if q := mustParse(t, o.LogoutURL("")).Query(); q.Has("id_token_hint") {
		t.Fatal("no id_token_hint without a token")
	}
	idp2 := newFakeIdP(t)
	idp2.noEndSession = true
	if got := newTestOIDC(t, idp2, nil).LogoutURL("raw"); got != "" {
		t.Fatalf("no end_session_endpoint: want empty, got %q", got)
	}
}

func mustParse(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestOIDCCookieAndRedirectWithHTTPSAndPrefix(t *testing.T) {
	t.Parallel()
	idp := newFakeIdP(t)
	pub, _ := url.Parse("https://school.example/zc/")
	o, err := NewOIDC(config.OIDCConfig{IssuerURL: idp.url(), ClientID: testClientID, Scopes: []string{"openid"}}, pub, make([]byte, 32), nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.RedirectURL() != "https://school.example/zc/auth/oidc/callback" {
		t.Fatalf("RedirectURL = %s", o.RedirectURL())
	}
	rec := httptest.NewRecorder()
	if _, err := o.StartLogin(rec, httptest.NewRequest("GET", "/", nil), "/"); err != nil {
		t.Fatal(err)
	}
	c := rec.Result().Cookies()[0]
	if !c.Secure || c.Path != "/zc/auth/oidc" {
		t.Fatalf("cookie: %+v", c)
	}
	if strings.Count(o.cfg.Scopes[0], "openid") != 1 || len(o.cfg.Scopes) != 1 {
		t.Fatalf("scopes: %q", o.cfg.Scopes)
	}
}

func TestFlowSealOpen(t *testing.T) {
	t.Parallel()
	idp := newFakeIdP(t)
	o := newTestOIDC(t, idp, nil)
	f := flowState{State: "s", Nonce: "n", Verifier: "v", Next: "/x", Created: time.Now().Unix()}
	v, err := o.seal(f)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := o.open(v); err != nil || got != f {
		t.Fatalf("open(seal(f)) = %+v, %v", got, err)
	}
	if v2, _ := o.seal(f); v2 == v {
		t.Fatal("sealing must use a random nonce")
	}
	if strings.Contains(v, "\"s\"") || strings.Contains(string(mustB64(t, v)), "\"v\":\"v\"") {
		t.Fatal("cookie must be encrypted")
	}
	raw := mustB64(t, v)
	for i := range raw {
		b := append([]byte(nil), raw...)
		b[i] ^= 0x80
		if _, err := o.open(b64url(b)); err == nil {
			t.Fatalf("tampering byte %d not detected", i)
		}
	}
	other, _ := NewOIDC(o.cfg, mustParse(t, "http://app.test"), DeriveKey([]byte("another secret"), "oidc-flow-cookie"), nil)
	if _, err := other.open(v); err == nil {
		t.Fatal("cookie sealed with another key must be rejected")
	}
	for _, bad := range []string{"", "!!!", b64url([]byte("short"))} {
		if _, err := o.open(bad); err == nil {
			t.Fatalf("open(%q) must fail", bad)
		}
	}
	old := f
	old.Created = time.Now().Add(-11 * time.Minute).Unix()
	v, _ = o.seal(old)
	if _, err := o.open(v); err == nil {
		t.Fatal("expired flow must be rejected")
	}
	evil := f
	evil.Next = "//evil.com"
	v, _ = o.seal(evil)
	if got, _ := o.open(v); got.Next != "/" {
		t.Fatalf("next must be re-sanitized: %q", got.Next)
	}
}

// TestFlowCookieSize: a next path that JSON-escapes into a huge cookie must
// not break the login (browsers drop cookies over 4 KiB); it falls back to "/".
func TestFlowCookieSize(t *testing.T) {
	t.Parallel()
	idp := newFakeIdP(t)
	o := newTestOIDC(t, idp, nil)
	for _, c := range []struct{ next, want string }{
		{"/" + strings.Repeat("a", 2040), "/" + strings.Repeat("a", 2040)},
		{"/" + strings.Repeat("<", 2040), "/"},
		{"/" + strings.Repeat("я", 1000), "/" + strings.Repeat("я", 1000)},
	} {
		rec := httptest.NewRecorder()
		if _, err := o.StartLogin(rec, httptest.NewRequest("GET", "/", nil), c.next); err != nil {
			t.Fatal(err)
		}
		ck := rec.Result().Cookies()[0]
		if len(ck.Value) > maxFlowCookieLen {
			t.Fatalf("cookie value is %d bytes", len(ck.Value))
		}
		if f, err := o.open(ck.Value); err != nil || f.Next != c.want {
			t.Fatalf("next %.20q...: got %.20q, %v", c.next, f.Next, err)
		}
	}
}

func TestIsLoopbackHost(t *testing.T) {
	t.Parallel()
	for h, want := range map[string]bool{"localhost": true, "LOCALHOST": true, "kc.localhost": true, "127.0.0.1": true, "::1": true,
		"sso.example.com": false, "10.0.0.1": false, "localhost.evil.com": false} {
		if isLoopbackHost(h) != want {
			t.Errorf("isLoopbackHost(%q) != %v", h, want)
		}
	}
}

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestNewOIDCValidation(t *testing.T) {
	t.Parallel()
	pub, _ := url.Parse("http://app.test")
	cfg := config.OIDCConfig{IssuerURL: "http://idp", ClientID: "c"}
	if _, err := NewOIDC(cfg, pub, make([]byte, 16), nil); err == nil {
		t.Error("short key must fail")
	}
	if _, err := NewOIDC(config.OIDCConfig{ClientID: "c"}, pub, make([]byte, 32), nil); err == nil {
		t.Error("missing issuer must fail")
	}
	if _, err := NewOIDC(cfg, nil, make([]byte, 32), nil); err == nil {
		t.Error("missing PUBLIC_URL must fail")
	}
	bad := cfg
	bad.CAFile = "/nonexistent/ca.pem"
	if _, err := NewOIDC(bad, pub, make([]byte, 32), nil); err == nil {
		t.Error("missing CA file must fail")
	}
}

func TestOIDCDiagnose(t *testing.T) {
	t.Parallel()
	idp := newFakeIdP(t)
	steps := newTestOIDC(t, idp, nil).Diagnose(t.Context())
	if len(steps) != 5 {
		t.Fatalf("steps: %+v", steps)
	}
	for _, s := range steps {
		if !s.OK {
			t.Fatalf("step %q failed: %s", s.Name, s.Detail)
		}
		if strings.Contains(s.Detail, testClientSecret) {
			t.Fatalf("step %q leaks the secret", s.Name)
		}
	}

	steps = newTestOIDC(t, idp, func(c *config.OIDCConfig) { c.ClientSecret = "wrong" }).Diagnose(t.Context())
	if last := steps[len(steps)-1]; last.OK || !strings.Contains(last.Detail, "OIDC_CLIENT_SECRET") || strings.Contains(last.Detail, "wrong") {
		t.Fatalf("wrong secret: %+v", last)
	}
	steps = newTestOIDC(t, idp, func(c *config.OIDCConfig) { c.ClientID = "unknown" }).Diagnose(t.Context())
	if steps[3].OK || !strings.Contains(steps[3].Detail, "Valid redirect URIs") {
		t.Fatalf("unknown client: %+v", steps[3])
	}
	steps = newTestOIDC(t, idp, func(c *config.OIDCConfig) { c.IssuerURL += "/realms/nope" }).Diagnose(t.Context())
	if len(steps) != 1 || steps[0].OK {
		t.Fatalf("bad issuer URL: %+v", steps)
	}
	idp2 := newFakeIdP(t)
	idp2.issuer = "http://other.example"
	steps = newTestOIDC(t, idp2, nil).Diagnose(t.Context())
	if len(steps) != 2 || steps[1].OK || !strings.Contains(steps[1].Detail, "http://other.example") {
		t.Fatalf("issuer mismatch: %+v", steps)
	}
}

// TestOIDCStartLoginPrompt: "sign in as someone else" passes prompt to the
// provider; unknown values are refused before any cookie or network call.
func TestOIDCStartLoginPrompt(t *testing.T) {
	t.Parallel()
	idp := newFakeIdP(t)
	o := newTestOIDC(t, idp, nil)
	cases := []struct {
		name       string
		opts       []LoginOption
		wantPrompt string
		wantErr    bool
	}{
		{"no option", nil, "", false},
		{"empty prompt", []LoginOption{WithPrompt("")}, "", false},
		{"login", []LoginOption{WithPrompt(PromptLogin)}, "login", false},
		{"select account", []LoginOption{WithPrompt(PromptSelectAccount)}, "select_account", false},
		{"none is refused", []LoginOption{WithPrompt("none")}, "", true},
		{"consent is refused", []LoginOption{WithPrompt("consent")}, "", true},
		{"injection is refused", []LoginOption{WithPrompt("login&max_age=0")}, "", true},
		{"case matters", []LoginOption{WithPrompt("LOGIN")}, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			authURL, err := o.StartLogin(rec, httptest.NewRequest("GET", "/", nil), "/", c.opts...)
			if c.wantErr {
				if !errors.Is(err, ErrInvalidPrompt) || authURL != "" || len(rec.Result().Cookies()) != 0 {
					t.Fatalf("want ErrInvalidPrompt without a cookie, got %q %v %v", authURL, err, rec.Result().Cookies())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse(authURL)
			q := u.Query()
			if got, has := q.Get("prompt"), q.Has("prompt"); got != c.wantPrompt || has != (c.wantPrompt != "") {
				t.Fatalf("prompt = %q (present %v), want %q", got, has, c.wantPrompt)
			}
			if q.Get("state") == "" || q.Get("code_challenge") == "" {
				t.Fatalf("auth URL lost state/PKCE: %s", authURL)
			}
		})
	}
	for p, want := range map[string]bool{"": true, "login": true, "select_account": true, "none": false, " login": false} {
		if ValidPrompt(p) != want {
			t.Errorf("ValidPrompt(%q) != %v", p, want)
		}
	}
}

// cookieNamed returns the Set-Cookie of rec named name (nil when absent).
func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// TestOIDCSwitchAccount: after a denied login, "Войти под другой учётной
// записью" ends that account's provider session (id_token_hint, no
// confirmation) and comes back to /login?state=switch_account. Keycloak
// answers prompt=login alone with a form locked to the same account.
func TestOIDCSwitchAccount(t *testing.T) {
	t.Parallel()
	idp := newFakeIdP(t)
	o := newTestOIDC(t, idp, nil)
	_, raw, _, err, _ := login(t, o, idp, &grant{at: realmRoles("student")}, nil)
	if !errors.Is(err, ErrAccessDenied) || raw == "" {
		t.Fatalf("want a denied login with its id token, got %q %v", raw, err)
	}
	remember := httptest.NewRecorder()
	o.RememberDenied(remember, raw)
	denied := cookieNamed(remember, deniedCookieName)
	if denied == nil || !denied.HttpOnly || denied.Path != "/auth/oidc" || denied.SameSite != http.SameSiteLaxMode ||
		denied.MaxAge != int(flowMaxAge/time.Second) || strings.Contains(denied.Value, raw[:20]) {
		t.Fatalf("denied cookie = %+v", denied)
	}

	switchWith := func(c *http.Cookie) (string, *httptest.ResponseRecorder) {
		req := httptest.NewRequest("GET", "http://app.test/auth/oidc/login?prompt=login", nil)
		if c != nil {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		return o.SwitchAccountURL(rec, req), rec
	}
	target, rec := switchWith(denied)
	u := mustParse(t, target)
	q := u.Query()
	if u.Path != "/logout" || q.Get("id_token_hint") != raw || q.Get("state") != SwitchAccountState ||
		q.Get("post_logout_redirect_uri") != "http://app.test/login" || q.Get("client_id") != testClientID {
		t.Fatalf("SwitchAccountURL = %s", target)
	}
	// One-shot: the cookie is deleted.
	if c := cookieNamed(rec, deniedCookieName); c == nil || c.MaxAge >= 0 {
		t.Fatalf("denied cookie not deleted: %+v", c)
	}
	if got, _ := switchWith(nil); got != "" {
		t.Fatalf("no cookie: want \"\", got %q", got)
	}

	// Tampered, expired, sealed for another cookie, or with another key: ignored.
	tampered := *denied
	b := mustB64(t, denied.Value)
	b[len(b)-1] ^= 1
	tampered.Value = b64url(b)
	oldValue, _ := o.sealValue(deniedState{IDToken: raw, Created: time.Now().Add(-11 * time.Minute).Unix()}, deniedCookieName)
	flowValue, _ := o.sealValue(deniedState{IDToken: raw, Created: time.Now().Unix()}, flowCookieName)
	other, _ := NewOIDC(o.cfg, mustParse(t, "http://app.test"), DeriveKey([]byte("another secret"), "oidc-flow-cookie"), nil)
	otherValue, _ := other.sealValue(deniedState{IDToken: raw, Created: time.Now().Unix()}, deniedCookieName)
	for name, v := range map[string]string{"tampered": tampered.Value, "expired": oldValue, "flow cookie": flowValue,
		"other key": otherValue, "garbage": "!!!"} {
		if got, _ := switchWith(&http.Cookie{Name: deniedCookieName, Value: v}); got != "" {
			t.Errorf("%s: want \"\", got %q", name, got)
		}
	}

	// A provider without end_session_endpoint: nothing to end, prompt=login it is.
	idp2 := newFakeIdP(t)
	idp2.noEndSession = true
	o2 := newTestOIDC(t, idp2, nil)
	rec2 := httptest.NewRecorder()
	o2.RememberDenied(rec2, raw)
	req := httptest.NewRequest("GET", "http://app.test/auth/oidc/login?prompt=login", nil)
	req.AddCookie(cookieNamed(rec2, deniedCookieName))
	if got := o2.SwitchAccountURL(httptest.NewRecorder(), req); got != "" {
		t.Fatalf("no end_session_endpoint: want \"\", got %q", got)
	}
}

func TestOIDCRememberAndForgetDenied(t *testing.T) {
	t.Parallel()
	idp := newFakeIdP(t)
	o := newTestOIDC(t, idp, nil)
	for name, token := range map[string]string{"empty": "", "too large for a cookie": strings.Repeat("x", 4000)} {
		rec := httptest.NewRecorder()
		o.RememberDenied(rec, token)
		if c := cookieNamed(rec, deniedCookieName); c != nil {
			t.Errorf("%s: cookie written: %d bytes", name, len(c.Value))
		}
	}
	// ForgetDenied deletes the cookie only when the browser has one.
	rec := httptest.NewRecorder()
	o.ForgetDenied(rec, httptest.NewRequest("GET", "/auth/oidc/callback", nil))
	if len(rec.Result().Cookies()) != 0 {
		t.Fatalf("ForgetDenied without a cookie wrote %v", rec.Result().Cookies())
	}
	req := httptest.NewRequest("GET", "/auth/oidc/callback", nil)
	req.AddCookie(&http.Cookie{Name: deniedCookieName, Value: "x"})
	rec = httptest.NewRecorder()
	o.ForgetDenied(rec, req)
	if c := cookieNamed(rec, deniedCookieName); c == nil || c.MaxAge >= 0 || c.Path != "/auth/oidc" {
		t.Fatalf("ForgetDenied: %+v", c)
	}
}
