package helps

import (
	"fmt"
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// AttributeString returns a trimmed string attribute value when present.
func AttributeString(attrs map[string]string, key string) string {
	if attrs == nil || key == "" {
		return ""
	}
	return strings.TrimSpace(attrs[key])
}

// MetadataString returns the first non-empty trimmed string metadata value for
// any of the provided keys. Supported value types are string, []byte, and
// fmt.Stringer.
func MetadataString(metadata map[string]any, keys ...string) string {
	if metadata == nil || len(keys) == 0 {
		return ""
	}
	for _, key := range keys {
		if key == "" {
			continue
		}
		raw, ok := metadata[key]
		if !ok || raw == nil {
			continue
		}
		switch typed := raw.(type) {
		case string:
			if value := strings.TrimSpace(typed); value != "" {
				return value
			}
		case []byte:
			if value := strings.TrimSpace(string(typed)); value != "" {
				return value
			}
		case fmt.Stringer:
			if value := strings.TrimSpace(typed.String()); value != "" {
				return value
			}
		}
	}
	return ""
}

// ResolveAPIKeyAndBaseURL extracts the common API credential pair used by most
// executors.
//
// Priority:
//   - apiKey: attributes["api_key"], then the first non-empty metadata key in
//     metaAPIKeyKeys (default: "access_token")
//   - baseURL: attributes["base_url"], then metadata["base_url"]
//
// All returned values are trimmed.
func ResolveAPIKeyAndBaseURL(auth *cliproxyauth.Auth, metaAPIKeyKeys ...string) (apiKey, baseURL string) {
	if auth == nil {
		return "", ""
	}
	if len(metaAPIKeyKeys) == 0 {
		metaAPIKeyKeys = []string{"access_token"}
	}
	apiKey = AttributeString(auth.Attributes, "api_key")
	baseURL = AttributeString(auth.Attributes, "base_url")
	if apiKey == "" {
		apiKey = MetadataString(auth.Metadata, metaAPIKeyKeys...)
	}
	if baseURL == "" {
		baseURL = MetadataString(auth.Metadata, "base_url")
	}
	return apiKey, baseURL
}
