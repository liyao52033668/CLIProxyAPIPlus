package helps

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// APIKeyModelIsCompat reports whether the selected API-key model enables
// compatibility handling for Claude thinking blocks.
//
// The upstream is-compat model flag is not modeled in this fork: every API-key
// Claude request is treated as replay-compatible so multi-turn signed thinking
// persists. The API-key gate is enforced by the caller via the auth kind. Refine
// this if an explicit per-model gate is introduced.
func APIKeyModelIsCompat(req cliproxyexecutor.Request) bool {
	_ = req
	return true
}

// ApplyRequestThinking applies thinking configuration to a request body.
func ApplyRequestThinking(body []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, fromFormat, toFormat, provider string) ([]byte, error) {
	out, err := thinking.ApplyThinking(body, req.Model, fromFormat, toFormat, provider)
	if err != nil {
		return out, err
	}
	// Compatible OpenAI upstreams reject a Claude tool-call turn whose assistant
	// message has no reasoning_content while reasoning is active, because the
	// translated message dropped the (unreplayable) Claude thinking block. A
	// single-space placeholder keeps the turn well-formed.
	if shouldReconcileClaudeOpenAICompatToolCallReasoning(req, fromFormat, toFormat) && len(out) > 0 {
		if reconciled, changed := reconcileClaudeOpenAICompatToolCallReasoning(out); changed {
			out = reconciled
		}
	}
	return out, nil
}

func shouldReconcileClaudeOpenAICompatToolCallReasoning(req cliproxyexecutor.Request, fromFormat, toFormat string) bool {
	return APIKeyModelIsCompat(req) &&
		strings.EqualFold(strings.TrimSpace(fromFormat), "claude") &&
		strings.EqualFold(strings.TrimSpace(toFormat), "openai")
}

// reconcileClaudeOpenAICompatToolCallReasoning adds a single-space
// reasoning_content placeholder on assistant tool-call turns that have no
// replayable thinking text, and removes placeholders when reasoning is off.
func reconcileClaudeOpenAICompatToolCallReasoning(body []byte) ([]byte, bool) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body, false
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body, false
	}

	reasoningActive := false
	if effortRes := gjson.GetBytes(body, "reasoning_effort"); effortRes.Exists() {
		resolvedEffort := strings.ToLower(strings.TrimSpace(effortRes.String()))
		if resolvedEffort != "" && resolvedEffort != string(thinking.LevelNone) && resolvedEffort != "0" && resolvedEffort != "false" {
			reasoningActive = true
		}
	}

	modified := false
	out := body
	for idx, msg := range messages.Array() {
		if strings.TrimSpace(msg.Get("role").String()) != "assistant" {
			continue
		}
		toolCalls := msg.Get("tool_calls")
		if !toolCalls.Exists() || !toolCalls.IsArray() || len(toolCalls.Array()) == 0 {
			continue
		}
		reasoning := msg.Get("reasoning_content")
		if reasoningActive {
			if !reasoning.Exists() || (strings.TrimSpace(reasoning.String()) == "" && reasoning.String() != " ") {
				next, errSet := sjson.SetBytes(out, fmt.Sprintf("messages.%d.reasoning_content", idx), " ")
				if errSet == nil {
					out = next
					modified = true
				}
			}
			continue
		}
		// Reasoning is off: a leftover placeholder from a caller would look like
		// a real reasoning block to the upstream.
		if reasoning.Exists() && reasoning.String() == " " {
			next, errDel := sjson.DeleteBytes(out, fmt.Sprintf("messages.%d.reasoning_content", idx))
			if errDel == nil {
				out = next
				modified = true
			}
		}
	}
	return out, modified
}
