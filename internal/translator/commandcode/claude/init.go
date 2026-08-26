// Package claude provides translation between Claude Messages API and the
// Command Code wire protocol.
package claude

import (
	"encoding/json"

	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
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

	params := cc.WireParams{
		Model:     modelName,
		Messages:  convertMessages(root),
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
	if re := root.Get("reasoning_effort"); re.Exists() && re.String() != "" {
		params.ReasoningEffort = re.String()
	} else if th := root.Get("thinking.budget_tokens"); th.Exists() && th.Int() > 0 {
		params.ReasoningEffort = "high"
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
