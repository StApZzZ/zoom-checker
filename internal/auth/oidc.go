package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"zoom-checker/internal/config"
	"zoom-checker/internal/model"
)

const (
	flowCookieName   = "zc_oidc"
	deniedCookieName = "zc_oidc_denied"
	nextCookieName   = "zc_oidc_next"
	flowMaxAge       = 10 * time.Minute
	callbackPath     = "/auth/oidc/callback"
	oidcHTTPTimeout  = 10 * time.Second
	discoveryBackoff = 5 * time.Second // fail fast while the IdP stays down
	maxFlowCookieLen = 3800            // browsers drop cookies over ~4 KiB
)

// OIDC implements login through an OpenID Connect provider (Keycloak):
// authorization code flow with PKCE (S256), state and nonce. The provider is
// discovered lazily and re-tried until it succeeds, so the service starts even
// when the IdP is down. Safe for concurrent use.
type OIDC struct {
	cfg         config.OIDCConfig
	log         *slog.Logger
	publicBase  string // PUBLIC_URL without trailing slash
	redirectURL string
	cookiePath  string
	secure      bool
	aead        cipher.AEAD
	client      *http.Client
	now         func() time.Time

	mu          sync.Mutex
	prov        *oidcProvider
	lastErr     error
	lastAttempt time.Time
}

type oidcProvider struct {
	provider   *oidc.Provider
	issuer     string
	oauth      *oauth2.Config
	idVerifier *oidc.IDTokenVerifier
	atVerifier *oidc.IDTokenVerifier
	endSession string
}

// flowState travels in the sealed "zc_oidc" cookie between StartLogin and
// HandleCallback.
type flowState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Next     string `json:"x"`
	Created  int64  `json:"t"` // unix seconds
}

// deniedState travels in the sealed "zc_oidc_denied" cookie: the ID token of
// an account that signed in at the provider but has no access here, so that
// "Войти под другой учётной записью" can end that provider session first
// (see SwitchAccountURL).
type deniedState struct {
	IDToken string `json:"i"`
	Created int64  `json:"t"` // unix seconds
}

// nextState travels in the sealed "zc_oidc_next" cookie: where the user was
// going when "Войти под другой учётной записью" sent the browser through the
// provider's logout (see RememberNext).
type nextState struct {
	Next    string `json:"n"`
	Created int64  `json:"t"` // unix seconds
}

// SwitchAccountState is the logout state the provider sends back to
// PUBLIC_URL/login (as ?state=) after SwitchAccountURL ended the session of an
// account without access: the login page then asks for the other account.
const SwitchAccountState = "switch_account"

// NewOIDC prepares OIDC login. sealKey (32 bytes, e.g. DeriveKey(serverSecret,
// "oidc-flow-cookie")) encrypts the short-lived flow cookie. publicURL is
// PUBLIC_URL; the redirect URL is publicURL + "/auth/oidc/callback".
// No network call is made here.
func NewOIDC(cfg config.OIDCConfig, publicURL *url.URL, sealKey []byte, log *slog.Logger) (*OIDC, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if cfg.IssuerURL == "" || cfg.ClientID == "" {
		return nil, errors.New("auth: OIDC_ISSUER_URL and OIDC_CLIENT_ID are required")
	}
	if publicURL == nil || (publicURL.Scheme != "http" && publicURL.Scheme != "https") || publicURL.Host == "" {
		return nil, errors.New("auth: OIDC needs an absolute http(s) PUBLIC_URL")
	}
	if len(sealKey) != 32 {
		return nil, fmt.Errorf("auth: OIDC cookie key must be 32 bytes, got %d", len(sealKey))
	}
	block, err := aes.NewCipher(sealKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.CAFile != "" {
		pool, err := loadCAPool(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("auth: OIDC_CA_FILE: %w", err)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	if !slices.Contains(cfg.Scopes, oidc.ScopeOpenID) {
		cfg.Scopes = append([]string{oidc.ScopeOpenID}, cfg.Scopes...)
	}
	if iu, err := url.Parse(cfg.IssuerURL); err == nil && iu.Scheme == "http" && !isLoopbackHost(iu.Hostname()) {
		log.Warn("OIDC_ISSUER_URL is plain http: the client secret and tokens travel unencrypted; use https", "issuer", cfg.IssuerURL)
	}
	base := strings.TrimRight(publicURL.String(), "/")
	return &OIDC{
		cfg:         cfg,
		log:         log,
		publicBase:  base,
		redirectURL: base + callbackPath,
		cookiePath:  strings.TrimRight(publicURL.Path, "/") + "/auth/oidc",
		secure:      publicURL.Scheme == "https",
		aead:        aead,
		client:      &http.Client{Timeout: oidcHTTPTimeout, Transport: transport},
		now:         time.Now,
	}, nil
}

// RedirectURL is the callback URL to register in the IdP client.
func (o *OIDC) RedirectURL() string { return o.redirectURL }

// clientCtx makes go-oidc and oauth2 use our HTTP client (timeout, CA).
func (o *OIDC) clientCtx(ctx context.Context) context.Context {
	return oidc.ClientContext(ctx, o.client)
}

// provider returns the cached provider or runs discovery. After a failure,
// calls within discoveryBackoff fail fast with the same error.
func (o *OIDC) provider(ctx context.Context) (*oidcProvider, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.prov != nil {
		return o.prov, nil
	}
	now := o.now()
	if o.lastErr != nil && now.Sub(o.lastAttempt) < discoveryBackoff {
		return nil, o.lastErr
	}
	o.lastAttempt = now
	// Discovery outlives a cancelled browser request; it has its own timeout.
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), oidcHTTPTimeout)
	defer cancel()
	p, err := o.discover(dctx)
	if err != nil {
		o.lastErr = err
		return nil, err
	}
	o.prov, o.lastErr = p, nil
	o.log.Info("oidc provider discovered", "issuer", p.issuer, "end_session", p.endSession != "")
	return p, nil
}

func (o *OIDC) discover(ctx context.Context) (*oidcProvider, error) {
	ctx = o.clientCtx(ctx)
	issuer := o.cfg.IssuerURL
	p, err := oidc.NewProvider(ctx, issuer)
	var mismatch *oidc.IssuerMismatchError
	if errors.As(err, &mismatch) && strings.TrimRight(mismatch.Discovered, "/") == strings.TrimRight(issuer, "/") {
		// Only a trailing slash differs (config trims it; some IdPs keep it).
		issuer = mismatch.Discovered
		p, err = oidc.NewProvider(ctx, issuer)
	}
	if err != nil {
		if errors.As(err, &mismatch) {
			o.log.Error("oidc discovery: issuer mismatch; set OIDC_ISSUER_URL to the discovered issuer (for Keycloak check KC_HOSTNAME)",
				"configured", mismatch.Provided, "discovered", mismatch.Discovered)
		} else {
			o.log.Warn("oidc discovery failed", "issuer", issuer, "err", err)
		}
		return nil, unavailable("oidc discovery", err)
	}
	var meta struct {
		EndSession string `json:"end_session_endpoint"`
	}
	_ = p.Claims(&meta)
	return &oidcProvider{
		provider: p,
		issuer:   issuer,
		oauth: &oauth2.Config{
			ClientID:     o.cfg.ClientID,
			ClientSecret: o.cfg.ClientSecret,
			Endpoint:     p.Endpoint(),
			RedirectURL:  o.redirectURL,
			Scopes:       o.cfg.Scopes,
		},
		idVerifier: p.Verifier(&oidc.Config{ClientID: o.cfg.ClientID}),
		// Access tokens have aud=account (Keycloak) and are checked for azp instead.
		atVerifier: p.Verifier(&oidc.Config{SkipClientIDCheck: true}),
		endSession: meta.EndSession,
	}, nil
}

// Prompt values accepted by WithPrompt (OpenID Connect Core 3.1.2.1). They let
// a user whose IdP session belongs to the wrong account sign in as someone else
// instead of being sent straight back with the same account.
const (
	// PromptLogin makes the provider ask for credentials even when the browser
	// already has a session there.
	PromptLogin = "login"
	// PromptSelectAccount asks the provider to let the user choose an account.
	PromptSelectAccount = "select_account"
)

// ErrInvalidPrompt is returned by StartLogin for a prompt other than
// PromptLogin and PromptSelectAccount.
var ErrInvalidPrompt = errors.New("auth: unsupported oidc prompt value")

// LoginOption changes one login started with StartLogin.
type LoginOption func(*loginOptions)

type loginOptions struct {
	prompt string
}

// WithPrompt passes prompt to the authorization request ("" = no prompt, the
// provider decides). Values other than PromptLogin and PromptSelectAccount make
// StartLogin fail with ErrInvalidPrompt.
func WithPrompt(prompt string) LoginOption {
	return func(o *loginOptions) { o.prompt = prompt }
}

// ValidPrompt reports whether prompt is accepted by WithPrompt ("" included).
func ValidPrompt(prompt string) bool {
	switch prompt {
	case "", PromptLogin, PromptSelectAccount:
		return true
	}
	return false
}

// StartLogin begins a login: it stores state, nonce, PKCE verifier and the
// sanitized next path in the sealed "zc_oidc" cookie and returns the provider
// URL to redirect the browser to. Errors wrap ErrUnavailable when the
// provider cannot be discovered, or are ErrInvalidPrompt.
func (o *OIDC) StartLogin(w http.ResponseWriter, r *http.Request, next string, opts ...LoginOption) (redirectURL string, err error) {
	var lo loginOptions
	for _, opt := range opts {
		opt(&lo)
	}
	if !ValidPrompt(lo.prompt) {
		return "", fmt.Errorf("%w: %.40q", ErrInvalidPrompt, lo.prompt)
	}
	p, err := o.provider(r.Context())
	if err != nil {
		return "", err
	}
	f := flowState{
		State:    randomString(32),
		Nonce:    randomString(32),
		Verifier: oauth2.GenerateVerifier(),
		Next:     SanitizeNext(next),
		Created:  o.now().Unix(),
	}
	value, err := o.seal(f)
	if err == nil && len(value) > maxFlowCookieLen {
		// A long next (JSON escapes "<>&" sixfold) would push the cookie past
		// the browsers' 4 KiB limit; it would be dropped and the login fail.
		f.Next = "/"
		value, err = o.seal(f)
	}
	if err != nil {
		return "", err
	}
	o.setCookie(w, flowCookieName, value, int(flowMaxAge/time.Second))
	params := []oauth2.AuthCodeOption{oidc.Nonce(f.Nonce), oauth2.S256ChallengeOption(f.Verifier)}
	if lo.prompt != "" {
		params = append(params, oauth2.SetAuthURLParam("prompt", lo.prompt))
	}
	return p.oauth.AuthCodeURL(f.State, params...), nil
}

// HandleCallback completes the login on GET /auth/oidc/callback. It consumes
// the flow cookie (one-shot), checks state, exchanges the code with the PKCE
// verifier, verifies the ID token, nonce and at_hash, collects roles and maps
// them. It returns the identity, the raw ID token (keep it in the session for
// LogoutURL) and the local path to continue to.
//
// Errors: ErrOIDCFlow (retry the login), ErrUnavailable, *AccessDeniedError.
// With *AccessDeniedError the raw ID token is still returned, so the caller
// can offer an IdP logout (otherwise the next login silently reuses the same
// IdP session).
func (o *OIDC) HandleCallback(w http.ResponseWriter, r *http.Request) (id *Identity, rawIDToken, next string, err error) {
	cookie, cookieErr := r.Cookie(flowCookieName)
	o.setCookie(w, flowCookieName, "", -1)
	if cookieErr != nil {
		return nil, "", "/", ErrOIDCNoFlowCookie
	}
	f, err := o.open(cookie.Value)
	if err != nil {
		return nil, "", "/", o.flowErr(err.Error())
	}
	next = f.Next
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		return nil, "", next, o.flowErr(fmt.Sprintf("provider returned error %q: %.200s", e, q.Get("error_description")))
	}
	if st := q.Get("state"); st == "" || subtle.ConstantTimeCompare([]byte(st), []byte(f.State)) != 1 {
		return nil, "", next, o.flowErr("state mismatch")
	}
	code := q.Get("code")
	if code == "" {
		return nil, "", next, o.flowErr("authorization code missing")
	}
	p, err := o.provider(r.Context())
	if err != nil {
		return nil, "", next, err
	}
	// RFC 9207: when the provider names itself in the response it must be ours (mix-up defence).
	if iss := q.Get("iss"); iss != "" && iss != p.issuer {
		return nil, "", next, o.flowErr(fmt.Sprintf("callback issuer %q does not match %q", iss, p.issuer))
	}

	ctx, cancel := context.WithTimeout(o.clientCtx(r.Context()), 2*oidcHTTPTimeout)
	defer cancel()
	tok, err := p.oauth.Exchange(ctx, code, oauth2.VerifierOption(f.Verifier))
	if err != nil {
		return nil, "", next, o.exchangeErr(err)
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		return nil, "", next, o.flowErr("token response has no id_token (is the openid scope enabled?)")
	}
	idt, err := p.idVerifier.Verify(ctx, rawID)
	if err != nil {
		return nil, "", next, o.flowErr("id token rejected: " + err.Error())
	}
	// go-oidc does not check the nonce.
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(f.Nonce)) != 1 {
		return nil, "", next, o.flowErr("nonce mismatch")
	}
	if idt.AccessTokenHash != "" {
		if err := idt.VerifyAccessToken(tok.AccessToken); err != nil {
			return nil, "", next, o.flowErr("at_hash mismatch")
		}
	}
	var idc map[string]any
	if err := idt.Claims(&idc); err != nil {
		return nil, "", next, o.flowErr("id token claims: " + err.Error())
	}
	// OIDC Core 3.1.3.7 (go-oidc only checks that aud contains client_id): an
	// azp naming another client, or several audiences without azp, means the
	// token was issued to someone else.
	if azp, _ := idc["azp"].(string); (azp != "" && azp != o.cfg.ClientID) || (azp == "" && len(idt.Audience) > 1) {
		return nil, "", next, o.flowErr(fmt.Sprintf("id token issued to another client (azp %q, aud %q)", azp, idt.Audience))
	}
	values := roleValues(idc, o.cfg.ClientID, o.cfg.RolesClaim)

	// UserInfo may carry groups/roles and profile fields missing from the ID token.
	var uic map[string]any
	if p.provider.UserInfoEndpoint() != "" {
		ui, err := p.provider.UserInfo(ctx, oauth2.StaticTokenSource(tok))
		switch {
		case err != nil:
			o.log.Debug("oidc userinfo failed; using id token claims only", "err", err)
		case ui.Subject != idt.Subject:
			o.log.Warn("oidc userinfo subject differs from id token; ignored")
		default:
			if err := ui.Claims(&uic); err == nil {
				values = append(values, roleValues(uic, o.cfg.ClientID, o.cfg.RolesClaim)...)
			}
		}
	}
	// Keycloak puts realm_access/resource_access only into the access token by
	// default. Trust it only when it verifies against the provider keys, was
	// issued to us (azp) and is about the same subject.
	if isJWT(tok.AccessToken) {
		values = append(values, o.accessTokenRoles(ctx, p, tok.AccessToken, idt.Subject)...)
	}

	id = buildOIDCIdentity(idt.Subject, idc, uic)
	id.Groups = dedupe(values)
	role, ok := MapRole(id.Groups, o.cfg.AdminRoles, o.cfg.TeacherRoles)
	if !ok {
		o.log.Info("oidc login denied: user has none of the configured roles", "username", id.Username, "roles", id.Groups)
		return nil, rawID, next, &AccessDeniedError{
			Username: id.Username,
			Required: dedupe(slices.Concat(o.cfg.TeacherRoles, o.cfg.AdminRoles)),
			Found:    id.Groups,
		}
	}
	id.Role = role
	return id, rawID, next, nil
}

func (o *OIDC) accessTokenRoles(ctx context.Context, p *oidcProvider, raw, subject string) []string {
	at, err := p.atVerifier.Verify(ctx, raw)
	if err != nil {
		o.log.Warn("oidc access token not verifiable; its roles are ignored", "err", err)
		return nil
	}
	var atc map[string]any
	if err := at.Claims(&atc); err != nil {
		return nil
	}
	if azp, _ := atc["azp"].(string); azp != o.cfg.ClientID {
		o.log.Warn("oidc access token issued to another client; its roles are ignored", "azp", azp)
		return nil
	}
	if at.Subject != subject {
		o.log.Warn("oidc access token subject differs from id token; its roles are ignored")
		return nil
	}
	return roleValues(atc, o.cfg.ClientID, o.cfg.RolesClaim)
}

func (o *OIDC) flowErr(reason string) error {
	o.log.Warn("oidc login failed", "reason", reason)
	return fmt.Errorf("%w: %s", ErrOIDCFlow, reason)
}

// exchangeErr classifies a failed code exchange.
func (o *OIDC) exchangeErr(err error) error {
	var re *oauth2.RetrieveError
	if !errors.As(err, &re) {
		o.log.Warn("oidc code exchange failed", "err", err)
		return unavailable("oidc token request", err)
	}
	desc := strings.ToLower(re.ErrorDescription)
	switch {
	case re.ErrorCode == "invalid_client" || (re.ErrorCode == "unauthorized_client" && strings.Contains(desc, "invalid client")):
		o.log.Error("oidc code exchange: client authentication failed; check OIDC_CLIENT_ID and OIDC_CLIENT_SECRET",
			"error", re.ErrorCode, "description", re.ErrorDescription)
		return unavailable("oidc token request", err)
	case re.Response != nil && re.Response.StatusCode >= 500:
		o.log.Warn("oidc code exchange failed", "status", re.Response.StatusCode, "error", re.ErrorCode)
		return unavailable("oidc token request", err)
	}
	return o.flowErr(fmt.Sprintf("code exchange rejected: %s %s", re.ErrorCode, re.ErrorDescription))
}

// LogoutURL returns the provider's RP-initiated logout URL
// (end_session_endpoint with id_token_hint, post_logout_redirect_uri =
// PUBLIC_URL/login and client_id), or "" when the provider has none or cannot
// be discovered.
func (o *OIDC) LogoutURL(rawIDToken string) string {
	return o.logoutURL(rawIDToken, "")
}

// logoutURL is LogoutURL with an optional state the provider passes back to
// the post-logout redirect (OpenID Connect RP-Initiated Logout 1.0).
func (o *OIDC) logoutURL(rawIDToken, state string) string {
	p, err := o.provider(context.Background())
	if err != nil || p.endSession == "" {
		return ""
	}
	u, err := url.Parse(p.endSession)
	if err != nil {
		return ""
	}
	q := u.Query()
	if rawIDToken != "" {
		q.Set("id_token_hint", rawIDToken)
	}
	q.Set("post_logout_redirect_uri", o.publicBase+"/login")
	q.Set("client_id", o.cfg.ClientID)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// RememberDenied keeps the raw ID token of a login refused with
// *AccessDeniedError in a sealed, HttpOnly, 10-minute cookie. Without it the
// provider session of that account survives: Keycloak answers prompt=login
// with a password form locked to the same account, and a plain login sends it
// straight back. Tokens too large for a cookie are skipped (switching then
// falls back to prompt=login).
func (o *OIDC) RememberDenied(w http.ResponseWriter, rawIDToken string) {
	if rawIDToken == "" {
		return
	}
	value, err := o.sealValue(deniedState{IDToken: rawIDToken, Created: o.now().Unix()}, deniedCookieName)
	if err != nil || len(value) > maxFlowCookieLen {
		o.log.Debug("oidc: id token of the denied account not kept", "bytes", len(value), "err", err)
		return
	}
	o.setCookie(w, deniedCookieName, value, int(flowMaxAge/time.Second))
}

// ForgetDenied deletes the cookie written by RememberDenied (if any).
func (o *OIDC) ForgetDenied(w http.ResponseWriter, r *http.Request) {
	if _, err := r.Cookie(deniedCookieName); err == nil {
		o.setCookie(w, deniedCookieName, "", -1)
	}
}

// SwitchAccountURL serves "Войти под другой учётной записью" after a denied
// login: it consumes the RememberDenied cookie and returns the provider's
// logout URL for that account (id_token_hint, so Keycloak asks no
// confirmation), coming back to PUBLIC_URL/login?state=switch_account. It
// returns "" when there is no valid cookie or the provider has no
// end_session_endpoint; the caller then starts a login with prompt=login.
func (o *OIDC) SwitchAccountURL(w http.ResponseWriter, r *http.Request) string {
	c, err := r.Cookie(deniedCookieName)
	if err != nil {
		return ""
	}
	o.setCookie(w, deniedCookieName, "", -1)
	var d deniedState
	if err := o.openValue(c.Value, deniedCookieName, &d); err != nil || d.IDToken == "" {
		return ""
	}
	if age := o.now().Sub(time.Unix(d.Created, 0)); age > flowMaxAge || age < -time.Minute {
		return ""
	}
	return o.logoutURL(d.IDToken, SwitchAccountState)
}

// RememberNext keeps next (a local path) across the provider logout that
// SwitchAccountURL starts: that logout can only come back to the bare
// PUBLIC_URL/login (post_logout_redirect_uri must match the registered URI
// exactly), so the path would otherwise be lost. TakeNext reads it back when
// the next login starts. Nothing is kept for "/".
func (o *OIDC) RememberNext(w http.ResponseWriter, next string) {
	next = SanitizeNext(next)
	if next == "/" {
		return
	}
	value, err := o.sealValue(nextState{Next: next, Created: o.now().Unix()}, nextCookieName)
	if err != nil || len(value) > maxFlowCookieLen {
		return
	}
	o.setCookie(w, nextCookieName, value, int(flowMaxAge/time.Second))
}

// TakeNext returns the path kept by RememberNext and deletes the cookie; ""
// when there is none (or it expired or was tampered with).
func (o *OIDC) TakeNext(w http.ResponseWriter, r *http.Request) string {
	c, err := r.Cookie(nextCookieName)
	if err != nil {
		return ""
	}
	o.setCookie(w, nextCookieName, "", -1)
	var st nextState
	if err := o.openValue(c.Value, nextCookieName, &st); err != nil {
		return ""
	}
	if age := o.now().Sub(time.Unix(st.Created, 0)); age > flowMaxAge || age < -time.Minute {
		return ""
	}
	if next := SanitizeNext(st.Next); next != "/" {
		return next
	}
	return ""
}

// setCookie writes one of the OIDC cookies (path /auth/oidc under PUBLIC_URL);
// maxAge < 0 deletes it.
func (o *OIDC) setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     o.cookiePath,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   o.secure,
		SameSite: http.SameSiteLaxMode, // the callback is a top-level cross-site GET
	})
}

// seal encrypts the flow state into a cookie value (sealValue).
func (o *OIDC) seal(f flowState) (string, error) { return o.sealValue(f, flowCookieName) }

// sealValue encrypts v (as JSON) with AES-256-GCM (random nonce, cookie name
// as associated data, so one cookie cannot pass for another) into base64url.
func (o *OIDC) sealValue(v any, cookieName string) (string, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, o.aead.NonceSize())
	_, _ = rand.Read(nonce)
	return base64.RawURLEncoding.EncodeToString(o.aead.Seal(nonce, nonce, plain, []byte(cookieName))), nil
}

// openValue decrypts a value sealed by sealValue for cookieName into v.
func (o *OIDC) openValue(s, cookieName string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	ns := o.aead.NonceSize()
	if err != nil || len(raw) < ns+o.aead.Overhead() {
		return errors.New("login cookie malformed")
	}
	plain, err := o.aead.Open(nil, raw[:ns], raw[ns:], []byte(cookieName))
	if err != nil {
		return errors.New("login cookie tampered or sealed with another key")
	}
	if err := json.Unmarshal(plain, v); err != nil {
		return errors.New("login cookie content invalid")
	}
	return nil
}

// open decrypts and validates a flow cookie value.
func (o *OIDC) open(v string) (flowState, error) {
	var f flowState
	if err := o.openValue(v, flowCookieName, &f); err != nil {
		return flowState{}, err
	}
	if f.State == "" || f.Nonce == "" || f.Verifier == "" {
		return flowState{}, errors.New("login cookie content invalid")
	}
	age := o.now().Sub(time.Unix(f.Created, 0))
	if age > flowMaxAge || age < -time.Minute {
		return flowState{}, errors.New("login cookie expired")
	}
	f.Next = SanitizeNext(f.Next)
	return f, nil
}

// isLoopbackHost reports localhost names and loopback IPs (dev setups where
// plain http is fine).
func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") || strings.HasSuffix(strings.ToLower(h), ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func isJWT(s string) bool {
	parts := strings.Split(s, ".")
	return len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] != ""
}

// roleValues extracts role/group values from token or userinfo claims:
// realm_access.roles, resource_access.<clientID>.roles, groups, roles,
// realm_roles and the optional extra dot-path claim.
func roleValues(claims map[string]any, clientID, extraPath string) []string {
	if claims == nil {
		return nil
	}
	var out []string
	if ra, ok := claims["realm_access"].(map[string]any); ok {
		out = append(out, stringValues(ra["roles"])...)
	}
	if res, ok := claims["resource_access"].(map[string]any); ok {
		if c, ok := res[clientID].(map[string]any); ok {
			out = append(out, stringValues(c["roles"])...)
		}
	}
	for _, k := range []string{"groups", "roles", "realm_roles"} {
		out = append(out, stringValues(claims[k])...)
	}
	if extraPath != "" {
		out = append(out, stringValues(claimPath(claims, extraPath))...)
	}
	return out
}

// claimPath resolves "a.b.c"; a literal key containing dots (namespaced
// claims like "https://example.org/roles") is tried first.
func claimPath(claims map[string]any, path string) any {
	if v, ok := claims[path]; ok {
		return v
	}
	var cur any = claims
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		if cur, ok = m[part]; !ok {
			return nil
		}
	}
	return cur
}

func stringValues(v any) []string {
	switch t := v.(type) {
	case string:
		if t != "" {
			return []string{t}
		}
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	}
	return nil
}

func buildOIDCIdentity(sub string, idc, uic map[string]any) *Identity {
	claim := func(k string) string {
		if s, _ := idc[k].(string); strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
		s, _ := uic[k].(string)
		return strings.TrimSpace(s)
	}
	email := claim("email")
	username := strings.ToLower(cmpOr(claim("preferred_username"), email, sub))
	display := cmpOr(claim("name"), strings.TrimSpace(claim("given_name")+" "+claim("family_name")), username)
	return &Identity{
		Source:      model.AuthOIDC,
		ExternalID:  sub,
		Username:    username,
		DisplayName: display,
		Email:       email,
	}
}

// Diagnose checks the OIDC setup step by step: discovery, issuer match,
// endpoints and keys, the redirect URI and the client secret. Details are
// Russian and contain no secrets; it is meant for the check-oidc CLI and the
// admin UI. It does not use or change the cached provider.
func (o *OIDC) Diagnose(ctx context.Context) []DiagStep {
	var d diagSteps
	ctx, cancel := context.WithTimeout(ctx, 4*oidcHTTPTimeout)
	defer cancel()

	wellKnown := strings.TrimRight(o.cfg.IssuerURL, "/") + "/.well-known/openid-configuration"
	var meta struct {
		Issuer        string   `json:"issuer"`
		AuthURL       string   `json:"authorization_endpoint"`
		TokenURL      string   `json:"token_endpoint"`
		JWKSURL       string   `json:"jwks_uri"`
		UserInfoURL   string   `json:"userinfo_endpoint"`
		EndSession    string   `json:"end_session_endpoint"`
		GrantTypes    []string `json:"grant_types_supported"`
		CodeChallenge []string `json:"code_challenge_methods_supported"`
	}
	status, body, err := o.get(ctx, wellKnown)
	switch {
	case err != nil:
		d.add("Получение настроек (discovery)", false, fmt.Sprintf("Не удалось открыть %s: %s", wellKnown, describeHTTPError(err)))
		return d
	case status != http.StatusOK:
		d.add("Получение настроек (discovery)", false, fmt.Sprintf("%s ответил кодом %d. Проверьте OIDC_ISSUER_URL (для Keycloak: https://сервер/realms/<realm>).", wellKnown, status))
		return d
	case json.Unmarshal(body, &meta) != nil || meta.Issuer == "":
		d.add("Получение настроек (discovery)", false, wellKnown+" вернул не настройки OpenID Connect. Проверьте OIDC_ISSUER_URL.")
		return d
	}
	d.add("Получение настроек (discovery)", true, "Настройки получены: "+wellKnown)

	if strings.TrimRight(meta.Issuer, "/") != strings.TrimRight(o.cfg.IssuerURL, "/") {
		d.add("Совпадение issuer", false, fmt.Sprintf("В OIDC_ISSUER_URL указано %q, а сервер называет себя %q. Укажите ровно %q (для Keycloak проверьте также KC_HOSTNAME).",
			o.cfg.IssuerURL, meta.Issuer, meta.Issuer))
		return d
	}
	d.add("Совпадение issuer", true, "Issuer совпадает: "+meta.Issuer)

	if meta.AuthURL == "" || meta.TokenURL == "" || meta.JWKSURL == "" {
		d.add("Адреса сервера", false, "В настройках сервера нет authorization_endpoint, token_endpoint или jwks_uri.")
		return d
	}
	status, body, err = o.get(ctx, meta.JWKSURL)
	var jwks struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err != nil || status != http.StatusOK || json.Unmarshal(body, &jwks) != nil || len(jwks.Keys) == 0 {
		d.add("Адреса сервера", false, "Не удалось получить ключи подписи токенов ("+meta.JWKSURL+").")
		return d
	}
	detail := fmt.Sprintf("Вход: %s; токены: %s; ключей подписи: %d.", meta.AuthURL, meta.TokenURL, len(jwks.Keys))
	if meta.EndSession == "" {
		detail += " Адреса выхода (end_session_endpoint) нет: выход будет только из Zoom Checker."
	}
	if len(meta.CodeChallenge) > 0 && !slices.Contains(meta.CodeChallenge, "S256") {
		detail += " Внимание: сервер не заявляет поддержку PKCE S256."
	}
	d.add("Адреса сервера", true, detail)

	d.add(o.diagRedirect(ctx, meta.AuthURL))
	if o.cfg.ClientSecret == "" {
		d.add("Секрет клиента", false, "OIDC_CLIENT_SECRET не задан. Скопируйте секрет из настроек клиента (Credentials).")
		return d
	}
	d.add(o.diagSecret(ctx, meta.TokenURL))
	return d
}

// diagRedirect asks the authorization endpoint whether it accepts our
// client_id and redirect URI (Keycloak answers 400 for unknown ones).
func (o *OIDC) diagRedirect(ctx context.Context, authURL string) (string, bool, string) {
	const name = "Адрес возврата (redirect URI)"
	cfg := oauth2.Config{ClientID: o.cfg.ClientID, Endpoint: oauth2.Endpoint{AuthURL: authURL}, RedirectURL: o.redirectURL, Scopes: o.cfg.Scopes}
	u := cfg.AuthCodeURL("diagnose", oidc.Nonce("diagnose"), oauth2.S256ChallengeOption(oauth2.GenerateVerifier()))
	status, _, loc, err := o.getNoRedirect(ctx, u)
	switch {
	case err != nil:
		return name, false, "Страница входа недоступна: " + describeHTTPError(err)
	case status == http.StatusOK:
		return name, true, "Сервер принимает клиента " + o.cfg.ClientID + " и адрес возврата " + o.redirectURL + "."
	case status >= 300 && status < 400 && strings.HasPrefix(loc, o.redirectURL):
		return name, true, "Сервер принимает адрес возврата " + o.redirectURL + "."
	case status >= 300 && status < 400:
		return name, true, "Сервер перенаправляет на страницу входа; адрес возврата: " + o.redirectURL + "."
	case status == http.StatusBadRequest:
		return name, false, fmt.Sprintf("Сервер отклонил запрос входа. Проверьте OIDC_CLIENT_ID (%s) и добавьте %s в список разрешённых адресов возврата (Valid redirect URIs) клиента.",
			o.cfg.ClientID, o.redirectURL)
	}
	return name, false, fmt.Sprintf("Неожиданный ответ страницы входа: код %d.", status)
}

// diagSecret checks the client secret with a client_credentials request. A
// client without service accounts is fine: the provider then rejects the grant
// only after it accepted the secret.
func (o *OIDC) diagSecret(ctx context.Context, tokenURL string) (string, bool, string) {
	const name = "Секрет клиента"
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return name, false, err.Error()
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(o.cfg.ClientID), url.QueryEscape(o.cfg.ClientSecret))
	resp, err := o.client.Do(req)
	if err != nil {
		return name, false, "Сервер выдачи токенов недоступен: " + describeHTTPError(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusOK {
		return name, true, "Секрет принят."
	}
	var e struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &e)
	desc := strings.ToLower(e.Description)
	switch {
	case e.Error == "invalid_client" || strings.Contains(desc, "invalid client"):
		return name, false, "Неверный OIDC_CLIENT_ID или OIDC_CLIENT_SECRET (сервер не принял данные клиента)."
	case e.Error == "unauthorized_client" || e.Error == "unsupported_grant_type" || strings.Contains(desc, "service account"):
		return name, true, "Секрет принят (выдача служебных токенов этому клиенту выключена — это нормально)."
	}
	return name, true, fmt.Sprintf("Секрет однозначно проверить не удалось (ответ %d %s). Проверьте вход на практике.", resp.StatusCode, e.Error)
}

func (o *OIDC) get(ctx context.Context, u string) (int, []byte, error) {
	status, body, _, err := o.do(ctx, u, o.client)
	return status, body, err
}

func (o *OIDC) getNoRedirect(ctx context.Context, u string) (int, []byte, string, error) {
	c := *o.client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return o.do(ctx, u, &c)
}

func (o *OIDC) do(ctx context.Context, u string, c *http.Client) (int, []byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, nil, "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, resp.Header.Get("Location"), err
}

func describeHTTPError(err error) string {
	return describeNetError(err, oidcHTTPTimeout)
}
