// Package claude provides translation between Claude Messages API and the
// Command Code wire protocol.
package claude

import (
	"encoding/json"

	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cc "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/commandcode"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/translator"
	"github.com/tidwall/gjson"
)

func init() {
	translator.Register(
		Claude, // source format
		CommandCode,
		ConvertClaudeToCommandCodeRequest,
		interfaces.TranslateResponse{
			Stream:    ConvertCommandCodeStreamToClaude,
			NonStream: ConvertCommandCodeNonStreamToClaude,
		},
	)
}

// ConvertClaudeToCommandCodeRequest converts a Claude Messages request body
// into the /alpha/generate envelope.
func ConvertClaudeToCommandCodeRequest(modelName string, inputRawJSON []byte, stream bool) []byte {
	root := gjson.ParseBytes(inputRawJSON)

	// toolNames maps tool_use ids to declared names so tool-result parts can
	// carry the toolName field the upstream schema requires.
	toolNames := make(map[string]string)
	root.Get("messages").ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "assistant" {
			return true
		}
		msg.Get("content").ForEach(func(_, part gjson.Result) bool {
			if part.Get("type").String() == "tool_use" {
				id := part.Get("id").String()
				name := part.Get("name").String()
				if id != "" && name != "" {
					toolNames[id] = name
				}
			}
			return true
		})
		return true
	})

	params := cc.WireParams{
		Model:     modelName,
		Messages:  convertMessages(root, toolNames),
		Tools:     convertTools(root),
		System:    extractSystem(root),
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
	// Carry the client's thinking config into the wire envelope. The thinking
	// pipeline re-extracts it from params.reasoning_effort, validates it against
	// the model's ThinkingSupport, and applies the final value (suffix wins).
	if re := root.Get("reasoning_effort"); re.Exists() && re.String() != "" {
		params.ReasoningEffort = re.String()
	} else if tt := root.Get("thinking.type").String(); tt == "disabled" {
		params.ReasoningEffort = "none"
	} else if th := root.Get("thinking.budget_tokens"); th.Exists() {
		if level, ok := thinking.ConvertBudgetToLevel(int(th.Int())); ok {
			params.ReasoningEffort = level
		}
	} else if tt := root.Get("thinking.type").String(); tt == "enabled" {
		params.ReasoningEffort = "auto"
	} else if tt := root.Get("thinking.type").String(); tt == "adaptive" {
		if effort := root.Get("output_config.effort").String(); effort != "" {
			params.ReasoningEffort = effort
		}
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
		return inputRawJSON
	}
	return data
}
