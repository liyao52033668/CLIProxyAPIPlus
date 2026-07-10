package chat_completions

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertAntigravityResponseToOpenAI_ExtractsStringifiedTextContentBlocksFromStreamDelta(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertAntigravityResponseToOpenAI(ctx, "antigravity", nil, nil, []byte(`{"response":{"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"[{\"type\":\"text\",\"text\":\"hello\"},{\"type\":\"output_text\",\"text\":\" world\"}]"}]}}],"modelVersion":"antigravity"}}`), &param)
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
