package hcbapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// LoginConfig configures the authorization-code login flow.
// (HCB's device-flow endpoint 500s in production, so a localhost callback —
// the gh-style flow — is the supported way to log in.)
type LoginConfig struct {
	BaseURL      string
	ClientID     string
	ClientSecret string
	Scope        string
	// ListenAddr for the local callback server. Default 127.0.0.1:8910 — the
	// port must match the OAuth app's registered redirect URI.
	ListenAddr string
	// OpenBrowser opens the authorize URL; nil uses the OS default browser.
	OpenBrowser func(url string) error
}

// Login runs the OAuth authorization-code flow: starts a localhost callback
// listener, sends the user to the authorize page, exchanges the returned code,
// and returns credentials ready to Save. Progress is written to out.
func Login(ctx context.Context, cfg LoginConfig, out io.Writer) (*Credentials, error) {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:8910"
	}
	if cfg.OpenBrowser == nil {
		cfg.OpenBrowser = openBrowser
	}

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("starting callback listener on %s: %w", cfg.ListenAddr, err)
	}
	defer ln.Close()

	// Redirect URI must use "localhost" (that's what the OAuth app registers).
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	redirectURI := fmt.Sprintf("http://localhost:%s/callback", port)

	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return nil, err
	}
	state := hex.EncodeToString(stateBytes)

	authURL := cfg.BaseURL + "/api/v4/oauth/authorize?" + url.Values{
		"client_id":     {cfg.ClientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"scope":         {cfg.Scope},
		"state":         {state},
	}.Encode()

	type callback struct {
		code string
		err  error
	}
	got := make(chan callback, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		switch {
		case q.Get("state") != state:
			http.Error(w, "state mismatch — possible CSRF; run hcb login again", http.StatusBadRequest)
			got <- callback{err: errors.New("authorization state mismatch")}
		case q.Get("error") != "":
			http.Error(w, "authorization denied", http.StatusBadRequest)
			got <- callback{err: fmt.Errorf("authorization denied: %s", q.Get("error"))}
		case q.Get("code") == "":
			http.Error(w, "missing code", http.StatusBadRequest)
			got <- callback{err: errors.New("callback missing code")}
		default:
			fmt.Fprint(w, "<h1>Authorized — you can close this tab.</h1>")
			got <- callback{code: q.Get("code")}
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()

	fmt.Fprintf(out, "Opening browser to authorize:\n\n  %s\n\nWaiting for authorization…\n", authURL)
	if err := cfg.OpenBrowser(authURL); err != nil {
		fmt.Fprintf(out, "(couldn't open browser automatically: %v — open the URL above manually)\n", err)
	}

	var cb callback
	select {
	case cb = <-got:
	case <-ctx.Done():
		return nil, fmt.Errorf("timed out waiting for authorization: %w", ctx.Err())
	}
	if cb.err != nil {
		return nil, cb.err
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {cb.code},
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"redirect_uri":  {redirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cfg.BaseURL+"/api/v4/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("exchanging code: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed: HTTP %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Scope        string `json:"scope"`
		CreatedAt    int64  `json:"created_at"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("parsing token response: %w", err)
	}

	return &Credentials{
		BaseURL:      cfg.BaseURL,
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		Scope:        tok.Scope,
		CreatedAt:    tok.CreatedAt,
		ExpiresIn:    tok.ExpiresIn,
	}, nil
}

func openBrowser(u string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	default:
		return exec.Command("xdg-open", u).Start()
	}
}
