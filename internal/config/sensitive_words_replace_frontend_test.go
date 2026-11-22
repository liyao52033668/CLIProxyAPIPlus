package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// sensitiveWordsReplaceYAML mirrors what the management UI writes when an
// operator configures phrase -> replacement pairs for devin and antigravity.
const sensitiveWordsReplaceYAML = `
devin:
  sensitive-words:
    - "anthropic"
    - "claude"
  sensitive-words-replace:
    "anthropic": "vendor-a"
    "claude": "assistant-model"
antigravity:
  sensitive-words:
    - "openai"
  sensitive-words-replace:
    "openai": "vendor-b"
`

// TestFrontendSensitiveWordsReplaceRoundTrip verifies the mapping the UI emits
// parses into the backend fields and stays distinguishable from plain words.
func TestFrontendSensitiveWordsReplaceRoundTrip(t *testing.T) {
	var cfg Config
	if errUnmarshal := yaml.Unmarshal([]byte(sensitiveWordsReplaceYAML), &cfg); errUnmarshal != nil {
		t.Fatalf("unmarshal: %v", errUnmarshal)
	}

	if got := len(cfg.Devin.SensitiveWords); got != 2 {
		t.Fatalf("devin sensitive-words = %d, want 2", got)
	}
	if got := len(cfg.Devin.SensitiveWordsReplace); got != 2 {
		t.Fatalf("devin sensitive-words-replace = %d, want 2", got)
	}
	if got := cfg.Devin.SensitiveWordsReplace["anthropic"]; got != "vendor-a" {
		t.Fatalf("devin replacement[anthropic] = %q, want vendor-a", got)
	}

	if got := len(cfg.Antigravity.SensitiveWords); got != 1 {
		t.Fatalf("antigravity sensitive-words = %d, want 1", got)
	}
	if got := cfg.Antigravity.SensitiveWordsReplace["openai"]; got != "vendor-b" {
		t.Fatalf("antigravity replacement[openai] = %q, want vendor-b", got)
	}
}
