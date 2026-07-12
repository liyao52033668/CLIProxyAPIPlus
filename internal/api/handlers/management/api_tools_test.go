package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type repeatingByteReader struct{}

func (repeatingByteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestAPICallRejectsOversizedJSONRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{}
	payload := `{"method":"POST","url":"https://example.test","data":"` + strings.Repeat("x", (8<<20)+1) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v0/management/api-call", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = req

	h.APICall(ctx)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusRequestEntityTooLarge, recorder.Body.String())
	}
}

func TestAPICallRejectsOversizedJSONRequestWithTrailingWhitespace(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{}
	payload := `{"method":"GET","url":"https://example.test"}` + strings.Repeat(" ", 8<<20)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/api-call", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = req

	h.APICall(ctx)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusRequestEntityTooLarge, recorder.Body.String())
	}
}

func TestAPICallRejectsOversizedUpstreamResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.CopyN(w, repeatingByteReader{}, (32<<20)+1)
	}))
	defer upstream.Close()

	h := &Handler{}
	payload, errMarshal := json.Marshal(apiCallRequest{Method: http.MethodGet, URL: upstream.URL})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	req := httptest.NewRequest(http.MethodPost, "/v0/management/api-call", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = req

	h.APICall(ctx)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	if !strings.Contains(recorder.Body.String(), "response too large") {
		t.Fatalf("body = %s, want response too large", recorder.Body.String())
	}
}

func resetManagementTransportCacheForTest() {
	managementTransportCacheMutex.Lock()
	defer managementTransportCacheMutex.Unlock()
	managementTransportCache = make(map[string]http.RoundTripper)
}

func TestAPICallTransportDirectBypassesGlobalProxy(t *testing.T) {
	resetManagementTransportCacheForTest()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "direct"})
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", httpTransport)
	}
	if httpTransport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestAPICallTransportInvalidAuthFallsBackToGlobalProxy(t *testing.T) {
	resetManagementTransportCacheForTest()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "bad-value"})
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", httpTransport)
	}

	req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errRequest != nil {
		t.Fatalf("http.NewRequest returned error: %v", errRequest)
	}

	proxyURL, errProxy := httpTransport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://global-proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://global-proxy.example.com:8080", proxyURL)
	}
}

func TestAPICallTransportAPIKeyAuthFallsBackToConfigProxyURL(t *testing.T) {
	resetManagementTransportCacheForTest()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
			GeminiKey: []config.GeminiKey{{
				APIKey:   "gemini-key",
				ProxyURL: "http://gemini-proxy.example.com:8080",
			}},
			ClaudeKey: []config.ClaudeKey{{
				APIKey:   "claude-key",
				ProxyURL: "http://claude-proxy.example.com:8080",
			}},
			CodexKey: []config.CodexKey{{
				APIKey:   "codex-key",
				ProxyURL: "http://codex-proxy.example.com:8080",
			}},
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:    "bohe",
				BaseURL: "https://bohe.example.com",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
					APIKey:   "compat-key",
					ProxyURL: "http://compat-proxy.example.com:8080",
				}},
			}},
		},
	}

	cases := []struct {
		name      string
		auth      *coreauth.Auth
		wantProxy string
	}{
		{
			name: "gemini",
			auth: &coreauth.Auth{
				Provider:   "gemini",
				Attributes: map[string]string{"api_key": "gemini-key"},
			},
			wantProxy: "http://gemini-proxy.example.com:8080",
		},
		{
			name: "claude",
			auth: &coreauth.Auth{
				Provider:   "claude",
				Attributes: map[string]string{"api_key": "claude-key"},
			},
			wantProxy: "http://claude-proxy.example.com:8080",
		},
		{
			name: "codex",
			auth: &coreauth.Auth{
				Provider:   "codex",
				Attributes: map[string]string{"api_key": "codex-key"},
			},
			wantProxy: "http://codex-proxy.example.com:8080",
		},
		{
			name: "openai-compatibility",
			auth: &coreauth.Auth{
				Provider: "bohe",
				Attributes: map[string]string{
					"api_key":      "compat-key",
					"compat_name":  "bohe",
					"provider_key": "bohe",
				},
			},
			wantProxy: "http://compat-proxy.example.com:8080",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			transport := h.apiCallTransport(tc.auth)
			httpTransport, ok := transport.(*http.Transport)
			if !ok {
				t.Fatalf("transport type = %T, want *http.Transport", httpTransport)
			}

			req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
			if errRequest != nil {
				t.Fatalf("http.NewRequest returned error: %v", errRequest)
			}

			proxyURL, errProxy := httpTransport.Proxy(req)
			if errProxy != nil {
				t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
			}
			if proxyURL == nil || proxyURL.String() != tc.wantProxy {
				t.Fatalf("proxy URL = %v, want %s", proxyURL, tc.wantProxy)
			}
		})
	}
}

func TestGetOrBuildManagementTransportCapsCacheSize(t *testing.T) {
	resetManagementTransportCacheForTest()

	for i := 0; i < 130; i++ {
		proxyURL := fmt.Sprintf("http://proxy-%d.example.com:8080", i)
		transport := getOrBuildManagementTransport(proxyURL)
		if transport == nil {
			t.Fatalf("getOrBuildManagementTransport(%q) returned nil", proxyURL)
		}
	}

	managementTransportCacheMutex.RLock()
	defer managementTransportCacheMutex.RUnlock()
	if got := len(managementTransportCache); got > 128 {
		t.Fatalf("cache size = %d, want <= 128", got)
	}
}

func TestAuthByIndexDistinguishesSharedAPIKeysAcrossProviders(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	geminiAuth := &coreauth.Auth{
		ID:       "gemini:apikey:123",
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key": "shared-key",
		},
	}
	compatAuth := &coreauth.Auth{
		ID:       "openai-compatibility:bohe:456",
		Provider: "bohe",
		Label:    "bohe",
		Attributes: map[string]string{
			"api_key":      "shared-key",
			"compat_name":  "bohe",
			"provider_key": "bohe",
		},
	}

	if _, errRegister := manager.Register(context.Background(), geminiAuth); errRegister != nil {
		t.Fatalf("register gemini auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), compatAuth); errRegister != nil {
		t.Fatalf("register compat auth: %v", errRegister)
	}

	geminiIndex := geminiAuth.EnsureIndex()
	compatIndex := compatAuth.EnsureIndex()
	if geminiIndex == compatIndex {
		t.Fatalf("shared api key produced duplicate auth_index %q", geminiIndex)
	}

	h := &Handler{authManager: manager}

	gotGemini := h.authByIndex(geminiIndex)
	if gotGemini == nil {
		t.Fatal("expected gemini auth by index")
	}
	if gotGemini.ID != geminiAuth.ID {
		t.Fatalf("authByIndex(gemini) returned %q, want %q", gotGemini.ID, geminiAuth.ID)
	}

	gotCompat := h.authByIndex(compatIndex)
	if gotCompat == nil {
		t.Fatal("expected compat auth by index")
	}
	if gotCompat.ID != compatAuth.ID {
		t.Fatalf("authByIndex(compat) returned %q, want %q", gotCompat.ID, compatAuth.ID)
	}
}

func TestCopilotUsageResponseJSONParsing(t *testing.T) {
	t.Parallel()

	jsonData := `{
		"access_type_sku": "business",
		"analytics_tracking_id": "12345",
		"assigned_date": "2024-01-15",
		"can_signup_for_limited": true,
		"chat_enabled": true,
		"copilot_plan": "business",
		"quota_reset_date": "2024-02-01",
		"quota_snapshots": {
			"chat": {
				"entitlement": 1000,
				"overage_count": 0,
				"overage_permitted": false,
				"percent_remaining": 0.58,
				"quota_id": "chat",
				"quota_remaining": 580,
				"remaining": 580,
				"unlimited": false
			},
			"completions": {
				"entitlement": 500,
				"overage_count": 0,
				"overage_permitted": false,
				"percent_remaining": 0.871,
				"quota_id": "completions",
				"quota_remaining": 435.5,
				"remaining": 435.5,
				"unlimited": false
			},
			"premium_interactions": {
				"entitlement": 50,
				"overage_count": 0,
				"overage_permitted": false,
				"percent_remaining": 1.0,
				"quota_id": "premium_interactions",
				"quota_remaining": 50,
				"remaining": 50,
				"unlimited": false
			}
		}
	}`

	var response CopilotUsageResponse
	if err := json.Unmarshal([]byte(jsonData), &response); err != nil {
		t.Fatalf("failed to unmarshal CopilotUsageResponse: %v", err)
	}

	if response.AccessTypeSKU != "business" {
		t.Errorf("AccessTypeSKU = %q, want %q", response.AccessTypeSKU, "business")
	}
	if response.CopilotPlan != "business" {
		t.Errorf("CopilotPlan = %q, want %q", response.CopilotPlan, "business")
	}
	if !response.ChatEnabled {
		t.Error("ChatEnabled = false, want true")
	}

	if response.QuotaSnapshots.Chat.Entitlement != 1000 {
		t.Errorf("Chat.Entitlement = %v, want 1000", response.QuotaSnapshots.Chat.Entitlement)
	}
	if response.QuotaSnapshots.Chat.Remaining != 580 {
		t.Errorf("Chat.Remaining = %v, want 580", response.QuotaSnapshots.Chat.Remaining)
	}
	if response.QuotaSnapshots.Chat.PercentRemaining != 0.58 {
		t.Errorf("Chat.PercentRemaining = %v, want 0.58", response.QuotaSnapshots.Chat.PercentRemaining)
	}

	if response.QuotaSnapshots.Completions.Entitlement != 500 {
		t.Errorf("Completions.Entitlement = %v, want 500", response.QuotaSnapshots.Completions.Entitlement)
	}
	if response.QuotaSnapshots.Completions.Remaining != 435.5 {
		t.Errorf("Completions.Remaining = %v, want 435.5", response.QuotaSnapshots.Completions.Remaining)
	}
	if response.QuotaSnapshots.Completions.PercentRemaining != 0.871 {
		t.Errorf("Completions.PercentRemaining = %v, want 0.871", response.QuotaSnapshots.Completions.PercentRemaining)
	}

	if response.QuotaSnapshots.PremiumInteractions.Entitlement != 50 {
		t.Errorf("PremiumInteractions.Entitlement = %v, want 50", response.QuotaSnapshots.PremiumInteractions.Entitlement)
	}
	if response.QuotaSnapshots.PremiumInteractions.Remaining != 50 {
		t.Errorf("PremiumInteractions.Remaining = %v, want 50", response.QuotaSnapshots.PremiumInteractions.Remaining)
	}
	if response.QuotaSnapshots.PremiumInteractions.PercentRemaining != 1.0 {
		t.Errorf("PremiumInteractions.PercentRemaining = %v, want 1.0", response.QuotaSnapshots.PremiumInteractions.PercentRemaining)
	}
}

func TestFindCopilotAuth(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)

	copilotAuth := &coreauth.Auth{
		ID:       "copilot:github:123",
		Provider: "copilot",
		Attributes: map[string]string{
			"token": "ghp_testtoken",
		},
	}
	githubAuth := &coreauth.Auth{
		ID:       "github:copilot:456",
		Provider: "github",
		Attributes: map[string]string{
			"token": "ghp_githubtoken",
		},
	}
	githubCopilotAuth := &coreauth.Auth{
		ID:       "github-copilot:789",
		Provider: "github-copilot",
		Attributes: map[string]string{
			"token": "ghp_githubcoptoken",
		},
	}
	otherAuth := &coreauth.Auth{
		ID:       "other:abc",
		Provider: "other",
		Attributes: map[string]string{
			"api_key": "other-key",
		},
	}

	if _, err := manager.Register(context.Background(), copilotAuth); err != nil {
		t.Fatalf("register copilot auth: %v", err)
	}
	if _, err := manager.Register(context.Background(), githubAuth); err != nil {
		t.Fatalf("register github auth: %v", err)
	}
	if _, err := manager.Register(context.Background(), githubCopilotAuth); err != nil {
		t.Fatalf("register github-copilot auth: %v", err)
	}
	if _, err := manager.Register(context.Background(), otherAuth); err != nil {
		t.Fatalf("register other auth: %v", err)
	}

	h := &Handler{authManager: manager}

	found := h.findCopilotAuth("")
	if found == nil {
		t.Fatal("expected to find first copilot auth when authIndex is empty")
	}
	if found.ID != copilotAuth.ID {
		t.Errorf("found auth ID = %q, want first registered copilot auth %q", found.ID, copilotAuth.ID)
	}

	copilotIndex := copilotAuth.EnsureIndex()
	foundByIndex := h.findCopilotAuth(copilotIndex)
	if foundByIndex == nil {
		t.Fatal("expected to find copilot auth by index")
	}
	if foundByIndex.ID != copilotAuth.ID {
		t.Errorf("foundByIndex ID = %q, want %q", foundByIndex.ID, copilotAuth.ID)
	}

	githubIndex := githubAuth.EnsureIndex()
	foundGithub := h.findCopilotAuth(githubIndex)
	if foundGithub == nil {
		t.Fatal("expected to find github auth by index")
	}
	if foundGithub.ID != githubAuth.ID {
		t.Errorf("foundGithub ID = %q, want %q", foundGithub.ID, githubAuth.ID)
	}
}

func TestGetCopilotQuotaNoAuth(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	h := &Handler{authManager: nil}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/copilot-quota", nil)

	h.GetCopilotQuota(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusBadRequest)
	}

	var response map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if response["error"] != "no github copilot credential found" {
		t.Errorf("error = %q, want %q", response["error"], "no github copilot credential found")
	}
}
