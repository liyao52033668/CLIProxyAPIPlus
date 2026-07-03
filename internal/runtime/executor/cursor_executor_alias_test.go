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
