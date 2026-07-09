package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hackclub/hcb-cli-and-mcp/internal/hcbapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpSessionTimeout bounds the lifetime of clients that disconnect without
// sending the MCP DELETE request. The SDK otherwise retains them forever.
const mcpSessionTimeout = 15 * time.Minute

// httpConfig is the HTTP-mode configuration, sourced from the environment.
type httpConfig struct {
	// staticToken is the optional shared secret (MCP_AUTH_TOKEN). A request
	// presenting it uses the server-owned credentials file.
	staticToken string
	// hcbBaseURL is the upstream HCB instance.
	hcbBaseURL string
	// oauthClientID/Secret are the HCB OAuth app used by the OAuth bridge
	// (discovery + dynamic-registration stub + token proxy) so MCP clients
	// like claude.ai can run a real per-user authorization-code flow.
	oauthClientID     string
	oauthClientSecret string
}

func loadHTTPConfig() (httpConfig, error) {
	cfg := httpConfig{
		staticToken:       os.Getenv("MCP_AUTH_TOKEN"),
		hcbBaseURL:        os.Getenv("HCB_BASE_URL"),
		oauthClientID:     os.Getenv("HCB_OAUTH_CLIENT_ID"),
		oauthClientSecret: os.Getenv("HCB_OAUTH_CLIENT_SECRET"),
	}
	if cfg.hcbBaseURL == "" {
		cfg.hcbBaseURL = "https://hcb.hackclub.com"
	}
	if cfg.staticToken == "" && cfg.oauthClientID == "" {
		return cfg, fmt.Errorf("--http mode needs MCP_AUTH_TOKEN (shared-secret auth) and/or HCB_OAUTH_CLIENT_ID (per-user OAuth): the server exposes HCB data")
	}
	return cfg, nil
}

// origin reconstructs this server's public origin from the request.
func origin(r *http.Request) string {
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return scheme + "://" + r.Host
}

// cors adds permissive CORS for browser-based MCP clients and OAuth discovery.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Mcp-Session-Id, MCP-Protocol-Version, Last-Event-ID")
		h.Set("Access-Control-Expose-Headers", "Mcp-Session-Id, WWW-Authenticate")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// httpHandler builds the full HTTP surface:
//
//	/healthz                                    unauthenticated liveness
//	/.well-known/oauth-protected-resource       MCP OAuth discovery (RFC 9728)
//	/.well-known/oauth-authorization-server     AS metadata (RFC 8414) — issuer is
//	                                            this host; see oauth.go for the
//	                                            sub-authorization-server design
//	/oauth/authorize                            sub-AS authorize: any client
//	                                            redirect_uri, bounces through HCB
//	/oauth/callback                             the ONE redirect URI registered
//	                                            on the HCB OAuth app
//	/oauth/register                             dynamic client registration stub
//	/oauth/token                                redeems sub-AS codes (PKCE), else
//	                                            proxies upstream injecting the
//	                                            client secret
//	/mcp                                        the MCP endpoint (per-request auth)
func httpHandler(cfg httpConfig) http.Handler {
	return httpHandlerWithSessionTimeout(cfg, mcpSessionTimeout)
}

func httpHandlerWithSessionTimeout(cfg httpConfig, sessionTimeout time.Duration) http.Handler {
	mux := http.NewServeMux()
	bridge := newAuthBridge()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		o := origin(r)
		writeJSON(w, http.StatusOK, map[string]any{
			"resource":                 o + "/mcp",
			"authorization_servers":    []string{o},
			"scopes_supported":         supportedOAuthScopes,
			"bearer_methods_supported": []string{"header"},
			"resource_name":            "HCB (read-only)",
		})
	})

	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		if cfg.oauthClientID == "" {
			http.NotFound(w, r)
			return
		}
		o := origin(r)
		writeJSON(w, http.StatusOK, map[string]any{
			"issuer":                                o,
			"authorization_endpoint":                o + "/oauth/authorize",
			"token_endpoint":                        o + "/oauth/token",
			"registration_endpoint":                 o + "/oauth/register",
			"response_types_supported":              []string{"code"},
			"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
			"code_challenge_methods_supported":      []string{"S256"},
			"scopes_supported":                      supportedOAuthScopes,
			"token_endpoint_auth_methods_supported": []string{"none"},
		})
	})

	// Dynamic client registration stub: HCB (Doorkeeper) has no DCR, so every
	// "registration" returns the one pre-registered HCB OAuth app. The client
	// secret is never disclosed — the token proxy injects it server-side. The
	// redirect URIs echoed here must also be registered on the HCB app.
	mux.HandleFunc("/oauth/register", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || cfg.oauthClientID == "" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			RedirectURIs []string `json:"redirect_uris"`
			ClientName   string   `json:"client_name"`
			Scope        string   `json:"scope"`
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		json.Unmarshal(body, &req)
		scope, err := normalizeOAuthScope(req.Scope)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_scope", "error_description": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"client_id":                  cfg.oauthClientID,
			"redirect_uris":              req.RedirectURIs,
			"client_name":                req.ClientName,
			"token_endpoint_auth_method": "none",
			"grant_types":                []string{"authorization_code", "refresh_token"},
			"response_types":             []string{"code"},
			"scope":                      scope,
		})
	})

	// Sub-AS legs of the authorization-code flow (see oauth.go).
	mux.HandleFunc("/oauth/authorize", bridge.handleAuthorize(cfg))
	mux.HandleFunc("/oauth/callback", bridge.handleCallback(cfg))

	// Token endpoint: codes minted by the sub-AS are redeemed locally (PKCE);
	// anything else — refresh grants, legacy direct-to-HCB codes — is
	// forwarded to HCB with the confidential client's credentials injected so
	// they never leave the server.
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || cfg.oauthClientID == "" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}
		if requested := r.PostForm.Get("scope"); requested != "" {
			scope, err := normalizeOAuthScope(requested)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_scope", "error_description": err.Error()})
				return
			}
			r.PostForm.Set("scope", scope)
		}
		if r.PostForm.Get("grant_type") == "authorization_code" {
			tokenJSON, ok, errCode := bridge.redeem(
				r.PostForm.Get("code"), r.PostForm.Get("code_verifier"), r.PostForm.Get("redirect_uri"))
			if ok {
				if errCode != "" {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": errCode})
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write(tokenJSON)
				return
			}
		}
		form := url.Values{}
		for k, vs := range r.PostForm {
			for _, v := range vs {
				form.Add(k, v)
			}
		}
		form.Set("client_id", cfg.oauthClientID)
		form.Set("client_secret", cfg.oauthClientSecret)

		upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
			cfg.hcbBaseURL+"/api/v4/oauth/token", strings.NewReader(form.Encode()))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
			return
		}
		upstream.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(upstream)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "server_error", "error_description": "upstream token endpoint unreachable"})
			return
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
	})

	// The MCP endpoint. Per-request auth:
	//   - the shared secret (MCP_AUTH_TOKEN)   -> server-owned credentials
	//   - any other bearer token               -> treated as the caller's own
	//     HCB access token and forwarded upstream (per-user access)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		c := clientForRequest(cfg, r)
		if c == nil {
			return nil // handler responds 400; auth middleware already 401s empty tokens
		}
		server := mcp.NewServer(&mcp.Implementation{
			Name:    "hcb",
			Title:   "HCB (read-only)",
			Version: "0.1.0",
		}, nil)
		registerToolsWithOptions(server, c, toolOptions{AllowLocalFileWrites: false})
		return server
	}, &mcp.StreamableHTTPOptions{SessionTimeout: sessionTimeout})

	mux.Handle("/mcp", requireToken(mcpHandler))
	return cors(mux)
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
			seedBytes := []byte(seed)
			if os.Getenv("MCP_AUTH_TOKEN") != "" {
				if err := hcbapi.RequireCredentialsEncryptionKey(); err != nil {
					return "", fmt.Errorf("HCB_CREDENTIALS_JSON with MCP_AUTH_TOKEN requires encrypted credential storage: %w", err)
				}
				if !hcbapi.CredentialsJSONEncrypted(seedBytes) {
					return "", fmt.Errorf("HCB_CREDENTIALS_JSON with MCP_AUTH_TOKEN must be an encrypted credentials envelope")
				}
			}
			if hcbapi.CredentialsJSONEncrypted(seedBytes) {
				if _, err := hcbapi.LoadCredentialsBytes(seedBytes); err != nil {
					return "", fmt.Errorf("validating encrypted HCB_CREDENTIALS_JSON: %w", err)
				}
				if err := hcbapi.WriteCredentialsBytes(path, seedBytes); err != nil {
					return "", fmt.Errorf("seeding encrypted credentials file: %w", err)
				}
				return path, nil
			}
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

// presentedToken extracts the bearer token from the Authorization header or
// the ?key= query parameter (for clients that can't set headers).
func presentedToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return r.URL.Query().Get("key")
}

// requireToken rejects unauthenticated requests with a spec-compliant 401
// that points OAuth-capable clients at the discovery document.
func requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if presentedToken(r) == "" {
			w.Header().Set("WWW-Authenticate",
				fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource"`, origin(r)))
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientForRequest maps the presented token to an API client.
func clientForRequest(cfg httpConfig, r *http.Request) *hcbapi.Client {
	tok := presentedToken(r)
	if tok == "" {
		return nil
	}
	if cfg.staticToken != "" &&
		subtle.ConstantTimeCompare([]byte(tok), []byte(cfg.staticToken)) == 1 {
		return client // server-owned credentials (set in main)
	}
	return hcbapi.NewClientWithToken(cfg.hcbBaseURL, tok)
}

func validateHTTPServerCredentials(cfg httpConfig) error {
	if cfg.staticToken != "" && client == nil {
		return fmt.Errorf("MCP_AUTH_TOKEN (shared-secret mode) is set but no server credentials could be loaded — set HCB_CREDENTIALS_JSON or unset MCP_AUTH_TOKEN")
	}
	if cfg.staticToken != "" {
		if err := hcbapi.RequireCredentialsEncryptionKey(); err != nil {
			return fmt.Errorf("MCP_AUTH_TOKEN uses server-owned credentials, so encrypted credential storage is required: %w", err)
		}
		path := client.CredentialsPath()
		if path == "" {
			return fmt.Errorf("MCP_AUTH_TOKEN requires server-owned credentials loaded from an encrypted credentials file")
		}
		encrypted, err := hcbapi.CredentialsFileEncrypted(path)
		if err != nil {
			return fmt.Errorf("checking server credentials encryption: %w", err)
		}
		if !encrypted {
			return fmt.Errorf("server-owned credentials file %s is plaintext; rewrite it with HCB_CREDENTIALS_KEY set before starting shared-secret mode", path)
		}
	}
	return nil
}

func serveHTTP(addr string) error {
	cfg, err := loadHTTPConfig()
	if err != nil {
		return err
	}
	if err := validateHTTPServerCredentials(cfg); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "hcb-mcp listening on %s (endpoint /mcp, health /healthz, oauth bridge %v)\n",
		addr, cfg.oauthClientID != "")
	return http.ListenAndServe(addr, httpHandler(cfg))
}
