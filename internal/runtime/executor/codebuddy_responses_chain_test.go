package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// translateResponsesLikeCodeBuddy mirrors the CodeBuddy (tencent) executor
// request pipeline for /v1/responses traffic: translate the OpenAI Responses
// payload to OpenAI chat completions, then apply the system-to-user guard.
func translateResponsesLikeCodeBuddy(t *testing.T, payload []byte) []byte {
	t.Helper()
	from := sdktranslator.FromString("openai-response")
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, "default-model", payload, true)
	if len(translated) == 0 {
		t.Fatalf("TranslateRequest returned empty payload")
	}
	return helps.MoveOpenAISystemToUserMessage(translated)
}

func assertNoLeadingSystem(t *testing.T, updated []byte, scenario string) {
	t.Helper()
	first := gjson.GetBytes(updated, "messages.0")
	if first.Get("role").String() == "system" {
		t.Fatalf("[%s] messages.0.role must NOT be system, got system. Payload: %s", scenario, first.Raw)
	}
}

func TestCodeBuddyResponsesChainWithInstructions(t *testing.T) {
	payload := []byte(`{"model":"default-model","stream":true,` +
		`"instructions":"You are ZCode, a coding assistant.",` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	result := translateResponsesLikeCodeBuddy(t, payload)
	assertNoLeadingSystem(t, result, "instructions")
	// The instructions text should be prepended to the first user message.
	firstUser := gjson.GetBytes(result, "messages.0")
	if firstUser.Get("role").String() != "user" {
		t.Fatalf("expected first message to be user, got %s", firstUser.Get("role").String())
	}
	content := firstUser.Get("content").String()
	if content == "" {
		t.Fatalf("user message content must not be empty")
	}
}

func TestCodeBuddyResponsesChainWithoutInstructions(t *testing.T) {
	payload := []byte(`{"model":"default-model","stream":true,` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	result := translateResponsesLikeCodeBuddy(t, payload)
	assertNoLeadingSystem(t, result, "no-instructions")
}

func TestCodeBuddyResponsesChainEmptyInstructions(t *testing.T) {
	payload := []byte(`{"model":"default-model","stream":true,` +
		`"instructions":"",` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	result := translateResponsesLikeCodeBuddy(t, payload)
	assertNoLeadingSystem(t, result, "empty-instructions")
}

func TestCodeBuddyResponsesChainSystemInputItem(t *testing.T) {
	payload := []byte(`{"model":"default-model","stream":true,` +
		`"input":[{"type":"message","role":"system","content":[{"type":"input_text","text":"be nice"}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	result := translateResponsesLikeCodeBuddy(t, payload)
	assertNoLeadingSystem(t, result, "system-input-item")
}
