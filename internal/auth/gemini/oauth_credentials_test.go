package gemini

import "testing"

func TestOAuthClientCredentialsEnvironmentOverrides(t *testing.T) {
	t.Setenv("GEMINI_CLIENT_ID", "test-client-id")
	t.Setenv("GEMINI_CLIENT_SECRET", "test-client-secret")

	if got := OAuthClientID(); got != "test-client-id" {
		t.Fatalf("OAuthClientID() = %q, want environment override", got)
	}
	if got := OAuthClientSecret(); got != "test-client-secret" {
		t.Fatalf("OAuthClientSecret() = %q, want environment override", got)
	}
}

func TestOAuthClientCredentialsFallback(t *testing.T) {
	t.Setenv("GEMINI_CLIENT_ID", "")
	t.Setenv("GEMINI_CLIENT_SECRET", "")

	if OAuthClientID() == "" {
		t.Fatal("OAuthClientID() returned an empty fallback")
	}
	if OAuthClientSecret() == "" {
		t.Fatal("OAuthClientSecret() returned an empty fallback")
	}
}
