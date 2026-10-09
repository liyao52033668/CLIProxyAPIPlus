package helps

import (
	"bytes"
	"fmt"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ExpandClaudeResponsesCompaction rewrites sealed compaction items into context
// before translation. Unrecognized capsules fail the request instead of being dropped.
func ExpandClaudeResponsesCompaction(req *cliproxyexecutor.Request, opts *cliproxyexecutor.Options) error {
	if req != nil {
		req.Payload = DropForeignClaudeCompactionItems(req.Payload)
		if HasResponsesCompactionItem(req.Payload) {
			expanded, errExpand := ExpandAntigravityCompactionCapsules(req.Payload)
			if errExpand != nil {
				return errExpand
			}
			req.Payload = expanded
		}
	}
	if opts != nil {
		opts.OriginalRequest = DropForeignClaudeCompactionItems(opts.OriginalRequest)
		if HasResponsesCompactionItem(opts.OriginalRequest) {
			expanded, errExpand := ExpandAntigravityCompactionCapsules(opts.OriginalRequest)
			if errExpand != nil {
				return errExpand
			}
			opts.OriginalRequest = expanded
		}
	}
	return nil
}

func DropForeignClaudeCompactionItems(payload []byte) []byte {
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return payload
	}
	dropped := 0
	items := make([]string, 0, len(input.Array()))
	for _, item := range input.Array() {
		if item.Get("type").String() == "compaction" && !RecognizedAntigravityCompactionCapsule(item.Get("encrypted_content").String()) {
			dropped++
			continue
		}
		items = append(items, item.Raw)
	}
	if dropped == 0 {
		return payload
	}
	log.Warnf("claude compaction: dropped %d non-CPA compaction item(s)", dropped)
	out, _ := sjson.SetRawBytes(payload, "input", []byte("["+strings.Join(items, ",")+"]"))
	return out
}

func ClaudeResponsesCompactionRequested(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) bool {
	return opts.Alt == "responses/compact" ||
		HasResponsesCompactionTrigger(req.Payload) ||
		HasResponsesCompactionTrigger(opts.OriginalRequest)
}

func ClaudeCompactionSourcePayload(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) []byte {
	payload := req.Payload
	if !HasResponsesCompactionTrigger(payload) && HasResponsesCompactionTrigger(opts.OriginalRequest) {
		payload = opts.OriginalRequest
	}
	if len(payload) == 0 && len(opts.OriginalRequest) > 0 {
		payload = opts.OriginalRequest
	}
	return payload
}

// PrepareClaudeCompactionSummaryPayload keeps tool definitions that Claude needs
// in order to accept tool_use history, while still asking for a text summary.
func PrepareClaudeCompactionSummaryPayload(payload []byte, modelName string) []byte {
	tools := gjson.GetBytes(payload, "tools")
	additionalTools := gjson.GetBytes(payload, "additional_tools")
	out := PrepareAntigravityCompactionSummaryPayload(payload, modelName)
	if tools.Exists() {
		out, _ = sjson.SetRawBytes(out, "tools", []byte(tools.Raw))
	}
	if additionalTools.Exists() {
		out, _ = sjson.SetRawBytes(out, "additional_tools", []byte(additionalTools.Raw))
	}
	return out
}

// FinalizeClaudeCompactionSummaryBody runs after built-in translation and before
// user payload rules. Tool history stays structured when definitions exist, and
// tool_choice none stops the summary turn from calling a tool. History without
// definitions is flattened so Anthropic does not reject orphan tool blocks.
func FinalizeClaudeCompactionSummaryBody(body []byte) []byte {
	if claudeBodyHasToolDefinitions(body) {
		body, _ = sjson.SetRawBytes(body, "tool_choice", []byte(`{"type":"none"}`))
		return body
	}
	return flattenClaudeToolBlocksForCompaction(body)
}

func claudeBodyHasToolDefinitions(body []byte) bool {
	tools := gjson.GetBytes(body, "tools")
	return tools.IsArray() && len(tools.Array()) > 0
}

func flattenClaudeToolBlocksForCompaction(body []byte) []byte {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}
	changed := false
	rewritten := make([]string, 0, len(messages.Array()))
	for _, msg := range messages.Array() {
		content := msg.Get("content")
		if !content.IsArray() {
			rewritten = append(rewritten, msg.Raw)
			continue
		}
		parts := make([]string, 0, len(content.Array()))
		msgChanged := false
		for _, part := range content.Array() {
			switch part.Get("type").String() {
			case "tool_use":
				text := fmt.Sprintf("Tool call %s (%s): %s", part.Get("name").String(), part.Get("id").String(), part.Get("input").Raw)
				block, _ := sjson.SetBytes([]byte(`{"type":"text","text":""}`), "text", text)
				parts = append(parts, string(block))
				msgChanged = true
			case "tool_result":
				result := part.Get("content")
				resultText := result.Raw
				if result.Type == gjson.String {
					resultText = result.String()
				}
				text := fmt.Sprintf("Tool result %s: %s", part.Get("tool_use_id").String(), resultText)
				block, _ := sjson.SetBytes([]byte(`{"type":"text","text":""}`), "text", text)
				parts = append(parts, string(block))
				msgChanged = true
			default:
				parts = append(parts, part.Raw)
			}
		}
		if !msgChanged {
			rewritten = append(rewritten, msg.Raw)
			continue
		}
		changed = true
		updated, _ := sjson.SetRawBytes([]byte(msg.Raw), "content", []byte("["+strings.Join(parts, ",")+"]"))
		rewritten = append(rewritten, string(updated))
	}
	if !changed {
		return body
	}
	body, _ = sjson.SetRawBytes(body, "messages", []byte("["+strings.Join(rewritten, ",")+"]"))
	return body
}

// ClaudeCompactionResponsesUsage matches the Responses translator: input includes
// cache creation and cache read, cached_tokens is cache read, and total is their sum.
func ClaudeCompactionResponsesUsage(payload []byte) (inputTokens, outputTokens, totalTokens, cachedTokens int) {
	usage := gjson.GetBytes(payload, "usage")
	if !usage.Exists() {
		parsed := ParseOpenAIUsage(payload)
		return int(parsed.InputTokens), int(parsed.OutputTokens), int(parsed.TotalTokens), 0
	}
	rawInput := int(usage.Get("input_tokens").Int())
	outputTokens = int(usage.Get("output_tokens").Int())
	cacheCreation := int(usage.Get("cache_creation_input_tokens").Int())
	cachedTokens = int(usage.Get("cache_read_input_tokens").Int())
	inputTokens = rawInput + cacheCreation + cachedTokens
	totalTokens = inputTokens + outputTokens
	return inputTokens, outputTokens, totalTokens, cachedTokens
}

func PatchClaudeCompactionStreamUsage(chunk []byte, inputTokens, outputTokens, totalTokens, cachedTokens int) []byte {
	const dataPrefix = "data: "
	idx := bytes.Index(chunk, []byte(dataPrefix))
	if idx < 0 {
		return chunk
	}
	data := bytes.TrimSpace(chunk[idx+len(dataPrefix):])
	path := ""
	switch {
	case gjson.GetBytes(data, "response.usage").Exists():
		path = "response.usage"
	case gjson.GetBytes(data, "usage").Exists():
		path = "usage"
	default:
		return chunk
	}
	data, _ = sjson.SetBytes(data, path+".input_tokens", inputTokens)
	data, _ = sjson.SetBytes(data, path+".output_tokens", outputTokens)
	data, _ = sjson.SetBytes(data, path+".total_tokens", totalTokens)
	data, _ = sjson.SetBytes(data, path+".input_tokens_details.cached_tokens", cachedTokens)
	var buf bytes.Buffer
	buf.Write(chunk[:idx])
	buf.WriteString(dataPrefix)
	buf.Write(data)
	buf.WriteString("\n\n")
	return buf.Bytes()
}
