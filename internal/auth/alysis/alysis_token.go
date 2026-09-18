// Package alysis provides authentication and token management for the
// Alysis Code Pro hosted service.
package alysis

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	log "github.com/sirupsen/logrus"
)

// TokenStorage stores the gateway key persisted after a device-flow login.
type TokenStorage struct {
	// Key is the long-lived gateway key ("slk_...") used as the Bearer
	// credential against the OpenAI-compatible gateway.
	Key string `json:"gatewayKey"`

	// Email is the account email recorded at login time (best effort).
	Email string `json:"email"`

	// Type indicates the authentication provider type, always "alysis".
	Type string `json:"type"`
}

// SaveTokenToFile serializes the token storage to a JSON file.
func (ts *TokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "alysis"
	if err := os.MkdirAll(filepath.Dir(authFilePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	f, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("failed to create token file: %w", err)
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.Errorf("failed to close file: %v", errClose)
		}
	}()

	if err = json.NewEncoder(f).Encode(ts); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil
}

// CredentialFileName returns the filename used to persist alysis credentials.
// It mirrors the freebuff/qoder naming: prefer the account email, fall back to
// a hash of the gateway key so distinct accounts never overwrite each other,
// and use a fixed name only when neither is available.
func CredentialFileName(email, key string) string {
	email = strings.TrimSpace(email)
	if email != "" {
		return fmt.Sprintf("alysis-%s.json", email)
	}
	key = strings.TrimSpace(key)
	if key != "" {
		sum := sha256.Sum256([]byte(key))
		return fmt.Sprintf("alysis-%s.json", hex.EncodeToString(sum[:8]))
	}
	return "alysis-account.json"
}
