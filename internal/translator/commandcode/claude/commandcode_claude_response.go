// Package claude provides response translation from the Command Code wire
// protocol to Claude Messages SSE format.
package claude

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync/atomic"

	cc "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/commandcode"
	"github.com/tidwall/gjson"
)

// messageIDCounter provides process-wide unique message identifiers.
var messageIDCounter uint64

// claudeStreamState carries state across streaming conversion calls.
type claudeStreamState struct {
	Model        string
	MessageID    string
	BlockIndex   int
	TextOpen     bool
	ThinkingOpen bool
	ToolOpen     bool
	OpenToolID   string
	FinishSent   bool
}

// ConvertCommandCodeStreamToClaude converts one NDJSON event line from the
// Command Code stream into complete Claude SSE events (with "event:" prefix,
// as the handler layer writes chunks verbatim).
func ConvertCommandCodeStreamToClaude(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &claudeStreamState{
			Model:     modelName,
			MessageID: newMessageID(),
		}
	}
	state := (*param).(*claudeStreamState)

	line := strings.TrimSpace(string(rawJSON))
	if line == "" {
		return nil
	}
	event := gjson.Parse(line)
	if !event.Exists() || state.FinishSent {
		return nil
	}

	var out [][]byte
	switch event.Get("type").String() {
	case cc.EventTextDelta:
		text := event.Get("text").String()
		if text == "" {
			return nil
		}
		if !state.TextOpen && !state.ThinkingOpen && !state.ToolOpen {
			out = append(out, sseEvent("message_start", buildMessageStart(state, 0)))
			state.BlockIndex = 0
			out = append(out, blockStart(state, "text", "", ""))
			state.TextOpen = true
		} else if !state.TextOpen {
			closeBlock(&out, state)
			out = append(out, blockStart(state, "text", "", ""))
			state.TextOpen = true
		}
		out = append(out, textDelta(text, state.BlockIndex))

	case cc.EventReasoningDelta:
		text := event.Get("text").String()
		if text == "" {
			return nil
		}
		if !state.ThinkingOpen {
			if state.TextOpen {
				closeBlock(&out, state)
			}
			out = append(out, sseEvent("message_start", buildMessageStart(state, 0)))
			out = append(out, blockStart(state, "thinking", "", ""))
			state.ThinkingOpen = true
		}
		out = append(out, thinkingDelta(text, state.BlockIndex))

	case cc.EventToolCall:
		if state.TextOpen || state.ThinkingOpen {
			closeBlock(&out, state)
		}
		input := event.Get("input")
		args := "{}"
		if input.Exists() && input.Raw != "" && input.Raw != "null" {
			args = input.Raw
		}
		toolCallID := event.Get("toolCallId").String()
		out = append(out, blockStart(state, "tool_use", toolCallID, event.Get("toolName").String()))
		state.ToolOpen = true
		state.OpenToolID = toolCallID
		if args != "{}" {
			out = append(out, inputJSONDelta(args, state.BlockIndex))
		}

	case cc.EventFinish:
		if state.TextOpen || state.ThinkingOpen || state.ToolOpen {
			closeBlock(&out, state)
		}
		stopReason := mapStopReason(event.Get("finishReason").String())
		usageMap := map[string]any{"input_tokens": int64(0), "output_tokens": int64(0)}
		if u := parseUsage(event); u != nil {
			usageMap["input_tokens"] = u.InputTokens
			usageMap["output_tokens"] = u.OutputTokens
			if u.InputTokenDetails != nil {
				if u.InputTokenDetails.CacheReadTokens > 0 {
					usageMap["cache_read_input_tokens"] = u.InputTokenDetails.CacheReadTokens
				}
				if u.InputTokenDetails.CacheWriteTokens > 0 {
					usageMap["cache_creation_input_tokens"] = u.InputTokenDetails.CacheWriteTokens
				}
			}
		}
		deltaEvent := map[string]any{
			"type": "message_delta",
			"delta": map[string]any{
				"stop_reason":   stopReason,
				"stop_sequence": nil,
			},
			"usage": usageMap,
		}
		out = append(out, sseEvent("message_delta", mustJSON(deltaEvent)))
		out = append(out, sseEvent("message_stop", mustJSON(map[string]any{"type": "message_stop"})))
		state.FinishSent = true

	default:
		return nil
	}
	return out
}

// ConvertCommandCodeNonStreamToClaude converts a complete Command Code NDJSON
// body into one Claude Messages API JSON response.
func ConvertCommandCodeNonStreamToClaude(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	type block struct {
		typ      string
		text     string
		id       string
		name     string
		inputRaw string
	}
	var blocks []block
	var usageIn, usageOut, cacheRead, cacheWrite int64
	stopReason := "end_turn"

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
			t := event.Get("text").String()
			if t == "" {
				continue
			}
			if n := len(blocks); n > 0 && blocks[n-1].typ == "text" {
				blocks[n-1].text += t
			} else {
				blocks = append(blocks, block{typ: "text", text: t})
			}
		case cc.EventReasoningDelta:
			t := event.Get("text").String()
			if t == "" {
				continue
			}
			if n := len(blocks); n > 0 && blocks[n-1].typ == "thinking" {
				blocks[n-1].text += t
			} else {
				blocks = append(blocks, block{typ: "thinking", text: t})
			}
		case cc.EventToolCall:
			input := event.Get("input")
			blocks = append(blocks, block{
				typ:      "tool_use",
				id:       event.Get("toolCallId").String(),
				name:     event.Get("toolName").String(),
				inputRaw: input.Raw,
			})
		case cc.EventFinish:
			stopReason = mapStopReason(event.Get("finishReason").String())
			if u := parseUsage(event); u != nil {
				usageIn = u.InputTokens
				usageOut = u.OutputTokens
				if u.InputTokenDetails != nil {
					cacheRead = u.InputTokenDetails.CacheReadTokens
					cacheWrite = u.InputTokenDetails.CacheWriteTokens
				}
			}
		}
	}

	content := make([]map[string]any, 0, len(blocks))
	for i := range blocks {
		b := &blocks[i]
		switch b.typ {
		case "text":
			content = append(content, map[string]any{"type": "text", "text": b.text})
		case "thinking":
			content = append(content, map[string]any{"type": "thinking", "thinking": b.text})
		case "tool_use":
			var input any
			if err := json.Unmarshal([]byte(b.inputRaw), &input); err != nil || input == nil {
				input = map[string]any{}
			}
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    b.id,
				"name":  b.name,
				"input": input,
			})
		}
	}

	response := map[string]any{
		"id":            newMessageID(),
		"type":          "message",
		"role":          "assistant",
		"model":         modelName,
		"content":       content,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":                usageIn,
			"output_tokens":               usageOut,
			"cache_read_input_tokens":     cacheRead,
			"cache_creation_input_tokens": cacheWrite,
		},
	}
	result, err := json.Marshal(response)
	if err != nil {
		return []byte("{}")
	}
	return result
}

// --- SSE building helpers ---

func sseEvent(name string, data []byte) []byte {
	return []byte("event: " + name + "\ndata: " + string(data))
}

func mustJSON(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}

func buildMessageStart(state *claudeStreamState, inputTokens int64) []byte {
	return mustJSON(map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            state.MessageID,
			"type":          "message",
			"role":          "assistant",
			"content":       []any{},
			"model":         state.Model,
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]any{"input_tokens": inputTokens, "output_tokens": 0},
		},
	})
}

func blockStart(state *claudeStreamState, blockType, id, name string) []byte {
	var cb map[string]any
	switch blockType {
	case "tool_use":
		cb = map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{}}
	case "thinking":
		cb = map[string]any{"type": "thinking", "thinking": ""}
	default:
		cb = map[string]any{"type": "text", "text": ""}
	}
	return sseEvent("content_block_start", mustJSON(map[string]any{
		"type":          "content_block_start",
		"index":         state.BlockIndex,
		"content_block": cb,
	}))
}

func closeBlock(out *[][]byte, state *claudeStreamState) {
	*out = append(*out, sseEvent("content_block_stop", mustJSON(map[string]any{
		"type":  "content_block_stop",
		"index": state.BlockIndex,
	})))
	state.BlockIndex++
	state.TextOpen = false
	state.ThinkingOpen = false
	state.ToolOpen = false
}

func textDelta(text string, index int) []byte {
	return sseEvent("content_block_delta", mustJSON(map[string]any{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]any{"type": "text_delta", "text": text},
	}))
}

func thinkingDelta(text string, index int) []byte {
	return sseEvent("content_block_delta", mustJSON(map[string]any{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]any{"type": "thinking_delta", "thinking": text},
	}))
}

func inputJSONDelta(partialJSON string, index int) []byte {
	return sseEvent("content_block_delta", mustJSON(map[string]any{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": partialJSON},
	}))
}

// mapStopReason maps wire finish reasons to Claude stop reasons.
func mapStopReason(reason string) string {
	switch reason {
	case "tool-calls", "tool_calls", "tool_use":
		return "tool_use"
	case "length", "max_tokens":
		return "max_tokens"
	default:
		return "end_turn"
	}
}

// parseUsage extracts totalUsage from a finish event.
func parseUsage(event gjson.Result) *cc.FinishUsage {
	u := event.Get("totalUsage")
	if !u.Exists() {
		return nil
	}
	out := &cc.FinishUsage{
		InputTokens:       u.Get("inputTokens").Int(),
		OutputTokens:      u.Get("outputTokens").Int(),
		InputTokenDetails: &cc.FinishUsageInputDetails{},
	}
	if d := u.Get("inputTokenDetails"); d.Exists() {
		out.InputTokenDetails.CacheReadTokens = d.Get("cacheReadTokens").Int()
		out.InputTokenDetails.CacheWriteTokens = d.Get("cacheWriteTokens").Int()
	}
	return out
}

// newMessageID creates a fresh message identifier.
func newMessageID() string {
	n := atomic.AddUint64(&messageIDCounter, 1)
	return "msg_" + strconv.FormatUint(n, 16)
}
