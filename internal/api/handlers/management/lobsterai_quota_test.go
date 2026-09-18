package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	lobsterauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/lobsterai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// newLobsterAIQuotaHandler registers the given auth records on a real manager so
// the lookup path under test matches production.
func newLobsterAIQuotaHandler(t *testing.T, records ...*coreauth.Auth) *Handler {
	t.Helper()
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	for _, record := range records {
		if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
			t.Fatalf("register auth %q: %v", record.ID, errRegister)
		}
	}
	return NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
}

func TestBuildLobsterAIQuotaPayloadRendersCreditsAndItems(t *testing.T) {
	auth := &coreauth.Auth{ID: "lobsterai-1", FileName: "lobsterai-user.json", Provider: lobsterAIProvider}
	usage := &lobsterauth.Usage{
		CreditsRemaining:  5297.72,
		CycleCreditsLimit: 5000,
		CycleCreditsUsed:  200,
		PlanName:          "Standard",
		Subscription:      "active",
		Items: []lobsterauth.CreditItem{
			{Type: "subscription", Label: "标准版", LabelEn: "Standard", CreditsRemaining: 4800, ExpiresAt: "2026-12-01T00:00:00Z"},
			{Type: "campaign", Label: "活动赠送", LabelEn: "Campaign", CreditsRemaining: 497.72},
		},
	}

	payload := buildLobsterAIQuotaPayload(usage, auth)

	if payload["credits_remaining"] != 5297.72 {
		t.Fatalf("credits_remaining = %#v, want 5297.72", payload["credits_remaining"])
	}
	// Cycle counters must not be presented as a limit on credits_remaining: they
	// exclude campaign grants, so a consumer combining the two would render two
	// conflicting balances (the bug this separation fixes).
	if payload["cycle_credits_limit"] != 5000.0 || payload["cycle_credits_used"] != 200.0 {
		t.Fatalf("cycle counters = %#v/%#v, want 5000/200", payload["cycle_credits_limit"], payload["cycle_credits_used"])
	}
	if _, exists := payload["credits_limit"]; exists {
		t.Fatalf("credits_limit = %#v, want the ambiguous key absent", payload["credits_limit"])
	}
	if payload["plan_name"] != "Standard" || payload["subscription_status"] != "active" {
		t.Fatalf("unexpected plan fields %#v", payload)
	}
	if payload["auth_name"] != "lobsterai-user.json" {
		t.Fatalf("auth_name = %#v, want the file name", payload["auth_name"])
	}

	items, ok := payload["items"].([]gin.H)
	if !ok {
		t.Fatalf("items type = %T, want []gin.H", payload["items"])
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	if items[0]["type"] != "subscription" || items[0]["credits_remaining"] != 4800.0 {
		t.Fatalf("unexpected first item %#v", items[0])
	}
	// A bucket without an expiry must omit the field rather than send an empty value.
	if _, exists := items[1]["expires_at"]; exists {
		t.Fatalf("second item expires_at = %#v, want the field omitted", items[1]["expires_at"])
	}
}

func TestBuildLobsterAIQuotaPayloadHandlesNilUsage(t *testing.T) {
	payload := buildLobsterAIQuotaPayload(nil, &coreauth.Auth{ID: "lobsterai-2", FileName: "lobsterai.json"})
	if _, exists := payload["credits_remaining"]; exists {
		t.Fatalf("credits_remaining = %#v, want it omitted for a nil snapshot", payload["credits_remaining"])
	}
	if payload["auth_name"] != "lobsterai.json" {
		t.Fatalf("auth_name = %#v, want the file name", payload["auth_name"])
	}
}

func TestFindLobsterAIAuthPrefersIndexAndIgnoresOtherProviders(t *testing.T) {
	target := &coreauth.Auth{ID: "target.json", FileName: "target.json", Provider: lobsterAIProvider}
	other := &coreauth.Auth{ID: "other.json", FileName: "other.json", Provider: "devin"}
	handler := newLobsterAIQuotaHandler(t, target, other)

	target.EnsureIndex()
	other.EnsureIndex()

	if got := handler.findLobsterAIAuth(target.Index); got == nil || got.ID != target.ID {
		t.Fatalf("findLobsterAIAuth(index) = %#v, want the target credential", got)
	}
	// Another provider's credential must never satisfy a LobsterAI lookup.
	if got := handler.findLobsterAIAuth(other.Index); got != nil {
		t.Fatalf("findLobsterAIAuth(other provider index) = %#v, want nil", got)
	}
	// With no index the first enabled LobsterAI credential is used.
	if got := handler.findLobsterAIAuth(""); got == nil || got.ID != target.ID {
		t.Fatalf("findLobsterAIAuth(\"\") = %#v, want the target credential", got)
	}
	if got := handler.findLobsterAIAuth("missing-index"); got != nil {
		t.Fatalf("findLobsterAIAuth(missing) = %#v, want nil", got)
	}
}

func TestGetLobsterAIQuotaReportsMissingCredential(t *testing.T) {
	handler := newLobsterAIQuotaHandler(t)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/lobsterai-quota?auth_index=none", nil)

	handler.GetLobsterAIQuota(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "no lobsterai credential found") {
		t.Fatalf("body = %s, want a missing-credential error", recorder.Body.String())
	}
}

// TestGetLobsterAIQuotaFetchesUsage exercises the handler against a fixture
// upstream so the two-endpoint merge and payload shapes stay covered.
func TestGetLobsterAIQuotaFetchesUsage(t *testing.T) {
	allowLobsterAIQuotaLoopbackForTest(t)

	accessToken := strings.Join([]string{"fixture", "quota-access"}, "-")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+accessToken {
			t.Errorf("upstream Authorization = %q, want the credential token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/profile-summary":
			_, _ = w.Write([]byte(`{"code":0,"data":{"totalCreditsRemaining":42.5,"creditItems":[{"type":"free","label":"免费额度","labelEn":"Free","creditsRemaining":42.5}]}}`))
		case "/api/user/quota":
			_, _ = w.Write([]byte(`{"code":0,"data":{"planName":"Standard","subscriptionStatus":"active","monthlyCreditsLimit":100,"monthlyCreditsUsed":57.5}}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	auth := &coreauth.Auth{
		ID:       "lobsterai-quota.json",
		FileName: "lobsterai-quota.json",
		Provider: lobsterAIProvider,
		Metadata: map[string]any{
			"access_token": accessToken,
			"base_url":     server.URL,
		},
	}
	auth.EnsureIndex()
	handler := newLobsterAIQuotaHandler(t, auth)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/lobsterai-quota?auth_index="+auth.Index, nil)

	handler.GetLobsterAIQuota(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	if payload["credits_remaining"] != 42.5 {
		t.Fatalf("credits_remaining = %#v, want 42.5", payload["credits_remaining"])
	}
	if payload["cycle_credits_limit"] != 100.0 || payload["cycle_credits_used"] != 57.5 {
		t.Fatalf("cycle limit/used = %#v/%#v, want 100/57.5", payload["cycle_credits_limit"], payload["cycle_credits_used"])
	}
	if payload["plan_name"] != "Standard" {
		t.Fatalf("plan_name = %#v, want Standard", payload["plan_name"])
	}
	items, ok := payload["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items = %#v, want one credit bucket", payload["items"])
	}
}

// TestGetLobsterAIQuotaSurfacesExpiredCredential pins the status mapping for a
// rejected token so the panel can prompt a re-login.
func TestGetLobsterAIQuotaSurfacesExpiredCredential(t *testing.T) {
	allowLobsterAIQuotaLoopbackForTest(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":40100,"message":"登录已过期，请重新登录","data":null}`))
	}))
	defer server.Close()

	auth := &coreauth.Auth{
		ID:       "lobsterai-expired.json",
		Provider: lobsterAIProvider,
		Metadata: map[string]any{
			"access_token": strings.Join([]string{"fixture", "expired"}, "-"),
			"base_url":     server.URL,
		},
	}
	handler := newLobsterAIQuotaHandler(t, auth)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/lobsterai-quota", nil)

	handler.GetLobsterAIQuota(ctx)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", recorder.Code, recorder.Body.String())
	}
}

// TestGetLobsterAIQuotaRejectsUnsafeBaseURL pins the SSRF boundary: production
// validation must reject a credential pointing at a loopback address.
func TestGetLobsterAIQuotaRejectsUnsafeBaseURL(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "lobsterai-unsafe.json",
		Provider: lobsterAIProvider,
		Metadata: map[string]any{
			"access_token": strings.Join([]string{"fixture", "unsafe"}, "-"),
			"base_url":     "http://127.0.0.1:9000",
		},
	}
	handler := newLobsterAIQuotaHandler(t, auth)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/lobsterai-quota", nil)

	handler.GetLobsterAIQuota(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "base url is not allowed") {
		t.Fatalf("body = %s, want a rejected base url error", recorder.Body.String())
	}
}

// allowLobsterAIQuotaLoopbackForTest relaxes the SSRF guard so an httptest server
// can stand in for the upstream. Production validation has its own coverage.
func allowLobsterAIQuotaLoopbackForTest(t *testing.T) {
	t.Helper()
	original := lobsterAIQuotaBaseURLValidator
	lobsterAIQuotaBaseURLValidator = func(raw string) (string, error) {
		return strings.TrimRight(strings.TrimSpace(raw), "/"), nil
	}
	t.Cleanup(func() { lobsterAIQuotaBaseURLValidator = original })
}
