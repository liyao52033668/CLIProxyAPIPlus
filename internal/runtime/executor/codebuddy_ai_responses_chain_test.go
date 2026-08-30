package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codebuddy_ai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	responsesconverter "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/openai/responses"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// translateResponsesLikeExecutor mirrors the CodeBuddyAIExecutor request
// pipeline for /v1/responses traffic: translate the OpenAI Responses payload
// to OpenAI chat completions, then apply the leading-system guard.
func translateResponsesLikeExecutor(t *testing.T, payload []byte) []byte {
	t.Helper()
	from := sdktranslator.FromString("openai-response")
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, "default-model", payload, true)
	if len(translated) == 0 {
		t.Fatalf("TranslateRequest returned empty payload")
	}
	return helps.EnsureOpenAILeadingSystemMessage(translated, codebuddy_ai.DefaultSystemPrompt)
}

func assertLeadingSystem(t *testing.T, updated []byte, scenario string) {
	t.Helper()
	first := gjson.GetBytes(updated, "messages.0")
	if first.Get("role").String() != "system" {
		t.Fatalf("[%s] messages.0.role = %q, want system. Payload: %s", scenario, first.Get("role").String(), first.Raw)
	}
	content := first.Get("content")
	if content.Type != gjson.String || content.String() == "" {
		t.Fatalf("[%s] messages.0.content must be a non-empty string, got %s", scenario, content.Raw)
	}
}

func TestCodeBuddyAIResponsesChainWithInstructions(t *testing.T) {
	payload := []byte(`{"model":"default-model","stream":true,` +
		`"instructions":"You are ZCode, a coding assistant.",` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	assertLeadingSystem(t, translateResponsesLikeExecutor(t, payload), "instructions")
}

func TestCodeBuddyAIResponsesChainWithoutInstructions(t *testing.T) {
	payload := []byte(`{"model":"default-model","stream":true,` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	assertLeadingSystem(t, translateResponsesLikeExecutor(t, payload), "no-instructions")
}

func TestCodeBuddyAIResponsesChainEmptyInstructions(t *testing.T) {
	payload := []byte(`{"model":"default-model","stream":true,` +
		`"instructions":"",` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	assertLeadingSystem(t, translateResponsesLikeExecutor(t, payload), "empty-instructions")
}

func TestCodeBuddyAIResponsesChainSystemInputItem(t *testing.T) {
	payload := []byte(`{"model":"default-model","stream":true,` +
		`"input":[{"type":"message","role":"system","content":[{"type":"input_text","text":"be nice"}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	assertLeadingSystem(t, translateResponsesLikeExecutor(t, payload), "system-input-item")
}

// TestCodeBuddyAIResponsesBridgeChatFormatBody reproduces a chat completions
// body posted to /v1/responses (the live 11128 report from 2026-08-30): the
// bridge converter must fall back to the "messages" field and the executor
// guard must still guarantee a leading system prompt.
func TestCodeBuddyAIResponsesBridgeChatFormatBody(t *testing.T) {
	payload := []byte(`{"model":"default-model","messages":[{"role":"user","content":"你是什么模型"}]}`)

	converted := responsesconverter.ConvertOpenAIResponsesRequestToOpenAIChatCompletions("default-model", payload, true)
	updated := helps.EnsureOpenAILeadingSystemMessage(converted, codebuddy_ai.DefaultSystemPrompt)

	assertLeadingSystem(t, updated, "chat-format-body")
	user := gjson.GetBytes(updated, "messages.1")
	if user.Get("role").String() != "user" || user.Get("content").String() != "你是什么模型" {
		t.Fatalf("user turn must be preserved, got %s", user.Raw)
	}
}
