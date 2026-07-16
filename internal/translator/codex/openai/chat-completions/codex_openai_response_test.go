package chat_completions

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertCodexResponseToOpenAI_IncompleteTerminal(t *testing.T) {
	ctx := context.Background()
	terminal := []byte(`{"type":"response.incomplete","response":{"id":"resp_1","model":"gpt-5.5","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)

	var param any
	streamOut := ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, append([]byte("data: "), terminal...), &param)
	if len(streamOut) != 1 {
		t.Fatalf("expected 1 streaming terminal chunk, got %d", len(streamOut))
	}
	if got := gjson.GetBytes(streamOut[0], "choices.0.finish_reason").String(); got != "length" {
		t.Fatalf("stream finish_reason = %q, want length; payload=%s", got, streamOut[0])
	}
	if got := gjson.GetBytes(streamOut[0], "choices.0.native_finish_reason").String(); got != "max_output_tokens" {
		t.Fatalf("stream native_finish_reason = %q, want max_output_tokens; payload=%s", got, streamOut[0])
	}

	var toolParam any
	_ = ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1","name":"lookup"}}`), &toolParam)
	toolStreamOut := ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, append([]byte("data: "), terminal...), &toolParam)
	if got := gjson.GetBytes(toolStreamOut[0], "choices.0.finish_reason").String(); got != "length" {
		t.Fatalf("tool stream finish_reason = %q, want length; payload=%s", got, toolStreamOut[0])
	}

	nonStreamOut := ConvertCodexResponseToOpenAINonStream(ctx, "gpt-5.5", nil, nil, terminal, nil)
	if got := gjson.GetBytes(nonStreamOut, "choices.0.finish_reason").String(); got != "length" {
		t.Fatalf("non-stream finish_reason = %q, want length; payload=%s", got, nonStreamOut)
	}
}

func TestConvertCodexResponseToOpenAI_StreamSetsModelFromResponseCreated(t *testing.T) {
	ctx := context.Background()
	var param any

	modelName := "gpt-5.3-codex"

	out := ConvertCodexResponseToOpenAI(ctx, modelName, nil, nil, []byte(`data: {"type":"response.created","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.3-codex"}}`), &param)
	if len(out) != 0 {
		t.Fatalf("expected no output for response.created, got %d chunks", len(out))
	}

	out = ConvertCodexResponseToOpenAI(ctx, modelName, nil, nil, []byte(`data: {"type":"response.output_text.delta","delta":"hello"}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	gotModel := gjson.GetBytes(out[0], "model").String()
	if gotModel != modelName {
		t.Fatalf("expected model %q, got %q", modelName, gotModel)
	}
}

func TestConvertCodexResponseToOpenAI_FirstChunkUsesRequestModelName(t *testing.T) {
	ctx := context.Background()
	var param any

	modelName := "gpt-5.3-codex"

	out := ConvertCodexResponseToOpenAI(ctx, modelName, nil, nil, []byte(`data: {"type":"response.output_text.delta","delta":"hello"}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	gotModel := gjson.GetBytes(out[0], "model").String()
	if gotModel != modelName {
		t.Fatalf("expected model %q, got %q", modelName, gotModel)
	}
}

func TestConvertCodexResponseToOpenAI_ToolCallChunkOmitsNullContentFields(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, []byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_123","name":"websearch"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	if gjson.GetBytes(out[0], "choices.0.delta.content").Exists() {
		t.Fatalf("expected content to be omitted, got %s", string(out[0]))
	}
	if gjson.GetBytes(out[0], "choices.0.delta.reasoning_content").Exists() {
		t.Fatalf("expected reasoning_content to be omitted, got %s", string(out[0]))
	}
	if !gjson.GetBytes(out[0], "choices.0.delta.tool_calls").Exists() {
		t.Fatalf("expected tool_calls to exist, got %s", string(out[0]))
	}
}

func TestConvertCodexResponseToOpenAI_ToolCallArgumentsDeltaOmitsNullContentFields(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, []byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_123","name":"websearch"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected tool call announcement chunk, got %d", len(out))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, []byte(`data: {"type":"response.function_call_arguments.delta","delta":"{\"query\":\"OpenAI\"}"}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	if gjson.GetBytes(out[0], "choices.0.delta.content").Exists() {
		t.Fatalf("expected content to be omitted, got %s", string(out[0]))
	}
	if gjson.GetBytes(out[0], "choices.0.delta.reasoning_content").Exists() {
		t.Fatalf("expected reasoning_content to be omitted, got %s", string(out[0]))
	}
	if !gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.function.arguments").Exists() {
		t.Fatalf("expected tool call arguments delta to exist, got %s", string(out[0]))
	}
}

func TestConvertCodexResponseToOpenAI_StreamPartialImageEmitsDeltaImages(t *testing.T) {
	ctx := context.Background()
	var param any

	chunk := []byte(`data: {"type":"response.image_generation_call.partial_image","item_id":"ig_123","output_format":"png","partial_image_b64":"aGVsbG8=","partial_image_index":0}`)

	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, chunk, &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	gotURL := gjson.GetBytes(out[0], "choices.0.delta.images.0.image_url.url").String()
	if gotURL != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("expected image url %q, got %q; chunk=%s", "data:image/png;base64,aGVsbG8=", gotURL, string(out[0]))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, chunk, &param)
	if len(out) != 0 {
		t.Fatalf("expected duplicate image chunk to be suppressed, got %d", len(out))
	}
}

func TestConvertCodexResponseToOpenAI_StreamImageGenerationCallDoneEmitsDeltaImages(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, []byte(`data: {"type":"response.image_generation_call.partial_image","item_id":"ig_123","output_format":"png","partial_image_b64":"aGVsbG8=","partial_image_index":0}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, []byte(`data: {"type":"response.output_item.done","item":{"id":"ig_123","type":"image_generation_call","output_format":"png","result":"aGVsbG8="}}`), &param)
	if len(out) != 0 {
		t.Fatalf("expected output_item.done to be suppressed when identical to last partial image, got %d", len(out))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, []byte(`data: {"type":"response.output_item.done","item":{"id":"ig_123","type":"image_generation_call","output_format":"jpeg","result":"Ymll"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	gotURL := gjson.GetBytes(out[0], "choices.0.delta.images.0.image_url.url").String()
	if gotURL != "data:image/jpeg;base64,Ymll" {
		t.Fatalf("expected image url %q, got %q; chunk=%s", "data:image/jpeg;base64,Ymll", gotURL, string(out[0]))
	}
}

func TestConvertCodexResponseToOpenAI_NonStreamImageGenerationCallAddsMessageImages(t *testing.T) {
	ctx := context.Background()

	raw := []byte(`{"type":"response.completed","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.4","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]},{"type":"image_generation_call","output_format":"png","result":"aGVsbG8="}]}}`)
	out := ConvertCodexResponseToOpenAINonStream(ctx, "gpt-5.4", nil, nil, raw, nil)

	gotURL := gjson.GetBytes(out, "choices.0.message.images.0.image_url.url").String()
	if gotURL != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("expected image url %q, got %q; chunk=%s", "data:image/png;base64,aGVsbG8=", gotURL, string(out))
	}
}

func TestConvertCodexResponseToOpenAI_StreamForwardsCacheWriteTokens(t *testing.T) {
	ctx := context.Background()
	var param any

	_ = ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, []byte(`data: {"type":"response.created","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.4"}}`), &param)

	chunk := []byte(`data: {"type":"response.completed","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.4","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":30,"cache_write_tokens":40},"output_tokens_details":{"reasoning_tokens":5}}}}`)
	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, chunk, &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}
	assertUsageMapping(t, out[0], 40, true)
}

func TestConvertCodexResponseToOpenAI_StreamOmitsMissingCacheWriteTokens(t *testing.T) {
	ctx := context.Background()
	var param any

	_ = ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, []byte(`data: {"type":"response.created","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.4"}}`), &param)

	chunk := []byte(`data: {"type":"response.completed","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.4","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":30},"output_tokens_details":{"reasoning_tokens":5}}}}`)
	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, chunk, &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}
	assertUsageMapping(t, out[0], 0, false)
}

func TestConvertCodexResponseToOpenAI_NonStreamForwardsCacheWriteTokens(t *testing.T) {
	ctx := context.Background()
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.4","status":"completed","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":30,"cache_write_tokens":40},"output_tokens_details":{"reasoning_tokens":5}},"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}}`)
	out := ConvertCodexResponseToOpenAINonStream(ctx, "gpt-5.4", nil, nil, raw, nil)
	assertUsageMapping(t, out, 40, true)
}

func TestConvertCodexResponseToOpenAI_StreamCustomToolCall(t *testing.T) {
	ctx := context.Background()
	var param any
	originalName := strings.Repeat("custom_tool_", 7) + "apply_patch"
	shortName := buildShortNameMap([]string{originalName})[originalName]
	originalRequest := []byte(`{"tools":[{"type":"custom","name":"` + originalName + `"}]}`)

	added := []byte(`data: {"type":"response.output_item.added","item":{"type":"custom_tool_call","call_id":"call_custom","name":"` + shortName + `"}}`)
	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.6-sol", originalRequest, nil, added, &param)
	if len(out) != 1 {
		t.Fatalf("expected custom tool announcement, got %d chunks", len(out))
	}
	if got := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.type").String(); got != "custom" {
		t.Fatalf("custom tool type = %q, want custom: %s", got, out[0])
	}
	if got := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.custom.name").String(); got != originalName {
		t.Fatalf("custom tool name = %q, want %q: %s", got, originalName, out[0])
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.6-sol", originalRequest, nil, []byte(`data: {"type":"response.custom_tool_call_input.delta","delta":"patch"}`), &param)
	if len(out) != 1 || gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.custom.input").String() != "patch" {
		t.Fatalf("expected custom input delta, got %q", out)
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.6-sol", originalRequest, nil, []byte(`data: {"type":"response.custom_tool_call_input.done","input":"patch"}`), &param)
	if len(out) != 0 {
		t.Fatalf("expected done event to be suppressed after delta, got %d chunks", len(out))
	}
	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.6-sol", originalRequest, nil, []byte(`data: {"type":"response.output_item.done","item":{"type":"custom_tool_call","call_id":"call_custom","name":"`+shortName+`","input":"patch"}}`), &param)
	if len(out) != 0 {
		t.Fatalf("expected announced custom item done to be suppressed, got %d chunks", len(out))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.6-sol", originalRequest, nil, []byte(`data: {"type":"response.completed","response":{"usage":{}}}`), &param)
	if len(out) != 1 || gjson.GetBytes(out[0], "choices.0.finish_reason").String() != "tool_calls" {
		t.Fatalf("expected tool_calls finish reason, got %q", out)
	}
}

func TestConvertCodexResponseToOpenAI_StreamCustomToolCallDoneFallback(t *testing.T) {
	ctx := context.Background()
	var param any
	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.6-sol", nil, nil, []byte(`data: {"type":"response.output_item.done","item":{"type":"custom_tool_call","call_id":"call_custom","name":"apply_patch","input":"patch"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected one fallback custom tool chunk, got %d", len(out))
	}
	if got := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.custom.input").String(); got != "patch" {
		t.Fatalf("custom fallback input = %q, want patch: %s", got, out[0])
	}
}

func TestConvertCodexResponseToOpenAI_NonStreamCustomToolCall(t *testing.T) {
	ctx := context.Background()
	originalName := strings.Repeat("custom_tool_", 7) + "apply_patch"
	shortName := buildShortNameMap([]string{originalName})[originalName]
	originalRequest := []byte(`{"tools":[{"type":"custom","name":"` + originalName + `"}]}`)
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_custom","model":"gpt-5.6-sol","status":"completed","output":[{"type":"custom_tool_call","call_id":"call_custom","name":"` + shortName + `","input":"patch"}]}}`)

	out := ConvertCodexResponseToOpenAINonStream(ctx, "gpt-5.6-sol", originalRequest, nil, raw, nil)
	if got := gjson.GetBytes(out, "choices.0.message.tool_calls.0.type").String(); got != "custom" {
		t.Fatalf("custom tool type = %q, want custom: %s", got, out)
	}
	if got := gjson.GetBytes(out, "choices.0.message.tool_calls.0.custom.name").String(); got != originalName {
		t.Fatalf("custom tool name = %q, want %q: %s", got, originalName, out)
	}
	if got := gjson.GetBytes(out, "choices.0.message.tool_calls.0.custom.input").String(); got != "patch" {
		t.Fatalf("custom tool input = %q, want patch: %s", got, out)
	}
	if got := gjson.GetBytes(out, "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("finish reason = %q, want tool_calls: %s", got, out)
	}
}

func assertUsageMapping(t *testing.T, payload []byte, wantCachedCreation int64, expectCachedCreation bool) {
	t.Helper()

	if got := gjson.GetBytes(payload, "usage.prompt_tokens").Int(); got != 100 {
		t.Fatalf("expected prompt_tokens=100, got %d; payload=%s", got, string(payload))
	}
	if got := gjson.GetBytes(payload, "usage.completion_tokens").Int(); got != 20 {
		t.Fatalf("expected completion_tokens=20, got %d; payload=%s", got, string(payload))
	}
	if got := gjson.GetBytes(payload, "usage.total_tokens").Int(); got != 120 {
		t.Fatalf("expected total_tokens=120, got %d; payload=%s", got, string(payload))
	}
	if got := gjson.GetBytes(payload, "usage.prompt_tokens_details.cached_tokens").Int(); got != 30 {
		t.Fatalf("expected cached_tokens=30, got %d; payload=%s", got, string(payload))
	}
	if got := gjson.GetBytes(payload, "usage.completion_tokens_details.reasoning_tokens").Int(); got != 5 {
		t.Fatalf("expected reasoning_tokens=5, got %d; payload=%s", got, string(payload))
	}

	gotCachedCreation := gjson.GetBytes(payload, "usage.prompt_tokens_details.cached_creation_tokens")
	if expectCachedCreation {
		if !gotCachedCreation.Exists() {
			t.Fatalf("expected cached_creation_tokens to exist, payload=%s", string(payload))
		}
		if gotCachedCreation.Int() != wantCachedCreation {
			t.Fatalf("expected cached_creation_tokens=%d, got %d; payload=%s", wantCachedCreation, gotCachedCreation.Int(), string(payload))
		}
		return
	}
	if gotCachedCreation.Exists() {
		t.Fatalf("expected cached_creation_tokens to be omitted, payload=%s", string(payload))
	}
}

func TestConvertCodexResponseToOpenAI_StreamStringifiedTextDeltaContentBlocksExtractText(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, []byte(`data: {"type":"response.output_text.delta","delta":"[{\"type\":\"text\",\"text\":\"hello\"},{\"type\":\"output_text\",\"text\":\" world\"}]"}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	text := gjson.GetBytes(out[0], "choices.0.delta.content").String()
	if text != "hello world" {
		t.Fatalf("content = %q, want hello world; chunk=%s", text, string(out[0]))
	}
	if strings.Contains(text, `"type":"text"`) {
		t.Fatalf("content blocks were serialized into text: %q", text)
	}
}

func TestConvertCodexResponseToOpenAI_StreamSplitStringifiedTextDeltaContentBlocksBuffersUntilComplete(t *testing.T) {
	ctx := context.Background()
	var param any
	chunks := [][]byte{
		[]byte(`data: {"type":"response.output_text.delta","delta":"[{\"type\":\"text\",\"text\":\"hel"}`),
		[]byte(`data: {"type":"response.output_text.delta","delta":"lo\"},{\"type\":\"output_text\",\"text\":\" world\"}]"}`),
	}
	var deltas []string
	for _, chunk := range chunks {
		out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, chunk, &param)
		for _, event := range out {
			content := gjson.GetBytes(event, "choices.0.delta.content")
			if content.Exists() {
				deltas = append(deltas, content.String())
			}
		}
	}
	joined := strings.Join(deltas, "")
	if joined != "hello world" {
		t.Fatalf("joined content = %q, want hello world; deltas=%q", joined, deltas)
	}
	for _, delta := range deltas {
		if strings.Contains(delta, `"type":"text"`) || strings.Contains(delta, "output_text") || strings.Contains(delta, `[{`) {
			t.Fatalf("content block JSON leaked in stream delta: %q; all deltas=%q", delta, deltas)
		}
	}
}
