// Package claude provides request conversion helpers for the Claude to
// Command Code wire protocol translation.
package claude

import (
	"encoding/json"
	"strings"

	cc "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/commandcode"
	"github.com/tidwall/gjson"
)

// extractSystem concatenates the Claude system prompt (string or blocks).
func extractSystem(root gjson.Result) string {
	sys := root.Get("system")
	if !sys.Exists() {
		return ""
	}
	if sys.IsArray() {
		var sb strings.Builder
		sys.ForEach(func(_, part gjson.Result) bool {
			if s := part.Get("text").String(); s != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n\n")
				}
				sb.WriteString(s)
			}
			return true
		})
		return sb.String()
	}
	return sys.String()
}

// convertMessages maps Claude messages to wire messages. toolNames carries the
// tool_use id -> name mapping required on tool-result parts.
func convertMessages(root gjson.Result, toolNames map[string]string) []cc.WireMessage {
	var out []cc.WireMessage
	root.Get("messages").ForEach(func(_, msg gjson.Result) bool {
		role := msg.Get("role").String()
		content := msg.Get("content")

		if role == "assistant" {
			parts := make([]cc.WireContent, 0, 2)
			collect := func(part gjson.Result) {
				switch part.Get("type").String() {
				case "text":
					if s := part.Get("text").String(); s != "" {
						parts = append(parts, cc.WireContent{Type: "text", Text: s})
					}
				case "thinking":
					if s := part.Get("thinking").String(); s != "" {
						parts = append(parts, cc.WireContent{Type: "reasoning", Text: s})
					}
				case "tool_use":
					input := map[string]any{}
					if in := part.Get("input"); in.Exists() {
						_ = json.Unmarshal([]byte(in.Raw), &input)
					}
					parts = append(parts, cc.WireContent{
						Type:       "tool-call",
						ToolCallID: part.Get("id").String(),
						ToolName:   part.Get("name").String(),
						Input:      input,
					})
				}
			}
			if content.IsArray() {
				content.ForEach(func(_, part gjson.Result) bool {
					collect(part)
					return true
				})
			} else if s := content.String(); s != "" {
				parts = append(parts, cc.WireContent{Type: "text", Text: s})
			}
			if len(parts) > 0 {
				out = append(out, cc.WireMessage{Role: "assistant", Content: parts})
			}
			return true
		}

		// user role: split tool results and user-visible content.
		var toolParts []cc.WireContent
		var userParts []cc.WireContent
		handle := func(part gjson.Result) bool {
			switch part.Get("type").String() {
			case "tool_result":
				output := flattenToolResultContent(part.Get("content"))
				toolUseID := part.Get("tool_use_id").String()
				toolParts = append(toolParts, cc.WireContent{
					Type:       "tool-result",
					ToolCallID: toolUseID,
					ToolName:   toolNames[toolUseID],
					Output:     &cc.WireToolOutput{Type: "text", Value: output},
				})
			case "text":
				if s := part.Get("text").String(); s != "" {
					userParts = append(userParts, cc.WireContent{Type: "text", Text: s})
				}
			case "image":
				source := part.Get("source")
				media := source.Get("media_type").String()
				data := source.Get("data").String()
				if media != "" && data != "" {
					userParts = append(userParts, cc.WireContent{
						Type:  "image",
						Image: "data:" + media + ";base64," + data,
					})
				}
			case "document":
				// Unsupported upstream; skip.
			default:
				if s := part.String(); strings.TrimSpace(s) != "" && !strings.HasPrefix(strings.TrimSpace(s), "{") {
					userParts = append(userParts, cc.WireContent{Type: "text", Text: s})
				}
			}
			return true
		}
		if content.IsArray() {
			content.ForEach(func(_, part gjson.Result) bool {
				return handle(part)
			})
		} else if s := content.String(); s != "" {
			userParts = append(userParts, cc.WireContent{Type: "text", Text: s})
		}
		if len(toolParts) > 0 {
			// Role "tool" is a dedicated wire branch: each part must be a
			// tool-result carrying both toolCallId and toolName.
			out = append(out, cc.WireMessage{Role: "tool", Content: toolParts})
		}
		if len(userParts) > 0 {
			out = append(out, cc.WireMessage{Role: "user", Content: userParts})
		}
		return true
	})
	return out
}

// flattenToolResultContent flattens Claude tool_result content into text.
func flattenToolResultContent(c gjson.Result) string {
	if !c.Exists() {
		return ""
	}
	if c.IsArray() {
		var sb strings.Builder
		c.ForEach(func(_, part gjson.Result) bool {
			switch part.Get("type").String() {
			case "text":
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(part.Get("text").String())
			default:
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(part.Raw)
			}
			return true
		})
		return sb.String()
	}
	return c.String()
}

// convertTools maps Claude tool declarations to wire tools.
func convertTools(root gjson.Result) []cc.WireTool {
	result := root.Get("tools")
	if !result.Exists() || !result.IsArray() {
		return nil
	}
	var tools []cc.WireTool
	result.ForEach(func(_, tool gjson.Result) bool {
		name := tool.Get("name").String()
		if name == "" {
			return true
		}
		schema := tool.Get("input_schema")
		inputSchema := map[string]any{"type": "object"}
		if schema.Exists() {
			if err := json.Unmarshal([]byte(schema.Raw), &inputSchema); err != nil || inputSchema == nil {
				inputSchema = map[string]any{"type": "object"}
			}
		}
		tools = append(tools, cc.WireTool{
			Name:        name,
			Description: tool.Get("description").String(),
			InputSchema: inputSchema,
		})
		return true
	})
	return tools
}
