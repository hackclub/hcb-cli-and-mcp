package hcbapi

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadSaveCredentials(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")

	creds := &Credentials{
		BaseURL:      "https://hcb.hackclub.com",
		ClientID:     "cid",
		ClientSecret: "csec",
		AccessToken:  "hcb_abc",
		RefreshToken: "ref_1",
		Scope:        "read",
		CreatedAt:    1751500000,
		ExpiresIn:    7200,
	}
	if err := creds.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("credentials file mode = %o, want 600", perm)
	}

	got, err := LoadCredentials(path)
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if *got != *creds {
		t.Errorf("round-trip mismatch: got %+v want %+v", got, creds)
	}
}

func TestLoadCredentialsMissing(t *testing.T) {
	_, err := LoadCredentials(filepath.Join(t.TempDir(), "nope.json"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestCredentialsExpired(t *testing.T) {
	creds := &Credentials{CreatedAt: 1000, ExpiresIn: 7200}
	if creds.Expired(time.Unix(1000, 0)) {
		t.Error("fresh token reported expired")
	}
	// within the 120s safety margin counts as expired
	if !creds.Expired(time.Unix(1000+7200-60, 0)) {
		t.Error("token within safety margin not reported expired")
	}
	if !creds.Expired(time.Unix(1000+7200+1, 0)) {
		t.Error("stale token not reported expired")
	}
	// zero expiry means non-expiring token
	nonExp := &Credentials{CreatedAt: 1000, ExpiresIn: 0}
	if nonExp.Expired(time.Unix(999999999, 0)) {
		t.Error("non-expiring token reported expired")
	}
}
