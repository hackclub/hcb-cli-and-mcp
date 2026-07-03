package main

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/hackclub/hcb-mcp/internal/hcbapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// authMiddleware guards the MCP endpoint with a shared secret, accepted as
// either `Authorization: Bearer <token>` or `?key=<token>` (for clients that
// can't set custom headers).
func authMiddleware(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if presented == r.Header.Get("Authorization") { // no Bearer prefix
			presented = ""
		}
		if presented == "" {
			presented = r.URL.Query().Get("key")
		}
		if subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// httpHandler builds the full HTTP surface: authenticated /mcp + open /healthz.
func httpHandler(authToken string) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "hcb",
		Title:   "HCB (read-only)",
		Version: "0.1.0",
	}, nil)
	registerTools(server)

	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.Handle("/mcp", authMiddleware(authToken, mcpHandler))
	return mux
}

// resolveCredentialsPath returns the credentials file path, seeding it from
// $HCB_CREDENTIALS_JSON when the file doesn't exist yet (container bootstrap).
// An existing file is never overwritten — it may hold rotated tokens that are
// newer than the seed.
func resolveCredentialsPath() (string, error) {
	path := os.Getenv("HCB_CREDENTIALS")
	if path == "" {
		var err error
		path, err = hcbapi.DefaultCredentialsPath()
		if err != nil {
			return "", err
		}
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if seed := os.Getenv("HCB_CREDENTIALS_JSON"); seed != "" {
			var creds hcbapi.Credentials
			if err := jsonUnmarshalStrict(seed, &creds); err != nil {
				return "", fmt.Errorf("parsing HCB_CREDENTIALS_JSON: %w", err)
			}
			if err := creds.Save(path); err != nil {
				return "", fmt.Errorf("seeding credentials file: %w", err)
			}
		}
	}
	return path, nil
}

func serveHTTP(addr string) error {
	authToken := os.Getenv("MCP_AUTH_TOKEN")
	if authToken == "" {
		return fmt.Errorf("MCP_AUTH_TOKEN must be set in --http mode: the server exposes the credential owner's HCB data")
	}
	fmt.Fprintf(os.Stderr, "hcb-mcp listening on %s (endpoint /mcp, health /healthz)\n", addr)
	return http.ListenAndServe(addr, httpHandler(authToken))
}
