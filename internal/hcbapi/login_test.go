package hcbapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Component test for the authorization-code login flow: a fake HCB issues the
// token; the "browser" is simulated by hitting the callback URL with a code.
func TestLogin(t *testing.T) {
	var tokenForm url.Values
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/oauth/token" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		r.ParseForm()
		tokenForm = r.Form
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"hcb_new","refresh_token":"ref_new","scope":"read","created_at":1751500000,"expires_in":7200}`)
	}))
	defer fake.Close()

	browserURL := make(chan string, 1)
	cfg := LoginConfig{
		BaseURL:      fake.URL,
		ClientID:     "cid",
		ClientSecret: "csec",
		Scope:        "read",
		ListenAddr:   "127.0.0.1:0", // ephemeral port for the test
		OpenBrowser: func(u string) error {
			browserURL <- u
			return nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	type result struct {
		creds *Credentials
		err   error
	}
	done := make(chan result, 1)
	go func() {
		creds, err := Login(ctx, cfg, io.Discard)
		done <- result{creds, err}
	}()

	// The URL handed to the browser must be a proper authorize URL.
	authURL := <-browserURL
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("bad auth URL: %v", err)
	}
	if u.Path != "/api/v4/oauth/authorize" {
		t.Errorf("auth path = %s", u.Path)
	}
	q := u.Query()
	if q.Get("client_id") != "cid" || q.Get("response_type") != "code" || q.Get("scope") != "read" {
		t.Errorf("auth query = %v", q)
	}
	redirect := q.Get("redirect_uri")
	if !strings.HasPrefix(redirect, "http://") || !strings.HasSuffix(redirect, "/callback") {
		t.Errorf("redirect_uri = %q", redirect)
	}
	state := q.Get("state")
	if state == "" {
		t.Error("missing state param")
	}

	// Simulate the user approving: browser hits the callback with the code + state.
	resp, err := http.Get(redirect + "?code=authcode123&state=" + url.QueryEscape(state))
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	resp.Body.Close()

	res := <-done
	if res.err != nil {
		t.Fatalf("Login: %v", res.err)
	}
	if res.creds.AccessToken != "hcb_new" || res.creds.RefreshToken != "ref_new" {
		t.Errorf("creds = %+v", res.creds)
	}
	if res.creds.BaseURL != fake.URL || res.creds.ClientID != "cid" {
		t.Errorf("creds config = %+v", res.creds)
	}
	if tokenForm.Get("grant_type") != "authorization_code" || tokenForm.Get("code") != "authcode123" ||
		tokenForm.Get("redirect_uri") != redirect {
		t.Errorf("token exchange form = %v", tokenForm)
	}
}

// A mismatched state must be rejected (CSRF protection).
func TestLoginRejectsBadState(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("token endpoint must not be called on bad state")
	}))
	defer fake.Close()

	browserURL := make(chan string, 1)
	cfg := LoginConfig{
		BaseURL: fake.URL, ClientID: "cid", ClientSecret: "csec",
		ListenAddr:  "127.0.0.1:0",
		OpenBrowser: func(u string) error { browserURL <- u; return nil },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := Login(ctx, cfg, io.Discard)
		done <- err
	}()

	u, _ := url.Parse(<-browserURL)
	redirect := u.Query().Get("redirect_uri")
	resp, err := http.Get(redirect + "?code=evil&state=WRONG")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if err := <-done; err == nil {
		t.Fatal("Login accepted a forged state")
	}
}
