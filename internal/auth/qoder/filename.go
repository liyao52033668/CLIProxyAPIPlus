package qoder

import (
	"fmt"
	"strings"
)

// CredentialFileName returns the filename used to persist Qoder credentials.
// It prioritizes email if available, otherwise falls back to uid to disambiguate accounts.
func CredentialFileName(uid, email string) string {
	email = strings.TrimSpace(email)
	if email != "" {
		// Sanitize email for filename
		email = strings.ReplaceAll(email, "@", "_")
		email = strings.ReplaceAll(email, ".", "_")
		return fmt.Sprintf("qoder-%s.json", email)
	}
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return "qoder.json"
	}
	return fmt.Sprintf("qoder-%s.json", uid)
}
