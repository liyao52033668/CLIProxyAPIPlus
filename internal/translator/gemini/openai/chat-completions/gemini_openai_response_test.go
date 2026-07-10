package chat_completions

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiResponseToOpenAI_ExtractsStringifiedTextContentBlocksFromStreamDelta(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertGeminiResponseToOpenAI(ctx, "gemini", nil, nil, []byte(`data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"[{\"type\":\"text\",\"text\":\"hello\"},{\"type\":\"output_text\",\"text\":\" world\"}]"}]}}],"modelVersion":"gemini"}`), &param)
	if len(out) != 1 {
		t.Fatalf("got %d output chunks, want 1", len(out))
	}

	text := gjson.GetBytes(out[0], "choices.0.delta.content").String()
	if text != "hello world" {
		t.Fatalf("delta content = %q, want hello world", text)
	}
	if strings.Contains(text, `"type":"text"`) || strings.Contains(text, "output_text") {
		t.Fatalf("content blocks were serialized into stream delta: %q", text)
	}
}

func TestConvertGeminiResponseToOpenAI_BuffersSplitStringifiedTextContentBlocksFromStreamDelta(t *testing.T) {
	ctx := context.Background()
	var param any

	chunks := [][]byte{
		[]byte(`data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"[{\"type\":\"text\",\"text\":\"hel"}]}}],"modelVersion":"gemini"}`),
		[]byte(`data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"lo\"},{\"type\":\"output_text\",\"text\":\" world\"}]"}]}}],"modelVersion":"gemini"}`),
	}

	var deltas []string
	for _, chunk := range chunks {
		out := ConvertGeminiResponseToOpenAI(ctx, "gemini", nil, nil, chunk, &param)
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
