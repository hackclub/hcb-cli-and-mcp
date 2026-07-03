package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"v0.1.0", "v0.2.0", true},
		{"v0.2.0", "v0.1.0", false},
		{"v0.1.0", "v0.1.0", false},
		{"0.1.0", "v0.1.1", true},
		{"v0.9.0", "v0.10.0", true},
		{"v1.0.0", "v0.99.99", false},
		{"v0.1.0", "v1.0.0", true},
		{"dev", "v1.0.0", false}, // dev builds never self-update
		{"v0.1.0", "", false},
		{"v0.1.0", "not-a-version", false},
	}
	for _, c := range cases {
		if got := newerVersion(c.current, c.latest); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestBrewManaged(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/opt/homebrew/Cellar/hcb/0.1.0/bin/hcb", true},
		{"/usr/local/Cellar/hcb/0.1.0/bin/hcb", true},
		{"/home/linuxbrew/.linuxbrew/Cellar/hcb/0.1.0/bin/hcb", true},
		{"/opt/homebrew/opt/hcb/bin/hcb", true},
		{"/Users/zrl/go/bin/hcb", false},
		{"/usr/local/bin/hcb-built-from-source", false},
	}
	for _, c := range cases {
		if got := brewManaged(c.path); got != c.want {
			t.Errorf("brewManaged(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestFetchLatestVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"tag_name": "v0.3.1"})
	}))
	defer srv.Close()
	got, err := fetchLatestVersion(srv.URL)
	if err != nil || got != "v0.3.1" {
		t.Fatalf("fetchLatestVersion = %q, %v", got, err)
	}
}

func TestUpdateStateThrottle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update_state.json")

	// no state file -> due
	if !updateCheckDue(path, 0) {
		t.Error("missing state should be due")
	}
	if err := writeUpdateState(path, updateState{CheckedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	// nowUnix inside the window -> not due
	if updateCheckDueAt(path, 1000+3600, 86400) {
		t.Error("check within 24h should not be due")
	}
	if !updateCheckDueAt(path, 1000+86401, 86400) {
		t.Error("check after 24h should be due")
	}

	// notice round-trip
	if err := writeUpdateState(path, updateState{CheckedAt: 1, Notice: "updated to v9"}); err != nil {
		t.Fatal(err)
	}
	st := readUpdateState(path)
	if st.Notice != "updated to v9" {
		t.Errorf("notice = %q", st.Notice)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("state file mode = %o", info.Mode().Perm())
	}
}
