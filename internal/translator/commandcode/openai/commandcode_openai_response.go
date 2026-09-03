// Package openai provides response translation from the Command Code wire
// protocol to OpenAI Chat Completions format.
package openai

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	cc "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/commandcode"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// functionCallIDCounter provides a process-wide unique counter for function call identifiers.
var functionCallIDCounter uint64

// openaiStreamState carries state across streaming conversion calls.
type openaiStreamState struct {
	Model      string
	ResponseID string
	CreatedAt  int64
	TextSent   bool
	ToolIndex  int
	ToolCalls  map[string]int // toolCallId -> openai tool call index
	FinishSeen bool
}

// ConvertCommandCodeStreamToOpenAI converts one NDJSON event line from the
// Command Code stream into OpenAI SSE chunk payloads (raw JSON without the
// "data:" prefix; the handler layer adds it and emits [DONE]).
func ConvertCommandCodeStreamToOpenAI(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &openaiStreamState{
			Model:      modelName,
			ResponseID: newResponseID(),
			CreatedAt:  time.Now().Unix(),
			ToolCalls:  make(map[string]int),
		}
	}
	state := (*param).(*openaiStreamState)

	line := strings.TrimSpace(string(rawJSON))
	if line == "" {
		return nil
	}
	event := gjson.Parse(line)
	if !event.Exists() {
		return nil
	}
	template := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{},"finish_reason":null}]}`)
	template, _ = sjson.SetBytes(template, "id", state.ResponseID)
	template, _ = sjson.SetBytes(template, "created", state.CreatedAt)
	if state.Model != "" {
		template, _ = sjson.SetBytes(template, "model", state.Model)
	} else if modelName != "" {
		template, _ = sjson.SetBytes(template, "model", modelName)
	}

	switch event.Get("type").String() {
	case cc.EventTextDelta:
		text := event.Get("text").String()
		if text == "" {
			return nil
		}
		state.TextSent = true
		template, _ = sjson.SetBytes(template, "choices.0.delta.content", text)

	case cc.EventReasoningDelta:
		text := event.Get("text").String()
		if text == "" {
			return nil
		}
		template, _ = sjson.SetBytes(template, "choices.0.delta.reasoning_content", text)

	case cc.EventToolCall:
		toolCallID := event.Get("toolCallId").String()
		toolName := event.Get("toolName").String()
		input := event.Get("input")
		args := "{}"
		if input.Exists() && input.Raw != "" && input.Raw != "null" {
			args = input.Raw
		}
		if toolCallID == "" {
			toolCallID = generateToolCallID(toolName)
		}
		idx := state.ToolIndex
		state.ToolIndex++
		state.ToolCalls[toolCallID] = idx

		// Each streamed chunk carries exactly ONE tool_calls fragment at array
		// position 0; the parallel-call number lives in the fragment's "index"
		// field. Writing the fragment at array position idx would make sjson
		// null-pad the fresh template array (e.g. [null, {...}]), which clients
		// reject.
		template, _ = sjson.SetBytes(template, "choices.0.delta.tool_calls.0.index", idx)
		template, _ = sjson.SetBytes(template, "choices.0.delta.tool_calls.0.id", toolCallID)
		template, _ = sjson.SetBytes(template, "choices.0.delta.tool_calls.0.type", "function")
		template, _ = sjson.SetBytes(template, "choices.0.delta.tool_calls.0.function.name", toolName)
		template, _ = sjson.SetBytes(template, "choices.0.delta.tool_calls.0.function.arguments", args)

	case cc.EventFinish:
		state.FinishSeen = true
		finishReason := mapFinishReason(event.Get("finishReason").String())
		template, _ = sjson.SetBytes(template, "choices.0.delta", map[string]any{})
		template, _ = sjson.SetBytes(template, "choices.0.finish_reason", finishReason)
		if u := parseUsage(event); u != nil {
			detail := usage.Detail{
				InputTokens:         u.InputTokens,
				OutputTokens:        u.OutputTokens,
				CacheReadTokens:     u.InputTokenDetails.CacheReadTokens,
				CacheCreationTokens: u.InputTokenDetails.CacheWriteTokens,
			}
			template, _ = sjson.SetBytes(template, "usage", usageToMap(detail))
		}
		return [][]byte{template, []byte("[DONE]")}

	case cc.EventError:
		msg := event.Get("error.message").String()
		if msg == "" {
			msg = event.Get("error").String()
		}
		log.Warnf("commandcode: stream error event: %s", msg)
		return nil

	default:
		return nil
	}
	return [][]byte{template}
}

// ConvertCommandCodeNonStreamToOpenAI converts a complete Command Code NDJSON
// response body (all buffered lines) into one OpenAI Chat Completions response.
func ConvertCommandCodeNonStreamToOpenAI(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	var (
		contentSB    strings.Builder
		reasoningSB  strings.Builder
		typeOrder    int
		hasReasoning bool
		toolUses     []openAIToolUse
		usageInfo    usage.Detail
		stopReason   string
	)

	for _, line := range strings.Split(string(rawJSON), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		event := gjson.Parse(line)
		if !event.Exists() {
			continue
		}
		switch event.Get("type").String() {
		case cc.EventTextDelta:
			contentSB.WriteString(event.Get("text").String())
		case cc.EventReasoningDelta:
			hasReasoning = true
			reasoningSB.WriteString(event.Get("text").String())
		case cc.EventToolCall:
			input := event.Get("input")
			var in any
			if input.Exists() {
				_ = json.Unmarshal([]byte(input.Raw), &in)
			}
			id := event.Get("toolCallId").String()
			if id == "" {
				id = generateToolCallID(event.Get("toolName").String())
			}
			toolUses = append(toolUses, openAIToolUse{
				ID:    id,
				Name:  event.Get("toolName").String(),
				Input: in,
			})
		case cc.EventFinish:
			stopReason = mapFinishReason(event.Get("finishReason").String())
			if u := parseUsage(event); u != nil {
				usageInfo = usage.Detail{
					InputTokens:         u.InputTokens,
					OutputTokens:        u.OutputTokens,
					CacheReadTokens:     u.InputTokenDetails.CacheReadTokens,
					CacheCreationTokens: u.InputTokenDetails.CacheWriteTokens,
				}
			}
		}
	}
	_ = typeOrder
	return buildOpenAIResponse(contentSB.String(), reasoningSB.String(), hasReasoning, toolUses, modelName, usageInfo, stopReason)
}

// openAIToolUse is an intermediate tool call representation.
type openAIToolUse struct {
	ID    string
	Name  string
	Input any
}

// buildOpenAIResponse constructs a complete chat.completion response object.
func buildOpenAIResponse(content, reasoningContent string, hasReasoning bool, toolUses []openAIToolUse, model string, usageInfo usage.Detail, stopReason string) []byte {
	message := map[string]any{
		"role":    "assistant",
		"content": content,
	}
	if hasReasoning && reasoningContent != "" {
		message["reasoning_content"] = reasoningContent
	}
	if len(toolUses) > 0 {
		toolCalls := make([]map[string]any, 0, len(toolUses))
		for i, tu := range toolUses {
			inputJSON, _ := json.Marshal(tu.Input)
			toolCalls = append(toolCalls, map[string]any{
				"id":    tu.ID,
				"type":  "function",
				"index": i,
				"function": map[string]any{
					"name":      tu.Name,
					"arguments": string(inputJSON),
				},
			})
		}
		message["tool_calls"] = toolCalls
		if content == "" {
			message["content"] = nil
		}
	}
	if stopReason == "" {
		stopReason = "stop"
		if len(toolUses) > 0 {
			stopReason = "tool_calls"
		}
	}
	response := map[string]any{
		"id":      newResponseID(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       message,
				"finish_reason": stopReason,
			},
		},
		"usage": usageToMap(usageInfo),
	}
	result, err := json.Marshal(response)
	if err != nil {
		log.Errorf("commandcode: failed to marshal response: %v", err)
		return []byte("{}")
	}
	return result
}

// mapFinishReason maps wire finish reasons to OpenAI finish reasons.
func mapFinishReason(reason string) string {
	switch reason {
	case "tool-calls", "tool_calls", "tool_use":
		return "tool_calls"
	case "length", "max_tokens":
		return "length"
	case "", "end_turn":
		return "stop"
	default:
		return "stop"
	}
}

// parseUsage extracts usage from a finish event.
// Supports both upstream response formats:
// - Format A (old): totalUsage.inputTokens (camelCase)
// - Format B (new): usage.input_tokens (snake_case)
func parseUsage(event gjson.Result) *cc.FinishUsage {
	// Try "usage" first (new format), then "totalUsage" (old format)
	u := event.Get("usage")
	if !u.Exists() {
		u = event.Get("totalUsage")
	}
	if !u.Exists() {
		return nil
	}
	
	// Try multiple field name formats for each metric
	inputTokens := u.Get("input_tokens").Int()
	if inputTokens == 0 {
		inputTokens = u.Get("inputTokens").Int()
	}
	
	outputTokens := u.Get("output_tokens").Int()
	if outputTokens == 0 {
		outputTokens = u.Get("outputTokens").Int()
	}
	
	out := &cc.FinishUsage{
		InputTokens:       inputTokens,
		OutputTokens:      outputTokens,
		InputTokenDetails: &cc.FinishUsageInputDetails{},
	}
	
	// Try cache token fields from both nested and flattened structures
	d := u.Get("inputTokenDetails")
	if d.Exists() {
		out.InputTokenDetails.CacheReadTokens = d.Get("cacheReadTokens").Int()
		out.InputTokenDetails.CacheWriteTokens = d.Get("cacheWriteTokens").Int()
	}
	
	// Fallback to flattened snake_case fields
	if out.InputTokenDetails.CacheReadTokens == 0 {
		out.InputTokenDetails.CacheReadTokens = u.Get("cache_read_input_tokens").Int()
	}
	if out.InputTokenDetails.CacheWriteTokens == 0 {
		out.InputTokenDetails.CacheWriteTokens = u.Get("cache_creation_input_tokens").Int()
	}
	
	return out
}

// usageToMap converts a usage detail into an OpenAI usage object.
func usageToMap(detail usage.Detail) map[string]any {
	m := map[string]any{
		"prompt_tokens":     detail.InputTokens,
		"completion_tokens": detail.OutputTokens,
		"total_tokens":      detail.InputTokens + detail.OutputTokens,
	}
	if detail.CacheReadTokens > 0 || detail.CacheCreationTokens > 0 {
		m["prompt_tokens_details"] = map[string]any{
			"cached_tokens":          detail.CacheReadTokens,
			"cached_creation_tokens": detail.CacheCreationTokens,
		}
	}
	return m
}

// generateToolCallID produces a unique fallback tool call id.
func generateToolCallID(name string) string {
	n := atomic.AddUint64(&functionCallIDCounter, 1)
	return "call_" + name + "_" + strconv.FormatUint(n, 10)
}

// newResponseID creates a fresh response identifier.
func newResponseID() string {
	n := atomic.AddUint64(&functionCallIDCounter, 1)
	return "chatcmpl-" + strconv.FormatUint(n, 16)
}
