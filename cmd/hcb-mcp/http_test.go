package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hackclub/hcb-cli-and-mcp/internal/hcbapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func testCfg(hcbURL string) httpConfig {
	return httpConfig{
		staticToken:       "sekrit",
		hcbBaseURL:        hcbURL,
		oauthClientID:     "test-client-id",
		oauthClientSecret: "test-client-secret",
	}
}

// setServerClient points the package-global client (used for static-token
// requests) at a fake HCB API.
func setServerClient(t *testing.T, apiURL string) {
	t.Helper()
	creds := &hcbapi.Credentials{BaseURL: apiURL, AccessToken: "hcb_server", ClientID: "c", ClientSecret: "s"}
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := creds.Save(path); err != nil {
		t.Fatal(err)
	}
	var err error
	client, err = hcbapi.NewClient(path)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRequireTokenAndAuthModes(t *testing.T) {
	// fake HCB records which upstream token each request used
	var lastUpstreamAuth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastUpstreamAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"id":"usr_1","object":"user"}`))
	}))
	defer api.Close()
	setServerClient(t, api.URL)

	srv := httptest.NewServer(httpHandler(testCfg(api.URL)))
	defer srv.Close()

	// no token -> 401 with discovery pointer
	resp, err := http.Post(srv.URL+"/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("no-token status = %d, want 401", resp.StatusCode)
	}
	if wa := resp.Header.Get("WWW-Authenticate"); !strings.Contains(wa, "oauth-protected-resource") {
		t.Errorf("WWW-Authenticate = %q, want resource_metadata pointer", wa)
	}

	// static token -> uses server-owned creds upstream
	callProfile(t, srv.URL, "sekrit")
	if lastUpstreamAuth != "Bearer hcb_server" {
		t.Errorf("static-token upstream auth = %q, want server token", lastUpstreamAuth)
	}

	// arbitrary bearer -> forwarded upstream as the user's own HCB token
	callProfile(t, srv.URL, "hcb_users_own_token")
	if lastUpstreamAuth != "Bearer hcb_users_own_token" {
		t.Errorf("per-user upstream auth = %q, want caller token", lastUpstreamAuth)
	}
}

// callProfile runs a full MCP session over HTTP with the given bearer token.
func callProfile(t *testing.T, base, token string) {
	t.Helper()
	res := callHTTPTool(t, base, token, "hcb_get_profile", map[string]any{})
	if res.IsError {
		t.Fatalf("tool call failed: %v", res)
	}
}

func callHTTPTool(t *testing.T, base, token, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	hc := &http.Client{Transport: bearerRoundTripper{token: token, base: http.DefaultTransport}}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	session, err := mcpClient.Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: base + "/mcp", HTTPClient: hc}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: name, Arguments: args,
	})
	if err != nil {
		t.Fatalf("tool call failed: %v", err)
	}
	return res
}

func TestHealthzNoAuth(t *testing.T) {
	srv := httptest.NewServer(httpHandler(testCfg("https://hcb.example")))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("healthz = %d, want 200 without auth", resp.StatusCode)
	}
}

func TestHTTPModeDisablesFileDownloads(t *testing.T) {
	fileSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("file-bytes"))
	}))
	defer fileSrv.Close()

	srv := httptest.NewServer(httpHandler(testCfg("https://hcb.example")))
	defer srv.Close()

	res := callHTTPTool(t, srv.URL, "hcb_users_own_token", "hcb_download_file", map[string]any{
		"url":       fileSrv.URL + "/blob/receipt.pdf",
		"directory": t.TempDir(),
	})
	if !res.IsError {
		t.Fatal("HTTP-mode download unexpectedly succeeded")
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "disabled in HTTP mode") {
		t.Fatalf("download error = %q", text)
	}
}

func TestWellKnownDocuments(t *testing.T) {
	srv := httptest.NewServer(httpHandler(testCfg("https://hcb.example")))
	defer srv.Close()

	var prm map[string]any
	getJSON(t, srv.URL+"/.well-known/oauth-protected-resource", &prm)
	if prm["resource"] != srv.URL+"/mcp" {
		t.Errorf("resource = %v", prm["resource"])
	}
	as, _ := prm["authorization_servers"].([]any)
	if len(as) != 1 || as[0] != srv.URL {
		t.Errorf("authorization_servers = %v", prm["authorization_servers"])
	}
	if !jsonListContains(prm["scopes_supported"], "admin:read") {
		t.Errorf("protected resource scopes = %v, want admin:read", prm["scopes_supported"])
	}

	var asm map[string]any
	getJSON(t, srv.URL+"/.well-known/oauth-authorization-server", &asm)
	if asm["authorization_endpoint"] != srv.URL+"/oauth/authorize" {
		t.Errorf("authorization_endpoint = %v", asm["authorization_endpoint"])
	}
	if asm["token_endpoint"] != srv.URL+"/oauth/token" {
		t.Errorf("token_endpoint = %v", asm["token_endpoint"])
	}
	if asm["registration_endpoint"] != srv.URL+"/oauth/register" {
		t.Errorf("registration_endpoint = %v", asm["registration_endpoint"])
	}
	methods, _ := asm["code_challenge_methods_supported"].([]any)
	if len(methods) != 1 || methods[0] != "S256" {
		t.Errorf("code_challenge_methods = %v", asm["code_challenge_methods_supported"])
	}
	if !jsonListContains(asm["scopes_supported"], "admin:read") {
		t.Errorf("authorization server scopes = %v, want admin:read", asm["scopes_supported"])
	}
}

func TestDynamicRegistrationStub(t *testing.T) {
	srv := httptest.NewServer(httpHandler(testCfg("https://hcb.example")))
	defer srv.Close()

	body := `{"client_name":"Claude","redirect_uris":["https://claude.ai/api/mcp/auth_callback"]}`
	resp, err := http.Post(srv.URL+"/oauth/register", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("register status = %d", resp.StatusCode)
	}
	var reg map[string]any
	json.NewDecoder(resp.Body).Decode(&reg)
	if reg["client_id"] != "test-client-id" {
		t.Errorf("client_id = %v", reg["client_id"])
	}
	if _, hasSecret := reg["client_secret"]; hasSecret {
		t.Error("registration must NOT disclose the client secret")
	}
	if reg["scope"] != "read" {
		t.Errorf("default scope = %v, want read", reg["scope"])
	}
	uris, _ := reg["redirect_uris"].([]any)
	if len(uris) != 1 || uris[0] != "https://claude.ai/api/mcp/auth_callback" {
		t.Errorf("redirect_uris = %v", reg["redirect_uris"])
	}
}

func TestDynamicRegistrationRejectsWriteScopes(t *testing.T) {
	srv := httptest.NewServer(httpHandler(testCfg("https://hcb.example")))
	defer srv.Close()

	body := `{"client_name":"Claude","redirect_uris":["https://claude.ai/api/mcp/auth_callback"],"scope":"read admin:write"}`
	resp, err := http.Post(srv.URL+"/oauth/register", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("register status = %d, want 400", resp.StatusCode)
	}
}

func TestTokenProxyInjectsSecret(t *testing.T) {
	var upstreamForm url.Values
	hcb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/oauth/token" {
			t.Errorf("upstream path = %s", r.URL.Path)
		}
		r.ParseForm()
		upstreamForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"hcb_new","refresh_token":"r2","token_type":"Bearer","expires_in":7200}`))
	}))
	defer hcb.Close()

	srv := httptest.NewServer(httpHandler(testCfg(hcb.URL)))
	defer srv.Close()

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {"abc"},
		"code_verifier": {"ver123"},
		"client_id":     {"test-client-id"},
		"redirect_uri":  {"https://claude.ai/api/mcp/auth_callback"},
	}
	resp, err := http.Post(srv.URL+"/oauth/token", "application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(b), "hcb_new") {
		t.Fatalf("proxy response %d: %s", resp.StatusCode, b)
	}
	if upstreamForm.Get("client_secret") != "test-client-secret" {
		t.Error("client_secret not injected upstream")
	}
	if upstreamForm.Get("code_verifier") != "ver123" || upstreamForm.Get("code") != "abc" {
		t.Errorf("form not passed through: %v", upstreamForm)
	}
}

func TestTokenProxyRejectsWriteScopes(t *testing.T) {
	called := false
	hcb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer hcb.Close()
	srv := httptest.NewServer(httpHandler(testCfg(hcb.URL)))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/oauth/token", "application/x-www-form-urlencoded",
		strings.NewReader("grant_type=refresh_token&refresh_token=r1&scope=read+admin%3Awrite"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("proxy status = %d, want 400", resp.StatusCode)
	}
	if called {
		t.Fatal("disallowed scope was forwarded upstream")
	}
}

func TestTokenProxyForwardsErrors(t *testing.T) {
	hcb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer hcb.Close()
	srv := httptest.NewServer(httpHandler(testCfg(hcb.URL)))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/oauth/token", "application/x-www-form-urlencoded",
		strings.NewReader("grant_type=authorization_code&code=bad"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 400 || !strings.Contains(string(b), "invalid_grant") {
		t.Errorf("error not forwarded: %d %s", resp.StatusCode, b)
	}
}

func TestCORSPreflightOnOAuthEndpoints(t *testing.T) {
	srv := httptest.NewServer(httpHandler(testCfg("https://hcb.example")))
	defer srv.Close()
	for _, path := range []string{"/.well-known/oauth-authorization-server", "/oauth/token", "/oauth/register", "/mcp"} {
		req, _ := http.NewRequest(http.MethodOptions, srv.URL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 204 {
			t.Errorf("OPTIONS %s = %d, want 204", path, resp.StatusCode)
		}
		if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("%s missing CORS header", path)
		}
	}
}

func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s = %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func jsonListContains(v any, want string) bool {
	list, _ := v.([]any)
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (b bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(req)
}

func TestBootstrapCredentialsFromEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	t.Setenv("HCB_CREDENTIALS", path)
	t.Setenv("HCB_CREDENTIALS_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("HCB_CREDENTIALS_JSON", `{"base_url":"https://x","client_id":"c","client_secret":"s","access_token":"a","refresh_token":"r"}`)

	got, err := resolveCredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Errorf("path = %q, want %q", got, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("bootstrap did not write file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "access_token") || strings.Contains(string(raw), "refresh_token") {
		t.Fatalf("server bootstrap credentials were not encrypted: %s", raw)
	}

	// existing file must NOT be overwritten (it may hold rotated tokens)
	newer := &hcbapi.Credentials{BaseURL: "https://x", AccessToken: "rotated", RefreshToken: "r2", ClientID: "c", ClientSecret: "s"}
	if err := newer.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveCredentialsPath(); err != nil {
		t.Fatal(err)
	}
	after, err := hcbapi.LoadCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.AccessToken != "rotated" {
		t.Error("bootstrap overwrote an existing credentials file")
	}
}

func TestBootstrapCredentialsFromEncryptedEnv(t *testing.T) {
	key := "0123456789abcdef0123456789abcdef"
	t.Setenv("HCB_CREDENTIALS_KEY", key)

	source := filepath.Join(t.TempDir(), "source.json")
	creds := &hcbapi.Credentials{BaseURL: "https://x", AccessToken: "a", RefreshToken: "r"}
	if err := creds.Save(source); err != nil {
		t.Fatal(err)
	}
	encryptedSeed, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if !hcbapi.CredentialsJSONEncrypted(encryptedSeed) {
		t.Fatal("test seed was not encrypted")
	}

	path := filepath.Join(t.TempDir(), "credentials.json")
	t.Setenv("HCB_CREDENTIALS", path)
	t.Setenv("HCB_CREDENTIALS_JSON", string(encryptedSeed))
	t.Setenv("MCP_AUTH_TOKEN", "sekrit")

	got, err := resolveCredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Errorf("path = %q, want %q", got, path)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !hcbapi.CredentialsJSONEncrypted(written) {
		t.Fatal("encrypted seed was not written as encrypted credentials")
	}
	loaded, err := hcbapi.LoadCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AccessToken != "a" || loaded.RefreshToken != "r" {
		t.Fatalf("loaded credentials = %+v", loaded)
	}
}

func TestBootstrapServerCredentialsRefusesPlaintextSeed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	t.Setenv("HCB_CREDENTIALS", path)
	t.Setenv("HCB_CREDENTIALS_JSON", `{"base_url":"https://x","access_token":"a","refresh_token":"r"}`)
	t.Setenv("MCP_AUTH_TOKEN", "sekrit")
	t.Setenv("HCB_CREDENTIALS_KEY", "")

	if _, err := resolveCredentialsPath(); err == nil {
		t.Fatal("server credential seed without HCB_CREDENTIALS_KEY succeeded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("plaintext credentials file was created: %v", err)
	}

	t.Setenv("HCB_CREDENTIALS_KEY", "0123456789abcdef0123456789abcdef")
	if _, err := resolveCredentialsPath(); err == nil {
		t.Fatal("plaintext server credential seed with HCB_CREDENTIALS_KEY succeeded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("plaintext credentials file was created after key was set: %v", err)
	}
}

func TestServerOwnedCredentialsRequireEncryptionKey(t *testing.T) {
	previous := client
	t.Cleanup(func() { client = previous })

	cfg := testCfg("https://hcb.example")
	path := filepath.Join(t.TempDir(), "credentials.json")
	t.Setenv("HCB_CREDENTIALS_KEY", "")
	plaintext := &hcbapi.Credentials{BaseURL: "https://hcb.example", AccessToken: "hcb_server"}
	if err := plaintext.Save(path); err != nil {
		t.Fatal(err)
	}
	var err error
	client, err = hcbapi.NewClient(path)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("HCB_CREDENTIALS_KEY", "")
	if err := validateHTTPServerCredentials(cfg); err == nil {
		t.Fatal("static-token server accepted unencrypted server-owned credentials")
	}

	t.Setenv("HCB_CREDENTIALS_KEY", "0123456789abcdef0123456789abcdef")
	if err := validateHTTPServerCredentials(cfg); err == nil {
		t.Fatal("static-token server accepted plaintext credentials with an encryption key configured")
	}

	encrypted := &hcbapi.Credentials{BaseURL: "https://hcb.example", AccessToken: "hcb_server"}
	if err := encrypted.Save(path); err != nil {
		t.Fatal(err)
	}
	client, err = hcbapi.NewClient(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateHTTPServerCredentials(cfg); err != nil {
		t.Fatalf("validateHTTPServerCredentials with encrypted file and key: %v", err)
	}

	cfg.staticToken = ""
	t.Setenv("HCB_CREDENTIALS_KEY", "")
	if err := validateHTTPServerCredentials(cfg); err != nil {
		t.Fatalf("oauth-only server should not require server credential encryption: %v", err)
	}
}
