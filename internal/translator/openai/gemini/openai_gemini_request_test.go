package gemini

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiRequestToOpenAI_DeterministicToolCallPairing(t *testing.T) {
	input := []byte(`{
		"contents": [
			{"role":"model","parts":[
				{"functionCall":{"name":"lookup","args":{"q":"first"}}},
				{"functionCall":{"name":"lookup","args":{"q":"second"}}}
			]},
			{"role":"user","parts":[
				{"functionResponse":{"name":"lookup","response":{"content":"first result"}}},
				{"functionResponse":{"name":"lookup","response":{"content":"second result"}}}
			]}
		]
	}`)

	first := gjson.ParseBytes(ConvertGeminiRequestToOpenAI("gemini-test", input, false))
	second := gjson.ParseBytes(ConvertGeminiRequestToOpenAI("gemini-test", input, false))
	firstID := first.Get("messages.0.tool_calls.0.id").String()
	secondID := first.Get("messages.0.tool_calls.1.id").String()
	if firstID == "" || secondID == "" || firstID == secondID {
		t.Fatalf("unexpected tool call IDs: %q, %q", firstID, secondID)
	}
	if got := second.Get("messages.0.tool_calls.0.id").String(); got != firstID {
		t.Fatalf("first tool call ID changed across conversions: %q != %q", got, firstID)
	}
	if got := second.Get("messages.0.tool_calls.1.id").String(); got != secondID {
		t.Fatalf("second tool call ID changed across conversions: %q != %q", got, secondID)
	}

	messages := first.Get("messages").Array()
	if got := messages[1].Get("tool_call_id").String(); got != firstID {
		t.Fatalf("first response matched %q, want %q", got, firstID)
	}
	if got := messages[2].Get("tool_call_id").String(); got != secondID {
		t.Fatalf("second response matched %q, want %q", got, secondID)
	}
}

func TestConvertGeminiRequestToOpenAI_PreservesExplicitToolCallID(t *testing.T) {
	input := []byte(`{"contents":[{"role":"model","parts":[{"functionCall":{"id":"explicit-call","name":"lookup","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{}}}]}]}`)
	result := gjson.ParseBytes(ConvertGeminiRequestToOpenAI("gemini-test", input, false))
	if got := result.Get("messages.0.tool_calls.0.id").String(); got != "explicit-call" {
		t.Fatalf("tool call ID = %q, want explicit-call", got)
	}
	if got := result.Get("messages.1.tool_call_id").String(); got != "explicit-call" {
		t.Fatalf("tool response ID = %q, want explicit-call", got)
	}
}
