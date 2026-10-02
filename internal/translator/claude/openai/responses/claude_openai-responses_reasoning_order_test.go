package responses

import (
	"encoding/base64"
	"fmt"
	"testing"

	sigcompat "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
)

// testClaudeResponsesThinkingSignatureForModel builds a valid Claude thinking
// signature whose embedded model name makes signatures from different calls
// distinguishable.
func testClaudeResponsesThinkingSignatureForModel(t *testing.T, model string) (string, string) {
	t.Helper()
	channelBlock := []byte{}
	channelBlock = protowire.AppendTag(channelBlock, 1, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 12)
	channelBlock = protowire.AppendTag(channelBlock, 2, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 2)
	channelBlock = protowire.AppendTag(channelBlock, 6, protowire.BytesType)
	channelBlock = protowire.AppendString(channelBlock, model)

	container := []byte{}
	container = protowire.AppendTag(container, 1, protowire.BytesType)
	container = protowire.AppendBytes(container, channelBlock)

	payload := []byte{}
	payload = protowire.AppendTag(payload, 2, protowire.BytesType)
	payload = protowire.AppendBytes(payload, container)
	payload = protowire.AppendTag(payload, 3, protowire.VarintType)
	payload = protowire.AppendVarint(payload, 1)

	rawSignature := base64.StdEncoding.EncodeToString(payload)
	normalized, ok := sigcompat.CompatibleSignatureForProvider(sigcompat.SignatureProviderClaude, rawSignature)
	if !ok {
		t.Fatal("test Claude signature should be compatible")
	}
	return rawSignature, normalized
}

// responsesReasoningItem renders a Responses reasoning item carrying a Claude
// signature and one summary text part.
func responsesReasoningItem(signature, text string) string {
	return fmt.Sprintf(`{"type":"reasoning","encrypted_content":%q,"summary":[{"type":"summary_text","text":%q}]}`, signature, text)
}

func responsesFunctionCallItem(callID, name string) string {
	return fmt.Sprintf(`{"type":"function_call","call_id":%q,"name":%q,"arguments":"{}"}`, callID, name)
}

func responsesFunctionCallOutputItem(callID, output string) string {
	return fmt.Sprintf(`{"type":"function_call_output","call_id":%q,"output":%q}`, callID, output)
}

// responsesWebSearchCallItem renders a completed Responses web_search_call item.
func responsesWebSearchCallItem(id, query string) string {
	return fmt.Sprintf(`{"type":"web_search_call","id":%q,"status":"completed","action":{"type":"search","query":%q}}`, id, query)
}

// Fork note: unlike upstream, this translator appends assistant parts in input
// order without reordering tool_use parts to the end of the message, so the
// "function_call then web_search_call" history does not glue a tool_use onto a
// web_search_tool_result and needs no separator. Only a tool_use that follows a
// web_search_tool_result triggers the replay.
func TestConvertOpenAIResponsesRequestToClaude_WebSearchSeparatesToolUseWithThinking(t *testing.T) {
	rawSig, signature := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-test")

	t.Run("function_call then web_search_call", func(t *testing.T) {
		raw := responsesRequestFromItems(
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}`,
			responsesReasoningItem(rawSig, "I should search and then run a command."),
			`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Searching, then running."}]}`,
			responsesFunctionCallItem("call_00_abc", "exec_command"),
			responsesWebSearchCallItem("ws_srvtoolu_12_x", "hello world"),
			responsesFunctionCallOutputItem("call_00_abc", "hi"),
		)

		out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
		content := gjson.GetBytes(out, "messages.1.content").Array()
		// The tool_use precedes the web search pair in input order, so no
		// separator is inserted.
		wantTypes := []string{"thinking", "text", "tool_use", "server_tool_use", "web_search_tool_result"}
		if len(content) != len(wantTypes) {
			t.Fatalf("assistant content count = %d, want %d. Output: %s", len(content), len(wantTypes), out)
		}
		for index, wantType := range wantTypes {
			if got := content[index].Get("type").String(); got != wantType {
				t.Fatalf("assistant content[%d].type = %q, want %q. Output: %s", index, got, wantType, out)
			}
		}
		if got := content[0].Get("signature").String(); got != signature {
			t.Fatalf("first thinking signature = %q, want %q", got, signature)
		}
		if got := content[2].Get("id").String(); got != "call_00_abc" {
			t.Fatalf("tool_use id = %q, want call_00_abc", got)
		}
	})

	t.Run("web_search_call then function_call", func(t *testing.T) {
		raw := responsesRequestFromItems(
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}`,
			responsesReasoningItem(rawSig, "I should search and then run a command."),
			`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Searching, then running."}]}`,
			responsesWebSearchCallItem("ws_srvtoolu_12_x", "hello world"),
			responsesFunctionCallItem("call_00_abc", "exec_command"),
			responsesFunctionCallOutputItem("call_00_abc", "hi"),
		)

		out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
		content := gjson.GetBytes(out, "messages.1.content").Array()
		wantTypes := []string{"thinking", "text", "server_tool_use", "web_search_tool_result", "thinking", "tool_use"}
		if len(content) != len(wantTypes) {
			t.Fatalf("assistant content count = %d, want %d. Output: %s", len(content), len(wantTypes), out)
		}
		for index, wantType := range wantTypes {
			if got := content[index].Get("type").String(); got != wantType {
				t.Fatalf("assistant content[%d].type = %q, want %q. Output: %s", index, got, wantType, out)
			}
		}
		if got := content[4].Get("signature").String(); got != signature {
			t.Fatalf("second thinking signature = %q, want %q", got, signature)
		}
		if got := content[4].Get("thinking").String(); got != "I should search and then run a command." {
			t.Fatalf("second thinking text = %q, want original thinking text", got)
		}
		if got := content[5].Get("id").String(); got != "call_00_abc" {
			t.Fatalf("tool_use id = %q, want call_00_abc", got)
		}
	})

	t.Run("no thinking item present", func(t *testing.T) {
		raw := responsesRequestFromItems(
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}`,
			`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Searching, then running."}]}`,
			responsesWebSearchCallItem("ws_srvtoolu_12_x", "hello world"),
			responsesFunctionCallItem("call_00_abc", "exec_command"),
			responsesFunctionCallOutputItem("call_00_abc", "hi"),
		)

		out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
		content := gjson.GetBytes(out, "messages.1.content").Array()
		wantTypes := []string{"text", "server_tool_use", "web_search_tool_result", "tool_use"}
		if len(content) != len(wantTypes) {
			t.Fatalf("assistant content count = %d, want %d. Output: %s", len(content), len(wantTypes), out)
		}
		for index, wantType := range wantTypes {
			if got := content[index].Get("type").String(); got != wantType {
				t.Fatalf("assistant content[%d].type = %q, want %q. Output: %s", index, got, wantType, out)
			}
		}
	})

	t.Run("selects latest thinking block when multiple exist", func(t *testing.T) {
		firstRaw, _ := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-first")
		secondRaw, secondSignature := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-second")

		raw := responsesRequestFromItems(
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}`,
			responsesReasoningItem(firstRaw, "first reasoning"),
			`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"thought once"}]}`,
			responsesReasoningItem(secondRaw, "second reasoning"),
			responsesWebSearchCallItem("ws_srvtoolu_12_x", "hello world"),
			responsesFunctionCallItem("call_00_abc", "exec_command"),
			responsesFunctionCallOutputItem("call_00_abc", "hi"),
		)

		out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
		content := gjson.GetBytes(out, "messages.1.content").Array()
		wantTypes := []string{"thinking", "text", "thinking", "server_tool_use", "web_search_tool_result", "thinking", "tool_use"}
		if len(content) != len(wantTypes) {
			t.Fatalf("assistant content count = %d, want %d. Output: %s", len(content), len(wantTypes), out)
		}
		for index, wantType := range wantTypes {
			if got := content[index].Get("type").String(); got != wantType {
				t.Fatalf("assistant content[%d].type = %q, want %q. Output: %s", index, got, wantType, out)
			}
		}
		if got := content[5].Get("signature").String(); got != secondSignature {
			t.Fatalf("separated thinking signature = %q, want latest %q", got, secondSignature)
		}
		if got := content[5].Get("thinking").String(); got != "second reasoning" {
			t.Fatalf("separated thinking text = %q, want latest 'second reasoning'", got)
		}
	})
}
