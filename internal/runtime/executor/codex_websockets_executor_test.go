package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

func TestBuildCodexWebsocketRequestBodyPreservesPreviousResponseID(t *testing.T) {
	body := []byte(`{"model":"gpt-5-codex","previous_response_id":"resp-1","input":[{"type":"message","id":"msg-1"}]}`)

	wsReqBody := buildCodexWebsocketRequestBody(body)

	if got := gjson.GetBytes(wsReqBody, "type").String(); got != "response.create" {
		t.Fatalf("type = %s, want response.create", got)
	}
	if got := gjson.GetBytes(wsReqBody, "previous_response_id").String(); got != "resp-1" {
		t.Fatalf("previous_response_id = %s, want resp-1", got)
	}
	if gjson.GetBytes(wsReqBody, "input.0.id").String() != "msg-1" {
		t.Fatalf("input item id mismatch")
	}
	if got := gjson.GetBytes(wsReqBody, "type").String(); got == "response.append" {
		t.Fatalf("unexpected websocket request type: %s", got)
	}
}

func TestNormalizeCodexWebsocketParallelToolCallsRemovesValueWithoutTools(t *testing.T) {
	body := []byte(`{"model":"gpt-5-codex","parallel_tool_calls":true,"input":[]}`)
	updated := normalizeCodexWebsocketParallelToolCalls(body, nil)
	if gjson.GetBytes(updated, "parallel_tool_calls").Exists() {
		t.Fatalf("parallel_tool_calls should be removed without tools: %s", updated)
	}
}

func TestCodexWebsocketsExecutePreservesPreviousResponseIDUpstream(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	capturedPayload := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("request path = %s, want /responses", r.URL.Path)
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade websocket: %v", err)
		}
		defer func() { _ = conn.Close() }()

		msgType, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read upstream websocket message: %v", err)
		}
		if msgType != websocket.TextMessage {
			t.Fatalf("message type = %d, want text", msgType)
		}
		capturedPayload <- bytes.Clone(payload)

		completed := []byte(`{"type":"response.completed","response":{"id":"resp-2","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Fatalf("write completed websocket message: %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","previous_response_id":"resp-1","input":[{"type":"message","id":"msg-1"}]}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("codex")}

	if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	select {
	case payload := <-capturedPayload:
		if got := gjson.GetBytes(payload, "type").String(); got != "response.create" {
			t.Fatalf("upstream type = %s, want response.create; payload=%s", got, payload)
		}
		if got := gjson.GetBytes(payload, "previous_response_id").String(); got != "resp-1" {
			t.Fatalf("upstream previous_response_id = %s, want resp-1; payload=%s", got, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream websocket payload")
	}
}

func TestCodexWebsocketsExecuteStreamUnlocksSessionAfterHandshakeStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad websocket request", http.StatusBadRequest)
	}))
	defer server.Close()

	const sessionID = "session-handshake-status-error"
	globalCodexWebsocketSessionStore.mu.Lock()
	delete(globalCodexWebsocketSessionStore.sessions, sessionID)
	globalCodexWebsocketSessionStore.mu.Unlock()
	t.Cleanup(func() {
		globalCodexWebsocketSessionStore.mu.Lock()
		delete(globalCodexWebsocketSessionStore.sessions, sessionID)
		globalCodexWebsocketSessionStore.mu.Unlock()
	})

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: []byte(`{"model":"gpt-5-codex","input":[]}`)}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("codex"),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
		},
	}

	if _, err := exec.ExecuteStream(context.Background(), auth, req, opts); err == nil {
		t.Fatal("ExecuteStream() error = nil, want handshake status error")
	}
	sess := exec.getOrCreateSession(sessionID)
	if !sess.reqMu.TryLock() {
		t.Fatal("session request mutex remained locked after handshake status error")
	}
	sess.reqMu.Unlock()
}

func TestCodexWebsocketsUpstreamDisconnectChanSignalsOnInvalidate(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	sessionID := "sess-1"
	disconnectCh := exec.UpstreamDisconnectChan(sessionID)
	if disconnectCh == nil {
		t.Fatal("expected disconnect channel")
	}

	sess := exec.getOrCreateSession(sessionID)
	if sess == nil {
		t.Fatal("expected session")
	}
	sess.connMu.Lock()
	sess.conn = conn
	sess.authID = "auth-1"
	sess.wsURL = "ws://example.test/responses"
	sess.readerConn = conn
	sess.connMu.Unlock()

	upstreamErr := errors.New("upstream gone")
	exec.invalidateUpstreamConn(sess, conn, "test_invalidate", upstreamErr)

	select {
	case errRead, ok := <-disconnectCh:
		if !ok {
			t.Fatal("expected disconnect channel to deliver error before closing")
		}
		if errRead == nil || errRead.Error() != upstreamErr.Error() {
			t.Fatalf("disconnect error = %v, want %v", errRead, upstreamErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for disconnect signal")
	}
}

func TestCodexWebsocketsUpstreamDisconnectChanRecreatesChannelAfterInvalidate(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	const sessionID = "sess-recreate-disconnect-channel"
	globalCodexWebsocketSessionStore.mu.Lock()
	delete(globalCodexWebsocketSessionStore.sessions, sessionID)
	globalCodexWebsocketSessionStore.mu.Unlock()
	t.Cleanup(func() {
		globalCodexWebsocketSessionStore.mu.Lock()
		delete(globalCodexWebsocketSessionStore.sessions, sessionID)
		globalCodexWebsocketSessionStore.mu.Unlock()
	})

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	firstCh := exec.UpstreamDisconnectChan(sessionID)
	if firstCh == nil {
		t.Fatal("expected first disconnect channel")
	}

	sess := exec.getOrCreateSession(sessionID)
	if sess == nil {
		t.Fatal("expected session")
	}
	sess.connMu.Lock()
	sess.conn = conn
	sess.readerConn = conn
	sess.authID = "auth-1"
	sess.wsURL = "ws://example.test/responses"
	sess.connMu.Unlock()

	exec.invalidateUpstreamConn(sess, conn, "test_invalidate", errors.New("upstream gone"))

	select {
	case <-firstCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first disconnect signal")
	}

	secondCh := exec.UpstreamDisconnectChan(sessionID)
	if secondCh == nil {
		t.Fatal("expected second disconnect channel")
	}
	if secondCh == firstCh {
		t.Fatal("expected a new disconnect channel after invalidation")
	}

	select {
	case _, ok := <-secondCh:
		if !ok {
			t.Fatal("expected recreated disconnect channel to remain open")
		}
		t.Fatal("expected recreated disconnect channel to have no pending signal")
	default:
	}
}

func TestApplyCodexWebsocketHeadersDefaultsToCurrentResponsesBeta(t *testing.T) {
	headers := applyCodexWebsocketHeaders(context.Background(), http.Header{}, nil, "", nil)

	if got := headers.Get("OpenAI-Beta"); got != codexResponsesWebsocketBetaHeaderValue {
		t.Fatalf("OpenAI-Beta = %s, want %s", got, codexResponsesWebsocketBetaHeaderValue)
	}
	if got := headers.Get("User-Agent"); got != codexUserAgent {
		t.Fatalf("User-Agent = %s, want %s", got, codexUserAgent)
	}
	if !strings.HasPrefix(codexUserAgent, codexOriginator+"/") {
		t.Fatalf("default Codex User-Agent = %s, want prefix %s/", codexUserAgent, codexOriginator)
	}
	if strings.HasPrefix(codexUserAgent, "codex-tui/") {
		t.Fatalf("default Codex User-Agent = %s, must not use stale codex-tui prefix", codexUserAgent)
	}
	if strings.Contains(codexUserAgent, "(codex-tui;") {
		t.Fatalf("default Codex User-Agent = %s, must not include stale codex-tui suffix", codexUserAgent)
	}
	if got := headers.Get("Originator"); got != codexOriginator {
		t.Fatalf("Originator = %s, want %s", got, codexOriginator)
	}
	if got := headers.Get("Version"); got != "" {
		t.Fatalf("Version = %q, want empty", got)
	}
	if got := headers.Get("x-codex-beta-features"); got != "" {
		t.Fatalf("x-codex-beta-features = %q, want empty", got)
	}
	if got := headers.Get("X-Codex-Turn-Metadata"); got != "" {
		t.Fatalf("X-Codex-Turn-Metadata = %q, want empty", got)
	}
	if got := headers.Get("X-Client-Request-Id"); got != "" {
		t.Fatalf("X-Client-Request-Id = %q, want empty", got)
	}
}

func TestApplyCodexWebsocketHeadersPassesThroughClientIdentityHeaders(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	ctx := contextWithGinHeaders(map[string]string{
		"Originator":            "Codex Desktop",
		"User-Agent":            "codex_cli_rs/0.1.0",
		"Version":               "0.115.0-alpha.27",
		"X-Codex-Turn-Metadata": `{"turn_id":"turn-1"}`,
		"X-Client-Request-Id":   "019d2233-e240-7162-992d-38df0a2a0e0d",
		"session_id":            "sess-client",
	})

	headers := applyCodexWebsocketHeaders(ctx, http.Header{}, auth, "", nil)

	if got := headers.Get("Originator"); got != "Codex Desktop" {
		t.Fatalf("Originator = %s, want %s", got, "Codex Desktop")
	}
	if got := headers.Get("User-Agent"); got != "codex_cli_rs/0.1.0" {
		t.Fatalf("User-Agent = %s, want %s", got, "codex_cli_rs/0.1.0")
	}
	if got := headers.Get("Version"); got != "0.115.0-alpha.27" {
		t.Fatalf("Version = %s, want %s", got, "0.115.0-alpha.27")
	}
	if got := headers.Get("X-Codex-Turn-Metadata"); got != `{"turn_id":"turn-1"}` {
		t.Fatalf("X-Codex-Turn-Metadata = %s, want %s", got, `{"turn_id":"turn-1"}`)
	}
	if got := headers.Get("X-Client-Request-Id"); got != "019d2233-e240-7162-992d-38df0a2a0e0d" {
		t.Fatalf("X-Client-Request-Id = %s, want %s", got, "019d2233-e240-7162-992d-38df0a2a0e0d")
	}
	if got := headerValueCaseInsensitive(headers, "session_id"); got != "sess-client" {
		t.Fatalf("session_id = %s, want sess-client", got)
	}
	if _, ok := headers["session_id"]; !ok {
		t.Fatalf("expected lowercase session_id header key, got %#v", headers)
	}
}

func TestApplyCodexWebsocketHeadersUsesConfigDefaultsForOAuth(t *testing.T) {
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "my-codex-client/1.0",
			BetaFeatures: "feature-a,feature-b",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}

	headers := applyCodexWebsocketHeaders(context.Background(), http.Header{}, auth, "", cfg)

	if got := headers.Get("User-Agent"); got != "my-codex-client/1.0" {
		t.Fatalf("User-Agent = %s, want %s", got, "my-codex-client/1.0")
	}
	if got := headers.Get("x-codex-beta-features"); got != "feature-a,feature-b" {
		t.Fatalf("x-codex-beta-features = %s, want %s", got, "feature-a,feature-b")
	}
	if got := headers.Get("OpenAI-Beta"); got != codexResponsesWebsocketBetaHeaderValue {
		t.Fatalf("OpenAI-Beta = %s, want %s", got, codexResponsesWebsocketBetaHeaderValue)
	}
}

func TestApplyCodexWebsocketHeadersPrefersExistingHeadersOverClientAndConfig(t *testing.T) {
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "config-ua",
			BetaFeatures: "config-beta",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	ctx := contextWithGinHeaders(map[string]string{
		"User-Agent":            "client-ua",
		"X-Codex-Beta-Features": "client-beta",
	})
	headers := http.Header{}
	headers.Set("User-Agent", "existing-ua")
	headers.Set("X-Codex-Beta-Features", "existing-beta")

	got := applyCodexWebsocketHeaders(ctx, headers, auth, "", cfg)

	if gotVal := got.Get("User-Agent"); gotVal != "existing-ua" {
		t.Fatalf("User-Agent = %s, want %s", gotVal, "existing-ua")
	}
	if gotVal := got.Get("x-codex-beta-features"); gotVal != "existing-beta" {
		t.Fatalf("x-codex-beta-features = %s, want %s", gotVal, "existing-beta")
	}
}

func TestApplyCodexWebsocketHeadersConfigUserAgentOverridesClientHeader(t *testing.T) {
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "config-ua",
			BetaFeatures: "config-beta",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	ctx := contextWithGinHeaders(map[string]string{
		"User-Agent":            "client-ua",
		"X-Codex-Beta-Features": "client-beta",
	})

	headers := applyCodexWebsocketHeaders(ctx, http.Header{}, auth, "", cfg)

	if got := headers.Get("User-Agent"); got != "config-ua" {
		t.Fatalf("User-Agent = %s, want %s", got, "config-ua")
	}
	if got := headers.Get("x-codex-beta-features"); got != "client-beta" {
		t.Fatalf("x-codex-beta-features = %s, want %s", got, "client-beta")
	}
}

func TestApplyCodexWebsocketHeadersIgnoresConfigForAPIKeyAuth(t *testing.T) {
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "config-ua",
			BetaFeatures: "config-beta",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider:   "codex",
		Attributes: map[string]string{"api_key": "sk-test"},
	}

	headers := applyCodexWebsocketHeaders(context.Background(), http.Header{}, auth, "sk-test", cfg)

	if got := headers.Get("User-Agent"); got != "" {
		t.Fatalf("User-Agent = %s, want empty", got)
	}
	if got := headers.Get("x-codex-beta-features"); got != "" {
		t.Fatalf("x-codex-beta-features = %q, want empty", got)
	}
	if got := headers.Get("Originator"); got != "" {
		t.Fatalf("Originator = %s, want empty", got)
	}
}

func TestApplyCodexWebsocketHeadersPreservesExplicitAPIKeyUserAgent(t *testing.T) {
	auth := &cliproxyauth.Auth{Provider: "codex", Attributes: map[string]string{"api_key": "sk-test"}}
	ctx := contextWithGinHeaders(map[string]string{"User-Agent": "api-key-client/1.0", "Originator": "explicit-origin"})

	headers := applyCodexWebsocketHeaders(ctx, http.Header{}, auth, "sk-test", nil)

	if got := headers.Get("User-Agent"); got != "api-key-client/1.0" {
		t.Fatalf("User-Agent = %s, want api-key-client/1.0", got)
	}
	if got := headers.Get("Originator"); got != "explicit-origin" {
		t.Fatalf("Originator = %s, want explicit-origin", got)
	}
}

func TestApplyModelHeaderOverridesFromModelConfig(t *testing.T) {
	const wantUA = "codex-tui/0.144.0 (Mac OS 26.5.1; arm64) iTerm.app/3.6.11 (codex-tui; 0.144.0)"
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent: "config-ua",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	applyCodexHeaders(req, auth, "", true, cfg)
	applyModelHeaderOverrides(req.Header, "gpt-5.6-luna")

	if got := req.Header.Get("User-Agent"); got != wantUA {
		t.Fatalf("User-Agent = %q, want %q", got, wantUA)
	}
	if got := req.Header.Get("Originator"); got != "codex-tui" {
		t.Fatalf("Originator = %q, want codex-tui", got)
	}
	if got := headerValueCaseInsensitive(req.Header, "Session_id"); got == "" {
		t.Fatalf("Session_id should be generated for Mac OS override headers: %#v", req.Header)
	}
}

func TestApplyModelHeaderOverridesNoopForModelsWithoutConfig(t *testing.T) {
	headers := http.Header{}
	headers.Set("User-Agent", "existing-ua")
	applyModelHeaderOverrides(headers, "gpt-5.4")

	if got := headers.Get("User-Agent"); got != "existing-ua" {
		t.Fatalf("User-Agent = %q, want existing-ua", got)
	}
}

func TestApplyCodexPromptCacheHeadersSetsLowercaseSessionAndLegacyConversation(t *testing.T) {
	req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: []byte(`{"prompt_cache_key":"cache-1"}`)}

	_, headers := applyCodexPromptCacheHeaders("openai-response", req, []byte(`{"model":"gpt-5-codex"}`))

	if got := headerValueCaseInsensitive(headers, "session_id"); got != "cache-1" {
		t.Fatalf("session_id = %s, want cache-1", got)
	}
	if _, ok := headers["session_id"]; !ok {
		t.Fatalf("expected lowercase session_id key, got %#v", headers)
	}
	if got := headers.Get("Conversation_id"); got != "cache-1" {
		t.Fatalf("Conversation_id = %s, want cache-1", got)
	}
}

func TestApplyCodexWebsocketHeadersUsesCanonicalAccountHeader(t *testing.T) {
	auth := &cliproxyauth.Auth{Provider: "codex", Metadata: map[string]any{"account_id": "acct-1"}}

	headers := applyCodexWebsocketHeaders(context.Background(), http.Header{}, auth, "", nil)

	if got := headerValueCaseInsensitive(headers, "ChatGPT-Account-ID"); got != "acct-1" {
		t.Fatalf("ChatGPT-Account-ID = %s, want acct-1", got)
	}
	values, ok := headers["ChatGPT-Account-ID"]
	if !ok {
		t.Fatalf("expected exact ChatGPT-Account-ID key, got %#v", headers)
	}
	if len(values) != 1 || values[0] != "acct-1" {
		t.Fatalf("ChatGPT-Account-ID values = %#v, want [acct-1]", values)
	}
}

func TestBuildCodexResponsesWebsocketURLRequiresHTTPURL(t *testing.T) {
	if got, err := buildCodexResponsesWebsocketURL("https://example.com/backend/responses"); err != nil || got != "wss://example.com/backend/responses" {
		t.Fatalf("https URL = %q, %v; want wss URL", got, err)
	}
	if _, err := buildCodexResponsesWebsocketURL("ftp://example.com/responses"); err == nil {
		t.Fatalf("expected unsupported scheme error")
	}
	if _, err := buildCodexResponsesWebsocketURL("https:///responses"); err == nil {
		t.Fatalf("expected empty host error")
	}
}

func TestParseCodexWebsocketErrorMarksConnectionLimitRetryable(t *testing.T) {
	err, ok := parseCodexWebsocketError([]byte(`{"type":"error","status":429,"error":{"code":"websocket_connection_limit_reached","message":"too many websockets"},"headers":{"retry-after":"1"}}`))
	if !ok {
		t.Fatalf("expected websocket error")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %#v, want 429", err)
	}
	retryable, ok := err.(interface{ RetryAfter() *time.Duration })
	if !ok || retryable.RetryAfter() == nil {
		t.Fatalf("expected retryable websocket connection limit error")
	}
	if got := *retryable.RetryAfter(); got != 0 {
		t.Fatalf("retryAfter = %v, want connection-limit fallback 0", got)
	}
	withHeaders, ok := err.(interface{ Headers() http.Header })
	if !ok || withHeaders.Headers().Get("retry-after") != "1" {
		t.Fatalf("headers = %#v, want retry-after", err)
	}
}

func TestParseCodexWebsocketErrorUsesUsageLimitRetryMetadata(t *testing.T) {
	err, ok := parseCodexWebsocketError([]byte(`{"type":"error","status":429,"body":{"error":{"type":"usage_limit_reached","message":"usage limit reached","resets_in_seconds":7}}}`))
	if !ok {
		t.Fatalf("expected websocket error")
	}

	retryable, ok := err.(interface{ RetryAfter() *time.Duration })
	if !ok || retryable.RetryAfter() == nil {
		t.Fatalf("expected retryable usage limit websocket error")
	}
	if got := *retryable.RetryAfter(); got != 7*time.Second {
		t.Fatalf("retryAfter = %v, want 7s", got)
	}
}

func TestParseCodexWebsocketErrorPreservesWrappedBodyAndHeaders(t *testing.T) {
	err, ok := parseCodexWebsocketError([]byte(`{"type":"error","status":429,"body":{"error":{"code":"websocket_connection_limit_reached","type":"server_error","message":"too many websocket connections"}},"headers":{"x-request-id":"req-1"}}`))
	if !ok {
		t.Fatalf("expected websocket error")
	}

	parsed := gjson.Parse(err.Error())
	if got := parsed.Get("status").Int(); got != http.StatusTooManyRequests {
		t.Fatalf("wrapped status = %d, want 429; payload=%s", got, err.Error())
	}
	if got := parsed.Get("body.error.code").String(); got != "websocket_connection_limit_reached" {
		t.Fatalf("wrapped body error code = %s, want websocket_connection_limit_reached; payload=%s", got, err.Error())
	}
	if got := parsed.Get("error.code").String(); got != "websocket_connection_limit_reached" {
		t.Fatalf("surface error code = %s, want websocket_connection_limit_reached; payload=%s", got, err.Error())
	}
	retryable, ok := err.(interface{ RetryAfter() *time.Duration })
	if !ok || retryable.RetryAfter() == nil {
		t.Fatalf("expected body.error.code websocket connection limit to be retryable")
	}
	withHeaders, ok := err.(interface{ Headers() http.Header })
	if !ok || withHeaders.Headers().Get("x-request-id") != "req-1" {
		t.Fatalf("headers = %#v, want x-request-id", err)
	}
}

func TestApplyCodexHeadersUsesConfigUserAgentForOAuth(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "config-ua",
			BetaFeatures: "config-beta",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	req = req.WithContext(contextWithGinHeaders(map[string]string{
		"User-Agent": "client-ua",
	}))

	applyCodexHeaders(req, auth, "oauth-token", true, cfg)

	if got := req.Header.Get("User-Agent"); got != "config-ua" {
		t.Fatalf("User-Agent = %s, want %s", got, "config-ua")
	}
	if got := req.Header.Get("x-codex-beta-features"); got != "" {
		t.Fatalf("x-codex-beta-features = %q, want empty", got)
	}
}

func TestApplyCodexHeadersPassesThroughClientIdentityHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	req = req.WithContext(contextWithGinHeaders(map[string]string{
		"Originator":            "Codex Desktop",
		"Version":               "0.115.0-alpha.27",
		"X-Codex-Turn-Metadata": `{"turn_id":"turn-1"}`,
		"X-Client-Request-Id":   "019d2233-e240-7162-992d-38df0a2a0e0d",
	}))

	applyCodexHeaders(req, auth, "oauth-token", true, nil)

	if got := req.Header.Get("Originator"); got != "Codex Desktop" {
		t.Fatalf("Originator = %s, want %s", got, "Codex Desktop")
	}
	if got := req.Header.Get("Version"); got != "0.115.0-alpha.27" {
		t.Fatalf("Version = %s, want %s", got, "0.115.0-alpha.27")
	}
	if got := req.Header.Get("X-Codex-Turn-Metadata"); got != `{"turn_id":"turn-1"}` {
		t.Fatalf("X-Codex-Turn-Metadata = %s, want %s", got, `{"turn_id":"turn-1"}`)
	}
	if got := req.Header.Get("X-Client-Request-Id"); got != "019d2233-e240-7162-992d-38df0a2a0e0d" {
		t.Fatalf("X-Client-Request-Id = %s, want %s", got, "019d2233-e240-7162-992d-38df0a2a0e0d")
	}
}

func TestApplyCodexHeadersDoesNotInjectClientOnlyHeadersByDefault(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	applyCodexHeaders(req, nil, "oauth-token", true, nil)

	if got := req.Header.Get("Version"); got != "" {
		t.Fatalf("Version = %q, want empty", got)
	}
	if got := req.Header.Get("X-Codex-Turn-Metadata"); got != "" {
		t.Fatalf("X-Codex-Turn-Metadata = %q, want empty", got)
	}
	if got := req.Header.Get("X-Client-Request-Id"); got != "" {
		t.Fatalf("X-Client-Request-Id = %q, want empty", got)
	}
}

func contextWithGinHeaders(headers map[string]string) context.Context {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	ginCtx.Request.Header = make(http.Header, len(headers))
	for key, value := range headers {
		ginCtx.Request.Header.Set(key, value)
	}
	return context.WithValue(context.Background(), "gin", ginCtx)
}

func TestNewProxyAwareWebsocketDialerDirectDisablesProxy(t *testing.T) {
	t.Parallel()

	dialer := newProxyAwareWebsocketDialer(
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "direct"},
	)

	if dialer.Proxy != nil {
		t.Fatal("expected websocket proxy function to be nil for direct mode")
	}
}

func TestCodexWebsocketsExecuteStreamUnexpectedBinaryMessage(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("request path = %s, want /responses", r.URL.Path)
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade websocket: %v", err)
		}
		defer func() { _ = conn.Close() }()

		if _, _, err := conn.ReadMessage(); err != nil {
			t.Fatalf("read upstream websocket message: %v", err)
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, []byte{0x01}); err != nil {
			t.Fatalf("write binary websocket message: %v", err)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","input":[]}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("codex")}

	result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	select {
	case chunk, ok := <-result.Chunks:
		if !ok {
			t.Fatal("expected stream chunk before close")
		}
		if chunk.Err == nil || !strings.Contains(chunk.Err.Error(), "unexpected binary message") {
			t.Fatalf("chunk err = %v, want unexpected binary message", chunk.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for stream error chunk")
	}
}

func TestBuildCodexWebsocketRequestBodyShortensOverlongInputItemIDs(t *testing.T) {
	longCallItemID := strings.Repeat("grok-call-item-", 6)
	longOutputItemID := strings.Repeat("grok-output-item-", 6)
	body := []byte(`{"model":"gpt-5-codex","input":[{"type":"function_call","id":"` + longCallItemID + `","call_id":"call-1","name":"lookup"},{"type":"function_call_output","id":"` + longOutputItemID + `","call_id":"call-1","output":"ok"},{"type":"message","id":"item_74ec40c883248ebb4885ec84"}]}`)

	first := buildCodexWebsocketRequestBody(body)
	second := buildCodexWebsocketRequestBody(body)

	shortCallItemID := gjson.GetBytes(first, "input.0.id").String()
	shortOutputItemID := gjson.GetBytes(first, "input.1.id").String()
	if len([]rune(shortCallItemID)) > 64 || shortCallItemID == longCallItemID {
		t.Fatalf("input.0.id was not shortened to at most 64 characters: %q", shortCallItemID)
	}
	if len([]rune(shortOutputItemID)) > 64 || shortOutputItemID == longOutputItemID {
		t.Fatalf("input.1.id was not shortened to at most 64 characters: %q", shortOutputItemID)
	}
	if shortCallItemID == shortOutputItemID {
		t.Fatalf("distinct long IDs produced the same shortened ID: %q", shortCallItemID)
	}
	if got := gjson.GetBytes(second, "input.0.id").String(); got != shortCallItemID {
		t.Fatalf("input item ID shortening is not deterministic: first=%q second=%q", shortCallItemID, got)
	}
	if got := gjson.GetBytes(first, "input.0.call_id").String(); got != "call-1" {
		t.Fatalf("function call_id = %q, want call-1", got)
	}
	if got := gjson.GetBytes(first, "input.1.call_id").String(); got != "call-1" {
		t.Fatalf("function call output call_id = %q, want call-1", got)
	}
	if got := gjson.GetBytes(first, "input.2.id").String(); got != "msg_item_74ec40c883248ebb4885ec84" {
		t.Fatalf("message input item ID was not normalized: %q", got)
	}
}

func TestCodexWebsocketsExecuteResponsesLiteDoesNotInjectImageGenerationTool(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	capturedPayload := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()

		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("read upstream websocket message: %v", errRead)
			return
		}
		capturedPayload <- bytes.Clone(payload)

		completed := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Errorf("write completed websocket message: %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":   "sk-test",
			"base_url":  server.URL,
			"plan_type": "pro",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","input":[{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"exec"}]},{"role":"user","content":"hello"}],"parallel_tool_calls":true,"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"}}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("codex")}

	if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	select {
	case payload := <-capturedPayload:
		if tools := gjson.GetBytes(payload, "tools"); tools.Exists() {
			t.Fatalf("unexpected tools in responses-lite websocket payload: %s", tools.Raw)
		}
		if got := gjson.GetBytes(payload, "client_metadata.ws_request_header_x_openai_internal_codex_responses_lite").String(); got != "true" {
			t.Fatalf("responses-lite metadata = %q, want true; payload=%s", got, payload)
		}
		parallelToolCalls := gjson.GetBytes(payload, "parallel_tool_calls")
		if !parallelToolCalls.Exists() || parallelToolCalls.Bool() {
			t.Fatalf("responses-lite parallel_tool_calls should be false: %s", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream websocket payload")
	}
}

func TestCodexWebsocketsExecuteStreamResponsesLiteForcesParallelToolCallsFalse(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	capturedPayload := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()

		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("read upstream websocket message: %v", errRead)
			return
		}
		capturedPayload <- bytes.Clone(payload)

		completed := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Errorf("write completed websocket message: %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":   "sk-test",
			"base_url":  server.URL,
			"plan_type": "pro",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.6-luna",
		Payload: []byte(`{"model":"gpt-5.6-luna","input":[{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"exec"}]},{"role":"user","content":"hello"}],"parallel_tool_calls":true,"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"}}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("codex")}

	result, errExecute := exec.ExecuteStream(context.Background(), auth, req, opts)
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	streamComplete := false
	for !streamComplete {
		select {
		case chunk, ok := <-result.Chunks:
			if !ok {
				streamComplete = true
				continue
			}
			if chunk.Err != nil {
				t.Fatalf("stream chunk error = %v", chunk.Err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for websocket stream completion")
		}
	}

	select {
	case payload := <-capturedPayload:
		parallelToolCalls := gjson.GetBytes(payload, "parallel_tool_calls")
		if !parallelToolCalls.Exists() || parallelToolCalls.Bool() {
			t.Fatalf("responses-lite parallel_tool_calls should be false: %s", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream websocket payload")
	}
}

func TestSendTerminalWebsocketReadInvalidatesBeforeWaitingForCapacity(t *testing.T) {
	terminalErr := &websocket.CloseError{Code: websocket.CloseMessageTooBig}

	t.Run("available channel keeps fast path ordering", func(t *testing.T) {
		ch := make(chan codexWebsocketRead, 1)
		done := make(chan struct{})
		invalidateCalls := 0
		invalidated := sendTerminalWebsocketRead(ch, done, codexWebsocketRead{err: terminalErr}, func() {
			invalidateCalls++
		})
		if invalidated {
			t.Fatal("available channel should not invalidate before delivery")
		}
		if invalidateCalls != 0 {
			t.Fatalf("invalidate calls = %d, want 0", invalidateCalls)
		}
		event := <-ch
		if !errors.Is(event.err, terminalErr) {
			t.Fatalf("terminal error = %v, want %v", event.err, terminalErr)
		}
	})

	t.Run("full channel invalidates before waiting", func(t *testing.T) {
		ch := make(chan codexWebsocketRead, 1)
		ch <- codexWebsocketRead{payload: []byte("queued")}
		done := make(chan struct{})
		invalidateCalled := make(chan struct{})
		result := make(chan bool, 1)

		go func() {
			result <- sendTerminalWebsocketRead(ch, done, codexWebsocketRead{err: terminalErr}, func() {
				close(invalidateCalled)
			})
		}()

		select {
		case <-invalidateCalled:
		case <-time.After(time.Second):
			t.Fatal("invalidation did not happen before waiting for channel capacity")
		}
		select {
		case <-result:
			t.Fatal("terminal sender returned before capacity was released")
		default:
		}

		<-ch
		select {
		case event := <-ch:
			if !errors.Is(event.err, terminalErr) {
				t.Fatalf("terminal error = %v, want %v", event.err, terminalErr)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for terminal read")
		}
		select {
		case invalidated := <-result:
			if !invalidated {
				t.Fatal("full channel should report early invalidation")
			}
		case <-time.After(time.Second):
			t.Fatal("terminal sender did not finish")
		}
	})

	t.Run("full channel stops when invalidation cancels active read", func(t *testing.T) {
		ch := make(chan codexWebsocketRead, 1)
		ch <- codexWebsocketRead{payload: []byte("queued")}
		done := make(chan struct{})
		invalidated := sendTerminalWebsocketRead(ch, done, codexWebsocketRead{err: terminalErr}, func() {
			close(done)
		})
		if !invalidated {
			t.Fatal("full channel should report early invalidation")
		}
		if len(ch) != 1 {
			t.Fatalf("channel length = %d, want queued payload only", len(ch))
		}
	})

	t.Run("production invalidate cancels active done without clearing channel", func(t *testing.T) {
		sess := &codexWebsocketSession{}
		conn := &websocket.Conn{}
		// Use a capacity-1 channel so the terminal send hits the full-channel path.
		// activate() creates a large buffer which would take the non-blocking path.
		ch := make(chan codexWebsocketRead, 1)
		sess.setActive(conn, ch)
		ch <- codexWebsocketRead{payload: []byte("queued")}
		_, activeDone := sess.activeForConn(conn)

		finished := make(chan struct{})
		go func() {
			invalidated := sendTerminalWebsocketRead(ch, activeDone, codexWebsocketRead{err: terminalErr}, func() {
				// Mirror production invalidateUpstreamConn: cancel waiters first.
				sess.cancelActiveForConn(conn)
			})
			if !invalidated {
				t.Error("full channel should report early invalidation")
			}
			close(finished)
		}()

		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("sendTerminalWebsocketRead hung; production invalidate must cancel activeDone")
		}
		if len(ch) != 1 {
			t.Fatalf("channel length = %d, want queued payload only", len(ch))
		}
	})
}

func TestMapCodexWebsocketWriteErrorStopsRetryForMessageTooBig(t *testing.T) {
	networkWriteErr := errors.New("write: broken pipe")
	tests := []struct {
		name       string
		closeCode  int
		writeErr   error
		wantStatus int
		wantRetry  bool
	}{
		{
			name:       "close sent after message too big is request scoped",
			closeCode:  websocket.CloseMessageTooBig,
			writeErr:   websocket.ErrCloseSent,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantRetry:  false,
		},
		{
			name:       "network write error after message too big is request scoped",
			closeCode:  websocket.CloseMessageTooBig,
			writeErr:   networkWriteErr,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantRetry:  false,
		},
		{
			name:      "other close keeps stale connection retry",
			closeCode: websocket.CloseNormalClosure,
			writeErr:  websocket.ErrCloseSent,
			wantRetry: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := &codexWebsocketSession{}
			conn := &websocket.Conn{}
			sess.resetUpstreamDisconnectError(conn)
			sess.setUpstreamDisconnectError(conn, &websocket.CloseError{Code: tt.closeCode})

			mappedErr := mapCodexWebsocketWriteError(sess, conn, tt.writeErr)
			if got := shouldRetryCodexWebsocketSend(mappedErr); got != tt.wantRetry {
				t.Fatalf("shouldRetryCodexWebsocketSend() = %v, want %v; err=%v", got, tt.wantRetry, mappedErr)
			}
			if tt.wantStatus == 0 {
				if !errors.Is(mappedErr, tt.writeErr) {
					t.Fatalf("mapped error = %v, want %v", mappedErr, tt.writeErr)
				}
				return
			}
			statusError, ok := mappedErr.(interface{ StatusCode() int })
			if !ok || statusError.StatusCode() != tt.wantStatus {
				t.Fatalf("mapped status = %v, want %d; err=%v", statusError, tt.wantStatus, mappedErr)
			}
			requestErr, ok := mappedErr.(interface{ IsRequestScoped() bool })
			if !ok || !requestErr.IsRequestScoped() {
				t.Fatalf("mapped error should be request scoped, got %T", mappedErr)
			}
		})
	}
}

func TestMapCodexWebsocketWriteErrorDoesNotReusePriorConnectionClose(t *testing.T) {
	sess := &codexWebsocketSession{}
	priorConn := &websocket.Conn{}
	replacementConn := &websocket.Conn{}

	sess.resetUpstreamDisconnectError(priorConn)
	sess.setUpstreamDisconnectError(priorConn, &websocket.CloseError{Code: websocket.CloseMessageTooBig})
	priorErr := mapCodexWebsocketWriteError(sess, priorConn, websocket.ErrCloseSent)
	if shouldRetryCodexWebsocketSend(priorErr) {
		t.Fatalf("prior connection 1009 should not retry, got %v", priorErr)
	}

	sess.resetUpstreamDisconnectError(replacementConn)
	// A late close callback from the prior connection must not overwrite the
	// replacement connection's close state.
	sess.setUpstreamDisconnectError(priorConn, &websocket.CloseError{Code: websocket.CloseMessageTooBig})
	sess.setUpstreamDisconnectError(replacementConn, &websocket.CloseError{Code: websocket.CloseNormalClosure})
	replacementErr := mapCodexWebsocketWriteError(sess, replacementConn, websocket.ErrCloseSent)
	if !errors.Is(replacementErr, websocket.ErrCloseSent) {
		t.Fatalf("replacement connection error = %v, want %v", replacementErr, websocket.ErrCloseSent)
	}
	if !shouldRetryCodexWebsocketSend(replacementErr) {
		t.Fatalf("replacement connection should keep stale-connection retry, got %v", replacementErr)
	}
}

func TestMapCodexWebsocketWriteErrorWaitsForLateMessageTooBigClose(t *testing.T) {
	sess := &codexWebsocketSession{}
	conn := &websocket.Conn{}
	sess.resetUpstreamDisconnectError(conn)

	// Simulate the common race: WriteMessage fails with a network error before
	// the concurrent reader has delivered the peer's 1009 close frame.
	go func() {
		time.Sleep(5 * time.Millisecond)
		sess.setUpstreamDisconnectError(conn, &websocket.CloseError{Code: websocket.CloseMessageTooBig})
	}()

	mappedErr := mapCodexWebsocketWriteError(sess, conn, errors.New("write: broken pipe"))
	if shouldRetryCodexWebsocketSend(mappedErr) {
		t.Fatalf("late 1009 close should stop retry, got %v", mappedErr)
	}
	statusError, ok := mappedErr.(interface{ StatusCode() int })
	if !ok || statusError.StatusCode() != http.StatusRequestEntityTooLarge {
		t.Fatalf("mapped status = %v, want 413; err=%v", statusError, mappedErr)
	}
}

func TestMapCodexWebsocketReadErrorMapsMessageTooBig(t *testing.T) {
	mapped := mapCodexWebsocketReadError(&websocket.CloseError{Code: websocket.CloseMessageTooBig})
	statusError, ok := mapped.(interface{ StatusCode() int })
	if !ok || statusError.StatusCode() != http.StatusRequestEntityTooLarge {
		t.Fatalf("mapped status = %v, want 413", mapped)
	}
	requestErr, ok := mapped.(interface{ IsRequestScoped() bool })
	if !ok || !requestErr.IsRequestScoped() {
		t.Fatalf("mapped error should be request scoped, got %T", mapped)
	}
	otherErr := errors.New("other")
	if got := mapCodexWebsocketReadError(otherErr); !errors.Is(got, otherErr) {
		t.Fatalf("non-1009 error should pass through, got %v", got)
	}
}

func TestCodexWebsocketsExecuteHandshakeUsageLimitReachedSetsRetryAfter(t *testing.T) {
	body := []byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_in_seconds":120}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		if _, errWrite := w.Write(body); errWrite != nil {
			t.Errorf("write handshake rejection: %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: []byte(`{"model":"gpt-5-codex","input":[]}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("codex")}

	_, errExecute := exec.Execute(context.Background(), auth, req, opts)
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want handshake rejection")
	}
	statusErr, ok := errExecute.(interface{ StatusCode() int })
	if !ok || statusErr.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %#v, want 429", errExecute)
	}
	retryable, ok := errExecute.(interface{ RetryAfter() *time.Duration })
	if !ok || retryable.RetryAfter() == nil {
		t.Fatalf("expected RetryAfter for usage_limit_reached handshake error: %#v", errExecute)
	}
	if got := *retryable.RetryAfter(); got != 120*time.Second {
		t.Fatalf("RetryAfter = %v, want 120s", got)
	}
}

func TestCodexWebsocketsExecuteStreamHandshakeUsageLimitReachedSetsRetryAfter(t *testing.T) {
	body := []byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_in_seconds":120}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		if _, errWrite := w.Write(body); errWrite != nil {
			t.Errorf("write handshake rejection: %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: []byte(`{"model":"gpt-5-codex","input":[]}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("codex")}

	_, errExecuteStream := exec.ExecuteStream(context.Background(), auth, req, opts)
	if errExecuteStream == nil {
		t.Fatal("ExecuteStream() error = nil, want handshake rejection")
	}
	statusErr, ok := errExecuteStream.(interface{ StatusCode() int })
	if !ok || statusErr.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %#v, want 429", errExecuteStream)
	}
	retryable, ok := errExecuteStream.(interface{ RetryAfter() *time.Duration })
	if !ok || retryable.RetryAfter() == nil {
		t.Fatalf("expected RetryAfter for usage_limit_reached handshake error: %#v", errExecuteStream)
	}
	if got := *retryable.RetryAfter(); got != 120*time.Second {
		t.Fatalf("RetryAfter = %v, want 120s", got)
	}
}
func TestCodexWebsockets_PingHandlerDoesNotBlockOnWriteMu(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	serverConnCh := make(chan *websocket.Conn, 1)
	pongReceived := make(chan string, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		conn.SetPongHandler(func(appData string) error {
			pongReceived <- appData
			return nil
		})
		serverConnCh <- conn
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	clientConn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatalf("dial websocket failed: %v", errDial)
	}
	defer func() { _ = clientConn.Close() }()

	serverConn := <-serverConnCh
	defer func() { _ = serverConn.Close() }()

	sess := &codexWebsocketSession{sessionID: "test-keepalive"}
	sess.configureConn(clientConn)

	// Start client read loop so it processes control frames.
	go func() {
		for {
			if _, _, errRead := clientConn.ReadMessage(); errRead != nil {
				return
			}
		}
	}()

	// Simulate an active application message write holding writeMu.
	sess.writeMu.Lock()
	defer sess.writeMu.Unlock()

	// Upstream sends a keepalive ping while writeMu is held.
	errPing := serverConn.WriteControl(websocket.PingMessage, []byte("keepalive-ping"), time.Now().Add(time.Second))
	if errPing != nil {
		t.Fatalf("failed to send ping: %v", errPing)
	}

	// Pong must be received promptly without being starved by writeMu.
	select {
	case got := <-pongReceived:
		if got != "keepalive-ping" {
			t.Fatalf("unexpected pong payload: got %q, want keepalive-ping", got)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("pong response was blocked/starved while writeMu was held")
	}
}

func TestCodexWebsockets_KeepalivePingDuringUpload_WithSession(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	serverPongCh := make(chan string, 1)
	inWriteHook := make(chan struct{})
	pongDeliveredDuringWrite := make(chan struct{})

	testWebsocketWritePayloadHook = func(conn *websocket.Conn) {
		close(inWriteHook)
		// Wait until server confirms pong was received before allowing write to finish.
		select {
		case <-pongDeliveredDuringWrite:
		case <-time.After(2 * time.Second):
			t.Error("timed out waiting for pong delivery while payload write was held in hook")
		}
	}
	defer func() { testWebsocketWritePayloadHook = nil }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		conn.SetPongHandler(func(appData string) error {
			serverPongCh <- appData
			return nil
		})

		// Start server reader loop so server processes control frames.
		readErrCh := make(chan error, 1)
		go func() {
			for {
				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					readErrCh <- errRead
					return
				}
			}
		}()

		// Wait until client has entered writeMessage and is actively holding writeMu.
		select {
		case <-inWriteHook:
		case <-time.After(2 * time.Second):
			t.Errorf("timed out waiting for client write hook")
			return
		}

		// Upstream sends Ping WHILE client payload write is in progress holding writeMu.
		_ = conn.WriteControl(websocket.PingMessage, []byte("session-ping"), time.Now().Add(time.Second))

		// Server asserts Pong arrives while client write is still blocked in the hook.
		select {
		case got := <-serverPongCh:
			if got != "session-ping" {
				t.Errorf("unexpected pong payload: got %q, want session-ping", got)
			}
			close(pongDeliveredDuringWrite)
		case <-time.After(2 * time.Second):
			t.Errorf("pong was not received while payload write was in progress")
			return
		}

		// Now send terminal response.
		respPayload := []byte(`{"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[]}}`)
		_ = conn.WriteMessage(websocket.TextMessage, respPayload)
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{
		SDKConfig: config.SDKConfig{
			DisableImageGeneration: config.DisableImageGenerationAll,
		},
	})
	auth := &cliproxyauth.Auth{ID: "auth-session-ping", Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","input":[{"type":"message","role":"user","content":"ping test"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "session-ping-test",
		},
	}

	result, errStream := exec.ExecuteStream(context.Background(), auth, req, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() failed: %v", errStream)
	}

	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
	}
}

func TestCodexWebsockets_KeepalivePingDuringUpload_Sessionless(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	serverPongCh := make(chan string, 1)
	inWriteHook := make(chan struct{})
	pongDeliveredDuringWrite := make(chan struct{})

	testWebsocketWritePayloadHook = func(conn *websocket.Conn) {
		close(inWriteHook)
		select {
		case <-pongDeliveredDuringWrite:
		case <-time.After(2 * time.Second):
			t.Error("timed out waiting for pong delivery while payload write was held in hook")
		}
	}
	defer func() { testWebsocketWritePayloadHook = nil }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		conn.SetPongHandler(func(appData string) error {
			serverPongCh <- appData
			return nil
		})

		go func() {
			for {
				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					return
				}
			}
		}()

		// Wait until client has entered writeMessage on sessionless path.
		select {
		case <-inWriteHook:
		case <-time.After(2 * time.Second):
			t.Errorf("timed out waiting for client write hook")
			return
		}

		// Upstream sends Ping WHILE client payload write is in progress.
		_ = conn.WriteControl(websocket.PingMessage, []byte("sessionless-ping"), time.Now().Add(time.Second))

		// Server asserts Pong arrives while client write is still in progress.
		select {
		case got := <-serverPongCh:
			if got != "sessionless-ping" {
				t.Errorf("unexpected pong payload: got %q, want sessionless-ping", got)
			}
			close(pongDeliveredDuringWrite)
		case <-time.After(2 * time.Second):
			t.Errorf("pong was not received while payload write was in progress on sessionless connection")
			return
		}

		respPayload := []byte(`{"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[]}}`)
		_ = conn.WriteMessage(websocket.TextMessage, respPayload)
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{
		SDKConfig: config.SDKConfig{
			DisableImageGeneration: config.DisableImageGenerationAll,
		},
	})
	auth := &cliproxyauth.Auth{ID: "auth-sessionless-ping", Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","input":[{"type":"message","role":"user","content":"ping test sessionless"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
	}

	result, errStream := exec.ExecuteStream(context.Background(), auth, req, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() failed: %v", errStream)
	}

	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
	}
}

func TestCodexWebsockets_KeepalivePingDuringUpload_NonstreamSessionless(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	serverPongCh := make(chan string, 1)
	inWriteHook := make(chan struct{})
	pongDeliveredDuringWrite := make(chan struct{})

	testWebsocketWritePayloadHook = func(conn *websocket.Conn) {
		close(inWriteHook)
		select {
		case <-pongDeliveredDuringWrite:
		case <-time.After(2 * time.Second):
			t.Error("timed out waiting for pong delivery while nonstream payload write was held in hook")
		}
	}
	defer func() { testWebsocketWritePayloadHook = nil }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		conn.SetPongHandler(func(appData string) error {
			serverPongCh <- appData
			return nil
		})

		go func() {
			for {
				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					return
				}
			}
		}()

		// Wait until client has entered writeMessage on nonstream path.
		select {
		case <-inWriteHook:
		case <-time.After(2 * time.Second):
			t.Errorf("timed out waiting for client write hook")
			return
		}

		// Upstream sends Ping WHILE client payload write is in progress.
		_ = conn.WriteControl(websocket.PingMessage, []byte("nonstream-sessionless-ping"), time.Now().Add(time.Second))

		// Server asserts Pong arrives while client write is still in progress.
		select {
		case got := <-serverPongCh:
			if got != "nonstream-sessionless-ping" {
				t.Errorf("unexpected pong payload: got %q, want nonstream-sessionless-ping", got)
			}
			close(pongDeliveredDuringWrite)
		case <-time.After(2 * time.Second):
			t.Errorf("pong was not received while payload write was in progress on nonstream sessionless connection")
			return
		}

		respPayload := []byte(`{"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[]}}`)
		_ = conn.WriteMessage(websocket.TextMessage, respPayload)
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{
		SDKConfig: config.SDKConfig{
			DisableImageGeneration: config.DisableImageGenerationAll,
		},
	})
	auth := &cliproxyauth.Auth{ID: "auth-nonstream-ping", Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","input":[{"type":"message","role":"user","content":"ping test nonstream"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
	}

	resp, errExec := exec.Execute(context.Background(), auth, req, opts)
	if errExec != nil {
		t.Fatalf("Execute() failed: %v", errExec)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("Execute() returned empty payload")
	}
}

func TestCodexWebsockets_SessionlessBufferingImmediateTerminalClosesConnection(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	serverClosed := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() {
			_ = conn.Close()
			close(serverClosed)
		}()

		// Read client request.
		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			return
		}

		// Send immediate terminal event while buffering is enabled.
		respPayload := []byte(`{"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[]}}`)
		_ = conn.WriteMessage(websocket.TextMessage, respPayload)

		// Wait until client closes connection.
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer server.Close()

	bufferingCfg := &config.Config{}
	bufferingCfg.Streaming.CodexStreamBootstrapBuffering = true
	bufferingCfg.DisableImageGeneration = config.DisableImageGenerationAll
	exec := NewCodexWebsocketsExecutor(bufferingCfg)
	auth := &cliproxyauth.Auth{ID: "auth-buffering-close", Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","input":[{"type":"message","role":"user","content":"buffering close test"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
		// Sessionless
	}

	result, errStream := exec.ExecuteStream(context.Background(), auth, req, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() failed: %v", errStream)
	}

	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
	}

	// Server connection must be closed by client immediately upon terminal buffering.
	select {
	case <-serverClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("sessionless connection was not closed after immediate terminal buffering")
	}
}

func TestCodexWebsockets_LastEventAndTerminalTracking(t *testing.T) {
	conn1 := &websocket.Conn{}
	conn2 := &websocket.Conn{}
	sess := &codexWebsocketSession{sessionID: "track-session"}

	// Initially empty
	sess.resetUpstreamDisconnectError(conn1)
	if got := sess.getLastEventType(conn1); got != "" {
		t.Fatalf("initial lastEventType = %q, want empty", got)
	}

	// Non-terminal event
	sess.setLastEventType(conn1, "response.output_item.added")
	if got := sess.getLastEventType(conn1); got != "response.output_item.added" {
		t.Fatalf("lastEventType = %q, want response.output_item.added", got)
	}
	if isTerminalEvent(sess.getLastEventType(conn1)) {
		t.Fatalf("output_item.added should not be terminal")
	}

	// Terminal event
	sess.setLastEventType(conn1, "response.completed")
	if got := sess.getLastEventType(conn1); got != "response.completed" {
		t.Fatalf("lastEventType = %q, want response.completed", got)
	}
	if !isTerminalEvent(sess.getLastEventType(conn1)) {
		t.Fatalf("response.completed must be terminal")
	}

	// Reset for new connection resets tracking
	sess.resetUpstreamDisconnectError(conn2)
	if got := sess.getLastEventType(conn2); got != "" {
		t.Fatalf("reconnected lastEventType = %q, want empty", got)
	}
	// Old conn should not match
	if got := sess.getLastEventType(conn1); got != "" {
		t.Fatalf("stale conn lastEventType = %q, want empty", got)
	}
}

func TestCodexWebsockets_ChunkedWriteAllowsPongInterleaving(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	serverPongCh := make(chan string, 1)
	pongReceivedBeforeReadComplete := make(chan struct{})

	firstChunkReadOnServer := make(chan struct{})
	allowRemainingChunks := make(chan struct{})

	testWebsocketWriteChunkHook = func(chunkIndex int, totalChunks int) {
		if chunkIndex == 1 {
			// Chunk 0 was sent to the network. Now wait until server confirms it has
			// received chunk 0 and sent a keepalive Ping:
			select {
			case <-firstChunkReadOnServer:
			case <-time.After(5 * time.Second):
			}
			select {
			case <-allowRemainingChunks:
			case <-time.After(5 * time.Second):
			}
		}
	}
	defer func() { testWebsocketWriteChunkHook = nil }()

	// Large message that spans multiple 32KB chunks (128KB total).
	largeContent := strings.Repeat("A", 128*1024)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		conn.SetPongHandler(func(appData string) error {
			serverPongCh <- appData
			return nil
		})

		// Read the first chunk of data using NextReader, proving receipt of partial message on the wire.
		msgType, reader, errNext := conn.NextReader()
		if errNext != nil {
			t.Errorf("server NextReader error: %v", errNext)
			return
		}
		if msgType != websocket.TextMessage {
			t.Errorf("unexpected msgType: %d", msgType)
			return
		}

		firstChunk := make([]byte, 8192)
		n, errRead := io.ReadFull(reader, firstChunk)
		if errRead != nil || n < 8192 {
			t.Errorf("failed reading first chunk from wire: n=%d err=%v", n, errRead)
			return
		}

		// Server confirmed reading chunk 0 from the wire!
		close(firstChunkReadOnServer)

		// Server injects keepalive Ping while client is paused between chunks.
		_ = conn.WriteControl(websocket.PingMessage, []byte("chunked-interleaved-ping"), time.Now().Add(time.Second))

		// Goroutine to read reader so Gorilla processes the interleaved Pong frame.
		readDone := make(chan struct{})
		var totalMsg []byte
		var readErr error
		go func() {
			rest, errRest := io.ReadAll(reader)
			readErr = errRest
			totalMsg = append(firstChunk, rest...)
			close(readDone)
		}()

		// Server asserts Pong is received while remaining chunks are still paused.
		select {
		case got := <-serverPongCh:
			if got != "chunked-interleaved-ping" {
				t.Errorf("unexpected pong: got %q, want chunked-interleaved-ping", got)
			}
			close(pongReceivedBeforeReadComplete)
			close(allowRemainingChunks)
		case <-time.After(2 * time.Second):
			t.Errorf("pong was not received while client was paused between chunks")
			close(allowRemainingChunks)
			return
		}

		// Wait for read to finish now that allowRemainingChunks was closed.
		select {
		case <-readDone:
			if readErr != nil {
				t.Errorf("server ReadAll rest error: %v", readErr)
				return
			}
		case <-time.After(2 * time.Second):
			t.Errorf("timed out reading remaining message frames")
			return
		}

		if len(totalMsg) < 128*1024 {
			t.Errorf("total received payload too short: %d bytes", len(totalMsg))
			return
		}

		// Send terminal response.
		respPayload := []byte(`{"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[]}}`)
		_ = conn.WriteMessage(websocket.TextMessage, respPayload)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	clientConn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatalf("dial error: %v", errDial)
	}
	defer func() { _ = clientConn.Close() }()

	sess := &codexWebsocketSession{sessionID: "session-chunked-test"}
	sess.configureConn(clientConn)
	_ = sess.activate(clientConn)
	exec := NewCodexWebsocketsExecutor(&config.Config{})
	go exec.readUpstreamLoop(sess, clientConn)

	// Write 128KB message through production sess.writeMessage path:
	payload := []byte(largeContent)
	errWrite := sess.writeMessage(clientConn, websocket.TextMessage, payload)
	if errWrite != nil {
		t.Fatalf("writeMessage failed: %v", errWrite)
	}

	select {
	case <-pongReceivedBeforeReadComplete:
	case <-time.After(2 * time.Second):
		t.Fatal("pong was not received before message read completed")
	}
}

func TestCodexWebsockets_PingLoggingRedacted(t *testing.T) {
	origOut := log.StandardLogger().Out
	origLevel := log.GetLevel()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetLevel(log.DebugLevel)
	defer func() {
		log.SetOutput(origOut)
		log.SetLevel(origLevel)
	}()

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	clientConn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatalf("dial websocket failed: %v", errDial)
	}
	defer func() { _ = clientConn.Close() }()

	sess := &codexWebsocketSession{sessionID: "redact-session"}
	sess.configureConn(clientConn)

	sensitiveData := "SUPER-SECRET-PAYLOAD-12345"
	pingHandler := clientConn.PingHandler()
	if pingHandler == nil {
		t.Fatal("pingHandler is nil")
	}
	_ = pingHandler(sensitiveData)

	logOutput := buf.String()
	if strings.Contains(logOutput, sensitiveData) {
		t.Fatalf("log output leaked sensitive ping payload: %s", logOutput)
	}
	if !strings.Contains(logOutput, "ping_bytes=") {
		t.Fatalf("log output missing ping_bytes: %s", logOutput)
	}
}

func TestCodexWebsockets_SendErrorLogsSessionObject(t *testing.T) {
	origOut := log.StandardLogger().Out
	origLevel := log.GetLevel()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetLevel(log.DebugLevel)
	defer func() {
		log.SetOutput(origOut)
		log.SetLevel(origLevel)
	}()

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer server.Close()

	// Deterministically cause send error by expiring write deadline right before writing.
	testWebsocketWritePayloadHook = func(conn *websocket.Conn) {
		_ = conn.SetWriteDeadline(time.Now().Add(-time.Second))
	}
	defer func() { testWebsocketWritePayloadHook = nil }()

	exec := NewCodexWebsocketsExecutor(&config.Config{
		SDKConfig: config.SDKConfig{
			DisableImageGeneration: config.DisableImageGenerationAll,
		},
	})
	auth := &cliproxyauth.Auth{ID: "auth-send-fail", Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","input":[{"type":"message","role":"user","content":"send fail"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
		// Sessionless -> ephemeral
	}

	result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err == nil && result != nil {
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				err = chunk.Err
			}
		}
	}
	if err == nil {
		t.Fatal("expected ExecuteStream to fail when connection is closed before send")
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "session_object=ephemeral") {
		t.Fatalf("expected session_object=ephemeral in log output, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "reason=send_error") {
		t.Fatalf("expected reason=send_error in log output, got: %s", logOutput)
	}
}
func TestCodexWebsocketZeroTokenIncompleteReleasesSessionRequestLock(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()
		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			return
		}
		terminal := []byte(`{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":10,"output_tokens":0,"total_tokens":10}}}`)
		_ = conn.WriteMessage(websocket.TextMessage, terminal)
	}))
	defer server.Close()

	bufferingCfg := &config.Config{}
	bufferingCfg.Streaming.CodexStreamBootstrapBuffering = true
	bufferingCfg.DisableImageGeneration = config.DisableImageGenerationAll
	exec := NewCodexWebsocketsExecutor(bufferingCfg)
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	auth := &cliproxyauth.Auth{ID: "auth-a", Provider: "codex", Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: []byte(`{"model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"hello"}]}`)}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "zero-token-session",
		},
	}

	result, errExecute := exec.ExecuteStream(context.Background(), auth, req, opts)
	if errExecute == nil && result != nil {
		for chunk := range result.Chunks {
			_ = chunk
		}
	}

	sess := exec.getOrCreateSession("zero-token-session")
	acquired := make(chan struct{})
	go func() {
		sess.reqMu.Lock()
		defer sess.reqMu.Unlock()
		close(acquired)
	}()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("failed to acquire session request lock after zero-token incomplete failure")
	}
}
