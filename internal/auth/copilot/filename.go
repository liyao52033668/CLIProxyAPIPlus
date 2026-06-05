package copilot

import (
	"fmt"
	"strings"
	"unicode"
)

// CredentialFileName returns the filename used to persist GitHub Copilot OAuth credentials.
// When planType is available (e.g. "free", "pro", "pro+", "max"), it is appended after the username
// as a suffix to disambiguate subscriptions, similar to Codex credential naming.
func CredentialFileName(username, planType string, includeProviderPrefix bool) string {
	username = strings.TrimSpace(username)
	plan := normalizePlanTypeForFilename(planType)

	prefix := ""
	if includeProviderPrefix {
		prefix = "github-copilot"
	}

	if plan == "" {
		return fmt.Sprintf("%s-%s.json", prefix, username)
	}
	return fmt.Sprintf("%s-%s-%s.json", prefix, username, plan)
}

func normalizePlanTypeForFilename(planType string) string {
	planType = strings.TrimSpace(planType)
	if planType == "" {
		return ""
	}

	parts := strings.FieldsFunc(planType, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(parts) == 0 {
		return ""
	}

	for i, part := range parts {
		parts[i] = strings.ToLower(strings.TrimSpace(part))
	}
	return strings.Join(parts, "-")
}
