package configaccess

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

func TestProviderRejectsQueryAPIKeyByDefault(t *testing.T) {
	p := newProvider("test", []string{"secret-key"}, false)
	req := httptest.NewRequest(http.MethodGet, "/v1/models?key=secret-key", nil)

	result, authErr := p.Authenticate(context.Background(), req)

	if result != nil {
		t.Fatalf("result = %#v, want nil", result)
	}
	if !sdkaccess.IsAuthErrorCode(authErr, sdkaccess.AuthErrorCodeNoCredentials) {
		t.Fatalf("auth error = %#v, want no credentials", authErr)
	}
}

func TestProviderAllowsQueryAPIKeyWhenEnabled(t *testing.T) {
	p := newProvider("test", []string{"secret-key"}, true)
	req := httptest.NewRequest(http.MethodGet, "/v1/models?key=secret-key", nil)

	result, authErr := p.Authenticate(context.Background(), req)

	if authErr != nil {
		t.Fatalf("Authenticate returned error: %v", authErr)
	}
	if result == nil || result.Principal != "secret-key" {
		t.Fatalf("result = %#v, want matching principal", result)
	}
	if result.Metadata["source"] != "query-key" {
		t.Fatalf("source = %q, want query-key", result.Metadata["source"])
	}
}

func TestProviderAllowsQueryAuthTokenWhenEnabled(t *testing.T) {
	p := newProvider("test", []string{"secret-key"}, true)
	req := httptest.NewRequest(http.MethodGet, "/v1/models?auth_token=secret-key", nil)

	result, authErr := p.Authenticate(context.Background(), req)

	if authErr != nil {
		t.Fatalf("Authenticate returned error: %v", authErr)
	}
	if result == nil || result.Metadata["source"] != "query-auth-token" {
		t.Fatalf("result = %#v, want query-auth-token source", result)
	}
}

func TestProviderHeaderAuthenticationWorksWhenQueryDisabled(t *testing.T) {
	p := newProvider("test", []string{"secret-key"}, false)
	req := httptest.NewRequest(http.MethodGet, "/v1/models?key=ignored-key", nil)
	req.Header.Set("Authorization", "Bearer secret-key")

	result, authErr := p.Authenticate(context.Background(), req)

	if authErr != nil {
		t.Fatalf("Authenticate returned error: %v", authErr)
	}
	if result == nil || result.Metadata["source"] != "authorization" {
		t.Fatalf("result = %#v, want authorization source", result)
	}
}
