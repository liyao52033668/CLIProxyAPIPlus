package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiResponseToClaude_StringifiedTextContentBlocksExtractText(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertGeminiResponseToClaude(ctx, "gemini-test", nil, nil, []byte(`data: {"candidates":[{"content":{"parts":[{"text":"[{\"type\":\"text\",\"text\":\"hello\"},{\"type\":\"output_text\",\"text\":\" world\"}]"}]}}]}`), &param)

	var text string
	for _, chunk := range out {
		for _, line := range strings.Split(string(chunk), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if gjson.Get(data, "type").String() == "content_block_delta" {
				text += gjson.Get(data, "delta.text").String()
			}
		}
	}

	if text != "hello world" {
		t.Fatalf("text delta = %q, want hello world; outputs=%q", text, out)
	}
	if strings.Contains(text, `"type":"text"`) {
		t.Fatalf("content blocks were serialized into text: %q", text)
	}
}
