package commandcode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	log "github.com/sirupsen/logrus"
)

// CommandCodeTokenStorage stores credential information for Command Code
// authentication. The API key is long-lived and does not require refresh.
type CommandCodeTokenStorage struct {
	// APIKey is the Command Code API key (prefix "user_").
	APIKey string `json:"commandcodeApiKey"`

	// SessionToken is the optional Web console session token (from __Secure-commandcode_prod.session_token) for quota querying.
	SessionToken string `json:"sessionToken,omitempty"`

	// UserID is the authenticated user identifier.
	UserID string `json:"userId,omitempty"`

	// UserName is the authenticated user name.
	UserName string `json:"userName,omitempty"`

	// KeyName is the human-readable key label chosen at login.
	KeyName string `json:"keyName,omitempty"`

	// AuthenticatedAt records when the login completed.
	AuthenticatedAt string `json:"authenticatedAt,omitempty"`

	// Type indicates the authentication provider type, always "commandcode".
	Type string `json:"type"`

	// Metadata holds arbitrary key-value pairs injected via hooks.
	// It is not exported to JSON directly to allow flattening during serialization.
	Metadata map[string]any `json:"-"`
}

// GetAPIKey returns the stored API key.
func (ts *CommandCodeTokenStorage) GetAPIKey() string {
	return ts.APIKey
}

// GetSessionToken returns the stored web session token.
func (ts *CommandCodeTokenStorage) GetSessionToken() string {
	return ts.SessionToken
}

// SetSessionToken sets the web session token.
func (ts *CommandCodeTokenStorage) SetSessionToken(token string) {
	ts.SessionToken = token
}

// SetMetadata allows external callers to inject metadata into the storage before saving.
func (ts *CommandCodeTokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

// SaveTokenToFile serializes the Command Code token storage to a JSON file.
func (ts *CommandCodeTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "commandcode"
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

	// Merge metadata using helper
	data, errMerge := misc.MergeMetadata(ts, ts.Metadata)
	if errMerge != nil {
		return fmt.Errorf("failed to merge metadata: %w", errMerge)
	}

	if err = json.NewEncoder(f).Encode(data); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil
}

// CredentialFileName returns the filename used to persist Command Code credentials.
func CredentialFileName(userName string) string {
	if userName == "" {
		userName = "default"
	}
	return fmt.Sprintf("commandcode-%s.json", SanitizeFileNamePart(userName))
}

// SanitizeFileNamePart strips characters unsafe for filenames.
func SanitizeFileNamePart(part string) string {
	out := make([]rune, 0, len(part))
	for _, r := range part {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	if len(out) == 0 {
		return "default"
	}
	return string(out)
}
