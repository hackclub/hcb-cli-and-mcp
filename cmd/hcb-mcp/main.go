// Command hcb-mcp is a read-only MCP server for the HCB v4 API.
// Tools return the API's raw JSON as text content.
//
// Modes:
//
//	hcb-mcp             stdio transport (default; for local clients like Claude Code)
//	hcb-mcp --http :8080  streamable HTTP transport at /mcp, guarded by $MCP_AUTH_TOKEN
//
// Environment:
//
//	HCB_CREDENTIALS       path to credentials.json (default ~/.config/hcb/credentials.json)
//	HCB_CREDENTIALS_JSON  seeds the credentials file if it doesn't exist (container bootstrap)
//	MCP_AUTH_TOKEN        shared secret for --http mode (Bearer header or ?key=)
//	PORT                  listen port for --http mode when no address is given
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/hackclub/hcb-mcp/internal/hcbapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var client *hcbapi.Client

func main() {
	httpAddr := flag.String("http", "", `serve MCP over HTTP on this address (e.g. ":8080"); empty = stdio`)
	flag.Parse()

	credsPath, err := resolveCredentialsPath()
	if err != nil {
		log.Fatal(err)
	}
	client, err = hcbapi.NewClient(credsPath)
	if err != nil && *httpAddr == "" && os.Getenv("PORT") == "" {
		// stdio mode cannot work without server-owned credentials
		log.Fatalf("loading credentials: %v (run `hcb login` first, or set HCB_CREDENTIALS_JSON)", err)
	}

	if *httpAddr == "" && os.Getenv("PORT") != "" {
		*httpAddr = ":" + os.Getenv("PORT")
	}
	if *httpAddr != "" {
		if !strings.Contains(*httpAddr, ":") {
			*httpAddr = ":" + *httpAddr
		}
		log.Fatal(serveHTTP(*httpAddr))
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "hcb",
		Title:   "HCB (read-only)",
		Version: "0.1.0",
	}, nil)
	registerTools(server, client)
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}

// jsonResult wraps raw API JSON as an MCP text result.
func jsonResult(raw json.RawMessage) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}
}

func pageResult(p *hcbapi.Page) (*mcp.CallToolResult, error) {
	env, err := json.Marshal(map[string]any{
		"total_count": p.TotalCount,
		"has_more":    p.HasMore,
		"data":        json.RawMessage(p.Data),
	})
	if err != nil {
		return nil, err
	}
	return jsonResult(env), nil
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func errResult(err error) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("HCB API error: %v", err)}},
	}, nil, nil
}

// jsonUnmarshalStrict decodes s into v, rejecting trailing garbage.
func jsonUnmarshalStrict(s string, v any) error {
	dec := json.NewDecoder(strings.NewReader(s))
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}
