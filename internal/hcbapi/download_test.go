package hcbapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Signed blob URLs must be fetched WITHOUT the Authorization header (they're
// often on a different host; leaking the bearer token there would be a bug).
func TestDownloadFileNoAuthHeader(t *testing.T) {
	var gotAuth string
	fileSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Disposition", `inline; filename="receipt one.pdf"; filename*=UTF-8''receipt%20one.pdf`)
		w.Write([]byte("%PDF-1.4 fake"))
	}))
	defer fileSrv.Close()

	c := newTestClient(t, fileSrv, nil)
	dest := t.TempDir()
	path, err := c.DownloadFile(context.Background(), fileSrv.URL+"/rails/active_storage/blobs/xyz/receipt.pdf", dest, "")
	if err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization header leaked to file host: %q", gotAuth)
	}
	if filepath.Base(path) != "receipt one.pdf" {
		t.Errorf("filename = %q, want from Content-Disposition", filepath.Base(path))
	}
	b, _ := os.ReadFile(path)
	if string(b) != "%PDF-1.4 fake" {
		t.Errorf("content = %q", b)
	}
}

func TestDownloadFileFallbackNameAndExplicitName(t *testing.T) {
	fileSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("data"))
	}))
	defer fileSrv.Close()
	c := newTestClient(t, fileSrv, nil)
	dest := t.TempDir()

	// falls back to URL path basename
	path, err := c.DownloadFile(context.Background(), fileSrv.URL+"/blobs/logo.png?sig=abc", dest, "")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "logo.png" {
		t.Errorf("fallback name = %q", filepath.Base(path))
	}

	// explicit name wins
	path, err = c.DownloadFile(context.Background(), fileSrv.URL+"/blobs/logo.png", dest, "custom.bin")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "custom.bin" {
		t.Errorf("explicit name = %q", filepath.Base(path))
	}
}

func TestDownloadFileHTTPError(t *testing.T) {
	fileSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer fileSrv.Close()
	c := newTestClient(t, fileSrv, nil)
	_, err := c.DownloadFile(context.Background(), fileSrv.URL+"/blobs/gone.png", t.TempDir(), "")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("want 404 error, got %v", err)
	}
}
