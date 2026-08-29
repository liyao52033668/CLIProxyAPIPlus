// Package openai provides request translation from OpenAI Chat Completions
// to the Command Code wire protocol.
package openai

import (
	"encoding/json"
	"strings"

	cc "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/commandcode"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// ConvertOpenAIToCommandCodeRequest converts an OpenAI Chat Completions request
// body into the /alpha/generate envelope.
func ConvertOpenAIToCommandCodeRequest(modelName string, inputRawJSON []byte, stream bool) []byte {
	root := gjson.ParseBytes(inputRawJSON)

	params := cc.WireParams{
		Model:     modelName,
		Messages:  convertMessages(root),
		Tools:     convertTools(root),
		System:    ExtractOpenAISystem(inputRawJSON),
		MaxTokens: root.Get("max_tokens").Int(),
		Stream:    stream,
	}
	if params.MaxTokens <= 0 {
		params.MaxTokens = 64000
	}
	if t := root.Get("temperature"); t.Exists() {
		v := t.Float()
		params.Temperature = &v
	}
	if re := root.Get("reasoning_effort"); re.Exists() && re.String() != "" {
		params.ReasoningEffort = re.String()
	}

	req := cc.WireRequest{
		Config:         cc.DefaultServerConfig(),
		Memory:         "",
		Taste:          nil,
		Skills:         nil,
		PermissionMode: "standard",
		Params:         params,
	}
	data, err := json.Marshal(req)
	if err != nil {
		log.Errorf("commandcode: failed to marshal request: %v", err)
		return inputRawJSON
	}
	return data
}

// convertMessages maps OpenAI messages to wire messages. System and developer
// messages are skipped here; their text is carried in params.system via
// ExtractOpenAISystem. The upstream schema only accepts "user" and "assistant"
// roles, so tool results ride on user messages (Anthropic-style tool_result).
func convertMessages(root gjson.Result) []cc.WireMessage {
	var out []cc.WireMessage

	appendAssistantMessage := func(msg gjson.Result) {
		content := msg.Get("content")
		parts := make([]cc.WireContent, 0, 2)
		if content.IsArray() {
			content.ForEach(func(_, part gjson.Result) bool {
				switch part.Get("type").String() {
				case "text":
					parts = append(parts, cc.WireContent{Type: "text", Text: part.Get("text").String()})
				case "reasoning_content":
					if v := part.Get("reasoning_content").String(); v != "" {
						parts = append(parts, cc.WireContent{Type: "reasoning", ReasoningText: v})
					}
				default:
					// Plain string parts inside arrays.
					if s := strings.TrimSpace(part.Raw); s != "" {
						parts = append(parts, cc.WireContent{Type: "text", Text: part.String()})
					}
				}
				return true
			})
		} else if s := content.String(); s != "" {
			parts = append(parts, cc.WireContent{Type: "text", Text: s})
		}
		msg.Get("tool_calls").ForEach(func(_, call gjson.Result) bool {
			fn := call.Get("function")
			name := fn.Get("name").String()
			if name == "" {
				return true
			}
			input := map[string]any{}
			if args := strings.TrimSpace(fn.Get("arguments").String()); args != "" {
				_ = json.Unmarshal([]byte(args), &input)
			}
			parts = append(parts, cc.WireContent{
				Type:       "tool-call",
				ToolCallID: call.Get("id").String(),
				ToolName:   name,
				Input:      input,
			})
			return true
		})
		if len(parts) > 0 {
			out = append(out, cc.WireMessage{Role: "assistant", Content: parts})
		}
	}

	root.Get("messages").ForEach(func(_, msg gjson.Result) bool {
		role := msg.Get("role").String()
		switch role {
		case "system", "developer":
			// Skipped: carried in params.system by the caller.
		case "assistant":
			appendAssistantMessage(msg)
		case "tool":
			out = append(out, cc.WireMessage{Role: "user", Content: []cc.WireContent{{
				Type:       "tool-result",
				ToolCallID: msg.Get("tool_call_id").String(),
				Output: &cc.WireToolOutput{
					Type:  "text",
					Value: flattenContent(msg.Get("content")),
				},
			}}})
		case "user":
			parts := make([]cc.WireContent, 0, 1)
			c := msg.Get("content")
			if c.IsArray() {
				c.ForEach(func(_, part gjson.Result) bool {
					switch part.Get("type").String() {
					case "text":
						parts = append(parts, cc.WireContent{Type: "text", Text: part.Get("text").String()})
					case "image_url":
						url := part.Get("image_url.url").String()
						if url != "" {
							parts = append(parts, cc.WireContent{Type: "image", Image: url})
						}
					}
					return true
				})
			} else if s := c.String(); s != "" {
				parts = append(parts, cc.WireContent{Type: "text", Text: s})
			}
			if len(parts) > 0 {
				out = append(out, cc.WireMessage{Role: "user", Content: parts})
			}
		}
		return true
	})

	return out
}

// ExtractOpenAISystem returns the concatenated system prompt from an OpenAI body.
func ExtractOpenAISystem(body []byte) string {
	var sb strings.Builder
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		role := msg.Get("role").String()
		if role != "system" && role != "developer" {
			return true
		}
		c := msg.Get("content")
		if c.IsArray() {
			c.ForEach(func(_, part gjson.Result) bool {
				if s := part.Get("text").String(); s != "" {
					if sb.Len() > 0 {
						sb.WriteString("\n\n")
					}
					sb.WriteString(s)
				}
				return true
			})
		} else if s := c.String(); s != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString(s)
		}
		return true
	})
	return sb.String()
}

// flattenContent flattens an OpenAI tool message content into plain text.
func flattenContent(c gjson.Result) string {
	if !c.IsArray() {
		return c.String()
	}
	var sb strings.Builder
	c.ForEach(func(_, part gjson.Result) bool {
		if s := part.Get("text").String(); s != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(s)
		}
		return true
	})
	return sb.String()
}

// convertTools maps OpenAI tools declarations to wire tools.
func convertTools(root gjson.Result) []cc.WireTool {
	result := root.Get("tools")
	if !result.Exists() || !result.IsArray() {
		return nil
	}
	var tools []cc.WireTool
	result.ForEach(func(_, tool gjson.Result) bool {
		fn := tool.Get("function")
		if !fn.Exists() {
			fn = tool
		}
		name := fn.Get("name").String()
		if name == "" {
			return true
		}
		schema := fn.Get("parameters")
		inputSchema := map[string]any{"type": "object"}
		if schema.Exists() {
			if err := json.Unmarshal([]byte(schema.Raw), &inputSchema); err != nil || inputSchema == nil {
				inputSchema = map[string]any{"type": "object"}
			}
		}
		tools = append(tools, cc.WireTool{
			Name:        name,
			Description: fn.Get("description").String(),
			InputSchema: inputSchema,
		})
		return true
	})
	return tools
}
