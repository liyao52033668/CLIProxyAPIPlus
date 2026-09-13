package executor

import (
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// TestClaudeCodeCLIBetas_MatchesObservedClientMatrix pins the dynamic
// Anthropic-Beta assembly against the measured Claude Code 2.1.258 wire order.
// The mid-conversation-system beta and the probe/helper gating of the upstream
// matrix are omitted: this fork has no mid-conversation system splicing or
// Haiku helper transport subsystems.
func TestClaudeCodeCLIBetas_MatchesObservedClientMatrix(t *testing.T) {
	const constants = "claude-code-20250219,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05"

	tests := []struct {
		name      string
		body      string
		requested map[string]bool
		oauth     bool
		want      string
	}{
		{
			name: "legacy model without tools omits both conditional betas",
			body: `{"model":"claude-opus-4-6"}`,
			want: constants + ",effort-2025-11-24",
		},
		{
			name:      "context 1m sits right after claude-code, not at the end",
			body:      `{"model":"claude-opus-4-6"}`,
			requested: map[string]bool{claudeContext1MBeta: true},
			want: "claude-code-20250219,context-1m-2025-08-07," +
				"interleaved-thinking-2025-05-14,redact-thinking-2026-02-12," +
				"thinking-token-count-2026-05-13,context-management-2025-06-27," +
				"prompt-caching-scope-2026-01-05,effort-2025-11-24",
		},
		{
			name: "opus-5 1m variant reproduces the full observed order",
			body: `{"model":"claude-opus-5","tools":[{"name":"Read","defer_loading":true}]}`,
			requested: map[string]bool{
				claudeContext1MBeta:          true,
				claudeServerSideFallbackBeta: true,
				claudeFallbackCreditBeta:     true,
			},
			want: "claude-code-20250219,context-1m-2025-08-07," +
				"interleaved-thinking-2025-05-14,redact-thinking-2026-02-12," +
				"thinking-token-count-2026-05-13,context-management-2025-06-27," +
				"prompt-caching-scope-2026-01-05," +
				"advanced-tool-use-2025-11-20,effort-2025-11-24," +
				"server-side-fallback-2026-06-01,fallback-credit-2026-06-01",
		},
		{
			name:  "oauth uses tool search and the current cache TTL trailer",
			body:  `{"model":"claude-opus-4-6","tools":[{"name":"Read","defer_loading":true}]}`,
			oauth: true,
			want: "claude-code-20250219,oauth-2025-04-20," +
				"interleaved-thinking-2025-05-14,redact-thinking-2026-02-12," +
				"thinking-token-count-2026-05-13,context-management-2025-06-27," +
				"prompt-caching-scope-2026-01-05,advanced-tool-use-2025-11-20," +
				"effort-2025-11-24,fallback-credit-2026-06-01," +
				"extended-cache-ttl-2025-04-11",
		},
		{
			name: "api key path sends neither oauth beta",
			body: `{"model":"claude-opus-4-6"}`,
			want: constants + ",effort-2025-11-24",
		},
		{
			name: "claude-haiku-4-5-20251001 omits effort",
			body: `{"model":"claude-haiku-4-5-20251001"}`,
			want: constants,
		},
		{
			name: "legacy model with inline tools no longer adds advanced tool use",
			body: `{"model":"claude-sonnet-4-6","tools":[{"name":"Read"}]}`,
			want: constants + ",effort-2025-11-24",
		},
		{
			name: "deferred tool adds advanced tool use",
			body: `{"model":"claude-sonnet-4-6","tools":[{"name":"Read","defer_loading":true}]}`,
			want: constants + ",advanced-tool-use-2025-11-20,effort-2025-11-24",
		},
		{
			name: "tool search server tool adds advanced tool use",
			body: `{"model":"claude-sonnet-4-6","tools":[{"type":"tool_search_tool_regex_20251119","name":"tool_search_tool_regex"},{"name":"Read"}]}`,
			want: constants + ",advanced-tool-use-2025-11-20,effort-2025-11-24",
		},
		{
			name: "tool use examples add advanced tool use",
			body: `{"model":"claude-sonnet-4-6","tools":[{"name":"Read","input_examples":[{"path":"a.go"}]}]}`,
			want: constants + ",advanced-tool-use-2025-11-20,effort-2025-11-24",
		},
		{
			name: "programmatic tool calling adds advanced tool use",
			body: `{"model":"claude-sonnet-4-6","tools":[{"name":"Read","allowed_callers":["code_execution_20250825"]}]}`,
			want: constants + ",advanced-tool-use-2025-11-20,effort-2025-11-24",
		},
		{
			name:      "requested advanced tool use is honored for inline tools",
			body:      `{"model":"claude-sonnet-4-6","tools":[{"name":"Read"}]}`,
			requested: map[string]bool{claudeAdvancedToolUseBeta: true},
			want:      constants + ",advanced-tool-use-2025-11-20,effort-2025-11-24",
		},
		{
			name: "empty tools array does not add advanced tool use",
			body: `{"model":"claude-opus-4-6","tools":[]}`,
			want: constants + ",effort-2025-11-24",
		},
		{
			name: "thinking display summarized drops redact-thinking",
			body: `{"model":"claude-opus-5","thinking":{"type":"adaptive","display":"summarized"}}`,
			want: "claude-code-20250219,interleaved-thinking-2025-05-14," +
				"thinking-token-count-2026-05-13,context-management-2025-06-27," +
				"prompt-caching-scope-2026-01-05,effort-2025-11-24",
		},
		{
			name: "thinking without display keeps redact-thinking",
			body: `{"model":"claude-opus-4-6","thinking":{"type":"adaptive"}}`,
			want: constants + ",effort-2025-11-24",
		},
		{
			name:      "advisor tool beta requested is placed before advanced-tool-use",
			body:      `{"model":"claude-opus-5","tools":[{"name":"Read","defer_loading":true}]}`,
			requested: map[string]bool{"advisor-tool-2026-03-01": true},
			want:      constants + ",advisor-tool-2026-03-01,advanced-tool-use-2025-11-20,effort-2025-11-24",
		},
		{
			name: "body with advisor server tool automatically adds advisor-tool beta",
			body: `{"model":"claude-opus-5","tools":[{"type":"advisor_20260301","name":"advisor"}]}`,
			want: constants + ",advisor-tool-2026-03-01,effort-2025-11-24",
		},
		{
			// Captured 2026-09-02 from Claude Code 2.1.258 (cli entrypoint, OAuth,
			// auto mode on): 158 inline tools without tool search, advisor beta
			// enabled for the account, thinking adaptive without display.
			name:  "2.1.258 main thread capture with inline tools and afk-mode",
			body:  `{"model":"claude-fable-5-1","tools":[{"name":"Read"}],"thinking":{"type":"adaptive"}}`,
			oauth: true,
			requested: map[string]bool{
				claudeAdvisorToolBeta: true,
				claudeAFKModeBeta:     true,
			},
			want: "claude-code-20250219,oauth-2025-04-20," +
				"interleaved-thinking-2025-05-14,redact-thinking-2026-02-12," +
				"thinking-token-count-2026-05-13,context-management-2025-06-27," +
				"prompt-caching-scope-2026-01-05,advisor-tool-2026-03-01," +
				"effort-2025-11-24,fallback-credit-2026-06-01," +
				"afk-mode-2026-01-31,extended-cache-ttl-2025-04-11",
		},
		{
			name:      "afk-mode sits between fast-mode and extended-cache-ttl",
			body:      `{"model":"claude-opus-5","speed":"fast"}`,
			oauth:     true,
			requested: map[string]bool{claudeAFKModeBeta: true},
			want: "claude-code-20250219,oauth-2025-04-20," +
				"interleaved-thinking-2025-05-14,redact-thinking-2026-02-12," +
				"thinking-token-count-2026-05-13,context-management-2025-06-27," +
				"prompt-caching-scope-2026-01-05,effort-2025-11-24," +
				"fallback-credit-2026-06-01,fast-mode-2026-02-01," +
				"afk-mode-2026-01-31,extended-cache-ttl-2025-04-11",
		},
		{
			name:  "afk-mode is not added unless the caller sent it",
			body:  `{"model":"claude-opus-5"}`,
			oauth: true,
			want: "claude-code-20250219,oauth-2025-04-20," +
				"interleaved-thinking-2025-05-14,redact-thinking-2026-02-12," +
				"thinking-token-count-2026-05-13,context-management-2025-06-27," +
				"prompt-caching-scope-2026-01-05,effort-2025-11-24," +
				"fallback-credit-2026-06-01,extended-cache-ttl-2025-04-11",
		},
		{
			name:      "thinking display updates emits thinking-display-updates beta and drops redact-thinking",
			body:      `{"model":"claude-opus-5","thinking":{"type":"adaptive","display":"updates"}}`,
			requested: map[string]bool{claudeThinkingDisplayUpdatesBeta: true},
			want: "claude-code-20250219,interleaved-thinking-2025-05-14," +
				"thinking-token-count-2026-05-13,context-management-2025-06-27," +
				"prompt-caching-scope-2026-01-05,effort-2025-11-24," +
				"thinking-display-updates-2026-08-18",
		},
		{
			name: "structured outputs and server side fallback follow requested",
			body: `{"model":"claude-opus-5","fallbacks":[{"name":"f"}]}`,
			requested: map[string]bool{
				claudeStructuredOutputsBeta: true,
			},
			want: constants + ",effort-2025-11-24,server-side-fallback-2026-06-01,structured-outputs-2025-12-15",
		},
		{
			name: "diagnostics body adds cache diagnosis at the end",
			body: `{"model":"claude-opus-5","diagnostics":{"enabled":true}}`,
			want: constants + ",effort-2025-11-24,cache-diagnosis-2026-04-07",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := claudeCodeCLIBetas([]byte(tt.body), tt.requested, tt.oauth)
			if got != tt.want {
				t.Fatalf("claudeCodeCLIBetas() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestApplyClaudeHeaders_ForwardsUnmanagedCallerBetas verifies that caller betas
// the proxy does not manage are forwarded verbatim on direct Anthropic requests,
// while managed betas stay governed by the assembled baseline (#5738).
func TestApplyClaudeHeaders_ForwardsUnmanagedCallerBetas(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-123"}}
	req, errReq := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages?beta=true", nil)
	if errReq != nil {
		t.Fatalf("NewRequest() error = %v", errReq)
	}
	incoming := http.Header{}
	incoming.Set("Anthropic-Beta", "per-turn-control-2026-07-01,mid-conversation-tool-changes-2026-07-01")
	applyClaudeHeaders(req, auth, "key-123", false, nil, &config.Config{}, incoming, []byte(`{"model":"claude-fable-5-1"}`))
	betas := req.Header.Get("Anthropic-Beta")
	for _, want := range []string{"per-turn-control-2026-07-01", "mid-conversation-tool-changes-2026-07-01"} {
		if !strings.Contains(betas, want) {
			t.Fatalf("Anthropic-Beta = %q, want unmanaged caller beta %q forwarded", betas, want)
		}
	}
	// A managed beta the assembly gates off (effort on a Haiku model) is not
	// reinstated just because the caller asked for it.
	haikuReq, errReq2 := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages?beta=true", nil)
	if errReq2 != nil {
		t.Fatalf("NewRequest() error = %v", errReq2)
	}
	haikuIncoming := http.Header{}
	haikuIncoming.Set("Anthropic-Beta", "effort-2025-11-24")
	applyClaudeHeaders(haikuReq, auth, "key-123", false, nil, &config.Config{}, haikuIncoming, []byte(`{"model":"claude-haiku-4-5"}`))
	if betas := haikuReq.Header.Get("Anthropic-Beta"); strings.Contains(betas, "effort-2025-11-24") {
		t.Fatalf("Anthropic-Beta = %q, want gated effort beta kept off the Haiku request", betas)
	}
}
