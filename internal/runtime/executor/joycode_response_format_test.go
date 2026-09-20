package executor

import (
	"context"
	"testing"

	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// TestJoyCodeNonStreamTranslatesToClaude guards the regression where the
// executor forced the response format to openai, so /v1/messages clients
// received OpenAI JSON they could not parse.
func TestJoyCodeNonStreamTranslatesToClaude(t *testing.T) {
	claudeRequest := `{"model":"deepseek-v3","stream":false,"messages":[{"role":"user","content":"hi"}]}`
	openAIResp := []byte(`{"id":"chatcmpl-123","object":"chat.completion","created":1234567890,"model":"deepseek-v3","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)

	var param any
	out := sdktranslator.TranslateNonStream(
		context.Background(),
		sdktranslator.FormatOpenAI,
		sdktranslator.FormatClaude,
		"deepseek-v3",
		[]byte(claudeRequest),
		[]byte(claudeRequest),
		openAIResp,
		&param,
	)

	if got := gjson.GetBytes(out, "type").String(); got != "message" {
		t.Fatalf("expected Claude message envelope, got type=%q body=%s", got, out)
	}
	if got := gjson.GetBytes(out, "content.0.type").String(); got == "" {
		t.Fatalf("expected Claude content blocks, got %s", out)
	}
	if gjson.GetBytes(out, "choices").Exists() {
		t.Fatalf("OpenAI choices leaked into the Claude response: %s", out)
	}
}
