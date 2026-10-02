package codex

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRefreshTokensWithRetry_NonRetryableOnlyAttemptsOnce(t *testing.T) {
	var calls int32
	auth := &CodexAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				atomic.AddInt32(&calls, 1)
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Body:       io.NopCloser(strings.NewReader(`{"error":"invalid_grant","code":"refresh_token_reused"}`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		},
	}

	_, err := auth.RefreshTokensWithRetry(context.Background(), "dummy_refresh_token", 3)
	if err == nil {
		t.Fatalf("expected error for non-retryable refresh failure")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "refresh_token_reused") {
		t.Fatalf("expected refresh_token_reused in error, got: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 refresh attempt, got %d", got)
	}
}

func TestNewCodexAuthWithProxyURL_OverrideDirectDisablesProxy(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: "http://proxy.example.com:8080"}}
	auth := NewCodexAuthWithProxyURL(cfg, "direct")

	transport, ok := auth.httpClient.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("expected http.Transport, got %T", auth.httpClient.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestNewCodexAuthWithProxyURL_OverrideProxyTakesPrecedence(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: "http://global.example.com:8080"}}
	auth := NewCodexAuthWithProxyURL(cfg, "http://override.example.com:8081")

	transport, ok := auth.httpClient.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("expected http.Transport, got %T", auth.httpClient.Transport)
	}
	req, errReq := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errReq != nil {
		t.Fatalf("new request: %v", errReq)
	}
	proxyURL, errProxy := transport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("proxy func: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://override.example.com:8081" {
		t.Fatalf("proxy URL = %v, want http://override.example.com:8081", proxyURL)
	}
}

func TestRefreshTokens_PlanTypeDefaultsToFreeWhenMissing(t *testing.T) {
	idTokenWithoutPlan := makeTestJWT(map[string]any{
		"email": "user@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acc-12345",
		},
	})

	auth := &CodexAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				body := `{"access_token":"at-1","refresh_token":"rt-1","id_token":"` + idTokenWithoutPlan + `","token_type":"Bearer","expires_in":3600}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		},
	}

	tokenData, errRefresh := auth.RefreshTokens(context.Background(), "dummy-refresh")
	if errRefresh != nil {
		t.Fatalf("RefreshTokens failed: %v", errRefresh)
	}
	if tokenData == nil {
		t.Fatal("tokenData is nil")
	}
	if got := tokenData.PlanType; got != "free" {
		t.Fatalf("tokenData.PlanType = %q, want free", got)
	}
}

func TestRefreshTokens_ExtractsPlanTypeWhenPresent(t *testing.T) {
	idTokenWithPlan := makeTestJWT(map[string]any{
		"email": "user@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acc-12345",
			"chatgpt_plan_type":  "pro",
		},
	})

	auth := &CodexAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				body := `{"access_token":"at-1","refresh_token":"rt-1","id_token":"` + idTokenWithPlan + `","token_type":"Bearer","expires_in":3600}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		},
	}

	tokenData, errRefresh := auth.RefreshTokens(context.Background(), "dummy-refresh")
	if errRefresh != nil {
		t.Fatalf("RefreshTokens failed: %v", errRefresh)
	}
	if tokenData == nil {
		t.Fatal("tokenData is nil")
	}
	if got := tokenData.PlanType; got != "pro" {
		t.Fatalf("tokenData.PlanType = %q, want pro", got)
	}
}

func TestCreateAndUpdateTokenStorage_PlanType(t *testing.T) {
	auth := NewCodexAuth(nil)

	// Test CreateTokenStorage with missing plan type
	bundleDefault := &CodexAuthBundle{
		TokenData: CodexTokenData{
			IDToken:     "id-tok",
			AccessToken: "acc-tok",
		},
	}
	storage := auth.CreateTokenStorage(bundleDefault)
	if storage.PlanType != "free" {
		t.Fatalf("storage.PlanType = %q, want free", storage.PlanType)
	}

	// Test UpdateTokenStorage with plan type
	auth.UpdateTokenStorage(storage, &CodexTokenData{
		IDToken:     "id-tok-2",
		AccessToken: "acc-tok-2",
		PlanType:    "team",
	})
	if storage.PlanType != "team" {
		t.Fatalf("updated storage.PlanType = %q, want team", storage.PlanType)
	}

	// Test UpdateTokenStorage with empty plan type defaults to free
	auth.UpdateTokenStorage(storage, &CodexTokenData{
		IDToken:     "id-tok-3",
		AccessToken: "acc-tok-3",
		PlanType:    "",
	})
	if storage.PlanType != "free" {
		t.Fatalf("updated storage.PlanType = %q, want free", storage.PlanType)
	}
}
