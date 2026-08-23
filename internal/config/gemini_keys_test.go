package config

import "testing"

func TestSanitizeGeminiKeysPreservesBaseURLOnlyCredentials(t *testing.T) {
	cfg := &Config{GeminiKey: []GeminiKey{
		{BaseURL: " https://example.test/v1 "},
		{},
		{APIKey: " key ", BaseURL: " https://other.test "},
	}}

	cfg.SanitizeGeminiKeys()
	if len(cfg.GeminiKey) != 2 {
		t.Fatalf("GeminiKey count = %d, want 2", len(cfg.GeminiKey))
	}
	if got := cfg.GeminiKey[0].BaseURL; got != "https://example.test/v1" {
		t.Fatalf("base URL-only credential was not normalized: %q", got)
	}
	if got := cfg.GeminiKey[1].APIKey; got != "key" {
		t.Fatalf("API key was not normalized: %q", got)
	}
}
