package hcbapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// newTestClient returns a Client pointed at a test server, with creds stored in a temp file.
func newTestClient(t *testing.T, srv *httptest.Server, creds *Credentials) *Client {
	t.Helper()
	if creds == nil {
		creds = &Credentials{
			AccessToken:  "hcb_valid",
			RefreshToken: "ref_1",
			ClientID:     "cid",
			ClientSecret: "csec",
		}
	}
	creds.BaseURL = srv.URL
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := creds.Save(path); err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(path)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestGetSendsAuthAndDecodes(t *testing.T) {
	var gotAuth, gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"org_abc","object":"organization"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, nil)
	raw, err := c.Get(context.Background(), "/api/v4/organizations/org_abc", url.Values{"expand": {"balance_cents"}})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotAuth != "Bearer hcb_valid" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotPath != "/api/v4/organizations/org_abc" {
		t.Errorf("path = %q", gotPath)
	}
	if gotQuery != "expand=balance_cents" {
		t.Errorf("query = %q", gotQuery)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj["id"] != "org_abc" {
		t.Errorf("bad body: %s (err %v)", raw, err)
	}
}

func TestGetAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"not_authorized"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, nil)
	_, err := c.Get(context.Background(), "/api/v4/checks/chk_x", nil)
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != 403 || apiErr.Code != "not_authorized" {
		t.Errorf("got %+v", apiErr)
	}
}

func TestGetErrorMessages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_operation","messages":["Limit is capped at 100."]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, nil)
	_, err := c.Get(context.Background(), "/api/v4/receipts", nil)
	apiErr, ok := err.(*APIError)
	if !ok || len(apiErr.Messages) != 1 || apiErr.Messages[0] != "Limit is capped at 100." {
		t.Fatalf("got %v", err)
	}
}

// A 401 from the API must trigger one refresh (rotating tokens on disk) and a retry.
func TestRefreshOn401AndRotation(t *testing.T) {
	var apiCalls, refreshCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		apiCalls.Add(1)
		if r.Header.Get("Authorization") == "Bearer hcb_new" {
			w.Write([]byte(`{"id":"usr_1","object":"user"}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid_auth"}`))
	})
	mux.HandleFunc("/api/v4/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		refreshCalls.Add(1)
		r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "ref_1" {
			t.Errorf("bad refresh request: %v", r.Form)
		}
		w.Write([]byte(`{"access_token":"hcb_new","refresh_token":"ref_2","created_at":2000,"expires_in":7200,"scope":"read","token_type":"Bearer"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	creds := &Credentials{AccessToken: "hcb_stale", RefreshToken: "ref_1", ClientID: "cid", ClientSecret: "csec"}
	c := newTestClient(t, srv, creds)

	raw, err := c.Get(context.Background(), "/api/v4/user", nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(raw) != `{"id":"usr_1","object":"user"}` {
		t.Errorf("body = %s", raw)
	}
	if refreshCalls.Load() != 1 {
		t.Errorf("refreshCalls = %d, want 1", refreshCalls.Load())
	}

	// rotated tokens must be persisted
	saved, err := LoadCredentials(c.CredentialsPath())
	if err != nil {
		t.Fatal(err)
	}
	if saved.AccessToken != "hcb_new" || saved.RefreshToken != "ref_2" {
		t.Errorf("rotation not persisted: %+v", saved)
	}
}

// A proactively-expired token refreshes before the first API call.
func TestProactiveRefresh(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer hcb_new" {
			t.Errorf("expected refreshed token, got %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{}`))
	})
	mux.HandleFunc("/api/v4/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"hcb_new","refresh_token":"ref_2","created_at":2000,"expires_in":7200}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	creds := &Credentials{AccessToken: "hcb_old", RefreshToken: "ref_1", ClientID: "cid", ClientSecret: "csec", CreatedAt: 1, ExpiresIn: 10}
	c := newTestClient(t, srv, creds)
	if _, err := c.Get(context.Background(), "/api/v4/user", nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
}

func TestGetPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"total_count":42,"has_more":true,"data":[{"id":"txn_a"},{"id":"txn_b"}]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, nil)
	page, err := c.GetPage(context.Background(), "/api/v4/user/transactions/missing_receipt", nil)
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if page.TotalCount != 42 || !page.HasMore {
		t.Errorf("envelope = %+v", page)
	}
	var items []map[string]any
	if err := json.Unmarshal(page.Data, &items); err != nil || len(items) != 2 {
		t.Errorf("data = %s", page.Data)
	}
}
