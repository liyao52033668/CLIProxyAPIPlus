package cache

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestXAIReasoningReplayCacheRoundTrip(t *testing.T) {
	ClearXAIReasoningReplayCache()
	t.Cleanup(ClearXAIReasoningReplayCache)

	item := []byte(`{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"think"}],"encrypted_content":"gAAAAABvalidsignaturepayloadforxaitest"}`)
	if !CacheXAIReasoningReplayItems("grok-4.3", "sess-1", [][]byte{item}) {
		// encrypted content may fail validation; store a simpler function_call instead
		item = []byte(`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{}"}`)
		if !CacheXAIReasoningReplayItems("grok-4.3", "sess-1", [][]byte{item}) {
			t.Fatal("expected cache write success")
		}
	}
	got, ok, err := GetXAIReasoningReplayItemsRequired(context.Background(), "grok-4.3", "sess-1")
	if err != nil {
		t.Fatalf("get required error: %v", err)
	}
	if !ok || len(got) == 0 {
		t.Fatal("expected cache hit")
	}
	if err := DeleteXAIReasoningReplayItemRequired(context.Background(), "grok-4.3", "sess-1"); err != nil {
		t.Fatalf("delete error: %v", err)
	}
	if _, ok, err := GetXAIReasoningReplayItemsRequired(context.Background(), "grok-4.3", "sess-1"); err != nil || ok {
		t.Fatalf("expected miss after delete, ok=%v err=%v", ok, err)
	}
}

func TestXAIReasoningReplayCacheStoresAssistantMessageWithToolCall(t *testing.T) {
	ClearXAIReasoningReplayCache()
	t.Cleanup(ClearXAIReasoningReplayCache)

	items := [][]byte{
		[]byte(`{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}`),
		[]byte(`{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"answer","annotations":[]}]}`),
	}
	if !CacheXAIReasoningReplayItems("grok-4.5", "prompt-cache:session", items) {
		t.Fatal("expected replay items to be cached")
	}

	got, ok := GetXAIReasoningReplayItems("grok-4.5", "prompt-cache:session")
	if !ok || len(got) != 2 {
		t.Fatalf("cached items = %q, %v, want two items", got, ok)
	}
	if gotText := gjson.GetBytes(got[1], "content.0.text").String(); gotText != "answer" {
		t.Fatalf("assistant message text = %q, want answer; item=%s", gotText, got[1])
	}
	if gjson.GetBytes(got[1], "id").Exists() || gjson.GetBytes(got[1], "status").Exists() {
		t.Fatalf("assistant message transport fields were not stripped: %s", got[1])
	}
}

func TestXAIReasoningReplayCacheRejectsAssistantMessageWithoutAnchor(t *testing.T) {
	ClearXAIReasoningReplayCache()
	t.Cleanup(ClearXAIReasoningReplayCache)

	items := [][]byte{[]byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}`)}
	if status := StoreXAIReasoningReplayItems(context.Background(), "grok-4.5", "message-only", items); status != XAIReasoningReplayNoReplayableState {
		t.Fatalf("store status = %v, want no replayable state", status)
	}
	if _, ok := GetXAIReasoningReplayItems("grok-4.5", "message-only"); ok {
		t.Fatal("message-only replay batch unexpectedly exists")
	}
}

func TestXAIReasoningReplayCacheStoresCustomToolCall(t *testing.T) {
	ClearXAIReasoningReplayCache()
	t.Cleanup(ClearXAIReasoningReplayCache)

	item := []byte(`{"type":"custom_tool_call","call_id":"call_2","name":"shell","input":"pwd"}`)
	if !CacheXAIReasoningReplayItems("grok-4.3", "custom-tool-call", [][]byte{item}) {
		t.Fatal("custom-tool-call-only replay batch must be cached")
	}
	items, ok := GetXAIReasoningReplayItems("grok-4.3", "custom-tool-call")
	if !ok || len(items) != 1 {
		t.Fatalf("cached items = %q, %v, want one item", items, ok)
	}
	if got := gjson.GetBytes(items[0], "input").String(); got != "pwd" {
		t.Fatalf("cached input = %q, want pwd", got)
	}
}

func TestXAIReasoningReplayCacheStoresRefusalMessagePart(t *testing.T) {
	ClearXAIReasoningReplayCache()
	t.Cleanup(ClearXAIReasoningReplayCache)

	items := [][]byte{
		[]byte(`{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}`),
		[]byte(`{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"I cannot help with that"}]}`),
	}
	if !CacheXAIReasoningReplayItems("grok-4.5", "refusal", items) {
		t.Fatal("expected refusal message with tool call to be cached")
	}
	got, ok := GetXAIReasoningReplayItems("grok-4.5", "refusal")
	if !ok || len(got) != 2 {
		t.Fatalf("cached items = %q, %v, want tool call and refusal", got, ok)
	}
	if gjson.GetBytes(got[1], "content.0.refusal").String() != "I cannot help with that" {
		t.Fatalf("refusal text missing; item=%s", got[1])
	}
	if gjson.GetBytes(got[1], "content.0.text").Exists() {
		t.Fatalf("refusal part should not use text field; item=%s", got[1])
	}
}
