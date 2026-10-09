package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (e *XAIExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if opts.Alt == "responses/compact" {
		return e.executeCompact(ctx, auth, req, opts)
	}
	if endpointPath := xaiImageEndpointPath(opts); endpointPath != "" {
		return e.executeImages(ctx, auth, req, endpointPath)
	}
	if xaiIsVideoRequest(opts) {
		return e.executeVideos(ctx, auth, req, opts)
	}
	if xaiIsSpeechRequest(opts) {
		return e.executeSpeech(ctx, auth, req, opts)
	}

	token, _ := xaiCreds(auth)
	baseURL := xaiChatBaseURL(auth)
	logXAIResolvedBaseURL(ctx, baseURL)

	prepared, err := e.prepareResponsesRequest(ctx, req, opts, true)
	if err != nil {
		return resp, err
	}

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), prepared.baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	url := helps.JoinBaseURL(baseURL, "/responses")
	headers := make(http.Header)
	tmpReq := &http.Request{Header: headers}
	applyXAIChatHeaders(tmpReq, auth, token, true, prepared.sessionID)
	headers = tmpReq.Header
	e.recordXAIRequest(ctx, auth, url, headers.Clone(), prepared.body)

	_, data, respHeaders, errDo := helps.DoJSON(ctx, e.cfg, helps.UpstreamRequest{
		Provider:       e.Identifier(),
		Auth:           auth,
		Method:         http.MethodPost,
		URL:            url,
		Headers:        headers,
		Body:           prepared.body,
		SkipRequestLog: true,
	})
	if errDo != nil {
		if ue, ok := errDo.(helps.UpstreamStatusError); ok {
			return resp, xaiStatusErr(ue.Code, []byte(ue.Msg))
		}
		return resp, errDo
	}
	_ = respHeaders

	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	responseFilter := newXAIInternalXSearchResponseFilter(prepared.filterInternalXSearch, prepared.clientDeclaredTools)
	for _, line := range bytes.Split(data, []byte("\n")) {
		if !bytes.HasPrefix(line, xaiDataTag) {
			continue
		}
		eventData := xaiNormalizeReasoningSummaryData(bytes.TrimSpace(line[len(xaiDataTag):]))
		eventData = restoreXAINamespaceToolCalls(eventData, prepared.namespaceTools)
		if prepared.webSearchAlias != "" {
			eventData = restoreXAIClientWebSearchName(eventData, prepared.webSearchAlias)
		}
		eventData = responseFilter.apply(eventData)
		if len(eventData) == 0 {
			continue
		}
		reporter.ObserveResponseModel(eventData)
		switch gjson.GetBytes(eventData, "type").String() {
		case "response.output_item.done":
			xaiCollectOutputItemDone(eventData, outputItemsByIndex, &outputItemsFallback)
		case "response.completed":
			if detail, ok := helps.ParseCodexUsage(eventData); ok {
				reporter.Publish(ctx, detail)
			}
			reporter.EnsurePublished(ctx)
			completedData := xaiPatchCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
			completedData = xaiNormalizeReasoningSummaryData(completedData)
			cacheXAIReasoningReplayFromCompleted(ctx, prepared.replayScope, completedData)
			var param any
			out := sdktranslator.TranslateNonStream(ctx, prepared.to, prepared.responseFormat, req.Model, prepared.originalPayload, prepared.body, completedData, &param)
			return cliproxyexecutor.Response{Payload: out, Headers: respHeaders}, nil
		}
	}

	return resp, statusErr{code: http.StatusRequestTimeout, msg: "xai stream error: stream disconnected before response.completed"}
}

func (e *XAIExecutor) executeCompact(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	prepared, data, headers, errCompact := e.executeCompactRequest(ctx, auth, req, opts)
	if errCompact != nil {
		return resp, errCompact
	}

	var param any
	out := sdktranslator.TranslateNonStream(ctx, prepared.to, prepared.responseFormat, req.Model, prepared.originalPayload, prepared.body, data, &param)
	return cliproxyexecutor.Response{Payload: out, Headers: headers}, nil
}

func (e *XAIExecutor) executeCompactRequest(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*xaiPreparedRequest, []byte, http.Header, error) {
	token, _ := xaiCreds(auth)
	// Compact must not use xaiChatBaseURL: CLI chat-proxy returns 404 for
	// /responses/compact and a 404 cools down the whole xAI auth pool.
	// Local Plus keeps xaiOfficialOrExplicitBaseURL semantics for empty/default/proxy.
	baseURL := xaiCompactBaseURL(auth)
	logXAIResolvedBaseURL(ctx, baseURL)

	prepared, err := e.prepareResponsesRequestTo(ctx, req, opts, false, sdktranslator.FormatOpenAIResponse)
	if err != nil {
		return nil, nil, nil, err
	}
	prepared.body, _ = sjson.DeleteBytes(prepared.body, "stream")
	prepared.body, _ = sjson.DeleteBytes(prepared.body, "tools")
	prepared.body = xaiRemoveInputItemsByType(prepared.body, "compaction_trigger")
	if previousResponseID := strings.TrimSpace(gjson.GetBytes(req.Payload, "previous_response_id").String()); previousResponseID != "" {
		prepared.body, _ = sjson.SetBytes(prepared.body, "previous_response_id", previousResponseID)
	}

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), prepared.baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	requestURL := helps.JoinBaseURL(baseURL, "/responses/compact")
	headers := make(http.Header)
	tmpReq := &http.Request{Header: headers}
	// Official API / custom compact endpoints use standard API headers, not CLI
	// chat-proxy identity headers (which applyXAIChatHeaders may still attach for OAuth chat).
	applyXAIHeaders(tmpReq, auth, token, false, prepared.sessionID)
	headers = tmpReq.Header
	e.recordXAIRequest(ctx, auth, requestURL, headers.Clone(), prepared.body)

	_, data, respHeaders, errDo := helps.DoJSON(ctx, e.cfg, helps.UpstreamRequest{
		Provider:       e.Identifier(),
		Auth:           auth,
		Method:         http.MethodPost,
		URL:            requestURL,
		Headers:        headers,
		Body:           prepared.body,
		SkipRequestLog: true,
	})
	if errDo != nil {
		if ue, ok := errDo.(helps.UpstreamStatusError); ok {
			helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", ue.Code, helps.SummarizeErrorBody("application/json", []byte(ue.Msg)))
			return nil, nil, nil, statusErr{code: ue.Code, msg: ue.Msg}
		}
		return nil, nil, nil, errDo
	}

	reporter.ObserveResponseModel(data)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(data))
	reporter.EnsurePublished(ctx)
	clearXAIReasoningReplayAfterCompaction(ctx, prepared.replayScope)
	return prepared, data, respHeaders, nil
}

func (e *XAIExecutor) executeCompactionTriggerStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	prepared, data, headers, err := e.executeCompactRequest(ctx, auth, req, opts)
	if err != nil {
		return nil, err
	}

	headers = headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Content-Type", "text/event-stream")

	chunks := xaiBuildCompactionTriggerStreamChunks(prepared, data)
	out := make(chan cliproxyexecutor.StreamChunk, len(chunks))
	for _, chunk := range chunks {
		out <- cliproxyexecutor.StreamChunk{Payload: chunk}
	}
	close(out)
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}, nil
}

func xaiInputHasItemType(body []byte, itemType string) bool {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		if item.Get("type").String() == itemType {
			return true
		}
	}
	return false
}

func validateXAIToolOutputsHavePriorCalls(body []byte, hasPreviousResponseID bool) error {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return nil
	}
	seenCalls := map[string]struct{}{}
	for _, item := range input.Array() {
		itemType := item.Get("type").String()
		callID := strings.TrimSpace(item.Get("call_id").String())
		if callID == "" {
			continue
		}
		switch itemType {
		case "function_call", "custom_tool_call":
			seenCalls[callID] = struct{}{}
		case "function_call_output", "custom_tool_call_output":
			if _, ok := seenCalls[callID]; !ok && !hasPreviousResponseID {
				return fmt.Errorf("xai executor: tool output call_id %q has no prior tool call", callID)
			}
		}
	}
	return nil
}

func xaiRemoveInputItemsByType(body []byte, itemType string) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}

	var buf bytes.Buffer
	buf.WriteByte('[')
	kept := 0
	for _, item := range input.Array() {
		if item.Get("type").String() == itemType {
			continue
		}
		if kept > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(item.Raw)
		kept++
	}
	buf.WriteByte(']')

	updated, err := sjson.SetRawBytes(body, "input", buf.Bytes())
	if err != nil {
		return body
	}
	return updated
}

func xaiBuildCompactionTriggerStreamChunks(prepared *xaiPreparedRequest, compactData []byte) [][]byte {
	responseID := xaiCompactionResponseID(compactData)
	now := time.Now().Unix()
	createdAt := gjson.GetBytes(compactData, "created_at").Int()
	if createdAt == 0 {
		createdAt = now
	}
	completedAt := gjson.GetBytes(compactData, "completed_at").Int()
	if completedAt == 0 {
		completedAt = now
	}

	item := xaiCompactionOutputItem(compactData, responseID)
	output := make([]byte, 0, len(item)+2)
	output = append(output, '[')
	output = append(output, item...)
	output = append(output, ']')

	createdResponse := xaiBuildCompactionBaseResponse(prepared, compactData, responseID, createdAt, "in_progress")
	inProgressResponse := xaiBuildCompactionBaseResponse(prepared, compactData, responseID, createdAt, "in_progress")
	completedResponse := xaiBuildCompactionBaseResponse(prepared, compactData, responseID, createdAt, "completed")
	completedResponse, _ = sjson.SetBytes(completedResponse, "completed_at", completedAt)
	completedResponse, _ = sjson.SetRawBytes(completedResponse, "output", output)
	if usage := gjson.GetBytes(compactData, "usage"); usage.Exists() {
		completedResponse, _ = sjson.SetRawBytes(completedResponse, "usage", []byte(usage.Raw))
	}

	createdPayload := []byte(`{"type":"response.created","sequence_number":0}`)
	createdPayload, _ = sjson.SetRawBytes(createdPayload, "response", createdResponse)
	inProgressPayload := []byte(`{"type":"response.in_progress","sequence_number":1}`)
	inProgressPayload, _ = sjson.SetRawBytes(inProgressPayload, "response", inProgressResponse)
	addedPayload := []byte(`{"type":"response.output_item.added","sequence_number":2,"output_index":0}`)
	addedPayload, _ = sjson.SetRawBytes(addedPayload, "item", item)
	keepalivePayload := []byte(`{"type":"keepalive","sequence_number":3}`)
	donePayload := []byte(`{"type":"response.output_item.done","sequence_number":4,"output_index":0}`)
	donePayload, _ = sjson.SetRawBytes(donePayload, "item", item)
	completedPayload := []byte(`{"type":"response.completed","sequence_number":5}`)
	completedPayload, _ = sjson.SetRawBytes(completedPayload, "response", completedResponse)

	return [][]byte{
		xaiBuildSSEFrame("response.created", createdPayload),
		xaiBuildSSEFrame("response.in_progress", inProgressPayload),
		xaiBuildSSEFrame("response.output_item.added", addedPayload),
		xaiBuildSSEFrame("keepalive", keepalivePayload),
		xaiBuildSSEFrame("response.output_item.done", donePayload),
		xaiBuildSSEFrame("response.completed", completedPayload),
	}
}

func xaiBuildCompactionBaseResponse(prepared *xaiPreparedRequest, compactData []byte, responseID string, createdAt int64, status string) []byte {
	response := []byte(`{"id":"","object":"response","created_at":0,"status":"","background":false,"error":null,"incomplete_details":null,"output":[]}`)
	response, _ = sjson.SetBytes(response, "id", responseID)
	response, _ = sjson.SetBytes(response, "created_at", createdAt)
	response, _ = sjson.SetBytes(response, "status", status)
	if model := gjson.GetBytes(compactData, "model").String(); model != "" {
		response, _ = sjson.SetBytes(response, "model", model)
	} else if prepared != nil && prepared.baseModel != "" {
		response, _ = sjson.SetBytes(response, "model", prepared.baseModel)
	}

	if prepared == nil {
		return response
	}
	for _, field := range []string{
		"instructions",
		"max_output_tokens",
		"max_tool_calls",
		"parallel_tool_calls",
		"previous_response_id",
		"prompt_cache_key",
		"reasoning",
		"text",
		"tool_choice",
		"tools",
		"top_logprobs",
		"top_p",
		"truncation",
		"user",
		"metadata",
	} {
		if value := gjson.GetBytes(prepared.body, field); value.Exists() {
			response, _ = sjson.SetRawBytes(response, field, []byte(value.Raw))
		}
	}
	return response
}

func xaiCompactionOutputItem(compactData []byte, responseID string) []byte {
	itemResult := gjson.GetBytes(compactData, "output.0")
	item := []byte(`{"type":"compaction"}`)
	if itemResult.Exists() && itemResult.Type == gjson.JSON {
		item = []byte(itemResult.Raw)
	}
	if !gjson.GetBytes(item, "type").Exists() {
		item, _ = sjson.SetBytes(item, "type", "compaction")
	}
	if !gjson.GetBytes(item, "id").Exists() {
		item, _ = sjson.SetBytes(item, "id", xaiCompactionItemID(responseID))
	}
	return item
}

func xaiCompactionResponseID(compactData []byte) string {
	if responseID := strings.TrimSpace(gjson.GetBytes(compactData, "id").String()); responseID != "" {
		if strings.HasPrefix(responseID, "resp_") {
			return responseID
		}
		return "resp_" + strings.TrimPrefix(responseID, "cmp_")
	}
	return fmt.Sprintf("resp_xai_compaction_%d", time.Now().UnixNano())
}

func xaiCompactionItemID(responseID string) string {
	if suffix := strings.TrimPrefix(responseID, "resp_"); suffix != "" && suffix != responseID {
		return "cmp_" + suffix
	}
	return "cmp_" + responseID
}

func xaiBuildSSEFrame(eventName string, data []byte) []byte {
	out := make([]byte, 0, len(eventName)+len(data)+16)
	out = append(out, "event: "...)
	out = append(out, eventName...)
	out = append(out, '\n')
	out = append(out, "data: "...)
	out = append(out, data...)
	out = append(out, '\n', '\n')
	return out
}
