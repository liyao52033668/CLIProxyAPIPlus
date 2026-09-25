package responses

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func prettyJSONForTest(raw []byte) string {
	if !gjson.ValidBytes(raw) {
		return string(raw)
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return string(raw)
	}
	return out.String()
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_ConvertsStructuredCustomToolImageOutput(t *testing.T) {
	raw := []byte(`{"input":[{"type":"custom_tool_call","call_id":"call_image","name":"view_image","input":"{}"},{"type":"custom_tool_call_output","call_id":"call_image","output":"[{\"type\":\"input_image\",\"image_url\":\"data:image/png;base64,AA==\",\"detail\":\"original\"}]"}]}`)
	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, false)
	content := gjson.GetBytes(out, "messages.1.content")
	if !content.IsArray() {
		t.Fatalf("content should be an array, got %s", content.Raw)
	}
	if got := content.Get("0.image_url.url").String(); got != "data:image/png;base64,AA==" {
		t.Fatalf("image URL = %q", got)
	}
	if got := content.Get("0.image_url.detail").String(); got != "high" {
		t.Fatalf("image detail = %q, want high", got)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesCustomToolTextOutput(t *testing.T) {
	raw := []byte(`{"input":[{"type":"custom_tool_call","call_id":"call_text","name":"inspect","input":"{}"},{"type":"custom_tool_call_output","call_id":"call_text","output":"plain output"}]}`)
	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, false)
	if got := gjson.GetBytes(out, "messages.1.content").String(); got != "plain output" {
		t.Fatalf("custom tool content = %q, want plain output", got)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MergeConsecutiveFunctionCalls(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"exec_command:0","name":"exec_command","arguments":"{\"cmd\":\"ls\"}"},
			{"type":"function_call","call_id":"exec_command:1","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"exec_command:0","output":"ok0"},
			{"type":"function_call_output","call_id":"exec_command:1","output":"ok1"}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, true)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	msgs := gjson.GetBytes(out, "messages")
	if !msgs.Exists() || !msgs.IsArray() {
		t.Fatalf("messages should be an array")
	}
	if got := len(msgs.Array()); got != 3 {
		t.Fatalf("messages count = %d, want %d", got, 3)
	}

	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want %q", got, "assistant")
	}
	if got := len(gjson.GetBytes(out, "messages.0.tool_calls").Array()); got != 2 {
		t.Fatalf("messages.0.tool_calls length = %d, want %d", got, 2)
	}
	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String(); got != "exec_command:0" {
		t.Fatalf("messages.0.tool_calls.0.id = %q, want %q", got, "exec_command:0")
	}
	if got := gjson.GetBytes(out, "messages.0.tool_calls.1.id").String(); got != "exec_command:1" {
		t.Fatalf("messages.0.tool_calls.1.id = %q, want %q", got, "exec_command:1")
	}

	if got := gjson.GetBytes(out, "messages.1.tool_call_id").String(); got != "exec_command:0" {
		t.Fatalf("messages.1.tool_call_id = %q, want %q", got, "exec_command:0")
	}
	if got := gjson.GetBytes(out, "messages.2.tool_call_id").String(); got != "exec_command:1" {
		t.Fatalf("messages.2.tool_call_id = %q, want %q", got, "exec_command:1")
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_SplitFunctionCallsWhenInterrupted(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
			{"type":"message","role":"user","content":"next"},
			{"type":"function_call","call_id":"call_b","name":"tool_b","arguments":"{}"}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, false)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := len(gjson.GetBytes(out, "messages").Array()); got != 3 {
		t.Fatalf("messages count = %d, want %d", got, 3)
	}
	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String(); got != "call_a" {
		t.Fatalf("messages.0.tool_calls.0.id = %q, want %q", got, "call_a")
	}
	if got := gjson.GetBytes(out, "messages.2.tool_calls.0.id").String(); got != "call_b" {
		t.Fatalf("messages.2.tool_calls.0.id = %q, want %q", got, "call_b")
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_DefersMessageUntilToolOutput(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_x","name":"exec_command","arguments":"{\"cmd\":\"echo hi\"}"},
			{"type":"message","role":"user","content":"Approved command prefix saved"},
			{"type":"function_call_output","call_id":"call_x","output":"ok"},
			{"type":"message","role":"user","content":"next"}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, true)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := len(gjson.GetBytes(out, "messages").Array()); got != 4 {
		t.Fatalf("messages count = %d, want %d", got, 4)
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want %q", got, "assistant")
	}
	if got := gjson.GetBytes(out, "messages.1.role").String(); got != "tool" {
		t.Fatalf("messages.1.role = %q, want %q", got, "tool")
	}
	if got := gjson.GetBytes(out, "messages.1.tool_call_id").String(); got != "call_x" {
		t.Fatalf("messages.1.tool_call_id = %q, want %q", got, "call_x")
	}
	if got := gjson.GetBytes(out, "messages.2.role").String(); got != "user" {
		t.Fatalf("messages.2.role = %q, want %q", got, "user")
	}
	if got := gjson.GetBytes(out, "messages.2.content").String(); got != "Approved command prefix saved" {
		t.Fatalf("messages.2.content = %q, want %q", got, "Approved command prefix saved")
	}
	if got := gjson.GetBytes(out, "messages.3.content").String(); got != "next" {
		t.Fatalf("messages.3.content = %q, want %q", got, "next")
	}
}
func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesJSONSchemaTextFormat(t *testing.T) {
	raw := []byte(`{
		"text": {
			"format": {
				"type": "json_schema",
				"name": "answer",
				"description": "Structured answer",
				"strict": true,
				"schema": {
					"type": "object",
					"properties": {
						"ok": {"type": "boolean"}
					},
					"required": ["ok"],
					"additionalProperties": false
				}
			}
		}
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", raw, false)

	if got := gjson.GetBytes(out, "response_format.type").String(); got != "json_schema" {
		t.Fatalf("response_format.type = %q, want json_schema; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "response_format.json_schema.name").String(); got != "answer" {
		t.Fatalf("response_format.json_schema.name = %q, want answer; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "response_format.json_schema.description").String(); got != "Structured answer" {
		t.Fatalf("response_format.json_schema.description = %q, want Structured answer; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "response_format.json_schema.strict"); !got.Exists() || !got.Bool() {
		t.Fatalf("response_format.json_schema.strict = %v, want true; output=%s", got.Value(), out)
	}
	if got := gjson.GetBytes(out, "response_format.json_schema.schema.properties.ok.type").String(); got != "boolean" {
		t.Fatalf("response_format.json_schema.schema.properties.ok.type = %q, want boolean; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "response_format.json_schema.schema.required.0").String(); got != "ok" {
		t.Fatalf("response_format.json_schema.schema.required.0 = %q, want ok; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "response_format.json_schema.schema.additionalProperties"); !got.Exists() || got.Bool() {
		t.Fatalf("response_format.json_schema.schema.additionalProperties = %v, want false; output=%s", got.Value(), out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesJSONObjectTextFormat(t *testing.T) {
	raw := []byte(`{"text":{"format":{"type":"json_object"}}}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", raw, false)

	if got := gjson.GetBytes(out, "response_format.type").String(); got != "json_object" {
		t.Fatalf("response_format.type = %q, want json_object; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "response_format.json_schema"); got.Exists() {
		t.Fatalf("response_format.json_schema should be omitted; output=%s", out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_OmitsResponseFormatWithoutTextFormat(t *testing.T) {
	raw := []byte(`{"input":"Return plain text."}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", raw, false)

	if got := gjson.GetBytes(out, "response_format"); got.Exists() {
		t.Fatalf("response_format should be omitted, got %s; output=%s", got.Raw, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MapsReasoningEffort(t *testing.T) {
	raw := []byte(`{"input":"hi","reasoning":{"effort":"high"}}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.5", raw, false)

	if got := gjson.GetBytes(out, "reasoning_effort").String(); got != "high" {
		t.Fatalf("reasoning_effort = %q, want high; output=%s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_OmitsReasoningEffortWithoutReasoning(t *testing.T) {
	raw := []byte(`{"input":"hi"}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.5", raw, false)

	if got := gjson.GetBytes(out, "reasoning_effort"); got.Exists() {
		t.Fatalf("reasoning_effort should be omitted, got %s; output=%s", got.Raw, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_ConvertsCanonicalResponsesNamedToolChoice(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-5.4",
		"input": [{"role": "user", "content": "Call gateway_echo with value TOOL_OK."}],
		"tools": [{
			"type": "function",
			"name": "gateway_echo",
			"description": "Returns the given value",
			"parameters": {
				"type": "object",
				"properties": {"value": {"type": "string"}},
				"required": ["value"],
				"additionalProperties": false
			}
		}],
		"tool_choice": {"type": "function", "name": "gateway_echo"},
		"max_output_tokens": 512
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", raw, false)

	if got := gjson.GetBytes(out, "tool_choice.type").String(); got != "function" {
		t.Fatalf("tool_choice.type = %q, want function; output=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tool_choice.function.name").String(); got != "gateway_echo" {
		t.Fatalf("tool_choice.function.name = %q, want gateway_echo; output=%s", got, string(out))
	}
	if gjson.GetBytes(out, "tool_choice.name").Exists() {
		t.Fatalf("tool_choice.name should be absent at top-level; output=%s", string(out))
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_ConvertsNamespaceAndCustomToolChoice(t *testing.T) {
	rawNamespace := []byte(`{
		"model": "gpt-5.4",
		"input": "test",
		"tools": [
			{
				"type": "namespace",
				"name": "service_tools",
				"tools": [
					{
						"type": "function",
						"name": "lookup",
						"parameters": {"type": "object"}
					}
				]
			}
		],
		"tool_choice": {
			"type": "function",
			"name": "lookup"
		}
	}`)

	outNamespace := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", rawNamespace, false)
	if got := gjson.GetBytes(outNamespace, "tool_choice.type").String(); got != "function" {
		t.Fatalf("tool_choice.type = %q, want function; output=%s", got, string(outNamespace))
	}
	if got := gjson.GetBytes(outNamespace, "tool_choice.function.name").String(); got != "service_tools__lookup" {
		t.Fatalf("tool_choice.function.name = %q, want service_tools__lookup; output=%s", got, string(outNamespace))
	}
	if declaredToolName := gjson.GetBytes(outNamespace, "tools.0.function.name").String(); declaredToolName != gjson.GetBytes(outNamespace, "tool_choice.function.name").String() {
		t.Fatalf("tool_choice.function.name (%q) must match declared tools.0.function.name (%q); output=%s", gjson.GetBytes(outNamespace, "tool_choice.function.name").String(), declaredToolName, string(outNamespace))
	}
	if gjson.GetBytes(outNamespace, "tool_choice.name").Exists() {
		t.Fatalf("tool_choice.name should be absent at top-level; output=%s", string(outNamespace))
	}

	rawExplicitNamespace := []byte(`{
		"model": "gpt-5.4",
		"input": "test",
		"tools": [
			{
				"type": "namespace",
				"name": "service_tools",
				"tools": [
					{
						"type": "function",
						"name": "lookup",
						"parameters": {"type": "object"}
					}
				]
			}
		],
		"tool_choice": {
			"type": "function",
			"name": "lookup",
			"namespace": "service_tools"
		}
	}`)

	outExplicitNamespace := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", rawExplicitNamespace, false)
	if got := gjson.GetBytes(outExplicitNamespace, "tool_choice.function.name").String(); got != "service_tools__lookup" {
		t.Fatalf("explicit namespace tool_choice.function.name = %q, want service_tools__lookup; output=%s", got, string(outExplicitNamespace))
	}
	if gjson.GetBytes(outExplicitNamespace, "tool_choice.namespace").Exists() {
		t.Fatalf("tool_choice.namespace should be absent at top-level; output=%s", string(outExplicitNamespace))
	}
	if gjson.GetBytes(outExplicitNamespace, "tool_choice.name").Exists() {
		t.Fatalf("tool_choice.name should be absent at top-level; output=%s", string(outExplicitNamespace))
	}

	rawCustom := []byte(`{
		"model": "gpt-5.4",
		"input": "test",
		"tools": [
			{
				"type": "custom",
				"name": "patch_runner",
				"description": "Applies diff"
			}
		],
		"tool_choice": {
			"type": "custom",
			"name": "patch_runner"
		}
	}`)

	outCustom := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", rawCustom, false)
	if got := gjson.GetBytes(outCustom, "tool_choice.type").String(); got != "function" {
		t.Fatalf("tool_choice.type = %q, want function; output=%s", got, string(outCustom))
	}
	if got := gjson.GetBytes(outCustom, "tool_choice.function.name").String(); got != "patch_runner" {
		t.Fatalf("tool_choice.function.name = %q, want patch_runner; output=%s", got, string(outCustom))
	}

	for _, scalar := range []string{`"auto"`, `"none"`, `"required"`} {
		rawScalar := []byte(`{
			"model": "gpt-5.4",
			"input": "test",
			"tools": [
				{
					"type": "function",
					"name": "lookup",
					"parameters": {"type": "object"}
				}
			],
			"tool_choice": ` + scalar + `
		}`)
		outScalar := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", rawScalar, false)
		if got := gjson.GetBytes(outScalar, "tool_choice").Raw; got != scalar {
			t.Fatalf("tool_choice = %q, want %s; output=%s", got, scalar, string(outScalar))
		}
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_OrphanFunctionCallOutputBecomesUserMessage(t *testing.T) {
	inputJSON := []byte(`{
		"model": "deepseek-v4.1-flash",
		"input": [
			{"role":"user","content":[{"type":"input_text","text":"Task initialization"}]},
			{"type":"function_call_output","id":"fco_01a09fca-8d33-73a1-97fd-4d83ecc02f9d","name":"send_message_to_thread","output":"<codex_delegation>\n  <source_thread_id>01a022d7-d4d0-72b2-8571-4590484ccaee</source_thread_id>\n  <input>Execute sub-task</input>\n</codex_delegation>"},
			{"type":"function_call","call_id":"call_1789387253098037589_85","name":"Bash","arguments":"{\"command\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"call_1789387253098037589_85","id":"fco_01a09fca-a5f0-7b40-9943-21fbc923c537","output":"/Users/developer"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4.1-flash", inputJSON, false)
	messages := gjson.GetBytes(out, "messages").Array()

	delegationFound := false
	bashToolFound := false
	for _, message := range messages {
		role := message.Get("role").String()
		if role == "tool" && strings.TrimSpace(message.Get("tool_call_id").String()) == "" {
			t.Fatalf("orphan output emitted as tool message with empty tool_call_id: %s", string(out))
		}
		if role == "user" && strings.Contains(message.Get("content").String(), "<codex_delegation>") {
			delegationFound = true
		}
		if role == "tool" && message.Get("tool_call_id").String() == "call_1789387253098037589_85" {
			bashToolFound = true
			if got := message.Get("content").String(); got != "/Users/developer" {
				t.Fatalf("bash tool content = %q, want /Users/developer; output=%s", got, string(out))
			}
		}
	}
	if !delegationFound {
		t.Fatalf("expected orphan send_message_to_thread output as user content; output=%s", string(out))
	}
	if !bashToolFound {
		t.Fatalf("expected paired Bash tool message; output=%s", string(out))
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_UnpairedExplicitCallIDBecomesUserMessage(t *testing.T) {
	inputJSON := []byte(`{
		"model": "deepseek-v4.1-flash",
		"input": [
			{"role":"user","content":[{"type":"input_text","text":"Task initialization"}]},
			{"type":"function_call_output","call_id":"call_missing","name":"send_message_to_thread","output":"<codex_delegation>Execute sub-task</codex_delegation>"},
			{"type":"function_call","call_id":"call_1789387253098037589_85","name":"Bash","arguments":"{\"command\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"call_1789387253098037589_85","output":"/Users/developer"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4.1-flash", inputJSON, false)
	messages := gjson.GetBytes(out, "messages").Array()

	delegationFound := false
	bashToolFound := false
	for _, message := range messages {
		role := message.Get("role").String()
		if role == "tool" && message.Get("tool_call_id").String() == "call_missing" {
			t.Fatalf("unpaired output emitted as tool message: %s", string(out))
		}
		if role == "user" && strings.Contains(message.Get("content").String(), "<codex_delegation>") {
			delegationFound = true
		}
		if role == "tool" && message.Get("tool_call_id").String() == "call_1789387253098037589_85" {
			bashToolFound = true
		}
	}
	if !delegationFound {
		t.Fatalf("expected unpaired send_message_to_thread output as user content; output=%s", string(out))
	}
	if !bashToolFound {
		t.Fatalf("expected paired Bash tool message; output=%s", string(out))
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MapsMaxOutputTokensToMaxTokens(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-5.4",
		"input": "hello",
		"max_output_tokens": 1024
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", raw, false)

	if got := gjson.GetBytes(out, "max_tokens").Int(); got != 1024 {
		t.Fatalf("max_tokens = %d, want 1024; output=%s", got, string(out))
	}
	if gjson.GetBytes(out, "max_completion_tokens").Exists() {
		t.Fatalf("max_completion_tokens should be absent; output=%s", string(out))
	}

	rawWithoutLimit := []byte(`{
		"model": "gpt-5.4",
		"input": "hello"
	}`)

	outWithoutLimit := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", rawWithoutLimit, false)
	if gjson.GetBytes(outWithoutLimit, "max_completion_tokens").Exists() {
		t.Fatalf("max_completion_tokens should be absent when omitted; output=%s", string(outWithoutLimit))
	}
	if gjson.GetBytes(outWithoutLimit, "max_tokens").Exists() {
		t.Fatalf("max_tokens should be absent when omitted; output=%s", string(outWithoutLimit))
	}

	rawNull := []byte(`{
		"model": "gpt-5.4",
		"input": "hello",
		"max_output_tokens": null
	}`)

	outNull := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", rawNull, false)
	if got := gjson.GetBytes(outNull, "max_tokens"); !got.Exists() || got.Type != gjson.Null {
		t.Fatalf("max_tokens = %v, want null; output=%s", got, string(outNull))
	}
	if gjson.GetBytes(outNull, "max_completion_tokens").Exists() {
		t.Fatalf("max_completion_tokens should be absent; output=%s", string(outNull))
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesReasoningOnFollowUpToolTurns(t *testing.T) {
	raw := []byte(`{
		"model": "deepseek-v4.1-flash",
		"reasoning": {"effort": "high"},
		"input": [
			{"type": "reasoning", "id": "rs_1", "summary": [{"type": "summary_text", "text": "first plan"}]},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "starting"}]},
			{"type": "function_call", "call_id": "call_1", "name": "exec_command", "arguments": "{\"cmd\":\"ls\"}"},
			{"type": "function_call_output", "call_id": "call_1", "output": "ok"},
			{"type": "function_call", "call_id": "call_2", "name": "write_stdin", "arguments": "{\"data\":\"x\"}"},
			{"type": "function_call_output", "call_id": "call_2", "output": "ok"},
			{"type": "reasoning", "id": "rs_2", "summary": [{"type": "summary_text", "text": "second plan"}]},
			{"type": "function_call", "call_id": "call_3", "name": "exec_command", "arguments": "{\"cmd\":\"pwd\"}"},
			{"type": "function_call_output", "call_id": "call_3", "output": "ok"},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "apply_patch is available"}]},
			{"type": "function_call", "call_id": "call_4", "name": "exec_command", "arguments": "{\"cmd\":\"cat\"}"},
			{"type": "function_call_output", "call_id": "call_4", "output": "ok"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4.1-flash", raw, true)

	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 10 {
		t.Fatalf("messages count = %d, want 10; output=%s", len(messages), out)
	}

	for i, msg := range messages {
		if msg.Get("role").String() == "assistant" && msg.Get("tool_calls").Exists() && len(msg.Get("tool_calls").Array()) > 0 {
			rc := msg.Get("reasoning_content").String()
			if rc == "" {
				t.Fatalf("messages[%d] with tool_calls is missing reasoning_content; message=%s", i, msg.Raw)
			}
		}
	}

	// Local conversion emits one assistant(tool_calls) message per flushed batch
	// (no merge into the preceding text assistant message), so the tool-call
	// assistant for call_2 carries the latest reasoning observed so far.
	if got := messages[3].Get("tool_calls.0.id").String(); got != "call_2" {
		t.Fatalf("messages.3 tool call id = %q, want call_2; output=%s", got, out)
	}
	if got := messages[3].Get("reasoning_content").String(); got != "first plan" {
		t.Fatalf("messages.3 reasoning_content = %q, want %q; output=%s", got, "first plan", out)
	}
	if got := messages[8].Get("tool_calls.0.id").String(); got != "call_4" {
		t.Fatalf("messages.8 tool call id = %q, want call_4; output=%s", got, out)
	}
	if got := messages[8].Get("reasoning_content").String(); got != "second plan" {
		t.Fatalf("messages.8 reasoning_content = %q, want %q; output=%s", got, "second plan", out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_FallsBackToPlaceholderWhenNoPriorReasoning(t *testing.T) {
	raw := []byte(`{
		"model": "deepseek-v4.1-flash",
		"reasoning": {"effort": "high"},
		"input": [
			{"type": "function_call", "call_id": "call_1", "name": "exec_command", "arguments": "{\"cmd\":\"ls\"}"},
			{"type": "function_call_output", "call_id": "call_1", "output": "ok"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4.1-flash", raw, false)

	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 2 {
		t.Fatalf("messages count = %d, want 2; output=%s", len(messages), out)
	}
	if got := messages[0].Get("reasoning_content").String(); got != "[reasoning unavailable]" {
		t.Fatalf("messages.0 reasoning_content = %q, want %q; output=%s", got, "[reasoning unavailable]", out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_EffortNoneDoesNotInjectReasoning(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-4o",
		"reasoning": {"effort": "none"},
		"input": [
			{"type": "function_call", "call_id": "call_1", "name": "exec_command", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "call_1", "output": "ok"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-4o", raw, false)

	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 2 {
		t.Fatalf("messages count = %d, want 2; output=%s", len(messages), out)
	}
	if messages[0].Get("reasoning_content").Exists() {
		t.Fatalf("messages.0 should not have reasoning_content when effort is none; output=%s", out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesReasoningOnCustomToolCallTurns(t *testing.T) {
	raw := []byte(`{
		"model": "deepseek-v4.1-flash",
		"reasoning": {"effort": "high"},
		"input": [
			{"type": "reasoning", "id": "rs_1", "summary": [{"type": "summary_text", "text": "custom plan"}]},
			{"type": "custom_tool_call", "call_id": "cust_1", "name": "do_work", "input": "step1"},
			{"type": "custom_tool_call_output", "call_id": "cust_1", "output": "done"},
			{"type": "custom_tool_call", "call_id": "cust_2", "name": "do_work", "input": "step2"},
			{"type": "custom_tool_call_output", "call_id": "cust_2", "output": "done"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4.1-flash", raw, false)

	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 4 {
		t.Fatalf("messages count = %d, want 4; output=%s", len(messages), out)
	}
	if got := messages[0].Get("reasoning_content").String(); got != "custom plan" {
		t.Fatalf("messages.0 reasoning_content = %q, want %q; output=%s", got, "custom plan", out)
	}
	if got := messages[2].Get("reasoning_content").String(); got != "custom plan" {
		t.Fatalf("messages.2 reasoning_content = %q, want %q; output=%s", got, "custom plan", out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_ResetsReasoningAcrossUserMessageBoundary(t *testing.T) {
	raw := []byte(`{
		"model": "deepseek-v4.1-flash",
		"reasoning": {"effort": "high"},
		"input": [
			{"type": "reasoning", "id": "rs_1", "summary": [{"type": "summary_text", "text": "turn 1 plan"}]},
			{"type": "function_call", "call_id": "call_1", "name": "exec_command", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "call_1", "output": "ok"},
			{"type": "message", "role": "user", "content": "now do step 2"},
			{"type": "function_call", "call_id": "call_2", "name": "exec_command", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "call_2", "output": "ok"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4.1-flash", raw, false)

	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 5 {
		t.Fatalf("messages count = %d, want 5; output=%s", len(messages), out)
	}
	if got := messages[0].Get("reasoning_content").String(); got != "turn 1 plan" {
		t.Fatalf("messages.0 reasoning_content = %q, want %q; output=%s", got, "turn 1 plan", out)
	}
	if got := messages[3].Get("reasoning_content").String(); got != "[reasoning unavailable]" {
		t.Fatalf("messages.3 reasoning_content = %q, want %q; output=%s", got, "[reasoning unavailable]", out)
	}
}
