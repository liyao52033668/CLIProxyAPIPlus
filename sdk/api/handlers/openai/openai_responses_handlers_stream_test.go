package openai

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

func newResponsesStreamTestHandler(t *testing.T) (*OpenAIResponsesAPIHandler, *httptest.ResponseRecorder, *gin.Context, http.Flusher) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	h := NewOpenAIResponsesAPIHandler(base)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatalf("expected gin writer to implement http.Flusher")
	}

	return h, recorder, c, flusher
}

func TestForwardResponsesStreamSeparatesDataOnlySSEChunks(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 2)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"arguments\":\"{}\"}}")
	data <- []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)
	body := recorder.Body.String()
	parts := strings.Split(strings.TrimSpace(body), "\n\n")
	if len(parts) != 2 {
		t.Fatalf("expected 2 SSE events, got %d. Body: %q", len(parts), body)
	}

	expectedPart1 := "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"arguments\":\"{}\"}}"
	if parts[0] != expectedPart1 {
		t.Errorf("unexpected first event.\nGot: %q\nWant: %q", parts[0], expectedPart1)
	}

	expectedPart2 := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[{\"type\":\"function_call\",\"arguments\":\"{}\"}]}}"
	if parts[1] != expectedPart2 {
		t.Errorf("unexpected second event.\nGot: %q\nWant: %q", parts[1], expectedPart2)
	}
}

func TestForwardResponsesStreamRepairsEmptyCompletedOutputFromDoneItems(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 3)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs-1","summary":[]}}`)
	data <- []byte(`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc-1","call_id":"call-1","name":"shell","arguments":"{\"cmd\":\"pwd\"}","status":"completed"}}`)
	data <- []byte(`data: {"type":"response.completed","response":{"id":"resp-1","output":[]}}`)
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	parts := strings.Split(strings.TrimSpace(recorder.Body.String()), "\n\n")
	if len(parts) != 3 {
		t.Fatalf("expected 3 SSE events, got %d. Body: %q", len(parts), recorder.Body.String())
	}

	payload := strings.TrimPrefix(parts[2], "data: ")
	output := gjson.Get(payload, "response.output")
	if !output.IsArray() || len(output.Array()) != 2 {
		t.Fatalf("expected repaired completed output with 2 items, got %s", output.Raw)
	}
	if got := gjson.Get(payload, "response.output.1.name").String(); got != "shell" {
		t.Fatalf("expected function_call name to be preserved, got %q in %s", got, payload)
	}
	if got := gjson.Get(payload, "response.output.1.arguments").String(); got != `{"cmd":"pwd"}` {
		t.Fatalf("expected function_call arguments to be preserved, got %q in %s", got, payload)
	}
}

func TestForwardResponsesStreamRepairsMixedIndexedAndUnindexedDoneItems(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 3)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte(`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc-1","call_id":"call-1","name":"shell","arguments":"{}","status":"completed"}}`)
	data <- []byte(`data: {"type":"response.output_item.done","item":{"type":"message","id":"msg-1","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`)
	data <- []byte(`data: {"type":"response.completed","response":{"id":"resp-1","output":[]}}`)
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	parts := strings.Split(strings.TrimSpace(recorder.Body.String()), "\n\n")
	if len(parts) != 3 {
		t.Fatalf("expected 3 SSE events, got %d. Body: %q", len(parts), recorder.Body.String())
	}

	payload := strings.TrimPrefix(parts[2], "data: ")
	output := gjson.Get(payload, "response.output")
	if !output.IsArray() || len(output.Array()) != 2 {
		t.Fatalf("expected repaired completed output with 2 items, got %s", output.Raw)
	}
	if got := gjson.Get(payload, "response.output.0.name").String(); got != "shell" {
		t.Fatalf("expected indexed function_call to be preserved first, got %q in %s", got, payload)
	}
	if got := gjson.Get(payload, "response.output.1.id").String(); got != "msg-1" {
		t.Fatalf("expected unindexed message to be appended, got %q in %s", got, payload)
	}
}

func TestForwardResponsesStreamRepairsMultilineCompletedOutputAsSSEDataLines(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 2)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","arguments":"{}"}}`)
	data <- []byte("data: {\"type\":\"response.completed\",\ndata: \"response\":{\"id\":\"resp-1\",\"output\":[]}}\n\n")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	parts := strings.Split(strings.TrimSpace(recorder.Body.String()), "\n\n")
	if len(parts) != 2 {
		t.Fatalf("expected 2 SSE events, got %d. Body: %q", len(parts), recorder.Body.String())
	}

	completedFrame := []byte(parts[1])
	for _, line := range strings.Split(parts[1], "\n") {
		if line != "" && !strings.HasPrefix(line, "data: ") {
			t.Fatalf("expected every completed payload line to be an SSE data line, got %q in %q", line, parts[1])
		}
	}

	payload, ok := responsesSSEDataPayload(completedFrame)
	if !ok {
		t.Fatalf("expected completed frame to contain data payload: %q", parts[1])
	}
	output := gjson.GetBytes(payload, "response.output")
	if !output.IsArray() || len(output.Array()) != 1 {
		t.Fatalf("expected repaired completed output with 1 item, got %s from %q", output.Raw, payload)
	}
}

func TestForwardResponsesStreamReassemblesSplitSSEEventChunks(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 3)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("event: response.created")
	data <- []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}")
	data <- []byte("\n")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	got := strings.TrimSuffix(recorder.Body.String(), "\n")
	want := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n"
	if got != want {
		t.Fatalf("unexpected split-event framing.\nGot:  %q\nWant: %q", got, want)
	}
}

func TestForwardResponsesStreamPreservesValidFullSSEEventChunks(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	chunk := []byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n")
	data <- chunk
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	got := strings.TrimSuffix(recorder.Body.String(), "\n")
	if got != string(chunk) {
		t.Fatalf("unexpected full-event framing.\nGot:  %q\nWant: %q", got, string(chunk))
	}
}

func TestForwardResponsesStreamBuffersSplitDataPayloadChunks(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 2)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("data: {\"type\":\"response.created\"")
	data <- []byte(",\"response\":{\"id\":\"resp-1\"}}")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	got := recorder.Body.String()
	want := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n\n"
	if got != want {
		t.Fatalf("unexpected split-data framing.\nGot:  %q\nWant: %q", got, want)
	}
}

func TestResponsesSSENeedsLineBreakSkipsChunksThatAlreadyStartWithNewline(t *testing.T) {
	if responsesSSENeedsLineBreak([]byte("event: response.created"), []byte("\n")) {
		t.Fatal("expected no injected newline before newline-only chunk")
	}
	if responsesSSENeedsLineBreak([]byte("event: response.created"), []byte("\r\n")) {
		t.Fatal("expected no injected newline before CRLF chunk")
	}
}

func TestForwardResponsesStreamDropsIncompleteTrailingDataChunkOnFlush(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("data: {\"type\":\"response.created\"")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	if got := recorder.Body.String(); got != "\n" {
		t.Fatalf("expected incomplete trailing data to be dropped on flush.\nGot: %q", got)
	}
}

func TestResponsesSSEFramer_FiltersUpstreamPrivateEvents(t *testing.T) {
	var output bytes.Buffer
	framer := &responsesSSEFramer{}

	rateLimitsFrame := []byte("event: codex.rate_limits\ndata: {\"type\":\"codex.rate_limits\",\"rate_limits\":{\"primary\":{\"used_percent\":42}}}\n\n")
	framer.WriteChunk(&output, rateLimitsFrame)

	metadataFrame := []byte("event: codex.response.metadata\ndata: {\"type\":\"codex.response.metadata\",\"headers\":{\"x-turn-state\":\"turn-1\"}}\n\n")
	framer.WriteChunk(&output, metadataFrame)

	createdFrame := []byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n")
	framer.WriteChunk(&output, createdFrame)

	timingFrame := []byte("event: responsesapi.websocket_timing\ndata: {\"type\":\"responsesapi.websocket_timing\",\"timing\":{\"duration_ms\":100}}\n\n")
	framer.WriteChunk(&output, timingFrame)

	completedFrame := []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\"}}\n\n")
	framer.WriteChunk(&output, completedFrame)

	got := output.String()
	if strings.Contains(got, "codex.rate_limits") {
		t.Fatalf("expected codex.rate_limits to be stripped, got: %q", got)
	}
	if strings.Contains(got, "codex.response.metadata") {
		t.Fatalf("expected codex.response.metadata to be stripped, got: %q", got)
	}
	if strings.Contains(got, "responsesapi.websocket_timing") {
		t.Fatalf("expected responsesapi.websocket_timing to be stripped, got: %q", got)
	}
	if !strings.Contains(got, "event: response.created") {
		t.Fatalf("expected response.created in output, got: %q", got)
	}
	if !strings.Contains(got, "event: response.completed") {
		t.Fatalf("expected response.completed in output, got: %q", got)
	}
}

func TestForwardResponsesStream_FiltersUpstreamPrivateEvents(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 5)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("event: codex.rate_limits\ndata: {\"type\":\"codex.rate_limits\",\"rate_limits\":{\"primary\":{\"used_percent\":42}}}\n\n")
	data <- []byte("event: codex.response.metadata\ndata: {\"type\":\"codex.response.metadata\",\"headers\":{\"x-turn-state\":\"turn-1\"}}\n\n")
	data <- []byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n")
	data <- []byte("event: responsesapi.websocket_timing\ndata: {\"type\":\"responsesapi.websocket_timing\",\"timing\":{\"duration_ms\":100}}\n\n")
	data <- []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\"}}\n\n")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	got := recorder.Body.String()
	if strings.Contains(got, "codex.rate_limits") {
		t.Fatalf("expected codex.rate_limits to be stripped in stream, got: %q", got)
	}
	if strings.Contains(got, "codex.response.metadata") {
		t.Fatalf("expected codex.response.metadata to be stripped in stream, got: %q", got)
	}
	if strings.Contains(got, "responsesapi.websocket_timing") {
		t.Fatalf("expected responsesapi.websocket_timing to be stripped in stream, got: %q", got)
	}
	if !strings.Contains(got, "event: response.created") {
		t.Fatalf("expected response.created in stream output, got: %q", got)
	}
	if !strings.Contains(got, "event: response.completed") {
		t.Fatalf("expected response.completed in stream output, got: %q", got)
	}
}

func TestResponsesSSEFramer_PreservesMetadataForCodexClient(t *testing.T) {
	var output bytes.Buffer
	framer := &responsesSSEFramer{isCodexClient: true}

	metadataFrame := []byte("event: codex.response.metadata\ndata: {\"type\":\"codex.response.metadata\",\"headers\":{\"x-turn-state\":\"turn-1\"}}\n\n")
	framer.WriteChunk(&output, metadataFrame)

	createdFrame := []byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n")
	framer.WriteChunk(&output, createdFrame)

	completedFrame := []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\"}}\n\n")
	framer.WriteChunk(&output, completedFrame)

	got := output.String()
	if !strings.Contains(got, "codex.response.metadata") {
		t.Fatalf("expected codex.response.metadata to be preserved for official Codex client, got: %q", got)
	}
	if !strings.Contains(got, "event: response.created") {
		t.Fatalf("expected response.created in output, got: %q", got)
	}
	if !strings.Contains(got, "event: response.completed") {
		t.Fatalf("expected response.completed in output, got: %q", got)
	}
}

func TestResponsesSSEFramer_FiltersDataOnlyPrivateEventsAndNonJSON(t *testing.T) {
	var output bytes.Buffer
	framer := &responsesSSEFramer{}

	// Data-only frame with private type
	dataOnlyPrivateFrame := []byte("data: {\"type\":\"codex.rate_limits\",\"rate_limits\":{\"primary\":{\"used_percent\":42}}}\n\n")
	framer.WriteChunk(&output, dataOnlyPrivateFrame)

	// Non-JSON payload with private event line
	nonJSONPrivateFrame := []byte("event: codex.rate_limits\ndata: not-json-data\n\n")
	framer.WriteChunk(&output, nonJSONPrivateFrame)

	// Event-only frame without data
	eventOnlyPrivateFrame := []byte("event: codex.response.metadata\n\n")
	framer.WriteChunk(&output, eventOnlyPrivateFrame)

	// Standard response.created
	createdFrame := []byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n")
	framer.WriteChunk(&output, createdFrame)

	// Standard response.completed
	completedFrame := []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\"}}\n\n")
	framer.WriteChunk(&output, completedFrame)

	got := output.String()
	if strings.Contains(got, "codex.rate_limits") {
		t.Fatalf("expected codex.rate_limits to be stripped, got: %q", got)
	}
	if strings.Contains(got, "not-json-data") {
		t.Fatalf("expected not-json-data to be stripped, got: %q", got)
	}
	if strings.Contains(got, "codex.response.metadata") {
		t.Fatalf("expected codex.response.metadata to be stripped, got: %q", got)
	}
	if !strings.Contains(got, "event: response.created") {
		t.Fatalf("expected response.created in output, got: %q", got)
	}
	if !strings.Contains(got, "event: response.completed") {
		t.Fatalf("expected response.completed in output, got: %q", got)
	}
}

func TestResponsesSSEFramer_CodexClientFiltersRateLimitsAndTiming(t *testing.T) {
	var output bytes.Buffer
	framer := &responsesSSEFramer{isCodexClient: true}

	rateLimitsFrame := []byte("event: codex.rate_limits\ndata: {\"type\":\"codex.rate_limits\",\"rate_limits\":{\"primary\":{\"used_percent\":42}}}\n\n")
	framer.WriteChunk(&output, rateLimitsFrame)

	metadataFrame := []byte("event: codex.response.metadata\ndata: {\"type\":\"codex.response.metadata\",\"headers\":{\"x-turn-state\":\"turn-1\"}}\n\n")
	framer.WriteChunk(&output, metadataFrame)

	timingFrame := []byte("event: responsesapi.websocket_timing\ndata: {\"type\":\"responsesapi.websocket_timing\",\"timing\":{\"duration_ms\":100}}\n\n")
	framer.WriteChunk(&output, timingFrame)

	createdFrame := []byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n")
	framer.WriteChunk(&output, createdFrame)

	completedFrame := []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\"}}\n\n")
	framer.WriteChunk(&output, completedFrame)

	got := output.String()
	if strings.Contains(got, "codex.rate_limits") {
		t.Fatalf("expected codex.rate_limits to be stripped for Codex client, got: %q", got)
	}
	if strings.Contains(got, "responsesapi.websocket_timing") {
		t.Fatalf("expected responsesapi.websocket_timing to be stripped for Codex client, got: %q", got)
	}
	if !strings.Contains(got, "codex.response.metadata") {
		t.Fatalf("expected codex.response.metadata to be preserved for official Codex client, got: %q", got)
	}
	if !strings.Contains(got, "event: response.created") {
		t.Fatalf("expected response.created in output, got: %q", got)
	}
	if !strings.Contains(got, "event: response.completed") {
		t.Fatalf("expected response.completed in output, got: %q", got)
	}
}
