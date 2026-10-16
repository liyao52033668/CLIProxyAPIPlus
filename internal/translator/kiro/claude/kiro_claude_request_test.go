package claude

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// TestBuildKiroPayload_OmitsPerRequestTimestampContext verifies that BuildKiroPayload
// does not inject a dynamic "[Context: Current time is ...]" timestamp header into the
// system prompt, preserving prefix caching on the Kiro backend across turns.
func TestBuildKiroPayload_OmitsPerRequestTimestampContext(t *testing.T) {
	claudeReq := `{
		"model": "claude-sonnet-4-5",
		"max_tokens": 1024,
		"system": "You are an assistant.",
		"messages": [
			{"role": "user", "content": "hello"}
		]
	}`

	out1, _ := BuildKiroPayload([]byte(claudeReq), "claude-sonnet-4-5", "arn:test", "test", false, false, http.Header{}, nil)
	out2, _ := BuildKiroPayload([]byte(claudeReq), "claude-sonnet-4-5", "arn:test", "test", false, false, http.Header{}, nil)

	content1 := gjson.GetBytes(out1, "conversationState.currentMessage.userInputMessage.content").String()
	content2 := gjson.GetBytes(out2, "conversationState.currentMessage.userInputMessage.content").String()

	if strings.Contains(content1, "[Context: Current time is") {
		t.Fatalf("expected payload content to omit timestamp context, got: %s", content1)
	}
	if content1 != content2 {
		t.Fatalf("expected consecutive payloads with identical prompts to be byte-identical, but got:\n1: %s\n2: %s", content1, content2)
	}
	if !strings.Contains(content1, "You are an assistant.") {
		t.Fatalf("expected system prompt to be preserved, got: %s", content1)
	}
}
