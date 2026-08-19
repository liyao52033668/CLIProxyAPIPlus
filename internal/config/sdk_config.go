// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing YAML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

import (
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// APIKeyEntry represents a client API key with optional model filtering.
type APIKeyEntry struct {
	// Key is the API key string for authentication.
	Key string `yaml:"key" json:"key"`
	// Models is an optional whitelist of model IDs. When set, /v1/models only returns these models.
	// Empty or nil means all models are visible.
	Models []string `yaml:"models,omitempty" json:"models,omitempty"`
}

// APIKeyList is a custom list of API key entries that supports both legacy string format
// and new object format in YAML/JSON.
type APIKeyList []APIKeyEntry

// UnmarshalYAML implements custom YAML unmarshaling to support both formats:
// - Legacy: ["key1", "key2"]
// - New: [{key: "key1", models: [...]}, "key2"]
func (l *APIKeyList) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.SequenceNode {
		return fmt.Errorf("api-keys must be a sequence")
	}

	entries := make([]APIKeyEntry, 0, len(value.Content))
	for _, node := range value.Content {
		var entry APIKeyEntry
		// Try object format first
		if node.Kind == yaml.MappingNode {
			if err := node.Decode(&entry); err != nil {
				return fmt.Errorf("invalid api-key entry: %w", err)
			}
			entries = append(entries, entry)
			continue
		}
		// Fall back to string format (legacy)
		var keyStr string
		if err := node.Decode(&keyStr); err != nil {
			return fmt.Errorf("api-key entry must be string or object: %w", err)
		}
		entries = append(entries, APIKeyEntry{Key: keyStr})
	}
	*l = entries
	return nil
}

// MarshalYAML implements custom YAML marshaling.
func (l APIKeyList) MarshalYAML() (interface{}, error) {
	// Check if all entries have no models (legacy format)
	allLegacy := true
	for _, entry := range l {
		if len(entry.Models) > 0 {
			allLegacy = false
			break
		}
	}
	if allLegacy {
		// Output as simple string list
		keys := make([]string, len(l))
		for i, entry := range l {
			keys[i] = entry.Key
		}
		return keys, nil
	}
	// Output as object list
	return []APIKeyEntry(l), nil
}

// UnmarshalJSON implements custom JSON unmarshaling.
func (l *APIKeyList) UnmarshalJSON(data []byte) error {
	// Try array of strings first (legacy)
	var strArr []string
	if err := json.Unmarshal(data, &strArr); err == nil {
		entries := make([]APIKeyEntry, len(strArr))
		for i, s := range strArr {
			entries[i] = APIKeyEntry{Key: s}
		}
		*l = entries
		return nil
	}
	// Try array of objects
	var objArr []APIKeyEntry
	if err := json.Unmarshal(data, &objArr); err != nil {
		return fmt.Errorf("api-keys must be array of strings or objects: %w", err)
	}
	*l = objArr
	return nil
}

// MarshalJSON implements custom JSON marshaling.
func (l APIKeyList) MarshalJSON() ([]byte, error) {
	// Check if all entries have no models (legacy format)
	allLegacy := true
	for _, entry := range l {
		if len(entry.Models) > 0 {
			allLegacy = false
			break
		}
	}
	if allLegacy {
		keys := make([]string, len(l))
		for i, entry := range l {
			keys[i] = entry.Key
		}
		return json.Marshal(keys)
	}
	return json.Marshal([]APIKeyEntry(l))
}

// ToStrings returns a list of key strings (for backward compatibility).
func (l APIKeyList) ToStrings() []string {
	keys := make([]string, len(l))
	for i, entry := range l {
		keys[i] = entry.Key
	}
	return keys
}

// GetEntry returns the entry for a given key, or nil if not found.
func (l APIKeyList) GetEntry(key string) *APIKeyEntry {
	for i := range l {
		if l[i].Key == key {
			return &l[i]
		}
	}
	return nil
}

// SDKConfig represents the application's configuration, loaded from a YAML file.
type SDKConfig struct {
	// ProxyURL is the URL of an optional proxy server to use for outbound requests.
	ProxyURL string `yaml:"proxy-url" json:"proxy-url"`

	// DisableUTLS disables the uTLS Chrome ClientHello used for Anthropic HTTPS requests.
	// The default is false for compatibility; enable it to use the standard Go TLS stack.
	DisableUTLS bool `yaml:"disable-utls" json:"disable-utls"`

	// CPAToken is the authorization token for CPA API requests (e.g., fetching latest release version).
	CPAToken string `yaml:"cpa-token" json:"cpa-token"`
	// DisableImageGeneration controls whether the built-in image_generation tool is injected/allowed.
	//
	// Supported values:
	//   - false (default): image_generation is enabled everywhere (normal behavior).
	//   - true: image_generation is disabled everywhere. The server stops injecting it, removes it from request payloads,
	//     and returns 404 for /v1/images/generations and /v1/images/edits.
	//   - "chat": disable image_generation injection for all non-images endpoints (e.g. /v1/responses, /v1/chat/completions),
	//     while keeping /v1/images/generations and /v1/images/edits enabled and preserving image_generation there.
	DisableImageGeneration DisableImageGenerationMode `yaml:"disable-image-generation" json:"disable-image-generation"`

	// EnableGeminiCLIEndpoint controls whether Gemini CLI internal endpoints (/v1internal:*) are enabled.
	// Default is false for safety; when false, /v1internal:* requests are rejected.
	EnableGeminiCLIEndpoint bool `yaml:"enable-gemini-cli-endpoint" json:"enable-gemini-cli-endpoint"`

	// ForceModelPrefix requires explicit model prefixes (e.g., "teamA/gemini-3-pro-preview")
	// to target prefixed credentials. When false, unprefixed model requests may use prefixed
	// credentials as well.
	ForceModelPrefix bool `yaml:"force-model-prefix" json:"force-model-prefix"`

	// RequestLog enables or disables detailed request logging functionality.
	RequestLog bool `yaml:"request-log" json:"request-log"`

	// APIKeys is a list of keys for authenticating clients to this proxy server.
	// Each entry can be a plain string (legacy) or an object with "key" and optional "models" fields.
	// When "models" is set, the /v1/models endpoint only returns those models for that key.
	APIKeys APIKeyList `yaml:"api-keys" json:"api-keys"`

	// AllowQueryAPIKey allows API keys in URL query parameters for legacy clients.
	// It defaults to false because URLs can leak through logs, browser history, and referrers.
	AllowQueryAPIKey bool `yaml:"allow-query-api-key" json:"allow-query-api-key"`

	// PassthroughHeaders controls whether upstream response headers are forwarded to downstream clients.
	// Default is false (disabled).
	PassthroughHeaders bool `yaml:"passthrough-headers" json:"passthrough-headers"`

	// Streaming configures server-side streaming behavior (keep-alives and safe bootstrap retries).
	Streaming StreamingConfig `yaml:"streaming" json:"streaming"`

	// NonStreamKeepAliveInterval controls how often blank lines are emitted for non-streaming responses.
	// <= 0 disables keep-alives. Value is in seconds.
	NonStreamKeepAliveInterval int `yaml:"nonstream-keepalive-interval,omitempty" json:"nonstream-keepalive-interval,omitempty"`
}

// StreamingConfig holds server streaming behavior configuration.
type StreamingConfig struct {
	// KeepAliveSeconds controls how often the server emits SSE heartbeats (": keep-alive\n\n").
	// <= 0 disables keep-alives. Default is 0.
	KeepAliveSeconds int `yaml:"keepalive-seconds,omitempty" json:"keepalive-seconds,omitempty"`

	// BootstrapRetries controls how many times the server may retry a streaming request before any bytes are sent,
	// to allow auth rotation / transient recovery.
	// <= 0 disables bootstrap retries. Default is 0.
	BootstrapRetries int `yaml:"bootstrap-retries,omitempty" json:"bootstrap-retries,omitempty"`
}
