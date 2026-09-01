package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// fakeDoorkeeper models HCB's OAuth server closely enough to reproduce the
// reported "consent bypass". The relevant upstream behaviour is Doorkeeper
// 5.8.2's AuthorizationsController#can_authorize_response? — for a
// *confidential* app it skips the consent screen whenever matching_token?
// finds an unrevoked token for the same (app, user, exact scope set). That
// lookup passes include_expired: true, so the skip survives token expiry and
// lasts until the user revokes the app.
//
// That is why HCB's consent screen cannot be the bridge's only consent: every
// bridge client shares one HCB app, so one returning user's prior grant makes
// HCB wave through a request that any stranger could have started.
type fakeDoorkeeper struct {
	priorGrants  map[string]bool // exact scope strings the user already granted
	consentShown int             // times a human would have had to click on HCB
}

func (d *fakeDoorkeeper) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/oauth/authorize":
			q := r.URL.Query()
			if !d.priorGrants[q.Get("scope")] {
				d.consentShown++
				w.Write([]byte("HCB consent screen — a human must click Authorize"))
				return
			}
			http.Redirect(w, r, q.Get("redirect_uri")+"?"+url.Values{
				"code": {"hcb-code-1"}, "state": {q.Get("state")},
			}.Encode(), http.StatusFound)
		case "/api/v4/oauth/token":
			r.ParseForm()
			if r.PostForm.Get("code") != "hcb-code-1" || r.PostForm.Get("client_secret") != "test-client-secret" {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token":"VICTIM_HCB_TOKEN","refresh_token":"VICTIM_REFRESH","token_type":"Bearer","expires_in":7200,"scope":"read","created_at":1}`))
		default:
			t.Errorf("unexpected upstream path %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// browser is an http.Client with a cookie jar that surfaces redirects instead
// of following them, so a test can walk the flow hop by hop the way a real
// browser would — and so two independent "browsers" don't share cookies.
type browser struct {
	t      *testing.T
	client *http.Client
	// forwardedProto, when set, is sent as X-Forwarded-Proto to imitate the
	// TLS-terminating ingress the hosted deployment sits behind.
	forwardedProto string
}

func newBrowser(t *testing.T) *browser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &browser{t: t, client: &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

func (b *browser) do(req *http.Request) (*http.Response, string) {
	b.t.Helper()
	if b.forwardedProto != "" {
		req.Header.Set("X-Forwarded-Proto", b.forwardedProto)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp, string(body)
}

func (b *browser) get(u string) (*http.Response, string) {
	b.t.Helper()
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		b.t.Fatal(err)
	}
	return b.do(req)
}

// follow walks 302s until the next hop lands on stopHost (the client's
// callback, which no test server answers) or a page renders instead. It
// returns the stop URL, or nil plus the rendered body.
func (b *browser) follow(from *http.Response, fromBody string, cur *url.URL, stopHost string) (*url.URL, string) {
	b.t.Helper()
	resp, body := from, fromBody
	for hop := 0; hop < 10; hop++ {
		if resp.StatusCode != http.StatusFound {
			return nil, body
		}
		next, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			b.t.Fatal(err)
		}
		cur = cur.ResolveReference(next)
		if cur.Host == stopHost {
			return cur, ""
		}
		resp, body = b.get(cur.String())
	}
	b.t.Fatal("redirect loop")
	return nil, ""
}

var nonceRE = regexp.MustCompile(`name="auth" value="([0-9a-f]{64})"`)

func consentNonce(t *testing.T, page string) string {
	t.Helper()
	m := nonceRE.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no consent nonce in page: %.300s", page)
	}
	return m[1]
}

func approve(t *testing.T, b *browser, bridgeURL, nonce, secFetchSite string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, bridgeURL+"/oauth/authorize",
		strings.NewReader(url.Values{"auth": {nonce}, "approve": {"yes"}}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if secFetchSite != "" {
		req.Header.Set("Sec-Fetch-Site", secFetchSite)
	}
	resp, _ := b.do(req)
	return resp
}

// authorizeVia starts an authorize request and clicks through the bridge's
// consent page. It returns the upstream HCB authorize URL the browser is sent
// to, plus that browser — the callback leg has to run in the same one, since
// it must present the consent cookie.
func authorizeVia(t *testing.T, bridgeURL string, q url.Values) (*browser, *url.URL) {
	t.Helper()
	b := newBrowser(t)
	resp, page := b.get(bridgeURL + "/oauth/authorize?" + q.Encode())
	if resp.StatusCode == http.StatusOK { // consent page
		resp = approve(t, b, bridgeURL, consentNonce(t, page), "same-origin")
		page = ""
	}
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize status = %d, want 302: %s", resp.StatusCode, fmt.Sprintf("%.300s", page))
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return b, loc
}

// get302 issues a GET in this browser and returns the parsed Location of the
// expected redirect.
func (b *browser) get302(u string) *url.URL {
	b.t.Helper()
	resp, body := b.get(u)
	if resp.StatusCode != http.StatusFound {
		b.t.Fatalf("status = %d, want 302: %.300s", resp.StatusCode, body)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		b.t.Fatal(err)
	}
	return loc
}

// authorizeLink is the entire exploit from the report: a URL. No client
// registration and no client_id — nothing the attacker must prove. (The same
// shape serves a legitimate client; only the callback differs.)
func authorizeLink(bridgeURL, callback string) string {
	return bridgeURL + "/oauth/authorize?" + url.Values{
		"response_type":         {"code"},
		"redirect_uri":          {callback},
		"scope":                 {"read"},
		"code_challenge":        {pkce("v")},
		"code_challenge_method": {"S256"},
	}.Encode()
}

// bridgeWithPriorGrant returns a bridge whose user already authorized the
// shared HCB app once (via `hcb login` or the hosted MCP connector), which is
// the precondition for HCB's silent re-approval.
func bridgeWithPriorGrant(t *testing.T) (bridge, hcb *httptest.Server, dk *fakeDoorkeeper) {
	t.Helper()
	dk = &fakeDoorkeeper{priorGrants: map[string]bool{"read": true}}
	hcb = dk.start(t)
	bridge = httptest.NewServer(httpHandler(testCfg(hcb.URL)))
	t.Cleanup(bridge.Close)
	return bridge, hcb, dk
}

// TestConsentBypassViaArbitraryRedirectURI is the regression test for the
// reported vulnerability: following an attacker's link must not deliver an
// authorization code to the attacker's callback. Before the bridge grew its
// own consent step this walked straight through to evil.example and the
// attacker redeemed the victim's HCB access and refresh tokens.
func TestConsentBypassViaArbitraryRedirectURI(t *testing.T) {
	bridge, _, dk := bridgeWithPriorGrant(t)
	const attackerCallback = "https://evil.example/cb"

	victim := newBrowser(t)
	link := authorizeLink(bridge.URL, attackerCallback)
	resp, page := victim.get(link)
	base, _ := url.Parse(link)
	landed, page := victim.follow(resp, page, base, "evil.example")

	if landed != nil {
		t.Fatalf("EXPLOITED: code delivered to the attacker at %s", landed)
	}
	if dk.consentShown != 0 {
		t.Errorf("the bridge contacted HCB before asking the user: %d upstream consent screens", dk.consentShown)
	}
	if !strings.Contains(page, "https://evil.example") {
		t.Errorf("consent page must name the real destination, got: %s", fmt.Sprintf("%.300s", page))
	}
	if !strings.Contains(page, "Read your own HCB") {
		t.Errorf("consent page must spell out the scopes, got: %s", fmt.Sprintf("%.300s", page))
	}
}

// TestConsentApprovalRejectsCrossSiteForgery covers the way an attacker would
// try to click the consent button for the victim: mint a nonce in their own
// browser, then have the victim's browser auto-submit the form. The nonce is
// only disclosed in the consent page body, and approval is bound to the
// browser that was shown it, so a foreign nonce is useless.
func TestConsentApprovalRejectsCrossSiteForgery(t *testing.T) {
	bridge, _, _ := bridgeWithPriorGrant(t)
	const attackerCallback = "https://evil.example/cb"

	// Each case mints its own nonce so none can mask another's failure.
	freshNonce := func(t *testing.T) string {
		t.Helper()
		_, page := newBrowser(t).get(authorizeLink(bridge.URL, attackerCallback))
		return consentNonce(t, page)
	}

	for _, tc := range []struct {
		name         string
		secFetchSite string
		// victimCookie, when set, is a valid-looking binding the victim's
		// browser already holds — this is what actually exercises the
		// constant-time comparison rather than short-circuiting on "no cookie".
		victimCookie string
	}{
		{name: "auto-submitted from the attacker's page", secFetchSite: "cross-site"},
		{name: "replayed with no Sec-Fetch-Site header"},
		{name: "victim holds a different valid binding", victimCookie: strings.Repeat("ab", 32)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nonce := freshNonce(t)
			victim := newBrowser(t)
			if tc.victimCookie != "" {
				u, _ := url.Parse(bridge.URL)
				victim.client.Jar.SetCookies(u, []*http.Cookie{
					{Name: consentCookieName(false, nonce), Value: tc.victimCookie, Path: "/"},
				})
			}
			resp := approve(t, victim, bridge.URL, nonce, tc.secFetchSite)
			if resp.StatusCode == http.StatusFound {
				t.Fatalf("EXPLOITED: forged approval redirected to %s", resp.Header.Get("Location"))
			}
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("status = %d, want 403", resp.StatusCode)
			}
		})
	}
}

// TestApprovalFailureLeavesLoginAlive — the nonce travels onward as the
// upstream state, so it is not a lasting secret. A third party who learns one
// must not be able to kill the real user's login by POSTing it.
func TestApprovalFailureLeavesLoginAlive(t *testing.T) {
	bridge, _, _ := bridgeWithPriorGrant(t)
	const clientCallback = "https://claude.ai/api/mcp/auth_callback"

	user := newBrowser(t)
	_, page := user.get(authorizeLink(bridge.URL, clientCallback))
	nonce := consentNonce(t, page)

	if got := approve(t, newBrowser(t), bridge.URL, nonce, "").StatusCode; got != http.StatusForbidden {
		t.Fatalf("forged approval = %d, want 403", got)
	}
	if got := approve(t, user, bridge.URL, nonce, "same-origin").StatusCode; got != http.StatusFound {
		t.Errorf("real user's approval = %d after a forged attempt, want 302", got)
	}
}

// TestConsentApprovedFlowCompletes is the happy path: a user who actually
// sees the page and clicks through still gets a working authorization code.
func TestConsentApprovedFlowCompletes(t *testing.T) {
	bridge, _, _ := bridgeWithPriorGrant(t)
	const clientCallback = "https://claude.ai/api/mcp/auth_callback"

	user := newBrowser(t)
	start := authorizeLink(bridge.URL, clientCallback) // same shape, legitimate client
	_, page := user.get(start)
	nonce := consentNonce(t, page)

	resp := approve(t, user, bridge.URL, nonce, "same-origin")
	base, _ := url.Parse(bridge.URL + "/oauth/authorize")
	landed, body := user.follow(resp, "", base, "claude.ai")
	if landed == nil {
		t.Fatalf("approved flow stalled: %s", fmt.Sprintf("%.300s", body))
	}
	code := landed.Query().Get("code")
	if code == "" {
		t.Fatalf("no code at %s", landed)
	}

	tokenResp, err := http.Post(bridge.URL+"/oauth/token", "application/x-www-form-urlencoded",
		strings.NewReader(url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {code},
			"code_verifier": {"v"},
			"redirect_uri":  {clientCallback},
		}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer tokenResp.Body.Close()
	var tok map[string]any
	json.NewDecoder(tokenResp.Body).Decode(&tok)
	if tok["access_token"] != "VICTIM_HCB_TOKEN" {
		t.Fatalf("redeem = %d %v", tokenResp.StatusCode, tok)
	}
}

// TestLoopbackRedirectAlsoAsksForConsent covers `hcb login`. Loopback clients
// get the same page as everyone else: a code delivered to the victim's own
// machine is still readable by anything local (a browser extension holding
// only the tabs permission, a dev server that logs request paths), and
// exempting them would leave one uninspected path to a silent token grant.
func TestLoopbackRedirectAlsoAsksForConsent(t *testing.T) {
	bridge, _, _ := bridgeWithPriorGrant(t)
	const cli = "http://localhost:8910/callback"

	user := newBrowser(t)
	_, page := user.get(authorizeLink(bridge.URL, cli))
	if !strings.Contains(page, cli) {
		t.Fatalf("loopback client must see a consent page naming %s, got: %.300s", cli, page)
	}
	resp := approve(t, user, bridge.URL, consentNonce(t, page), "same-origin")
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusFound || !strings.Contains(loc.Path, "/api/v4/oauth/authorize") {
		t.Fatalf("approved loopback login went to %d %s, want HCB authorize", resp.StatusCode, loc)
	}
}

// TestConsentCancelDeniesClient checks the Cancel button reports a normal
// OAuth denial back to the client rather than leaving it hanging.
func TestConsentCancelDeniesClient(t *testing.T) {
	bridge, _, dk := bridgeWithPriorGrant(t)
	const clientCallback = "https://claude.ai/api/mcp/auth_callback"

	user := newBrowser(t)
	_, page := user.get(authorizeLink(bridge.URL, clientCallback) + "&state=s1")
	nonce := consentNonce(t, page)

	req, err := http.NewRequest(http.MethodPost, bridge.URL+"/oauth/authorize",
		strings.NewReader(url.Values{"auth": {nonce}, "approve": {"no"}}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, _ := user.do(req)

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("cancel status = %d, want 302", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(loc.String(), clientCallback) ||
		loc.Query().Get("error") != "access_denied" || loc.Query().Get("state") != "s1" {
		t.Errorf("cancel redirect = %s, want access_denied back to the client", loc)
	}
	if dk.consentShown != 0 {
		t.Errorf("cancelled flow still reached HCB")
	}
}

// TestUnapprovedPendingRejectedAtCallback is the regression test for the
// second, subtler half of the bypass: rendering a consent page is not enough
// if the callback will honour a request that was never approved.
//
// Every input the attacker needs is public — /oauth/register hands the shared
// client_id to anyone, and the bridge's callback URL is fixed — so they can
// park a pending in their own browser, never click Continue, and send the
// victim a link straight to HCB carrying that state. The consent page is
// never shown to anyone who matters.
func TestUnapprovedPendingRejectedAtCallback(t *testing.T) {
	bridge, hcb, _ := bridgeWithPriorGrant(t)
	const attackerCallback = "https://evil.example/cb"

	attacker := newBrowser(t)
	_, page := attacker.get(authorizeLink(bridge.URL, attackerCallback))
	nonce := consentNonce(t, page) // parked, never approved

	reg, err := http.Post(bridge.URL+"/oauth/register", "application/json",
		strings.NewReader(`{"redirect_uris":["https://evil.example/cb"]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Body.Close()
	var regBody map[string]any
	json.NewDecoder(reg.Body).Decode(&regBody)
	clientID, _ := regBody["client_id"].(string)
	if clientID == "" {
		t.Fatal("no client_id from the public registration stub")
	}

	victim := newBrowser(t)
	direct := hcb.URL + "/api/v4/oauth/authorize?" + url.Values{
		"client_id":     {clientID},
		"redirect_uri":  {bridge.URL + "/oauth/callback"},
		"response_type": {"code"},
		"scope":         {"read"},
		"state":         {nonce},
	}.Encode()
	resp, body := victim.get(direct)
	base, _ := url.Parse(direct)
	landed, _ := victim.follow(resp, body, base, "evil.example")
	if landed != nil {
		t.Fatalf("EXPLOITED: consent page skipped entirely, code delivered to %s", landed)
	}
}

// TestConsentPageShowsFullRedirectURI guards the page's core promise. Showing
// only the origin would let an attacker borrow a trusted one: the user reads
// "https://claude.ai" while the code goes to an attacker-chosen path on it.
func TestConsentPageShowsFullRedirectURI(t *testing.T) {
	bridge, _, _ := bridgeWithPriorGrant(t)
	const callback = "https://claude.ai/attacker/controlled/path"

	_, page := newBrowser(t).get(authorizeLink(bridge.URL, callback))
	if !strings.Contains(page, callback) {
		t.Errorf("consent page must show the full redirect_uri %q, got: %.400s", callback, page)
	}
}

// TestAuthorizeRejectsUnsafeRedirectShapes covers redirect URIs that would
// break the page's promise even though their origin looks fine.
func TestAuthorizeRejectsUnsafeRedirectShapes(t *testing.T) {
	bridge, _, _ := bridgeWithPriorGrant(t)
	for name, uri := range map[string]string{
		"fragment steals the code":  "https://claude.ai/cb#frag",
		"pre-set code shadows ours": "https://claude.ai/cb?code=attacker",
		"pre-set state":             "https://claude.ai/cb?state=attacker",
		"homoglyph host":            "https://clаude.ai/cb",             // Cyrillic а
		"userinfo phishing prefix":  "https://claude.ai@example.com/cb", // host is example.com
	} {
		t.Run(name, func(t *testing.T) {
			resp, _ := newBrowser(t).get(authorizeLink(bridge.URL, uri))
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("redirect_uri %q accepted with %d, want 400", uri, resp.StatusCode)
			}
		})
	}
}

// TestAuthorizeErrorsDoNotRedirect keeps /oauth/authorize from doubling as an
// open redirector on the origin whose consent page users are told to trust.
func TestAuthorizeErrorsDoNotRedirect(t *testing.T) {
	bridge, _, _ := bridgeWithPriorGrant(t)
	resp, _ := newBrowser(t).get(bridge.URL + "/oauth/authorize?" + url.Values{
		"response_type": {"token"}, // unsupported
		"redirect_uri":  {"https://evil.example/"},
	}.Encode())
	if resp.StatusCode == http.StatusFound {
		t.Fatalf("validation error redirected to %s", resp.Header.Get("Location"))
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestAuthorizeRequiresPKCE — no client authenticates to this server, so the
// verifier is the only thing binding a minted code to whoever requested it.
func TestAuthorizeRequiresPKCE(t *testing.T) {
	bridge, _, _ := bridgeWithPriorGrant(t)
	resp, _ := newBrowser(t).get(bridge.URL + "/oauth/authorize?" + url.Values{
		"response_type": {"code"},
		"redirect_uri":  {"https://claude.ai/cb"},
	}.Encode())
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing code_challenge accepted with %d, want 400", resp.StatusCode)
	}
}

// TestSelfApprovedRequestRejectedAtCallback is the regression test for the
// sharpest version of the bypass: the attacker does see a consent page, and
// approves it — for their own request, in their own browser.
//
// Consent alone cannot be the check, because clicking Continue is something
// the attacker can always do. What makes this fail is that the callback leg
// demands the same cookie, and the victim's browser never had it.
func TestSelfApprovedRequestRejectedAtCallback(t *testing.T) {
	bridge, hcb, _ := bridgeWithPriorGrant(t)
	const attackerCallback = "https://evil.example/cb"

	attacker := newBrowser(t)
	_, page := attacker.get(authorizeLink(bridge.URL, attackerCallback))
	nonce := consentNonce(t, page)
	if got := approve(t, attacker, bridge.URL, nonce, "same-origin").StatusCode; got != http.StatusFound {
		t.Fatalf("attacker's self-approval = %d, want 302 (it is allowed — it is their own request)", got)
	}

	// The victim authenticates at HCB carrying the attacker's state.
	victim := newBrowser(t)
	direct := hcb.URL + "/api/v4/oauth/authorize?" + url.Values{
		"client_id":     {"test-client-id"},
		"redirect_uri":  {bridge.URL + "/oauth/callback"},
		"response_type": {"code"},
		"scope":         {"read"},
		"state":         {nonce},
	}.Encode()
	resp, body := victim.get(direct)
	base, _ := url.Parse(direct)
	landed, _ := victim.follow(resp, body, base, "evil.example")
	if landed != nil {
		t.Fatalf("EXPLOITED: victim's code delivered to the attacker at %s", landed)
	}
}

// TestConsentCookieBindsProductionHTTPS drives the flow the way the hosted
// deployment does — behind a TLS-terminating proxy — so the __Host- prefixed
// cookie is the one under test. Without this the suite only ever exercised
// the plain-http fallback name, which production never uses.
func TestConsentCookieBindsProductionHTTPS(t *testing.T) {
	bridge, _, _ := bridgeWithPriorGrant(t)
	const clientCallback = "https://claude.ai/api/mcp/auth_callback"

	user := newBrowser(t)
	user.forwardedProto = "https" // what the ingress adds
	resp, page := user.get(authorizeLink(bridge.URL, clientCallback))

	var consent *http.Cookie
	for _, c := range resp.Cookies() {
		if strings.HasPrefix(c.Name, "__Host-hcb_consent_") {
			consent = c
		}
	}
	if consent == nil {
		t.Fatalf("no __Host- consent cookie; got %v", resp.Cookies())
	}
	if !consent.Secure || !consent.HttpOnly || consent.Path != "/" {
		t.Errorf("__Host- prefix requires Secure, Path=/: %+v", consent)
	}
	if consent.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax so the cookie rides HCB's redirect back", consent.SameSite)
	}

	// The jar drops Secure cookies on a plain-http test server, so replay it
	// explicitly — the point here is that both legs agree on the name.
	req, _ := http.NewRequest(http.MethodPost, bridge.URL+"/oauth/authorize",
		strings.NewReader(url.Values{"auth": {consentNonce(t, page)}, "approve": {"yes"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.AddCookie(consent)
	if got, _ := newBrowser(t).do(req); got.StatusCode != http.StatusFound {
		t.Errorf("approval over https = %d, want 302", got.StatusCode)
	}
}

// TestConsentPageIsLockedDown pins the headers the page depends on. The CORS
// deletion is a cross-file seam with the blanket cors middleware: change how
// that middleware wraps and the page becomes cross-origin readable, silently.
func TestConsentPageIsLockedDown(t *testing.T) {
	bridge, _, _ := bridgeWithPriorGrant(t)
	resp, _ := newBrowser(t).get(authorizeLink(bridge.URL, "https://claude.ai/cb"))

	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("consent page is cross-origin readable (ACAO = %q)", got)
	}
	for header, want := range map[string]string{
		"X-Frame-Options": "DENY",
		"Referrer-Policy": "no-referrer",
		"Cache-Control":   "no-store",
	} {
		if got := resp.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP missing frame-ancestors: %q", csp)
	}
}

// TestConcurrentLoginsDoNotClobber covers two flows in one browser — a
// terminal `hcb login` alongside a connector, or a double-clicked button.
// A single shared cookie name would let the second GET overwrite the first
// flow's binding and 403 it at approval, blaming the user's cookie settings.
func TestConcurrentLoginsDoNotClobber(t *testing.T) {
	bridge, _, _ := bridgeWithPriorGrant(t)

	user := newBrowser(t) // one jar, two flows
	_, first := user.get(authorizeLink(bridge.URL, "https://claude.ai/cb"))
	_, second := user.get(authorizeLink(bridge.URL, "http://localhost:8910/callback"))

	// Approve the older one last: it must still hold its own binding.
	if got := approve(t, user, bridge.URL, consentNonce(t, second), "same-origin").StatusCode; got != http.StatusFound {
		t.Errorf("second flow = %d, want 302", got)
	}
	if got := approve(t, user, bridge.URL, consentNonce(t, first), "same-origin").StatusCode; got != http.StatusFound {
		t.Errorf("first flow = %d after a second login started in the same browser, want 302", got)
	}
}
