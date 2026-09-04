package executor

import (
	"context"
	"strings"
	"testing"

	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// claudeRequest mirrors the body a /v1/messages client sends.
const claudeRequest = `{"model":"openpangu-2.0-pro","stream":true,"messages":[{"role":"user","content":"hi"}]}`

// TestCodeArtsNonStreamTranslatesToClaude guards the regression where the
// executor forced the response format to openai, so /v1/messages clients
// received OpenAI JSON they could not parse and reported an empty response.
func TestCodeArtsNonStreamTranslatesToClaude(t *testing.T) {
	openAIResp := buildOpenAINonStreamResponse("hello", "thinking", "openpangu-2.0-pro", "chat123", 16, 139, nil)

	var param any
	out := sdktranslator.TranslateNonStream(
		context.Background(),
		sdktranslator.FormatOpenAI,
		sdktranslator.FormatClaude,
		"openpangu-2.0-pro",
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

// TestCodeArtsStreamChunkTranslatesToClaude verifies that executor chunks are
// framed as SSE data lines. The Claude stream translator drops unframed
// payloads outright, which silently emptied the whole stream.
func TestCodeArtsStreamChunkTranslatesToClaude(t *testing.T) {
	state := &codeartsStreamState{}
	res := codeartsStreamResult{
		HasContent:   true,
		ContentValue: "hello",
		ModelName:    "openpangu-2.0-pro",
		Role:         "assistant",
	}
	chunk := buildCodeArtsOpenAIChunk(state, &res)
	if chunk == nil {
		t.Fatal("expected a chunk to be built")
	}

	var param any
	events := sdktranslator.TranslateStream(
		context.Background(),
		sdktranslator.FormatOpenAI,
		sdktranslator.FormatClaude,
		"openpangu-2.0-pro",
		[]byte(claudeRequest),
		[]byte(claudeRequest),
		append([]byte("data: "), chunk...),
		&param,
	)
	if len(events) == 0 {
		t.Fatal("framed chunk produced no Claude events")
	}

	var joined strings.Builder
	for _, ev := range events {
		joined.Write(ev)
	}
	if !strings.Contains(joined.String(), "event: message_start") {
		t.Fatalf("expected message_start event, got %s", joined.String())
	}
	if !strings.Contains(joined.String(), "hello") {
		t.Fatalf("expected content delta to carry the text, got %s", joined.String())
	}

	// An unframed chunk is what the executor used to emit; assert it is dropped
	// so the framing above is provably load-bearing.
	var bareParam any
	if bare := sdktranslator.TranslateStream(
		context.Background(),
		sdktranslator.FormatOpenAI,
		sdktranslator.FormatClaude,
		"openpangu-2.0-pro",
		[]byte(claudeRequest),
		[]byte(claudeRequest),
		chunk,
		&bareParam,
	); len(bare) != 0 {
		t.Fatalf("unframed chunk unexpectedly translated: %s", bare)
	}
}

// TestCodeArtsStreamDoneEmitsClaudeTerminator ensures the terminal marker
// reaches the translator so Claude clients receive message_stop.
func TestCodeArtsStreamDoneEmitsClaudeTerminator(t *testing.T) {
	var param any
	ctx := context.Background()

	state := &codeartsStreamState{}
	res := codeartsStreamResult{HasContent: true, ContentValue: "hi", ModelName: "openpangu-2.0-pro", Role: "assistant"}
	chunk := buildCodeArtsOpenAIChunk(state, &res)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, sdktranslator.FormatClaude,
		"openpangu-2.0-pro", []byte(claudeRequest), []byte(claudeRequest), append([]byte("data: "), chunk...), &param)

	events := sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, sdktranslator.FormatClaude,
		"openpangu-2.0-pro", []byte(claudeRequest), []byte(claudeRequest), []byte("data: [DONE]"), &param)

	var joined strings.Builder
	for _, ev := range events {
		joined.Write(ev)
	}
	if !strings.Contains(joined.String(), "message_stop") {
		t.Fatalf("expected message_stop terminator, got %s", joined.String())
	}
}

// TestCodeArtsOpenAIClientStillPassesThrough pins the Apifox-style path that
// already worked: openai clients keep receiving the OpenAI payload verbatim.
func TestCodeArtsOpenAIClientStillPassesThrough(t *testing.T) {
	openAIResp := buildOpenAINonStreamResponse("hello", "", "openpangu-2.0-pro", "chat123", 16, 139, nil)
	openAIRequest := `{"model":"openpangu-2.0-pro","messages":[{"role":"user","content":"hi"}]}`

	var param any
	out := sdktranslator.TranslateNonStream(
		context.Background(),
		sdktranslator.FormatOpenAI,
		sdktranslator.FormatOpenAI,
		"openpangu-2.0-pro",
		[]byte(openAIRequest),
		[]byte(openAIRequest),
		openAIResp,
		&param,
	)
	if got := gjson.GetBytes(out, "choices.0.message.content").String(); got != "hello" {
		t.Fatalf("expected OpenAI passthrough content, got %s", out)
	}
}
