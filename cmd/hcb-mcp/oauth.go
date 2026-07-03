package main

// oauth.go — the bridge's sub-authorization-server.
//
// HCB's Doorkeeper only redirects to redirect URIs pre-registered on the
// OAuth app, but MCP clients (claude.ai, other AI agents, the hcb CLI) each
// bring their own callback. The bridge closes that gap by acting as a
// minimal authorization server of its own:
//
//	client → GET /oauth/authorize (any redirect_uri, PKCE)
//	       ← 302 to HCB's authorize page, redirect_uri = this server's
//	         registered /oauth/callback
//	HCB    → GET /oauth/callback?code&state (user approved on HCB's page)
//	         the server exchanges the HCB code confidentially and mints its
//	         own single-use code bound to the client's redirect_uri + PKCE
//	       ← 302 client_redirect_uri?code=<minted>&state=<client state>
//	client → POST /oauth/token (code + code_verifier)
//	       ← the upstream HCB token response, verbatim
//
// Only ONE redirect URI ever needs registering on the HCB app: this
// server's /oauth/callback. Tokens are the user's own HCB tokens and consent
// happens on HCB's authorize page. No client registry is kept — possession
// of the single-use code plus the PKCE verifier is the client identity, so
// any redirect_uri works without pre-registration here or on HCB.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	pendingTTL = 10 * time.Minute // browser round-trip through HCB's consent page
	codeTTL    = 5 * time.Minute  // minted code → token exchange
)

// pendingAuth is a client authorize request waiting for the HCB round-trip,
// keyed by the state nonce we send upstream.
type pendingAuth struct {
	clientRedirect string
	clientState    string
	codeChallenge  string // S256, empty if the client skipped PKCE
	upstreamURI    string // exact redirect_uri sent to HCB (needed again at exchange)
	expires        time.Time
}

// mintedCode is a redeemable authorization code holding the upstream token
// response, keyed by the code value.
type mintedCode struct {
	tokenJSON      []byte
	clientRedirect string
	codeChallenge  string
	expires        time.Time
}

type authBridge struct {
	mu      sync.Mutex
	pending map[string]*pendingAuth
	codes   map[string]*mintedCode
	now     func() time.Time // test seam
}

func newAuthBridge() *authBridge {
	return &authBridge{
		pending: map[string]*pendingAuth{},
		codes:   map[string]*mintedCode{},
		now:     time.Now,
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
	for k, p := range b.pending {
		if now.After(p.expires) {
			delete(b.pending, k)
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
func validClientRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		h := u.Hostname()
		return h == "localhost" || h == "127.0.0.1" || h == "::1"
	}
	return false
}

// handleAuthorize starts the sub-flow: park the client's request and bounce
// the browser to HCB's consent page with our own registered callback.
func (b *authBridge) handleAuthorize(cfg httpConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || cfg.oauthClientID == "" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		redirect := q.Get("redirect_uri")
		if !validClientRedirect(redirect) {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error":             "invalid_request",
				"error_description": "redirect_uri must be https, or http on localhost",
			})
			return
		}
		// Errors after this point go back to the client's redirect_uri, per
		// RFC 6749 §4.1.2.1.
		fail := func(code, desc string) {
			v := url.Values{"error": {code}, "error_description": {desc}}
			if s := q.Get("state"); s != "" {
				v.Set("state", s)
			}
			http.Redirect(w, r, redirect+redirectSep(redirect)+v.Encode(), http.StatusFound)
		}
		if q.Get("response_type") != "code" {
			fail("unsupported_response_type", "only response_type=code is supported")
			return
		}
		if m := q.Get("code_challenge_method"); m != "" && m != "S256" {
			fail("invalid_request", "only code_challenge_method=S256 is supported")
			return
		}
		if q.Get("code_challenge") == "" && q.Get("code_challenge_method") != "" {
			fail("invalid_request", "code_challenge_method without code_challenge")
			return
		}
		scope := q.Get("scope")
		scope, err := normalizeOAuthScope(scope)
		if err != nil {
			fail("invalid_scope", err.Error())
			return
		}

		nonce := randNonce()
		upstreamURI := origin(r) + "/oauth/callback"
		b.mu.Lock()
		b.sweepLocked()
		b.pending[nonce] = &pendingAuth{
			clientRedirect: redirect,
			clientState:    q.Get("state"),
			codeChallenge:  q.Get("code_challenge"),
			upstreamURI:    upstreamURI,
			expires:        b.now().Add(pendingTTL),
		}
		b.mu.Unlock()

		authURL := cfg.hcbBaseURL + "/api/v4/oauth/authorize?" + url.Values{
			"client_id":     {cfg.oauthClientID},
			"redirect_uri":  {upstreamURI},
			"response_type": {"code"},
			"scope":         {scope},
			"state":         {nonce},
		}.Encode()
		http.Redirect(w, r, authURL, http.StatusFound)
	}
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
		b.mu.Lock()
		p := b.pending[q.Get("state")]
		delete(b.pending, q.Get("state")) // single use
		b.mu.Unlock()
		if p == nil || b.now().After(p.expires) {
			http.Error(w, "login attempt expired or unknown — start over", http.StatusBadRequest)
			return
		}
		back := func(v url.Values) {
			if p.clientState != "" {
				v.Set("state", p.clientState)
			}
			http.Redirect(w, r, p.clientRedirect+redirectSep(p.clientRedirect)+v.Encode(), http.StatusFound)
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

// redirectSep picks ? or & depending on whether the URI already has a query.
func redirectSep(uri string) string {
	if strings.Contains(uri, "?") {
		return "&"
	}
	return "?"
}
