package helps

import (
	"testing"

	kimiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kimi"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestResolveKimiChatURL(t *testing.T) {
	tests := []struct {
		name string
		auth *cliproxyauth.Auth
		want string
	}{
		{
			name: "nil auth",
			auth: nil,
			want: kimiauth.KimiAPIBaseURL + "/v1/chat/completions",
		},
		{
			name: "kimi.ai auth by provider",
			auth: &cliproxyauth.Auth{Provider: "kimi-ai"},
			want: kimiauth.KimiAIAPIBaseURL + "/v1/chat/completions",
		},
		{
			name: "kimi.ai auth by domain attribute",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"domain": "kimi.ai"}},
			want: kimiauth.KimiAIAPIBaseURL + "/v1/chat/completions",
		},
		{
			name: "base_url without v1",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.ai/coding"}},
			want: "https://api.kimi.ai/coding/v1/chat/completions",
		},
		{
			name: "base_url with trailing v1",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.ai/coding/v1"}},
			want: "https://api.kimi.ai/coding/v1/chat/completions",
		},
		{
			name: "base_url with trailing slash after v1",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.ai/coding/v1/"}},
			want: "https://api.kimi.ai/coding/v1/chat/completions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveKimiChatURL(tt.auth)
			if got != tt.want {
				t.Fatalf("ResolveKimiChatURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveKimiClaudeBaseURL(t *testing.T) {
	tests := []struct {
		name string
		auth *cliproxyauth.Auth
		want string
	}{
		{
			name: "nil auth",
			auth: nil,
			want: kimiauth.KimiAPIBaseURL,
		},
		{
			name: "kimi.ai auth by provider",
			auth: &cliproxyauth.Auth{Provider: "kimi-ai"},
			want: kimiauth.KimiAIAPIBaseURL,
		},
		{
			name: "base_url with v1",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.ai/coding/v1"}},
			want: "https://api.kimi.ai/coding",
		},
		{
			name: "base_url without v1",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.ai/coding"}},
			want: "https://api.kimi.ai/coding",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveKimiClaudeBaseURL(tt.auth)
			if got != tt.want {
				t.Fatalf("ResolveKimiClaudeBaseURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveKimiBaseURL(t *testing.T) {
	tests := []struct {
		name string
		auth *cliproxyauth.Auth
		want string
	}{
		{
			name: "nil auth",
			auth: nil,
			want: kimiauth.KimiAPIBaseURL,
		},
		{
			name: "base_url attribute wins",
			auth: &cliproxyauth.Auth{
				Provider:   "kimi-ai",
				Attributes: map[string]string{"base_url": "https://custom.example.com/coding/"},
			},
			want: "https://custom.example.com/coding",
		},
		{
			name: "metadata base_url wins over provider",
			auth: &cliproxyauth.Auth{
				Provider: "kimi",
				Metadata: map[string]any{"base_url": "https://api.kimi.ai/coding"},
			},
			want: "https://api.kimi.ai/coding",
		},
		{
			name: "kimi.com provider",
			auth: &cliproxyauth.Auth{Provider: "kimi"},
			want: kimiauth.KimiAPIBaseURL,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveKimiBaseURL(tt.auth)
			if got != tt.want {
				t.Fatalf("ResolveKimiBaseURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
