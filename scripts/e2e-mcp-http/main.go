// Live check for a hosted hcb-mcp instance over streamable HTTP.
//
// Usage:
//
//	MCP_URL=https://host/mcp MCP_AUTH_TOKEN=... go run ./scripts/e2e-mcp-http
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearer struct {
	token string
	base  http.RoundTripper
}

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(req)
}

func main() {
	url := os.Getenv("MCP_URL")
	token := os.Getenv("MCP_AUTH_TOKEN")
	if url == "" || token == "" {
		fmt.Println("set MCP_URL and MCP_AUTH_TOKEN")
		os.Exit(1)
	}
	org := os.Getenv("HCB_E2E_ORG")
	if org == "" {
		org = "hq"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	hc := &http.Client{Transport: bearer{token: token, base: http.DefaultTransport}}
	client := mcp.NewClient(&mcp.Implementation{Name: "live-check", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: hc}, nil)
	if err != nil {
		fmt.Println("FATAL: connect:", err)
		os.Exit(1)
	}
	defer session.Close()

	count := 0
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			fmt.Println("FATAL: list tools:", err)
			os.Exit(1)
		}
		_ = tool
		count++
	}
	fmt.Printf("PASS  tools/list: %d tools\n", count)

	pass, fail := 1, 0
	checks := []struct {
		tool     string
		args     map[string]any
		contains string
	}{
		{"hcb_token_info", map[string]any{}, "scope"},
		{"hcb_get_organization", map[string]any{"organization": org, "expand": "balance_cents"}, `"organization"`},
		{"hcb_list_transactions", map[string]any{"organization": org, "limit": 3}, `"total_count"`},
		{"hcb_get_profile", map[string]any{}, `"object":"user"`},
	}
	for _, c := range checks {
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: c.tool, Arguments: c.args})
		if err != nil || res.IsError {
			fmt.Printf("FAIL  %s: err=%v isError=%v\n", c.tool, err, res != nil && res.IsError)
			fail++
			continue
		}
		text := res.Content[0].(*mcp.TextContent).Text
		if !strings.Contains(text, c.contains) {
			fmt.Printf("FAIL  %s: missing %q\n", c.tool, c.contains)
			fail++
			continue
		}
		var v any
		if json.Unmarshal([]byte(text), &v) != nil {
			fmt.Printf("FAIL  %s: not JSON\n", c.tool)
			fail++
			continue
		}
		fmt.Printf("PASS  %s\n", c.tool)
		pass++
	}
	fmt.Printf("\nRESULT: %d passed, %d failed\n", pass, fail)
	if fail > 0 {
		os.Exit(1)
	}
}
