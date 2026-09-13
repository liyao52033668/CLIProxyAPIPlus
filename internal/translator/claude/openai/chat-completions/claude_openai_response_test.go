package chat_completions

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestMapAnthropicStopReasonToOpenAIContentFilter(t *testing.T) {
	for _, reason := range []string{"refusal", "sensitive"} {
		if got := mapAnthropicStopReasonToOpenAI(reason); got != "content_filter" {
			t.Fatalf("%s maps to %q, want content_filter", reason, got)
		}
	}
}

func TestConvertClaudeResponseToOpenAI_StreamUsageIncludesCachedTokens(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertClaudeResponseToOpenAI(
		ctx,
		"claude-opus-4-6",
		nil,
		nil,
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":13,"output_tokens":4,"cache_read_input_tokens":22000,"cache_creation_input_tokens":31}}`),
		&param,
	)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	if gotPromptTokens := gjson.GetBytes(out[0], "usage.prompt_tokens").Int(); gotPromptTokens != 22044 {
		t.Fatalf("expected prompt_tokens %d, got %d", 22044, gotPromptTokens)
	}
	if gotCompletionTokens := gjson.GetBytes(out[0], "usage.completion_tokens").Int(); gotCompletionTokens != 4 {
		t.Fatalf("expected completion_tokens %d, got %d", 4, gotCompletionTokens)
	}
	if gotTotalTokens := gjson.GetBytes(out[0], "usage.total_tokens").Int(); gotTotalTokens != 22048 {
		t.Fatalf("expected total_tokens %d, got %d", 22048, gotTotalTokens)
	}
	if gotCachedTokens := gjson.GetBytes(out[0], "usage.prompt_tokens_details.cached_tokens").Int(); gotCachedTokens != 22000 {
		t.Fatalf("expected cached_tokens %d, got %d", 22000, gotCachedTokens)
	}
}

func TestConvertClaudeResponseToOpenAI_StreamUsageMergesMessageStartUsage(t *testing.T) {
	ctx := context.Background()
	var param any

	ConvertClaudeResponseToOpenAI(
		ctx,
		"claude-opus-4-6",
		nil,
		nil,
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","model":"claude-opus-4-6","usage":{"input_tokens":13,"output_tokens":1,"cache_read_input_tokens":22000,"cache_creation_input_tokens":31}}}`),
		&param,
	)
	out := ConvertClaudeResponseToOpenAI(
		ctx,
		"claude-opus-4-6",
		nil,
		nil,
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`),
		&param,
	)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	if gotPromptTokens := gjson.GetBytes(out[0], "usage.prompt_tokens").Int(); gotPromptTokens != 22044 {
		t.Fatalf("expected prompt_tokens %d, got %d", 22044, gotPromptTokens)
	}
	if gotCompletionTokens := gjson.GetBytes(out[0], "usage.completion_tokens").Int(); gotCompletionTokens != 4 {
		t.Fatalf("expected completion_tokens %d, got %d", 4, gotCompletionTokens)
	}
	if gotTotalTokens := gjson.GetBytes(out[0], "usage.total_tokens").Int(); gotTotalTokens != 22048 {
		t.Fatalf("expected total_tokens %d, got %d", 22048, gotTotalTokens)
	}
	if gotCachedTokens := gjson.GetBytes(out[0], "usage.prompt_tokens_details.cached_tokens").Int(); gotCachedTokens != 22000 {
		t.Fatalf("expected cached_tokens %d, got %d", 22000, gotCachedTokens)
	}
}

func TestConvertClaudeResponseToOpenAINonStream_UsageIncludesCachedTokens(t *testing.T) {
	rawJSON := []byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"model\":\"claude-opus-4-6\"}}\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":13,\"output_tokens\":4,\"cache_read_input_tokens\":22000,\"cache_creation_input_tokens\":31}}\n")

	out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "", nil, nil, rawJSON, nil)

	if gotPromptTokens := gjson.GetBytes(out, "usage.prompt_tokens").Int(); gotPromptTokens != 22044 {
		t.Fatalf("expected prompt_tokens %d, got %d", 22044, gotPromptTokens)
	}
	if gotCompletionTokens := gjson.GetBytes(out, "usage.completion_tokens").Int(); gotCompletionTokens != 4 {
		t.Fatalf("expected completion_tokens %d, got %d", 4, gotCompletionTokens)
	}
	if gotTotalTokens := gjson.GetBytes(out, "usage.total_tokens").Int(); gotTotalTokens != 22048 {
		t.Fatalf("expected total_tokens %d, got %d", 22048, gotTotalTokens)
	}
	if gotCachedTokens := gjson.GetBytes(out, "usage.prompt_tokens_details.cached_tokens").Int(); gotCachedTokens != 22000 {
		t.Fatalf("expected cached_tokens %d, got %d", 22000, gotCachedTokens)
	}
}

func TestConvertClaudeResponseToOpenAINonStream_UsageMergesMessageStartUsage(t *testing.T) {
	rawJSON := []byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"model\":\"claude-opus-4-6\",\"usage\":{\"input_tokens\":13,\"output_tokens\":1,\"cache_read_input_tokens\":22000,\"cache_creation_input_tokens\":31}}}\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":4}}\n")

	out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "", nil, nil, rawJSON, nil)

	if gotPromptTokens := gjson.GetBytes(out, "usage.prompt_tokens").Int(); gotPromptTokens != 22044 {
		t.Fatalf("expected prompt_tokens %d, got %d", 22044, gotPromptTokens)
	}
	if gotCompletionTokens := gjson.GetBytes(out, "usage.completion_tokens").Int(); gotCompletionTokens != 4 {
		t.Fatalf("expected completion_tokens %d, got %d", 4, gotCompletionTokens)
	}
	if gotTotalTokens := gjson.GetBytes(out, "usage.total_tokens").Int(); gotTotalTokens != 22048 {
		t.Fatalf("expected total_tokens %d, got %d", 22048, gotTotalTokens)
	}
	if gotCachedTokens := gjson.GetBytes(out, "usage.prompt_tokens_details.cached_tokens").Int(); gotCachedTokens != 22000 {
		t.Fatalf("expected cached_tokens %d, got %d", 22000, gotCachedTokens)
	}
}

func TestConvertClaudeResponseToOpenAI_StreamStringifiedTextDeltaContentBlocksExtractText(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertClaudeResponseToOpenAI(
		ctx,
		"claude-opus-4-6",
		nil,
		nil,
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"[{\"type\":\"text\",\"text\":\"hello\"},{\"type\":\"output_text\",\"text\":\" world\"}]"}}`),
		&param,
	)
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

func TestConvertClaudeResponseToOpenAI_StreamSplitStringifiedTextDeltaContentBlocksBuffersUntilComplete(t *testing.T) {
	ctx := context.Background()
	var param any
	chunks := [][]byte{
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"[{\"type\":\"text\",\"text\":\"hel"}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo\"},{\"type\":\"output_text\",\"text\":\" world\"}]"}}`),
	}
	var deltas []string
	for _, chunk := range chunks {
		out := ConvertClaudeResponseToOpenAI(ctx, "claude-opus-4-6", nil, nil, chunk, &param)
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

func TestConvertClaudeResponseToOpenAI_StreamEmitsTrailingUsageChunkWithCacheDetails(t *testing.T) {
	ctx := context.Background()
	var param any

	events := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","model":"claude-opus-4-6","usage":{"input_tokens":100,"cache_creation_input_tokens":20,"cache_read_input_tokens":50,"output_tokens":1}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":15}}`),
		[]byte(`data: {"type":"message_stop"}`),
		[]byte(`data: {"type":"message_stop"}`), // Duplicate message_stop should be ignored via TrailingUsageSent
	}

	var allChunks [][]byte
	for _, ev := range events {
		chunks := ConvertClaudeResponseToOpenAI(ctx, "claude-opus-4-6", nil, nil, ev, &param)
		allChunks = append(allChunks, chunks...)
	}

	// Verify exactly one trailing usage chunk with empty choices array exists as expected by OpenAI streaming spec and LiteLLM
	var trailingUsageChunk []byte
	var trailingUsageChunkIndex = -1
	var finishReasonChunkIndex = -1
	trailingUsageChunkCount := 0

	for i, chunk := range allChunks {
		if gjson.GetBytes(chunk, "choices.0.finish_reason").Exists() && gjson.GetBytes(chunk, "choices.0.finish_reason").String() != "" {
			finishReasonChunkIndex = i
		}
		choices := gjson.GetBytes(chunk, "choices")
		if choices.Exists() && len(choices.Array()) == 0 && gjson.GetBytes(chunk, "usage").Exists() {
			trailingUsageChunk = chunk
			trailingUsageChunkIndex = i
			trailingUsageChunkCount++
		}
	}

	if trailingUsageChunkCount != 1 {
		t.Fatalf("expected exactly 1 trailing usage chunk with empty choices array (choices: []), got %d; chunks: %s", trailingUsageChunkCount, string(bytes.Join(allChunks, []byte("\n"))))
	}

	if finishReasonChunkIndex >= trailingUsageChunkIndex {
		t.Fatalf("expected finish_reason chunk (index %d) before trailing usage chunk (index %d)", finishReasonChunkIndex, trailingUsageChunkIndex)
	}

	if gotPromptTokens := gjson.GetBytes(trailingUsageChunk, "usage.prompt_tokens").Int(); gotPromptTokens != 170 {
		t.Errorf("expected prompt_tokens 170, got %d", gotPromptTokens)
	}
	if gotCompletionTokens := gjson.GetBytes(trailingUsageChunk, "usage.completion_tokens").Int(); gotCompletionTokens != 15 {
		t.Errorf("expected completion_tokens 15, got %d", gotCompletionTokens)
	}
	if gotTotalTokens := gjson.GetBytes(trailingUsageChunk, "usage.total_tokens").Int(); gotTotalTokens != 185 {
		t.Errorf("expected total_tokens 185, got %d", gotTotalTokens)
	}
	if gotCachedTokens := gjson.GetBytes(trailingUsageChunk, "usage.prompt_tokens_details.cached_tokens").Int(); gotCachedTokens != 50 {
		t.Errorf("expected cached_tokens 50, got %d", gotCachedTokens)
	}
	if gotCacheWriteTokens := gjson.GetBytes(trailingUsageChunk, "usage.prompt_tokens_details.cache_write_tokens").Int(); gotCacheWriteTokens != 20 {
		t.Errorf("expected cache_write_tokens 20, got %d", gotCacheWriteTokens)
	}
	if gotCachedCreationTokens := gjson.GetBytes(trailingUsageChunk, "usage.prompt_tokens_details.cached_creation_tokens").Int(); gotCachedCreationTokens != 20 {
		t.Errorf("expected cached_creation_tokens 20, got %d", gotCachedCreationTokens)
	}
}
