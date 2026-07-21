package copilot

import (
	"fmt"
	"strings"
)

// CredentialFileName returns the filename used to persist GitHub Copilot OAuth credentials.
func CredentialFileName(username string, includeProviderPrefix bool) string {
	username = strings.TrimSpace(username)

	prefix := ""
	if includeProviderPrefix {
		prefix = "github-copilot"
	}

	return fmt.Sprintf("%s-%s.json", prefix, username)
}
