package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func writeMetaResponsesOK(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	payload, _ := json.Marshal(map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":     "resp_1",
			"object": "response",
			"status": "completed",
			"model":  "muse-spark-1.3",
			"output": []map[string]any{
				{
					"type": "message",
					"role": "assistant",
					"content": []map[string]any{
						{"type": "output_text", "text": text},
					},
				},
			},
			"usage": map[string]any{
				"input_tokens":  1,
				"output_tokens": 1,
				"total_tokens":  2,
			},
		},
	})
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(payload)
	_, _ = w.Write([]byte("\n\n"))
}

func TestMetaExecutor_PrepareRequest_ClientIdHeader_Issue6117(t *testing.T) {
	exec := NewMetaExecutor(&config.Config{})
	req, errReq := http.NewRequest(http.MethodPost, "https://api.meta.ai/responses", nil)
	if errReq != nil {
		t.Fatal(errReq)
	}
	auth := &cliproxyauth.Auth{
		Provider: "meta",
		Attributes: map[string]string{
			"api_key": "test-key",
		},
	}
	if errPrepare := exec.PrepareRequest(req, auth); errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := req.Header.Get("X-Client-Id"); got != "tbh:tui" {
		t.Errorf("X-Client-Id = %q, want %q", got, "tbh:tui")
	}
	if _, exists := req.Header["X-Client-Id:"]; exists {
		t.Errorf("X-Client-Id: with trailing colon must not exist in headers")
	}
}

func TestMetaExecutor_ApplyMetaAPIHeaders_ClientIdHeader_Issue6117(t *testing.T) {
	req, errReq := http.NewRequest(http.MethodPost, "https://api.meta.ai/responses", nil)
	if errReq != nil {
		t.Fatal(errReq)
	}
	auth := &cliproxyauth.Auth{
		Provider: "meta",
		Attributes: map[string]string{
			"api_key": "test-key",
		},
	}
	applyMetaAPIHeaders(req, auth, "test-key", true, nil)
	if got := req.Header.Get("X-Client-Id"); got != "tbh:tui" {
		t.Errorf("applyMetaAPIHeaders: X-Client-Id = %q, want %q", got, "tbh:tui")
	}
}

func TestMetaExecutor_Execute_SendsClientIdHeader_Issue6117(t *testing.T) {
	var capturedClientId string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedClientId = r.Header.Get("X-Client-Id")
		writeMetaResponsesOK(w, "ok")
	}))
	defer server.Close()

	exec := NewMetaExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "meta",
		Attributes: map[string]string{
			"api_key":  "meta-token",
			"base_url": server.URL,
		},
	}
	_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "muse-spark-1.3",
		Payload: []byte(`{"model":"muse-spark-1.3","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if errExec != nil {
		t.Fatalf("Execute() error = %v", errExec)
	}
	if capturedClientId != "tbh:tui" {
		t.Errorf("Execute: captured X-Client-Id = %q, want %q", capturedClientId, "tbh:tui")
	}
}

func TestMetaExecutor_Refresh_PreservesSubscriptionMetadata_Issue6117(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"api_key":            "LLM|minted-sub",
			"subs_tier_name":     "High Usage",
			"subs_tier_id":       "high_usage",
			"is_subs_active":     true,
			"has_payment_method": true,
		})
	}))
	defer server.Close()
	t.Setenv("META_MINT_URL", server.URL)

	auth := &cliproxyauth.Auth{
		Provider: "meta",
		Metadata: map[string]any{
			"dca_token": "dca:test-sub",
		},
		Attributes: map[string]string{
			"base_url": server.URL,
		},
	}
	refreshed, errRefresh := NewMetaExecutor(nil).Refresh(context.Background(), auth)
	if errRefresh != nil {
		t.Fatalf("Refresh() error = %v", errRefresh)
	}
	for key, want := range map[string]any{
		"subs_tier_name":     "High Usage",
		"subs_tier_id":       "high_usage",
		"is_subs_active":     true,
		"has_payment_method": true,
	} {
		got, ok := refreshed.Metadata[key]
		if !ok || got != want {
			t.Errorf("refreshed.Metadata[%q] = %v (present=%t), want %v", key, got, ok, want)
		}
	}
}

func TestMetaExecutor_Refresh_ClearsStaleSubscriptionMetadata_Issue6117(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"api_key":            "LLM|minted-sub",
			"subs_tier_name":     "",
			"subs_tier_id":       "",
			"is_subs_active":     false,
			"has_payment_method": false,
		})
	}))
	defer server.Close()
	t.Setenv("META_MINT_URL", server.URL)

	auth := &cliproxyauth.Auth{
		Provider: "meta",
		Metadata: map[string]any{
			"dca_token":      "dca:test-sub",
			"subs_tier_name": "Old Tier",
			"subs_tier_id":   "old_tier",
			"is_subs_active": true,
		},
		Attributes: map[string]string{
			"base_url": server.URL,
		},
	}
	refreshed, errRefresh := NewMetaExecutor(nil).Refresh(context.Background(), auth)
	if errRefresh != nil {
		t.Fatalf("Refresh() error = %v", errRefresh)
	}
	if tier, ok := refreshed.Metadata["subs_tier_name"]; ok && tier != "" {
		t.Errorf("expected subs_tier_name to be cleared, got %v", tier)
	}
	if tierID, ok := refreshed.Metadata["subs_tier_id"]; ok && tierID != "" {
		t.Errorf("expected subs_tier_id to be cleared, got %v", tierID)
	}
	if active, ok := refreshed.Metadata["is_subs_active"].(bool); !ok || active {
		t.Errorf("expected is_subs_active to be false, got %v", active)
	}
}

func TestMetaExecutor_NotFoundCooldown_Shortened_Issue6117(t *testing.T) {
	previous := cliproxyauth.QuotaCooldownDisabledForAuth(nil)
	cliproxyauth.SetQuotaCooldownDisabled(false)
	t.Cleanup(func() { cliproxyauth.SetQuotaCooldownDisabled(previous) })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"model_not_found","message":"model not found"}}`))
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewMetaExecutor(&config.Config{}))
	auth := &cliproxyauth.Auth{
		ID:       "meta-404-test",
		Provider: "meta",
		Metadata: map[string]any{
			"api_key":   "LLM|test",
			"auth_kind": "oauth",
		},
		Attributes: map[string]string{
			"base_url": server.URL,
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatal(errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "meta", []*registry.ModelInfo{{ID: "muse-spark-1.3"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	req := cliproxyexecutor.Request{
		Model:   "muse-spark-1.3",
		Payload: []byte(`{"model":"muse-spark-1.3","messages":[{"role":"user","content":"hi"}]}`),
	}
	_, _ = manager.Execute(context.Background(), []string{"meta"}, req, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to be registered")
	}
	state := updated.ModelStates["muse-spark-1.3"]
	if state == nil || !state.Unavailable {
		t.Fatalf("expected model state to be unavailable, got %#v", state)
	}
	remaining := time.Until(state.NextRetryAfter)
	if remaining < 4*time.Minute || remaining > 6*time.Minute {
		t.Fatalf("expected short ~5m cooldown for Meta 404, got remaining=%v", remaining)
	}
}

func TestMetaExecutor_ExecuteNonStreamMultiEventSSE_RecordsModelAndWarnsOnSubstitution(t *testing.T) {
	multiEventSSE := "event: response.output_item.added\n" +
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"item_0\",\"type\":\"message\",\"role\":\"assistant\"}}\n\n" +
		"event: response.output_item.done\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"item_0\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"hello\"}]}}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"substituted-meta-model\",\"usage\":{\"total_tokens\":10}}}\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(multiEventSSE))
	}))
	defer server.Close()

	const alias = "meta-multi-event-sse-test"
	capture := registerMultiProviderCapture(alias)

	hook := new(logtest.Hook)
	log.StandardLogger().AddHook(hook)
	t.Cleanup(func() {
		log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	})

	exec := NewMetaExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "meta",
		Attributes: map[string]string{
			"api_key":  "meta-token",
			"base_url": server.URL,
		},
	}

	ctx := coreusage.WithRequestedModelAlias(context.Background(), alias)
	resp, err := exec.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "muse-spark-1.3",
		Payload: []byte(`{"model":"muse-spark-1.3","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("expected non-empty translated payload")
	}

	record := capture.await(t)
	if record.Model != "muse-spark-1.3" {
		t.Fatalf("record.Model = %q, want muse-spark-1.3", record.Model)
	}
	if record.ResponseModel != "substituted-meta-model" {
		t.Fatalf("record.ResponseModel = %q, want substituted-meta-model", record.ResponseModel)
	}

	var foundWarning bool
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "upstream served model") && strings.Contains(entry.Message, "substituted-meta-model") {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Fatalf("expected substitution warning in logs for substituted-meta-model")
	}
}
