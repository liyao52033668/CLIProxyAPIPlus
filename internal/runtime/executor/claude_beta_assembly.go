package executor

import (
	"strings"

	"github.com/tidwall/gjson"
)

// Claude beta constants shared by the dynamic Anthropic-Beta assembly. The
// values match the wire names observed on Claude Code 2.1.258 traffic.
const (
	claudeTokenCountingBeta          = "token-counting-2024-11-01"
	claudeFastModeBeta               = "fast-mode-2026-02-01"
	claudeOAuthBeta                  = "oauth-2025-04-20"
	claudeCodeBeta                   = "claude-code-20250219"
	claudeContext1MBeta              = "context-1m-2025-08-07"
	claudeAdvisorToolBeta            = "advisor-tool-2026-03-01"
	claudeAdvancedToolUseBeta        = "advanced-tool-use-2025-11-20"
	claudeEffortBeta                 = "effort-2025-11-24"
	claudeServerSideFallbackBeta     = "server-side-fallback-2026-06-01"
	claudeFallbackCreditBeta         = "fallback-credit-2026-06-01"
	claudeStructuredOutputsBeta      = "structured-outputs-2025-12-15"
	claudeThinkingDisplayUpdatesBeta = "thinking-display-updates-2026-08-18"
	claudeExtendedCacheTTLBeta       = "extended-cache-ttl-2025-04-11"
	claudeCacheDiagnosisBeta         = "cache-diagnosis-2026-04-07"
	claudeRedactThinkingBeta         = "redact-thinking-2026-02-12"
	claudeAFKModeBeta                = "afk-mode-2026-01-31"
)

// claudeCodeCLIConstantBetas are the betas Claude Code sends on every
// /v1/messages request from the "cli" entrypoint, in wire order, excluding the
// leading claude-code-20250219.
//
// redact-thinking-2026-02-12 belongs here because cloaked requests always claim
// cc_entrypoint=cli; the "sdk-cli" entrypoint omits it. It is still dropped for
// requests that carry thinking.display, see claudeThinkingDisplaySet.
var claudeCodeCLIConstantBetas = []string{
	"interleaved-thinking-2025-05-14",
	claudeRedactThinkingBeta,
	"thinking-token-count-2026-05-13",
	"context-management-2025-06-27",
	"prompt-caching-scope-2026-01-05",
}

// claudeCodeTrailingBetas are caller-supplied betas that real Claude Code emits
// after effort-2025-11-24, in that relative order. They are forwarded when the
// caller asks for them and dropped otherwise.
var claudeCodeTrailingBetas = []string{
	claudeServerSideFallbackBeta,
	claudeFallbackCreditBeta,
	claudeStructuredOutputsBeta,
}

// claudeLegacyBaseBetas is the fixed beta baseline this fork used before the
// dynamic assembly landed. /v1/messages/count_tokens keeps it so the counting
// endpoint never receives inference-only betas.
const claudeLegacyBaseBetas = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,context-management-2025-06-27,prompt-caching-scope-2026-01-05,structured-outputs-2025-12-15,fast-mode-2026-02-01,redact-thinking-2026-02-12,token-efficient-tools-2026-03-28"

// claudeCodeCLIBetas assembles the Anthropic-Beta baseline the way Claude Code
// 2.1.258 does: the list is per-request, not a fixed string. requested holds the
// betas the caller asked for (from the request body "betas" array and the
// incoming Anthropic-Beta header), which decide the capability flags below.
//
// Verified against api.anthropic.com with native 2.1.258 captures on interactive,
// non-interactive, subagent, and multi-model paths (Sonnet, Opus, Fable, Haiku).
// The full observed order is:
//
//	 1 claude-code-20250219
//	 2 oauth-2025-04-20                  OAuth credentials only
//	 3 context-1m-2025-08-07             [1m] model variants only
//	 4 interleaved-thinking-2025-05-14
//	 5 redact-thinking-2026-02-12        cli entrypoint, no thinking.display
//	 6 thinking-token-count-2026-05-13
//	 7 context-management-2025-06-27
//	 8 prompt-caching-scope-2026-01-05
//	 9 advisor-tool-2026-03-01             requests declaring advisor tools or requesting advisor beta
//	10 advanced-tool-use-2025-11-20       requests using tool search or another advanced tool-use feature
//	11 effort-2025-11-24                  effort-supporting models with active thinking
//	12 server-side-fallback-2026-06-01    requests with fallbacks or requested
//	13 fallback-credit-2026-06-01         OAuth credentials
//	14 structured-outputs-2025-12-15      structured output requests
//	15 thinking-display-updates-2026-08-18 requests with thinking.display=updates
//	16 fast-mode-2026-02-01               speed:fast requests only
//	17 afk-mode-2026-01-31                forwarded when the caller sends it
//	18 extended-cache-ttl-2025-04-11      OAuth credentials
//	19 cache-diagnosis-2026-04-07         requests with diagnostics only
//
// The mid-conversation-system and probe/helper gating of the upstream assembly
// are omitted: this fork has no mid-conversation system splicing or Haiku helper
// transport subsystems.
func claudeCodeCLIBetas(body []byte, requested map[string]bool, oauthToken bool) string {
	betas := make([]string, 0, len(claudeCodeCLIConstantBetas)+len(claudeCodeTrailingBetas)+9)
	betas = append(betas, claudeCodeBeta)
	if oauthToken {
		betas = append(betas, claudeOAuthBeta)
	}
	if requested[claudeContext1MBeta] {
		betas = append(betas, claudeContext1MBeta)
	}
	redactThinking := !claudeThinkingDisplaySet(body)
	for _, beta := range claudeCodeCLIConstantBetas {
		if beta == claudeRedactThinkingBeta && !redactThinking {
			continue
		}
		betas = append(betas, beta)
	}
	if requested[claudeAdvisorToolBeta] || claudeBodyHasAdvisorTool(body) {
		betas = append(betas, claudeAdvisorToolBeta)
	}
	if requested[claudeAdvancedToolUseBeta] || claudeBodyUsesAdvancedToolUse(body) {
		betas = append(betas, claudeAdvancedToolUseBeta)
	}
	if claudeRequestSupportsEffort(body, requested) {
		betas = append(betas, claudeEffortBeta)
	}
	if requested[claudeServerSideFallbackBeta] || gjson.GetBytes(body, "fallbacks").Exists() {
		betas = append(betas, claudeServerSideFallbackBeta)
	}
	if requested[claudeFallbackCreditBeta] || oauthToken {
		betas = append(betas, claudeFallbackCreditBeta)
	}
	for _, beta := range claudeCodeTrailingBetas {
		if beta == claudeServerSideFallbackBeta || beta == claudeFallbackCreditBeta {
			continue
		}
		if requested[beta] {
			betas = append(betas, beta)
		}
	}
	thinkingType := gjson.GetBytes(body, "thinking.type").String()
	if thinkingType != "disabled" && (requested[claudeThinkingDisplayUpdatesBeta] || claudeThinkingDisplayUpdates(body)) {
		betas = append(betas, claudeThinkingDisplayUpdatesBeta)
	}
	if claudeRequestUsesFastMode(body, requested) {
		betas = append(betas, claudeFastModeBeta)
	}
	if requested[claudeAFKModeBeta] {
		betas = append(betas, claudeAFKModeBeta)
	}
	if oauthToken {
		betas = append(betas, claudeExtendedCacheTTLBeta)
	}
	if diagnostics := gjson.GetBytes(body, "diagnostics"); diagnostics.IsObject() {
		betas = append(betas, claudeCacheDiagnosisBeta)
	}
	return strings.Join(betas, ",")
}

func isClaudeHaikuModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "haiku")
}

func claudeRequestSupportsEffort(body []byte, requested map[string]bool) bool {
	if len(body) > 0 {
		model := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "model").String()))
		if isClaudeHaikuModel(model) {
			return false
		}
		thinkingType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "thinking.type").String()))
		if thinkingType == "disabled" {
			return false
		}
	}
	return true
}

func claudeThinkingDisplayUpdates(body []byte) bool {
	display := gjson.GetBytes(body, "thinking.display")
	return display.Type == gjson.String && strings.EqualFold(strings.TrimSpace(display.String()), "updates")
}

// claudeBodyUsesAdvancedToolUse reports whether the request needs
// advanced-tool-use-2025-11-20. Claude Code 2.1.258 adds the beta only while
// tool search is active, which puts a tool_search_tool_* server tool and
// defer_loading tools on the wire; plain tool declarations no longer carry it
// (measured 2026-09-02: 158 inline tools, no beta). Tool use examples
// (input_examples) and programmatic tool calling (allowed_callers) sit behind
// the same beta and are just as visible in the body, so callers using them keep
// working without requesting the beta explicitly.
func claudeBodyUsesAdvancedToolUse(body []byte) bool {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		toolType := strings.ToLower(strings.TrimSpace(tool.Get("type").String()))
		if strings.HasPrefix(toolType, "tool_search_tool_") {
			return true
		}
		if tool.Get("defer_loading").Bool() || tool.Get("input_examples").Exists() || tool.Get("allowed_callers").Exists() {
			return true
		}
	}
	return false
}

// claudeBodyHasAdvisorTool reports whether the request body declares an
// advisor server tool.
func claudeBodyHasAdvisorTool(body []byte) bool {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		toolType := strings.ToLower(strings.TrimSpace(tool.Get("type").String()))
		if strings.HasPrefix(toolType, "advisor_") {
			return true
		}
	}
	return false
}

// claudeRequestUsesFastMode reports whether the request selects the fast service
// tier. Anthropic rejects the body's speed field with "Extra inputs are not
// permitted" unless fast-mode-2026-02-01 is declared, so the beta has to follow
// the body.
func claudeRequestUsesFastMode(body []byte, requested map[string]bool) bool {
	if requested[claudeFastModeBeta] {
		return true
	}
	speed := gjson.GetBytes(body, "speed")
	return speed.Type == gjson.String && strings.EqualFold(strings.TrimSpace(speed.String()), "fast")
}

// claudeManagedBetaSet holds every beta the proxy itself assembles or gates.
// Caller betas outside this set are unknown to the pinned Claude Code profile —
// newer client releases ship betas past it — and are forwarded verbatim so
// their features keep working (#5738).
var claudeManagedBetaSet = func() map[string]bool {
	managed := []string{
		claudeTokenCountingBeta,
		claudeFastModeBeta,
		claudeOAuthBeta,
		claudeCodeBeta,
		claudeContext1MBeta,
		claudeAdvisorToolBeta,
		claudeAdvancedToolUseBeta,
		claudeEffortBeta,
		claudeServerSideFallbackBeta,
		claudeFallbackCreditBeta,
		claudeStructuredOutputsBeta,
		claudeThinkingDisplayUpdatesBeta,
		claudeExtendedCacheTTLBeta,
		claudeCacheDiagnosisBeta,
		claudeRedactThinkingBeta,
		claudeAFKModeBeta,
		// Fork-specific baseline beta; callers may keep requesting it.
		"token-efficient-tools-2026-03-28",
	}
	managed = append(managed, claudeCodeCLIConstantBetas...)
	managed = append(managed, claudeCodeTrailingBetas...)
	set := make(map[string]bool, len(managed))
	for _, beta := range managed {
		set[beta] = true
	}
	return set
}()

func isManagedClaudeBeta(beta string) bool {
	return claudeManagedBetaSet[strings.TrimSpace(beta)]
}

// appendClaudeBetaOnce appends beta to a comma-separated baseline unless an
// identical entry is already present.
func appendClaudeBetaOnce(baseline, beta string) string {
	if strings.Contains(","+baseline+",", ","+beta+",") {
		return baseline
	}
	return baseline + "," + beta
}
