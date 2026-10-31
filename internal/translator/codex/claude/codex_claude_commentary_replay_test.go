package claude

import (
	"context"
	"strings"
	"testing"

	sigcompat "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/tidwall/gjson"
)

// The internal commentary marker is an implementation contract between the
// Codex-to-Claude response translator and the Claude-to-Codex request
// translator. It must never be forwarded to an upstream as a signature.
const testCommentaryMarkerSignature = codexCommentarySignatureMarker

func TestConvertCodexResponseToClaudeNonStream_CommentaryCarriesInternalMarker(t *testing.T) {
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5","output":[
		{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"COMMENTARY_SENTINEL"}]},
		{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"FINAL_SENTINEL"}]}
	],"usage":{"input_tokens":1,"output_tokens":1}}}`)
	var param any
	out := ConvertCodexResponseToClaudeNonStream(context.Background(), "", []byte(`{"messages":[]}`), nil, raw, &param)
	content := gjson.GetBytes(out, "content").Array()
	if len(content) != 2 {
		t.Fatalf("expected 2 content blocks, got %s", out)
	}
	if content[0].Get("type").String() != "thinking" || content[0].Get("thinking").String() != "COMMENTARY_SENTINEL" {
		t.Fatalf("commentary should be a thinking block, got %s", content[0].Raw)
	}
	if got := content[0].Get("signature").String(); got != testCommentaryMarkerSignature {
		t.Fatalf("commentary thinking block signature = %q, want internal marker %q", got, testCommentaryMarkerSignature)
	}
	if content[1].Get("type").String() != "text" || content[1].Get("text").String() != "FINAL_SENTINEL" {
		t.Fatalf("final_answer should be a text block, got %s", content[1].Raw)
	}
}

type codexClaudeStreamBlocks struct {
	thinking   []string
	signatures []string
	text       []string
}

func collectCodexClaudeBlocks(t *testing.T, chunks [][]byte) codexClaudeStreamBlocks {
	t.Helper()
	var param any
	var got codexClaudeStreamBlocks
	for _, chunk := range chunks {
		for _, event := range ConvertCodexResponseToClaude(context.Background(), "", []byte(`{"messages":[]}`), nil, chunk, &param) {
			// One returned []byte may hold several concatenated SSE events; walk
			// every data line so nothing is missed.
			for _, line := range strings.Split(string(event), "\n") {
				trimmed := strings.TrimSpace(line)
				if !strings.HasPrefix(trimmed, "data:") {
					continue
				}
				dataJSON := []byte(strings.TrimSpace(trimmed[len("data:"):]))
				if len(dataJSON) == 0 {
					continue
				}
				deltaType := gjson.GetBytes(dataJSON, "delta.type").String()
				switch deltaType {
				case "thinking_delta":
					if txt := gjson.GetBytes(dataJSON, "delta.thinking").String(); txt != "" {
						got.thinking = append(got.thinking, txt)
					}
				case "signature_delta":
					if sig := gjson.GetBytes(dataJSON, "delta.signature").String(); sig != "" {
						got.signatures = append(got.signatures, sig)
					}
				case "text_delta":
					if txt := gjson.GetBytes(dataJSON, "delta.text").String(); txt != "" {
						got.text = append(got.text, txt)
					}
				}
			}
		}
	}
	return got
}

func TestConvertCodexResponseToClaude_StreamCommentaryCarriesInternalMarker(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"message","role":"assistant","phase":"commentary"}}`),
		[]byte(`data: {"type":"response.content_part.added","part":{"type":"output_text"}}`),
		[]byte(`data: {"type":"response.output_text.delta","delta":"COMMENTARY_SENTINEL"}`),
		[]byte(`data: {"type":"response.content_part.done","part":{"type":"output_text","text":"COMMENTARY_SENTINEL"}}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"COMMENTARY_SENTINEL"}]}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"message","role":"assistant","phase":"final_answer"}}`),
		[]byte(`data: {"type":"response.output_text.delta","delta":"FINAL_SENTINEL"}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"FINAL_SENTINEL"}]}}`),
		[]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}`),
	}
	got := collectCodexClaudeBlocks(t, chunks)
	if len(got.thinking) != 1 || got.thinking[0] != "COMMENTARY_SENTINEL" {
		t.Fatalf("commentary should be one thinking block, got %q", got.thinking)
	}
	if len(got.signatures) != 1 || got.signatures[0] != testCommentaryMarkerSignature {
		t.Fatalf("commentary should carry exactly the internal marker signature, got %v", got.signatures)
	}
	if len(got.text) != 1 || got.text[0] != "FINAL_SENTINEL" {
		t.Fatalf("final_answer should be the only text block, got %q", got.text)
	}
}

func TestConvertClaudeRequestToCodex_CommentaryMarkerRestoresCommentaryMessage(t *testing.T) {
	payload := []byte(`{"model":"gpt-6.1-sol","max_tokens":128,"messages":[
		{"role":"user","content":"First turn"},
		{"role":"assistant","content":[
			{"type":"thinking","thinking":"COMMENTARY_SENTINEL","signature":"` + testCommentaryMarkerSignature + `"},
			{"type":"text","text":"FINAL_SENTINEL"}
		]},
		{"role":"user","content":"Next turn"}
	]}`)

	for _, stream := range []bool{false, true} {
		result := ConvertClaudeRequestToCodex("gpt-6.1-sol", payload, stream)
		if strings.Contains(string(result), testCommentaryMarkerSignature) {
			t.Fatalf("stream=%v: internal marker leaked into upstream request: %s", stream, result)
		}
		inputs := gjson.GetBytes(result, "input").Array()
		if len(inputs) != 4 {
			t.Fatalf("stream=%v: got %d input items, want 4: %s", stream, len(inputs), result)
		}
		commentary := inputs[1]
		if commentary.Get("type").String() != "message" || commentary.Get("role").String() != "assistant" || commentary.Get("phase").String() != "commentary" {
			t.Fatalf("stream=%v: second input should be an assistant commentary message, got %s", stream, commentary.Raw)
		}
		if got := commentary.Get("content.0.type").String(); got != "output_text" {
			t.Fatalf("stream=%v: commentary content type = %q, want output_text", stream, got)
		}
		if got := commentary.Get("content.0.text").String(); got != "COMMENTARY_SENTINEL" {
			t.Fatalf("stream=%v: commentary text = %q, want COMMENTARY_SENTINEL", stream, got)
		}
		final := inputs[2]
		if final.Get("type").String() != "message" || final.Get("role").String() != "assistant" || final.Get("phase").Exists() {
			t.Fatalf("stream=%v: third input should be a plain assistant message, got %s", stream, final.Raw)
		}
		if got := final.Get("content.0.text").String(); got != "FINAL_SENTINEL" {
			t.Fatalf("stream=%v: final text = %q, want FINAL_SENTINEL", stream, got)
		}
		for _, item := range inputs {
			if item.Get("type").String() == "reasoning" {
				t.Fatalf("stream=%v: commentary must not become a reasoning item: %s", stream, result)
			}
		}
	}
}

func TestRestoreCodexCommentaryMarkersRewritesMarkedThinkingBlocks(t *testing.T) {
	payload := []byte(`{"messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":[
			{"type":"thinking","thinking":"COMMENTARY_SENTINEL","signature":"` + testCommentaryMarkerSignature + `"},
			{"type":"text","text":"FINAL_SENTINEL"}
		]},
		{"role":"assistant","content":[
			{"type":"thinking","thinking":"REAL_REASONING","signature":"real-signature"}
		]}
	]}`)

	restored, count, _ := sigcompat.RestoreCodexCommentaryMarkers(payload)
	if count != 1 {
		t.Fatalf("restored count = %d, want 1", count)
	}
	messages := gjson.GetBytes(restored, "messages").Array()
	assistant := messages[1]
	if got := assistant.Get("content.0.type").String(); got != "text" {
		t.Fatalf("marked thinking block type = %q, want text", got)
	}
	if got := assistant.Get("content.0.text").String(); got != "COMMENTARY_SENTINEL" {
		t.Fatalf("restored text = %q, want COMMENTARY_SENTINEL", got)
	}
	if got := assistant.Get("content.1.text").String(); got != "FINAL_SENTINEL" {
		t.Fatalf("following text = %q, want FINAL_SENTINEL", got)
	}
	if strings.Contains(string(restored), testCommentaryMarkerSignature) {
		t.Fatalf("marker survived the restore: %s", restored)
	}
	other := messages[2]
	if other.Get("content.0.type").String() != "thinking" || other.Get("content.0.signature").String() != "real-signature" {
		t.Fatalf("real reasoning block must stay untouched: %s", other.Raw)
	}

	// No marker: payload returned unchanged.
	unchanged, count, _ := sigcompat.RestoreCodexCommentaryMarkers([]byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"x","signature":"sig"}]}]}`))
	if count != 0 || strings.Contains(string(unchanged), `"type":"text"`) {
		t.Fatalf("payload without markers must be returned unchanged: %s", unchanged)
	}
}
