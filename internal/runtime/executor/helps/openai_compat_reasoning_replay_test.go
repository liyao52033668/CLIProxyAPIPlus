package helps

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

type reasoningLookupCall struct {
	hasToolCalls       bool
	toolCallIDs        []string
	contentFingerprint string
	reasoning          string
	ok                 bool
}

func newRecordingLookup(calls *[]reasoningLookupCall) ReasoningLookup {
	return func(hasToolCalls bool, toolCallIDs []string, contentFingerprint string) (string, bool) {
		*calls = append(*calls, reasoningLookupCall{
			hasToolCalls:       hasToolCalls,
			toolCallIDs:        append([]string(nil), toolCallIDs...),
			contentFingerprint: contentFingerprint,
		})
		return "cached reasoning", true
	}
}

func TestRestoreOpenAICompatReasoningContentToolCallHit(t *testing.T) {
	payload := []byte(`{"model":"deepseek-reasoner","messages":[` +
		`{"role":"user","content":"hi"},` +
		`{"role":"assistant","content":"","tool_calls":[{"id":"call_0_abc","type":"function","function":{"name":"lookup","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"call_0_abc","content":"result"}` +
		`]}`)

	var calls []reasoningLookupCall
	updated, injected := RestoreOpenAICompatReasoningContent(payload, newRecordingLookup(&calls))
	if injected != 1 {
		t.Fatalf("injected = %d, want 1", injected)
	}
	reasoning := gjson.GetBytes(updated, "messages.1.reasoning_content").String()
	if reasoning != "cached reasoning" {
		t.Fatalf("assistant reasoning_content = %q, want cached reasoning", reasoning)
	}
	if gjson.GetBytes(updated, "messages.0.reasoning_content").Exists() {
		t.Fatal("user message must not receive reasoning_content")
	}
	if len(calls) != 1 {
		t.Fatalf("lookup calls = %d, want 1", len(calls))
	}
	if !calls[0].hasToolCalls || len(calls[0].toolCallIDs) != 1 || calls[0].toolCallIDs[0] != "call_0_abc" {
		t.Fatalf("lookup args = %+v, want tool call call_0_abc", calls[0])
	}
}

func TestRestoreOpenAICompatReasoningContentSkipsExisting(t *testing.T) {
	payload := []byte(`{"messages":[` +
		`{"role":"assistant","content":"answer","reasoning_content":"kept reasoning"},` +
		`{"role":"assistant","content":"","reasoning_content":"   ","tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]}` +
		`]}`)

	updated, injected := RestoreOpenAICompatReasoningContent(payload, func(bool, []string, string) (string, bool) {
		return "cached reasoning", true
	})
	if injected != 1 {
		t.Fatalf("injected = %d, want 1 (empty reasoning counts as missing)", injected)
	}
	if reasoning := gjson.GetBytes(updated, "messages.0.reasoning_content").String(); reasoning != "kept reasoning" {
		t.Fatalf("existing reasoning was overwritten: %q", reasoning)
	}
	if reasoning := gjson.GetBytes(updated, "messages.1.reasoning_content").String(); reasoning != "cached reasoning" {
		t.Fatalf("blank reasoning was not replaced: %q", reasoning)
	}
}

func TestRestoreOpenAICompatReasoningContentPlainMessageFingerprintOnly(t *testing.T) {
	fingerprint := OpenAICompatReasoningContentFingerprint(gjson.Parse(`"plain assistant answer"`))
	payload := []byte(`{"messages":[{"role":"assistant","content":"plain assistant answer"}]}`)

	var sawHasToolCalls bool
	updated, injected := RestoreOpenAICompatReasoningContent(payload, func(hasToolCalls bool, toolCallIDs []string, fp string) (string, bool) {
		sawHasToolCalls = hasToolCalls
		if fp != fingerprint {
			t.Fatalf("fingerprint = %q, want %q", fp, fingerprint)
		}
		return "matched reasoning", true
	})
	if injected != 1 || sawHasToolCalls {
		t.Fatalf("injected = %d hasToolCalls = %v, want 1 false", injected, sawHasToolCalls)
	}
	if reasoning := gjson.GetBytes(updated, "messages.0.reasoning_content").String(); reasoning != "matched reasoning" {
		t.Fatalf("plain message reasoning = %q", reasoning)
	}

	updated, injected = RestoreOpenAICompatReasoningContent(payload, func(bool, []string, string) (string, bool) {
		return "latest reasoning", true
	})
	_ = updated
	if injected != 1 {
		t.Fatalf("plain message must still be fillable through the lookup, injected = %d", injected)
	}
}

func TestRestoreOpenAICompatReasoningContentNoMessages(t *testing.T) {
	payload := []byte(`{"model":"m"}`)
	updated, injected := RestoreOpenAICompatReasoningContent(payload, func(bool, []string, string) (string, bool) {
		return "r", true
	})
	if injected != 0 || string(updated) != string(payload) {
		t.Fatalf("payload without messages must be untouched, injected = %d", injected)
	}
}

func TestCaptureOpenAICompatReasoningResponse(t *testing.T) {
	body := []byte(`{"id":"cmpl-1","object":"chat.completion","choices":[` +
		`{"index":0,"message":{"role":"assistant","content":"let me check","reasoning_content":"think step by step","tool_calls":[{"id":"call_0_a","type":"function","function":{"name":"f","arguments":"{}"}},{"id":"call_0_b","type":"function","function":{"name":"g","arguments":"{}"}}]},"finish_reason":"tool_calls"},` +
		`{"index":1,"message":{"role":"assistant","content":"no reasoning here"},"finish_reason":"stop"}` +
		`]}`)

	turns := CaptureOpenAICompatReasoningResponse(body)
	if len(turns) != 1 {
		t.Fatalf("turns = %d, want 1", len(turns))
	}
	if turns[0].Reasoning != "think step by step" {
		t.Fatalf("reasoning = %q", turns[0].Reasoning)
	}
	if len(turns[0].ToolCallIDs) != 2 || turns[0].ToolCallIDs[0] != "call_0_a" || turns[0].ToolCallIDs[1] != "call_0_b" {
		t.Fatalf("tool call ids = %v", turns[0].ToolCallIDs)
	}
	want := OpenAICompatReasoningContentFingerprint(gjson.Parse(`"let me check"`))
	if turns[0].ContentFingerprint != want {
		t.Fatalf("content fingerprint = %q, want %q", turns[0].ContentFingerprint, want)
	}
}

func TestStreamCaptureAccumulatesAcrossChunks(t *testing.T) {
	capture := NewOpenAICompatReasoningStreamCapture()
	capture.Observe([]byte(`data: {"choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"think "}}]}`))
	capture.Observe([]byte(`data: {"choices":[{"index":0,"delta":{"reasoning_content":"hard"}}]}`))
	capture.Observe([]byte(`data: {"choices":[{"index":0,"delta":{"content":"let me "}}]}`))
	capture.Observe([]byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_0_x","type":"function","function":{"name":"f","arguments":""}}]}}]}`))
	capture.Observe([]byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`))
	capture.Observe([]byte(`data: {"choices":[{"index":0,"delta":{"content":"check"}}]}`))
	capture.Observe([]byte(`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`))
	capture.Observe([]byte(`data: [DONE]`))

	turns := capture.Finish()
	if len(turns) != 1 {
		t.Fatalf("turns = %d, want 1", len(turns))
	}
	if turns[0].Reasoning != "think hard" {
		t.Fatalf("reasoning = %q, want think hard", turns[0].Reasoning)
	}
	if len(turns[0].ToolCallIDs) != 1 || turns[0].ToolCallIDs[0] != "call_0_x" {
		t.Fatalf("tool call ids = %v, want [call_0_x]", turns[0].ToolCallIDs)
	}
	want := OpenAICompatReasoningContentFingerprint(gjson.Parse(`"let me check"`))
	if turns[0].ContentFingerprint != want {
		t.Fatalf("content fingerprint = %q, want %q", turns[0].ContentFingerprint, want)
	}
	if again := capture.Finish(); len(again) != 0 {
		t.Fatalf("capture must reset after Finish, got %d turns", len(again))
	}
}

func TestStreamCaptureIgnoresInvalidLines(t *testing.T) {
	capture := NewOpenAICompatReasoningStreamCapture()
	capture.Observe([]byte(`data: [DONE]`))
	capture.Observe([]byte(`data: not-json`))
	capture.Observe([]byte(`event: ping`))
	if turns := capture.Finish(); len(turns) != 0 {
		t.Fatalf("turns = %d, want 0", len(turns))
	}
}

func TestStreamCaptureSkipsChoicesWithoutReasoning(t *testing.T) {
	capture := NewOpenAICompatReasoningStreamCapture()
	capture.Observe([]byte(`data: {"choices":[{"index":0,"delta":{"content":"plain answer"}}]}`))
	if turns := capture.Finish(); len(turns) != 0 {
		t.Fatalf("turns = %d, want 0", len(turns))
	}
}

func TestOpenAICompatReasoningContentFingerprintStable(t *testing.T) {
	asString := OpenAICompatReasoningContentFingerprint(gjson.Parse(`"hello world"`))
	asArray := OpenAICompatReasoningContentFingerprint(gjson.Parse(`[{"type":"text","text":"hello world"}]`))
	if asString == "" || asString != asArray {
		t.Fatalf("string fingerprint %q != array fingerprint %q", asString, asArray)
	}
	if OpenAICompatReasoningContentFingerprint(gjson.Parse(`""`)) != "" {
		t.Fatal("empty content must produce an empty fingerprint")
	}
	if OpenAICompatReasoningContentFingerprint(gjson.Parse(`null`)) != "" {
		t.Fatal("null content must produce an empty fingerprint")
	}
}

func TestRestoreOpenAICompatReasoningContentRoundTripWithCache(t *testing.T) {
	// Simulate the full cycle: capture a streamed turn, then restore it into a
	// replayed history where the client dropped reasoning_content.
	capture := NewOpenAICompatReasoningStreamCapture()
	capture.Observe([]byte(`data: {"choices":[{"index":0,"delta":{"reasoning_content":"deep thought"}}]}`))
	capture.Observe([]byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_0_rt","type":"function","function":{"name":"f","arguments":"{}"}}]}}]}`))
	turns := capture.Finish()
	if len(turns) != 1 {
		t.Fatalf("turns = %d, want 1", len(turns))
	}

	payload := []byte(`{"messages":[` +
		`{"role":"assistant","content":"","tool_calls":[{"id":"call_0_rt","type":"function","function":{"name":"f","arguments":"{}"}}]}` +
		`]}`)
	updated, injected := RestoreOpenAICompatReasoningContent(payload, func(hasToolCalls bool, toolCallIDs []string, fingerprint string) (string, bool) {
		if hasToolCalls {
			for _, id := range toolCallIDs {
				if id == "call_0_rt" {
					return turns[0].Reasoning, true
				}
			}
		}
		return "", false
	})
	if injected != 1 {
		t.Fatalf("injected = %d, want 1", injected)
	}
	if reasoning := gjson.GetBytes(updated, "messages.0.reasoning_content").String(); reasoning != "deep thought" {
		t.Fatalf("round-trip reasoning = %q, want deep thought", reasoning)
	}
	if !strings.Contains(gjson.GetBytes(updated, "messages.0.tool_calls.0.id").String(), "call_0_rt") {
		t.Fatal("tool_calls must remain intact after restore")
	}
}
