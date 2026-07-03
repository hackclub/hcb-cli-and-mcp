package hcbapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// APIError is a non-2xx response from the v4 API, decoded from the standard
// {"error": "...", "messages": [...]} envelope.
type APIError struct {
	StatusCode int
	Code       string
	Messages   []string
	Body       string
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("HCB API error %d (%s)", e.StatusCode, e.Code)
	if len(e.Messages) > 0 {
		msg += ": " + strings.Join(e.Messages, "; ")
	}
	return msg
}

// Page is the cursor-pagination envelope used by v4 list endpoints.
type Page struct {
	TotalCount int             `json:"total_count"`
	HasMore    bool            `json:"has_more"`
	Data       json.RawMessage `json:"data"`
}

// Client is a read-only HCB v4 API client with automatic token refresh.
// Safe for concurrent use.
type Client struct {
	hc        *http.Client
	credsPath string

	mu    sync.Mutex
	creds *Credentials

	now func() time.Time // test seam
}

// NewClient loads credentials from path and returns a client.
func NewClient(credsPath string) (*Client, error) {
	creds, err := LoadCredentials(credsPath)
	if err != nil {
		return nil, err
	}
	return &Client{
		hc:        &http.Client{Timeout: 60 * time.Second},
		credsPath: credsPath,
		creds:     creds,
		now:       time.Now,
	}, nil
}

// NewClientFromDefaultPath loads ~/.config/hcb/credentials.json.
func NewClientFromDefaultPath() (*Client, error) {
	path, err := DefaultCredentialsPath()
	if err != nil {
		return nil, err
	}
	return NewClient(path)
}

func (c *Client) CredentialsPath() string { return c.credsPath }

// BaseURL returns the API host (e.g. https://hcb.hackclub.com).
func (c *Client) BaseURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.creds.BaseURL
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.creds.Expired(c.now()) && c.creds.RefreshToken != "" {
		if err := c.refreshLocked(ctx); err != nil {
			return "", err
		}
	}
	return c.creds.AccessToken, nil
}

// refreshLocked exchanges the refresh token and persists the rotated pair.
// Callers must hold c.mu.
func (c *Client) refreshLocked(ctx context.Context) error {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {c.creds.RefreshToken},
		"client_id":     {c.creds.ClientID},
		"client_secret": {c.creds.ClientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.creds.BaseURL+"/api/v4/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("refreshing token: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("refreshing token: HTTP %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Scope        string `json:"scope"`
		CreatedAt    int64  `json:"created_at"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return fmt.Errorf("parsing token response: %w", err)
	}
	c.creds.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		c.creds.RefreshToken = tok.RefreshToken // HCB rotates refresh tokens
	}
	if tok.Scope != "" {
		c.creds.Scope = tok.Scope
	}
	c.creds.CreatedAt = tok.CreatedAt
	c.creds.ExpiresIn = tok.ExpiresIn
	if err := c.creds.Save(c.credsPath); err != nil {
		return fmt.Errorf("persisting rotated tokens: %w", err)
	}
	return nil
}

// Refresh forces a token refresh (used by `hcb auth refresh`).
func (c *Client) Refresh(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refreshLocked(ctx)
}

// Get performs an authenticated GET and returns the raw JSON body.
// On 401 it refreshes once and retries with the fresh token.
func (c *Client) Get(ctx context.Context, path string, query url.Values) (json.RawMessage, error) {
	token, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	raw, status, err := c.doGet(ctx, token, path, query)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized {
		c.mu.Lock()
		refreshErr := c.refreshLocked(ctx)
		token = c.creds.AccessToken
		c.mu.Unlock()
		if refreshErr != nil {
			return nil, fmt.Errorf("401 and refresh failed: %w", refreshErr)
		}
		raw, status, err = c.doGet(ctx, token, path, query)
		if err != nil {
			return nil, err
		}
	}
	if status < 200 || status > 299 {
		return nil, decodeAPIError(status, raw)
	}
	return raw, nil
}

func (c *Client) doGet(ctx context.Context, token, path string, query url.Values) (json.RawMessage, int, error) {
	u := c.BaseURL() + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "hcb-mcp/0.1 (github.com/hackclub/hcb-mcp)")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, 0, err
	}
	return body, resp.StatusCode, nil
}

// GetPage performs Get and decodes the pagination envelope.
func (c *Client) GetPage(ctx context.Context, path string, query url.Values) (*Page, error) {
	raw, err := c.Get(ctx, path, query)
	if err != nil {
		return nil, err
	}
	var p Page
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("decoding pagination envelope: %w", err)
	}
	return &p, nil
}

func decodeAPIError(status int, body []byte) *APIError {
	apiErr := &APIError{StatusCode: status, Body: truncate(string(body), 2000)}
	var envelope struct {
		Error    string   `json:"error"`
		Messages []string `json:"messages"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil {
		apiErr.Code = envelope.Error
		apiErr.Messages = envelope.Messages
	}
	if apiErr.Code == "" {
		apiErr.Code = http.StatusText(status)
	}
	return apiErr
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
