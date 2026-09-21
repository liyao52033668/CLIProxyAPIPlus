package executor

import (
	"bufio"
	"bytes"
	"context"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func (e *XAIExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "streaming not supported for /responses/compact"}
	}
	if xaiInputHasItemType(req.Payload, "compaction_trigger") {
		return e.executeCompactionTriggerStream(ctx, auth, req, opts)
	}

	token, _ := xaiCreds(auth)
	baseURL := xaiChatBaseURL(auth)
	logXAIResolvedBaseURL(ctx, baseURL)

	prepared, err := e.prepareResponsesRequest(ctx, req, opts, true)
	if err != nil {
		return nil, err
	}

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), prepared.baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	url := helps.JoinBaseURL(baseURL, "/responses")
	headers := make(http.Header)
	tmpReq := &http.Request{Header: headers}
	applyXAIChatHeaders(tmpReq, auth, token, true, prepared.sessionID)
	headers = tmpReq.Header
	e.recordXAIRequest(ctx, auth, url, headers.Clone(), prepared.body)

	httpResp, errDo := helps.DoStream(ctx, e.cfg, helps.UpstreamRequest{
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
			return nil, xaiStatusErr(ue.Code, []byte(ue.Msg))
		}
		return nil, errDo
	}

	out := make(chan cliproxyexecutor.StreamChunk, 16)
	go func() {
		defer close(out)
		defer helps.CloseResponseBody(e.Identifier(), httpResp.Body)
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), streamScannerBuffer)
		var param any
		outputItemsByIndex := make(map[int64][]byte)
		textContentPartsSeen := make(map[string]struct{})
		var outputItemsFallback [][]byte
		responseFilter := newXAIInternalXSearchResponseFilter(prepared.filterInternalXSearch, prepared.clientDeclaredTools)
		var pendingEventLine []byte
		emitTranslatedLine := func(translatedLine []byte) bool {
			chunks := sdktranslator.TranslateStream(ctx, prepared.to, prepared.responseFormat, req.Model, prepared.originalPayload, prepared.body, translatedLine, &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return false
				}
			}
			return true
		}
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)

			if bytes.HasPrefix(line, xaiEventTag) {
				if pendingEventLine != nil && !emitTranslatedLine(xaiNormalizeReasoningSummaryEventLine(pendingEventLine, "")) {
					return
				}
				pendingEventLine = bytes.Clone(line)
				continue
			}

			if bytes.HasPrefix(line, xaiDataTag) {
				eventDataList := xaiNormalizeReasoningSummaryDataEvents(bytes.TrimSpace(line[len(xaiDataTag):]))
				hasPendingEventLine := pendingEventLine != nil
				for i, eventData := range eventDataList {
					eventData = restoreXAINamespaceToolCalls(eventData, prepared.namespaceTools)
					if prepared.webSearchAlias != "" {
						eventData = restoreXAIClientWebSearchName(eventData, prepared.webSearchAlias)
					}
					eventData = responseFilter.apply(eventData)
					if len(eventData) == 0 {
						if hasPendingEventLine && i == 0 {
							pendingEventLine = nil
						}
						continue
					}
					normalizedEventName := gjson.GetBytes(eventData, "type").String()
					switch normalizedEventName {
					case "response.content_part.added":
						xaiMarkTextContentPartSeen(eventData, textContentPartsSeen)
					case "response.output_text.delta":
						if syntheticEvent := xaiSyntheticTextContentPartAdded(eventData, textContentPartsSeen); syntheticEvent != nil {
							if hasPendingEventLine {
								pendingEventLine = nil
							}
							if !emitTranslatedLine([]byte("event: response.content_part.added")) {
								return
							}
							if !emitTranslatedLine(append([]byte("data: "), syntheticEvent...)) {
								return
							}
							hasPendingEventLine = true
						}
					case "response.output_item.done":
						xaiCollectOutputItemDone(eventData, outputItemsByIndex, &outputItemsFallback)
					case "response.completed":
						if detail, ok := helps.ParseCodexUsage(eventData); ok {
							reporter.Publish(ctx, detail)
						}
						eventData = xaiPatchCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
						eventData = xaiNormalizeReasoningSummaryData(eventData)
						cacheXAIReasoningReplayFromCompleted(ctx, prepared.replayScope, eventData)
						normalizedEventName = gjson.GetBytes(eventData, "type").String()
					}

					if hasPendingEventLine {
						eventLine := []byte("event: " + normalizedEventName)
						if i == 0 {
							eventLine = xaiNormalizeReasoningSummaryEventLine(pendingEventLine, normalizedEventName)
							pendingEventLine = nil
						}
						if !emitTranslatedLine(eventLine) {
							return
						}
					}
					if !emitTranslatedLine(append([]byte("data: "), eventData...)) {
						return
					}
				}
				continue
			}

			if pendingEventLine != nil {
				if !emitTranslatedLine(xaiNormalizeReasoningSummaryEventLine(pendingEventLine, "")) {
					return
				}
				pendingEventLine = nil
			}
			if !emitTranslatedLine(bytes.Clone(line)) {
				return
			}
		}
		if pendingEventLine != nil {
			emitTranslatedLine(xaiNormalizeReasoningSummaryEventLine(pendingEventLine, ""))
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		}
		reporter.EnsurePublished(ctx)
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}
