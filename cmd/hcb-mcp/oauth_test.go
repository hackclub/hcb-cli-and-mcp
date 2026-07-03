package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// noRedirect returns a client that surfaces 302s instead of following them.
func noRedirect() *http.Client {
	return &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

// fakeHCB is an upstream that only accepts the one authorization code HCB
// "issued", so replayed sub-AS codes falling through to the proxy fail like
// they would in production.
func fakeHCB(t *testing.T, wantRedirectURI *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/oauth/token" {
			t.Errorf("upstream path = %s", r.URL.Path)
		}
		r.ParseForm()
		if r.PostForm.Get("client_secret") != "test-client-secret" {
			t.Error("client_secret not injected upstream")
		}
		if r.PostForm.Get("code") != "hcb-code-1" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		if wantRedirectURI != nil && r.PostForm.Get("redirect_uri") != *wantRedirectURI {
			t.Errorf("upstream redirect_uri = %q, want %q", r.PostForm.Get("redirect_uri"), *wantRedirectURI)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"hcb_user_tok","refresh_token":"r1","token_type":"Bearer","expires_in":7200,"scope":"read","created_at":1}`))
	}))
}

func pkce(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// get302 GETs u and returns the parsed Location of the expected redirect.
func get302(t *testing.T, u string) *url.URL {
	t.Helper()
	resp, err := noRedirect().Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 302: %s", resp.StatusCode, b)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestSubOAuthFullFlow(t *testing.T) {
	wantRedirect := "" // set once the bridge server URL is known
	hcb := fakeHCB(t, &wantRedirect)
	defer hcb.Close()
	srv := httptest.NewServer(httpHandler(testCfg(hcb.URL)))
	defer srv.Close()
	wantRedirect = srv.URL + "/oauth/callback"

	const verifier = "cli-verifier-abcdefghijklmnop"
	clientRedirect := "http://localhost:9999/cb"

	// Leg 1: client authorize → 302 to HCB with the server's own callback.
	loc := get302(t, srv.URL+"/oauth/authorize?"+url.Values{
		"client_id":             {"hcb-cli"},
		"redirect_uri":          {clientRedirect},
		"response_type":         {"code"},
		"state":                 {"client-state-1"},
		"scope":                 {"read"},
		"code_challenge":        {pkce(verifier)},
		"code_challenge_method": {"S256"},
	}.Encode())
	if !strings.HasPrefix(loc.String(), hcb.URL+"/api/v4/oauth/authorize") {
		t.Fatalf("authorize redirected to %s, want HCB", loc)
	}
	up := loc.Query()
	if up.Get("client_id") != "test-client-id" || up.Get("redirect_uri") != wantRedirect {
		t.Fatalf("upstream authorize params wrong: %v", up)
	}
	nonce := up.Get("state")
	if nonce == "" || nonce == "client-state-1" {
		t.Fatalf("upstream state must be a fresh nonce, got %q", nonce)
	}

	// Leg 2: HCB sends the browser back → code minted, client state restored.
	loc = get302(t, srv.URL+"/oauth/callback?"+url.Values{
		"code": {"hcb-code-1"}, "state": {nonce},
	}.Encode())
	if !strings.HasPrefix(loc.String(), clientRedirect) {
		t.Fatalf("callback redirected to %s, want client", loc)
	}
	cbq := loc.Query()
	if cbq.Get("state") != "client-state-1" {
		t.Errorf("client state = %q", cbq.Get("state"))
	}
	code := cbq.Get("code")
	if code == "" || code == "hcb-code-1" {
		t.Fatalf("minted code missing or leaked upstream code: %q", code)
	}

	// Leg 3: redeem the minted code with the PKCE verifier.
	resp, err := http.Post(srv.URL+"/oauth/token", "application/x-www-form-urlencoded",
		strings.NewReader(url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {code},
			"code_verifier": {verifier},
			"redirect_uri":  {clientRedirect},
			"client_id":     {"hcb-cli"},
		}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var tok map[string]any
	json.NewDecoder(resp.Body).Decode(&tok)
	if resp.StatusCode != 200 || tok["access_token"] != "hcb_user_tok" {
		t.Fatalf("redeem = %d %v", resp.StatusCode, tok)
	}

	// Replay: the code is single-use; the fall-through proxy hits upstream,
	// which rejects the unknown code.
	resp2, err := http.Post(srv.URL+"/oauth/token", "application/x-www-form-urlencoded",
		strings.NewReader(url.Values{
			"grant_type": {"authorization_code"}, "code": {code},
			"code_verifier": {verifier}, "redirect_uri": {clientRedirect},
		}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode == 200 {
		t.Error("replayed code must not succeed")
	}
}

func TestSubOAuthWrongVerifierRejected(t *testing.T) {
	hcb := fakeHCB(t, nil)
	defer hcb.Close()
	srv := httptest.NewServer(httpHandler(testCfg(hcb.URL)))
	defer srv.Close()

	loc := get302(t, srv.URL+"/oauth/authorize?"+url.Values{
		"client_id": {"x"}, "redirect_uri": {"https://claude.ai/api/mcp/auth_callback"},
		"response_type": {"code"}, "code_challenge": {pkce("right-verifier")},
		"code_challenge_method": {"S256"},
	}.Encode())
	nonce := loc.Query().Get("state")
	loc = get302(t, srv.URL+"/oauth/callback?"+url.Values{"code": {"hcb-code-1"}, "state": {nonce}}.Encode())
	code := loc.Query().Get("code")

	resp, err := http.Post(srv.URL+"/oauth/token", "application/x-www-form-urlencoded",
		strings.NewReader(url.Values{
			"grant_type": {"authorization_code"}, "code": {code},
			"code_verifier": {"wrong-verifier"},
			"redirect_uri":  {"https://claude.ai/api/mcp/auth_callback"},
		}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 400 || !strings.Contains(string(b), "invalid_grant") {
		t.Errorf("wrong verifier = %d %s, want 400 invalid_grant", resp.StatusCode, b)
	}
}

func TestSubOAuthRejectsUnsafeRedirects(t *testing.T) {
	srv := httptest.NewServer(httpHandler(testCfg("https://hcb.example")))
	defer srv.Close()

	for _, uri := range []string{
		"http://evil.example/steal", // plain http on a non-loopback host
		"javascript:alert(1)",       // nonsense scheme
		"",                          // missing
		"custom-app://cb",           // custom schemes can't be verified
	} {
		resp, err := noRedirect().Get(srv.URL + "/oauth/authorize?" + url.Values{
			"client_id": {"x"}, "redirect_uri": {uri}, "response_type": {"code"},
		}.Encode())
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Errorf("redirect_uri %q accepted with %d, want 400", uri, resp.StatusCode)
		}
	}
}

func TestSubOAuthUpstreamDenialPassedThrough(t *testing.T) {
	srv := httptest.NewServer(httpHandler(testCfg("https://hcb.example")))
	defer srv.Close()

	loc := get302(t, srv.URL+"/oauth/authorize?"+url.Values{
		"client_id": {"x"}, "redirect_uri": {"https://claude.ai/cb"},
		"response_type": {"code"}, "state": {"s1"},
	}.Encode())
	nonce := loc.Query().Get("state")

	loc = get302(t, srv.URL+"/oauth/callback?"+url.Values{
		"error": {"access_denied"}, "state": {nonce},
	}.Encode())
	q := loc.Query()
	if !strings.HasPrefix(loc.String(), "https://claude.ai/cb") ||
		q.Get("error") != "access_denied" || q.Get("state") != "s1" {
		t.Errorf("denial redirect = %s", loc)
	}
}

func TestSubOAuthUnknownCallbackState(t *testing.T) {
	srv := httptest.NewServer(httpHandler(testCfg("https://hcb.example")))
	defer srv.Close()
	resp, err := noRedirect().Get(srv.URL + "/oauth/callback?code=x&state=never-issued")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("unknown state = %d, want 400", resp.StatusCode)
	}
}
