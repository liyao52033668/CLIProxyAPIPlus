package test

import (
	"context"
	"encoding/json"
	"testing"

	qoderauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/cpa/dto/apicall"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/quota"
)

func TestQoderProviderCallsQuotaUsageRequest(t *testing.T) {
	body := `{"userId":"u-1","userType":"personal_standard","usageType":"credits","totalUsagePercentage":42.5,"isQuotaExceeded":false,"expiresAt":1893456000000,"upgradeUrl":"https://qoder.com/pricing?client=qoder","userQuota":{"total":300,"used":127.5,"remaining":172.5,"percentage":42.5,"unit":"credits"},"isPlanQuotaProrated":false}`
	caller := &recordingManagementCaller{responses: []*apicall.Response{{
		StatusCode: 200,
		BodyText:   body,
		Body:       json.RawMessage(body),
	}}}
	provider := quota.NewQoderProvider(caller, quota.DefaultProviderConfigs().Qoder)

	output, err := provider.Check(context.Background(), quota.ProviderInput{Identity: entities.UsageIdentity{Identity: "qoder-auth"}})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if output.Provider != "qoder" {
		t.Fatalf("expected qoder output provider, got %q", output.Provider)
	}
	result, ok := output.Result.(quota.QoderResult)
	if !ok {
		t.Fatalf("expected qoder result type, got %T", output.Result)
	}
	if result.Usage == nil || result.Usage.UserType != "personal_standard" || result.Usage.UsageType != "credits" || result.Usage.IsQuotaExceeded || result.Usage.UserQuota == nil || result.Usage.UserQuota.Total != 300 || result.Usage.UserQuota.Used != 127.5 || result.Usage.UserQuota.Remaining != 172.5 {
		t.Fatalf("expected parsed qoder usage payload, got %#v", result.Usage)
	}
	encoded, err := json.Marshal(output.Result)
	if err != nil {
		t.Fatalf("marshal qoder result: %v", err)
	}
	encodedBody := string(encoded)
	if !contains(encodedBody, `"usage":{"userId"`) || contains(encodedBody, "bodyText") || contains(encodedBody, "statusCode") {
		t.Fatalf("unexpected qoder result JSON: %s", encodedBody)
	}
	if len(caller.requests) != 1 {
		t.Fatalf("expected one api-call request, got %d", len(caller.requests))
	}
	request := caller.requests[0]
	if request.AuthIndex != "qoder-auth" || request.Method != "GET" || request.URL != "https://openapi.qoder.sh/api/v2/quota/usage" {
		t.Fatalf("unexpected api-call request: %+v", request)
	}
	wantUA := "qoder/" + qoderauth.GetCosyVersion()
	if request.Header["Authorization"] != "Bearer $TOKEN$" || request.Header["Accept"] != "application/json" || request.Header["User-Agent"] != wantUA {
		t.Fatalf("unexpected api-call headers: %+v, want User-Agent %q", request.Header, wantUA)
	}
	if request.Data != nil {
		t.Fatalf("expected no data body, got %#v", request.Data)
	}
}

func TestQoderProviderReturnsTargetErrorMessage(t *testing.T) {
	caller := &recordingManagementCaller{responses: []*apicall.Response{{
		StatusCode: 401,
		BodyText:   `{"error":"unauthorized"}`,
		Body:       json.RawMessage(`{"error":"unauthorized"}`),
	}}}
	provider := quota.NewQoderProvider(caller, quota.DefaultProviderConfigs().Qoder)

	_, err := provider.Check(context.Background(), quota.ProviderInput{Identity: entities.UsageIdentity{Identity: "qoder-auth"}})
	if err == nil {
		t.Fatal("expected quota check error")
	}
	if !contains(err.Error(), "401") {
		t.Fatalf("expected status in error, got %v", err)
	}
}
