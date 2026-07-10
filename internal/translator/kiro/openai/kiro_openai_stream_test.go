package openai

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertKiroStreamToOpenAI_ExtractsStringifiedTextContentBlocks(t *testing.T) {
	ctx := context.Background()
	var param any

	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"kiro","stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"[{\"type\":\"text\",\"text\":\"hello\"},{\"type\":\"output_text\",\"text\":\" world\"}]"}}`),
	}

	var text strings.Builder
	for _, chunk := range chunks {
		out := ConvertKiroStreamToOpenAI(ctx, "kiro", nil, nil, chunk, &param)
		for _, event := range out {
			content := gjson.GetBytes(event, "choices.0.delta.content")
			if content.Exists() {
				text.WriteString(content.String())
			}
		}
	}

	if got := text.String(); got != "hello world" {
		t.Fatalf("content text = %q, want hello world", got)
	}
	if strings.Contains(text.String(), `"type":"text"`) || strings.Contains(text.String(), "output_text") {
		t.Fatalf("content blocks were serialized into text: %q", text.String())
	}
}

func TestConvertKiroStreamToOpenAI_BuffersSplitStringifiedTextContentBlocks(t *testing.T) {
	ctx := context.Background()
	var param any

	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"kiro","stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"[{\"type\":\"text\",\"text\":\"hel"}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo\"},{\"type\":\"output_text\",\"text\":\" world\"}]"}}`),
	}

	var deltas []string
	for _, chunk := range chunks {
		out := ConvertKiroStreamToOpenAI(ctx, "kiro", nil, nil, chunk, &param)
		for _, event := range out {
			content := gjson.GetBytes(event, "choices.0.delta.content")
			if content.Exists() {
				deltas = append(deltas, content.String())
			}
		}
	}

	joined := strings.Join(deltas, "")
	if joined != "hello world" {
		t.Fatalf("joined delta content = %q, want hello world; deltas=%q", joined, deltas)
	}
	for _, delta := range deltas {
		if strings.Contains(delta, `"type":"text"`) || strings.Contains(delta, "output_text") || strings.Contains(delta, `[{`) {
			t.Fatalf("content block JSON leaked in stream delta: %q; all deltas=%q", delta, deltas)
		}
	}
}
