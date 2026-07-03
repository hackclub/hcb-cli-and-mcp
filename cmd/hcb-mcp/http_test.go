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
	hc := &http.Client{Transport: bearerRoundTripper{token: token, base: http.DefaultTransport}}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	session, err := mcpClient.Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: base + "/mcp", HTTPClient: hc}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "hcb_get_profile", Arguments: map[string]any{},
	})
	if err != nil || res.IsError {
		t.Fatalf("tool call failed: %v %v", err, res)
	}
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
	if !hasScope(prm["scopes_supported"], "admin:read") || !hasScope(prm["scopes_supported"], "restricted") {
		t.Errorf("protected resource scopes = %v, want admin read-only scopes", prm["scopes_supported"])
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
	if !hasScope(asm["scopes_supported"], "admin:read") || !hasScope(asm["scopes_supported"], "restricted") {
		t.Errorf("authorization server scopes = %v, want admin read-only scopes", asm["scopes_supported"])
	}
}

func hasScope(raw any, want string) bool {
	scopes, ok := raw.([]any)
	if !ok {
		return false
	}
	for _, s := range scopes {
		if s == want {
			return true
		}
	}
	return false
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
	uris, _ := reg["redirect_uris"].([]any)
	if len(uris) != 1 || uris[0] != "https://claude.ai/api/mcp/auth_callback" {
		t.Errorf("redirect_uris = %v", reg["redirect_uris"])
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
