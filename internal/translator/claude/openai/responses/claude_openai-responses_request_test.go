package responses

import (
	"encoding/base64"
	"testing"

	sigcompat "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestConvertOpenAIResponsesRequestToClaude_ReasoningItemToThinkingBlock(t *testing.T) {
	rawSignature, expectedSignature := testClaudeResponsesThinkingSignature(t)
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{
				"type":"reasoning",
				"encrypted_content":"` + rawSignature + `",
				"summary":[{"type":"summary_text","text":"internal reasoning"}]
			},
			{
				"type":"message",
				"role":"assistant",
				"content":[{"type":"output_text","text":"visible answer"}]
			},
			{
				"type":"message",
				"role":"user",
				"content":[{"type":"input_text","text":"continue"}]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)

	assistant := root.Get("messages.0")
	if got := assistant.Get("role").String(); got != "assistant" {
		t.Fatalf("first message role = %q, want assistant. Output: %s", got, string(out))
	}
	if got := assistant.Get("content.0.type").String(); got != "thinking" {
		t.Fatalf("first content type = %q, want thinking. Output: %s", got, string(out))
	}
	if got := assistant.Get("content.0.signature").String(); got != expectedSignature {
		t.Fatalf("thinking signature = %q, want %q", got, expectedSignature)
	}
	if got := assistant.Get("content.0.thinking").String(); got != "internal reasoning" {
		t.Fatalf("thinking text = %q, want internal reasoning", got)
	}
	if got := assistant.Get("content.1.type").String(); got != "text" {
		t.Fatalf("second content type = %q, want text. Output: %s", got, string(out))
	}
	if got := assistant.Get("content.1.text").String(); got != "visible answer" {
		t.Fatalf("assistant text = %q, want visible answer", got)
	}
	if got := root.Get("messages.1.role").String(); got != "user" {
		t.Fatalf("second message role = %q, want user. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_SignatureOnlyReasoningFlushesBeforeUser(t *testing.T) {
	rawSignature, expectedSignature := testClaudeResponsesThinkingSignature(t)
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{
				"type":"reasoning",
				"encrypted_content":"` + rawSignature + `",
				"summary":[]
			},
			{
				"type":"message",
				"role":"user",
				"content":[{"type":"input_text","text":"continue"}]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)

	thinking := root.Get("messages.0.content.0")
	if got := thinking.Get("type").String(); got != "thinking" {
		t.Fatalf("first content type = %q, want thinking. Output: %s", got, string(out))
	}
	if got := thinking.Get("signature").String(); got != expectedSignature {
		t.Fatalf("thinking signature = %q, want %q", got, expectedSignature)
	}
	if got := thinking.Get("thinking").String(); got != "" {
		t.Fatalf("thinking text = %q, want empty", got)
	}
	if got := root.Get("messages.1.role").String(); got != "user" {
		t.Fatalf("second message role = %q, want user. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_DropsIncompatibleReasoningSignature(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{
				"type":"reasoning",
				"encrypted_content":"` + testGPTResponsesReasoningSignature() + `",
				"summary":[{"type":"summary_text","text":"must not become Claude thinking"}]
			},
			{
				"type":"message",
				"role":"user",
				"content":[{"type":"input_text","text":"continue"}]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)

	if gjson.GetBytes(out, "messages.0.content.0.type").String() == "thinking" {
		t.Fatalf("GPT encrypted_content should not become Claude thinking. Output: %s", string(out))
	}
	if gjson.GetBytes(out, "messages.0.content.0.signature").Exists() {
		t.Fatalf("incompatible signature should not be forwarded. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "user" {
		t.Fatalf("first message role = %q, want user. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_DropsEmptyReadPages(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[{
			"type":"function_call",
			"call_id":"call_read",
			"name":"Read",
			"arguments":"{\"file_path\":\"/tmp/file.go\",\"limit\":2000,\"offset\":0,\"pages\":\"\"}"
		}]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	input := gjson.GetBytes(out, "messages.0.content.0.input")

	if input.Get("pages").Exists() {
		t.Fatalf("empty Read.pages should be removed. Output: %s", string(out))
	}
	if got := input.Get("file_path").String(); got != "/tmp/file.go" {
		t.Fatalf("file_path = %q, want /tmp/file.go. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_DropsEmptyReadPagesForStreamingRequest(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[{
			"type":"function_call",
			"call_id":"call_read",
			"name":"Read",
			"arguments":"{\"file_path\":\"/tmp/file.go\",\"pages\":\"\"}"
		}]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, true)
	input := gjson.GetBytes(out, "messages.0.content.0.input")

	if !gjson.GetBytes(out, "stream").Bool() {
		t.Fatalf("stream should remain true. Output: %s", string(out))
	}
	if input.Get("pages").Exists() {
		t.Fatalf("empty Read.pages should be removed for streaming requests. Output: %s", string(out))
	}
	if got := input.Get("file_path").String(); got != "/tmp/file.go" {
		t.Fatalf("file_path = %q, want /tmp/file.go. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_KeepsNonReadEmptyPages(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[{
			"type":"function_call",
			"call_id":"call_other",
			"name":"Other",
			"arguments":"{\"pages\":\"\"}"
		}]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	pages := gjson.GetBytes(out, "messages.0.content.0.input.pages")

	if !pages.Exists() || pages.String() != "" {
		t.Fatalf("non-Read empty pages should be preserved. Output: %s", string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_KeepsNonEmptyReadPages(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[{
			"type":"function_call",
			"call_id":"call_read_pdf",
			"name":"Read",
			"arguments":"{\"file_path\":\"/tmp/file.pdf\",\"pages\":\"1-5\"}"
		}]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	pages := gjson.GetBytes(out, "messages.0.content.0.input.pages")

	if got := pages.String(); got != "1-5" {
		t.Fatalf("Read.pages = %q, want 1-5. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_MergesAdditionalToolsAndPrefersTopLevel(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"tools":[
			{
				"type":"function",
				"name":"exec",
				"description":"top-level exec",
				"parameters":{"type":"object","properties":{"command":{"type":"string"}}}
			},
			{
				"type":"namespace",
				"name":"collaboration",
				"tools":[{"type":"function","name":"spawn","description":"top-level spawn","parameters":{"type":"object","properties":{}}}]
			}
		],
		"input":[
			{
				"type":"additional_tools",
				"role":"developer",
				"tools":[
					{"type":"custom","name":"exec","description":"additional exec"},
					{"type":"function","name":"wait","parameters":{"type":"object","properties":{}}},
					{"type":"namespace","name":"collaboration","tools":[
						{"type":"function","name":"spawn","parameters":{"type":"object","properties":{}}},
						{"type":"custom","name":"send","description":"send a message"}
					]}
				]
			},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}
		]
	}`)

	root := gjson.ParseBytes(ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false))
	if got := root.Get("tools.#").Int(); got != 4 {
		t.Fatalf("tools count = %d, want 4; output=%s", got, root.Raw)
	}
	if got := root.Get(`tools.#(name=="exec").description`).String(); got != "top-level exec" {
		t.Fatalf("exec description = %q, want top-level exec", got)
	}
	if got := root.Get(`tools.#(name=="wait").name`).String(); got != "wait" {
		t.Fatalf("additional function name = %q, want wait", got)
	}
	if got := root.Get(`tools.#(name=="collaboration__spawn").name`).String(); got != "collaboration__spawn" {
		t.Fatalf("namespace function name = %q, want collaboration__spawn", got)
	}
	custom := root.Get(`tools.#(name=="collaboration__send")`)
	if !custom.Exists() {
		t.Fatal("missing namespace custom tool")
	}
	if got := custom.Get("input_schema.properties.input.type").String(); got != "string" {
		t.Fatalf("custom input schema type = %q, want string", got)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_DeduplicatesExpandedToolNames(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"tools":[{"type":"function","name":"collaboration__send","description":"top-level send","parameters":{"type":"object","properties":{}}}],
		"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"collaboration","tools":[
			{"type":"function","name":"send","description":"additional send","parameters":{"type":"object","properties":{}}},
			{"type":"function","name":"other","parameters":{"type":"object","properties":{}}}
		]}]}]
	}`)

	root := gjson.ParseBytes(ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false))
	if got := root.Get("tools.#").Int(); got != 2 {
		t.Fatalf("tools count = %d, want 2; output=%s", got, root.Raw)
	}
	if got := root.Get(`tools.#(name=="collaboration__send").description`).String(); got != "top-level send" {
		t.Fatalf("duplicate final name description = %q, want top-level send", got)
	}
	if !root.Get(`tools.#(name=="collaboration__other")`).Exists() {
		t.Fatal("unique namespace child was dropped")
	}
	customNames := responsesCustomToolNames(raw)
	if _, ok := customNames["collaboration__send"]; ok {
		t.Fatal("final-name collision should keep the top-level function type")
	}
}

func TestConvertOpenAIResponsesRequestToClaude_DirectToolWinsOverEarlierNamespaceCollision(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"tools":[
			{"type":"namespace","name":"n","tools":[{"type":"function","name":"x","parameters":{"type":"object","properties":{}}}]},
			{"type":"custom","name":"n__x"}
		],
		"tool_choice":{"type":"custom","name":"n__x"}
	}`)

	root := gjson.ParseBytes(ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false))
	if got := root.Get("tools.#").Int(); got != 1 {
		t.Fatalf("tools count = %d, want 1; output=%s", got, root.Raw)
	}
	if got := root.Get("tools.0.name").String(); got != "n__x" {
		t.Fatalf("winning tool name = %q, want n__x", got)
	}
	if got := root.Get("tools.0.input_schema.properties.input.type").String(); got != "string" {
		t.Fatalf("winning tool schema type = %q, want string for custom tool", got)
	}
	if got := root.Get("tool_choice.name").String(); got != "n__x" {
		t.Fatalf("tool_choice.name = %q, want n__x; output=%s", got, root.Raw)
	}
	if _, ok := responsesCustomToolNames(raw)["n__x"]; !ok {
		t.Fatal("winning direct custom tool was not classified as custom")
	}
}

func TestConvertOpenAIResponsesRequestToClaude_PrefersDirectToolAcrossAdditionalSources(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{"type":"additional_tools","tools":[{"type":"namespace","name":"n","tools":[{"type":"function","name":"x","description":"namespace x","parameters":{"type":"object","properties":{}}}]}]},
			{"type":"additional_tools","tools":[{"type":"custom","name":"n__x","description":"direct x"}]}
		]
	}`)

	root := gjson.ParseBytes(ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false))
	if got := root.Get("tools.#").Int(); got != 1 {
		t.Fatalf("tools count = %d, want 1; output=%s", got, root.Raw)
	}
	tool := root.Get("tools.0")
	if got := tool.Get("name").String(); got != "n__x" {
		t.Fatalf("winning tool name = %q, want n__x", got)
	}
	if got := tool.Get("description").String(); got != "direct x" {
		t.Fatalf("winning tool description = %q, want direct x", got)
	}
	if got := tool.Get("input_schema.properties.input.type").String(); got != "string" {
		t.Fatalf("winning tool schema type = %q, want string for custom tool", got)
	}
	if _, ok := responsesCustomToolNames(raw)["n__x"]; !ok {
		t.Fatal("direct custom tool should win classification across additional sources")
	}
}

func TestConvertOpenAIResponsesRequestToClaude_PreservesToolDeclarationOrder(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"tools":[
			{"type":"function","name":"first","parameters":{"type":"object","properties":{}}},
			{"type":"namespace","name":"n","tools":[{"type":"function","name":"middle","parameters":{"type":"object","properties":{}}}]},
			{"type":"function","name":"last","parameters":{"type":"object","properties":{}}}
		]
	}`)

	root := gjson.ParseBytes(ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false))
	want := []string{"first", "n__middle", "last"}
	got := root.Get("tools.#.name").Array()
	if len(got) != len(want) {
		t.Fatalf("tools count = %d, want %d; output=%s", len(got), len(want), root.Raw)
	}
	for i, wantName := range want {
		if got[i].String() != wantName {
			t.Errorf("tools[%d].name = %q, want %q", i, got[i].String(), wantName)
		}
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ReplaysCustomToolCallHistory(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{"type":"custom_tool_call","call_id":"call.custom:1","name":"exec","input":"pwd"},
			{"type":"custom_tool_call_output","call_id":"call.custom:1","output":"/workspace"}
		]
	}`)

	root := gjson.ParseBytes(ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false))
	toolUse := root.Get("messages.0.content.0")
	if got := toolUse.Get("type").String(); got != "tool_use" {
		t.Fatalf("tool use type = %q, want tool_use; output=%s", got, root.Raw)
	}
	if got := toolUse.Get("id").String(); got != "call_custom_1" {
		t.Fatalf("tool use id = %q, want call_custom_1", got)
	}
	if got := toolUse.Get("input.input").String(); got != "pwd" {
		t.Fatalf("custom tool input = %q, want pwd", got)
	}
	toolResult := root.Get("messages.1.content.0")
	if got := toolResult.Get("type").String(); got != "tool_result" {
		t.Fatalf("tool result type = %q, want tool_result", got)
	}
	if got := toolResult.Get("tool_use_id").String(); got != "call_custom_1" {
		t.Fatalf("tool result id = %q, want call_custom_1", got)
	}
	if got := toolResult.Get("content").String(); got != "/workspace" {
		t.Fatalf("tool result content = %q, want /workspace", got)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ReplaysNamespacedFunctionCallHistory(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{"type":"additional_tools","tools":[{"type":"namespace","name":"mcp__node_repl","tools":[{"type":"function","name":"js","parameters":{"type":"object","properties":{}}}]}]},
			{"type":"function_call","call_id":"call.namespace","name":"js","namespace":"mcp__node_repl","arguments":"{\"code\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"call.namespace","output":"ok"}
		]
	}`)

	root := gjson.ParseBytes(ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false))
	if !root.Get(`tools.#(name=="mcp__node_repl__js")`).Exists() {
		t.Fatal("missing qualified namespace tool declaration")
	}
	toolUse := root.Get("messages.0.content.0")
	if got := toolUse.Get("name").String(); got != "mcp__node_repl__js" {
		t.Fatalf("historical tool_use name = %q, want mcp__node_repl__js", got)
	}
	if got := root.Get("messages.1.content.0.tool_use_id").String(); got != "call_namespace" {
		t.Fatalf("historical tool_result id = %q, want call_namespace", got)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_MapsCustomAndNamespacedToolChoice(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		wantToolName string
	}{
		{
			name: "custom",
			raw: `{
				"model":"claude-test",
				"tools":[{"type":"custom","name":"exec"}],
				"tool_choice":{"type":"custom","name":"exec"}
			}`,
			wantToolName: "exec",
		},
		{
			name: "namespace",
			raw: `{
				"model":"claude-test",
				"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"mcp__node_repl","tools":[{"type":"function","name":"js"}]}]}],
				"tool_choice":{"type":"function","name":"js","namespace":"mcp__node_repl"}
			}`,
			wantToolName: "mcp__node_repl__js",
		},
		{
			name: "top-level-short-name-wins",
			raw: `{
				"model":"claude-test",
				"tools":[{"type":"function","name":"foo"}],
				"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"mcp__tools","tools":[{"type":"function","name":"foo"}]}]}],
				"tool_choice":{"type":"function","name":"foo"}
			}`,
			wantToolName: "foo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := gjson.ParseBytes(ConvertOpenAIResponsesRequestToClaude("claude-test", []byte(tt.raw), false))
			if got := root.Get("tool_choice.type").String(); got != "tool" {
				t.Fatalf("tool_choice.type = %q, want tool; output=%s", got, root.Raw)
			}
			if got := root.Get("tool_choice.name").String(); got != tt.wantToolName {
				t.Fatalf("tool_choice.name = %q, want %q", got, tt.wantToolName)
			}
		})
	}
}

func TestQualifyResponsesNamespaceToolNameAvoidsPrefixCollision(t *testing.T) {
	tests := []struct {
		namespace string
		child     string
		want      string
	}{
		{namespace: "collab", child: "collaboration", want: "collab__collaboration"},
		{namespace: "collab", child: "collab__send", want: "collab__send"},
		{namespace: "collab__", child: "send", want: "collab__send"},
		{namespace: "mcp__node_repl", child: "mcp__node_repl__js", want: "mcp__node_repl__js"},
	}

	for _, tt := range tests {
		got := qualifyResponsesNamespaceToolName(tt.namespace, tt.child)
		if got != tt.want {
			t.Errorf("qualifyResponsesNamespaceToolName(%q, %q) = %q, want %q", tt.namespace, tt.child, got, tt.want)
		}
	}

	raw := []byte(`{
		"tools":[{"type":"namespace","name":"collab","tools":[{"type":"function","name":"collaboration"}]}]
	}`)
	root := gjson.ParseBytes(ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false))
	if got := root.Get("tools.0.name").String(); got != "collab__collaboration" {
		t.Fatalf("qualified tool declaration = %q, want collab__collaboration", got)
	}
}

func testClaudeResponsesThinkingSignature(t *testing.T) (string, string) {
	t.Helper()
	channelBlock := []byte{}
	channelBlock = protowire.AppendTag(channelBlock, 1, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 12)
	channelBlock = protowire.AppendTag(channelBlock, 2, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 2)
	channelBlock = protowire.AppendTag(channelBlock, 6, protowire.BytesType)
	channelBlock = protowire.AppendString(channelBlock, "claude-sonnet-4-6")

	container := []byte{}
	container = protowire.AppendTag(container, 1, protowire.BytesType)
	container = protowire.AppendBytes(container, channelBlock)

	payload := []byte{}
	payload = protowire.AppendTag(payload, 2, protowire.BytesType)
	payload = protowire.AppendBytes(payload, container)
	payload = protowire.AppendTag(payload, 3, protowire.VarintType)
	payload = protowire.AppendVarint(payload, 1)

	rawSignature := base64.StdEncoding.EncodeToString(payload)
	normalized, ok := sigcompat.CompatibleSignatureForProvider(sigcompat.SignatureProviderClaude, rawSignature)
	if !ok {
		t.Fatal("test Claude signature should be compatible")
	}
	return rawSignature, normalized
}

func testGPTResponsesReasoningSignature() string {
	payload := make([]byte, 1+8+16+16+32)
	payload[0] = 0x80
	payload[8] = 1
	for i := 9; i < len(payload); i++ {
		payload[i] = byte(i)
	}
	return base64.URLEncoding.EncodeToString(payload)
}

func TestConvertOpenAIResponsesRequestToClaude_PreservesContentPartCacheControl(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"input": [
			{
				"type": "message",
				"role": "user",
				"content": [
					{"type": "input_text", "text": "cached prefix", "cache_control": {"type": "ephemeral"}},
					{"type": "input_text", "text": "fresh question"}
				]
			}
		]
	}`

	result := ConvertOpenAIResponsesRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	content := resultJSON.Get("messages.0.content")
	if !content.IsArray() {
		t.Fatalf("expected content array when cache_control is present, got %s", result)
	}
	if got := content.Get("0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("content.0.cache_control.type = %q, want ephemeral. Output: %s", got, result)
	}
	if content.Get("1.cache_control").Exists() {
		t.Fatalf("content.1 should not have cache_control. Output: %s", result)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_RedactedReasoningItemRestoresRedactedThinking(t *testing.T) {
	const data = "EroBCkYIBRgCKkA"
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{
				"type":"reasoning",
				"encrypted_content":"` + ClaudeResponsesRedactedThinkingPrefix + data + `",
				"summary":[]
			},
			{
				"type":"message",
				"role":"assistant",
				"content":[{"type":"output_text","text":"visible answer"}]
			},
			{
				"type":"message",
				"role":"user",
				"content":[{"type":"input_text","text":"continue"}]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)

	block := root.Get("messages.0.content.0")
	if got := block.Get("type").String(); got != "redacted_thinking" {
		t.Fatalf("first content type = %q, want redacted_thinking. Output: %s", got, string(out))
	}
	if got := block.Get("data").String(); got != data {
		t.Fatalf("redacted_thinking data = %q, want %q", got, data)
	}
	if block.Get("signature").Exists() {
		t.Fatalf("redacted_thinking must not carry a signature. Output: %s", string(out))
	}
	if got := root.Get("messages.0.content.1.text").String(); got != "visible answer" {
		t.Fatalf("assistant text = %q, want visible answer. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_EmptyRedactedReasoningItemIsDropped(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{
				"type":"reasoning",
				"encrypted_content":"` + ClaudeResponsesRedactedThinkingPrefix + `",
				"summary":[]
			},
			{
				"type":"message",
				"role":"user",
				"content":[{"type":"input_text","text":"continue"}]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)

	if got := root.Get("messages.#").Int(); got != 1 {
		t.Fatalf("message count = %d, want only the user turn. Output: %s", got, string(out))
	}
	if got := root.Get("messages.0.role").String(); got != "user" {
		t.Fatalf("first message role = %q, want user. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ReasoningContentTextRebuildsThinking(t *testing.T) {
	rawSignature, expectedSignature := testClaudeResponsesThinkingSignature(t)
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{
				"type":"reasoning",
				"encrypted_content":"` + rawSignature + `",
				"summary":[],
				"content":[{"type":"reasoning_text","text":"restored from content"}]
			},
			{
				"type":"message",
				"role":"user",
				"content":[{"type":"input_text","text":"continue"}]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)

	thinking := root.Get("messages.0.content.0")
	if got := thinking.Get("thinking").String(); got != "restored from content" {
		t.Fatalf("thinking text = %q, want restored from content. Output: %s", got, string(out))
	}
	if got := thinking.Get("signature").String(); got != expectedSignature {
		t.Fatalf("thinking signature = %q, want %q", got, expectedSignature)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_SummaryWinsOverDuplicatedReasoningContent(t *testing.T) {
	rawSignature, _ := testClaudeResponsesThinkingSignature(t)
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{
				"type":"reasoning",
				"encrypted_content":"` + rawSignature + `",
				"summary":[{"type":"summary_text","text":"chain of thought"}],
				"content":[{"type":"reasoning_text","text":"chain of thought"}]
			},
			{
				"type":"message",
				"role":"user",
				"content":[{"type":"input_text","text":"continue"}]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	if got := gjson.ParseBytes(out).Get("messages.0.content.0.thinking").String(); got != "chain of thought" {
		t.Fatalf("thinking text = %q, want the summary text exactly once. Output: %s", got, string(out))
	}
}
