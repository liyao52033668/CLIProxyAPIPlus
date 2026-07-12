package helps

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestJoinBaseURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		base, path, want string
	}{
		{"https://api.example.com/", "/v1/chat", "https://api.example.com/v1/chat"},
		{"https://api.example.com", "v1/chat", "https://api.example.com/v1/chat"},
		{"https://api.example.com///", "/v1/chat", "https://api.example.com/v1/chat"},
		{"", "/v1/chat", "/v1/chat"},
		{"https://api.example.com", "", "https://api.example.com"},
	}
	for _, tc := range cases {
		if got := JoinBaseURL(tc.base, tc.path); got != tc.want {
			t.Fatalf("JoinBaseURL(%q, %q) = %q, want %q", tc.base, tc.path, got, tc.want)
		}
	}
}

func TestAuthLogFields(t *testing.T) {
	t.Parallel()

	id, label, authType, authValue := AuthLogFields(nil)
	if id != "" || label != "" || authType != "" || authValue != "" {
		t.Fatalf("AuthLogFields(nil) = (%q,%q,%q,%q), want empty", id, label, authType, authValue)
	}

	auth := &cliproxyauth.Auth{
		ID:    "auth-1",
		Label: "primary",
		Attributes: map[string]string{
			"api_key": "sk-test",
		},
	}
	id, label, authType, authValue = AuthLogFields(auth)
	if id != "auth-1" || label != "primary" {
		t.Fatalf("AuthLogFields id/label = %q/%q", id, label)
	}
	if authType == "" {
		t.Fatalf("expected non-empty auth type")
	}
	_ = authValue
}

func TestReadHTTPErrorBody(t *testing.T) {
	t.Parallel()

	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(`{"error":"bad"}`)),
	}
	body := ReadHTTPErrorBody(context.Background(), nil, resp)
	if string(body) != `{"error":"bad"}` {
		t.Fatalf("body = %q", body)
	}
}
