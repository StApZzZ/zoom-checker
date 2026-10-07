package httpapi

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

	"zoom-checker/internal/auth"
	"zoom-checker/internal/config"
	"zoom-checker/internal/model"
)

const (
	azOIDCClientID     = "zoom-checker"
	azOIDCClientSecret = "oidc-client-secret-value"
)

var azOIDCKey = sync.OnceValue(func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
})

// azIdP is a minimal OpenID provider: discovery, JWKS, token and end session.
type azIdP struct {
	srv   *httptest.Server
	mu    sync.Mutex
	codes map[string]azGrant
}

type azGrant struct {
	nonce, challenge string
	claims           map[string]any
}

func newAZIdP(t *testing.T) *azIdP {
	f := &azIdP{codes: map[string]azGrant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		azWriteJSON(w, map[string]any{
			"issuer":                                f.srv.URL,
			"authorization_endpoint":                f.srv.URL + "/auth",
			"token_endpoint":                        f.srv.URL + "/token",
			"jwks_uri":                              f.srv.URL + "/jwks",
			"end_session_endpoint":                  f.srv.URL + "/logout",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		pub := azOIDCKey().PublicKey
		azWriteJSON(w, map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
			"n": azB64(pub.N.Bytes()), "e": azB64(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		g, ok := f.codes[r.PostForm.Get("code")]
		delete(f.codes, r.PostForm.Get("code"))
		f.mu.Unlock()
		if !ok || oauth2.S256ChallengeFromVerifier(r.PostForm.Get("code_verifier")) != g.challenge {
			w.WriteHeader(http.StatusBadRequest)
			azWriteJSON(w, map[string]string{"error": "invalid_grant"})
			return
		}
		now := time.Now().Unix()
		claims := map[string]any{"iss": f.srv.URL, "sub": "kc-sub-1", "aud": azOIDCClientID, "azp": azOIDCClientID,
			"iat": now, "exp": now + 300, "nonce": g.nonce, "preferred_username": "Smirnova",
			"email": "smirnova@example.test", "name": "Анна Смирнова"}
		maps.Copy(claims, g.claims)
		azWriteJSON(w, map[string]any{"access_token": "opaque-access-token", "token_type": "Bearer",
			"expires_in": 300, "id_token": azSignJWT(claims)})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func azWriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func azB64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func azSignJWT(claims map[string]any) string {
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "k1"})
	c, _ := json.Marshal(claims)
	in := azB64(h) + "." + azB64(c)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, azOIDCKey(), crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return in + "." + azB64(sig)
}

func azTestOIDC(t *testing.T, issuer string, mod func(*config.OIDCConfig)) *auth.OIDC {
	t.Helper()
	cfg := config.OIDCConfig{
		IssuerURL: issuer, ClientID: azOIDCClientID, ClientSecret: azOIDCClientSecret,
		Scopes: []string{"profile", "email"}, ButtonLabel: "Войти через Keycloak",
		AdminRoles: []string{"zc-admin"}, TeacherRoles: []string{"zc-teacher"},
	}
	if mod != nil {
		mod(&cfg)
	}
	pub, _ := url.Parse("http://app.test")
	o, err := auth.NewOIDC(cfg, pub, auth.DeriveKey([]byte("test server secret"), "oidc-flow-cookie"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func newAZOIDCEnv(t *testing.T) (*azEnv, *azIdP) {
	idp := newAZIdP(t)
	e := newAZEnv(t, func(s *azSetup) {
		s.cfg.OIDC = config.OIDCConfig{IssuerURL: idp.srv.URL, ClientID: azOIDCClientID, ClientSecret: azOIDCClientSecret}
		s.deps.OIDC = azTestOIDC(t, idp.srv.URL, nil)
	})
	return e, idp
}

// oidcLogin runs the browser round trip; claims are added to the ID token.
// tamper may change the callback query. Returns the callback response.
func (e *azEnv) oidcLogin(idp *azIdP, next string, claims map[string]any, tamper func(url.Values)) *httptest.ResponseRecorder {
	e.t.Helper()
	start := e.do("GET", "/auth/oidc/login?next="+url.QueryEscape(next), nil, nil)
	if start.Code != http.StatusFound {
		e.t.Fatalf("start login: %d %s", start.Code, start.Body.String())
	}
	loc, err := url.Parse(start.Header().Get("Location"))
	if err != nil || !strings.HasPrefix(loc.String(), idp.srv.URL+"/auth?") {
		e.t.Fatalf("redirect to IdP: %q", start.Header().Get("Location"))
	}
	q := loc.Query()
	idp.mu.Lock()
	idp.codes["code-1"] = azGrant{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), claims: claims}
	idp.mu.Unlock()
	cb := url.Values{"code": {"code-1"}, "state": {q.Get("state")}}
	if tamper != nil {
		tamper(cb)
	}
	r := azRequest(e.t, "GET", "/auth/oidc/callback?"+cb.Encode(), nil)
	for _, c := range start.Result().Cookies() {
		r.AddCookie(c)
	}
	return e.serve(r)
}

func TestOIDCLoginFlow(t *testing.T) {
	t.Parallel()
	e, idp := newAZOIDCEnv(t)
	rec := e.oidcLogin(idp, "/lessons/7", map[string]any{"roles": []string{"zc-teacher"}}, nil)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/lessons/7" {
		t.Fatalf("callback: %d %q %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	cookie := azCookie(rec, SessionCookie)
	if cookie == nil {
		t.Fatal("no session cookie after OIDC login")
	}
	me := azDecode[loginResponse](t, e.do("GET", "/api/auth/me", nil, cookie)).User
	if me.AuthSource != "oidc" || me.Username != "smirnova" || me.DisplayName != "Анна Смирнова" ||
		me.Email != "smirnova@example.test" || me.Role != model.RoleTeacher {
		t.Fatalf("me = %+v", me)
	}

	// Second login: same row (by sub), role re-synced; unsafe next falls back to "/".
	rec = e.oidcLogin(idp, "//evil.example/x", map[string]any{"roles": []string{"zc-admin"}, "name": "Анна С."}, nil)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("second callback: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	again := azDecode[loginResponse](t, e.do("GET", "/api/auth/me", nil, azCookie(rec, SessionCookie))).User
	if again.ID != me.ID || again.Role != model.RoleAdmin || again.DisplayName != "Анна С." {
		t.Fatalf("second login = %+v (first %+v)", again, me)
	}

	// Logout ends the Keycloak session too.
	out := e.do("POST", "/api/auth/logout", map[string]any{}, azCookie(rec, SessionCookie))
	azWantStatus(t, out, http.StatusOK)
	ru := azDecode[logoutResponse](t, out).RedirectURL
	lu, err := url.Parse(ru)
	if err != nil || !strings.HasPrefix(ru, idp.srv.URL+"/logout?") || lu.Query().Get("id_token_hint") == "" ||
		lu.Query().Get("post_logout_redirect_uri") != "http://app.test/login" {
		t.Fatalf("logout redirect = %q", ru)
	}
}

func TestOIDCLoginErrors(t *testing.T) {
	t.Parallel()
	e, idp := newAZOIDCEnv(t)

	rec := e.oidcLogin(idp, "/", map[string]any{"roles": []string{"student"}}, nil)
	// The login page names the roles that grant access (teacher rules first).
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login?error=access_denied&required=zc-teacher%2Czc-admin" ||
		azCookie(rec, SessionCookie) != nil {
		t.Fatalf("access denied: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if loc, _ := url.Parse(rec.Header().Get("Location")); loc.Query().Get("required") != "zc-teacher,zc-admin" {
		t.Fatalf("required = %q", loc.Query().Get("required"))
	}
	rec = e.oidcLogin(idp, "/", map[string]any{"roles": []string{"zc-teacher"}}, func(q url.Values) { q.Set("state", "forged") })
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login?error=oidc_failed" || azCookie(rec, SessionCookie) != nil {
		t.Fatalf("bad state: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// Without the flow cookie: usually the login was started on another
	// address than PUBLIC_URL, which the login page then names.
	rec = e.do("GET", "/auth/oidc/callback?code=x&state=y", nil, nil)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login?error=oidc_host" {
		t.Fatalf("no cookie: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if !strings.Contains(e.logs.String(), "another address than PUBLIC_URL") {
		t.Errorf("the log does not explain the missing cookie:\n%s", e.logs.String())
	}
	// A failure after the flow cookie was read keeps where the user was going.
	rec = e.oidcLogin(idp, "/groups/3", map[string]any{"roles": []string{"zc-teacher"}}, func(q url.Values) { q.Set("state", "forged") })
	if rec.Header().Get("Location") != "/login?error=oidc_failed&next=%2Fgroups%2F3" {
		t.Fatalf("bad state with next: %d %q", rec.Code, rec.Header().Get("Location"))
	}

	// A locally disabled account cannot sign in through Keycloak.
	e.createUser("smirnova", "", model.RoleTeacher, func(u *model.User) {
		u.AuthSource, u.ExternalID, u.Disabled = model.AuthOIDC, "kc-sub-1", true
	})
	rec = e.oidcLogin(idp, "/", map[string]any{"roles": []string{"zc-teacher"}}, nil)
	if rec.Header().Get("Location") != "/login?error=disabled" || azCookie(rec, SessionCookie) != nil {
		t.Fatalf("disabled: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// Keycloak keeps that account signed in, so switching to another account
	// must end its session first, exactly as after a missing role.
	denied := azCookie(rec, "zc_oidc_denied")
	if denied == nil {
		t.Fatal("disabled: the account's ID token must be kept for «Войти под другой учётной записью»")
	}
	sw := e.do("GET", "/auth/oidc/login?prompt=login", nil, denied)
	if loc, err := url.Parse(sw.Header().Get("Location")); sw.Code != http.StatusFound || err != nil ||
		!strings.HasPrefix(loc.String(), idp.srv.URL+"/logout?") || loc.Query().Get("state") != "switch_account" {
		t.Fatalf("switch after disabled: %d %q", sw.Code, sw.Header().Get("Location"))
	}
}

func TestOIDCDisabledRoutes(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	azWantError(t, e.do("GET", "/auth/oidc/login", nil, nil), http.StatusNotFound, CodeNotFound)
	azWantError(t, e.do("GET", "/auth/oidc/callback?code=x", nil, nil), http.StatusNotFound, CodeNotFound)
}

func TestOIDCProviderDown(t *testing.T) {
	t.Parallel()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "maintenance", http.StatusServiceUnavailable)
	}))
	t.Cleanup(down.Close)
	e := newAZEnv(t, func(s *azSetup) { s.deps.OIDC = azTestOIDC(t, down.URL, nil) })
	rec := e.do("GET", "/auth/oidc/login?next=/x", nil, nil)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login?error=oidc_failed" {
		t.Fatalf("provider down: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestUpsertExternalUserByExternalID(t *testing.T) {
	t.Parallel()
	e := newAZEnv(t)
	ctx := context.Background()
	// An older directory account with the same login but another id keeps its row.
	old := e.createUser("bob", "", model.RoleTeacher, func(u *model.User) { u.AuthSource, u.ExternalID = model.AuthLDAP, "old-guid" })

	id := &auth.Identity{Source: model.AuthLDAP, ExternalID: "new-guid", Username: "bob", DisplayName: "Боб", Email: "b@x", Role: model.RoleTeacher}
	u, ch, err := e.srv.upsertExternalUser(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if u.ID == old.ID || u.Username != "bob-2" || u.ExternalID != "new-guid" || !ch.created {
		t.Fatalf("created %+v %+v", u, ch)
	}
	// Same external id, nothing new: no change to report.
	if _, ch, err := e.srv.upsertExternalUser(ctx, id); err != nil || ch.created || ch.next != "" {
		t.Fatalf("unchanged upsert %+v %v", ch, err)
	}
	// Same external id again: same row, profile synced.
	id.DisplayName, id.Email, id.Role = "Боб Б.", "", model.RoleAdmin
	again, ch, err := e.srv.upsertExternalUser(ctx, id)
	if err != nil || again.ID != u.ID || again.DisplayName != "Боб Б." || again.Email != "" || again.Role != model.RoleAdmin {
		t.Fatalf("second upsert %+v %v", again, err)
	}
	if ch.created || !strings.Contains(ch.prev, "роль: преподаватель") || !strings.Contains(ch.next, "роль: администратор") {
		t.Fatalf("sync change = %+v", ch)
	}
	stored, _ := e.st.GetUser(ctx, u.ID)
	if stored.Role != model.RoleAdmin || stored.DisplayName != "Боб Б." {
		t.Fatalf("not stored: %+v", stored)
	}
	// A local account with the same name does not matter for directory accounts.
	e.createUser("carol", "", model.RoleTeacher, nil)
	c, _, err := e.srv.upsertExternalUser(ctx, &auth.Identity{Source: model.AuthOIDC, ExternalID: "sub-c", Username: "carol", Role: model.RoleTeacher})
	if err != nil || c.Username != "carol" || c.DisplayName != "carol" {
		t.Fatalf("oidc carol %+v %v", c, err)
	}
	if _, _, err := e.srv.upsertExternalUser(ctx, &auth.Identity{Source: model.AuthOIDC, Username: "x"}); err == nil {
		t.Fatal("identity without external id accepted")
	}
}

// "Войти под другой учётной записью": prompt=login|select_account reaches
// Keycloak; anything else is refused.
func TestOIDCLoginPrompt(t *testing.T) {
	t.Parallel()
	e, idp := newAZOIDCEnv(t)
	for _, c := range []struct{ query, want string }{
		{"", ""},
		{"&prompt=login", "login"},
		{"&prompt=select_account", "select_account"},
	} {
		rec := e.do("GET", "/auth/oidc/login?next=%2Flessons"+c.query, nil, nil)
		loc, err := url.Parse(rec.Header().Get("Location"))
		if rec.Code != http.StatusFound || err != nil || !strings.HasPrefix(loc.String(), idp.srv.URL+"/auth?") {
			t.Fatalf("%q: %d %q", c.query, rec.Code, rec.Header().Get("Location"))
		}
		if got := loc.Query().Get("prompt"); got != c.want || loc.Query().Has("prompt") != (c.want != "") {
			t.Fatalf("%q: prompt = %q, want %q", c.query, got, c.want)
		}
	}
	for _, bad := range []string{"none", "consent", "login%20consent"} {
		rec := e.do("GET", "/auth/oidc/login?prompt="+bad, nil, nil)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login?error=oidc_failed" || azCookie(rec, "zc_oidc") != nil {
			t.Fatalf("prompt=%s: %d %q", bad, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestAccessDeniedQuery(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("r", 600)
	many := make([]string, 30)
	for i := range many {
		many[i] = fmt.Sprintf("role-%02d", i)
	}
	cases := []struct {
		name     string
		required []string
		want     string
	}{
		{"nothing configured", nil, "error=access_denied"},
		{"roles", []string{"zc-teacher", "/Teachers", "zc admin"}, "error=access_denied&required=zc-teacher%2C%2FTeachers%2Czc+admin"},
		{"blank and comma values skipped", []string{" ", "a,b", "ok"}, "error=access_denied&required=ok"},
		{"length capped", []string{long, long, "x"}, "error=access_denied&required=" + long},
		{"count capped", many, "error=access_denied&required=" + url.QueryEscape(strings.Join(many[:20], ","))},
	}
	for _, c := range cases {
		if got := accessDeniedQuery(c.required).Encode(); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// "Войти под другой учётной записью" after a denial: the denied account's
// Keycloak session is ended first (Keycloak would otherwise answer
// prompt=login with a password form locked to that account), then the browser
// comes back to /login?state=switch_account.
func TestOIDCSwitchAccountAfterDenial(t *testing.T) {
	t.Parallel()
	e, idp := newAZOIDCEnv(t)

	rec := e.oidcLogin(idp, "/", map[string]any{"roles": []string{"student"}}, nil)
	if !strings.HasPrefix(rec.Header().Get("Location"), "/login?error=access_denied") {
		t.Fatalf("denied: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	denied := azCookie(rec, "zc_oidc_denied")
	if denied == nil || !denied.HttpOnly || denied.Path != "/auth/oidc" {
		t.Fatalf("denied account cookie = %+v", denied)
	}

	sw := e.do("GET", "/auth/oidc/login?prompt=login&next=%2Fgroups", nil, denied)
	loc, err := url.Parse(sw.Header().Get("Location"))
	if sw.Code != http.StatusFound || err != nil || !strings.HasPrefix(loc.String(), idp.srv.URL+"/logout?") {
		t.Fatalf("switch: %d %q", sw.Code, sw.Header().Get("Location"))
	}
	q := loc.Query()
	if q.Get("id_token_hint") == "" || q.Get("state") != "switch_account" || q.Get("post_logout_redirect_uri") != "http://app.test/login" {
		t.Fatalf("switch logout URL = %s", loc)
	}
	if azCookie(sw, "zc_oidc") != nil {
		t.Fatal("no login may start before the old session has ended")
	}
	consumed := false
	for _, c := range sw.Result().Cookies() {
		consumed = consumed || (c.Name == "zc_oidc_denied" && c.MaxAge < 0)
	}
	if !consumed {
		t.Fatalf("denied cookie must be consumed: %+v", sw.Result().Cookies())
	}

	// Without that cookie (expired, another browser) it is plain prompt=login,
	// and the button without prompt retries the same account.
	for query, prompt := range map[string]string{"?prompt=login": "login", "": ""} {
		cookie := denied
		if prompt != "" {
			cookie = nil
		}
		rec := e.do("GET", "/auth/oidc/login"+query, nil, cookie)
		loc, _ := url.Parse(rec.Header().Get("Location"))
		if rec.Code != http.StatusFound || !strings.HasPrefix(loc.String(), idp.srv.URL+"/auth?") || loc.Query().Get("prompt") != prompt {
			t.Fatalf("login%s: %d %q", query, rec.Code, rec.Header().Get("Location"))
		}
	}

	// A successful login forgets a leftover denied account.
	start := e.do("GET", "/auth/oidc/login", nil, nil)
	sl, _ := url.Parse(start.Header().Get("Location"))
	idp.mu.Lock()
	idp.codes["code-ok"] = azGrant{nonce: sl.Query().Get("nonce"), challenge: sl.Query().Get("code_challenge"),
		claims: map[string]any{"roles": []string{"zc-teacher"}}}
	idp.mu.Unlock()
	cb := azRequest(t, "GET", "/auth/oidc/callback?"+url.Values{"code": {"code-ok"}, "state": {sl.Query().Get("state")}}.Encode(), nil)
	cb.AddCookie(azCookie(start, "zc_oidc"))
	cb.AddCookie(denied)
	ok := e.serve(cb)
	if ok.Code != http.StatusFound || azCookie(ok, SessionCookie) == nil {
		t.Fatalf("login: %d %q", ok.Code, ok.Header().Get("Location"))
	}
	forgotten := false
	for _, c := range ok.Result().Cookies() {
		forgotten = forgotten || (c.Name == "zc_oidc_denied" && c.MaxAge < 0)
	}
	if !forgotten {
		t.Fatal("successful login must delete the denied account cookie")
	}
}

// The path the user was going to survives the whole "Войти под другой учётной
// записью" round trip: denied login -> login page -> provider logout (which
// can only return to the bare /login) -> new login -> callback.
func TestOIDCSwitchAccountKeepsNext(t *testing.T) {
	t.Parallel()
	e, idp := newAZOIDCEnv(t)

	rec := e.oidcLogin(idp, "/lessons/42", map[string]any{"roles": []string{"student"}}, nil)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Path != "/login" || loc.Query().Get("error") != "access_denied" || loc.Query().Get("next") != "/lessons/42" {
		t.Fatalf("denied redirect = %q, want it to keep next", rec.Header().Get("Location"))
	}
	denied := azCookie(rec, "zc_oidc_denied")

	sw := e.do("GET", "/auth/oidc/login?prompt=login&next="+url.QueryEscape(loc.Query().Get("next")), nil, denied)
	if sw.Code != http.StatusFound || !strings.HasPrefix(sw.Header().Get("Location"), idp.srv.URL+"/logout?") {
		t.Fatalf("switch: %d %q", sw.Code, sw.Header().Get("Location"))
	}
	kept := azCookie(sw, "zc_oidc_next")
	if kept == nil || !kept.HttpOnly || kept.Path != "/auth/oidc" || strings.Contains(kept.Value, "lessons") {
		t.Fatalf("next cookie = %+v (must be sealed, HttpOnly, path /auth/oidc)", kept)
	}

	// Back on /login?state=switch_account the button carries no next.
	start := e.do("GET", "/auth/oidc/login", nil, kept)
	consumed := false
	for _, c := range start.Result().Cookies() {
		consumed = consumed || (c.Name == "zc_oidc_next" && c.MaxAge < 0)
	}
	if !consumed {
		t.Fatal("the kept next must be consumed by the login that follows")
	}
	sl, _ := url.Parse(start.Header().Get("Location"))
	idp.mu.Lock()
	idp.codes["code-next"] = azGrant{nonce: sl.Query().Get("nonce"), challenge: sl.Query().Get("code_challenge"),
		claims: map[string]any{"roles": []string{"zc-teacher"}}}
	idp.mu.Unlock()
	cb := azRequest(t, "GET", "/auth/oidc/callback?"+url.Values{"code": {"code-next"}, "state": {sl.Query().Get("state")}}.Encode(), nil)
	cb.AddCookie(azCookie(start, "zc_oidc"))
	done := e.serve(cb)
	if done.Code != http.StatusFound || done.Header().Get("Location") != "/lessons/42" || azCookie(done, SessionCookie) == nil {
		t.Fatalf("callback: %d %q, want a session and /lessons/42", done.Code, done.Header().Get("Location"))
	}

	// An explicit next wins over a kept one; a forged cookie is ignored.
	sw2 := e.do("GET", "/auth/oidc/login?prompt=login&next=%2Fgroups", nil, azCookie(e.oidcLogin(idp, "/", map[string]any{"roles": []string{"student"}}, nil), "zc_oidc_denied"))
	kept2 := azCookie(sw2, "zc_oidc_next")
	if kept2 == nil {
		t.Fatal("no next cookie on the second switch")
	}
	r := azRequest(t, "GET", "/auth/oidc/login?next=%2Fzoom", nil)
	r.AddCookie(kept2)
	st := e.serve(r)
	sl2, _ := url.Parse(st.Header().Get("Location"))
	idp.mu.Lock()
	idp.codes["code-explicit"] = azGrant{nonce: sl2.Query().Get("nonce"), challenge: sl2.Query().Get("code_challenge"),
		claims: map[string]any{"roles": []string{"zc-teacher"}}}
	idp.mu.Unlock()
	cb2 := azRequest(t, "GET", "/auth/oidc/callback?"+url.Values{"code": {"code-explicit"}, "state": {sl2.Query().Get("state")}}.Encode(), nil)
	cb2.AddCookie(azCookie(st, "zc_oidc"))
	if done := e.serve(cb2); done.Header().Get("Location") != "/zoom" {
		t.Fatalf("explicit next: callback went to %q, want /zoom", done.Header().Get("Location"))
	}
	forged := &http.Cookie{Name: "zc_oidc_next", Value: "AAAA" + kept2.Value[4:]}
	if got := e.srv.OIDC.TakeNext(httptest.NewRecorder(), func() *http.Request {
		r := azRequest(t, "GET", "/auth/oidc/login", nil)
		r.AddCookie(forged)
		return r
	}()); got != "" {
		t.Fatalf("forged next cookie accepted: %q", got)
	}
}
