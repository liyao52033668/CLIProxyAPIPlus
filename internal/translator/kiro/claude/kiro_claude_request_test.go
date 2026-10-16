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

// TestBuildKiroPayload_SynthesizesToolSpecsFromHistory verifies that when a
// client omits the top-level tools array but history contains tool turns
// (e.g. after client-side compaction), the payload synthesizes stub tool
// specs so Kiro does not reject it with "Improperly formed request".
func TestBuildKiroPayload_SynthesizesToolSpecsFromHistory(t *testing.T) {
	claudeReq := `{
		"model": "claude-sonnet-4-5",
		"max_tokens": 1024,
		"messages": [
			{"role": "user", "content": "list files"},
			{"role": "assistant", "content": [
				{"type": "tool_use", "id": "toolu_01", "name": "Grep", "input": {"pattern": "*.go"}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "toolu_01", "content": "main.go"}
			]}
		]
	}`

	out, _ := BuildKiroPayload([]byte(claudeReq), "claude-sonnet-4-5", "arn:test", "test", false, false, http.Header{}, nil)

	tools := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.userInputMessageContext.tools")
	if !tools.IsArray() || len(tools.Array()) == 0 {
		t.Fatalf("expected stub tools to be synthesized, got: %s", tools.Raw)
	}
	found := false
	for _, tool := range tools.Array() {
		if tool.Get("toolSpecification.name").String() == "Grep" {
			found = true
			schemaType := tool.Get("toolSpecification.inputSchema.json.type").String()
			if schemaType != "object" {
				t.Fatalf("expected stub schema type 'object', got: %s", schemaType)
			}
		}
	}
	if !found {
		t.Fatalf("expected synthesized stub spec for tool 'Grep', got: %s", tools.Raw)
	}
}

// TestBuildKiroPayload_ChatOnlySkipsStubSynthesis verifies that chat-only
// model variants never re-advertise tools, even when history has tool turns.
func TestBuildKiroPayload_ChatOnlySkipsStubSynthesis(t *testing.T) {
	claudeReq := `{
		"model": "claude-sonnet-4-5-chat",
		"max_tokens": 1024,
		"messages": [
			{"role": "user", "content": "list files"},
			{"role": "assistant", "content": [
				{"type": "tool_use", "id": "toolu_01", "name": "Grep", "input": {}}
			]},
			{"role": "user", "content": "thanks"}
		]
	}`

	out, _ := BuildKiroPayload([]byte(claudeReq), "claude-sonnet-4-5-chat", "arn:test", "test", false, true, http.Header{}, nil)

	tools := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.userInputMessageContext.tools")
	if tools.Exists() && len(tools.Array()) > 0 {
		t.Fatalf("chat-only requests must not synthesize tools, got: %s", tools.Raw)
	}
}

// TestBuildKiroPayload_FallbackContentNonEmpty verifies the fallback current
// message gets non-empty content when there is no system prompt, since Kiro
// rejects empty currentMessage content with "Improperly formed request".
func TestBuildKiroPayload_FallbackContentNonEmpty(t *testing.T) {
	// Only an assistant message: currentUserMsg stays nil, forcing the fallback path.
	claudeReq := `{
		"model": "claude-sonnet-4-5",
		"max_tokens": 1024,
		"messages": [
			{"role": "assistant", "content": "done"}
		]
	}`

	out, _ := BuildKiroPayload([]byte(claudeReq), "claude-sonnet-4-5", "arn:test", "test", false, false, http.Header{}, nil)

	content := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.content").String()
	if strings.TrimSpace(content) == "" {
		t.Fatalf("expected fallback content to be non-empty, got empty string")
	}
}
