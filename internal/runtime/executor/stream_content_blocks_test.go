package executor

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
)

func TestCodeBuddyAggregateOpenAIChatCompletionStreamBuffersSplitStringifiedTextContentBlocks(t *testing.T) {
	raw := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":123,"model":"upstream-model","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":123,"model":"upstream-model","choices":[{"index":0,"delta":{"content":"[{\"type\":\"text\",\"text\":\"hel"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":123,"model":"upstream-model","choices":[{"index":0,"delta":{"content":"lo\"},{\"type\":\"output_text\",\"text\":\" world\"}]"},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}, "\n")

	out, _, err := aggregateOpenAIChatCompletionStream([]byte(raw))
	if err != nil {
		t.Fatalf("aggregateOpenAIChatCompletionStream error: %v", err)
	}
	content := gjson.GetBytes(out, "choices.0.message.content").String()
	if content != "hello world" {
		t.Fatalf("content = %q, want hello world; payload=%s", content, string(out))
	}
	if strings.Contains(content, `"type":"text"`) || strings.Contains(content, "output_text") || strings.Contains(content, `[{`) {
		t.Fatalf("content block JSON leaked into CodeBuddy aggregation: %q; payload=%s", content, string(out))
	}
}

func TestQoderParseQoderSSEToCompletionBuffersSplitStringifiedTextContentBlocks(t *testing.T) {
	raw := strings.Join([]string{
		`data: {"body":"{\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"created\":123,\"model\":\"upstream-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"[{\\\"type\\\":\\\"text\\\",\\\"text\\\":\\\"hel\"},\"finish_reason\":null}]}"}`,
		`data: {"body":"{\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"created\":123,\"model\":\"upstream-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\\\"},{\\\"type\\\":\\\"output_text\\\",\\\"text\\\":\\\" world\\\"}]\"},\"finish_reason\":\"stop\"}]}"}`,
		`data: [DONE]`,
	}, "\n")

	executor := NewQoderExecutor(&config.Config{})
	out := executor.parseQoderSSEToCompletion([]byte(raw), "upstream-model")
	content := gjson.GetBytes(out, "choices.0.message.content").String()
	if content != "hello world" {
		t.Fatalf("content = %q, want hello world; payload=%s", content, string(out))
	}
	if strings.Contains(content, `"type":"text"`) || strings.Contains(content, "output_text") || strings.Contains(content, `[{`) {
		t.Fatalf("content block JSON leaked into Qoder aggregation: %q; payload=%s", content, string(out))
	}
}
