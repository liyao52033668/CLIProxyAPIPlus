package executor

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

func (e *MetaExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	if errExpand := helps.ExpandResponsesCompactionCapsules(&req, &opts, "meta"); errExpand != nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
	}
	if helps.HasResponsesCompactionTriggerRequest(req, opts) {
		return e.executeMetaCompactionStream(ctx, auth, req, opts)
	}

	enriched, errAuth := e.ensureAuth(ctx, auth)
	if errAuth != nil {
		return nil, errAuth
	}

	prepared, errPrepare := e.prepareResponsesRequest(ctx, enriched, req, opts, true)
	if errPrepare != nil {
		return nil, errPrepare
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.baseModel, enriched)
	defer reporter.TrackFailure(ctx, &err)
	reporter.SetTranslatedReasoningEffort(prepared.body, prepared.to.String())

	baseURL, token := metaCreds(enriched)
	if strings.TrimSpace(baseURL) == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "meta executor: missing provider baseURL"}
	}

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	httpReq, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(prepared.body))
	if errRequest != nil {
		return nil, errRequest
	}
	applyMetaAPIHeaders(httpReq, enriched, token, true, opts.Headers)
	e.recordMetaRequest(ctx, enriched, url, httpReq.Header.Clone(), prepared.body)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, enriched, 0)
	// Early keepalive/metadata frames are only first-packet fallbacks; effective
	// TTFT is marked on the first substantive token event below.
	httpClient = reporter.TrackHTTPClientRoundTripOnly(httpClient)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return nil, errDo
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("meta executor: close response body error: %v", errClose)
		}
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			return nil, errRead
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		return nil, wrapMetaUpstreamError(httpResp.StatusCode, data)
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("meta executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		claudeInputTokens := helps.NewClaudeInputTokenState(prepared.from, prepared.to, prepared.responseFormat, prepared.originalPayload)
		var param any
		outputItemsByIndex := make(map[int64][]byte)
		var outputItemsFallback [][]byte
		var pendingEventLine []byte
		var filteringSubscriptionUsage bool
		var skipFilteredBlankLine bool
		emitTranslatedLine := func(translatedLine []byte) bool {
			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, prepared.to, prepared.responseFormat, req.Model, prepared.originalPayload, prepared.body, translatedLine, &param, claudeInputTokens)
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
			trimmedLine := bytes.TrimSpace(line)
			if bytes.HasPrefix(trimmedLine, metaEventTag) {
				eventName := bytes.TrimSpace(trimmedLine[len(metaEventTag):])
				if bytes.Equal(eventName, []byte(metaSubscriptionUsageEventType)) {
					if pendingEventLine != nil {
						if !emitTranslatedLine(pendingEventLine) {
							return
						}
						pendingEventLine = nil
					}
					filteringSubscriptionUsage = true
					skipFilteredBlankLine = true
					continue
				}
				filteringSubscriptionUsage = false
				skipFilteredBlankLine = false
				if pendingEventLine != nil {
					if !emitTranslatedLine(pendingEventLine) {
						return
					}
				}
				pendingEventLine = bytes.Clone(line)
				continue
			}
			if !bytes.HasPrefix(trimmedLine, dataTag) {
				if len(trimmedLine) == 0 {
					filteringSubscriptionUsage = false
					if skipFilteredBlankLine {
						skipFilteredBlankLine = false
						continue
					}
				}
				if filteringSubscriptionUsage {
					continue
				}
				skipFilteredBlankLine = false
				if pendingEventLine != nil {
					if !emitTranslatedLine(pendingEventLine) {
						return
					}
					pendingEventLine = nil
				}
				if !emitTranslatedLine(bytes.Clone(line)) {
					return
				}
				continue
			}
			eventData := bytes.TrimSpace(trimmedLine[len(dataTag):])
			eventType := gjson.GetBytes(eventData, "type").String()
			if isMetaSubscriptionUsageEvent(eventType, eventData) {
				pendingEventLine = nil
				filteringSubscriptionUsage = false
				skipFilteredBlankLine = true
				continue
			}
			if filteringSubscriptionUsage {
				if eventType == "" {
					pendingEventLine = nil
					if gjson.ValidBytes(eventData) {
						filteringSubscriptionUsage = false
					}
					skipFilteredBlankLine = true
					continue
				}
				filteringSubscriptionUsage = false
			}
			skipFilteredBlankLine = false
			// Observe upstream content before translation or failure publication.
			helps.ObserveResponsesTokenEvent(reporter, eventData)
			if errEvent := metaStreamEventError(eventData); errEvent != nil {
				helps.RecordAPIResponseError(ctx, e.cfg, errEvent)
				reporter.PublishFailure(ctx, errEvent)
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: errEvent}:
				case <-ctx.Done():
				}
				return
			}
			switch eventType {
			case "response.output_item.done":
				xaiCollectOutputItemDone(eventData, outputItemsByIndex, &outputItemsFallback)
			case "response.completed", "response.incomplete":
				if detail, ok := helps.ParseCodexUsage(eventData); ok {
					reporter.Publish(ctx, detail)
				}
				eventData = patchCodexCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
			}
			if pendingEventLine != nil {
				if !emitTranslatedLine(pendingEventLine) {
					return
				}
				pendingEventLine = nil
			}
			if !emitTranslatedLine(append([]byte("data: "), eventData...)) {
				return
			}
		}
		if pendingEventLine != nil {
			if !emitTranslatedLine(pendingEventLine) {
				return
			}
			pendingEventLine = nil
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
			return
		}
		// Ensure we record the request if no usage chunk was ever seen; the
		// zero-token deferral in publishWithOutcome would otherwise drop
		// empty-but-successful terminal streams entirely.
		reporter.EnsurePublished(ctx)
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}
