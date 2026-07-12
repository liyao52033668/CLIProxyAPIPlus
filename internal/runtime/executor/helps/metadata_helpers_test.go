package helps

import (
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type stringerValue string

func (s stringerValue) String() string { return string(s) }

func TestAttributeString(t *testing.T) {
	t.Parallel()

	if got := AttributeString(nil, "api_key"); got != "" {
		t.Fatalf("AttributeString(nil) = %q, want empty", got)
	}
	if got := AttributeString(map[string]string{"api_key": "  key  "}, "api_key"); got != "key" {
		t.Fatalf("AttributeString trimmed = %q, want key", got)
	}
	if got := AttributeString(map[string]string{"api_key": "key"}, ""); got != "" {
		t.Fatalf("AttributeString empty key = %q, want empty", got)
	}
}

func TestMetadataString(t *testing.T) {
	t.Parallel()

	meta := map[string]any{
		"empty":         "   ",
		"access_token":  " token ",
		"refresh_token": []byte(" refresh "),
		"label":         stringerValue(" label "),
	}

	if got := MetadataString(nil, "access_token"); got != "" {
		t.Fatalf("MetadataString(nil) = %q, want empty", got)
	}
	if got := MetadataString(meta); got != "" {
		t.Fatalf("MetadataString no keys = %q, want empty", got)
	}
	if got := MetadataString(meta, "missing", "empty", "access_token"); got != "token" {
		t.Fatalf("MetadataString fallback = %q, want token", got)
	}
	if got := MetadataString(meta, "refresh_token"); got != "refresh" {
		t.Fatalf("MetadataString []byte = %q, want refresh", got)
	}
	if got := MetadataString(meta, "label"); got != "label" {
		t.Fatalf("MetadataString Stringer = %q, want label", got)
	}
}

func TestResolveAPIKeyAndBaseURL(t *testing.T) {
	t.Parallel()

	t.Run("nil auth", func(t *testing.T) {
		t.Parallel()
		apiKey, baseURL := ResolveAPIKeyAndBaseURL(nil)
		if apiKey != "" || baseURL != "" {
			t.Fatalf("got (%q, %q), want empty", apiKey, baseURL)
		}
	})

	t.Run("attributes win and are trimmed", func(t *testing.T) {
		t.Parallel()
		auth := &cliproxyauth.Auth{
			Attributes: map[string]string{
				"api_key":  "  attr-key  ",
				"base_url": "  https://example.test  ",
			},
			Metadata: map[string]any{
				"access_token": "meta-token",
				"base_url":     "https://meta.test",
			},
		}
		apiKey, baseURL := ResolveAPIKeyAndBaseURL(auth)
		if apiKey != "attr-key" {
			t.Fatalf("apiKey = %q, want attr-key", apiKey)
		}
		if baseURL != "https://example.test" {
			t.Fatalf("baseURL = %q, want https://example.test", baseURL)
		}
	})

	t.Run("metadata fallbacks", func(t *testing.T) {
		t.Parallel()
		auth := &cliproxyauth.Auth{
			Metadata: map[string]any{
				"access_token": "meta-token",
				"base_url":     " https://meta.test ",
			},
		}
		apiKey, baseURL := ResolveAPIKeyAndBaseURL(auth)
		if apiKey != "meta-token" {
			t.Fatalf("apiKey = %q, want meta-token", apiKey)
		}
		if baseURL != "https://meta.test" {
			t.Fatalf("baseURL = %q, want https://meta.test", baseURL)
		}
	})

	t.Run("custom metadata api key keys", func(t *testing.T) {
		t.Parallel()
		auth := &cliproxyauth.Auth{
			Metadata: map[string]any{
				"api_key":      "iflow-key",
				"access_token": "ignored",
				"base_url":     "https://iflow.test",
			},
		}
		apiKey, baseURL := ResolveAPIKeyAndBaseURL(auth, "api_key")
		if apiKey != "iflow-key" {
			t.Fatalf("apiKey = %q, want iflow-key", apiKey)
		}
		if baseURL != "https://iflow.test" {
			t.Fatalf("baseURL = %q, want https://iflow.test", baseURL)
		}
	})
}
