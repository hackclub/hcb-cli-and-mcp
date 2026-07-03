package hcbapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
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
	// AuthServer, when set, brokers the flow through the hosted bridge's
	// sub-authorization-server (<AuthServer>/oauth/authorize + /oauth/token)
	// instead of talking to HCB directly. The bridge holds the client secret
	// and accepts any localhost callback port, so no local client config is
	// needed; token refreshes route through the bridge too (via
	// Credentials.TokenURL). PKCE (S256) binds the code to this process.
	AuthServer string
	// ListenAddr for the local callback server. Direct flow default is
	// 127.0.0.1:8910 (the port registered on the OAuth app); the AuthServer
	// flow defaults to an ephemeral port since any port is accepted.
	ListenAddr string
	// OpenBrowser opens the authorize URL; nil uses the OS default browser.
	OpenBrowser func(url string) error
}

// Login runs the OAuth authorization-code flow: starts a localhost callback
// listener, sends the user to the authorize page, exchanges the returned code,
// and returns credentials ready to Save. Progress is written to out.
func Login(ctx context.Context, cfg LoginConfig, out io.Writer) (*Credentials, error) {
	cfg.AuthServer = strings.TrimSuffix(cfg.AuthServer, "/")
	authorizeEndpoint := cfg.BaseURL + "/api/v4/oauth/authorize"
	tokenEndpoint := cfg.BaseURL + "/api/v4/oauth/token"
	if cfg.AuthServer != "" {
		authorizeEndpoint = cfg.AuthServer + "/oauth/authorize"
		tokenEndpoint = cfg.AuthServer + "/oauth/token"
	}
	if cfg.ListenAddr == "" {
		if cfg.AuthServer != "" {
			cfg.ListenAddr = "127.0.0.1:0"
		} else {
			cfg.ListenAddr = "127.0.0.1:8910"
		}
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

	authQuery := url.Values{
		"client_id":     {cfg.ClientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"scope":         {cfg.Scope},
		"state":         {state},
	}
	var pkceVerifier string
	if cfg.AuthServer != "" {
		verifierBytes := make([]byte, 32)
		if _, err := rand.Read(verifierBytes); err != nil {
			return nil, err
		}
		pkceVerifier = base64.RawURLEncoding.EncodeToString(verifierBytes)
		sum := sha256.Sum256([]byte(pkceVerifier))
		authQuery.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
		authQuery.Set("code_challenge_method", "S256")
	}
	authURL := authorizeEndpoint + "?" + authQuery.Encode()

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
		"grant_type":   {"authorization_code"},
		"code":         {cb.code},
		"client_id":    {cfg.ClientID},
		"redirect_uri": {redirectURI},
	}
	if pkceVerifier != "" {
		form.Set("code_verifier", pkceVerifier)
	}
	if cfg.ClientSecret != "" {
		form.Set("client_secret", cfg.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		tokenEndpoint, strings.NewReader(form.Encode()))
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

	creds := &Credentials{
		BaseURL:      cfg.BaseURL,
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		Scope:        tok.Scope,
		CreatedAt:    tok.CreatedAt,
		ExpiresIn:    tok.ExpiresIn,
	}
	if cfg.AuthServer != "" {
		creds.TokenURL = cfg.AuthServer + "/oauth/token"
	}
	return creds, nil
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
