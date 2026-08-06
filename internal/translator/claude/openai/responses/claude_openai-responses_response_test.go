package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func parseClaudeResponsesSSEEvent(t *testing.T, chunk []byte) (string, gjson.Result) {
	t.Helper()

	var event string
	var data string
	for _, line := range strings.Split(string(chunk), "\n") {
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
		}
	}
	if data == "" {
		t.Fatalf("SSE chunk has no data line: %s", string(chunk))
	}

	return event, gjson.Parse(data)
}

func TestConvertClaudeResponseToOpenAIResponses_ThinkingIncludesSignature(t *testing.T) {
	signature := "claude_sig_123"
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"internal "}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"reasoning"}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"` + signature + `"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", nil, nil, chunk, &param)...)
	}

	var reasoningDone gjson.Result
	var completed gjson.Result
	for _, output := range outputs {
		event, data := parseClaudeResponsesSSEEvent(t, output)
		switch event {
		case "response.output_item.done":
			if data.Get("item.type").String() == "reasoning" {
				reasoningDone = data
			}
		case "response.completed":
			completed = data
		}
	}

	if !reasoningDone.Exists() {
		t.Fatal("expected reasoning output_item.done event")
	}
	if got := reasoningDone.Get("item.encrypted_content").String(); got != signature {
		t.Fatalf("reasoning encrypted_content = %q, want %q", got, signature)
	}
	if got := reasoningDone.Get("item.summary.0.text").String(); got != "internal reasoning" {
		t.Fatalf("reasoning summary text = %q", got)
	}
	if got := completed.Get("response.output.0.encrypted_content").String(); got != signature {
		t.Fatalf("completed reasoning encrypted_content = %q, want %q", got, signature)
	}
	if got := completed.Get("response.output.0.summary.0.text").String(); got != "internal reasoning" {
		t.Fatalf("completed reasoning summary text = %q", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponsesNonStream_ThinkingIncludesSignature(t *testing.T) {
	signature := "claude_sig_nonstream"
	raw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_nonstream","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"nonstream reasoning"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"` + signature + `"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))

	out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", nil, nil, raw, nil)
	root := gjson.ParseBytes(out)

	if got := root.Get("output.0.encrypted_content").String(); got != signature {
		t.Fatalf("non-stream reasoning encrypted_content = %q, want %q", got, signature)
	}
	if got := root.Get("output.0.summary.0.text").String(); got != "nonstream reasoning" {
		t.Fatalf("non-stream reasoning summary text = %q", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_StringifiedTextDeltaContentBlocksExtractText(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"[{\"type\":\"text\",\"text\":\"hello\"},{\"type\":\"output_text\",\"text\":\" world\"}]"}}`),
	}

	var param any
	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", nil, nil, chunk, &param)...)
	}

	var text string
	for _, output := range outputs {
		event, data := parseClaudeResponsesSSEEvent(t, output)
		if event == "response.output_text.delta" {
			text = data.Get("delta").String()
		}
	}
	if text != "hello world" {
		t.Fatalf("delta = %q, want hello world; outputs=%q", text, outputs)
	}
	if strings.Contains(text, `"type":"text"`) {
		t.Fatalf("content blocks were serialized into text: %q", text)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_RedactedThinkingBecomesMarkedReasoningItem(t *testing.T) {
	const data = "EroBCkYIBRgCKkA"
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"` + data + `"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"done"}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", nil, nil, chunk, &param)...)
	}

	want := ClaudeResponsesRedactedThinkingPrefix + data
	var reasoningDone, completed gjson.Result
	for _, output := range outputs {
		event, parsed := parseClaudeResponsesSSEEvent(t, output)
		switch event {
		case "response.output_item.done":
			if parsed.Get("item.type").String() == "reasoning" {
				reasoningDone = parsed
			}
		case "response.completed":
			completed = parsed
		}
	}

	if !reasoningDone.Exists() {
		t.Fatal("expected reasoning output_item.done event for redacted_thinking")
	}
	if got := reasoningDone.Get("item.encrypted_content").String(); got != want {
		t.Fatalf("reasoning encrypted_content = %q, want %q", got, want)
	}
	if got := completed.Get("response.output.0.encrypted_content").String(); got != want {
		t.Fatalf("completed reasoning encrypted_content = %q, want %q", got, want)
	}
	if got := completed.Get("response.output.1.type").String(); got != "message" {
		t.Fatalf("completed output[1].type = %q, want message", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponsesNonStream_RedactedThinkingBecomesMarkedReasoningItem(t *testing.T) {
	const data = "EroBCkYIBRgCKkA"
	raw := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"` + data + `"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}, "\n")

	out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", nil, nil, []byte(raw), nil)
	parsed := gjson.ParseBytes(out)
	if got := parsed.Get("output.0.type").String(); got != "reasoning" {
		t.Fatalf("output.0.type = %q, want reasoning; body=%s", got, out)
	}
	want := ClaudeResponsesRedactedThinkingPrefix + data
	if got := parsed.Get("output.0.encrypted_content").String(); got != want {
		t.Fatalf("output.0.encrypted_content = %q, want %q", got, want)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_RestoresAdditionalCustomToolCall(t *testing.T) {
	originalRequest := []byte(`{
		"model":"gpt-test",
		"input":[{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"exec"}]}]
	}`)
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_custom","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_custom","name":"exec","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"pwd\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var added, inputDone, done, completed gjson.Result
	functionEvents := 0
	for _, chunk := range chunks {
		for _, output := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", originalRequest, nil, chunk, &param) {
			event, data := parseClaudeResponsesSSEEvent(t, output)
			switch event {
			case "response.output_item.added":
				if data.Get("item.type").String() == "custom_tool_call" {
					added = data
				}
			case "response.custom_tool_call_input.done":
				inputDone = data
			case "response.output_item.done":
				if data.Get("item.type").String() == "custom_tool_call" {
					done = data
				}
			case "response.function_call_arguments.delta", "response.function_call_arguments.done":
				functionEvents++
			case "response.completed":
				completed = data
			}
		}
	}

	if !added.Exists() || !inputDone.Exists() || !done.Exists() || !completed.Exists() {
		t.Fatalf("missing custom tool lifecycle events: added=%v input_done=%v done=%v completed=%v", added.Exists(), inputDone.Exists(), done.Exists(), completed.Exists())
	}
	if functionEvents != 0 {
		t.Fatalf("function call events = %d, want 0", functionEvents)
	}
	if got := added.Get("item.name").String(); got != "exec" {
		t.Fatalf("added name = %q, want exec", got)
	}
	if got := inputDone.Get("input").String(); got != "pwd" {
		t.Fatalf("custom input.done input = %q, want pwd", got)
	}
	if got := done.Get("item.input").String(); got != "pwd" {
		t.Fatalf("done input = %q, want pwd", got)
	}
	if got := completed.Get("response.output.0.type").String(); got != "custom_tool_call" {
		t.Fatalf("completed output type = %q, want custom_tool_call", got)
	}
	if got := completed.Get("response.output.0.input").String(); got != "pwd" {
		t.Fatalf("completed input = %q, want pwd", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_DirectCustomWinsNamespaceCollision(t *testing.T) {
	originalRequest := []byte(`{
		"model":"gpt-test",
		"tools":[
			{"type":"namespace","name":"n","tools":[{"type":"function","name":"x"}]},
			{"type":"custom","name":"n__x"}
		]
	}`)
	streamChunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_collision","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_collision","name":"n__x","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"pwd\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var streamCompleted gjson.Result
	for _, chunk := range streamChunks {
		for _, output := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", originalRequest, nil, chunk, &param) {
			event, data := parseClaudeResponsesSSEEvent(t, output)
			if event == "response.completed" {
				streamCompleted = data
			}
		}
	}
	if got := streamCompleted.Get("response.output.0.type").String(); got != "custom_tool_call" {
		t.Fatalf("stream output type = %q, want custom_tool_call", got)
	}
	if got := streamCompleted.Get("response.output.0.input").String(); got != "pwd" {
		t.Fatalf("stream output input = %q, want pwd", got)
	}

	nonStreamRaw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_collision_nonstream","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_collision_nonstream","name":"n__x","input":{}}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"pwd\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))
	nonStream := gjson.ParseBytes(ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", originalRequest, nil, nonStreamRaw, nil))
	if got := nonStream.Get("output.0.type").String(); got != "custom_tool_call" {
		t.Fatalf("non-stream output type = %q, want custom_tool_call", got)
	}
	if got := nonStream.Get("output.0.input").String(); got != "pwd" {
		t.Fatalf("non-stream output input = %q, want pwd", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponsesNonStream_RestoresAdditionalCustomToolCall(t *testing.T) {
	originalRequest := []byte(`{
		"model":"gpt-test",
		"input":[{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"exec"}]}]
	}`)
	raw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_custom_nonstream","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_custom_nonstream","name":"exec","input":{}}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"pwd\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))

	root := gjson.ParseBytes(ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", originalRequest, nil, raw, nil))
	if got := root.Get("output.0.type").String(); got != "custom_tool_call" {
		t.Fatalf("non-stream output type = %q, want custom_tool_call; output=%s", got, root.Raw)
	}
	if got := root.Get("output.0.input").String(); got != "pwd" {
		t.Fatalf("non-stream input = %q, want pwd", got)
	}
	if got := root.Get("output.0.call_id").String(); got != "call_custom_nonstream" {
		t.Fatalf("non-stream call_id = %q, want call_custom_nonstream", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_CustomToolEmptyInputMatchesNonStream(t *testing.T) {
	originalRequest := []byte(`{
		"model":"gpt-test",
		"tools":[{"type":"custom","name":"exec"}]
	}`)
	streamChunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_custom_empty","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_custom_empty","name":"exec","input":{}}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var streamCompleted gjson.Result
	for _, chunk := range streamChunks {
		for _, output := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", originalRequest, nil, chunk, &param) {
			event, data := parseClaudeResponsesSSEEvent(t, output)
			if event == "response.completed" {
				streamCompleted = data
			}
		}
	}
	if got := streamCompleted.Get("response.output.0.input").String(); got != "" {
		t.Fatalf("stream empty custom input = %q, want empty string", got)
	}

	raw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_custom_empty_nonstream","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_custom_empty","name":"exec","input":{}}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))
	nonStream := gjson.ParseBytes(ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", originalRequest, nil, raw, nil))
	if got := nonStream.Get("output.0.input").String(); got != "" {
		t.Fatalf("non-stream empty custom input = %q, want empty string", got)
	}
}
