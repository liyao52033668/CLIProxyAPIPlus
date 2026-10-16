package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestIsOpenCodeBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		want    bool
	}{
		{"apex domain", "https://opencode.ai/v1", true},
		{"no path", "https://opencode.ai", true},
		{"inference path", "https://opencode.ai/inference", true},
		{"www subdomain", "https://www.opencode.ai/v1", true},
		{"api subdomain", "https://api.opencode.ai/v1", true},
		{"uppercase host", "https://OpenCode.AI/v1", true},
		{"http scheme", "http://opencode.ai/v1", true},
		{"suffix impersonation", "https://opencode.ai.evil.com/v1", false},
		{"prefix lookalike", "https://notopencode.ai/v1", false},
		{"unrelated host", "https://openrouter.ai/api/v1", false},
		{"localhost", "http://127.0.0.1:8080/v1", false},
		{"empty", "", false},
		{"no host", "not a url", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isOpenCodeBaseURL(tc.baseURL); got != tc.want {
				t.Fatalf("isOpenCodeBaseURL(%q) = %v, want %v", tc.baseURL, got, tc.want)
			}
		})
	}
}

// descendingTimePart matches the layout Identifier.descending() mints: low 12
// hex digits end in f[c-f]X with counter in the 1..63 window.
var descendingTimePart = regexp.MustCompile(`^ses_[0-9a-f]{9}f[c-f][0-9a-f][0-9A-Za-z]{14}$`)

func TestOpenCodeHeaderFingerprint(t *testing.T) {
	executor := NewOpenAICompatExecutor("openai-compatibility", nil)
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "session-1"},
	}

	httpReq := httptest.NewRequest("POST", "https://opencode.ai/inference/v1/chat/completions", nil)
	executor.applyOpenCodeSessionHeader(httpReq, "https://opencode.ai/inference", cliproxyexecutor.Request{}, opts)

	if got := httpReq.Header.Get("User-Agent"); got != "opencode/latest/2.0.29/cli" {
		t.Fatalf("expected OpenCode cli User-Agent, got %q", got)
	}
	if got := httpReq.Header.Get("x-opencode-client"); got != "cli" {
		t.Fatalf("expected x-opencode-client=cli, got %q", got)
	}
	if got := httpReq.Header.Get("x-opencode-project"); !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(got) {
		t.Fatalf("expected bare 40-hex project id, got %q", got)
	}
	session := httpReq.Header.Get("x-opencode-session")
	if !descendingTimePart.MatchString(session) {
		t.Fatalf("session id %q does not match the descending ses_ layout", session)
	}
	for _, header := range []string{"x-opencode-session-id", "x-session-affinity", "X-Session-Id"} {
		if got := httpReq.Header.Get(header); got != session {
			t.Fatalf("expected %s=%q, got %q", header, session, got)
		}
	}

	// Same identity => same session id.
	httpReqAgain := httptest.NewRequest("POST", "https://opencode.ai/inference/v1/chat/completions", nil)
	executor.applyOpenCodeSessionHeader(httpReqAgain, "https://opencode.ai/inference", cliproxyexecutor.Request{}, opts)
	if got := httpReqAgain.Header.Get("x-opencode-session"); got != session {
		t.Fatalf("same session produced different ids: %q vs %q", session, got)
	}

	// Different identity => different id.
	optsOther := cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "session-2"},
	}
	httpReqOther := httptest.NewRequest("POST", "https://opencode.ai/inference/v1/chat/completions", nil)
	executor.applyOpenCodeSessionHeader(httpReqOther, "https://opencode.ai/inference", cliproxyexecutor.Request{}, optsOther)
	if httpReqOther.Header.Get("x-opencode-session") == session {
		t.Fatalf("different sessions produced the same id")
	}
}

func TestOpenCodeHeaderDerivesFromRequest(t *testing.T) {
	executor := NewOpenAICompatExecutor("openai-compatibility", nil)

	httpReq := httptest.NewRequest("POST", "https://opencode.ai/inference/v1/chat/completions", nil)
	optsHeader := cliproxyexecutor.Options{Headers: http.Header{}}
	optsHeader.Headers.Set("X-Session-ID", "client-session-abc")
	executor.applyOpenCodeSessionHeader(httpReq, "https://opencode.ai/inference", cliproxyexecutor.Request{}, optsHeader)
	if got := httpReq.Header.Get("x-opencode-session"); !descendingTimePart.MatchString(got) {
		t.Fatalf("derived session id %q does not match the descending ses_ layout", got)
	}
	if got := httpReq.Header.Get("User-Agent"); got != "opencode/latest/2.0.29/cli" {
		t.Fatalf("expected OpenCode cli User-Agent, got %q", got)
	}

	// Message-content hash fallback still yields a valid id.
	httpReqHashed := httptest.NewRequest("POST", "https://opencode.ai/inference/v1/chat/completions", nil)
	executor.applyOpenCodeSessionHeader(httpReqHashed, "https://opencode.ai/inference", cliproxyexecutor.Request{
		Payload: []byte(`{"model":"m","messages":[{"role":"user","content":"你是什么模型"}]}`),
	}, cliproxyexecutor.Options{})
	if got := httpReqHashed.Header.Get("x-opencode-session"); !descendingTimePart.MatchString(got) {
		t.Fatalf("hashed session id %q does not match the descending ses_ layout", got)
	}
}

func TestOpenCodeHeaderNonOpenCode(t *testing.T) {
	executor := NewOpenAICompatExecutor("openai-compatibility", nil)

	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "session-1"},
	}
	httpReq := httptest.NewRequest("POST", "https://openrouter.ai/api/v1/chat/completions", nil)
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	executor.applyOpenCodeSessionHeader(httpReq, "https://openrouter.ai/api/v1", cliproxyexecutor.Request{}, opts)
	if got := httpReq.Header.Get("x-opencode-session"); got != "" {
		t.Fatalf("expected no session header for non-OpenCode base URL, got %q", got)
	}
	if got := httpReq.Header.Get("User-Agent"); got != "cli-proxy-openai-compat" {
		t.Fatalf("User-Agent must stay untouched for non-OpenCode base URL, got %q", got)
	}

	httpReqEmpty := httptest.NewRequest("POST", "https://opencode.ai/v1/chat/completions", nil)
	executor.applyOpenCodeSessionHeader(httpReqEmpty, "", cliproxyexecutor.Request{}, opts)
	if got := httpReqEmpty.Header.Get("x-opencode-session"); got != "" {
		t.Fatalf("expected no header for empty base URL, got %q", got)
	}
}

func TestIsOpenCodeAnonymousLane(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		apiKey  string
		want    bool
	}{
		{"anonymous free tier", "https://opencode.ai/inference", "public", true},
		{"public with whitespace", "https://opencode.ai/inference", " public ", true},
		{"paid key untouched", "https://opencode.ai/inference", "oc_abc123", false},
		{"public on other host", "https://example.com/v1", "public", false},
		{"empty key", "https://opencode.ai/inference", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isOpenCodeAnonymousLane(tc.baseURL, tc.apiKey); got != tc.want {
				t.Fatalf("isOpenCodeAnonymousLane(%q, %q) = %v, want %v", tc.baseURL, tc.apiKey, got, tc.want)
			}
		})
	}
}

func TestOpenCodePaidKeyBodyUntouched(t *testing.T) {
	// A paid key rides the standard upstream contract: no body shaping, no
	// injected tools, no stream_options on a non-streaming request.
	var gotBody []byte
	var gotUA string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "compat",
			BaseURL: server.URL,
		}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     server.URL,
			"api_key":      "oc_" + strings.Repeat("k", 12),
			"compat_name":  "compat",
			"provider_key": "compat",
		},
	}
	// The base URL cannot point at the real opencode.ai from a test, so this
	// asserts the non-OpenCode path of the same condition branch.
	if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "glm-5.3-flash",
		Payload: []byte(`{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if _, has := payload["tools"]; has {
		t.Fatalf("paid-key request must not receive injected tools")
	}
	if _, has := payload["stream_options"]; has {
		t.Fatalf("paid-key non-streaming request must not gain stream_options")
	}
	if gotUA != "cli-proxy-openai-compat" {
		t.Fatalf("paid-key request must keep the default User-Agent, got %q", gotUA)
	}
}

func TestOpenCodePrepareAnonymousBody(t *testing.T) {
	// Non-streaming chat body: stream forced on, usage attached, core tools added.
	body := []byte(`{"model":"glm-5.3-flash-free","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	out := helps.OpenCodePrepareAnonymousBody(body)
	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("shaped body is not valid JSON: %v", err)
	}
	if stream, _ := payload["stream"].(bool); !stream {
		t.Fatalf("expected stream=true in shaped body")
	}
	options, _ := payload["stream_options"].(map[string]any)
	if usage, _ := options["include_usage"].(bool); !usage {
		t.Fatalf("expected stream_options.include_usage=true in shaped body")
	}
	tools, _ := payload["tools"].([]any)
	if len(tools) != 5 {
		t.Fatalf("expected 5 core tools injected, got %d", len(tools))
	}
	names := map[string]bool{}
	for _, item := range tools {
		entry, _ := item.(map[string]any)
		fn, _ := entry["function"].(map[string]any)
		name, _ := fn["name"].(string)
		names[name] = true
		if fn["description"] == nil || fn["parameters"] == nil {
			t.Fatalf("tool %q must carry the official description and parameters", name)
		}
	}
	for _, expected := range []string{"shell", "edit", "glob", "grep", "read"} {
		if !names[expected] {
			t.Fatalf("expected core tool %q in the shaped body", expected)
		}
	}

	// Client-declared tools are preserved, only missing core tools appended.
	bodyWithTool := []byte(`{"model":"m","stream":true,"tools":[{"type":"function","function":{"name":"my_tool","description":"custom","parameters":{"type":"object"}}}]}`)
	out2 := helps.OpenCodePrepareAnonymousBody(bodyWithTool)
	var payload2 map[string]any
	if err := json.Unmarshal(out2, &payload2); err != nil {
		t.Fatalf("shaped body is not valid JSON: %v", err)
	}
	tools2, _ := payload2["tools"].([]any)
	if len(tools2) != 6 {
		t.Fatalf("expected client tool kept + 5 core tools, got %d", len(tools2))
	}

	// Already-complete body is returned unchanged.
	complete := helps.OpenCodePrepareAnonymousBody(out)
	if string(complete) != string(out) {
		t.Fatalf("expected idempotent shaping for a complete body")
	}

	// Invalid JSON passes through.
	garbage := []byte("not json")
	if got := helps.OpenCodePrepareAnonymousBody(garbage); string(got) != "not json" {
		t.Fatalf("expected garbage passthrough, got %q", got)
	}
}

func TestOpenCodeEndToEndHeadersOnServer(t *testing.T) {
	// Drive the real Execute path against a test server to confirm the header
	// fingerprint and shaped body arrive together. (The upstream host check
	// cannot be triggered by httptest, so this asserts the non-OpenCode path
	// leaves bodies untouched.)
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "compat",
			BaseURL: server.URL,
		}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     server.URL,
			"api_key":      "oc_" + strings.Repeat("k", 12),
			"compat_name":  "compat",
			"provider_key": "compat",
		},
	}

	if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "some-model",
		Payload: []byte(`{"model":"some-model","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if _, has := payload["tools"]; has {
		t.Fatalf("non-OpenCode upstream must not receive injected tools")
	}
	if _, has := payload["stream_options"]; has {
		t.Fatalf("non-OpenCode non-streaming request must not gain stream_options")
	}
}
