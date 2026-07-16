package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/tiktoken-go/tokenizer"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	codexUserAgent             = "codex_cli_rs/0.144.1 (Mac OS 26.3.1; arm64) iTerm.app/3.6.9"
	codexOriginator            = "codex_cli_rs"
	codexDefaultImageToolModel = "gpt-image-2"
	codexResponsesLiteHeader   = "X-OpenAI-Internal-Codex-Responses-Lite"
	codexResponsesLiteMetadata = "client_metadata.ws_request_header_x_openai_internal_codex_responses_lite"
)

var dataTag = []byte("data:")

const codexIncompleteStreamMessage = "stream error: stream disconnected before completion: stream closed before response.completed"

type codexIncompleteStreamError struct {
	statusErr
}

func newCodexIncompleteStreamError() codexIncompleteStreamError {
	return codexIncompleteStreamError{statusErr: statusErr{
		code: http.StatusRequestTimeout,
		msg:  codexIncompleteStreamMessage,
	}}
}

func (codexIncompleteStreamError) IsRequestScoped() bool {
	return true
}

// Streamed Codex responses may emit response.output_item.done events while leaving
// response.completed.response.output empty. Keep the stream path aligned with the
// already-patched non-stream path by reconstructing response.output from those items.
func collectCodexOutputItemDone(eventData []byte, outputItemsByIndex map[int64][]byte, outputItemsFallback *[][]byte) {
	itemResult := gjson.GetBytes(eventData, "item")
	if !itemResult.Exists() || itemResult.Type != gjson.JSON {
		return
	}
	outputIndexResult := gjson.GetBytes(eventData, "output_index")
	if outputIndexResult.Exists() {
		outputItemsByIndex[outputIndexResult.Int()] = []byte(itemResult.Raw)
		return
	}
	*outputItemsFallback = append(*outputItemsFallback, []byte(itemResult.Raw))
}

func patchCodexCompletedOutput(eventData []byte, outputItemsByIndex map[int64][]byte, outputItemsFallback [][]byte) []byte {
	outputResult := gjson.GetBytes(eventData, "response.output")
	shouldPatchOutput := (!outputResult.Exists() || !outputResult.IsArray() || len(outputResult.Array()) == 0) && (len(outputItemsByIndex) > 0 || len(outputItemsFallback) > 0)
	if !shouldPatchOutput {
		return eventData
	}

	indexes := make([]int64, 0, len(outputItemsByIndex))
	for idx := range outputItemsByIndex {
		indexes = append(indexes, idx)
	}
	sort.Slice(indexes, func(i, j int) bool {
		return indexes[i] < indexes[j]
	})

	items := make([][]byte, 0, len(outputItemsByIndex)+len(outputItemsFallback))
	for _, idx := range indexes {
		items = append(items, outputItemsByIndex[idx])
	}
	items = append(items, outputItemsFallback...)

	outputArray := []byte("[]")
	if len(items) > 0 {
		var buf bytes.Buffer
		totalLen := 2
		for _, item := range items {
			totalLen += len(item)
		}
		if len(items) > 1 {
			totalLen += len(items) - 1
		}
		buf.Grow(totalLen)
		buf.WriteByte('[')
		for i, item := range items {
			if i > 0 {
				buf.WriteByte(',')
			}
			buf.Write(item)
		}
		buf.WriteByte(']')
		outputArray = buf.Bytes()
	}

	completedDataPatched, _ := sjson.SetRawBytes(eventData, "response.output", outputArray)
	return completedDataPatched
}

func codexTerminalFailureErr(eventData []byte) (statusErr, []byte, bool) {
	body, ok := codexTerminalFailureBody(eventData)
	if !ok {
		return statusErr{}, nil, false
	}
	return newCodexStatusErr(codexTerminalFailureStatus(body), body), body, true
}

func codexTerminalFailureStatus(body []byte) int {
	for _, path := range []string{"error.status_code", "error.status"} {
		if status := int(gjson.GetBytes(body, path).Int()); status >= 400 && status <= 599 {
			return status
		}
	}

	errorType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.type").String()))
	errorCode := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.code").String()))
	switch {
	case errorType == "invalid_request_error", errorType == "bad_request_error",
		errorCode == "context_length_exceeded", errorCode == "context_too_large",
		errorCode == "thinking_signature_invalid", errorCode == "previous_response_not_found":
		return http.StatusBadRequest
	case errorType == "authentication_error", errorCode == "invalid_api_key", errorCode == "unauthorized":
		return http.StatusUnauthorized
	case errorType == "permission_error", errorCode == "forbidden", errorCode == "permission_denied":
		return http.StatusForbidden
	case errorType == "not_found_error", errorCode == "not_found", errorCode == "model_not_found":
		return http.StatusNotFound
	case errorType == "rate_limit_error", errorType == "usage_limit_reached",
		errorCode == "rate_limit_exceeded", errorCode == "usage_limit_reached":
		return http.StatusTooManyRequests
	default:
		return http.StatusBadGateway
	}
}

func codexTerminalFailureBody(eventData []byte) ([]byte, bool) {
	root := gjson.ParseBytes(eventData)
	var upstreamError gjson.Result
	switch root.Get("type").String() {
	case "response.failed":
		upstreamError = root.Get("response.error")
		if !upstreamError.Exists() {
			upstreamError = root.Get("error")
		}
	case "error":
		upstreamError = root.Get("error")
	default:
		return nil, false
	}

	body := []byte(`{"error":{}}`)
	if upstreamError.IsObject() {
		body, _ = sjson.SetRawBytes(body, "error", []byte(upstreamError.Raw))
	} else if message := strings.TrimSpace(upstreamError.String()); message != "" {
		body, _ = sjson.SetBytes(body, "error.message", message)
	}
	if strings.TrimSpace(gjson.GetBytes(body, "error.message").String()) == "" {
		if message := strings.TrimSpace(root.Get("response.error.message").String()); message != "" {
			body, _ = sjson.SetBytes(body, "error.message", message)
		}
	}
	for _, field := range []struct {
		source string
		target string
	}{
		{source: "code", target: "error.code"},
		{source: "error_type", target: "error.type"},
		{source: "param", target: "error.param"},
	} {
		if gjson.GetBytes(body, field.target).Exists() {
			continue
		}
		if value := strings.TrimSpace(root.Get(field.source).String()); value != "" {
			body, _ = sjson.SetBytes(body, field.target, value)
		}
	}
	if strings.TrimSpace(gjson.GetBytes(body, "error.message").String()) == "" {
		message := strings.TrimSpace(root.Get("message").String())
		if message == "" {
			message = strings.TrimSpace(gjson.GetBytes(body, "error.code").String())
		}
		if message == "" {
			message = strings.TrimSpace(gjson.GetBytes(body, "error.type").String())
		}
		if message == "" {
			message = "upstream stream failed without error details"
		}
		body, _ = sjson.SetBytes(body, "error.message", message)
	}
	return body, true
}

// CodexExecutor is a stateless executor for Codex (OpenAI Responses API entrypoint).
// If api_key is unavailable on auth, it falls back to legacy via ClientAdapter.
type CodexExecutor struct {
	cfg *config.Config
}

func NewCodexExecutor(cfg *config.Config) *CodexExecutor { return &CodexExecutor{cfg: cfg} }

func (e *CodexExecutor) Identifier() string { return "codex" }

// PrepareRequest injects Codex credentials into the outgoing HTTP request.
func (e *CodexExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	apiKey, _ := codexCreds(auth)
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects Codex credentials into the request and executes it.
func (e *CodexExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("codex executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

func (e *CodexExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if opts.Alt == "responses/compact" {
		return e.executeCompact(ctx, auth, req, opts)
	}
	if isCodexOpenAIImageRequest(opts) {
		return e.executeOpenAIImage(ctx, auth, req, opts)
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("codex")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, false)
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body, _ = sjson.SetBytes(body, "model", baseModel)
	body, _ = sjson.SetBytes(body, "stream", true)
	body, _ = sjson.DeleteBytes(body, "previous_response_id")
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "safety_identifier")
	body, _ = sjson.DeleteBytes(body, "stream_options")
	body = normalizeCodexInstructions(body)
	if e.cfg == nil || e.cfg.DisableImageGeneration == config.DisableImageGenerationOff {
		body = ensureImageGenerationTool(body, baseModel, auth, opts.Headers)
	}
	body = sanitizeOpenAIResponsesReasoningEncryptedContent(ctx, e.Identifier(), body)
	body, replayScope := applyCodexReasoningReplayCache(ctx, from, req, opts, body)

	url := helps.JoinBaseURL(baseURL, "/responses")
	httpReq, err := e.cacheHelper(ctx, from, url, req, body)
	if err != nil {
		return resp, err
	}
	applyCodexHeaders(httpReq, auth, apiKey, true, e.cfg)
	applyModelHeaderOverrides(httpReq.Header, baseModel)
	_, data, respHeaders, errDo := helps.DoJSON(ctx, e.cfg, helps.UpstreamRequest{
		Provider: e.Identifier(),
		Auth:     auth,
		Method:   http.MethodPost,
		URL:      url,
		Headers:  httpReq.Header,
		Body:     body,
	})
	if errDo != nil {
		if ue, ok := errDo.(helps.UpstreamStatusError); ok {
			return resp, newCodexStatusErr(ue.Code, []byte(ue.Msg))
		}
		return resp, errDo
	}
	_ = respHeaders

	lines := bytes.Split(data, []byte("\n"))
	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	for _, line := range lines {
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}

		eventData := bytes.TrimSpace(line[5:])
		eventType := gjson.GetBytes(eventData, "type").String()

		if eventType == "response.output_item.done" {
			itemResult := gjson.GetBytes(eventData, "item")
			if !itemResult.Exists() || itemResult.Type != gjson.JSON {
				continue
			}
			outputIndexResult := gjson.GetBytes(eventData, "output_index")
			if outputIndexResult.Exists() {
				outputItemsByIndex[outputIndexResult.Int()] = []byte(itemResult.Raw)
			} else {
				outputItemsFallback = append(outputItemsFallback, []byte(itemResult.Raw))
			}
			continue
		}

		if terminalErr, _, ok := codexTerminalFailureErr(eventData); ok {
			clearCodexReasoningReplayOnInvalidSignature(replayScope, eventData)
			err = terminalErr
			return resp, err
		}
		if eventType != "response.completed" && eventType != "response.incomplete" {
			continue
		}

		if detail, ok := helps.ParseCodexUsage(eventData); ok {
			reporter.Publish(ctx, detail)
		}
		publishCodexImageToolUsage(ctx, reporter, body, eventData)
		reporter.EnsurePublished(ctx)

		completedData := patchCodexCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
		if eventType == "response.completed" {
			cacheCodexReasoningReplayFromCompleted(replayScope, completedData)
		}

		var param any
		out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, originalPayload, body, completedData, &param)
		resp = cliproxyexecutor.Response{Payload: out, Headers: respHeaders}
		return resp, nil
	}
	err = newCodexIncompleteStreamError()
	return resp, err
}

func (e *CodexExecutor) executeCompact(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai-response")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, false)
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body, _ = sjson.SetBytes(body, "model", baseModel)
	body, _ = sjson.DeleteBytes(body, "stream")
	body = normalizeCodexInstructions(body)
	// Compact requests should not inject image_generation tools; keep the body
	// focused on history + compaction_trigger.
	body = sanitizeOpenAIResponsesReasoningEncryptedContent(ctx, e.Identifier(), body)

	url := helps.JoinBaseURL(baseURL, "/responses/compact")
	httpReq, err := e.cacheHelper(ctx, from, url, req, body)
	if err != nil {
		return resp, err
	}
	applyCodexHeaders(httpReq, auth, apiKey, false, e.cfg)
	applyModelHeaderOverrides(httpReq.Header, baseModel)
	_, data, respHeaders, errDo := helps.DoJSON(ctx, e.cfg, helps.UpstreamRequest{
		Provider: e.Identifier(),
		Auth:     auth,
		Method:   http.MethodPost,
		URL:      url,
		Headers:  httpReq.Header,
		Body:     body,
	})
	if errDo != nil {
		if ue, ok := errDo.(helps.UpstreamStatusError); ok {
			return resp, newCodexStatusErr(ue.Code, []byte(ue.Msg))
		}
		return resp, errDo
	}
	reporter.Publish(ctx, helps.ParseOpenAIUsage(data))
	reporter.EnsurePublished(ctx)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, originalPayload, body, data, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: respHeaders}
	return resp, nil
}

func (e *CodexExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "streaming not supported for /responses/compact"}
	}
	if isCodexOpenAIImageRequest(opts) {
		return e.executeOpenAIImageStream(ctx, auth, req, opts)
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("codex")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, true)
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body, _ = sjson.DeleteBytes(body, "previous_response_id")
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "safety_identifier")
	body, _ = sjson.DeleteBytes(body, "stream_options")
	body, _ = sjson.SetBytes(body, "model", baseModel)
	body = normalizeCodexInstructions(body)
	if e.cfg == nil || e.cfg.DisableImageGeneration == config.DisableImageGenerationOff {
		body = ensureImageGenerationTool(body, baseModel, auth, opts.Headers)
	}
	body = sanitizeOpenAIResponsesReasoningEncryptedContent(ctx, e.Identifier(), body)
	body, replayScope := applyCodexReasoningReplayCache(ctx, from, req, opts, body)

	url := helps.JoinBaseURL(baseURL, "/responses")
	httpReq, err := e.cacheHelper(ctx, from, url, req, body)
	if err != nil {
		return nil, err
	}
	applyCodexHeaders(httpReq, auth, apiKey, true, e.cfg)
	applyModelHeaderOverrides(httpReq.Header, baseModel)
	httpResp, errDo := helps.DoStream(ctx, e.cfg, helps.UpstreamRequest{
		Provider: e.Identifier(),
		Auth:     auth,
		Method:   http.MethodPost,
		URL:      url,
		Headers:  httpReq.Header,
		Body:     body,
	})
	if errDo != nil {
		if ue, ok := errDo.(helps.UpstreamStatusError); ok {
			return nil, newCodexStatusErr(ue.Code, []byte(ue.Msg))
		}
		return nil, errDo
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer helps.CloseResponseBody(e.Identifier(), httpResp.Body)
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB
		var param any
		outputItemsByIndex := make(map[int64][]byte)
		var outputItemsFallback [][]byte
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			translatedLine := bytes.Clone(line)
			terminalSuccess := false

			if bytes.HasPrefix(line, dataTag) {
				data := bytes.TrimSpace(line[5:])
				eventType := gjson.GetBytes(data, "type").String()
				if terminalErr, _, ok := codexTerminalFailureErr(data); ok {
					clearCodexReasoningReplayOnInvalidSignature(replayScope, data)
					helps.RecordAPIResponseError(ctx, e.cfg, terminalErr)
					reporter.PublishFailure(ctx, terminalErr)
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: terminalErr}:
					case <-ctx.Done():
					}
					return
				}
				switch eventType {
				case "response.output_item.done":
					collectCodexOutputItemDone(data, outputItemsByIndex, &outputItemsFallback)
				case "response.completed", "response.incomplete":
					terminalSuccess = true
					if detail, ok := helps.ParseCodexUsage(data); ok {
						reporter.Publish(ctx, detail)
					}
					publishCodexImageToolUsage(ctx, reporter, body, data)
					data = patchCodexCompletedOutput(data, outputItemsByIndex, outputItemsFallback)
					if eventType == "response.completed" {
						cacheCodexReasoningReplayFromCompleted(replayScope, data)
					}
					translatedLine = append([]byte("data: "), data...)
				}
			}

			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, originalPayload, body, translatedLine, &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
			if terminalSuccess {
				reporter.EnsurePublished(ctx)
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
		}
		streamErr := newCodexIncompleteStreamError()
		helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
		reporter.PublishFailure(ctx, streamErr)
		select {
		case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
		case <-ctx.Done():
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func translateCodexRequestPair(from, to sdktranslator.Format, model string, originalPayload, payload []byte, stream bool) ([]byte, []byte) {
	translated := sdktranslator.TranslateRequest(from, to, model, payload, stream)
	if bytes.Equal(originalPayload, payload) {
		return translated, translated
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, model, originalPayload, stream)
	return originalTranslated, translated
}

func (e *CodexExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	from := opts.SourceFormat
	to := sdktranslator.FromString("codex")
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	body, err := thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	body, _ = sjson.SetBytes(body, "model", baseModel)
	body, _ = sjson.DeleteBytes(body, "previous_response_id")
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "safety_identifier")
	body, _ = sjson.DeleteBytes(body, "stream_options")
	body, _ = sjson.SetBytes(body, "stream", false)
	body = normalizeCodexInstructions(body)

	enc, err := tokenizerForCodexModel(baseModel)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("codex executor: tokenizer init failed: %w", err)
	}

	count, err := countCodexInputTokens(enc, body)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("codex executor: token counting failed: %w", err)
	}

	usageJSON := fmt.Sprintf(`{"response":{"usage":{"input_tokens":%d,"output_tokens":0,"total_tokens":%d}}}`, count, count)
	translated := sdktranslator.TranslateTokenCount(ctx, to, from, count, []byte(usageJSON))
	return cliproxyexecutor.Response{Payload: translated}, nil
}

func tokenizerForCodexModel(model string) (tokenizer.Codec, error) {
	sanitized := strings.ToLower(strings.TrimSpace(model))
	switch {
	case sanitized == "":
		return tokenizer.Get(tokenizer.Cl100kBase)
	case strings.HasPrefix(sanitized, "gpt-5"):
		return tokenizer.ForModel(tokenizer.GPT5)
	case strings.HasPrefix(sanitized, "gpt-4.1"):
		return tokenizer.ForModel(tokenizer.GPT41)
	case strings.HasPrefix(sanitized, "gpt-4o"):
		return tokenizer.ForModel(tokenizer.GPT4o)
	case strings.HasPrefix(sanitized, "gpt-4"):
		return tokenizer.ForModel(tokenizer.GPT4)
	case strings.HasPrefix(sanitized, "gpt-3.5"), strings.HasPrefix(sanitized, "gpt-3"):
		return tokenizer.ForModel(tokenizer.GPT35Turbo)
	default:
		return tokenizer.Get(tokenizer.Cl100kBase)
	}
}

func countCodexInputTokens(enc tokenizer.Codec, body []byte) (int64, error) {
	if enc == nil {
		return 0, fmt.Errorf("encoder is nil")
	}
	if len(body) == 0 {
		return 0, nil
	}

	root := gjson.ParseBytes(body)
	var segments []string

	if inst := strings.TrimSpace(root.Get("instructions").String()); inst != "" {
		segments = append(segments, inst)
	}

	inputItems := root.Get("input")
	if inputItems.IsArray() {
		arr := inputItems.Array()
		for i := range arr {
			item := arr[i]
			switch item.Get("type").String() {
			case "message":
				content := item.Get("content")
				if content.IsArray() {
					parts := content.Array()
					for j := range parts {
						part := parts[j]
						if text := strings.TrimSpace(part.Get("text").String()); text != "" {
							segments = append(segments, text)
						}
					}
				}
			case "function_call":
				if name := strings.TrimSpace(item.Get("name").String()); name != "" {
					segments = append(segments, name)
				}
				if args := strings.TrimSpace(item.Get("arguments").String()); args != "" {
					segments = append(segments, args)
				}
			case "function_call_output":
				if out := strings.TrimSpace(item.Get("output").String()); out != "" {
					segments = append(segments, out)
				}
			default:
				if text := strings.TrimSpace(item.Get("text").String()); text != "" {
					segments = append(segments, text)
				}
			}
		}
	}

	tools := root.Get("tools")
	if tools.IsArray() {
		tarr := tools.Array()
		for i := range tarr {
			tool := tarr[i]
			if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
				segments = append(segments, name)
			}
			if desc := strings.TrimSpace(tool.Get("description").String()); desc != "" {
				segments = append(segments, desc)
			}
			if params := tool.Get("parameters"); params.Exists() {
				val := params.Raw
				if params.Type == gjson.String {
					val = params.String()
				}
				if trimmed := strings.TrimSpace(val); trimmed != "" {
					segments = append(segments, trimmed)
				}
			}
		}
	}

	textFormat := root.Get("text.format")
	if textFormat.Exists() {
		if name := strings.TrimSpace(textFormat.Get("name").String()); name != "" {
			segments = append(segments, name)
		}
		if schema := textFormat.Get("schema"); schema.Exists() {
			val := schema.Raw
			if schema.Type == gjson.String {
				val = schema.String()
			}
			if trimmed := strings.TrimSpace(val); trimmed != "" {
				segments = append(segments, trimmed)
			}
		}
	}

	text := strings.Join(segments, "\n")
	if text == "" {
		return 0, nil
	}

	count, err := enc.Count(text)
	if err != nil {
		return 0, err
	}
	return int64(count), nil
}

func (e *CodexExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("codex executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, statusErr{code: 500, msg: "codex executor: auth is nil"}
	}
	var refreshToken string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["refresh_token"].(string); ok && v != "" {
			refreshToken = v
		}
	}
	if refreshToken == "" {
		return auth, nil
	}
	svc := codexauth.NewCodexAuthWithProxyURL(e.cfg, auth.ProxyURL)
	td, err := svc.RefreshTokensWithRetry(ctx, refreshToken, 3)
	if err != nil {
		return nil, err
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["id_token"] = td.IDToken
	auth.Metadata["access_token"] = td.AccessToken
	if td.RefreshToken != "" {
		auth.Metadata["refresh_token"] = td.RefreshToken
	}
	if td.AccountID != "" {
		auth.Metadata["account_id"] = td.AccountID
	}
	auth.Metadata["email"] = td.Email
	// Use unified key in files
	auth.Metadata["expired"] = td.Expire
	auth.Metadata["type"] = "codex"
	now := time.Now().Format(time.RFC3339)
	auth.Metadata["last_refresh"] = now
	return auth, nil
}

func (e *CodexExecutor) cacheHelper(ctx context.Context, from sdktranslator.Format, url string, req cliproxyexecutor.Request, rawJSON []byte) (*http.Request, error) {
	var cache helps.CodexCache
	if from == "claude" {
		userIDResult := gjson.GetBytes(req.Payload, "metadata.user_id")
		if userIDResult.Exists() {
			key := fmt.Sprintf("%s-%s", req.Model, userIDResult.String())
			var ok bool
			if cache, ok = helps.GetCodexCache(key); !ok {
				cache = helps.CodexCache{
					ID:     uuid.New().String(),
					Expire: time.Now().Add(1 * time.Hour),
				}
				helps.SetCodexCache(key, cache)
			}
		}
	} else if from == "openai-response" {
		promptCacheKey := gjson.GetBytes(req.Payload, "prompt_cache_key")
		if promptCacheKey.Exists() {
			cache.ID = promptCacheKey.String()
		}
	} else if from == "openai" {
		if apiKey := strings.TrimSpace(helps.APIKeyFromContext(ctx)); apiKey != "" {
			cache.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:"+apiKey)).String()
		}
	}

	if cache.ID != "" {
		rawJSON, _ = sjson.SetBytes(rawJSON, "prompt_cache_key", cache.ID)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(rawJSON))
	if err != nil {
		return nil, err
	}
	if cache.ID != "" {
		httpReq.Header.Set("Session_id", cache.ID)
	}
	return httpReq, nil
}

func applyCodexHeaders(r *http.Request, auth *cliproxyauth.Auth, token string, stream bool, cfg *config.Config) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)

	var ginHeaders http.Header
	if ginCtx, ok := r.Context().Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		ginHeaders = ginCtx.Request.Header
	}

	if ginHeaders.Get("X-Codex-Beta-Features") != "" {
		r.Header.Set("X-Codex-Beta-Features", ginHeaders.Get("X-Codex-Beta-Features"))
	}
	misc.EnsureHeader(r.Header, ginHeaders, "Version", "")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Codex-Turn-Metadata", "")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Client-Request-Id", "")
	cfgUserAgent, _ := codexHeaderDefaults(cfg, auth)
	ensureHeaderWithConfigPrecedence(r.Header, ginHeaders, "User-Agent", cfgUserAgent, codexUserAgent)

	if strings.Contains(r.Header.Get("User-Agent"), "Mac OS") {
		misc.EnsureHeader(r.Header, ginHeaders, "Session_id", uuid.NewString())
	}

	if stream {
		r.Header.Set("Accept", "text/event-stream")
	} else {
		r.Header.Set("Accept", "application/json")
	}
	r.Header.Set("Connection", "Keep-Alive")

	isAPIKey := false
	if auth != nil && auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["api_key"]); v != "" {
			isAPIKey = true
		}
	}
	if originator := strings.TrimSpace(ginHeaders.Get("Originator")); originator != "" {
		r.Header.Set("Originator", originator)
	} else if !isAPIKey {
		r.Header.Set("Originator", codexOriginator)
	}
	if !isAPIKey {
		if auth != nil && auth.Metadata != nil {
			if accountID, ok := auth.Metadata["account_id"].(string); ok {
				r.Header.Set("Chatgpt-Account-Id", accountID)
			}
		}
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(r, attrs)
}

func codexSessionHeaderValue(headers http.Header) string {
	if headers == nil {
		return ""
	}
	for key, values := range headers {
		if !strings.EqualFold(key, "Session_id") && !strings.EqualFold(key, "Session-Id") {
			continue
		}
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}
	return ""
}

func applyModelHeaderOverrides(headers http.Header, modelName string) {
	if headers == nil {
		return
	}
	overrides := registry.ModelOverrideHeaders(modelName)
	if len(overrides) == 0 {
		return
	}
	for key, value := range overrides {
		headers.Set(key, value)
	}
	if strings.Contains(headers.Get("User-Agent"), "Mac OS") && codexSessionHeaderValue(headers) == "" {
		headers.Set("Session_id", uuid.NewString())
	}
}

func newCodexStatusErr(statusCode int, body []byte) statusErr {
	errCode := statusCode
	if isCodexModelCapacityError(body) {
		errCode = http.StatusTooManyRequests
	}
	body = classifyCodexStatusError(errCode, body)
	err := statusErr{code: errCode, msg: string(body)}
	if retryAfter := parseCodexRetryAfter(errCode, body, time.Now()); retryAfter != nil {
		err.retryAfter = retryAfter
	}
	return err
}

func classifyCodexStatusError(statusCode int, body []byte) []byte {
	code, errType, ok := codexStatusErrorClassification(statusCode, body)
	if !ok {
		return body
	}
	message := gjson.GetBytes(body, "error.message").String()
	if message == "" {
		message = gjson.GetBytes(body, "message").String()
	}
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	if message == "" {
		message = http.StatusText(statusCode)
	}
	out := []byte(`{"error":{}}`)
	out, _ = sjson.SetBytes(out, "error.message", message)
	out, _ = sjson.SetBytes(out, "error.type", errType)
	out, _ = sjson.SetBytes(out, "error.code", code)
	return out
}

func codexStatusErrorClassification(statusCode int, body []byte) (code string, errType string, ok bool) {
	errorMessage := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.message").String()))
	if errorMessage == "" {
		errorMessage = strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "message").String()))
	}
	lower := strings.ToLower(strings.TrimSpace(string(body)))
	upstreamCode := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.code").String()))
	upstreamType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.type").String()))
	isInvalidRequest := upstreamType == "" || upstreamType == "invalid_request_error"

	switch {
	case statusCode == http.StatusRequestEntityTooLarge || upstreamCode == "context_length_exceeded" || upstreamCode == "context_too_large" || isInvalidRequest && (strings.Contains(errorMessage, "context length") || strings.Contains(errorMessage, "context_length") || strings.Contains(errorMessage, "maximum context") || strings.Contains(errorMessage, "too many tokens")):
		return "context_too_large", "invalid_request_error", true
	case strings.Contains(lower, "invalid signature in thinking block") || strings.Contains(lower, "invalid_encrypted_content"):
		return "thinking_signature_invalid", "invalid_request_error", true
	case upstreamCode == "previous_response_not_found" || strings.Contains(lower, "previous_response_not_found") || strings.Contains(lower, "previous_response_id") && strings.Contains(lower, "not found"):
		return "previous_response_not_found", "invalid_request_error", true
	case statusCode == http.StatusUnauthorized || upstreamType == "authentication_error" || upstreamCode == "invalid_api_key" || strings.Contains(lower, "invalid or expired token") || strings.Contains(lower, "refresh_token_reused"):
		return "auth_unavailable", "authentication_error", true
	default:
		return "", "", false
	}
}

func normalizeCodexInstructions(body []byte) []byte {
	instructions := strings.TrimSpace(gjson.GetBytes(body, "instructions").String())
	var moved []string

	body, movedFromInput := moveCodexSystemMessagesToInstructions(body, "input")
	moved = append(moved, movedFromInput...)
	body, movedFromMessages := moveCodexSystemMessagesToInstructions(body, "messages")
	moved = append(moved, movedFromMessages...)

	if len(moved) > 0 {
		parts := make([]string, 0, len(moved)+1)
		if instructions != "" {
			parts = append(parts, instructions)
		}
		parts = append(parts, moved...)
		instructions = strings.Join(parts, "\n\n")
	}
	body, _ = sjson.SetBytes(body, "instructions", instructions)
	return body
}

func moveCodexSystemMessagesToInstructions(body []byte, path string) ([]byte, []string) {
	itemsResult := gjson.GetBytes(body, path)
	if !itemsResult.Exists() || !itemsResult.IsArray() {
		return body, nil
	}

	items := itemsResult.Array()
	kept := make([][]byte, 0, len(items))
	moved := make([]string, 0)
	changed := false
	for _, item := range items {
		itemType := strings.TrimSpace(item.Get("type").String())
		role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
		if (itemType == "" || itemType == "message") && (role == "system" || role == "developer") {
			changed = true
			if text := codexInstructionTextFromMessage(item); text != "" {
				moved = append(moved, text)
			}
			continue
		}
		kept = append(kept, []byte(item.Raw))
	}
	if !changed {
		return body, nil
	}

	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, raw := range kept {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(raw)
	}
	buf.WriteByte(']')
	body, _ = sjson.SetRawBytes(body, path, buf.Bytes())
	return body, moved
}

func codexInstructionTextFromMessage(item gjson.Result) string {
	content := item.Get("content")
	if !content.Exists() || content.Type == gjson.Null {
		return ""
	}
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String())
	}
	if !content.IsArray() {
		return strings.TrimSpace(content.String())
	}

	parts := make([]string, 0)
	content.ForEach(func(_, part gjson.Result) bool {
		text := strings.TrimSpace(part.Get("text").String())
		if text == "" {
			text = strings.TrimSpace(part.Get("input_text").String())
		}
		if text == "" && part.Type == gjson.String {
			text = strings.TrimSpace(part.String())
		}
		if text != "" {
			parts = append(parts, text)
		}
		return true
	})
	return strings.Join(parts, "\n")
}

var imageGenToolJSON = []byte(`{"type":"image_generation","output_format":"png"}`)
var imageGenToolArrayJSON = []byte(`[{"type":"image_generation","output_format":"png"}]`)

func isCodexFreePlanAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Attributes["plan_type"]), "free")
}

func isCodexResponsesLiteRequest(body []byte, headers http.Header) bool {
	if strings.EqualFold(strings.TrimSpace(headers.Get(codexResponsesLiteHeader)), "true") {
		return true
	}
	value := gjson.GetBytes(body, codexResponsesLiteMetadata)
	if !value.Exists() {
		return false
	}
	return value.Type == gjson.True || value.Type == gjson.String && strings.EqualFold(strings.TrimSpace(value.String()), "true")
}

func ensureImageGenerationTool(body []byte, baseModel string, auth *cliproxyauth.Auth, headers http.Header) []byte {
	if isCodexResponsesLiteRequest(body, headers) {
		return body
	}
	if strings.HasSuffix(baseModel, "spark") {
		return body
	}
	if isCodexFreePlanAuth(auth) {
		return body
	}

	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() {
		body, _ = sjson.SetRawBytes(body, "tools", imageGenToolArrayJSON)
		return body
	}
	for _, t := range tools.Array() {
		if t.Get("type").String() == "image_generation" {
			return body
		}
	}
	body, _ = sjson.SetRawBytes(body, "tools.-1", imageGenToolJSON)
	return body
}

type codexReasoningReplayScope struct {
	modelName  string
	sessionKey string
}

func (s codexReasoningReplayScope) valid() bool {
	return strings.TrimSpace(s.modelName) != "" && strings.TrimSpace(s.sessionKey) != ""
}

func codexReasoningReplaySessionKeyFromPayload(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	if sessionID := strings.TrimSpace(gjson.GetBytes(payload, "client_metadata.x-codex-window-id").String()); sessionID != "" {
		return "window:" + sessionID
	}
	return ""
}

func codexReasoningReplaySessionKeyFromHeaders(headers http.Header) string {
	if headers == nil {
		return ""
	}
	for key, values := range headers {
		if !strings.EqualFold(key, "Session_id") && !strings.EqualFold(key, "Session-Id") {
			continue
		}
		for _, value := range values {
			if sessionID := strings.TrimSpace(value); sessionID != "" {
				return "session-id:" + sessionID
			}
		}
	}
	if windowID := strings.TrimSpace(headers.Get("X-Codex-Window-Id")); windowID != "" {
		return "window:" + windowID
	}
	return ""
}

func codexReasoningReplaySessionKey(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, body []byte) string {
	if from == sdktranslator.FromString("claude") {
		userID := strings.TrimSpace(gjson.GetBytes(req.Payload, "metadata.user_id").String())
		if strings.HasPrefix(userID, "{") {
			if sessionID := strings.TrimSpace(gjson.Get(userID, "session_id").String()); sessionID != "" {
				return "claude:" + sessionID
			}
		}
	}
	if key := codexReasoningReplaySessionKeyFromPayload(body); key != "" {
		return key
	}
	if key := codexReasoningReplaySessionKeyFromHeaders(opts.Headers); key != "" {
		return key
	}
	if key := codexReasoningReplaySessionKeyFromHeaders(reqToHeaders(ctx)); key != "" {
		return key
	}
	return ""
}

func codexReasoningReplayScopeFromRequest(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, body []byte) codexReasoningReplayScope {
	return codexReasoningReplayScope{
		modelName:  thinking.ParseSuffix(req.Model).ModelName,
		sessionKey: codexReasoningReplaySessionKey(ctx, from, req, opts, body),
	}
}

func applyCodexReasoningReplayCache(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, body []byte) ([]byte, codexReasoningReplayScope) {
	scope := codexReasoningReplayScopeFromRequest(ctx, from, req, opts, body)
	if !scope.valid() || from != sdktranslator.FromString("claude") {
		return body, scope
	}
	cachedItems, ok := internalcache.GetCodexReasoningReplayItems(scope.modelName, scope.sessionKey)
	if !ok || len(cachedItems) == 0 {
		return body, scope
	}
	if hasReasoningInput(body) {
		return body, scope
	}

	matchedOutputByCallID := claudeToolResultOutputsByCallID(req.Payload)
	replayItems := filterReplayItemsForClaudeOutputs(cachedItems, matchedOutputByCallID)
	if len(replayItems) == 0 {
		return body, scope
	}

	inputItems := gjson.GetBytes(body, "input").Array()
	insertAt := claudeReplayInsertIndex(inputItems)
	updated, err := insertReplayItems(body, replayItems, insertAt)
	if err != nil {
		return body, scope
	}
	return updated, scope
}

func cacheCodexReasoningReplayFromCompleted(scope codexReasoningReplayScope, completedData []byte) {
	if !scope.valid() {
		return
	}
	output := gjson.GetBytes(completedData, "response.output")
	if !output.IsArray() {
		return
	}
	items := make([][]byte, 0, len(output.Array()))
	for _, item := range output.Array() {
		if item.Type == gjson.JSON {
			items = append(items, []byte(item.Raw))
		}
	}
	internalcache.CacheCodexReasoningReplayItems(scope.modelName, scope.sessionKey, items)
}

func clearCodexReasoningReplayOnInvalidSignature(scope codexReasoningReplayScope, eventData []byte) {
	if !scope.valid() {
		return
	}
	if !strings.EqualFold(gjson.GetBytes(eventData, "type").String(), "response.failed") {
		return
	}
	if code, errType, ok := codexStatusErrorClassification(http.StatusBadRequest, eventData); ok && code == "thinking_signature_invalid" && errType == "invalid_request_error" {
		internalcache.DeleteCodexReasoningReplayItem(scope.modelName, scope.sessionKey)
	}
}

func newCodexFailedEventErr(eventData []byte) error {
	message := strings.TrimSpace(gjson.GetBytes(eventData, "response.error.message").String())
	if message == "" {
		message = strings.TrimSpace(gjson.GetBytes(eventData, "error.message").String())
	}
	if message == "" {
		message = strings.TrimSpace(gjson.GetBytes(eventData, "message").String())
	}
	if message == "" {
		message = strings.TrimSpace(string(eventData))
	}
	body := []byte(`{"error":{}}`)
	if errType := strings.TrimSpace(gjson.GetBytes(eventData, "response.error.type").String()); errType != "" {
		body, _ = sjson.SetBytes(body, "error.type", errType)
	}
	if code := strings.TrimSpace(gjson.GetBytes(eventData, "response.error.code").String()); code != "" {
		body, _ = sjson.SetBytes(body, "error.code", code)
	}
	body, _ = sjson.SetBytes(body, "error.message", message)
	return newCodexStatusErr(http.StatusBadRequest, body)
}

func publishCodexImageToolUsage(ctx context.Context, reporter *helps.UsageReporter, body []byte, eventData []byte) {
	if reporter == nil {
		return
	}
	detail, ok := helps.ParseCodexImageToolUsage(eventData)
	if !ok {
		return
	}
	reporter.PublishAdditionalModel(ctx, codexImageGenerationToolModel(body), detail)
}

func reqToHeaders(ctx context.Context) http.Header {
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		return ginCtx.Request.Header
	}
	return nil
}

func hasReasoningInput(body []byte) bool {
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() == "reasoning" {
			return true
		}
	}
	return false
}

func claudeReplayInsertIndex(items []gjson.Result) int {
	for index, item := range items {
		itemType := item.Get("type").String()
		if itemType == "message" && item.Get("role").String() == "assistant" {
			return index
		}
		if itemType == "function_call_output" {
			return index
		}
	}
	return 0
}

func insertReplayItems(body []byte, replayItems [][]byte, insertAt int) ([]byte, error) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		input = gjson.ParseBytes([]byte(`[]`))
	}
	arr := input.Array()
	parts := make([][]byte, 0, len(arr)+len(replayItems))
	for index, item := range arr {
		if index == insertAt {
			parts = append(parts, replayItems...)
		}
		parts = append(parts, []byte(item.Raw))
	}
	if insertAt >= len(arr) {
		parts = append(parts, replayItems...)
	}
	joined := []byte("[]")
	if len(parts) > 0 {
		var buf bytes.Buffer
		buf.WriteByte('[')
		for index, part := range parts {
			if index > 0 {
				buf.WriteByte(',')
			}
			buf.Write(part)
		}
		buf.WriteByte(']')
		joined = buf.Bytes()
	}
	return sjson.SetRawBytes(body, "input", joined)
}

func claudeToolResultOutputsByCallID(payload []byte) map[string][]byte {
	matched := make(map[string][]byte)
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return matched
	}
	for _, message := range messages.Array() {
		contents := message.Get("content")
		if !contents.IsArray() {
			continue
		}
		for _, content := range contents.Array() {
			if content.Get("type").String() != "tool_result" {
				continue
			}
			callID := shortenCodexReplayCallIDIfNeeded(content.Get("tool_use_id").String())
			if callID == "" {
				continue
			}
			output := []byte(`{"type":"function_call_output"}`)
			output, _ = sjson.SetBytes(output, "call_id", callID)
			if contentValue := content.Get("content"); contentValue.IsArray() {
				toolResultContent := []byte(`[]`)
				toolResultIndex := 0
				for _, entry := range contentValue.Array() {
					if entry.Get("type").String() != "text" {
						continue
					}
					text := entry.Get("text").String()
					if isResponsesContent(text) {
						for _, block := range gjson.Parse(text).Array() {
							toolResultContent, _ = sjson.SetRawBytes(toolResultContent, fmt.Sprintf("%d", toolResultIndex), []byte(block.Raw))
							toolResultIndex++
						}
						continue
					}
					toolResultContent, _ = sjson.SetBytes(toolResultContent, fmt.Sprintf("%d.type", toolResultIndex), "input_text")
					toolResultContent, _ = sjson.SetBytes(toolResultContent, fmt.Sprintf("%d.text", toolResultIndex), text)
					toolResultIndex++
				}
				if toolResultIndex > 0 {
					output, _ = sjson.SetRawBytes(output, "output", toolResultContent)
				}
			} else {
				text := content.Get("content").String()
				if isResponsesContent(text) {
					output, _ = sjson.SetRawBytes(output, "output", []byte(text))
				} else {
					output, _ = sjson.SetBytes(output, "output", text)
				}
			}
			matched[callID] = output
		}
	}
	return matched
}

func isResponsesContent(text string) bool {
	if !gjson.Valid(text) {
		return false
	}
	content := gjson.Parse(text)
	if !content.IsArray() {
		return false
	}
	blocks := content.Array()
	if len(blocks) == 0 {
		return false
	}
	if !blocks[0].Get("type").Exists() {
		return false
	}
	for _, block := range blocks {
		switch block.Get("type").String() {
		case "input_text", "input_image", "text", "image", "output_text":
			continue
		default:
			return false
		}
	}
	return true
}

func filterReplayItemsForClaudeOutputs(cachedItems [][]byte, outputs map[string][]byte) [][]byte {
	replayItems := make([][]byte, 0, len(cachedItems))
	for _, item := range cachedItems {
		itemType := gjson.GetBytes(item, "type").String()
		switch itemType {
		case "reasoning":
			replayItems = append(replayItems, item)
		case "function_call":
			callID := shortenCodexReplayCallIDIfNeeded(gjson.GetBytes(item, "call_id").String())
			if _, ok := outputs[callID]; !ok {
				continue
			}
			updated := item
			updated, _ = sjson.SetBytes(updated, "call_id", callID)
			replayItems = append(replayItems, updated)
		case "custom_tool_call":
			continue
		}
	}
	return replayItems
}

func shortenCodexReplayCallIDIfNeeded(id string) string {
	const limit = 64
	id = strings.TrimSpace(id)
	if len(id) <= limit {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	suffix := "_" + hex.EncodeToString(sum[:8])
	prefixLen := limit - len(suffix)
	if prefixLen <= 0 {
		return suffix[len(suffix)-limit:]
	}
	return id[:prefixLen] + suffix
}

func codexImageGenerationToolModel(body []byte) string {
	tools := gjson.GetBytes(body, "tools")
	if tools.IsArray() {
		for _, tool := range tools.Array() {
			if tool.Get("type").String() != "image_generation" {
				continue
			}
			if model := strings.TrimSpace(tool.Get("model").String()); model != "" {
				return model
			}
			break
		}
	}
	return codexDefaultImageToolModel
}

func isCodexModelCapacityError(errorBody []byte) bool {
	if len(errorBody) == 0 {
		return false
	}
	candidates := []string{
		gjson.GetBytes(errorBody, "error.message").String(),
		gjson.GetBytes(errorBody, "message").String(),
		string(errorBody),
	}
	for _, candidate := range candidates {
		lower := strings.ToLower(strings.TrimSpace(candidate))
		if lower == "" {
			continue
		}
		if strings.Contains(lower, "selected model is at capacity") ||
			strings.Contains(lower, "model is at capacity. please try a different model") {
			return true
		}
	}
	return false
}

func parseCodexRetryAfter(statusCode int, errorBody []byte, now time.Time) *time.Duration {
	if statusCode != http.StatusTooManyRequests || len(errorBody) == 0 {
		return nil
	}
	if strings.TrimSpace(gjson.GetBytes(errorBody, "error.type").String()) != "usage_limit_reached" {
		return nil
	}
	if resetsAt := gjson.GetBytes(errorBody, "error.resets_at").Int(); resetsAt > 0 {
		resetAtTime := time.Unix(resetsAt, 0)
		if resetAtTime.After(now) {
			retryAfter := resetAtTime.Sub(now)
			return &retryAfter
		}
	}
	if resetsInSeconds := gjson.GetBytes(errorBody, "error.resets_in_seconds").Int(); resetsInSeconds > 0 {
		retryAfter := time.Duration(resetsInSeconds) * time.Second
		return &retryAfter
	}
	return nil
}

func codexCreds(a *cliproxyauth.Auth) (apiKey, baseURL string) {
	return helps.ResolveAPIKeyAndBaseURL(a)
}

func (e *CodexExecutor) resolveCodexConfig(auth *cliproxyauth.Auth) *config.CodexKey {
	if auth == nil || e.cfg == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range e.cfg.CodexKey {
		entry := &e.cfg.CodexKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && attrBase != "" {
			if strings.EqualFold(cfgKey, attrKey) && strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	if attrKey != "" {
		for i := range e.cfg.CodexKey {
			entry := &e.cfg.CodexKey[i]
			if strings.EqualFold(strings.TrimSpace(entry.APIKey), attrKey) {
				return entry
			}
		}
	}
	return nil
}
