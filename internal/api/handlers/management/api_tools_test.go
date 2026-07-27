package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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

type staticAPICallResolver map[string][]string

func (r staticAPICallResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	values, ok := r[host]
	if !ok {
		return nil, fmt.Errorf("host not found: %s", host)
	}
	addresses := make([]net.IPAddr, 0, len(values))
	for _, value := range values {
		addresses = append(addresses, net.IPAddr{IP: net.ParseIP(value)})
	}
	return addresses, nil
}

func mustParseAPICallURL(t *testing.T, rawURL string) *url.URL {
	t.Helper()
	parsed, errParse := url.Parse(rawURL)
	if errParse != nil {
		t.Fatalf("url.Parse(%q): %v", rawURL, errParse)
	}
	return parsed
}

func TestValidateAPICallURLRejectsUnsafeTargets(t *testing.T) {
	t.Parallel()

	resolver := staticAPICallResolver{
		"public.example":  {"93.184.216.34"},
		"private.example": {"10.0.0.1"},
		"mixed.example":   {"93.184.216.34", "192.168.1.5"},
	}
	cases := []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		{name: "public HTTPS", rawURL: "https://public.example/v1"},
		{name: "HTTP scheme", rawURL: "http://public.example/v1", wantErr: true},
		{name: "userinfo", rawURL: "https://user@public.example/v1", wantErr: true},
		{name: "loopback literal", rawURL: "https://127.0.0.1/v1", wantErr: true},
		{name: "metadata literal", rawURL: "https://169.254.169.254/latest/meta-data", wantErr: true},
		{name: "private DNS", rawURL: "https://private.example/v1", wantErr: true},
		{name: "mixed DNS", rawURL: "https://mixed.example/v1", wantErr: true},
		{name: "localhost name", rawURL: "https://localhost/v1", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			errValidate := validateAPICallURL(context.Background(), mustParseAPICallURL(t, tc.rawURL), resolver)
			if tc.wantErr && errValidate == nil {
				t.Fatal("validateAPICallURL returned nil, want error")
			}
			if !tc.wantErr && errValidate != nil {
				t.Fatalf("validateAPICallURL returned error: %v", errValidate)
			}
		})
	}
}

func TestTrustedAPICallTokenDestination(t *testing.T) {
	t.Parallel()

	h := &Handler{}
	cases := []struct {
		name   string
		auth   *coreauth.Auth
		rawURL string
		want   bool
	}{
		{
			name:   "provider host",
			auth:   &coreauth.Auth{Provider: "claude"},
			rawURL: "https://api.anthropic.com/api/oauth/usage",
			want:   true,
		},
		{
			name:   "qoder openapi host",
			auth:   &coreauth.Auth{Provider: "qoder"},
			rawURL: "https://openapi.qoder.sh/api/v2/quota/usage",
			want:   true,
		},
		{
			name:   "qoder unrelated host not trusted",
			auth:   &coreauth.Auth{Provider: "qoder"},
			rawURL: "https://api3.qoder.sh/api/v2/model/list",
		},
		{
			name:   "provider subdomain not trusted",
			auth:   &coreauth.Auth{Provider: "claude"},
			rawURL: "https://evil.api.anthropic.com/v1",
		},
		{
			name:   "unrelated host",
			auth:   &coreauth.Auth{Provider: "claude"},
			rawURL: "https://attacker.example/v1",
		},
		{
			name: "configured base URL",
			auth: &coreauth.Auth{
				Provider:   "custom",
				Attributes: map[string]string{"base_url": "https://gateway.example:8443/api"},
			},
			rawURL: "https://gateway.example:8443/v1/models",
			want:   true,
		},
		{
			name: "configured base URL different port",
			auth: &coreauth.Auth{
				Provider:   "custom",
				Attributes: map[string]string{"base_url": "https://gateway.example:8443/api"},
			},
			rawURL: "https://gateway.example/v1/models",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := h.isTrustedAPICallTokenDestination(mustParseAPICallURL(t, tc.rawURL), tc.auth); got != tc.want {
				t.Fatalf("isTrustedAPICallTokenDestination() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestAPICallRedirectPolicyRevalidatesTargets(t *testing.T) {
	t.Parallel()

	resolver := staticAPICallResolver{
		"api.anthropic.com": {"160.79.104.10"},
		"public.example":    {"93.184.216.34"},
		"private.example":   {"10.0.0.1"},
	}
	initialURL := mustParseAPICallURL(t, "https://api.anthropic.com/v1")
	cases := []struct {
		name          string
		rawURL        string
		tokenInjected bool
		wantErr       bool
	}{
		{name: "same origin with token", rawURL: "https://api.anthropic.com/v2", tokenInjected: true},
		{name: "cross origin without token", rawURL: "https://public.example/v2"},
		{name: "cross origin with token", rawURL: "https://public.example/v2", tokenInjected: true, wantErr: true},
		{name: "private target", rawURL: "https://private.example/v2", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodGet, tc.rawURL, nil)
			errRedirect := newAPICallRedirectPolicy(initialURL, tc.tokenInjected, resolver)(request, nil)
			if tc.wantErr && errRedirect == nil {
				t.Fatal("redirect policy returned nil, want error")
			}
			if !tc.wantErr && errRedirect != nil {
				t.Fatalf("redirect policy returned error: %v", errRedirect)
			}
		})
	}
}

func TestAPICallRejectsUnsafeRequestOptions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	resolver := staticAPICallResolver{"public.example": {"93.184.216.34"}}
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "claude:test",
		Provider: "claude",
		Metadata: map[string]any{"access_token": "test-token"},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	authIndex := auth.EnsureIndex()

	cases := []struct {
		name string
		body apiCallRequest
	}{
		{
			name: "HTTP target",
			body: apiCallRequest{Method: http.MethodGet, URL: "http://public.example/v1"},
		},
		{
			name: "private target",
			body: apiCallRequest{Method: http.MethodGet, URL: "https://169.254.169.254/latest/meta-data"},
		},
		{
			name: "Host override",
			body: apiCallRequest{
				Method: http.MethodGet,
				URL:    "https://public.example/v1",
				Header: map[string]string{"Host": "internal.example"},
			},
		},
		{
			name: "token without auth",
			body: apiCallRequest{
				Method: http.MethodGet,
				URL:    "https://public.example/v1",
				Header: map[string]string{"Authorization": "Bearer $TOKEN$"},
			},
		},
		{
			name: "token to untrusted host",
			body: apiCallRequest{
				AuthIndexSnake: &authIndex,
				Method:         http.MethodGet,
				URL:            "https://public.example/v1",
				Header:         map[string]string{"Authorization": "Bearer $TOKEN$"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, errMarshal := json.Marshal(tc.body)
			if errMarshal != nil {
				t.Fatalf("marshal request: %v", errMarshal)
			}
			req := httptest.NewRequest(http.MethodPost, "/v0/management/api-call", strings.NewReader(string(payload)))
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = req

			h := &Handler{authManager: manager, apiCallResolver: resolver}
			h.APICall(ctx)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
		})
	}
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

func TestReadAPICallBodyRejectsOversizedResponse(t *testing.T) {
	_, errRead := readAPICallBody(repeatingByteReader{}, maxAPICallResponseBodyBytes)
	if !errors.Is(errRead, errAPICallBodyTooLarge) {
		t.Fatalf("readAPICallBody error = %v, want %v", errRead, errAPICallBodyTooLarge)
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

func TestParseCopilotUsageBodyPaidPlanUsesSnapshots(t *testing.T) {
	t.Parallel()

	// Pro/Business/Enterprise return native quota_snapshots; limited_user_quotas is absent.
	body := []byte(`{
		"access_type_sku": "free_limited_copilot",
		"chat_enabled": true,
		"copilot_plan": "individual",
		"quota_reset_date": "2026-08-01",
		"quota_reset_date_utc": "2026-08-01T00:00:00Z",
		"quota_snapshots": {
			"chat": {
				"entitlement": 1000,
				"percent_remaining": 0.58,
				"quota_id": "chat",
				"quota_remaining": 580,
				"remaining": 580,
				"unlimited": false
			},
			"completions": {
				"entitlement": 500,
				"percent_remaining": 0.871,
				"quota_id": "completions",
				"quota_remaining": 435.5,
				"remaining": 435.5,
				"unlimited": false
			},
			"premium_interactions": {
				"entitlement": 300,
				"percent_remaining": 0.5,
				"quota_id": "premium_interactions",
				"quota_remaining": 150,
				"remaining": 150,
				"unlimited": false
			}
		}
	}`)

	usage, err := parseCopilotUsageBody(body)
	if err != nil {
		t.Fatalf("parseCopilotUsageBody: %v", err)
	}
	if usage.CopilotPlan != "individual" {
		t.Fatalf("CopilotPlan = %q, want individual", usage.CopilotPlan)
	}
	if usage.QuotaResetDate != "2026-08-01T00:00:00Z" {
		t.Fatalf("QuotaResetDate = %q, want UTC reset date", usage.QuotaResetDate)
	}
	if usage.QuotaSnapshots.PremiumInteractions.Entitlement != 300 {
		t.Fatalf("premium entitlement = %v, want 300", usage.QuotaSnapshots.PremiumInteractions.Entitlement)
	}
	if usage.QuotaSnapshots.PremiumInteractions.Remaining != 150 {
		t.Fatalf("premium remaining = %v, want 150", usage.QuotaSnapshots.PremiumInteractions.Remaining)
	}
	if usage.QuotaSnapshots.PremiumInteractions.Usage != 150 {
		t.Fatalf("premium usage = %v, want 150", usage.QuotaSnapshots.PremiumInteractions.Usage)
	}
	if usage.QuotaSnapshots.Chat.Usage != 420 {
		t.Fatalf("chat usage = %v, want 420", usage.QuotaSnapshots.Chat.Usage)
	}
}

func TestParseCopilotUsageBodyFreePlanUsesLimitedQuotas(t *testing.T) {
	t.Parallel()

	// Free plan meters chat/completions via limited_user_quotas + monthly_quotas.
	body := []byte(`{
		"access_type_sku": "free_limited_copilot",
		"chat_enabled": true,
		"copilot_plan": "free",
		"limited_user_reset_date": "2026-08-15",
		"monthly_quotas": {
			"chat": 50,
			"completions": 2000
		},
		"limited_user_quotas": {
			"chat": 12,
			"completions": 1500
		}
	}`)

	usage, err := parseCopilotUsageBody(body)
	if err != nil {
		t.Fatalf("parseCopilotUsageBody: %v", err)
	}
	if usage.CopilotPlan != "free" {
		t.Fatalf("CopilotPlan = %q, want free", usage.CopilotPlan)
	}
	if usage.QuotaResetDate != "2026-08-15" {
		t.Fatalf("QuotaResetDate = %q, want limited_user_reset_date", usage.QuotaResetDate)
	}
	if usage.QuotaSnapshots.Chat.Entitlement != 50 || usage.QuotaSnapshots.Chat.Remaining != 12 {
		t.Fatalf("chat snapshot = %+v, want entitlement=50 remaining=12", usage.QuotaSnapshots.Chat)
	}
	if usage.QuotaSnapshots.Chat.Usage != 38 {
		t.Fatalf("chat usage = %v, want 38", usage.QuotaSnapshots.Chat.Usage)
	}
	if usage.QuotaSnapshots.Completions.Entitlement != 2000 || usage.QuotaSnapshots.Completions.Remaining != 1500 {
		t.Fatalf("completions snapshot = %+v", usage.QuotaSnapshots.Completions)
	}
	if !usage.QuotaSnapshots.PremiumInteractions.Unlimited {
		t.Fatal("expected free premium interactions to be marked unlimited")
	}
}

func TestParseCopilotUsageBodyNegativeRemaining(t *testing.T) {
	t.Parallel()

	// Live Free/individual accounts can report remaining=-1 after overshooting chat quota.
	body := []byte(`{
		"copilot_plan": "individual",
		"quota_reset_date_utc": "2026-08-01T00:00:00.000Z",
		"quota_snapshots": {
			"chat": {
				"entitlement": 200,
				"remaining": -1,
				"quota_remaining": -1,
				"percent_remaining": 0.0,
				"quota_id": "chat",
				"unlimited": false
			},
			"completions": {
				"entitlement": 2000,
				"remaining": 1959,
				"quota_remaining": 1959,
				"percent_remaining": 97.9,
				"quota_id": "completions",
				"unlimited": false
			},
			"premium_interactions": {
				"entitlement": 0,
				"remaining": 0,
				"percent_remaining": 0.0,
				"quota_id": "premium_interactions",
				"unlimited": false
			}
		}
	}`)

	usage, err := parseCopilotUsageBody(body)
	if err != nil {
		t.Fatalf("parseCopilotUsageBody: %v", err)
	}
	if usage.QuotaSnapshots.Chat.Usage != 200 {
		t.Fatalf("chat usage = %v, want 200 for remaining=-1", usage.QuotaSnapshots.Chat.Usage)
	}
	if usage.QuotaSnapshots.Chat.PercentRemaining != 0 {
		t.Fatalf("chat percent_remaining = %v, want 0", usage.QuotaSnapshots.Chat.PercentRemaining)
	}
	if usage.QuotaSnapshots.Completions.PercentRemaining != 97.9 {
		t.Fatalf("completions percent_remaining = %v, want 97.9", usage.QuotaSnapshots.Completions.PercentRemaining)
	}
	if usage.QuotaSnapshots.Completions.Usage != 41 {
		t.Fatalf("completions usage = %v, want 41", usage.QuotaSnapshots.Completions.Usage)
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
