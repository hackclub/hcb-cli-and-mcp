package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hackclub/hcb-mcp/internal/hcbapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAuthMiddleware(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	h := authMiddleware("sekrit", inner)
	srv := httptest.NewServer(h)
	defer srv.Close()

	cases := []struct {
		name   string
		header string
		query  string
		want   int
	}{
		{"no auth", "", "", 401},
		{"wrong bearer", "Bearer nope", "", 401},
		{"right bearer", "Bearer sekrit", "", 200},
		{"right query key", "", "?key=sekrit", 200},
		{"wrong query key", "", "?key=nope", 401},
	}
	for _, c := range cases {
		req, _ := http.NewRequest("GET", srv.URL+"/mcp"+c.query, nil)
		if c.header != "" {
			req.Header.Set("Authorization", c.header)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s: status = %d, want %d", c.name, resp.StatusCode, c.want)
		}
	}
}

func TestHealthzNoAuth(t *testing.T) {
	srv := httptest.NewServer(httpHandler("sekrit"))
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

// Full round-trip: MCP session over authenticated streamable HTTP.
func TestMCPOverHTTP(t *testing.T) {
	// fake HCB API + client
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"usr_1","object":"user","name":"Example"}`))
	}))
	defer api.Close()
	creds := &hcbapi.Credentials{BaseURL: api.URL, AccessToken: "hcb_test", ClientID: "c", ClientSecret: "s"}
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := creds.Save(path); err != nil {
		t.Fatal(err)
	}
	var err error
	client, err = hcbapi.NewClient(path)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(httpHandler("sekrit"))
	defer srv.Close()

	// custom transport injecting the bearer token
	hc := &http.Client{Transport: bearerRoundTripper{token: "sekrit", base: http.DefaultTransport}}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	session, err := mcpClient.Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp", HTTPClient: hc}, nil)
	if err != nil {
		t.Fatalf("connect over HTTP: %v", err)
	}
	defer session.Close()

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "hcb_get_profile", Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool errored: %v", res.Content)
	}
	var user map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &user); err != nil || user["object"] != "user" {
		t.Errorf("bad result: %v %v", user, err)
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
