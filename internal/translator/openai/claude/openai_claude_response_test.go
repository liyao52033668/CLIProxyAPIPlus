package claude

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

type sseEvent struct {
	Type    string
	Payload string
}

func runStream(t *testing.T, originalReq string, chunks ...string) []sseEvent {
	t.Helper()

	var paramAny any
	var emitted [][]byte
	for _, chunk := range chunks {
		emitted = append(emitted, ConvertOpenAIResponseToClaude(
			context.Background(),
			"",
			[]byte(originalReq),
			nil,
			[]byte("data: "+chunk),
			&paramAny,
		)...)
	}
	emitted = append(emitted, ConvertOpenAIResponseToClaude(
		context.Background(),
		"",
		[]byte(originalReq),
		nil,
		[]byte("data: [DONE]"),
		&paramAny,
	)...)

	var events []sseEvent
	for _, raw := range emitted {
		s := string(raw)
		if !strings.HasPrefix(s, "event: ") {
			continue
		}
		nl := strings.Index(s, "\n")
		if nl < 0 {
			continue
		}
		typ := strings.TrimPrefix(s[:nl], "event: ")
		rest := s[nl+1:]
		if !strings.HasPrefix(rest, "data: ") {
			continue
		}
		payload := strings.TrimRight(strings.TrimPrefix(rest, "data: "), "\n")
		events = append(events, sseEvent{Type: typ, Payload: payload})
	}
	return events
}

func countByType(events []sseEvent, typ string) int {
	n := 0
	for _, e := range events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func toolUseStarts(events []sseEvent) []sseEvent {
	var out []sseEvent
	for _, e := range events {
		if e.Type != "content_block_start" {
			continue
		}
		if gjson.Get(e.Payload, "content_block.type").String() == "tool_use" {
			out = append(out, e)
		}
	}
	return out
}

func blockIndices(events []sseEvent) []int64 {
	var idx []int64
	for _, e := range events {
		if e.Type == "content_block_start" {
			idx = append(idx, gjson.Get(e.Payload, "index").Int())
		}
	}
	return idx
}

func lastStopReason(events []sseEvent) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == "message_delta" {
			return gjson.Get(events[i].Payload, "delta.stop_reason").String()
		}
	}
	return ""
}

const streamReq = `{"stream":true}`

// TestConvertOpenAIResponseToClaude_StreamReasoningContentEmitsThinkingBlocks
// covers the cursor stream shape after the executor stopped wrapping reasoning
// in literal <think> tags: reasoning arrives as a reasoning_content field and
// must surface as thinking_delta blocks without any tag text leaking into the
// user-visible text, and the thinking block must be closed on [DONE].
func TestConvertOpenAIResponseToClaude_StreamReasoningContentEmitsThinkingBlocks(t *testing.T) {
	events := runStream(t,
		streamReq,
		`{"id":"chatcmpl_1","model":"cursor-model","created":1,"choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"let me think"},"finish_reason":null}]}`,
		`{"id":"chatcmpl_1","model":"cursor-model","created":1,"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
		`{"id":"chatcmpl_1","model":"cursor-model","created":1,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)

	var thinkingText, textText strings.Builder
	for _, e := range events {
		switch e.Type {
		case "content_block_delta":
			switch gjson.Get(e.Payload, "delta.type").String() {
			case "thinking_delta":
				thinkingText.WriteString(gjson.Get(e.Payload, "delta.thinking").String())
			case "text_delta":
				textText.WriteString(gjson.Get(e.Payload, "delta.text").String())
			}
		}
	}

	if thinkingText.String() != "let me think" {
		t.Fatalf("thinking text = %q", thinkingText.String())
	}
	if textText.String() != "hi" {
		t.Fatalf("text = %q", textText.String())
	}
	for _, leaked := range []string{"<think>", "</think>"} {
		for _, e := range events {
			if strings.Contains(e.Payload, leaked) {
				t.Fatalf("tag %q leaked into %s: %s", leaked, e.Type, e.Payload)
			}
		}
	}
	// Both the thinking block and the text block must be closed by [DONE].
	if got := countByType(events, "content_block_stop"); got < 2 {
		t.Fatalf("content_block_stop count = %d, want >= 2 (thinking + text)", got)
	}
}

func TestConvertOpenAIResponseToClaude_StreamIgnoresNullToolNameDelta(t *testing.T) {
	originalRequest := []byte(streamReq)
	var param any

	firstChunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"test-model",
		originalRequest,
		nil,
		[]byte(`data: {"id":"chatcmpl_1","model":"test-model","created":1,"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":""}}]},"finish_reason":null}]}`),
		&param,
	)
	firstOutput := bytes.Join(firstChunks, nil)
	if !bytes.Contains(firstOutput, []byte(`"name":"read_file"`)) {
		t.Fatalf("expected first chunk to start read_file tool block, got %s", string(firstOutput))
	}

	secondChunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"test-model",
		originalRequest,
		nil,
		[]byte(`data: {"id":"chatcmpl_1","model":"test-model","created":1,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":null,"arguments":"{\"path\":\"/tmp/a\"}"}}]},"finish_reason":null}]}`),
		&param,
	)
	secondOutput := bytes.Join(secondChunks, nil)
	if bytes.Contains(secondOutput, []byte(`content_block_start`)) {
		t.Fatalf("did not expect null tool name delta to start a new content block, got %s", string(secondOutput))
	}
	if bytes.Contains(secondOutput, []byte(`"name":""`)) {
		t.Fatalf("did not expect null tool name delta to emit an empty tool name, got %s", string(secondOutput))
	}
}

func TestConvertOpenAIResponseToClaude_StreamContentBlockArrayUsesText(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":[{"text":"hello","type":"text"}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)

	for _, e := range events {
		if e.Type != "content_block_delta" {
			continue
		}
		if got := gjson.Get(e.Payload, "delta.text").String(); got != "hello" {
			t.Fatalf("delta text = %q, want hello; payload=%s", got, e.Payload)
		}
		return
	}
	t.Fatalf("expected content_block_delta event, got %+v", events)
}

func TestConvertOpenAIResponseToClaude_StreamNestedStringifiedContentBlockArrayUsesText(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":[{"text":"[{\"text\":\"hello\",\"type\":\"text\"}]","type":"text"}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)

	for _, e := range events {
		if e.Type != "content_block_delta" {
			continue
		}
		text := gjson.Get(e.Payload, "delta.text").String()
		if text != "hello" {
			t.Fatalf("delta text = %q, want hello; payload=%s", text, e.Payload)
		}
		if strings.Contains(text, `"type":"text"`) {
			t.Fatalf("content blocks were serialized into text: %q", text)
		}
		return
	}
	t.Fatalf("expected content_block_delta event, got %+v", events)
}

func TestConvertOpenAIResponseToClaudeNonStream_ContentBlockArrayUsesText(t *testing.T) {
	out := ConvertOpenAIResponseToClaudeNonStream(
		context.Background(),
		"",
		[]byte(`{"stream":false}`),
		nil,
		[]byte(`{"id":"chatcmpl_1","model":"m","choices":[{"message":{"role":"assistant","content":[{"type":"text","text":"hello"},{"type":"output_text","text":" world"},{"type":"image_url","image_url":{"url":"ignored"}}]},"finish_reason":"stop"}]}`),
		nil,
	)

	text := gjson.GetBytes(out, "content.0.text").String()
	if text != "hello world" {
		t.Fatalf("content text = %q, want hello world; output=%s", text, string(out))
	}
	if strings.Contains(text, `"type":"text"`) {
		t.Fatalf("content blocks were serialized into text: %q", text)
	}
}

func TestConvertOpenAIResponseToClaudeNonStream_StringifiedContentBlockArrayUsesText(t *testing.T) {
	out := ConvertOpenAIResponseToClaudeNonStream(
		context.Background(),
		"",
		[]byte(`{"stream":false}`),
		nil,
		[]byte(`{"id":"chatcmpl_1","model":"m","choices":[{"message":{"role":"assistant","content":"[{\"type\":\"text\",\"text\":\"hello\"},{\"type\":\"output_text\",\"text\":\" world\"}]"},"finish_reason":"stop"}]}`),
		nil,
	)

	text := gjson.GetBytes(out, "content.0.text").String()
	if text != "hello world" {
		t.Fatalf("content text = %q, want hello world; output=%s", text, string(out))
	}
	if strings.Contains(text, `"type":"text"`) {
		t.Fatalf("content blocks were serialized into text: %q", text)
	}
}

func TestConvertOpenAIResponseToClaudeNonStream_NestedStringifiedContentBlockArrayUsesText(t *testing.T) {
	out := ConvertOpenAIResponseToClaudeNonStream(
		context.Background(),
		"",
		[]byte(`{"stream":false}`),
		nil,
		[]byte(`{"id":"chatcmpl_1","model":"m","choices":[{"message":{"role":"assistant","content":[{"type":"text","text":"[{\"text\":\"hello\",\"type\":\"text\"}]"}]},"finish_reason":"stop"}]}`),
		nil,
	)

	text := gjson.GetBytes(out, "content.0.text").String()
	if text != "hello" {
		t.Fatalf("content text = %q, want hello; output=%s", text, string(out))
	}
	if strings.Contains(text, `"type":"text"`) {
		t.Fatalf("content blocks were serialized into text: %q", text)
	}
}

func TestConvertOpenAIResponseToClaudeNonStreamFallback_StringifiedContentBlockArrayUsesText(t *testing.T) {
	var param any
	chunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"",
		[]byte(`{"stream":false}`),
		nil,
		[]byte(`data: {"id":"chatcmpl_1","model":"m","choices":[{"message":{"role":"assistant","content":"[{\"type\":\"text\",\"text\":\"hello\"},{\"type\":\"output_text\",\"text\":\" world\"}]"},"finish_reason":"stop"}]}`),
		&param,
	)
	out := bytes.Join(chunks, nil)

	text := gjson.GetBytes(out, "content.0.text").String()
	if text != "hello world" {
		t.Fatalf("content text = %q, want hello world; output=%s", text, string(out))
	}
	if strings.Contains(text, `"type":"text"`) {
		t.Fatalf("content blocks were serialized into text: %q", text)
	}
}

func TestStreamingTool_EmptyNameThroughout(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","function":{"name":"","arguments":""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":"{\"x\":1}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("expected one tool_use content_block_start with synthetic name, got %d (events=%+v)", len(starts), events)
	}
	if name := gjson.Get(starts[0].Payload, "content_block.name").String(); name != "tool_0" {
		t.Fatalf("expected synthetic name tool_0, got %q", name)
	}
	if got := countByType(events, "content_block_delta"); got != 1 {
		t.Fatalf("expected one content_block_delta, got %d", got)
	}
	if got := countByType(events, "content_block_stop"); got != 1 {
		t.Fatalf("expected one content_block_stop, got %d", got)
	}
	if got := lastStopReason(events); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use", got)
	}
}

func TestStreamingTool_EmptyNameAtDone(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","function":{"name":"","arguments":"{\"x\":1}"}}]}}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("expected one tool_use content_block_start at [DONE], got %d (events=%+v)", len(starts), events)
	}
	if name := gjson.Get(starts[0].Payload, "content_block.name").String(); name != "tool_0" {
		t.Fatalf("expected synthetic name tool_0, got %q", name)
	}
	if got := countByType(events, "content_block_delta"); got != 1 {
		t.Fatalf("expected one content_block_delta, got %d", got)
	}
	if got := countByType(events, "content_block_stop"); got != 1 {
		t.Fatalf("expected one content_block_stop, got %d", got)
	}
	if got := lastStopReason(events); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use", got)
	}
}

func TestStreamingTool_NullName(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","function":{"name":null,"arguments":""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	if got := len(toolUseStarts(events)); got != 0 {
		t.Fatalf("null name must not produce a tool_use start; got %d", got)
	}
	if got := countByType(events, "content_block_stop"); got != 0 {
		t.Fatalf("null name must not produce content_block_stop; got %d", got)
	}
	if got := lastStopReason(events); got == "tool_use" {
		t.Fatalf("stop_reason must not be tool_use when no tool block was emitted")
	}
}

func TestStreamingTool_NonStringName(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","function":{"name":123,"arguments":""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	if got := len(toolUseStarts(events)); got != 0 {
		t.Fatalf("non-string name must not produce a tool_use start; got %d", got)
	}
}

func TestStreamingTool_RepeatedName(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","function":{"name":"do_it","arguments":""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"do_it","arguments":"{\"x\""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"do_it","arguments":":1}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("expected exactly one tool_use start, got %d", len(starts))
	}
	if name := gjson.Get(starts[0].Payload, "content_block.name").String(); name != "do_it" {
		t.Fatalf("announced tool name = %q, want %q", name, "do_it")
	}
	if got := countByType(events, "content_block_stop"); got != 1 {
		t.Fatalf("expected exactly one content_block_stop, got %d", got)
	}
}

func TestStreamingTool_MixedSuppressedAndValid(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[
			{"index":0,"id":"call_skip","function":{"name":"","arguments":""}},
			{"index":1,"id":"call_real","function":{"name":"do_it","arguments":""}}
		]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[
			{"index":1,"function":{"arguments":"{}"}}
		]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("expected exactly one tool_use start, got %d", len(starts))
	}
	if got := countByType(events, "content_block_stop"); got != 1 {
		t.Fatalf("expected exactly one content_block_stop, got %d", got)
	}

	indices := blockIndices(events)
	if len(indices) == 0 || indices[0] != 0 {
		t.Fatalf("first content_block_start index must be 0, got %v", indices)
	}
}

func TestStreamingTool_EmptyIDDeferStart(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"","function":{"name":"do_it","arguments":""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_real","function":{"arguments":"{}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("expected exactly one tool_use start once id arrived, got %d", len(starts))
	}
	if id := gjson.Get(starts[0].Payload, "content_block.id").String(); id != "call_real" {
		t.Fatalf("announced tool id = %q, want %q", id, "call_real")
	}
}

func TestStreamingTool_IDInDeltaWithoutFunction(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"function":{"name":"do_it"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_real"}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("expected exactly one tool_use start when id arrives in a function-less delta, got %d", len(starts))
	}
	if id := gjson.Get(starts[0].Payload, "content_block.id").String(); id != "call_real" {
		t.Fatalf("announced tool id = %q, want %q", id, "call_real")
	}
	if name := gjson.Get(starts[0].Payload, "content_block.name").String(); name != "do_it" {
		t.Fatalf("announced tool name = %q, want %q", name, "do_it")
	}
	if got := countByType(events, "content_block_stop"); got != 1 {
		t.Fatalf("expected exactly one content_block_stop, got %d", got)
	}
}

func TestStreamingTool_StopReasonWithEmittedTool(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","function":{"name":"do_it","arguments":"{}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
	)
	if got := lastStopReason(events); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want %q", got, "tool_use")
	}
}

func TestStreamingTool_StopReasonWhenIDNeverArrives(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"function":{"name":"do_it","arguments":""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("expected one belated tool_use start with synthetic id, got %d", len(starts))
	}
	id := gjson.Get(starts[0].Payload, "content_block.id").String()
	if !strings.HasPrefix(id, "toolu_") {
		t.Fatalf("synthetic id should match toolu_<nanos>_<n>, got %q", id)
	}
	if name := gjson.Get(starts[0].Payload, "content_block.name").String(); name != "do_it" {
		t.Fatalf("announced tool name = %q, want %q", name, "do_it")
	}
	if got := lastStopReason(events); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want %q", got, "tool_use")
	}
}

func TestStreamingTool_BelatedStartsUseOpenAIToolIndexOrder(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[
			{"index":2,"function":{"name":"third_tool","arguments":"{}"}},
			{"index":0,"function":{"name":"first_tool","arguments":"{}"}},
			{"index":1,"function":{"name":"second_tool","arguments":"{}"}}
		]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 3 {
		t.Fatalf("expected three belated tool_use starts, got %d", len(starts))
	}

	wantNames := []string{"first_tool", "second_tool", "third_tool"}
	for i, wantName := range wantNames {
		if name := gjson.Get(starts[i].Payload, "content_block.name").String(); name != wantName {
			t.Fatalf("tool_use start %d name = %q, want %q (starts=%+v)", i, name, wantName, starts)
		}
		if blockIndex := gjson.Get(starts[i].Payload, "index").Int(); blockIndex != int64(i) {
			t.Fatalf("tool_use start %d block index = %d, want %d", i, blockIndex, i)
		}
	}
}

func TestStreamingTool_LateIDAfterFinalization(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"function":{"name":"do_it"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_late"}]}}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("expected one belated tool_use start, got %d", len(starts))
	}

	var sawMessageStop bool
	for _, e := range events {
		if e.Type == "message_stop" {
			sawMessageStop = true
			continue
		}
		if sawMessageStop {
			switch e.Type {
			case "content_block_start", "content_block_delta", "content_block_stop":
				t.Fatalf("event %q emitted after message_stop (events=%+v)", e.Type, events)
			}
		}
	}
}

func TestStreamingTool_StopReasonMixedSuppressedAndValid(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[
			{"index":0,"id":"call_skip","function":{"name":"","arguments":""}},
			{"index":1,"id":"call_real","function":{"name":"do_it","arguments":"{}"}}
		]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	if got := lastStopReason(events); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want %q", got, "tool_use")
	}
}

func assertSequentialContentBlocks(t *testing.T, events []sseEvent) {
	t.Helper()
	activeBlockIndex := int64(-1)
	for _, e := range events {
		switch e.Type {
		case "content_block_start":
			if activeBlockIndex != -1 {
				t.Fatalf("content_block_start emitted for index %d while block %d is still open (events=%+v)",
					gjson.Get(e.Payload, "index").Int(), activeBlockIndex, events)
			}
			activeBlockIndex = gjson.Get(e.Payload, "index").Int()
		case "content_block_delta":
			idx := gjson.Get(e.Payload, "index").Int()
			if activeBlockIndex == -1 {
				t.Fatalf("content_block_delta emitted for index %d but no block is open (events=%+v)", idx, events)
			}
			if idx != activeBlockIndex {
				t.Fatalf("content_block_delta emitted for index %d but active block is %d (events=%+v)", idx, activeBlockIndex, events)
			}
		case "content_block_stop":
			idx := gjson.Get(e.Payload, "index").Int()
			if activeBlockIndex == -1 {
				t.Fatalf("content_block_stop emitted for index %d but no block is open (events=%+v)", idx, events)
			}
			if idx != activeBlockIndex {
				t.Fatalf("content_block_stop emitted for index %d but active block is %d (events=%+v)", idx, activeBlockIndex, events)
			}
			activeBlockIndex = -1
		}
	}
	if activeBlockIndex != -1 {
		t.Fatalf("stream ended with unclosed block %d (events=%+v)", activeBlockIndex, events)
	}
}

func TestStreaming_InterleavedContentAndToolUse_StrictSequentialBlocks(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Bash","arguments":"{\"command\":"}}]},"finish_reason":null}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"\n"},"finish_reason":null}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\"}"}}]},"finish_reason":null}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	assertSequentialContentBlocks(t, events)

	// Ensure tool call arguments were preserved and complete
	var toolDeltas []sseEvent
	for _, e := range events {
		if e.Type == "content_block_delta" && gjson.Get(e.Payload, "delta.type").String() == "input_json_delta" {
			toolDeltas = append(toolDeltas, e)
		}
	}
	if len(toolDeltas) != 1 {
		t.Fatalf("expected 1 tool input_json_delta, got %d", len(toolDeltas))
	}
	partialJSON := gjson.Get(toolDeltas[0].Payload, "delta.partial_json").String()
	if gotCmd := gjson.Get(partialJSON, "command").String(); gotCmd != "ls" {
		t.Fatalf("expected command 'ls', got %q", gotCmd)
	}
}

func TestStreaming_ParallelToolCalls_StrictSequentialBlocks(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[
			{"index":0,"id":"call_1","type":"function","function":{"name":"Bash","arguments":"{\"command\":\"ls\"}"}},
			{"index":1,"id":"call_2","type":"function","function":{"name":"Read","arguments":"{\"path\":\"/tmp\"}"}}
		]},"finish_reason":"tool_calls"}]}`,
	)

	assertSequentialContentBlocks(t, events)

	starts := toolUseStarts(events)
	if len(starts) != 2 {
		t.Fatalf("expected 2 tool_use starts, got %d", len(starts))
	}
	if id := gjson.Get(starts[0].Payload, "content_block.id").String(); id != "call_1" {
		t.Fatalf("first tool id = %q, want %q", id, "call_1")
	}
	if name := gjson.Get(starts[0].Payload, "content_block.name").String(); name != "Bash" {
		t.Fatalf("first tool name = %q, want %q", name, "Bash")
	}
	if id := gjson.Get(starts[1].Payload, "content_block.id").String(); id != "call_2" {
		t.Fatalf("second tool id = %q, want %q", id, "call_2")
	}
	if name := gjson.Get(starts[1].Payload, "content_block.name").String(); name != "Read" {
		t.Fatalf("second tool name = %q, want %q", name, "Read")
	}

	var toolDeltas []sseEvent
	for _, e := range events {
		if e.Type == "content_block_delta" && gjson.Get(e.Payload, "delta.type").String() == "input_json_delta" {
			toolDeltas = append(toolDeltas, e)
		}
	}
	if len(toolDeltas) != 2 {
		t.Fatalf("expected 2 tool input_json_delta, got %d", len(toolDeltas))
	}
	if cmd := gjson.Get(gjson.Get(toolDeltas[0].Payload, "delta.partial_json").String(), "command").String(); cmd != "ls" {
		t.Fatalf("first tool cmd = %q, want 'ls'", cmd)
	}
	if path := gjson.Get(gjson.Get(toolDeltas[1].Payload, "delta.partial_json").String(), "path").String(); path != "/tmp" {
		t.Fatalf("second tool path = %q, want '/tmp'", path)
	}
}

func TestStreaming_InterleavedTextAndThinkingPreservesOrder(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Bash","arguments":"{\"command\":"}}]},"finish_reason":null}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"Note A: "},"finish_reason":null}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"running check"},"finish_reason":null}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"reasoning_content":"Thinking about safety"},"finish_reason":null}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"Note B: done"},"finish_reason":null}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"pwd\"}"}}]},"finish_reason":null}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	assertSequentialContentBlocks(t, events)

	// Block sequence should be:
	// 0: tool_use (Bash, command: "pwd")
	// 1: text ("Note A: running check")
	// 2: thinking ("Thinking about safety")
	// 3: text ("Note B: done")
	var blockStarts []string
	for _, e := range events {
		if e.Type == "content_block_start" {
			blockStarts = append(blockStarts, gjson.Get(e.Payload, "content_block.type").String())
		}
	}
	expectedStarts := []string{"tool_use", "text", "thinking", "text"}
	if len(blockStarts) != len(expectedStarts) {
		t.Fatalf("expected block starts %v, got %v", expectedStarts, blockStarts)
	}
	for i, want := range expectedStarts {
		if blockStarts[i] != want {
			t.Fatalf("block %d type = %q, want %q", i, blockStarts[i], want)
		}
	}

	// Verify text and thinking contents
	var textDeltas []string
	var thinkingDeltas []string
	var toolDelta string
	for _, e := range events {
		if e.Type == "content_block_delta" {
			dt := gjson.Get(e.Payload, "delta.type").String()
			switch dt {
			case "text_delta":
				textDeltas = append(textDeltas, gjson.Get(e.Payload, "delta.text").String())
			case "thinking_delta":
				thinkingDeltas = append(thinkingDeltas, gjson.Get(e.Payload, "delta.thinking").String())
			case "input_json_delta":
				toolDelta = gjson.Get(e.Payload, "delta.partial_json").String()
			}
		}
	}

	if gotCmd := gjson.Get(toolDelta, "command").String(); gotCmd != "pwd" {
		t.Fatalf("tool command = %q, want 'pwd'", gotCmd)
	}
	if len(textDeltas) != 2 || textDeltas[0] != "Note A: running check" || textDeltas[1] != "Note B: done" {
		t.Fatalf("unexpected text deltas: %v", textDeltas)
	}
	if len(thinkingDeltas) != 1 || thinkingDeltas[0] != "Thinking about safety" {
		t.Fatalf("unexpected thinking deltas: %v", thinkingDeltas)
	}
}
