package main

// oauth.go — the bridge's sub-authorization-server.
//
// HCB's Doorkeeper only redirects to redirect URIs pre-registered on the
// OAuth app, but MCP clients (claude.ai, other AI agents, the hcb CLI) each
// bring their own callback. The bridge closes that gap by acting as a
// minimal authorization server of its own:
//
//	client → GET /oauth/authorize (validated redirect_uri, PKCE required)
//	       ← a consent page naming the exact redirect_uri the code will go to
//	user   → POST /oauth/authorize (approve)
//	       ← 302 to HCB's authorize page, redirect_uri = this server's
//	         registered /oauth/callback
//	HCB    → GET /oauth/callback?code&state (HCB may have shown no page of
//	         its own — see below — so this leg proves nothing by itself)
//	         the server exchanges the HCB code confidentially and mints its
//	         own single-use code bound to the client's redirect_uri + PKCE
//	       ← 302 client_redirect_uri?code=<minted>&state=<client state>
//
// One browser must carry all three legs. Consent is not a flag anyone can
// set: the approve and callback legs both have to present the cookie minted
// when the consent page was rendered, so approving your own request and then
// sending someone else to HCB with its state gets you nothing.
//	client → POST /oauth/token (code + code_verifier)
//	       ← the upstream HCB token response, verbatim
//
// Only ONE redirect URI ever needs registering on the HCB app: this
// server's /oauth/callback. Tokens are the user's own HCB tokens. No client
// registry is kept — possession of the single-use code plus the PKCE
// verifier is the client identity, so any redirect_uri works without
// pre-registration here or on HCB.
//
// That last property is why the consent step exists. Every client shares one
// HCB app, so HCB's own consent screen cannot be the only consent in the
// flow: it names the shared app rather than the client, and Doorkeeper skips
// it outright for an app the user has already authorized (matching_token?,
// which counts expired tokens). Without a consent step of its own the bridge
// would hand tokens to any redirect_uri on a single click. Since the user's
// reading of that page is the only thing standing between an attacker and a
// token, everything it displays must be exactly what the code does — see
// validClientRedirect and handleAuthorize.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	pendingTTL = 10 * time.Minute // browser round-trip through HCB's consent page
	codeTTL    = 5 * time.Minute  // minted code → token exchange

	// Bounds on state parked by unauthenticated requests. Real redirect URIs
	// and client state are far below these.
	maxRedirectLen = 2048
	maxStateLen    = 512
	maxPending     = 10000
)

// pendingAuth is a client authorize request waiting for the HCB round-trip,
// keyed by the state nonce we send upstream.
type pendingAuth struct {
	clientRedirect string
	clientState    string
	codeChallenge  string // S256, empty if the client skipped PKCE
	upstreamURI    string // exact redirect_uri sent to HCB (needed again at exchange)
	scope          string // normalized scope, replayed when consent is approved
	// consentSecret is the value of the cookie set when the consent page was
	// rendered. It is a secret, not an identifier: never log or echo it. Both
	// the approve leg and the callback leg must present it, which is what ties
	// the whole flow to one browser. Approval alone is not enough — an
	// attacker can approve their own request, and the upstream authorize URL
	// is public, so without this they could then send a victim to HCB
	// carrying that state and collect the victim's tokens.
	consentSecret string
	// cookieName is the exact name the consent cookie was issued under.
	// Re-deriving it per request would mean the approve or callback leg could
	// look for a name the GET leg never set — behind a proxy that sets
	// X-Forwarded-Proto inconsistently that fails every login — so it is
	// decided once and carried.
	cookieName string
	secure     bool
	expires    time.Time
}

// mintedCode is a redeemable authorization code holding the upstream token
// response, keyed by the code value.
type mintedCode struct {
	tokenJSON      []byte
	clientRedirect string
	codeChallenge  string
	expires        time.Time
}

// authBridge holds the in-flight state. Requests live in awaitingConsent
// until the user approves them and never move back, so a code path that
// reaches for the wrong map is visible rather than silently insecure —
// handleCallback simply cannot see a request nobody consented to.
type authBridge struct {
	mu              sync.Mutex
	awaitingConsent map[string]*pendingAuth
	consented       map[string]*pendingAuth
	codes           map[string]*mintedCode
	now             func() time.Time // test seam
}

func newAuthBridge() *authBridge {
	return &authBridge{
		awaitingConsent: map[string]*pendingAuth{},
		consented:       map[string]*pendingAuth{},
		codes:           map[string]*mintedCode{},
		now:             time.Now,
	}
}

func randNonce() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// sweepLocked drops expired entries; called opportunistically on writes.
func (b *authBridge) sweepLocked() {
	now := b.now()
	for _, m := range []map[string]*pendingAuth{b.awaitingConsent, b.consented} {
		for k, p := range m {
			if now.After(p.expires) {
				delete(m, k)
			}
		}
	}
	for k, c := range b.codes {
		if now.After(c.expires) {
			delete(b.codes, k)
		}
	}
}

// validClientRedirect accepts https URIs, plus plain http only for loopback
// hosts (CLI-style listeners). Anything else can't safely receive a code.
//
// The rest of the rejections keep the consent page honest — it promises the
// user that what they read is where the code goes. A fragment would push the
// code out of the query entirely (RFC 6749 §3.1.2 forbids one anyway), a
// pre-set code/state/error would let an attacker shadow the real parameter for
// clients that read the first occurrence, a non-ASCII host can render as a
// homoglyph of a domain the user trusts, and userinfo ("https://claude.ai@evil
// .example/cb") is a phishing lead-in with no legitimate use in a redirect.
func validClientRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || strings.Contains(raw, "#") {
		return false
	}
	q := u.Query()
	if q.Has("code") || q.Has("state") || q.Has("error") {
		return false
	}
	host := u.Hostname()
	for _, c := range host {
		if c > unicode.MaxASCII {
			return false
		}
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		return host == "localhost" || host == "127.0.0.1" || host == "::1"
	}
	return false
}

// clientRedirectWith appends OAuth response parameters to the client's
// redirect URI. Parsing instead of concatenating keeps a redirect_uri that
// already carries a query from mangling the result.
func clientRedirectWith(raw string, v url.Values) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	for k, vals := range v {
		q.Set(k, vals[0])
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// consentCookieName binds the consent page to one browser. Over https it uses
// the __Host- prefix, which forbids a Domain attribute and pins the cookie to
// this exact host: hcb-mcp is deployed under the shared hackclub.dev apex,
// which is not on the Public Suffix List, so without the prefix any sibling
// subdomain could plant or shadow the binding. The prefix requires Secure, so
// plain-http local development falls back to the bare name.
//
// The name carries the flow's nonce so two logins in one browser — a terminal
// `hcb login` alongside a connector, or a double-clicked button — cannot
// clobber each other's binding. (__Host- constrains the prefix, not the rest
// of the name.)
func consentCookieName(secure bool, nonce string) string {
	if secure {
		return "__Host-hcb_consent_" + nonce
	}
	return "hcb_consent_" + nonce
}

// setConsentCookie issues a fresh binding. The value is always server-minted:
// adopting one the request presented would let an attacker who can set a
// cookie on this domain choose the value the approval is checked against.
//
// SameSite=Lax, not Strict: the cookie has to ride HCB's top-level redirect
// back to /oauth/callback, which is a cross-site navigation. Lax still keeps
// it off the cross-site POST that a forged approval would need.
func setConsentCookie(w http.ResponseWriter, p *pendingAuth) {
	http.SetCookie(w, &http.Cookie{
		Name:     p.cookieName,
		Value:    p.consentSecret,
		Path:     "/", // required by the __Host- prefix
		MaxAge:   int(pendingTTL.Seconds()),
		HttpOnly: true,
		Secure:   p.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// consentGiven reports whether this request carries the binding minted when
// the consent page was rendered. Both the approve leg and the HCB callback
// must satisfy it: approval by itself only proves that *somebody* clicked,
// and an attacker can always click for their own request.
func (p *pendingAuth) consentGiven(r *http.Request) bool {
	c, err := r.Cookie(p.cookieName)
	return err == nil &&
		subtle.ConstantTimeCompare([]byte(c.Value), []byte(p.consentSecret)) == 1
}

// consentHeaders lock down the one page in this server a human is asked to act
// on: not framable, not readable cross-origin, not cached.
func consentHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Del("Access-Control-Allow-Origin") // the blanket cors middleware set it
	// No form-action: it is enforced on every hop of a redirect chain, so
	// 'self' would block this page's own 302 to HCB — silently, with
	// net::ERR_ABORTED — in Chrome and Safari. base-uri is what actually
	// stops a <base> tag from retargeting the form.
	h.Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Type", "text/html; charset=utf-8")
}

// scopeDescriptions spells out, in the user's terms, what each scope hands to
// the client. Anything unlisted falls back to the raw scope name.
var scopeDescriptions = map[string]string{
	"read":       "Read your own HCB organizations, transactions, cards, and receipts",
	"admin:read": "Read data across ALL HCB organizations (admin auditor access)",
}

var consentTmpl = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en">
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light">
<title>Send your HCB data to {{.Host}}?</title>
<style>
body{font:16px/1.5 system-ui,sans-serif;max-width:34rem;margin:3rem auto;padding:0 1.25rem;
     color:#1a1a1a;background:#fff}
.host{font-size:1.5rem;font-weight:700;overflow-wrap:anywhere}
.uri{color:#5c5c5c;font-size:.85rem;overflow-wrap:anywhere;margin-top:.25rem}
.admin{background:#fdecef;border:2px solid #c1121f;border-radius:6px;padding:.75rem 1rem;margin:1.25rem 0}
ul{padding-left:1.25rem}
.actions{display:flex;gap:.75rem;flex-wrap:wrap;margin-top:1.75rem}
button{font:inherit;padding:.6rem 1.2rem;border-radius:6px;cursor:pointer;border:2px solid transparent}
.deny{background:#e6e6e6;color:#1a1a1a;border-color:#8a8a8a}
.approve{background:#b3072c;color:#fff;border-color:#b3072c;text-align:left}
@media (forced-colors:active){button{border-color:ButtonText}}
</style>
<h1>Send your HCB data to this site?</h1>
<p class="host">{{.Host}}</p>
<p class="uri">{{.Destination}}</p>
<p>Whoever controls that address will be able to:</p>
<ul>{{range .Scopes}}<li>{{.}}</li>{{end}}</ul>
{{if .Elevated}}<p class="admin"><strong>This is admin access.</strong> It reads
every organization on HCB, not just yours.</p>{{end}}
<p>If you did not just start this login yourself — from your app, or by running
<strong>hcb login</strong> — close this page.</p>
<form method="POST" action="/oauth/authorize">
<input type="hidden" name="auth" value="{{.Nonce}}">
<div class="actions">
<button class="deny" type="submit" name="approve" value="no">Cancel</button>
<button class="approve" type="submit" name="approve" value="yes">Send my HCB data to {{.Host}}</button>
</div>
</form>
</html>
`))

type consentView struct {
	// Host is the parsed host, shown as the headline: it is the part that
	// decides where the code goes, and unlike the raw string it cannot be
	// front-loaded with something reassuring. Destination is the full URI,
	// shown underneath so nothing is hidden.
	Host        string
	Destination string
	Scopes      []string
	Elevated    bool // an admin scope was requested; call it out separately
	Nonce       string
}

// handleAuthorize starts the sub-flow. GET parks the client's request and
// renders the bridge's own consent page; POST is the approval, which bounces
// the browser to HCB.
//
// The bridge must ask for consent itself: HCB's screen names the one shared
// OAuth app rather than the client, and Doorkeeper skips it entirely for users
// who have authorized that app before — so without this step an attacker's
// link would silently mint tokens for their own callback.
func (b *authBridge) handleAuthorize(cfg httpConfig) http.HandlerFunc {
	start, approve := b.authorizeStart(), b.authorizeApprove(cfg)
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg.oauthClientID == "" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			start(w, r)
		case http.MethodPost:
			approve(w, r)
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func (b *authBridge) authorizeStart() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Validation failures are reported here rather than redirected to the
		// client. RFC 6749 §4.1.2.1 redirects them, but only to a *registered*
		// redirect URI; this bridge keeps no registry, so redirecting would
		// make /oauth/authorize an open redirector on the very origin we are
		// asking users to read the destination from.
		fail := func(code, desc string) {
			w.Header().Del("Access-Control-Allow-Origin") // set by the cors middleware
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": code, "error_description": desc,
			})
		}
		q := r.URL.Query()
		redirect := q.Get("redirect_uri")
		switch {
		case !validClientRedirect(redirect):
			fail("invalid_request", "redirect_uri must be https (or http on localhost), with no fragment, userinfo, code/state/error parameter, or non-ASCII host")
			return
		// Bounds keep an unauthenticated GET from parking megabytes of
		// attacker-chosen data for the full pendingTTL.
		case len(redirect) > maxRedirectLen || len(q.Get("state")) > maxStateLen:
			fail("invalid_request", "redirect_uri or state too long")
			return
		case q.Get("response_type") != "code":
			fail("unsupported_response_type", "only response_type=code is supported")
			return
		case q.Get("code_challenge_method") != "" && q.Get("code_challenge_method") != "S256":
			fail("invalid_request", "only code_challenge_method=S256 is supported")
			return
		// PKCE is mandatory. No client authenticates to this server, so the
		// verifier is the only thing binding a minted code to its requester.
		case q.Get("code_challenge") == "":
			fail("invalid_request", "code_challenge is required (PKCE, S256)")
			return
		}
		scope, err := normalizeOAuthScope(q.Get("scope"))
		if err != nil {
			fail("invalid_scope", err.Error())
			return
		}

		nonce := randNonce()
		secure := strings.HasPrefix(origin(r), "https://")
		p := &pendingAuth{
			clientRedirect: redirect,
			clientState:    q.Get("state"),
			codeChallenge:  q.Get("code_challenge"),
			upstreamURI:    origin(r) + "/oauth/callback",
			scope:          scope,
			consentSecret:  randNonce(),
			secure:         secure,
			cookieName:     consentCookieName(secure, nonce),
			expires:        b.now().Add(pendingTTL),
		}
		b.mu.Lock()
		b.sweepLocked()
		full := len(b.awaitingConsent) >= maxPending
		if !full {
			b.awaitingConsent[nonce] = p
		}
		b.mu.Unlock()
		if full {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "temporarily_unavailable", "error_description": "too many logins in flight — try again shortly",
			})
			return
		}

		dest, _ := url.Parse(redirect) // already parsed by validClientRedirect
		view := consentView{Host: dest.Host, Destination: redirect, Nonce: nonce}
		for _, sc := range strings.Fields(scope) {
			if strings.HasPrefix(sc, "admin:") {
				view.Elevated = true
			}
			if d, ok := scopeDescriptions[sc]; ok {
				view.Scopes = append(view.Scopes, d)
			} else {
				view.Scopes = append(view.Scopes, sc)
			}
		}
		setConsentCookie(w, p)
		consentHeaders(w)
		_ = consentTmpl.Execute(w, view)
	}
}

// authorizeApprove handles the consent form submission and promotes the
// request from awaitingConsent to consented. The nonce reaches only the
// consent page's body, and the cookie check means a nonce an attacker minted
// in their own browser is useless in the victim's.
func (b *authBridge) authorizeApprove(cfg httpConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// A browser labels every cross-origin form POST. Note SameSite alone
		// would not be enough: hackclub.dev is not on the Public Suffix List,
		// so a sibling subdomain counts as same-site and would keep the cookie.
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
			http.Error(w, "cross-site approval rejected", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		nonce := r.PostForm.Get("auth")

		b.mu.Lock()
		p := b.awaitingConsent[nonce]
		if p != nil && b.now().After(p.expires) {
			delete(b.awaitingConsent, nonce)
			p = nil
		}
		b.mu.Unlock()
		if p == nil {
			http.Error(w, "login attempt expired or unknown — start over", http.StatusBadRequest)
			return
		}

		// A failed check never drops the pending entry: the nonce travels
		// onward as the upstream state, so a third party who learns one could
		// otherwise kill a legitimate login in flight.
		if !p.consentGiven(r) {
			http.Error(w, "this approval did not come from the browser that was shown the consent page (are cookies enabled?) — start over", http.StatusForbidden)
			return
		}

		if r.PostForm.Get("approve") != "yes" { // Cancel
			b.mu.Lock()
			delete(b.awaitingConsent, nonce)
			b.mu.Unlock()
			http.Redirect(w, r, p.redirectBack(url.Values{"error": {"access_denied"}}), http.StatusFound)
			return
		}

		b.mu.Lock()
		delete(b.awaitingConsent, nonce)
		b.consented[nonce] = p
		b.mu.Unlock()
		http.Redirect(w, r, cfg.hcbBaseURL+"/api/v4/oauth/authorize?"+url.Values{
			"client_id":     {cfg.oauthClientID},
			"redirect_uri":  {p.upstreamURI},
			"response_type": {"code"},
			"scope":         {p.scope},
			"state":         {nonce},
		}.Encode(), http.StatusFound)
	}
}

// redirectBack builds the hop to the client's redirect_uri, always carrying
// the client's state (RFC 6749 §4.1.2). Every response to the client goes
// through here so no call site can forget it.
func (p *pendingAuth) redirectBack(v url.Values) string {
	if p.clientState != "" {
		v.Set("state", p.clientState)
	}
	return clientRedirectWith(p.clientRedirect, v)
}

// handleCallback finishes the upstream leg: exchange HCB's code for tokens
// (client secret stays server-side), mint our own code, send the browser
// back to the client.
func (b *authBridge) handleCallback(cfg httpConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || cfg.oauthClientID == "" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		// Look up, verify, and consume under one lock. The binding is checked
		// before the entry is dropped so that a third party who learns a state
		// cannot burn a login in flight, and consuming inside the same lock
		// keeps two concurrent callbacks from both redeeming it.
		//
		// Verifying here is what defeats self-approval: an attacker can always
		// click Continue on their own request, then send a victim to HCB
		// carrying its state. The victim's browser has no matching cookie.
		b.mu.Lock()
		p := b.consented[q.Get("state")] // only ever the approved map
		ok := p != nil && !b.now().After(p.expires) && p.consentGiven(r)
		if ok {
			delete(b.consented, q.Get("state")) // single use
		}
		b.mu.Unlock()
		if !ok {
			http.Error(w, "this login did not start in this browser, or it expired — start over", http.StatusForbidden)
			return
		}
		back := func(v url.Values) {
			http.Redirect(w, r, p.redirectBack(v), http.StatusFound)
		}
		if e := q.Get("error"); e != "" { // user denied on HCB's page
			back(url.Values{"error": {e}})
			return
		}
		if q.Get("code") == "" {
			back(url.Values{"error": {"invalid_request"}, "error_description": {"upstream callback missing code"}})
			return
		}

		form := url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {q.Get("code")},
			"client_id":     {cfg.oauthClientID},
			"client_secret": {cfg.oauthClientSecret},
			"redirect_uri":  {p.upstreamURI},
		}
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
			cfg.hcbBaseURL+"/api/v4/oauth/token", strings.NewReader(form.Encode()))
		if err != nil {
			back(url.Values{"error": {"server_error"}})
			return
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			back(url.Values{"error": {"server_error"}, "error_description": {"upstream token endpoint unreachable"}})
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode != http.StatusOK {
			back(url.Values{"error": {"invalid_grant"}, "error_description": {"upstream code exchange failed"}})
			return
		}

		code := randNonce()
		b.mu.Lock()
		b.sweepLocked()
		b.codes[code] = &mintedCode{
			tokenJSON:      body,
			clientRedirect: p.clientRedirect,
			codeChallenge:  p.codeChallenge,
			expires:        b.now().Add(codeTTL),
		}
		b.mu.Unlock()
		back(url.Values{"code": {code}})
	}
}

// redeem consumes a minted code, enforcing PKCE and redirect_uri binding.
// ok=false means the code isn't ours (the caller should proxy upstream —
// e.g. refresh grants or legacy direct-to-HCB codes).
func (b *authBridge) redeem(code, verifier, redirectURI string) (tokenJSON []byte, ok bool, errCode string) {
	if code == "" {
		return nil, false, ""
	}
	b.mu.Lock()
	m := b.codes[code]
	delete(b.codes, code) // single use, even on failed attempts
	b.mu.Unlock()
	if m == nil {
		return nil, false, ""
	}
	if b.now().After(m.expires) {
		return nil, true, "invalid_grant"
	}
	if redirectURI != m.clientRedirect {
		return nil, true, "invalid_grant"
	}
	if m.codeChallenge != "" {
		sum := sha256.Sum256([]byte(verifier))
		got := base64.RawURLEncoding.EncodeToString(sum[:])
		if verifier == "" || subtle.ConstantTimeCompare([]byte(got), []byte(m.codeChallenge)) != 1 {
			return nil, true, "invalid_grant"
		}
	}
	return m.tokenJSON, true, ""
}
