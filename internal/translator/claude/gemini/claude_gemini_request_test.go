package gemini

import (
	"testing"

	"github.com/tidwall/gjson"
)

// Ported from upstream 75b854eb: MCP-style tool names must be sanitized for
// Claude and parameterless declarations must receive a fallback input_schema.
// Adaptations: the local translator reads the snake_case tool_config key and
// maps ANY mode to {"type":"any"} without a name (no allowedFunctionNames
// mapping), so the tool_choice assertion checks the type only.
func TestConvertGeminiRequestToClaude_SanitizesToolNamesAndProvidesFallbackSchema(t *testing.T) {
	inputJSON := `{
		"contents": [
			{
				"role": "model",
				"parts": [
					{
						"functionCall": {
							"name": "mcp.server:get_data",
							"args": {}
						}
					}
				]
			},
			{
				"role": "user",
				"parts": [
					{
						"functionResponse": {
							"name": "mcp.server:get_data",
							"response": {"result": "ok"}
						}
					}
				]
			}
		],
		"tools": [
			{
				"functionDeclarations": [
					{
						"name": "mcp.server:get_data",
						"description": "parameterless mcp tool"
					}
				]
			}
		],
		"tool_config": {
			"function_calling_config": {
				"mode": "ANY",
				"allowedFunctionNames": ["mcp.server:get_data"]
			}
		}
	}`

	result := ConvertGeminiRequestToClaude("claude-test", []byte(inputJSON), false)

	// 1. Tool declaration name sanitized
	toolName := gjson.GetBytes(result, "tools.0.name").String()
	if toolName != "mcp_server_get_data" {
		t.Fatalf("tools.0.name = %q, want mcp_server_get_data. Output: %s", toolName, result)
	}

	// 2. Fallback input_schema
	schema := gjson.GetBytes(result, "tools.0.input_schema")
	if !schema.Exists() || schema.Get("type").String() != "object" {
		t.Fatalf("tools.0.input_schema = %s, want object schema. Output: %s", schema, result)
	}

	// 3. Historical tool_use in model/assistant turn sanitized
	toolUseName := gjson.GetBytes(result, "messages.0.content.0.name").String()
	if toolUseName != "mcp_server_get_data" {
		t.Fatalf("messages.0.content.0.name = %q, want mcp_server_get_data. Output: %s", toolUseName, result)
	}

	// 4. Tool choice maps ANY mode to the any type
	toolChoiceType := gjson.GetBytes(result, "tool_choice.type").String()
	if toolChoiceType != "any" {
		t.Fatalf("tool_choice.type = %q, want any. Output: %s", toolChoiceType, result)
	}
}
