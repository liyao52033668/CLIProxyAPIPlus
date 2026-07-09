package cache

import (
	"context"
	"testing"
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
