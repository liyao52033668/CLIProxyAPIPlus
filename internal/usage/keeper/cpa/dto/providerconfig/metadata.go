package providerconfig

import (
	"encoding/json"
	"fmt"
)

// ProviderMetadataConfig is an aggregated view of AI provider management configs, not a raw single-endpoint CPA response DTO.
type ProviderMetadataConfig struct {
	GeminiAPIKeys       []ProviderKeyConfig         `json:"gemini-api-key"`
	ClaudeAPIKeys       []ProviderKeyConfig         `json:"claude-api-key"`
	CodexAPIKeys        []ProviderKeyConfig         `json:"codex-api-key"`
	VertexAPIKeys       []ProviderKeyConfig         `json:"vertex-api-key"`
	OpenAICompatibility []OpenAICompatibilityConfig `json:"openai-compatibility"`
}

// ProviderKeyConfig is a compatibility-normalized view of gemini/claude/codex/vertex API-key config, supporting multiple CPA key names.
type ProviderKeyConfig struct {
	APIKey    string
	Prefix    string
	Name      string
	AuthIndex string
}

func (p *ProviderKeyConfig) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("decode provider key config: %w", err)
	}
	p.APIKey = firstString(raw, "apiKey", "api-key", "key")
	p.Prefix = firstString(raw, "prefix")
	p.Name = firstString(raw, "name")
	p.AuthIndex = firstString(raw, "auth-index", "auth_index", "authIndex")
	return nil
}

// OpenAICompatibilityConfig is a compatibility-normalized openai-compatibility provider config view, not identical to raw CPA JSON.
type OpenAICompatibilityConfig struct {
	Name          string
	Prefix        string
	APIKeyEntries []OpenAIApiKeyEntry
}

func (c *OpenAICompatibilityConfig) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("decode openai compatibility config: %w", err)
	}
	c.Name = firstString(raw, "name", "id")
	c.Prefix = firstString(raw, "prefix")
	c.APIKeyEntries = nil
	for _, key := range []string{"apiKeyEntries", "api-key-entries", "api-keys"} {
		value, ok := raw[key]
		if !ok {
			continue
		}
		entries, err := decodeOpenAIApiKeyEntries(value)
		if err != nil {
			return err
		}
		c.APIKeyEntries = entries
		break
	}
	return nil
}

// OpenAIApiKeyEntry is a compatibility-normalized openai-compatibility API-key entry supporting both string and object CPA shapes.
type OpenAIApiKeyEntry struct {
	APIKey    string
	AuthIndex string
}

func (e *OpenAIApiKeyEntry) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("decode openai api key entry: %w", err)
	}
	entry, err := decodeOpenAIApiKeyEntry(raw)
	if err != nil {
		return err
	}
	*e = entry
	return nil
}
