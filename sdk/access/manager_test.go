package access

import (
	"context"
	"net/http"
	"testing"
)

func TestManagerAuthenticateRejectsEmptyProviderSet(t *testing.T) {
	manager := NewManager()
	req, errRequest := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/v1/models", nil)
	if errRequest != nil {
		t.Fatalf("create request: %v", errRequest)
	}

	result, authErr := manager.Authenticate(req.Context(), req)
	if result != nil {
		t.Fatalf("result = %#v, want nil", result)
	}
	if !IsAuthErrorCode(authErr, AuthErrorCodeNoCredentials) {
		t.Fatalf("auth error = %#v, want no_credentials", authErr)
	}
	if got := authErr.HTTPStatusCode(); got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", got, http.StatusUnauthorized)
	}
}
