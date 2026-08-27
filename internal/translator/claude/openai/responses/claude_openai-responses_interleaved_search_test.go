package responses

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// Regression for a production incident: a Claude turn that interleaved thinking
// with server-side web searches used to lose the search blocks, leaving two
// adjacent reasoning items. Replaying those produced two adjacent Claude
// `thinking` blocks, which Anthropic rejects with
// "`thinking` ... blocks in the latest assistant message cannot be modified",
// killing the session mid-turn.
//
// This translator aggregates consecutive thinking blocks into one reasoning item
// and consecutive text blocks into one message item, so the round trip collapses
// the three thinking blocks into one; the server-side search pairs must still
// survive, in order.
func TestInterleavedThinkingAndSearchSurvivesRoundTrip(t *testing.T) {
	signature := mustTestSignature(t)
	thinking := func(index int, text string) [][]byte {
		prefix := `data: {"type":"content_block_`
		return [][]byte{
			[]byte(prefix + `start","index":` + strconv.Itoa(index) + `,"content_block":{"type":"thinking","thinking":""}}`),
			[]byte(prefix + `delta","index":` + strconv.Itoa(index) + `,"delta":{"type":"thinking_delta","thinking":"` + text + `"}}`),
			[]byte(prefix + `delta","index":` + strconv.Itoa(index) + `,"delta":{"type":"signature_delta","signature":"` + signature + `"}}`),
			[]byte(prefix + `stop","index":` + strconv.Itoa(index) + `}`),
		}
	}
	search := func(index int, id string) [][]byte {
		prefix := `data: {"type":"content_block_`
		return [][]byte{
			[]byte(prefix + `start","index":` + strconv.Itoa(index) + `,"content_block":{"type":"server_tool_use","id":"` + id + `","name":"web_search","input":{}}}`),
			[]byte(prefix + `delta","index":` + strconv.Itoa(index) + `,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"lindorm\"}"}}`),
			[]byte(prefix + `stop","index":` + strconv.Itoa(index) + `}`),
			[]byte(prefix + `start","index":` + strconv.Itoa(index+1) + `,"content_block":{"type":"web_search_tool_result","tool_use_id":"` + id + `","content":[{"type":"web_search_result","title":"T","url":"https://example.com/` + id + `"}]}}`),
			[]byte(prefix + `stop","index":` + strconv.Itoa(index+1) + `}`),
		}
	}

	chunks := [][]byte{[]byte(`data: {"type":"message_start","message":{"id":"msg_x","usage":{"input_tokens":1,"output_tokens":0}}}`)}
	chunks = append(chunks, thinking(0, "first")...)
	chunks = append(chunks, []byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Researching."}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`))
	chunks = append(chunks, search(2, "srvtoolu_a")...)
	chunks = append(chunks, thinking(6, "second")...)
	chunks = append(chunks, search(7, "srvtoolu_b")...)
	chunks = append(chunks, thinking(11, "third")...)
	chunks = append(chunks,
		[]byte(`data: {"type":"content_block_start","index":12,"content_block":{"type":"tool_use","id":"toolu_1","name":"exec","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":12,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"pwd\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":12}`),
		[]byte(`data: {"type":"message_stop"}`))

	outputs := translateClaudeResponsesStream(chunks)
	var completed gjson.Result
	for _, o := range outputs {
		if ev, data := parseClaudeResponsesSSEEvent(t, o); ev == "response.completed" {
			completed = data
		}
	}
	var items []json.RawMessage
	completed.Get("response.output").ForEach(func(_, v gjson.Result) bool {
		items = append(items, json.RawMessage(v.Raw))
		return true
	})
	req, _ := json.Marshal(map[string]any{"model": "claude-test", "input": items})
	out := ConvertOpenAIResponsesRequestToClaude("claude-test", req, false)

	got := claudeAssistantBlockTypes(t, out)
	want := []string{
		"thinking", "text", "server_tool_use", "web_search_tool_result",
		"server_tool_use", "web_search_tool_result",
		"tool_use",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("interleaved blocks = %v\nwant %v", got, want)
	}
}

// Regression: response.completed must interleave function calls and web search
// calls by Claude content-block index, matching the non-stream aggregation.
// The streaming path used to emit every web_search_call before every
// function_call regardless of block order, which contradicted the items' own
// output_index ordering and diverged from the non-stream output array.
func TestCompletedInterleavesFunctionCallAndWebSearchByBlockIndex(t *testing.T) {
	chunks := [][]byte{[]byte(`data: {"type":"message_start","message":{"id":"msg_x","usage":{"input_tokens":1,"output_tokens":0}}}`)}
	// function_call at block 0
	chunks = append(chunks,
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_fn","name":"exec","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"pwd\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
	)
	// web_search at block 5 (higher index, so it must come after the function call)
	chunks = append(chunks,
		[]byte(`data: {"type":"content_block_start","index":5,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":5,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"lindorm\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":5}`),
		[]byte(`data: {"type":"content_block_start","index":6,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","title":"T","url":"https://example.com/x"}]}}`),
		[]byte(`data: {"type":"content_block_stop","index":6}`),
		[]byte(`data: {"type":"message_stop"}`),
	)

	outputs := translateClaudeResponsesStream(chunks)
	var completed gjson.Result
	for _, o := range outputs {
		if ev, data := parseClaudeResponsesSSEEvent(t, o); ev == "response.completed" {
			completed = data
		}
	}
	var kinds []string
	completed.Get("response.output").ForEach(func(_, v gjson.Result) bool {
		kinds = append(kinds, v.Get("type").String())
		return true
	})
	want := []string{"function_call", "web_search_call"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("completed output types = %v, want %v", kinds, want)
	}
}
