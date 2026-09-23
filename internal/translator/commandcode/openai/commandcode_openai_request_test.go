package openai

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIToCommandCodeRequest_RaisesMaxTokensFloor(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"ping"}],"max_tokens":1}`)
	out := ConvertOpenAIToCommandCodeRequest("gpt-5.6-luna", body, false)
	if got := gjson.GetBytes(out, "params.max_tokens").Int(); got != 16 {
		t.Fatalf("params.max_tokens = %d, want 16", got)
	}
}

func TestConvertOpenAIToCommandCodeRequest_KeepsMaxTokensAboveFloor(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"ping"}],"max_tokens":32}`)
	out := ConvertOpenAIToCommandCodeRequest("gpt-5.6-luna", body, false)
	if got := gjson.GetBytes(out, "params.max_tokens").Int(); got != 32 {
		t.Fatalf("params.max_tokens = %d, want 32", got)
	}
}
