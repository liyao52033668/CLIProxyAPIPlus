package executor

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
	"github.com/tidwall/gjson"
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

func TestMetaExecutor_Execute_StripsSearchContentTypesFromWebSearch(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read body: %v", errRead)
			http.Error(w, errRead.Error(), http.StatusInternalServerError)
			return
		}
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

	payload := []byte(`{
		"model": "muse-spark-1.3-contributor",
		"input": [{"role": "user", "content": "search something"}],
		"tools": [
			{"type": "function", "name": "lookup", "parameters": {"type": "object"}},
			{"type": "web_search", "external_web_access": true, "search_content_types": ["text", "image"]}
		]
	}`)

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "muse-spark-1.3-contributor",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("codex"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	tools := gjson.GetBytes(gotBody, "tools").Array()
	var foundWebSearch bool
	for _, tool := range tools {
		if tool.Get("type").String() == "web_search" {
			foundWebSearch = true
			if tool.Get("search_content_types").Exists() {
				t.Fatalf("web_search tool still contains search_content_types: %s", gotBody)
			}
		}
	}
	if !foundWebSearch {
		t.Fatalf("web_search tool missing from forwarded body: %s", gotBody)
	}
}

func TestMetaExecutor_ExecuteStream_StripsSearchContentTypesFromWebSearch(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read body: %v", errRead)
			http.Error(w, errRead.Error(), http.StatusInternalServerError)
			return
		}
		writeMetaResponsesOK(w, "stream-ok")
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

	payload := []byte(`{
		"model": "muse-spark-1.3-contributor",
		"input": [{"role": "user", "content": "search something"}],
		"tools": [
			{"type": "web_search", "external_web_access": true, "search_content_types": ["text", "image"]}
		]
	}`)

	streamResult, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "muse-spark-1.3-contributor",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("codex"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	// Consume stream chunks
	for range streamResult.Chunks {
	}

	tools := gjson.GetBytes(gotBody, "tools").Array()
	var foundWebSearch bool
	for _, tool := range tools {
		if tool.Get("type").String() == "web_search" {
			foundWebSearch = true
			if tool.Get("search_content_types").Exists() {
				t.Fatalf("web_search tool still contains search_content_types in stream request: %s", gotBody)
			}
		}
	}
	if !foundWebSearch {
		t.Fatalf("web_search tool missing from forwarded stream body: %s", gotBody)
	}
}

func TestMetaExecutor_NormalizesToolFieldsForCodexUserAgent(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`))
	}))
	defer server.Close()

	exec := NewMetaExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "meta",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}

	payload := []byte(`{
		"model": "muse-spark-1.3",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},
			{
				"type": "additional_tools",
				"tools": [
					{
						"type": "function",
						"name": "functions__exec_command",
						"parameters": {
							"type": "object",
							"properties": {
								"yield_time_ms": {"type": "number"}
							}
						}
					}
				]
			}
		],
		"tools": [
			{
				"type": "function",
				"name": "exec_command",
				"parameters": {
					"type": "object",
					"properties": {
						"cmd": {"type": "string"},
						"yield_time_ms": {"type": "number"},
						"max_output_tokens": {"type": "number"},
						"timeout_ms": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "write_stdin",
				"parameters": {
					"type": "object",
					"properties": {
						"session_id": {"type": "number"},
						"yield_time_ms": {"type": "number"},
						"max_output_tokens": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "sleep",
				"parameters": {
					"type": "object",
					"properties": {
						"duration_ms": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "wait_agent",
				"parameters": {
					"type": "object",
					"properties": {
						"timeout_ms": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "wait",
				"parameters": {
					"type": "object",
					"properties": {
						"yield_time_ms": {"type": "number"},
						"max_tokens": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "tool_search",
				"parameters": {
					"type": "object",
					"properties": {
						"limit": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "test_sync_tool",
				"parameters": {
					"type": "object",
					"properties": {
						"sleep_before_ms": {"type": "number"},
						"sleep_after_ms": {"type": "number"},
						"participants": {"type": "number"},
						"timeout_ms": {"type": "number"}
					}
				}
			}
		]
	}`)

	// 1. With non-Codex User-Agent, tool types must remain unchanged (number).
	gotBody = nil
	_, errExecNonCodex := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "muse-spark-1.3",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Headers:      http.Header{"User-Agent": []string{"curl/8.7.1"}},
	})
	if errExecNonCodex != nil {
		t.Fatalf("Execute(non-codex) error = %v", errExecNonCodex)
	}
	if gotType := gjson.GetBytes(gotBody, "tools.0.parameters.properties.yield_time_ms.type").String(); gotType != "number" {
		t.Fatalf("expected non-Codex UA to preserve number, got %q", gotType)
	}

	// 2. With nil headers, tool types must remain unchanged (number).
	gotBody = nil
	_, errExecNilHeaders := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "muse-spark-1.3",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
	})
	if errExecNilHeaders != nil {
		t.Fatalf("Execute(nil headers) error = %v", errExecNilHeaders)
	}
	if gotType := gjson.GetBytes(gotBody, "tools.0.parameters.properties.yield_time_ms.type").String(); gotType != "number" {
		t.Fatalf("expected nil headers to preserve number, got %q", gotType)
	}

	// 3. With Codex User-Agent, specified fields must be normalized to integer.
	gotBody = nil
	_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "muse-spark-1.3",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Headers:      http.Header{"User-Agent": []string{"codex-desktop/0.159.0"}},
	})
	if errExec != nil {
		t.Fatalf("Execute() error = %v", errExec)
	}

	toolMap := make(map[string]gjson.Result)
	for _, tool := range gjson.GetBytes(gotBody, "tools").Array() {
		toolMap[tool.Get("name").String()] = tool
	}

	expected := map[string][]string{
		"exec_command":   {"yield_time_ms", "max_output_tokens", "timeout_ms"},
		"write_stdin":    {"session_id", "yield_time_ms", "max_output_tokens"},
		"sleep":          {"duration_ms"},
		"wait_agent":     {"timeout_ms"},
		"wait":           {"yield_time_ms", "max_tokens"},
		"tool_search":    {"limit"},
		"test_sync_tool": {"sleep_before_ms", "sleep_after_ms", "participants", "timeout_ms"},
	}

	for toolName, fields := range expected {
		tool, ok := toolMap[toolName]
		if !ok {
			t.Fatalf("missing tool: %s", toolName)
		}
		for _, field := range fields {
			if got := tool.Get("parameters.properties." + field + ".type").String(); got != "integer" {
				t.Errorf("tool %s field %s type = %q, want integer", toolName, field, got)
			}
		}
	}

	// Verify input[].additional_tools is also normalized to integer
	if gotType := gjson.GetBytes(gotBody, "input.#(type==\"additional_tools\").tools.0.parameters.properties.yield_time_ms.type").String(); gotType != "integer" {
		t.Fatalf("input additional_tools yield_time_ms type = %q, want integer", gotType)
	}
}

func TestMetaExecutor_FiltersSubscriptionUsageGlobally(t *testing.T) {
	const subscriptionUsageSSE = "event: response.subscription_usage\n" +
		"data: {\"subscription\":{\"tier\":\"1234567890123456\",\"weekly\":{\"resets_at\":1792368000,\"used_percent\":80},\"window\":{\"resets_at\":1792272519,\"used_percent\":10,\"window_duration_mins\":300}},\"type\":\"response.subscription_usage\"}\n\n"
	const createdSSE = "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"muse-spark-1.3\"}}\n\n"
	const deltaSSE = "event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"hello\"}\n\n"
	const completedSSE = "event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"muse-spark-1.3\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n"

	upstreamStream := subscriptionUsageSSE + createdSSE + deltaSSE + subscriptionUsageSSE + completedSSE + subscriptionUsageSSE

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamStream)
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

	formats := []struct {
		format  string
		payload string
	}{
		{
			format:  "openai-response",
			payload: `{"model":"muse-spark-1.3","input":"hello"}`,
		},
		{
			format:  "codex",
			payload: `{"model":"muse-spark-1.3","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`,
		},
		{
			format:  "openai",
			payload: `{"model":"muse-spark-1.3","messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			format:  "claude",
			payload: `{"model":"muse-spark-1.3","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			format:  "gemini",
			payload: `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
		},
	}

	for _, tc := range formats {
		t.Run("stream/"+tc.format, func(t *testing.T) {
			streamResult, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "muse-spark-1.3",
				Payload: []byte(tc.payload),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString(tc.format),
				Stream:       true,
			})
			if errStream != nil {
				t.Fatalf("ExecuteStream(%s) error = %v", tc.format, errStream)
			}

			var combined strings.Builder
			for chunk := range streamResult.Chunks {
				if chunk.Err != nil {
					t.Fatalf("ExecuteStream(%s) chunk error = %v", tc.format, chunk.Err)
				}
				combined.Write(chunk.Payload)
				combined.WriteByte('\n')
			}

			got := combined.String()
			if strings.Contains(got, "subscription_usage") || strings.Contains(got, "1234567890123456") {
				t.Fatalf("ExecuteStream(%s) leaked subscription_usage event:\n%s", tc.format, got)
			}
			if !strings.Contains(got, "hello") {
				t.Fatalf("ExecuteStream(%s) missing normal response content:\n%s", tc.format, got)
			}
		})

		t.Run("nonstream/"+tc.format, func(t *testing.T) {
			resp, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "muse-spark-1.3",
				Payload: []byte(tc.payload),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString(tc.format),
			})
			if errExec != nil {
				t.Fatalf("Execute(%s) error = %v", tc.format, errExec)
			}

			got := string(resp.Payload)
			if strings.Contains(got, "subscription_usage") || strings.Contains(got, "1234567890123456") {
				t.Fatalf("Execute(%s) leaked subscription_usage event:\n%s", tc.format, got)
			}
			if !strings.Contains(got, "hello") {
				t.Fatalf("Execute(%s) missing normal response content:\n%s", tc.format, got)
			}
		})
	}
}

// claudeCompactionCompletedResponse extracts the response.completed payload from
// a compaction SSE stream produced by the executor.
func claudeCompactionCompletedResponse(t *testing.T, stream string) gjson.Result {
	t.Helper()
	for _, frame := range strings.Split(stream, "\n\n") {
		if !strings.Contains(frame, "event: response.completed") {
			continue
		}
		for _, line := range strings.Split(frame, "\n") {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			return gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	t.Fatalf("response.completed missing from stream: %s", stream)
	return gjson.Result{}
}

func TestMetaExecutor_ResponsesCompactionTriggerAndReplay_Issue6781(t *testing.T) {
	var seenBodies [][]byte
	var returnIncomplete bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read upstream body: %v", errRead)
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		seenBodies = append(seenBodies, body)
		for idx, item := range gjson.GetBytes(body, "input").Array() {
			switch item.Get("type").String() {
			case "compaction_trigger", "compaction":
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":null,"message":"` + "`" + fmt.Sprintf("input[%d]", idx) + "`" + ` did not match any supported type","param":"` + fmt.Sprintf("input[%d]", idx) + `","type":"invalid_request_error"}}`))
				return
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		if returnIncomplete {
			_, _ = io.WriteString(w, "event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_meta_inc\",\"object\":\"response\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"model\":\"muse-wonder-1.0\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Partial truncated summary.\"}]}],\"usage\":{\"input_tokens\":15,\"output_tokens\":5,\"total_tokens\":20}}}\n\n")
			return
		}
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_meta_sum\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"muse-wonder-1.0\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Summary retaining CPA_COMPACTION_MARKER_42.\"}]}],\"usage\":{\"input_tokens\":15,\"output_tokens\":5,\"total_tokens\":20,\"input_tokens_details\":{\"cached_tokens\":3}}}}\n\n")
	}))
	defer server.Close()

	exec := NewMetaExecutor(&config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "muse-wonder-1.0"}},
				Params: map[string]any{
					"max_output_tokens": 2048,
				},
			}},
		},
	})
	auth := &cliproxyauth.Auth{
		Provider: "meta",
		Attributes: map[string]string{
			"api_key":  "meta-token",
			"base_url": server.URL,
		},
	}

	triggerPayload := []byte(`{
		"model": "muse-wonder-1.0",
		"stream": true,
		"store": false,
		"reasoning": {"effort": "low"},
		"input": [
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": "Remember the exact test marker CPA_COMPACTION_MARKER_42."}]
			},
			{"type": "compaction_trigger"}
		]
	}`)

	streamResult, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "muse-wonder-1.0",
		Payload: triggerPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		Stream:          true,
		OriginalRequest: triggerPayload,
	})
	if errStream != nil {
		t.Fatalf("ExecuteStream(compaction_trigger) error = %v", errStream)
	}

	var streamBuf bytes.Buffer
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		streamBuf.Write(chunk.Payload)
	}

	completed := claudeCompactionCompletedResponse(t, streamBuf.String())
	outputItems := completed.Get("response.output").Array()
	if len(outputItems) != 1 || outputItems[0].Get("type").String() != "compaction" {
		t.Fatalf("expected single compaction item in stream response, got: %s", completed.Raw)
	}
	capsule := outputItems[0].Get("encrypted_content").String()
	if !strings.HasPrefix(capsule, "cpa-ag-compact-v1:") {
		t.Fatalf("expected CPA compaction capsule prefix, got %q", capsule)
	}
	if gotCached := completed.Get("response.usage.input_tokens_details.cached_tokens").Int(); gotCached != 3 {
		t.Fatalf("cached_tokens = %d, want 3", gotCached)
	}

	nonStreamResp, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "muse-wonder-1.0",
		Payload: triggerPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: triggerPayload,
	})
	if errExec != nil {
		t.Fatalf("Execute(compaction_trigger) error = %v", errExec)
	}
	nonStreamItems := gjson.GetBytes(nonStreamResp.Payload, "output").Array()
	if len(nonStreamItems) != 1 || nonStreamItems[0].Get("type").String() != "compaction" {
		t.Fatalf("expected single compaction item in non-stream response, got: %s", nonStreamResp.Payload)
	}

	replayPayload := []byte(fmt.Sprintf(`{
		"model": "muse-wonder-1.0",
		"stream": true,
		"input": [
			{"type": "compaction", "encrypted_content": "foreign-opaque-token"},
			{"type": "compaction", "id": %q, "encrypted_content": %q},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "What is the exact test marker? Reply with only the marker."}]}
		]
	}`, outputItems[0].Get("id").String(), capsule))

	contStream, errCont := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "muse-wonder-1.0",
		Payload: replayPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		Stream:          true,
		OriginalRequest: replayPayload,
	})
	if errCont != nil {
		t.Fatalf("ExecuteStream(replay) error = %v", errCont)
	}
	for chunk := range contStream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("continuation stream chunk error = %v", chunk.Err)
		}
	}

	lastSeen := seenBodies[len(seenBodies)-1]
	if !bytes.Contains(lastSeen, []byte("Summary retaining CPA_COMPACTION_MARKER_42.")) {
		t.Fatalf("expected expanded summary in continuation upstream request, got: %s", lastSeen)
	}
	if bytes.Contains(lastSeen, []byte("foreign-opaque-token")) {
		t.Fatalf("expected foreign compaction item to be dropped, got: %s", lastSeen)
	}

	if _, errNonStreamReplay := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "muse-wonder-1.0",
		Payload: replayPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: replayPayload,
	}); errNonStreamReplay != nil {
		t.Fatalf("Execute(replay) error = %v", errNonStreamReplay)
	}
	nonStreamReplaySeen := seenBodies[len(seenBodies)-1]
	if !bytes.Contains(nonStreamReplaySeen, []byte("Summary retaining CPA_COMPACTION_MARKER_42.")) || bytes.Contains(nonStreamReplaySeen, []byte("foreign-opaque-token")) {
		t.Fatalf("unexpected non-stream replay upstream body: %s", nonStreamReplaySeen)
	}

	strippedTriggerPayload := []byte(`{"model":"muse-wonder-1.0","stream":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Remember the exact test marker CPA_COMPACTION_MARKER_42."}]}]}`)
	origOnlyResp, errOrigOnly := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "muse-wonder-1.0",
		Payload: strippedTriggerPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: triggerPayload,
	})
	if errOrigOnly != nil {
		t.Fatalf("Execute(OriginalRequest-only trigger) error = %v", errOrigOnly)
	}
	if got := gjson.GetBytes(origOnlyResp.Payload, "output.0.type").String(); got != "compaction" {
		t.Fatalf("OriginalRequest-only trigger output[0].type = %q, want compaction", got)
	}

	corruptedPayload := []byte(`{
		"model": "muse-wonder-1.0",
		"stream": false,
		"input": [
			{"type": "compaction", "encrypted_content": "cpa-ag-compact-v1:not-valid"},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "continue"}]}
		]
	}`)
	_, errCorrupt := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "muse-wonder-1.0",
		Payload: corruptedPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: corruptedPayload,
	})
	if errCorrupt == nil {
		t.Fatal("expected error for corrupted compaction capsule, got nil")
	}
	assertStatusErr(t, errCorrupt, http.StatusBadRequest)

	secondTriggerPayload := []byte(fmt.Sprintf(`{
		"model": "muse-wonder-1.0",
		"stream": false,
		"input": [
			{"type": "compaction", "id": %q, "encrypted_content": %q},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Second phase work."}]},
			{"type": "compaction_trigger"}
		]
	}`, outputItems[0].Get("id").String(), capsule))
	secondResp, errSecond := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "muse-wonder-1.0",
		Payload: secondTriggerPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: secondTriggerPayload,
	})
	if errSecond != nil {
		t.Fatalf("Execute(second compaction_trigger) error = %v", errSecond)
	}
	if got := len(gjson.GetBytes(secondResp.Payload, "output").Array()); got != 1 {
		t.Fatalf("second compaction output len = %d, want 1", got)
	}
	secondSeen := seenBodies[len(seenBodies)-1]
	if !bytes.Contains(secondSeen, []byte("Summary retaining CPA_COMPACTION_MARKER_42.")) {
		t.Fatalf("expected first capsule summary in second compaction request, got: %s", secondSeen)
	}
	if got := gjson.GetBytes(secondSeen, "max_output_tokens").Int(); got != 2048 {
		t.Fatalf("max_output_tokens = %d, want payload rule 2048; body=%s", got, secondSeen)
	}

	returnIncomplete = true
	_, errIncExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "muse-wonder-1.0",
		Payload: triggerPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: triggerPayload,
	})
	if errIncExec == nil {
		t.Fatal("expected Execute(compaction_trigger) to fail on incomplete summary, got nil")
	}
	assertStatusErr(t, errIncExec, http.StatusBadGateway)

	_, errIncStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "muse-wonder-1.0",
		Payload: triggerPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		Stream:          true,
		OriginalRequest: triggerPayload,
	})
	if errIncStream == nil {
		t.Fatal("expected ExecuteStream(compaction_trigger) to fail on incomplete summary, got nil")
	}
	assertStatusErr(t, errIncStream, http.StatusBadGateway)
}
