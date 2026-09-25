package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	freebuffauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/freebuff"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestFreebuffCredentialFileNamePrefersEmail(t *testing.T) {
	if got := freebuffCredentialFileName("uid", "user@example.com"); got != "freebuff-user_example_com.json" {
		t.Fatalf("file name = %q, want email-derived name", got)
	}
	if got := freebuffCredentialFileName("uid-42", ""); got != "freebuff-uid-42.json" {
		t.Fatalf("file name = %q, want uid-derived name", got)
	}
	if got := freebuffCredentialFileName("", ""); got != "freebuff.json" {
		t.Fatalf("file name = %q, want default name", got)
	}
}

func TestBuildFreebuffAuthRecordMetadata(t *testing.T) {
	record := buildFreebuffAuthRecord(" fb-token ", "https://www.codebuff.com", "user@example.com", "User", "")
	if record.Provider != "freebuff" {
		t.Fatalf("provider = %q, want freebuff", record.Provider)
	}
	if record.FileName != "freebuff-user_example_com.json" {
		t.Fatalf("file name = %q, want email-derived name", record.FileName)
	}
	if got, _ := record.Metadata["access_token"].(string); got != "fb-token" {
		t.Fatalf("access_token = %#v, want trimmed token", got)
	}
	if got := record.Metadata["email"]; got != "user@example.com" {
		t.Fatalf("email = %#v, want account email", got)
	}
	if got := record.Metadata["login_method"]; got != "device_flow" {
		t.Fatalf("login_method = %#v, want device_flow", got)
	}
	// The default upstream must not be persisted as a custom base_url.
	if _, exists := record.Metadata["base_url"]; exists {
		t.Fatalf("base_url = %#v, want absent for default upstream", record.Metadata["base_url"])
	}

	custom := buildFreebuffAuthRecord("fb-token", "https://mirror.example.com", "", "", "")
	if got, _ := custom.Metadata["base_url"].(string); got != "https://mirror.example.com" {
		t.Fatalf("base_url = %#v, want custom upstream preserved", custom.Metadata["base_url"])
	}
	// Without an email the file name falls back to the token-hash uid.
	if custom.FileName == "" || custom.FileName == "freebuff-user_example_com.json" || custom.FileName == "freebuff.json" {
		t.Fatalf("file name = %q, want uid-derived name", custom.FileName)
	}
}

func TestRequestFreebuffTokenStartsDeviceFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Swap the device-flow hooks under the hook mutex: the background poll
	// goroutine may still be running when cleanup restores the originals.
	freebuffOAuthHooksMu.Lock()
	originalRequestCode := freebuffRequestLoginCodeFn
	originalPoll := freebuffPollLoginStatusFn
	freebuffRequestLoginCodeFn = func(_ context.Context, _ *http.Client, _ string, fingerprintID string) (*freebuffauth.LoginCode, error) {
		return &freebuffauth.LoginCode{
			FingerprintID:   fingerprintID,
			LoginURL:        "https://www.codebuff.com/login?fixture=1",
			FingerprintHash: "fixture-hash",
			ExpiresAt:       time.Now().Add(10 * time.Minute).UnixMilli(),
		}, nil
	}
	// Stay pending until the test cancels the session; never touch the network.
	freebuffPollLoginStatusFn = func(_ context.Context, _ *http.Client, _ string, _ *freebuffauth.LoginCode) (*freebuffauth.LoginUser, bool, error) {
		return nil, true, nil
	}
	freebuffOAuthHooksMu.Unlock()
	t.Cleanup(func() {
		freebuffOAuthHooksMu.Lock()
		defer freebuffOAuthHooksMu.Unlock()
		freebuffRequestLoginCodeFn = originalRequestCode
		freebuffPollLoginStatusFn = originalPoll
	})

	store := &memoryAuthStore{}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, coreauth.NewManager(nil, nil, nil))
	h.tokenStore = store

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/freebuff-auth-url?is_webui=true", nil)

	h.RequestFreebuffToken(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := resp["status"]; got != "ok" {
		t.Fatalf("status = %#v, want ok", got)
	}
	state, _ := resp["state"].(string)
	if state == "" {
		t.Fatal("state = empty, want an OAuth session state")
	}
	loginURL, _ := resp["url"].(string)
	if loginURL == "" {
		t.Fatal("url = empty, want a browser login URL")
	}
	// The pending session must exist so the background poll loop and the UI
	// status endpoint agree on the provider.
	if !IsOAuthSessionPending(state, "freebuff") {
		t.Fatalf("session state %q not pending for freebuff", state)
	}
	CancelOAuthSession(state)
}
