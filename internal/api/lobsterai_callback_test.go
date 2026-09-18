package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	managementHandlers "github.com/router-for-me/CLIProxyAPI/v7/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
)

// lobsterAICallbackEngine builds a minimal engine carrying only the callback
// middleware plus a fallthrough handler standing in for the Amp proxy.
func lobsterAICallbackEngine(authDir string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(lobsterAICallbackMiddleware(authDir))
	engine.Any("/auth/*path", func(c *gin.Context) {
		c.String(http.StatusTeapot, "amp-passthrough")
	})
	return engine
}

func TestLobsterAICallbackMiddlewareWritesPendingSession(t *testing.T) {
	authDir := t.TempDir()
	engine := lobsterAICallbackEngine(authDir)

	state := "lobsterai-middleware-state"
	managementHandlers.RegisterOAuthSession(state, constant.LobsterAI)
	t.Cleanup(func() { managementHandlers.CompleteOAuthSession(state) })

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/auth/callback?code=code-1&state="+state, nil)
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	payload := readLobsterAICallbackFile(t, authDir, state)
	if payload["code"] != "code-1" {
		t.Fatalf("callback code = %q, want code-1", payload["code"])
	}
	if payload["state"] != state {
		t.Fatalf("callback state = %q, want %q", payload["state"], state)
	}
}

// readLobsterAICallbackFile reads and removes the callback file the middleware
// writes for a pending LobsterAI session.
func readLobsterAICallbackFile(t *testing.T, authDir, state string) map[string]string {
	t.Helper()
	path := filepath.Join(authDir, ".oauth-lobsterai-"+state+".oauth")
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read callback file %s: %v", path, errRead)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	var payload map[string]string
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
		t.Fatalf("decode callback file: %v", errUnmarshal)
	}
	return payload
}

func TestLobsterAICallbackMiddlewarePassesThroughOtherRequests(t *testing.T) {
	authDir := t.TempDir()
	engine := lobsterAICallbackEngine(authDir)

	cases := []string{
		// No pending session for this state: Amp's own OAuth flow must handle it.
		"/auth/callback?code=code-1&state=amp-state",
		// A callback without a code or error is not ours.
		"/auth/callback?state=lobsterai-middleware-state",
		// Different paths under /auth must never be intercepted.
		"/auth/cli-login",
		"/auth/sign-in",
	}
	for _, target := range cases {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, target, nil)
		engine.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusTeapot {
			t.Fatalf("request %q status = %d, want the Amp passthrough (418)", target, recorder.Code)
		}
	}

	entries, errRead := os.ReadDir(authDir)
	if errRead != nil && !os.IsNotExist(errRead) {
		t.Fatalf("read auth dir: %v", errRead)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".oauth" {
			t.Fatalf("passthrough requests produced a callback file %q", entry.Name())
		}
	}
}

func TestLobsterAICallbackMiddlewareRejectsNonGET(t *testing.T) {
	authDir := t.TempDir()
	engine := lobsterAICallbackEngine(authDir)

	state := "lobsterai-post-state"
	managementHandlers.RegisterOAuthSession(state, constant.LobsterAI)
	t.Cleanup(func() { managementHandlers.CompleteOAuthSession(state) })

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/auth/callback?code=code-1&state="+state, nil)
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want the Amp passthrough (418) for a non-GET callback", recorder.Code)
	}
}

func TestLobsterAICallbackMiddlewareRouteRegistersWithoutConflict(t *testing.T) {
	// The whole point of middleware interception is that the Amp module owns
	// /auth/*; building the real server proves no wildcard conflict exists.
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	server := newTestServer(t)
	if server.engine == nil {
		t.Fatal("server engine was not initialised")
	}

	// The management login route must be registered next to the other providers.
	want := http.MethodGet + " /v0/management/lobsterai-auth-url"
	for _, route := range server.engine.Routes() {
		if route.Method+" "+route.Path == want {
			return
		}
	}
	t.Fatalf("route %q was not registered", want)
}
