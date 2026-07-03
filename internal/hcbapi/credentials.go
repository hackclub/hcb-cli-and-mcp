package hcbapi

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const credentialsKeyEnv = "HCB_CREDENTIALS_KEY"

// Credentials is the on-disk auth state at ~/.config/hcb/credentials.json.
type Credentials struct {
	BaseURL      string `json:"base_url"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	// TokenURL overrides BaseURL+/api/v4/oauth/token for refresh grants.
	// Set by AuthServer logins, where the hosted bridge holds the client
	// secret and proxies the exchange to HCB.
	TokenURL     string `json:"token_url,omitempty"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope,omitempty"`
	CreatedAt    int64  `json:"created_at"`
	ExpiresIn    int64  `json:"expires_in"`
}

type encryptedCredentialsEnvelope struct {
	Version    int    `json:"version"`
	Encrypted  bool   `json:"encrypted"`
	Cipher     string `json:"cipher"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
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
	c, err := LoadCredentialsBytes(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// LoadCredentialsBytes decodes a plaintext credentials JSON object or an
// encrypted credentials envelope.
func LoadCredentialsBytes(b []byte) (*Credentials, error) {
	var env encryptedCredentialsEnvelope
	if err := json.Unmarshal(b, &env); err == nil && env.Encrypted {
		var err error
		b, err = decryptCredentialsPayload(env)
		if err != nil {
			return nil, fmt.Errorf("decrypting credentials: %w", err)
		}
	}
	var c Credentials
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parsing credentials: %w", err)
	}
	return &c, nil
}

// CredentialsJSONEncrypted reports whether b looks like an encrypted
// credentials envelope. It does not decrypt the payload.
func CredentialsJSONEncrypted(b []byte) bool {
	var env encryptedCredentialsEnvelope
	return json.Unmarshal(b, &env) == nil && env.Encrypted
}

// CredentialsFileEncrypted reports whether path is an encrypted credentials
// envelope. It does not decrypt the file.
func CredentialsFileEncrypted(path string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("reading credentials: %w", err)
	}
	var env encryptedCredentialsEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		return false, fmt.Errorf("parsing credentials %s: %w", path, err)
	}
	return env.Encrypted, nil
}

// Save writes the credentials atomically with 0600 permissions.
func (c *Credentials) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b := payload
	if key, ok, err := credentialsEncryptionKey(); err != nil {
		return err
	} else if ok {
		b, err = encryptCredentialsPayload(payload, key)
		if err != nil {
			return err
		}
	}
	return WriteCredentialsBytes(path, b)
}

// WriteCredentialsBytes writes an already-encoded credentials document
// atomically with 0600 permissions.
func WriteCredentialsBytes(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
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

// RequireCredentialsEncryptionKey returns an error unless server-side
// credential encryption is configured.
func RequireCredentialsEncryptionKey() error {
	_, ok, err := credentialsEncryptionKey()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s must be set for server-owned credentials", credentialsKeyEnv)
	}
	return nil
}

func credentialsEncryptionKey() ([]byte, bool, error) {
	raw := strings.TrimSpace(os.Getenv(credentialsKeyEnv))
	if raw == "" {
		return nil, false, nil
	}
	key, err := parseCredentialsKey(raw)
	if err != nil {
		return nil, false, err
	}
	return key, true, nil
}

func parseCredentialsKey(raw string) ([]byte, error) {
	decoders := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, enc := range decoders {
		if b, err := enc.DecodeString(raw); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	if b, err := hex.DecodeString(raw); err == nil && len(b) == 32 {
		return b, nil
	}
	if len([]byte(raw)) == 32 {
		return []byte(raw), nil
	}
	return nil, fmt.Errorf("%s must be a 32-byte key encoded as base64, hex, or raw text", credentialsKeyEnv)
}

func encryptCredentialsPayload(payload, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	env := encryptedCredentialsEnvelope{
		Version:    1,
		Encrypted:  true,
		Cipher:     "AES-256-GCM",
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(gcm.Seal(nil, nonce, payload, []byte("hcb-credentials-v1"))),
	}
	return json.MarshalIndent(env, "", "  ")
}

func decryptCredentialsPayload(env encryptedCredentialsEnvelope) ([]byte, error) {
	if env.Version != 1 || env.Cipher != "AES-256-GCM" {
		return nil, fmt.Errorf("unsupported encrypted credentials format")
	}
	key, ok, err := credentialsEncryptionKey()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%s is required to decrypt this credentials file", credentialsKeyEnv)
	}
	nonce, err := base64.StdEncoding.DecodeString(env.Nonce)
	if err != nil {
		return nil, fmt.Errorf("decoding nonce: %w", err)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(env.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decoding ciphertext: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, ciphertext, []byte("hcb-credentials-v1"))
}

// Expired reports whether the access token is expired (or within a 120s
// safety margin of expiring). ExpiresIn == 0 means a non-expiring token.
func (c *Credentials) Expired(now time.Time) bool {
	if c.ExpiresIn == 0 {
		return false
	}
	return now.Unix() >= c.CreatedAt+c.ExpiresIn-120
}
