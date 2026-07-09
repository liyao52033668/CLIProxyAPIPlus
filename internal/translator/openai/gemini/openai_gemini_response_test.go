package gemini

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponseToGemini_StreamContentBlocksExtractText(t *testing.T) {
	ctx := context.Background()
	var param any
	chunk := []byte(`data: {"choices":[{"index":0,"delta":{"content":[{"type":"text","text":"hello"},{"type":"output_text","text":" world"},{"type":"image_url","image_url":{"url":"ignored"}}]},"finish_reason":null}]}`)

	outputs := ConvertOpenAIResponseToGemini(ctx, "", nil, nil, chunk, &param)
	if len(outputs) != 1 {
		t.Fatalf("expected one output, got %d: %q", len(outputs), outputs)
	}

	text := gjson.GetBytes(outputs[0], "candidates.0.content.parts.0.text").String()
	if text != "hello world" {
		t.Fatalf("content text = %q, want hello world. Output=%s", text, string(outputs[0]))
	}
	if strings.Contains(text, `"type":"text"`) {
		t.Fatalf("content blocks were serialized into text: %q", text)
	}
}

func TestConvertOpenAIResponseToGeminiNonStream_ContentBlocksExtractText(t *testing.T) {
	ctx := context.Background()
	response := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"text","text":"hello"},{"type":"output_text","text":" world"}]},"finish_reason":"stop"}]}`)

	out := ConvertOpenAIResponseToGeminiNonStream(ctx, "", nil, nil, response, nil)
	text := gjson.GetBytes(out, "candidates.0.content.parts.0.text").String()
	if text != "hello world" {
		t.Fatalf("content text = %q, want hello world. Output=%s", text, string(out))
	}
	if strings.Contains(text, `"type":"text"`) {
		t.Fatalf("content blocks were serialized into text: %q", text)
	}
}
