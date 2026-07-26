package management

import (
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestAuthenticateManagementKey_LocalhostIPBan_BlocksCorrectKeyDuringBan(t *testing.T) {
	h := &Handler{
		cfg:            &config.Config{},
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}

	for i := 0; i < 5; i++ {
		allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "wrong-secret")
		if allowed {
			t.Fatalf("expected auth to be denied at attempt %d", i+1)
		}
		if statusCode != http.StatusUnauthorized || errMsg != "invalid management key" {
			t.Fatalf("unexpected auth failure at attempt %d: status=%d msg=%q", i+1, statusCode, errMsg)
		}
	}

	allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret")
	if allowed {
		t.Fatalf("expected correct key to be denied while banned")
	}
	if statusCode != http.StatusForbidden {
		t.Fatalf("expected forbidden status while banned, got %d", statusCode)
	}
	if !strings.HasPrefix(errMsg, "IP banned due to too many failed attempts. Try again in") {
		t.Fatalf("unexpected banned message: %q", errMsg)
	}
}

func TestAuthenticateManagementKey_EnvSecretDoesNotOverrideAllowRemote(t *testing.T) {
	const envSecret = "env-management-secret"

	t.Run("remote denied when AllowRemote false even with correct envSecret", func(t *testing.T) {
		h := &Handler{
			cfg: &config.Config{
				RemoteManagement: config.RemoteManagement{
					AllowRemote: false,
				},
			},
			failedAttempts: make(map[string]*attemptInfo),
			envSecret:      envSecret,
		}

		allowed, statusCode, errMsg := h.AuthenticateManagementKey("203.0.113.10", false, envSecret)
		if allowed {
			t.Fatal("expected remote auth to be denied when AllowRemote is false")
		}
		if statusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", statusCode, http.StatusForbidden)
		}
		if errMsg != "remote management disabled" {
			t.Fatalf("errMsg = %q, want %q", errMsg, "remote management disabled")
		}
	})

	t.Run("remote allowed when AllowRemote true with correct envSecret", func(t *testing.T) {
		h := &Handler{
			cfg: &config.Config{
				RemoteManagement: config.RemoteManagement{
					AllowRemote: true,
				},
			},
			failedAttempts: make(map[string]*attemptInfo),
			envSecret:      envSecret,
		}

		allowed, statusCode, errMsg := h.AuthenticateManagementKey("203.0.113.10", false, envSecret)
		if !allowed {
			t.Fatalf("expected remote auth to succeed: status=%d msg=%q", statusCode, errMsg)
		}
		if statusCode != 0 {
			t.Fatalf("status = %d, want 0", statusCode)
		}
		if errMsg != "" {
			t.Fatalf("errMsg = %q, want empty", errMsg)
		}
	})

	t.Run("localhost allowed with correct envSecret when AllowRemote false", func(t *testing.T) {
		h := &Handler{
			cfg: &config.Config{
				RemoteManagement: config.RemoteManagement{
					AllowRemote: false,
				},
			},
			failedAttempts: make(map[string]*attemptInfo),
			envSecret:      envSecret,
		}

		allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, envSecret)
		if !allowed {
			t.Fatalf("expected localhost auth to succeed: status=%d msg=%q", statusCode, errMsg)
		}
		if statusCode != 0 {
			t.Fatalf("status = %d, want 0", statusCode)
		}
		if errMsg != "" {
			t.Fatalf("errMsg = %q, want empty", errMsg)
		}
	})
}

func TestManagementRequestClientIP_UsesRemoteAddrNotForwardedHeaders(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		wantIP     string
		wantLocal  bool
	}{
		{name: "ipv4 loopback", remoteAddr: "127.0.0.1:54321", wantIP: "127.0.0.1", wantLocal: true},
		{name: "ipv6 loopback", remoteAddr: "[::1]:54321", wantIP: "::1", wantLocal: true},
		{name: "remote ipv4", remoteAddr: "203.0.113.10:443", wantIP: "203.0.113.10", wantLocal: false},
		{name: "raw ipv4 without port", remoteAddr: "127.0.0.1", wantIP: "127.0.0.1", wantLocal: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := &http.Request{
				RemoteAddr: tc.remoteAddr,
				Header:     http.Header{"X-Forwarded-For": []string{"127.0.0.1"}},
			}
			gotIP, gotLocal := managementRequestClientIP(req)
			if gotIP != tc.wantIP {
				t.Fatalf("clientIP = %q, want %q", gotIP, tc.wantIP)
			}
			if gotLocal != tc.wantLocal {
				t.Fatalf("localClient = %v, want %v", gotLocal, tc.wantLocal)
			}
		})
	}
}
