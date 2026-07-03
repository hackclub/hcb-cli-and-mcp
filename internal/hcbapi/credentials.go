package hcbapi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Credentials is the on-disk auth state at ~/.config/hcb/credentials.json.
type Credentials struct {
	BaseURL      string `json:"base_url"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope,omitempty"`
	CreatedAt    int64  `json:"created_at"`
	ExpiresIn    int64  `json:"expires_in"`
}

// DefaultCredentialsPath is ~/.config/hcb/credentials.json.
func DefaultCredentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "hcb", "credentials.json"), nil
}

func LoadCredentials(path string) (*Credentials, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading credentials: %w (run `hcb login`)", err)
	}
	var c Credentials
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parsing credentials %s: %w", path, err)
	}
	return &c, nil
}

// Save writes the credentials atomically with 0600 permissions.
func (c *Credentials) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".credentials-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Expired reports whether the access token is expired (or within a 120s
// safety margin of expiring). ExpiresIn == 0 means a non-expiring token.
func (c *Credentials) Expired(now time.Time) bool {
	if c.ExpiresIn == 0 {
		return false
	}
	return now.Unix() >= c.CreatedAt+c.ExpiresIn-120
}
