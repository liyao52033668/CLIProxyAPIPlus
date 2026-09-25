package chat_completions

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToClaude_MaxCompletionTokensFallback(t *testing.T) {
	result := gjson.ParseBytes(ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(`{"max_completion_tokens":123,"messages":[{"role":"user","content":"hi"}]}`), false))
	if got := result.Get("max_tokens").Int(); got != 123 {
		t.Fatalf("max_tokens = %d, want 123", got)
	}

	both := gjson.ParseBytes(ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(`{"max_tokens":456,"max_completion_tokens":123,"messages":[{"role":"user","content":"hi"}]}`), false))
	if got := both.Get("max_tokens").Int(); got != 456 {
		t.Fatalf("max_tokens with both fields = %d, want 456", got)
	}
}

func TestConvertOpenAIRequestToClaude_ToolResultTextAndBase64Image(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [
			{
				"role": "assistant",
				"content": "",
				"tool_calls": [
					{
						"id": "call_1",
						"type": "function",
						"function": {
							"name": "do_work",
							"arguments": "{\"a\":1}"
						}
					}
				]
			},
			{
				"role": "tool",
				"tool_call_id": "call_1",
				"content": [
					{"type": "text", "text": "tool ok"},
					{
						"type": "image_url",
						"image_url": {
							"url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUg=="
						}
					}
				]
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}

	toolResult := messages[1].Get("content.0")
	if got := toolResult.Get("type").String(); got != "tool_result" {
		t.Fatalf("Expected content[0].type %q, got %q", "tool_result", got)
	}
	if got := toolResult.Get("tool_use_id").String(); got != "call_1" {
		t.Fatalf("Expected tool_use_id %q, got %q", "call_1", got)
	}

	toolContent := toolResult.Get("content")
	if !toolContent.IsArray() {
		t.Fatalf("Expected tool_result content array, got %s", toolContent.Raw)
	}
	if got := toolContent.Get("0.type").String(); got != "text" {
		t.Fatalf("Expected first tool_result part type %q, got %q", "text", got)
	}
	if got := toolContent.Get("0.text").String(); got != "tool ok" {
		t.Fatalf("Expected first tool_result part text %q, got %q", "tool ok", got)
	}
	if got := toolContent.Get("1.type").String(); got != "image" {
		t.Fatalf("Expected second tool_result part type %q, got %q", "image", got)
	}
	if got := toolContent.Get("1.source.type").String(); got != "base64" {
		t.Fatalf("Expected image source type %q, got %q", "base64", got)
	}
	if got := toolContent.Get("1.source.media_type").String(); got != "image/png" {
		t.Fatalf("Expected image media type %q, got %q", "image/png", got)
	}
	if got := toolContent.Get("1.source.data").String(); got != "iVBORw0KGgoAAAANSUhEUg==" {
		t.Fatalf("Unexpected base64 image data: %q", got)
	}
}

func TestConvertOpenAIRequestToClaude_ToolResultURLImageOnly(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [
			{
				"role": "assistant",
				"content": "",
				"tool_calls": [
					{
						"id": "call_1",
						"type": "function",
						"function": {
							"name": "do_work",
							"arguments": "{\"a\":1}"
						}
					}
				]
			},
			{
				"role": "tool",
				"tool_call_id": "call_1",
				"content": [
					{
						"type": "image_url",
						"image_url": {
							"url": "https://example.com/tool.png"
						}
					}
				]
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}

	toolContent := messages[1].Get("content.0.content")
	if !toolContent.IsArray() {
		t.Fatalf("Expected tool_result content array, got %s", toolContent.Raw)
	}
	if got := toolContent.Get("0.type").String(); got != "image" {
		t.Fatalf("Expected tool_result part type %q, got %q", "image", got)
	}
	if got := toolContent.Get("0.source.type").String(); got != "url" {
		t.Fatalf("Expected image source type %q, got %q", "url", got)
	}
	if got := toolContent.Get("0.source.url").String(); got != "https://example.com/tool.png" {
		t.Fatalf("Unexpected image URL: %q", got)
	}
}

func TestConvertOpenAIRequestToClaude_SystemRoleBecomesTopLevelSystem(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "Hello"}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	system := resultJSON.Get("system")
	if !system.IsArray() {
		t.Fatalf("Expected top-level system array, got %s", system.Raw)
	}
	if len(system.Array()) != 1 {
		t.Fatalf("Expected 1 system block, got %d. System: %s", len(system.Array()), system.Raw)
	}
	if got := system.Get("0.type").String(); got != "text" {
		t.Fatalf("Expected system block type %q, got %q", "text", got)
	}
	if got := system.Get("0.text").String(); got != "You are a helpful assistant." {
		t.Fatalf("Expected system text %q, got %q", "You are a helpful assistant.", got)
	}

	messages := resultJSON.Get("messages").Array()
	if len(messages) != 1 {
		t.Fatalf("Expected 1 non-system message, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("Expected remaining message role %q, got %q", "user", got)
	}
	if got := messages[0].Get("content.0.text").String(); got != "Hello" {
		t.Fatalf("Expected user text %q, got %q", "Hello", got)
	}
}

func TestConvertOpenAIRequestToClaude_MultipleSystemMessagesMergedIntoTopLevelSystem(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [
			{"role": "system", "content": "Rule 1"},
			{"role": "system", "content": [{"type": "text", "text": "Rule 2"}]},
			{"role": "user", "content": "Hello"}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	system := resultJSON.Get("system").Array()
	if len(system) != 2 {
		t.Fatalf("Expected 2 system blocks, got %d. System: %s", len(system), resultJSON.Get("system").Raw)
	}
	if got := system[0].Get("text").String(); got != "Rule 1" {
		t.Fatalf("Expected first system text %q, got %q", "Rule 1", got)
	}
	if got := system[1].Get("text").String(); got != "Rule 2" {
		t.Fatalf("Expected second system text %q, got %q", "Rule 2", got)
	}

	messages := resultJSON.Get("messages").Array()
	if len(messages) != 1 {
		t.Fatalf("Expected 1 non-system message, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("Expected remaining message role %q, got %q", "user", got)
	}
	if got := messages[0].Get("content.0.text").String(); got != "Hello" {
		t.Fatalf("Expected user text %q, got %q", "Hello", got)
	}
}

func TestConvertOpenAIRequestToClaude_SystemOnlyInputKeepsFallbackUserMessage(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	system := resultJSON.Get("system").Array()
	if len(system) != 1 {
		t.Fatalf("Expected 1 system block, got %d. System: %s", len(system), resultJSON.Get("system").Raw)
	}
	if got := system[0].Get("text").String(); got != "You are a helpful assistant." {
		t.Fatalf("Expected system text %q, got %q", "You are a helpful assistant.", got)
	}

	messages := resultJSON.Get("messages").Array()
	if len(messages) != 1 {
		t.Fatalf("Expected 1 fallback message, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("Expected fallback message role %q, got %q", "user", got)
	}
	if got := messages[0].Get("content.0.type").String(); got != "text" {
		t.Fatalf("Expected fallback content type %q, got %q", "text", got)
	}
	if got := messages[0].Get("content.0.text").String(); got != "" {
		t.Fatalf("Expected fallback text %q, got %q", "", got)
	}
}

func TestConvertOpenAIRequestToClaude_PreservesContentPartCacheControl(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "cached prefix", "cache_control": {"type": "ephemeral"}},
					{"type": "text", "text": "fresh question"}
				]
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	if got := resultJSON.Get("messages.0.content.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("content.0.cache_control.type = %q, want ephemeral. Output: %s", got, result)
	}
	if resultJSON.Get("messages.0.content.1.cache_control").Exists() {
		t.Fatalf("content.1 should not have cache_control. Output: %s", result)
	}
	if got := resultJSON.Get("messages.0.content.0.text").String(); got != "cached prefix" {
		t.Fatalf("content.0.text = %q, want %q", got, "cached prefix")
	}
}

func TestConvertOpenAIRequestToClaude_PreservesMessageLevelCacheControl(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [
			{
				"role": "user",
				"content": "cache me",
				"cache_control": {"type": "ephemeral", "ttl": "1h"}
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	if got := resultJSON.Get("messages.0.content.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("content.0.cache_control.type = %q, want ephemeral. Output: %s", got, result)
	}
	if got := resultJSON.Get("messages.0.content.0.cache_control.ttl").String(); got != "1h" {
		t.Fatalf("content.0.cache_control.ttl = %q, want 1h. Output: %s", got, result)
	}
}

func TestConvertOpenAIRequestToClaude_PreservesToolCacheControl(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "lookup",
					"description": "Lookup something",
					"parameters": {"type": "object", "properties": {}}
				},
				"cache_control": {"type": "ephemeral"}
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	if got := resultJSON.Get("tools.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("tools.0.cache_control.type = %q, want ephemeral. Output: %s", got, result)
	}
	if got := resultJSON.Get("tools.0.name").String(); got != "lookup" {
		t.Fatalf("tools.0.name = %q, want lookup", got)
	}
}

func TestConvertOpenAIRequestToClaude_PartCacheControlWinsOverMessageLevel(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [
			{
				"role": "user",
				"cache_control": {"type": "ephemeral", "ttl": "1h"},
				"content": [
					{"type": "text", "text": "part cached", "cache_control": {"type": "ephemeral"}}
				]
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	if got := resultJSON.Get("messages.0.content.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("content.0.cache_control.type = %q, want ephemeral. Output: %s", got, result)
	}
	if resultJSON.Get("messages.0.content.0.cache_control.ttl").Exists() {
		t.Fatalf("part-level cache_control should win; unexpected ttl: %s", result)
	}
}

func TestConvertOpenAIRequestToClaude_ResponseFormatJSONSchema(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-6",
		"messages": [{"role": "user", "content": "Extract facts from: Yesterday it rained in Beijing."}],
		"response_format": {
			"type": "json_schema",
			"json_schema": {
				"name": "extracted_facts",
				"strict": true,
				"schema": {
					"type": "object",
					"properties": {
						"facts": {
							"type": "array",
							"items": { "type": "string" }
						}
					},
					"required": ["facts"]
				}
			}
		}
	}`)

	out := ConvertOpenAIRequestToClaude("claude-sonnet-4-6", inputJSON, false)
	system := gjson.GetBytes(out, "system")
	if !system.Exists() || !system.IsArray() || len(system.Array()) == 0 {
		t.Fatalf("system blocks missing or empty. Output: %s", string(out))
	}

	foundSchemaInstruction := false
	for _, block := range system.Array() {
		text := block.Get("text").String()
		if strings.Contains(text, "JSON") && strings.Contains(text, "facts") {
			foundSchemaInstruction = true
			break
		}
	}
	if !foundSchemaInstruction {
		t.Fatalf("expected structured output instructions containing schema in system prompt. Output: %s", string(out))
	}
}

func TestConvertOpenAIRequestToClaude_ResponseFormatJSONObject(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-6",
		"messages": [{"role": "user", "content": "Return a JSON object."}],
		"response_format": {
			"type": "json_object"
		}
	}`)

	out := ConvertOpenAIRequestToClaude("claude-sonnet-4-6", inputJSON, false)
	system := gjson.GetBytes(out, "system")
	if !system.Exists() || !system.IsArray() || len(system.Array()) == 0 {
		t.Fatalf("system blocks missing or empty. Output: %s", string(out))
	}

	foundJSONInstruction := false
	for _, block := range system.Array() {
		text := block.Get("text").String()
		if strings.Contains(text, "JSON object") {
			foundJSONInstruction = true
			break
		}
	}
	if !foundJSONInstruction {
		t.Fatalf("expected JSON object instruction in system prompt. Output: %s", string(out))
	}
}

func TestConvertOpenAIRequestToClaude_ResponseFormatPreservesExistingSystem(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-6",
		"messages": [
			{"role": "system", "content": "Custom operator instruction."},
			{"role": "user", "content": "Extract facts."}
		],
		"response_format": {
			"type": "json_object"
		}
	}`)

	out := ConvertOpenAIRequestToClaude("claude-sonnet-4-6", inputJSON, false)
	system := gjson.GetBytes(out, "system")
	if !system.Exists() || !system.IsArray() || len(system.Array()) < 2 {
		t.Fatalf("expected at least 2 system blocks (original + response_format). Output: %s", string(out))
	}

	hasOriginal := false
	hasResponseFormat := false
	for _, block := range system.Array() {
		text := block.Get("text").String()
		if strings.Contains(text, "Custom operator instruction.") {
			hasOriginal = true
		}
		if strings.Contains(text, "JSON object") {
			hasResponseFormat = true
		}
	}
	if !hasOriginal || !hasResponseFormat {
		t.Fatalf("expected both original system and response_format instruction. Output: %s", string(out))
	}
}

func TestConvertOpenAIRequestToClaude_ResponseFormatAbsentOrTextNoOp(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "absent",
			body: `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"plain text"}]}`,
		},
		{
			name: "type text",
			body: `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"plain text"}],"response_format":{"type":"text"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := ConvertOpenAIRequestToClaude("claude-sonnet-4-6", []byte(tt.body), false)
			if gjson.GetBytes(out, "system").Exists() {
				t.Fatalf("system blocks should not be created when response_format is absent or text. Output: %s", string(out))
			}
		})
	}
}

// Anthropic only accepts cache_control on the tool_result block itself, never
// inside tool_result.content. A part-level marker on an OpenAI tool message
// must be hoisted to the block, while ordinary message parts keep theirs.
func TestConvertOpenAIRequestToClaude_ToolResultPartCacheControlHoisted(t *testing.T) {
	inputJSON := []byte(`{
		"messages":[
			{"role":"user","content":[{"type":"text","text":"Use calc for 2+2.","cache_control":{"type":"ephemeral"}}]},
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"calc","arguments":"{\"expr\":\"2+2\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":[{"type":"text","text":"4","cache_control":{"type":"ephemeral"}}]}
		],
		"tools":[{"type":"function","function":{"name":"calc","description":"calc","parameters":{"type":"object","properties":{"expr":{"type":"string"}},"required":["expr"]}}}]
	}`)
	out := ConvertOpenAIRequestToClaude("claude-test", inputJSON, false)

	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 3 {
		t.Fatalf("message count = %d, want 3. Output: %s", len(messages), string(out))
	}

	// Ordinary user content parts keep their part-level cache_control.
	firstText := messages[0].Get("content.0")
	if got := firstText.Get("cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("user text part cache_control.type = %q, want ephemeral (must not be stripped). Output: %s", got, string(out))
	}

	// The tool_result block carries the hoisted marker.
	toolResult := messages[2].Get("content.0")
	if got := toolResult.Get("type").String(); got != "tool_result" {
		t.Fatalf("messages[2].content[0].type = %q, want tool_result. Output: %s", got, string(out))
	}
	if got := toolResult.Get("cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("tool_result block cache_control.type = %q, want ephemeral (hoisted from the part). Output: %s", got, string(out))
	}

	// ... and the inner content parts carry no cache_control.
	innerParts := toolResult.Get("content").Array()
	for i, part := range innerParts {
		if part.Get("cache_control").Exists() {
			t.Fatalf("tool_result.content[%d] must not carry cache_control. Output: %s", i, string(out))
		}
	}
}

func TestConvertOpenAIRequestToClaude_ToolChoice(t *testing.T) {
	t.Run("none produces type none", func(t *testing.T) {
		inputJSON := `{
			"model": "claude-sonnet-4-6",
			"messages": [{"role": "user", "content": "Answer without calling tools."}],
			"tool_choice": "none",
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}},
				{"type": "function", "function": {"name": "tool_b", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToClaude("claude-sonnet-4-6", []byte(inputJSON), false)
		gotType := gjson.GetBytes(result, "tool_choice.type").String()
		if gotType != "none" {
			t.Fatalf("expected tool_choice.type to be 'none', got %q. Output: %s", gotType, result)
		}
	})

	t.Run("object none produces type none", func(t *testing.T) {
		inputJSON := `{
			"model": "claude-sonnet-4-6",
			"messages": [{"role": "user", "content": "Answer without calling tools."}],
			"tool_choice": {"type": "none"},
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToClaude("claude-sonnet-4-6", []byte(inputJSON), false)
		gotType := gjson.GetBytes(result, "tool_choice.type").String()
		if gotType != "none" {
			t.Fatalf("expected tool_choice.type to be 'none', got %q. Output: %s", gotType, result)
		}
	})

	t.Run("allowed_tools filters tools and sets auto mode", func(t *testing.T) {
		inputJSON := `{
			"model": "claude-sonnet-4-6",
			"messages": [{"role": "user", "content": "Use tool_b"}],
			"tool_choice": {
				"type": "allowed_tools",
				"allowed_tools": {
					"mode": "auto",
					"tools": [{"type": "function", "function": {"name": "tool_b"}}]
				}
			},
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}},
				{"type": "function", "function": {"name": "tool_b", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToClaude("claude-sonnet-4-6", []byte(inputJSON), false)
		gotType := gjson.GetBytes(result, "tool_choice.type").String()
		tools := gjson.GetBytes(result, "tools").Array()
		if gotType != "auto" {
			t.Fatalf("expected tool_choice type='auto', got %q. Output: %s", gotType, result)
		}
		if len(tools) != 1 || tools[0].Get("name").String() != "tool_b" {
			t.Fatalf("expected tools to contain only tool_b, got %v. Output: %s", tools, result)
		}
	})

	t.Run("allowed_tools multi function filters tools and supports required mode", func(t *testing.T) {
		inputJSON := `{
			"model": "claude-sonnet-4-6",
			"messages": [{"role": "user", "content": "Use tools"}],
			"tool_choice": {
				"type": "allowed_tools",
				"allowed_tools": {
					"mode": "required",
					"tools": [
						{"type": "function", "function": {"name": "tool_b"}},
						{"type": "function", "function": {"name": "tool_c"}}
					]
				}
			},
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}},
				{"type": "function", "function": {"name": "tool_b", "parameters": {"type": "object", "properties": {}}}},
				{"type": "function", "function": {"name": "tool_c", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToClaude("claude-sonnet-4-6", []byte(inputJSON), false)
		gotType := gjson.GetBytes(result, "tool_choice.type").String()
		tools := gjson.GetBytes(result, "tools").Array()
		if gotType != "any" {
			t.Fatalf("expected tool_choice type='any', got %q. Output: %s", gotType, result)
		}
		if len(tools) != 2 {
			t.Fatalf("expected 2 tools, got %d. Output: %s", len(tools), result)
		}
	})

	t.Run("parallel_tool_calls false adds disable_parallel_tool_use", func(t *testing.T) {
		inputJSON := `{
			"model": "claude-sonnet-4-6",
			"messages": [{"role": "user", "content": "test"}],
			"tool_choice": "required",
			"parallel_tool_calls": false,
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToClaude("claude-sonnet-4-6", []byte(inputJSON), false)
		gotType := gjson.GetBytes(result, "tool_choice.type").String()
		gotDisable := gjson.GetBytes(result, "tool_choice.disable_parallel_tool_use").Bool()
		if gotType != "any" || !gotDisable {
			t.Fatalf("expected type='any' with disable_parallel_tool_use=true, got type=%q disable=%v. Output: %s", gotType, gotDisable, result)
		}
	})

	t.Run("parallel_tool_calls null does not add disable_parallel_tool_use", func(t *testing.T) {
		inputJSON := `{
			"model": "claude-sonnet-4-6",
			"messages": [{"role": "user", "content": "test"}],
			"tool_choice": "required",
			"parallel_tool_calls": null,
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToClaude("claude-sonnet-4-6", []byte(inputJSON), false)
		gotDisable := gjson.GetBytes(result, "tool_choice.disable_parallel_tool_use")
		if gotDisable.Exists() && gotDisable.Bool() {
			t.Fatalf("expected disable_parallel_tool_use to not be true for parallel_tool_calls=null. Output: %s", result)
		}
	})

	t.Run("parallel_tool_calls true does not add disable_parallel_tool_use", func(t *testing.T) {
		inputJSON := `{
			"model": "claude-sonnet-4-6",
			"messages": [{"role": "user", "content": "test"}],
			"tool_choice": "required",
			"parallel_tool_calls": true,
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToClaude("claude-sonnet-4-6", []byte(inputJSON), false)
		gotDisable := gjson.GetBytes(result, "tool_choice.disable_parallel_tool_use")
		if gotDisable.Exists() && gotDisable.Bool() {
			t.Fatalf("expected disable_parallel_tool_use to not be true for parallel_tool_calls=true. Output: %s", result)
		}
	})

	t.Run("omitted tool_choice with parallel_tool_calls false sets auto with disable_parallel_tool_use", func(t *testing.T) {
		inputJSON := `{
			"model": "claude-sonnet-4-6",
			"messages": [{"role": "user", "content": "test"}],
			"parallel_tool_calls": false,
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToClaude("claude-sonnet-4-6", []byte(inputJSON), false)
		gotType := gjson.GetBytes(result, "tool_choice.type").String()
		gotDisable := gjson.GetBytes(result, "tool_choice.disable_parallel_tool_use").Bool()
		if gotType != "auto" || !gotDisable {
			t.Fatalf("expected type='auto' with disable_parallel_tool_use=true, got type=%q disable=%v. Output: %s", gotType, gotDisable, result)
		}
	})

	t.Run("empty allowed_tools fails closed to type none", func(t *testing.T) {
		inputJSON := `{
			"model": "claude-sonnet-4-6",
			"messages": [{"role": "user", "content": "test"}],
			"tool_choice": {
				"type": "allowed_tools",
				"allowed_tools": {
					"tools": []
				}
			},
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToClaude("claude-sonnet-4-6", []byte(inputJSON), false)
		gotType := gjson.GetBytes(result, "tool_choice.type").String()
		if gotType != "none" {
			t.Fatalf("expected tool_choice type='none', got %q. Output: %s", gotType, result)
		}
	})

	t.Run("function choice with missing name fails closed to type none", func(t *testing.T) {
		inputJSON := `{
			"model": "claude-sonnet-4-6",
			"messages": [{"role": "user", "content": "test"}],
			"tool_choice": {"type": "function", "function": {}},
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToClaude("claude-sonnet-4-6", []byte(inputJSON), false)
		gotType := gjson.GetBytes(result, "tool_choice.type").String()
		if gotType != "none" {
			t.Fatalf("expected tool_choice type='none', got %q. Output: %s", gotType, result)
		}
	})

	t.Run("tool_choice null does not set tool_choice", func(t *testing.T) {
		inputJSON := `{
			"model": "claude-sonnet-4-6",
			"messages": [{"role": "user", "content": "test"}],
			"tool_choice": null,
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToClaude("claude-sonnet-4-6", []byte(inputJSON), false)
		if gjson.GetBytes(result, "tool_choice").Exists() {
			t.Fatalf("expected tool_choice not to be set when tool_choice is null, got: %s", result)
		}
	})
}

func TestConvertOpenAIRequestToClaude_SanitizesToolNamesAndProvidesFallbackSchema(t *testing.T) {
	inputJSON := `{
		"model": "claude-sonnet-4-6",
		"messages": [
			{
				"role": "assistant",
				"content": "calling tool",
				"tool_calls": [
					{
						"id": "call_1",
						"type": "function",
						"function": {
							"name": "mcp.server.special:get_time",
							"arguments": "{}"
						}
					}
				]
			},
			{
				"role": "tool",
				"tool_call_id": "call_1",
				"content": "12:00 PM"
			},
			{
				"role": "user",
				"content": "continue"
			}
		],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "mcp.server.special:get_time",
					"description": "Get current time"
				}
			},
			{
				"type": "function",
				"function": {
					"name": "clean_tool",
					"description": "Parameterless clean tool"
				}
			}
		],
		"tool_choice": {
			"type": "function",
			"function": {
				"name": "mcp.server.special:get_time"
			}
		}
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-6", []byte(inputJSON), false)

	// 1. Tool name in declarations must be sanitized
	tool0Name := gjson.GetBytes(result, "tools.0.name").String()
	if tool0Name != "mcp_server_special_get_time" {
		t.Fatalf("tools.0.name = %q, want mcp_server_special_get_time. Output: %s", tool0Name, result)
	}

	// 2. Parameterless tool must have a fallback input_schema object
	tool0Schema := gjson.GetBytes(result, "tools.0.input_schema")
	if !tool0Schema.Exists() || tool0Schema.Get("type").String() != "object" {
		t.Fatalf("tools.0.input_schema = %s, want object schema. Output: %s", tool0Schema, result)
	}
	tool1Schema := gjson.GetBytes(result, "tools.1.input_schema")
	if !tool1Schema.Exists() || tool1Schema.Get("type").String() != "object" {
		t.Fatalf("tools.1.input_schema = %s, want object schema. Output: %s", tool1Schema, result)
	}

	// 3. Historical tool_use name in assistant turn must be sanitized
	toolUseName := gjson.GetBytes(result, "messages.0.content.1.name").String()
	if toolUseName != "mcp_server_special_get_time" {
		t.Fatalf("messages.0.content.1.name = %q, want mcp_server_special_get_time. Output: %s", toolUseName, result)
	}

	// 4. Tool choice function name must be sanitized
	toolChoiceName := gjson.GetBytes(result, "tool_choice.name").String()
	if toolChoiceName != "mcp_server_special_get_time" {
		t.Fatalf("tool_choice.name = %q, want mcp_server_special_get_time. Output: %s", toolChoiceName, result)
	}
}

func TestConvertOpenAIRequestToClaude_ToolStrict(t *testing.T) {
	t.Run("preserves strict true on function tool", func(t *testing.T) {
		inputJSON := `{
			"model": "claude-sonnet-4-6",
			"messages": [{"role": "user", "content": "hi"}],
			"tools": [
				{
					"type": "function",
					"function": {
						"name": "tool_a",
						"description": "Controlled tool.",
						"strict": true,
						"parameters": {"type": "object", "properties": {}}
					}
				}
			]
		}`
		result := ConvertOpenAIRequestToClaude("claude-sonnet-4-6", []byte(inputJSON), false)
		toolStrict := gjson.GetBytes(result, "tools.0.strict")
		if !toolStrict.Exists() {
			t.Fatalf("expected tools.0.strict to exist in Claude output: %s", result)
		}
		if !toolStrict.Bool() {
			t.Fatalf("expected tools.0.strict to be true, got %v", toolStrict.Value())
		}
	})

	t.Run("preserves strict true when on top level tool", func(t *testing.T) {
		inputJSON := `{
			"model": "claude-sonnet-4-6",
			"messages": [{"role": "user", "content": "hi"}],
			"tools": [
				{
					"type": "function",
					"strict": true,
					"function": {
						"name": "tool_b",
						"description": "Controlled tool.",
						"parameters": {"type": "object", "properties": {}}
					}
				}
			]
		}`
		result := ConvertOpenAIRequestToClaude("claude-sonnet-4-6", []byte(inputJSON), false)
		toolStrict := gjson.GetBytes(result, "tools.0.strict")
		if !toolStrict.Exists() || !toolStrict.Bool() {
			t.Fatalf("expected tools.0.strict to be true, got %s", result)
		}
	})

	t.Run("preserves strict false on function tool", func(t *testing.T) {
		inputJSON := `{
			"model": "claude-sonnet-4-6",
			"messages": [{"role": "user", "content": "hi"}],
			"tools": [
				{
					"type": "function",
					"function": {
						"name": "tool_c",
						"description": "Controlled tool.",
						"strict": false,
						"parameters": {"type": "object", "properties": {}}
					}
				}
			]
		}`
		result := ConvertOpenAIRequestToClaude("claude-sonnet-4-6", []byte(inputJSON), false)
		toolStrict := gjson.GetBytes(result, "tools.0.strict")
		if !toolStrict.Exists() {
			t.Fatalf("expected tools.0.strict to exist in Claude output: %s", result)
		}
		if toolStrict.Bool() {
			t.Fatalf("expected tools.0.strict to be false, got %v", toolStrict.Value())
		}
	})

	t.Run("omits strict when not provided", func(t *testing.T) {
		inputJSON := `{
			"model": "claude-sonnet-4-6",
			"messages": [{"role": "user", "content": "hi"}],
			"tools": [
				{
					"type": "function",
					"function": {
						"name": "tool_d",
						"description": "Controlled tool.",
						"parameters": {"type": "object", "properties": {}}
					}
				}
			]
		}`
		result := ConvertOpenAIRequestToClaude("claude-sonnet-4-6", []byte(inputJSON), false)
		if gjson.GetBytes(result, "tools.0.strict").Exists() {
			t.Fatalf("expected tools.0.strict to be omitted when not provided, got %s", result)
		}
	})
}
