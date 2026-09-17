package api

import (
	"net/http"
	"testing"
)

func TestOAuthSessionCancelRouteRegistered(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	server := newTestServer(t)

	for _, route := range server.engine.Routes() {
		if route.Method == http.MethodDelete && route.Path == "/v0/management/oauth-session" {
			return
		}
	}
	t.Fatal("oauth session cancel route not registered")
}
