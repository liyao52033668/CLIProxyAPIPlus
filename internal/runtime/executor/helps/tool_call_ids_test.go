package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestEncodeDevinToolCallID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{name: "empty stays empty", id: "", want: ""},
		{name: "portable untouched", id: "toolu_01ABC-123", want: "toolu_01ABC-123"},
		{name: "non-portable encoded", id: "call_abc.def", want: "cpa_tid_v1_Y2FsbF9hYmMuZGVm"},
		{name: "prefix namespace re-encoded", id: "cpa_tid_v1_bogus!!", want: "cpa_tid_v1_Y3BhX3RpZF92MV9ib2d1cyEh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EncodeDevinToolCallID(tt.id); got != tt.want {
				t.Fatalf("EncodeDevinToolCallID(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestDecodeDevinToolCallIDRoundTrip(t *testing.T) {
	for _, id := range []string{"call_abc.def", "工具", "cpa_tid_v1_bogus!!", "plain-id", ""} {
		encoded := EncodeDevinToolCallID(id)
		if got := DecodeDevinToolCallID(encoded); got != id {
			t.Fatalf("round trip %q -> %q -> %q", id, encoded, got)
		}
	}
}

func TestDecodeDevinToolCallIDNonCanonicalStaysIntact(t *testing.T) {
	// Not produced by EncodeDevinToolCallID: must stay untouched.
	if got := DecodeDevinToolCallID("cpa_tid_v1_not-base64!"); got != "cpa_tid_v1_not-base64!" {
		t.Fatalf("malformed encoding changed: %q", got)
	}
}

func TestRestoreDevinToolCallIDs(t *testing.T) {
	encoded := EncodeDevinToolCallID("call_abc.def")
	prompts := []DevinPrompt{
		{ToolCalls: []DevinToolCall{{ID: encoded}}, ToolCallID: encoded, OriginalToolCallID: encoded},
		{ToolCallID: "portable"},
	}
	RestoreDevinToolCallIDs(prompts)
	if prompts[0].ToolCalls[0].ID != "call_abc.def" || prompts[0].ToolCallID != "call_abc.def" || prompts[0].OriginalToolCallID != "call_abc.def" {
		t.Fatalf("decoded prompts incorrect: %+v", prompts[0])
	}
	if prompts[1].ToolCallID != "portable" {
		t.Fatalf("portable ID changed: %q", prompts[1].ToolCallID)
	}
}

func TestNormalizeClaudeToolCallIDs(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_abc.def","name":"f"}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_abc.def"}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"kept-1"}]}]}`)
	out := NormalizeClaudeToolCallIDs(body)
	toolUse := gjson.GetBytes(out, "messages.0.content.0.id").String()
	toolResult := gjson.GetBytes(out, "messages.1.content.0.tool_use_id").String()
	kept := gjson.GetBytes(out, "messages.2.content.0.tool_use_id").String()
	if toolUse == "" || toolUse != toolResult {
		t.Fatalf("tool_use/result IDs not normalized consistently: %q vs %q", toolUse, toolResult)
	}
	if toolUse == "call_abc.def" {
		t.Fatalf("non-portable ID was not encoded: %s", out)
	}
	if kept != "kept-1" {
		t.Fatalf("portable ID changed: %q", kept)
	}
	// Idempotent: second pass keeps IDs stable.
	second := NormalizeClaudeToolCallIDs(out)
	if gjson.GetBytes(second, "messages.0.content.0.id").String() != toolUse {
		t.Fatalf("normalization is not idempotent: %s", second)
	}
}
