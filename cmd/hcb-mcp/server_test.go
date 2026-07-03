package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hackclub/hcb-mcp/internal/hcbapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// startSession wires the real tool registry to a fake HCB API and connects an
// in-memory MCP client, exercising the full MCP layer.
func startSession(t *testing.T, apiHandler http.Handler) *mcp.ClientSession {
	t.Helper()
	api := httptest.NewServer(apiHandler)
	t.Cleanup(api.Close)

	creds := &hcbapi.Credentials{
		BaseURL: api.URL, AccessToken: "hcb_test", RefreshToken: "ref",
		ClientID: "cid", ClientSecret: "csec",
	}
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := creds.Save(path); err != nil {
		t.Fatal(err)
	}
	var err error
	client, err = hcbapi.NewClient(path) // package-global used by tools
	if err != nil {
		t.Fatal(err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "hcb-test", Version: "0.0.1"}, nil)
	registerTools(server, client)

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	session, err := mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func fixtureHandler(t *testing.T, routes map[string]string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture, ok := routes[r.URL.Path]
		if !ok {
			t.Errorf("unexpected API path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile(filepath.Join("..", "..", "internal", "hcbapi", "testdata", fixture))
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	})
}

func TestAllToolsRegistered(t *testing.T) {
	session := startSession(t, fixtureHandler(t, map[string]string{}))
	var names []string
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	want := []string{
		"hcb_get_profile", "hcb_available_icons", "hcb_lookup_user", "hcb_token_info",
		"hcb_list_organizations", "hcb_get_organization", "hcb_org_balance_history",
		"hcb_org_followers", "hcb_list_sub_organizations",
		"hcb_list_transactions", "hcb_get_transaction", "hcb_memo_suggestions", "hcb_missing_receipts",
		"hcb_list_receipts", "hcb_download_receipt", "hcb_download_file",
		"hcb_list_comments", "hcb_list_tags", "hcb_get_tag",
		"hcb_list_cards", "hcb_get_card", "hcb_card_transactions", "hcb_card_designs",
		"hcb_list_card_grants", "hcb_get_card_grant", "hcb_card_grant_transactions",
		"hcb_list_invitations", "hcb_get_invitation",
		"hcb_list_checks", "hcb_get_check", "hcb_list_check_deposits", "hcb_get_check_deposit",
		"hcb_list_sponsors", "hcb_get_sponsor", "hcb_list_invoices", "hcb_get_invoice",
	}
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("tool %s not registered", w)
		}
	}
	if len(names) != len(want) {
		t.Errorf("registered %d tools, want %d: %v", len(names), len(want), names)
	}
}

func TestCallToolReturnsAPIJSON(t *testing.T) {
	session := startSession(t, fixtureHandler(t, map[string]string{
		"/api/v4/organizations/hq": "organization.json",
	}))
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "hcb_get_organization",
		Arguments: map[string]any{"organization": "hq", "expand": "balance_cents,users"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool errored: %v", res.Content)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	var org map[string]any
	if err := json.Unmarshal([]byte(text), &org); err != nil {
		t.Fatalf("tool result not JSON: %v", err)
	}
	if org["object"] != "organization" || org["slug"] != "hq" {
		t.Errorf("unexpected org payload: %v", org)
	}
}

func TestCallPagedTool(t *testing.T) {
	session := startSession(t, fixtureHandler(t, map[string]string{
		"/api/v4/organizations/hq/transactions": "transactions.json",
	}))
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "hcb_list_transactions",
		Arguments: map[string]any{"organization": "hq", "limit": 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	var env struct {
		TotalCount int             `json:"total_count"`
		HasMore    bool            `json:"has_more"`
		Data       json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatal(err)
	}
	if env.TotalCount == 0 || len(env.Data) == 0 {
		t.Errorf("empty envelope: %s", text)
	}
}

// API errors must surface as MCP tool errors (IsError=true), not protocol errors.
func TestCallToolAPIError(t *testing.T) {
	session := startSession(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"not_authorized"}`))
	}))
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "hcb_get_check",
		Arguments: map[string]any{"id": "chk_x"},
	})
	if err != nil {
		t.Fatalf("protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true for 403")
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "403") || !strings.Contains(text, "not_authorized") {
		t.Errorf("error text = %q", text)
	}
}

// The download tool must save the file and not send the bearer token.
func TestDownloadFileTool(t *testing.T) {
	var gotAuth string
	fileSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte("file-bytes"))
	}))
	defer fileSrv.Close()

	session := startSession(t, fixtureHandler(t, map[string]string{}))
	dir := t.TempDir()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "hcb_download_file",
		Arguments: map[string]any{"url": fileSrv.URL + "/blob/receipt.pdf", "directory": dir},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool errored: %v", res.Content)
	}
	path := res.Content[0].(*mcp.TextContent).Text
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "file-bytes" {
		t.Fatalf("saved file bad: %v %q", err, b)
	}
	if gotAuth != "" {
		t.Errorf("bearer token leaked to file host: %q", gotAuth)
	}
}
