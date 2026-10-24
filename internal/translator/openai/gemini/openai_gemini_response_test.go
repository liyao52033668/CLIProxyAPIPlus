package gemini

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponseToGemini_StreamContentBlocksExtractText(t *testing.T) {
	ctx := context.Background()
	var param any
	chunk := []byte(`data: {"choices":[{"index":0,"delta":{"content":[{"type":"text","text":"hello"},{"type":"output_text","text":" world"},{"type":"image_url","image_url":{"url":"ignored"}}]},"finish_reason":null}]}`)

	outputs := ConvertOpenAIResponseToGemini(ctx, "", nil, nil, chunk, &param)
	if len(outputs) != 1 {
		t.Fatalf("expected one output, got %d: %q", len(outputs), outputs)
	}

	text := gjson.GetBytes(outputs[0], "candidates.0.content.parts.0.text").String()
	if text != "hello world" {
		t.Fatalf("content text = %q, want hello world. Output=%s", text, string(outputs[0]))
	}
	if strings.Contains(text, `"type":"text"`) {
		t.Fatalf("content blocks were serialized into text: %q", text)
	}
}

func TestConvertOpenAIResponseToGeminiNonStream_ContentBlocksExtractText(t *testing.T) {
	ctx := context.Background()
	response := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"text","text":"hello"},{"type":"output_text","text":" world"}]},"finish_reason":"stop"}]}`)

	out := ConvertOpenAIResponseToGeminiNonStream(ctx, "", nil, nil, response, nil)
	text := gjson.GetBytes(out, "candidates.0.content.parts.0.text").String()
	if text != "hello world" {
		t.Fatalf("content text = %q, want hello world. Output=%s", text, string(out))
	}
	if strings.Contains(text, `"type":"text"`) {
		t.Fatalf("content blocks were serialized into text: %q", text)
	}
}

func TestConvertOpenAIResponseToGeminiStream_NullFinishReasonIgnored(t *testing.T) {
	var param any

	// Chunk 1: Contentless delta with explicit finish_reason: null (e.g. role declaration or pre-reasoning delta)
	chunk1 := []byte(`{"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`)
	out1 := ConvertOpenAIResponseToGemini(context.Background(), "gpt-test", nil, nil, chunk1, &param)
	for i, chunk := range out1 {
		if fr := gjson.GetBytes(chunk, "candidates.0.finishReason"); fr.Exists() && fr.String() != "" {
			t.Fatalf("chunk1[%d] unexpectedly set finishReason = %q on non-final chunk; payload=%s", i, fr.String(), chunk)
		}
	}

	// Chunk 2: Reasoning delta with finish_reason: null
	chunk2 := []byte(`{"choices":[{"index":0,"delta":{"reasoning_content":"thinking..."},"finish_reason":null}]}`)
	out2 := ConvertOpenAIResponseToGemini(context.Background(), "gpt-test", nil, nil, chunk2, &param)
	if len(out2) == 0 {
		t.Fatalf("expected output for reasoning chunk, got 0 chunks")
	}
	if gotThought := gjson.GetBytes(out2[0], "candidates.0.content.parts.0.thought").Bool(); !gotThought {
		t.Fatalf("expected thought: true on reasoning chunk, got false")
	}
	if gotText := gjson.GetBytes(out2[0], "candidates.0.content.parts.0.text").String(); gotText != "thinking..." {
		t.Fatalf("expected reasoning text %q, got %q", "thinking...", gotText)
	}
	for i, chunk := range out2 {
		if fr := gjson.GetBytes(chunk, "candidates.0.finishReason"); fr.Exists() && fr.String() != "" {
			t.Fatalf("chunk2[%d] unexpectedly set finishReason = %q on reasoning chunk; payload=%s", i, fr.String(), chunk)
		}
	}

	// Chunk 3: Contentless delta with finish_reason: "" (empty string should not be treated as stop)
	chunk3 := []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":""}]}`)
	out3 := ConvertOpenAIResponseToGemini(context.Background(), "gpt-test", nil, nil, chunk3, &param)
	for i, chunk := range out3 {
		if fr := gjson.GetBytes(chunk, "candidates.0.finishReason"); fr.Exists() && fr.String() != "" {
			t.Fatalf("chunk3[%d] unexpectedly set finishReason = %q on contentless chunk with empty finish_reason; payload=%s", i, fr.String(), chunk)
		}
	}

	// Chunk 4: Content delta with finish_reason: null
	chunk4 := []byte(`{"choices":[{"index":0,"delta":{"content":"hello world"},"finish_reason":null}]}`)
	out4 := ConvertOpenAIResponseToGemini(context.Background(), "gpt-test", nil, nil, chunk4, &param)
	if len(out4) == 0 {
		t.Fatalf("expected output for content chunk, got 0 chunks")
	}
	if gotText := gjson.GetBytes(out4[0], "candidates.0.content.parts.0.text").String(); gotText != "hello world" {
		t.Fatalf("expected content text %q, got %q", "hello world", gotText)
	}
	for i, chunk := range out4 {
		if fr := gjson.GetBytes(chunk, "candidates.0.finishReason"); fr.Exists() && fr.String() != "" {
			t.Fatalf("chunk4[%d] unexpectedly set finishReason = %q on non-final content chunk; payload=%s", i, fr.String(), chunk)
		}
	}

	// Chunk 5: Final chunk with finish_reason: "stop"
	chunk5 := []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	out5 := ConvertOpenAIResponseToGemini(context.Background(), "gpt-test", nil, nil, chunk5, &param)
	if len(out5) == 0 {
		t.Fatalf("expected output for final chunk, got 0 chunks")
	}
	if got := gjson.GetBytes(out5[len(out5)-1], "candidates.0.finishReason").String(); got != "STOP" {
		t.Fatalf("expected finishReason STOP on final chunk, got %q", got)
	}
}

func TestConvertOpenAIResponseToGeminiNonStream_NullFinishReasonIgnored(t *testing.T) {
	testCases := []struct {
		name    string
		payload []byte
	}{
		{
			name:    "null finish_reason",
			payload: []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":null}]}`),
		},
		{
			name:    "empty finish_reason",
			payload: []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":""}]}`),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			out := ConvertOpenAIResponseToGeminiNonStream(context.Background(), "gpt-test", nil, nil, tc.payload, nil)
			if fr := gjson.GetBytes(out, "candidates.0.finishReason"); fr.Exists() && fr.String() != "" {
				t.Fatalf("unexpected finishReason = %q on non-stream response; output=%s", fr.String(), out)
			}
		})
	}
}

func TestConvertOpenAIResponseToGeminiNonStream_PreservesToolCallThoughtSignature(t *testing.T) {
	up := []byte(`{"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[
		{"id":"c1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"},"extra_content":{"google":{"thought_signature":"SIG"}}},
		{"id":"c2","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"London\"}"}}
	]}}]}`)
	res := ConvertOpenAIResponseToGeminiNonStream(context.Background(), "", nil, nil, up, nil)
	if got := gjson.GetBytes(res, "candidates.0.content.parts.0.thoughtSignature").String(); got != "SIG" {
		t.Fatalf("candidates.0.content.parts.0.thoughtSignature = %q, want SIG; res=%s", got, string(res))
	}
	if gjson.GetBytes(res, "candidates.0.content.parts.1.thoughtSignature").Exists() {
		t.Fatalf("expected unsigned second tool call not to have thoughtSignature; res=%s", string(res))
	}
}

func TestConvertOpenAIResponseToGeminiStream_PreservesToolCallThoughtSignature(t *testing.T) {
	var param any
	ConvertOpenAIResponseToGemini(context.Background(), "google/gemini-3-pro", nil, nil, []byte(`{"choices":[{"index":0,"delta":{"tool_calls":[
		{"index":1,"id":"c2","type":"function","function":{"name":"get_weather","arguments":"{\"city\":"}},
		{"index":0,"id":"c1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":"},"extra_content":{"google":{"thought_signature":"SIG"}}}
	]}}]}`), &param)
	ConvertOpenAIResponseToGemini(context.Background(), "google/gemini-3-pro", nil, nil, []byte(`{"choices":[{"index":0,"delta":{"tool_calls":[
		{"index":1,"function":{"arguments":"\"London\"}"}},
		{"index":0,"function":{"arguments":"\"Paris\"}"}}
	]}}]}`), &param)
	out := ConvertOpenAIResponseToGemini(context.Background(), "google/gemini-3-pro", nil, nil, []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`), &param)
	if len(out) == 0 {
		t.Fatalf("stream output is empty")
	}
	last := out[len(out)-1]
	if got := gjson.GetBytes(last, "candidates.0.content.parts.0.functionCall.id").String(); got != "c1" {
		t.Fatalf("candidates.0.content.parts.0.functionCall.id = %q, want c1; payload=%s", got, string(last))
	}
	if got := gjson.GetBytes(last, "candidates.0.content.parts.0.thoughtSignature").String(); got != "SIG" {
		t.Fatalf("candidates.0.content.parts.0.thoughtSignature = %q, want SIG; payload=%s", got, string(last))
	}
	if got := gjson.GetBytes(last, "candidates.0.content.parts.1.functionCall.id").String(); got != "c2" {
		t.Fatalf("candidates.0.content.parts.1.functionCall.id = %q, want c2; payload=%s", got, string(last))
	}
	if gjson.GetBytes(last, "candidates.0.content.parts.1.thoughtSignature").Exists() {
		t.Fatalf("expected unsigned second tool call not to have thoughtSignature; payload=%s", string(last))
	}

	// Single-chunk tool call with finish_reason in the same SSE delta
	var singleChunkParam any
	singleOut := ConvertOpenAIResponseToGemini(context.Background(), "google/gemini-3-pro", nil, nil, []byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c_single","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Rome\"}"},"extra_content":{"google":{"thought_signature":"SIG_SINGLE"}}}]},"finish_reason":"tool_calls"}]}`), &singleChunkParam)
	if len(singleOut) == 0 {
		t.Fatalf("expected output for single-chunk tool_calls + finish_reason")
	}
	if got := gjson.GetBytes(singleOut[len(singleOut)-1], "candidates.0.content.parts.0.thoughtSignature").String(); got != "SIG_SINGLE" {
		t.Fatalf("single-chunk thoughtSignature = %q, want SIG_SINGLE; payload=%s", got, string(singleOut[len(singleOut)-1]))
	}

	// Reasoning/content in the same delta as tool_calls, plus signature arriving in a later delta without function
	var mixedParam any
	mixedChunk1 := ConvertOpenAIResponseToGemini(context.Background(), "google/gemini-3-pro", nil, nil, []byte(`{"choices":[{"index":0,"delta":{"reasoning_content":"checking weather","tool_calls":[{"index":0,"id":"c_mixed","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Berlin\"}"}}]}}]}`), &mixedParam)
	if len(mixedChunk1) != 1 || !gjson.GetBytes(mixedChunk1[0], "candidates.0.content.parts.0.thought").Bool() {
		t.Fatalf("expected reasoning chunk output on mixed delta, got %v", mixedChunk1)
	}
	ConvertOpenAIResponseToGemini(context.Background(), "google/gemini-3-pro", nil, nil, []byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"extra_content":{"google":{"thought_signature":"SIG_LATE"}}}]}}]}`), &mixedParam)
	mixedOut := ConvertOpenAIResponseToGemini(context.Background(), "google/gemini-3-pro", nil, nil, []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`), &mixedParam)
	if len(mixedOut) == 0 {
		t.Fatalf("expected output when flushing mixed tool call")
	}
	if got := gjson.GetBytes(mixedOut[len(mixedOut)-1], "candidates.0.content.parts.0.thoughtSignature").String(); got != "SIG_LATE" {
		t.Fatalf("mixed/late-signature thoughtSignature = %q, want SIG_LATE; payload=%s", got, string(mixedOut[len(mixedOut)-1]))
	}
}
