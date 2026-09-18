package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	lobsterauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/lobsterai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// stubLobsterAIService replaces the upstream client for offline handler tests.
type stubLobsterAIService struct {
	loginURL    string
	payload     *lobsterauth.TokenPayload
	exchangeErr error
	usage       *lobsterauth.Usage
	usageErr    error
	gotCode     string
	gotUUID     string
}

func (s *stubLobsterAIService) BuildLoginURL(redirectURI, state string) string {
	if s.loginURL != "" {
		return s.loginURL
	}
	return "https://lobsterai.youdao.com/portal/#/login?source=electron&redirect_uri=" + redirectURI + "&state=" + state
}

func (s *stubLobsterAIService) ExchangeCode(_ context.Context, code, installationUUID string) (*lobsterauth.TokenPayload, error) {
	s.gotCode = code
	s.gotUUID = installationUUID
	if s.exchangeErr != nil {
		return nil, s.exchangeErr
	}
	return s.payload, nil
}

func (s *stubLobsterAIService) FetchUsage(_ context.Context, _ string) (*lobsterauth.Usage, error) {
	if s.usageErr != nil {
		return nil, s.usageErr
	}
	return s.usage, nil
}

func TestLobsterAIOAuthProviderIsNormalized(t *testing.T) {
	cases := map[string]string{
		"lobsterai": "lobsterai",
		"LobsterAI": "lobsterai",
		"lobster":   "lobsterai",
		"youdao":    "lobsterai",
	}
	for input, want := range cases {
		got, errNormalize := NormalizeOAuthProvider(input)
		if errNormalize != nil {
			t.Fatalf("NormalizeOAuthProvider(%q): %v", input, errNormalize)
		}
		if got != want {
			t.Fatalf("NormalizeOAuthProvider(%q) = %q, want %q", input, got, want)
		}
	}
	if _, errNormalize := NormalizeOAuthProvider("unknown-provider"); errNormalize == nil {
		t.Fatal("NormalizeOAuthProvider accepted an unknown provider")
	}
}

func TestRequestLobsterAITokenReturnsLoginURL(t *testing.T) {
	gin.SetMode(gin.TestMode)

	stub := &stubLobsterAIService{}
	originalFactory := newLobsterAIOAuthService
	newLobsterAIOAuthService = func(*config.Config) lobsterAIOAuthService { return stub }
	t.Cleanup(func() { newLobsterAIOAuthService = originalFactory })

	handler := &Handler{cfg: &config.Config{Port: 8317, AuthDir: t.TempDir()}}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/lobsterai-auth-url", nil)

	handler.RequestLobsterAIToken(ctx)

	// Complete the session first so the background waiter stops on its next poll.
	var body struct {
		Status string `json:"status"`
		URL    string `json:"url"`
		State  string `json:"state"`
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &body); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	defer CompleteOAuthSession(body.State)

	if body.Status != "ok" {
		t.Fatalf("status field = %q, want ok", body.Status)
	}
	if body.URL == "" || body.State == "" {
		t.Fatalf("response missing url/state: %s", recorder.Body.String())
	}
	// The state must be registered and pending so the callback can complete it.
	if !IsOAuthSessionPending(body.State, lobsterAIProvider) {
		t.Fatal("oauth session was not registered as pending")
	}
	// The login URL must carry the loopback redirect the portal validates.
	for _, want := range []string{"redirect_uri=", "/auth/callback", "state=" + body.State} {
		if !strings.Contains(body.URL, want) {
			t.Fatalf("login url = %q, want it to contain %q", body.URL, want)
		}
	}
}

func TestLobsterAIOAuthServiceFactoryCreatesService(t *testing.T) {
	service := newLobsterAIOAuthService(&config.Config{})
	if service == nil {
		t.Fatal("factory returned a nil service")
	}
}

func TestLobsterAICallbackFileWritesOnlyForPendingSession(t *testing.T) {
	authDir := t.TempDir()

	// An unknown state must be rejected: no credential file may be created.
	if _, errWrite := WriteOAuthCallbackFileForPendingSession(authDir, "lobsterai", "unknown-state", "code-1", ""); errWrite == nil {
		t.Fatal("WriteOAuthCallbackFileForPendingSession accepted an unknown session")
	}

	state := "lobsterai-test-state"
	RegisterOAuthSession(state, lobsterAIProvider)
	t.Cleanup(func() { CompleteOAuthSession(state) })

	if _, errWrite := WriteOAuthCallbackFileForPendingSession(authDir, "lobsterai", state, "code-1", ""); errWrite != nil {
		t.Fatalf("WriteOAuthCallbackFileForPendingSession: %v", errWrite)
	}
	payload, errWait := waitForOAuthCallbackFile(authDir, lobsterAIProvider, state, 0)
	if errWait != nil {
		t.Fatalf("waitForOAuthCallbackFile: %v", errWait)
	}
	if payload.Code != "code-1" || payload.State != state {
		t.Fatalf("callback payload = %#v, want code-1 for the registered state", payload)
	}
	if errValidate := validateOAuthCallbackPayload(lobsterAIProvider, state, payload, true); errValidate != nil {
		t.Fatalf("validateOAuthCallbackPayload: %v", errValidate)
	}
}

func TestLobsterAICallbackRejectsStateMismatch(t *testing.T) {
	state := "lobsterai-state-a"
	RegisterOAuthSession(state, lobsterAIProvider)
	t.Cleanup(func() { CompleteOAuthSession(state) })

	payload := &oauthCallbackPayload{Code: "code-1", State: "different-state"}
	if errValidate := validateOAuthCallbackPayload(lobsterAIProvider, state, payload, true); errValidate == nil {
		t.Fatal("validateOAuthCallbackPayload accepted a mismatched state")
	}
}

// TestPostLobsterAIOAuthCallbackAcceptsRedirectURL covers the remote-deployment
// path: the pasted callback URL carries the code and state for a pending login.
func TestPostLobsterAIOAuthCallbackAcceptsRedirectURL(t *testing.T) {
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	handler := &Handler{cfg: &config.Config{Port: 8317, AuthDir: authDir}}

	state := "lobsterai-remote-state"
	RegisterOAuthSession(state, lobsterAIProvider)
	t.Cleanup(func() { CompleteOAuthSession(state) })

	body := `{"provider":"lobsterai","redirect_url":"http://127.0.0.1:8317/auth/callback?code=code-1&state=` + state + `"}`
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/oauth-callback", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")

	handler.PostOAuthCallback(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	// The provider alias must normalize and the callback file must be written.
	path := filepath.Join(authDir, ".oauth-lobsterai-"+state+".oauth")
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("callback file was not written: %v", errRead)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	var payload map[string]string
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
		t.Fatalf("decode callback file: %v", errUnmarshal)
	}
	if payload["code"] != "code-1" {
		t.Fatalf("callback code = %q, want code-1", payload["code"])
	}
}
