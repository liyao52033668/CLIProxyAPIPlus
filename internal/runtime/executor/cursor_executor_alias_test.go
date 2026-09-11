package executor

import "testing"

func TestCursorRunRequestParamsUseNormalizedRequestModel(t *testing.T) {
	reqModel := "default"
	payload := []byte(`{"model":"cursor-auto","messages":[{"role":"user","content":"hello"}]}`)

	parsed := parseOpenAIRequest(payload)
	params := buildRunRequestParams(reqModel, parsed, "conv-1", nil)

	if params.ModelId != reqModel {
		t.Fatalf("params.ModelId = %q, want normalized request model %q", params.ModelId, reqModel)
	}
}

func TestCursorTokenUsageDetail(t *testing.T) {
	usage := &cursorTokenUsage{}
	usage.setInputEstimate(40) // ~10 tokens
	usage.addOutput(3)
	usage.addOutput(2)

	detail := usage.detail()
	if detail.InputTokens != 10 {
		t.Fatalf("InputTokens = %d, want %d", detail.InputTokens, 10)
	}
	if detail.OutputTokens != 5 {
		t.Fatalf("OutputTokens = %d, want %d", detail.OutputTokens, 5)
	}
	if detail.TotalTokens != 15 {
		t.Fatalf("TotalTokens = %d, want %d", detail.TotalTokens, 15)
	}
}

func TestCursorTokenUsageDetailNil(t *testing.T) {
	var usage *cursorTokenUsage
	detail := usage.detail()
	if detail.InputTokens != 0 || detail.OutputTokens != 0 || detail.TotalTokens != 0 {
		t.Fatalf("expected zero detail for nil usage, got %+v", detail)
	}
}

func TestCursorTextDeltaJSONSeparatesReasoningFromContent(t *testing.T) {
	roleSent := false

	first := cursorTextDeltaJSON("let me think", true, &roleSent)
	if first != `{"role":"assistant","reasoning_content":"let me think"}` {
		t.Fatalf("thinking delta = %s", first)
	}

	second := cursorTextDeltaJSON("plain answer", false, &roleSent)
	if second != `{"content":"plain answer"}` {
		t.Fatalf("content delta = %s", second)
	}
	if roleSent != true {
		t.Fatalf("roleSent = %v, want true", roleSent)
	}
}

func TestCursorTextDeltaJSONEmitsRoleOnce(t *testing.T) {
	roleSent := false
	_ = cursorTextDeltaJSON("a", false, &roleSent)
	again := cursorTextDeltaJSON("b", false, &roleSent)
	if again != `{"content":"b"}` {
		t.Fatalf("second delta = %s, want no role field", again)
	}
}

func TestCursorTextDeltaJSONEscapesText(t *testing.T) {
	roleSent := true
	got := cursorTextDeltaJSON("line1\nline2", false, &roleSent)
	if got != `{"content":"line1\nline2"}` {
		t.Fatalf("delta = %s", got)
	}
}

func TestCursorDoneMarkerSkipsOpenAIFormats(t *testing.T) {
	// OpenAI-format handlers write "data: [DONE]" themselves via
	// ForwardStream.WriteDone; an executor-emitted marker would duplicate it.
	if got := cursorDoneMarker(false); got != nil {
		t.Fatalf("cursorDoneMarker(false) = %q, want nil", got)
	}
	if got := cursorDoneMarker(true); string(got) != "data: [DONE]\n" {
		t.Fatalf("cursorDoneMarker(true) = %q, want the translator marker", got)
	}
}
