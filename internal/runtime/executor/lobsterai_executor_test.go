package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

const testLobsterAIBase = "https://lobsterai-fixture.example"

// fixtureToken builds a non-secret placeholder at runtime so no credential-like
// literal is committed to the repository.
func fixtureToken(name string) string {
	return strings.Join([]string{"fixture", name}, "-")
}

// lobsterAITestAuth builds an auth record bound to a fixture upstream.
func lobsterAITestAuth(baseURL string) *cliproxyauth.Auth {
	auth := &cliproxyauth.Auth{
		ID:       "lobsterai-test",
		Provider: "lobsterai",
		Metadata: map[string]any{
			"access_token":  fixtureToken("access"),
			"refresh_token": fixtureToken("refresh"),
			"user_id":       "yid-fixture",
		},
		Attributes: map[string]string{"auth_kind": "oauth"},
	}
	if baseURL != "" {
		auth.Metadata["base_url"] = baseURL
		auth.Attributes["base_url"] = baseURL
	}
	return auth
}

// allowLoopbackBaseURLForTest relaxes the SSRF guard so httptest servers (which
// bind to loopback) can act as the upstream within a test.
func allowLoopbackBaseURLForTest(t *testing.T) {
	t.Helper()
	original := lobsterAIValidateBaseURL
	lobsterAIValidateBaseURL = func(raw string) (string, error) {
		return strings.TrimRight(strings.TrimSpace(raw), "/"), nil
	}
	t.Cleanup(func() { lobsterAIValidateBaseURL = original })
}

func TestResolveBaseURLRejectsUnsafeOverride(t *testing.T) {
	// Production validation must reject a loopback override.
	if _, errResolve := resolveBaseURL("http://127.0.0.1:9000"); errResolve == nil {
		t.Fatal("resolveBaseURL accepted a loopback override")
	}
	if got, errResolve := resolveBaseURL(""); errResolve != nil || got != "https://lobsterai-server.youdao.com" {
		t.Fatalf("resolveBaseURL(\"\") = %q, %v; want the default upstream", got, errResolve)
	}
}

func TestLobsterAIPrepareRequestSetsIdentityHeaders(t *testing.T) {
	executor := NewLobsterAIExecutor(&config.Config{})
	req, errNew := http.NewRequest(http.MethodPost, "https://example.com/api/proxy/v1/chat/completions", nil)
	if errNew != nil {
		t.Fatalf("build request: %v", errNew)
	}
	if errPrepare := executor.PrepareRequest(req, lobsterAITestAuth("")); errPrepare != nil {
		t.Fatalf("PrepareRequest: %v", errPrepare)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer "+fixtureToken("access") {
		t.Fatalf("Authorization = %q, want bearer access token", got)
	}
	if got := req.Header.Get("X-LobsterAI-Client-Capabilities"); !strings.Contains(got, "kimi-k3-agentic-v1") {
		t.Fatalf("capabilities header = %q, want the agentic capability", got)
	}
	if got := req.Header.Get("X-LobsterAI-Client-Version"); got == "" {
		t.Fatal("client version header is empty")
	}
}

func TestLobsterAIPrepareRequestRejectsMissingToken(t *testing.T) {
	executor := NewLobsterAIExecutor(&config.Config{})
	req, _ := http.NewRequest(http.MethodPost, "https://example.com", nil)
	if errPrepare := executor.PrepareRequest(req, &cliproxyauth.Auth{}); errPrepare == nil {
		t.Fatal("PrepareRequest accepted an auth without an access token")
	}
}

// assertLobsterAIUpstreamAuth pins the headers every upstream chat request must
// carry. helps.DoStream forwards the supplied headers verbatim and injects no
// credentials, so a missing bearer token reaches the upstream as an anonymous
// request that it answers with its "session expired" error.
func assertLobsterAIUpstreamAuth(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("Authorization"); got != "Bearer "+fixtureToken("access") {
		t.Fatalf("upstream Authorization = %q, want the credential bearer token", got)
	}
	if got := r.Header.Get("X-LobsterAI-Client-Capabilities"); !strings.Contains(got, "kimi-k3-agentic-v1") {
		t.Fatalf("upstream capabilities header = %q, want the agentic capability", got)
	}
	if got := r.Header.Get("X-LobsterAI-Client-Version"); got == "" {
		t.Fatal("upstream client version header is empty")
	}
	if got := r.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("upstream Content-Type = %q, want application/json", got)
	}
}

func TestForceLobsterAIStreamAlwaysEnablesStreaming(t *testing.T) {
	updated := forceLobsterAIStream([]byte(`{"model":"glm-5","stream":false}`))
	if !strings.Contains(string(updated), `"stream":true`) {
		t.Fatalf("body = %s, want stream forced to true", updated)
	}
}

func TestApplyLobsterAIUpstreamModelStripsNamespaceAndSuffix(t *testing.T) {
	cases := map[string]string{
		"lobsterai/glm-5":      "glm-5",
		"glm-5(high)":          "glm-5",
		"lobsterai/glm-5(max)": "glm-5",
		"deepseek-v4-pro":      "deepseek-v4-pro",
	}
	for input, want := range cases {
		updated := applyLobsterAIUpstreamModel([]byte(`{"model":"placeholder"}`), input)
		var decoded struct {
			Model string `json:"model"`
		}
		if errUnmarshal := json.Unmarshal(updated, &decoded); errUnmarshal != nil {
			t.Fatalf("unmarshal %q: %v", input, errUnmarshal)
		}
		if decoded.Model != want {
			t.Fatalf("model for %q = %q, want %q", input, decoded.Model, want)
		}
	}
}

const lobsterAIStreamFixture = `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1753600000,"model":"glm-5","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1753600000,"model":"glm-5","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1753600000,"model":"glm-5","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1753600000,"model":"glm-5","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"weather\"}"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1753600000,"model":"glm-5","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}

data: [DONE]
`

func TestAggregateLobsterAIStreamMergesDeltasAndToolCalls(t *testing.T) {
	aggregated, usageDetail, errAggregate := aggregateLobsterAIStream(strings.NewReader(lobsterAIStreamFixture), "glm-5")
	if errAggregate != nil {
		t.Fatalf("aggregateLobsterAIStream: %v", errAggregate)
	}
	var decoded struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Role      string `json:"role"`
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
			TotalTokens      int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if errUnmarshal := json.Unmarshal(aggregated, &decoded); errUnmarshal != nil {
		t.Fatalf("unmarshal aggregated response: %v", errUnmarshal)
	}
	if decoded.Object != "chat.completion" {
		t.Fatalf("object = %q, want chat.completion", decoded.Object)
	}
	if decoded.ID != "chatcmpl-1" || decoded.Model != "glm-5" {
		t.Fatalf("id/model = %q/%q, want chatcmpl-1/glm-5", decoded.ID, decoded.Model)
	}
	if len(decoded.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(decoded.Choices))
	}
	choice := decoded.Choices[0]
	if choice.Message.Content != "Hello" {
		t.Fatalf("content = %q, want Hello", choice.Message.Content)
	}
	if choice.Message.Role != "assistant" {
		t.Fatalf("role = %q, want assistant", choice.Message.Role)
	}
	if choice.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls", choice.FinishReason)
	}
	if len(choice.Message.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %d, want 1", len(choice.Message.ToolCalls))
	}
	call := choice.Message.ToolCalls[0]
	if call.ID != "call-1" || call.Type != "function" || call.Function.Name != "lookup" {
		t.Fatalf("unexpected tool call %#v", call)
	}
	if call.Function.Arguments != `{"q":"weather"}` {
		t.Fatalf("arguments = %q, want the concatenated fragments", call.Function.Arguments)
	}
	if decoded.Usage.TotalTokens != 18 || usageDetail.TotalTokens != 18 {
		t.Fatalf("usage = %+v (detail %+v), want 18 total tokens", decoded.Usage, usageDetail)
	}
}

func TestAggregateLobsterAIStreamKeepsReasoningContent(t *testing.T) {
	stream := `data: {"id":"c2","model":"glm-5","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"thinking"},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"c2","model":"glm-5","choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	aggregated, _, errAggregate := aggregateLobsterAIStream(strings.NewReader(stream), "glm-5")
	if errAggregate != nil {
		t.Fatalf("aggregateLobsterAIStream: %v", errAggregate)
	}
	var decoded struct {
		Choices []struct {
			Message struct {
				ReasoningContent string `json:"reasoning_content"`
				Content          string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if errUnmarshal := json.Unmarshal(aggregated, &decoded); errUnmarshal != nil {
		t.Fatalf("unmarshal aggregated response: %v", errUnmarshal)
	}
	if len(decoded.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(decoded.Choices))
	}
	if decoded.Choices[0].Message.ReasoningContent != "thinking" {
		t.Fatalf("reasoning_content = %q, want thinking", decoded.Choices[0].Message.ReasoningContent)
	}
	if decoded.Choices[0].Message.Content != "answer" {
		t.Fatalf("content = %q, want answer", decoded.Choices[0].Message.Content)
	}
}

func TestAggregateLobsterAIStreamRejectsEmptyBody(t *testing.T) {
	if _, _, errAggregate := aggregateLobsterAIStream(strings.NewReader(""), "glm-5"); errAggregate == nil {
		t.Fatal("aggregateLobsterAIStream accepted an empty stream")
	}
}

func TestLobsterAIStreamErrorSurfacesBusinessFailures(t *testing.T) {
	frame := []byte(`data: {"code":40100,"message":"登录已过期"}`)
	errFrame := lobsterAIStreamError(frame, "glm-5")
	if errFrame == nil {
		t.Fatal("lobsterAIStreamError returned nil for an expired session frame")
	}
	statusErr, ok := errFrame.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error type %T does not expose StatusCode()", errFrame)
	}
	if statusErr.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", statusErr.StatusCode())
	}

	quotaFrame := []byte(`data: {"code":40202,"message":"积分额度已用完"}`)
	quotaErr := lobsterAIStreamError(quotaFrame, "glm-5")
	quotaStatus, ok := quotaErr.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error type %T does not expose StatusCode()", quotaErr)
	}
	if quotaStatus.StatusCode() != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", quotaStatus.StatusCode())
	}

	// Regular content frames and the terminator must not be treated as errors.
	if errFrame := lobsterAIStreamError([]byte(`data: {"id":"x","choices":[]}`), "glm-5"); errFrame != nil {
		t.Fatalf("lobsterAIStreamError flagged a content frame: %v", errFrame)
	}
	if errFrame := lobsterAIStreamError([]byte("data: [DONE]"), "glm-5"); errFrame != nil {
		t.Fatalf("lobsterAIStreamError flagged the stream terminator: %v", errFrame)
	}
	// A frame carrying choices is a content frame even if it mentions an error field.
	if errFrame := lobsterAIStreamError([]byte(`data: {"choices":[{"delta":{}}],"error":{"message":"x"}}`), "glm-5"); errFrame != nil {
		t.Fatalf("lobsterAIStreamError flagged a content frame with an error field: %v", errFrame)
	}
	// The user endpoints answer HTTP 200 with code -1 for a rejected credential.
	notLoggedIn := lobsterAIStreamError([]byte(`{"code":-1,"message":"未登录","data":null}`), "glm-5")
	if notLoggedIn == nil {
		t.Fatal("lobsterAIStreamError returned nil for a not-logged-in envelope")
	}
	notLoggedInStatus, ok := notLoggedIn.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error type %T does not expose StatusCode()", notLoggedIn)
	}
	if notLoggedInStatus.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", notLoggedInStatus.StatusCode())
	}
}

// TestAggregateLobsterAIStreamSurfacesErrorEnvelope covers an upstream that
// answers HTTP 200 with a JSON error envelope instead of an SSE stream.
func TestAggregateLobsterAIStreamSurfacesErrorEnvelope(t *testing.T) {
	body := `{"code":40100,"message":"登录已过期，请重新登录","data":null}`
	_, _, errAggregate := aggregateLobsterAIStream(strings.NewReader(body), "glm-5")
	if errAggregate == nil {
		t.Fatal("aggregateLobsterAIStream returned nil error for an error envelope")
	}
	statusErr, ok := errAggregate.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error type %T does not expose StatusCode()", errAggregate)
	}
	if statusErr.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", statusErr.StatusCode())
	}
}

func TestLobsterAIExecutorExecuteStreamForcesStreamingUpstream(t *testing.T) {
	allowLoopbackBaseURLForTest(t)

	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertLobsterAIUpstreamAuth(t, r)
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"c3\",\"model\":\"glm-5\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewLobsterAIExecutor(&config.Config{})
	auth := lobsterAITestAuth(server.URL)
	req := cliproxyexecutor.Request{
		Model:   "glm-5",
		Payload: []byte(`{"model":"glm-5","messages":[{"role":"user","content":"hi"}],"stream":false}`),
	}
	opts := cliproxyexecutor.Options{
		Stream:       true,
		SourceFormat: sdktranslator.FormatOpenAI,
	}
	result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream: %v", errStream)
	}
	if result == nil || result.Chunks == nil {
		t.Fatal("ExecuteStream returned no chunk stream")
	}
	var chunks []string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		chunks = append(chunks, string(chunk.Payload))
	}
	if gotPath != "/api/proxy/v1/chat/completions" {
		t.Fatalf("upstream path = %q, want the official proxy path", gotPath)
	}
	if gotBody["stream"] != true {
		t.Fatalf("upstream stream flag = %#v, want true", gotBody["stream"])
	}
	if gotBody["model"] != "glm-5" {
		t.Fatalf("upstream model = %#v, want glm-5", gotBody["model"])
	}
	if len(chunks) == 0 {
		t.Fatal("ExecuteStream produced no chunks")
	}
}

func TestLobsterAIExecutorExecuteAggregatesNonStreaming(t *testing.T) {
	allowLoopbackBaseURLForTest(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertLobsterAIUpstreamAuth(t, r)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"c4\",\"model\":\"glm-5\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"c4\",\"model\":\"glm-5\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewLobsterAIExecutor(&config.Config{})
	auth := lobsterAITestAuth(server.URL)
	req := cliproxyexecutor.Request{
		Model:   "glm-5",
		Payload: []byte(`{"model":"glm-5","messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}
	response, errExecute := executor.Execute(context.Background(), auth, req, opts)
	if errExecute != nil {
		t.Fatalf("Execute: %v", errExecute)
	}
	var decoded struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if errUnmarshal := json.Unmarshal(response.Payload, &decoded); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v", errUnmarshal)
	}
	if len(decoded.Choices) != 1 || decoded.Choices[0].Message.Content != "ok" {
		t.Fatalf("unexpected aggregated response %s", response.Payload)
	}
}

func TestLobsterAIExecutorRefreshRotatesTokenAndSignals(t *testing.T) {
	allowLoopbackBaseURLForTest(t)

	rotatedAccess := fixtureToken("rotated-access")
	rotatedRefresh := fixtureToken("rotated-refresh")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/auth/refresh":
			payload := fmt.Sprintf(
				`{"code":0,"data":{"accessToken":%q,"refreshToken":%q,"expiresIn":3600}}`,
				rotatedAccess, rotatedRefresh,
			)
			_, _ = w.Write([]byte(payload))
		case "/api/user/profile-summary":
			_, _ = w.Write([]byte(`{"code":0,"data":{"totalCreditsRemaining":42}}`))
		case "/api/user/quota":
			_, _ = w.Write([]byte(`{"code":0,"data":{"monthlyCreditsLimit":100,"monthlyCreditsUsed":58,"planName":"Standard","subscriptionStatus":"active"}}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	executor := NewLobsterAIExecutor(&config.Config{})
	auth := lobsterAITestAuth(server.URL)
	updated, errRefresh := executor.Refresh(context.Background(), auth)
	if errRefresh != nil {
		t.Fatalf("Refresh: %v", errRefresh)
	}
	if updated == auth {
		t.Fatal("Refresh returned the original auth pointer, want a clone")
	}
	if got, _ := updated.Metadata["access_token"].(string); got != rotatedAccess {
		t.Fatalf("access_token = %q, want the rotated token", got)
	}
	if got, _ := updated.Metadata["refresh_token"].(string); got != rotatedRefresh {
		t.Fatalf("refresh_token = %q, want the rotated token", got)
	}
	if _, ok := updated.Metadata["expires_at"].(string); !ok {
		t.Fatalf("expires_at = %#v, want an RFC3339 string", updated.Metadata["expires_at"])
	}
	if got := updated.Quota.Signals["total_credits_remaining"]; got != "42" {
		t.Fatalf("total_credits_remaining = %q, want 42", got)
	}
	if got := updated.Quota.Signals["plan"]; got != "Standard" {
		t.Fatalf("plan = %q, want Standard", got)
	}
	// The original record must stay untouched so a failed save cannot corrupt state.
	if auth.Metadata["access_token"] != fixtureToken("access") {
		t.Fatal("Refresh mutated the original auth metadata")
	}
}

func TestLobsterAIExecutorRefreshSurfacesSessionExpiry(t *testing.T) {
	allowLoopbackBaseURLForTest(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":40100,"message":"登录已过期，请重新登录"}`))
	}))
	defer server.Close()

	executor := NewLobsterAIExecutor(&config.Config{})
	auth := lobsterAITestAuth(server.URL)
	updated, errRefresh := executor.Refresh(context.Background(), auth)
	if errRefresh == nil {
		t.Fatal("Refresh returned nil error for a rejected refresh token")
	}
	if updated != auth {
		t.Fatal("Refresh must return the original auth on failure")
	}
}

func TestLobsterAIExecutorAppliesThinkingEffort(t *testing.T) {
	allowLoopbackBaseURLForTest(t)

	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertLobsterAIUpstreamAuth(t, r)
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"c5\",\"model\":\"glm-5\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewLobsterAIExecutor(&config.Config{})
	auth := lobsterAITestAuth(server.URL)
	// The thinking suffix must reach the upstream body as a discrete level.
	req := cliproxyexecutor.Request{
		Model:   "glm-5(high)",
		Payload: []byte(`{"model":"glm-5(high)","messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}
	if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
		t.Fatalf("Execute: %v", errExecute)
	}
	if gotBody["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %#v, want high", gotBody["reasoning_effort"])
	}
	// The suffix must be stripped from the wire model id.
	if gotBody["model"] != "glm-5" {
		t.Fatalf("model = %#v, want glm-5 without the suffix", gotBody["model"])
	}
}
