package executor

import "testing"

func TestCursorRunRequestParamsUseNormalizedRequestModel(t *testing.T) {
	reqModel := "default"
	payload := []byte(`{"model":"cursor-auto","messages":[{"role":"user","content":"hello"}]}`)

	parsed := parseOpenAIRequest(payload)
	params := buildRunRequestParams(reqModel, parsed, "conv-1")

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
