package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	openAICompatImageHandlerType            = "openai-image"
	openAICompatImagesGenerationsPath       = "/images/generations"
	openAICompatImagesEditsPath             = "/images/edits"
	openAICompatDefaultImageEndpoint        = openAICompatImagesGenerationsPath
	openAICompatMultipartMemory       int64 = 32 << 20
)

// OpenAICompatExecutor implements a stateless executor for OpenAI-compatible providers.
// It performs request/response translation and executes against the provider base URL
// using per-auth credentials (API key) and per-auth HTTP transport (proxy) from context.
type OpenAICompatExecutor struct {
	provider string
	cfg      *config.Config
}

// NewOpenAICompatExecutor creates an executor bound to a provider key (e.g., "openrouter").
func NewOpenAICompatExecutor(provider string, cfg *config.Config) *OpenAICompatExecutor {
	return &OpenAICompatExecutor{provider: provider, cfg: cfg}
}

// Identifier implements cliproxyauth.ProviderExecutor.
func (e *OpenAICompatExecutor) Identifier() string { return e.provider }

// PrepareRequest injects OpenAI-compatible credentials into the outgoing HTTP request.
func (e *OpenAICompatExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	_, apiKey := e.resolveCredentials(auth)
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

// HttpRequest injects OpenAI-compatible credentials into the request and executes it.
func (e *OpenAICompatExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("openai compat executor: request is nil")
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

func (e *OpenAICompatExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if endpointPath := openAICompatImageEndpointPath(opts); endpointPath != "" {
		return e.executeImages(ctx, auth, req, opts, endpointPath)
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName

	// Force-stream owns its own usage reporter. Branch before creating one here
	// so success/failure are not double-counted across two reporters.
	if opts.Alt != "responses/compact" {
		if compat := e.resolveCompatConfig(auth); compat != nil && compat.ForceStream {
			return e.executeChatCompletionsViaForcedStream(ctx, auth, req, opts)
		}
	}

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	endpoint := "/chat/completions"
	if opts.Alt == "responses/compact" {
		to = sdktranslator.FromString("openai-response")
		endpoint = "/responses/compact"
	}
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, opts.Stream)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, opts.Stream)

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", translated, originalTranslated, requestedModel, requestPath, opts.Headers)
	if opts.Alt == "responses/compact" {
		if updated, errDelete := sjson.DeleteBytes(translated, "stream"); errDelete == nil {
			translated = updated
		}
	}

	url := helps.JoinBaseURL(baseURL, endpoint)
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if apiKey != "" {
		headers.Set("Authorization", "Bearer "+apiKey)
	}
	headers.Set("User-Agent", "cli-proxy-openai-compat")
	tmpReq := &http.Request{Header: headers}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(tmpReq, attrs)
	headers = tmpReq.Header

	_, body, respHeaders, errDo := helps.DoJSON(ctx, e.cfg, helps.UpstreamRequest{
		Provider: e.Identifier(),
		Auth:     auth,
		Method:   http.MethodPost,
		URL:      url,
		Headers:  headers,
		Body:     translated,
	})
	if errDo != nil {
		return resp, toStatusErr(errDo)
	}
	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	// Ensure we at least record the request even if upstream doesn't return usage
	reporter.EnsurePublished(ctx)
	// Translate response back to source format when needed
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, body, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: respHeaders}
	return resp, nil
}

func (e *OpenAICompatExecutor) executeChatCompletionsViaForcedStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	// Match Execute/ExecuteStream so early credential/translate/HTTP failures are counted.
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return resp, err
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, opts.Stream)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, opts.Stream)

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", translated, originalTranslated, requestedModel, requestPath, opts.Headers)
	translated, _ = sjson.SetBytes(translated, "stream", true)
	translated, _ = sjson.SetBytes(translated, "stream_options.include_usage", true)

	url := helps.JoinBaseURL(baseURL, "/chat/completions")
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if apiKey != "" {
		headers.Set("Authorization", "Bearer "+apiKey)
	}
	headers.Set("User-Agent", "cli-proxy-openai-compat")
	tmpReq := &http.Request{Header: headers}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(tmpReq, attrs)
	tmpReq.Header.Set("Accept", "text/event-stream")
	tmpReq.Header.Set("Cache-Control", "no-cache")
	headers = tmpReq.Header

	httpResp, errDo := helps.DoStream(ctx, e.cfg, helps.UpstreamRequest{
		Provider: e.Identifier(),
		Auth:     auth,
		Method:   http.MethodPost,
		URL:      url,
		Headers:  headers,
		Body:     translated,
	})
	if errDo != nil {
		return resp, toStatusErr(errDo)
	}
	defer helps.CloseResponseBody(e.Identifier(), httpResp.Body)

	aggregator := newOpenAIChatStreamAggregator(baseModel)
	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(nil, 52_428_800) // 50MB
	for scanner.Scan() {
		line := scanner.Bytes()
		helps.AppendAPIResponseChunk(ctx, e.cfg, line)
		if detail, ok := helps.ParseOpenAIStreamUsage(line); ok {
			reporter.Publish(ctx, detail)
		}
		trimmedLine := bytes.TrimSpace(line)
		if len(trimmedLine) == 0 {
			continue
		}
		if !bytes.HasPrefix(trimmedLine, []byte("data:")) {
			if bytes.HasPrefix(trimmedLine, []byte(":")) || bytes.HasPrefix(trimmedLine, []byte("event:")) ||
				bytes.HasPrefix(trimmedLine, []byte("id:")) || bytes.HasPrefix(trimmedLine, []byte("retry:")) {
				continue
			}
			if bytes.HasPrefix(trimmedLine, []byte("{")) || bytes.HasPrefix(trimmedLine, []byte("[")) {
				streamErr := statusErr{code: http.StatusBadGateway, msg: string(trimmedLine)}
				helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
				reporter.PublishFailure(ctx, streamErr)
				return resp, streamErr
			}
			continue
		}
		data := bytes.TrimSpace(trimmedLine[len("data:"):])
		if bytes.Equal(data, []byte("[DONE]")) {
			break
		}
		if errAdd := aggregator.Add(data); errAdd != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errAdd)
			reporter.PublishFailure(ctx, errAdd)
			return resp, errAdd
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errScan)
		reporter.PublishFailure(ctx, errScan)
		return resp, errScan
	}

	body, err := aggregator.Build()
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		reporter.PublishFailure(ctx, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	reporter.EnsurePublished(ctx)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, body, &param)
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}

func (e *OpenAICompatExecutor) executeImages(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, endpointPath string) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return resp, err
	}

	payload, contentType, errPrepare := prepareOpenAICompatImagesPayload(req.Payload, baseModel, opts.Headers.Get("Content-Type"), false)
	if errPrepare != nil {
		err = errPrepare
		return resp, err
	}
	if contentType == "" {
		contentType = "application/json"
	}

	url := helps.JoinBaseURL(baseURL, endpointPath)
	headers := make(http.Header)
	headers.Set("Content-Type", contentType)
	if apiKey != "" {
		headers.Set("Authorization", "Bearer "+apiKey)
	}
	headers.Set("User-Agent", "cli-proxy-openai-compat")
	tmpReq := &http.Request{Header: headers}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(tmpReq, attrs)
	headers = tmpReq.Header

	_, body, respHeaders, errDo := helps.DoJSON(ctx, e.cfg, helps.UpstreamRequest{
		Provider: e.Identifier(),
		Auth:     auth,
		Method:   http.MethodPost,
		URL:      url,
		Headers:  headers,
		Body:     payload,
	})
	if errDo != nil {
		if ue, ok := errDo.(helps.UpstreamStatusError); ok {
			helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", ue.Code, helps.SummarizeErrorBody("application/json", []byte(ue.Msg)))
			err = statusErr{code: ue.Code, msg: ue.Msg}
			return resp, err
		}
		return resp, errDo
	}

	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	reporter.EnsurePublished(ctx)
	resp = cliproxyexecutor.Response{Payload: body, Headers: respHeaders}
	return resp, nil
}

func (e *OpenAICompatExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if endpointPath := openAICompatImageEndpointPath(opts); endpointPath != "" {
		return e.executeImagesStream(ctx, auth, req, opts, endpointPath)
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return nil, err
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, true)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", translated, originalTranslated, requestedModel, requestPath, opts.Headers)

	// Request usage data in the final streaming chunk so that token statistics
	// are captured even when the upstream is an OpenAI-compatible provider.
	translated, _ = sjson.SetBytes(translated, "stream_options.include_usage", true)

	url := helps.JoinBaseURL(baseURL, "/chat/completions")
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if apiKey != "" {
		headers.Set("Authorization", "Bearer "+apiKey)
	}
	headers.Set("User-Agent", "cli-proxy-openai-compat")
	tmpReq := &http.Request{Header: headers}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(tmpReq, attrs)
	tmpReq.Header.Set("Accept", "text/event-stream")
	tmpReq.Header.Set("Cache-Control", "no-cache")
	headers = tmpReq.Header

	httpResp, errDo := helps.DoStream(ctx, e.cfg, helps.UpstreamRequest{
		Provider: e.Identifier(),
		Auth:     auth,
		Method:   http.MethodPost,
		URL:      url,
		Headers:  headers,
		Body:     translated,
	})
	if errDo != nil {
		return nil, toStatusErr(errDo)
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer helps.CloseResponseBody(e.Identifier(), httpResp.Body)
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := helps.ParseOpenAIStreamUsage(line); ok {
				reporter.Publish(ctx, detail)
			}
			trimmedLine := bytes.TrimSpace(line)
			if len(trimmedLine) == 0 {
				continue
			}

			if !bytes.HasPrefix(trimmedLine, []byte("data:")) {
				if bytes.HasPrefix(trimmedLine, []byte(":")) || bytes.HasPrefix(trimmedLine, []byte("event:")) ||
					bytes.HasPrefix(trimmedLine, []byte("id:")) || bytes.HasPrefix(trimmedLine, []byte("retry:")) {
					continue
				}
				if bytes.HasPrefix(trimmedLine, []byte("{")) || bytes.HasPrefix(trimmedLine, []byte("[")) {
					streamErr := statusErr{code: http.StatusBadGateway, msg: string(trimmedLine)}
					helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
					reporter.PublishFailure(ctx, streamErr)
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
					case <-ctx.Done():
					}
					return
				}
				continue
			}

			// OpenAI-compatible streams must use SSE data lines.
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, bytes.Clone(trimmedLine), &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		} else {
			// In case the upstream close the stream without a terminal [DONE] marker.
			// Feed a synthetic done marker through the translator so pending
			// response.completed events are still emitted exactly once.
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, []byte("data: [DONE]"), &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		// Ensure we record the request if no usage chunk was ever seen
		reporter.EnsurePublished(ctx)
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *OpenAICompatExecutor) executeImagesStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, endpointPath string) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return nil, err
	}

	payload, contentType, errPrepare := prepareOpenAICompatImagesPayload(req.Payload, baseModel, opts.Headers.Get("Content-Type"), true)
	if errPrepare != nil {
		err = errPrepare
		return nil, err
	}
	if contentType == "" {
		contentType = "application/json"
	}

	url := helps.JoinBaseURL(baseURL, endpointPath)
	headers := make(http.Header)
	headers.Set("Content-Type", contentType)
	headers.Set("Accept", "text/event-stream")
	headers.Set("Cache-Control", "no-cache")
	if apiKey != "" {
		headers.Set("Authorization", "Bearer "+apiKey)
	}
	headers.Set("User-Agent", "cli-proxy-openai-compat")
	tmpReq := &http.Request{Header: headers}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(tmpReq, attrs)
	headers = tmpReq.Header

	httpResp, errDo := helps.DoStream(ctx, e.cfg, helps.UpstreamRequest{
		Provider: e.Identifier(),
		Auth:     auth,
		Method:   http.MethodPost,
		URL:      url,
		Headers:  headers,
		Body:     payload,
	})
	if errDo != nil {
		return nil, toStatusErr(errDo)
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			helps.CloseResponseBody(e.Identifier(), httpResp.Body)
			reporter.EnsurePublished(ctx)
		}()
		buffer := make([]byte, 32*1024)
		for {
			n, errRead := httpResp.Body.Read(buffer)
			if n > 0 {
				chunk := bytes.Clone(buffer[:n])
				helps.AppendAPIResponseChunk(ctx, e.cfg, chunk)
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
				case <-ctx.Done():
					return
				}
			}
			if errRead != nil {
				if errRead != io.EOF {
					helps.RecordAPIResponseError(ctx, e.cfg, errRead)
					reporter.PublishFailure(ctx, errRead)
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: errRead}:
					case <-ctx.Done():
					}
				}
				return
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *OpenAICompatExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	modelForCounting := baseModel

	translated, err := thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	enc, err := helps.TokenizerForModel(modelForCounting)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat executor: tokenizer init failed: %w", err)
	}

	count, err := helps.CountOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat executor: token counting failed: %w", err)
	}

	usageJSON := helps.BuildOpenAIUsageJSON(count)
	translatedUsage := sdktranslator.TranslateTokenCount(ctx, to, from, count, usageJSON)
	return cliproxyexecutor.Response{Payload: translatedUsage}, nil
}

// Refresh is a no-op for API-key based compatibility providers.
func (e *OpenAICompatExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("openai compat executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	return auth, nil
}

type openAIChatStreamAggregator struct {
	id      string
	created int64
	model   string
	usage   string
	choices map[int]*openAIChatAggregatedChoice
}

type openAIChatAggregatedChoice struct {
	index           int
	role            string
	content         strings.Builder
	contentBuffer   translatorcommon.ContentBlockTextBuffer
	reasoning       strings.Builder
	reasoningBuffer translatorcommon.ContentBlockTextBuffer
	finishReason    string
	toolCalls       map[int]*openAIChatAggregatedToolCall
}

type openAIChatAggregatedToolCall struct {
	index     int
	id        string
	callType  string
	name      string
	arguments strings.Builder
}

func newOpenAIChatStreamAggregator(model string) *openAIChatStreamAggregator {
	return &openAIChatStreamAggregator{
		model:   model,
		choices: make(map[int]*openAIChatAggregatedChoice),
	}
}

func (a *openAIChatStreamAggregator) Add(rawJSON []byte) error {
	if len(bytes.TrimSpace(rawJSON)) == 0 {
		return nil
	}
	if !gjson.ValidBytes(rawJSON) {
		return fmt.Errorf("invalid OpenAI stream data JSON: %q", string(rawJSON))
	}
	root := gjson.ParseBytes(rawJSON)
	if id := root.Get("id"); id.Exists() && a.id == "" {
		a.id = id.String()
	}
	if created := root.Get("created"); created.Exists() && a.created == 0 {
		a.created = created.Int()
	}
	if model := root.Get("model"); model.Exists() && a.model == "" {
		a.model = model.String()
	}
	if usage := root.Get("usage"); usage.Exists() && usage.Type != gjson.Null {
		a.usage = usage.Raw
	}
	choices := root.Get("choices")
	if !choices.Exists() || !choices.IsArray() {
		return nil
	}
	choices.ForEach(func(_, choice gjson.Result) bool {
		index := int(choice.Get("index").Int())
		aggregated := a.choice(index)
		delta := choice.Get("delta")
		if role := delta.Get("role"); role.Exists() && role.String() != "" {
			aggregated.role = role.String()
		}
		if content := delta.Get("content"); content.Exists() {
			aggregated.content.WriteString(aggregated.contentBuffer.Text(content))
		}
		if reasoning := delta.Get("reasoning_content"); reasoning.Exists() {
			aggregated.reasoning.WriteString(aggregated.reasoningBuffer.Text(reasoning))
		}
		if finishReason := choice.Get("finish_reason"); finishReason.Exists() && finishReason.Type != gjson.Null {
			aggregated.finishReason = finishReason.String()
		}
		toolCalls := delta.Get("tool_calls")
		if toolCalls.Exists() && toolCalls.IsArray() {
			toolCalls.ForEach(func(_, toolCall gjson.Result) bool {
				toolIndex := int(toolCall.Get("index").Int())
				aggregatedTool := aggregated.toolCall(toolIndex)
				if id := toolCall.Get("id"); id.Exists() && id.String() != "" {
					aggregatedTool.id = id.String()
				}
				if callType := toolCall.Get("type"); callType.Exists() && callType.String() != "" {
					aggregatedTool.callType = callType.String()
				}
				function := toolCall.Get("function")
				if name := function.Get("name"); name.Exists() && name.String() != "" {
					aggregatedTool.name = name.String()
				}
				if arguments := function.Get("arguments"); arguments.Exists() {
					aggregatedTool.arguments.WriteString(arguments.String())
				}
				return true
			})
		}
		return true
	})
	return nil
}

func (a *openAIChatStreamAggregator) Build() ([]byte, error) {
	if len(a.choices) == 0 && a.usage == "" {
		return nil, fmt.Errorf("openai compat executor: forced stream returned no chat completion chunks")
	}
	out := []byte(`{"id":"","object":"chat.completion","created":0,"model":"","choices":[]}`)
	out, _ = sjson.SetBytes(out, "id", a.id)
	out, _ = sjson.SetBytes(out, "created", a.created)
	out, _ = sjson.SetBytes(out, "model", a.model)
	choiceIndexes := make([]int, 0, len(a.choices))
	for index := range a.choices {
		choiceIndexes = append(choiceIndexes, index)
	}
	sort.Ints(choiceIndexes)
	for _, index := range choiceIndexes {
		choice := a.choices[index]
		choice.content.WriteString(choice.contentBuffer.Flush())
		choice.reasoning.WriteString(choice.reasoningBuffer.Flush())
		choiceJSON := []byte(`{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}`)
		choiceJSON, _ = sjson.SetBytes(choiceJSON, "index", choice.index)
		role := choice.role
		if role == "" {
			role = "assistant"
		}
		choiceJSON, _ = sjson.SetBytes(choiceJSON, "message.role", role)
		choiceJSON, _ = sjson.SetBytes(choiceJSON, "message.content", choice.content.String())
		if choice.reasoning.Len() > 0 {
			choiceJSON, _ = sjson.SetBytes(choiceJSON, "message.reasoning_content", choice.reasoning.String())
		}
		finishReason := choice.finishReason
		if finishReason == "" {
			finishReason = "stop"
		}
		choiceJSON, _ = sjson.SetBytes(choiceJSON, "finish_reason", finishReason)
		if len(choice.toolCalls) > 0 {
			toolIndexes := make([]int, 0, len(choice.toolCalls))
			for toolIndex := range choice.toolCalls {
				toolIndexes = append(toolIndexes, toolIndex)
			}
			sort.Ints(toolIndexes)
			for outIndex, toolIndex := range toolIndexes {
				toolCall := choice.toolCalls[toolIndex]
				path := fmt.Sprintf("message.tool_calls.%d", outIndex)
				choiceJSON, _ = sjson.SetBytes(choiceJSON, path+".id", toolCall.id)
				callType := toolCall.callType
				if callType == "" {
					callType = "function"
				}
				choiceJSON, _ = sjson.SetBytes(choiceJSON, path+".type", callType)
				choiceJSON, _ = sjson.SetBytes(choiceJSON, path+".function.name", toolCall.name)
				choiceJSON, _ = sjson.SetBytes(choiceJSON, path+".function.arguments", toolCall.arguments.String())
			}
		}
		out, _ = sjson.SetRawBytes(out, "choices.-1", choiceJSON)
	}
	if a.usage != "" {
		out, _ = sjson.SetRawBytes(out, "usage", []byte(a.usage))
	}
	return out, nil
}

func (a *openAIChatStreamAggregator) choice(index int) *openAIChatAggregatedChoice {
	if choice, ok := a.choices[index]; ok {
		return choice
	}
	choice := &openAIChatAggregatedChoice{
		index:     index,
		toolCalls: make(map[int]*openAIChatAggregatedToolCall),
	}
	a.choices[index] = choice
	return choice
}

func (c *openAIChatAggregatedChoice) toolCall(index int) *openAIChatAggregatedToolCall {
	if toolCall, ok := c.toolCalls[index]; ok {
		return toolCall
	}
	toolCall := &openAIChatAggregatedToolCall{index: index}
	c.toolCalls[index] = toolCall
	return toolCall
}

func openAICompatImageEndpointPath(opts cliproxyexecutor.Options) string {
	if opts.SourceFormat.String() != openAICompatImageHandlerType {
		return ""
	}
	path := helps.PayloadRequestPath(opts)
	if strings.HasSuffix(path, "/images/edits") {
		return openAICompatImagesEditsPath
	}
	if strings.HasSuffix(path, "/images/generations") {
		return openAICompatImagesGenerationsPath
	}
	return openAICompatDefaultImageEndpoint
}

func prepareOpenAICompatImagesPayload(payload []byte, model string, contentType string, stream bool) ([]byte, string, error) {
	model = strings.TrimSpace(model)
	contentType = strings.TrimSpace(contentType)
	if json.Valid(payload) {
		if model != "" {
			payload, _ = sjson.SetBytes(payload, "model", model)
		}
		if stream {
			payload, _ = sjson.SetBytes(payload, "stream", true)
		} else {
			payload, _ = sjson.DeleteBytes(payload, "stream")
		}
		return payload, "application/json", nil
	}

	mediaType, params, errParse := mime.ParseMediaType(contentType)
	if errParse != nil || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(mediaType)), "multipart/") {
		return payload, contentType, nil
	}
	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return nil, "", fmt.Errorf("multipart boundary is missing")
	}
	return rewriteOpenAICompatImagesMultipartPayload(payload, model, boundary, stream)
}

func cloneOpenAICompatMIMEHeader(src textproto.MIMEHeader) textproto.MIMEHeader {
	dst := make(textproto.MIMEHeader, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func rewriteOpenAICompatImagesMultipartPayload(payload []byte, model string, boundary string, stream bool) ([]byte, string, error) {
	reader := multipart.NewReader(bytes.NewReader(payload), boundary)
	form, errRead := reader.ReadForm(openAICompatMultipartMemory)
	if errRead != nil {
		return nil, "", fmt.Errorf("read multipart form failed: %w", errRead)
	}
	defer func() {
		if errRemove := form.RemoveAll(); errRemove != nil {
			log.Errorf("openai compat executor: remove multipart form files error: %v", errRemove)
		}
	}()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if model != "" {
		if errWrite := writer.WriteField("model", model); errWrite != nil {
			return nil, "", fmt.Errorf("write model field failed: %w", errWrite)
		}
	}
	if stream {
		if errWrite := writer.WriteField("stream", "true"); errWrite != nil {
			return nil, "", fmt.Errorf("write stream field failed: %w", errWrite)
		}
	}
	for key, values := range form.Value {
		if key == "model" || key == "stream" {
			continue
		}
		for _, value := range values {
			if errWrite := writer.WriteField(key, value); errWrite != nil {
				return nil, "", fmt.Errorf("write form field %s failed: %w", key, errWrite)
			}
		}
	}
	for key, files := range form.File {
		for _, fileHeader := range files {
			if fileHeader == nil {
				continue
			}
			header := cloneOpenAICompatMIMEHeader(fileHeader.Header)
			header.Set("Content-Disposition", multipart.FileContentDisposition(key, fileHeader.Filename))
			if header.Get("Content-Type") == "" {
				header.Set("Content-Type", "application/octet-stream")
			}
			part, errCreate := writer.CreatePart(header)
			if errCreate != nil {
				return nil, "", fmt.Errorf("create file field %s failed: %w", key, errCreate)
			}
			src, errOpen := fileHeader.Open()
			if errOpen != nil {
				return nil, "", fmt.Errorf("open upload file failed: %w", errOpen)
			}
			_, errCopy := io.Copy(part, src)
			if errClose := src.Close(); errClose != nil {
				log.Errorf("openai compat executor: close upload file error: %v", errClose)
				if errCopy == nil {
					errCopy = errClose
				}
			}
			if errCopy != nil {
				return nil, "", fmt.Errorf("copy upload file failed: %w", errCopy)
			}
		}
	}
	if errClose := writer.Close(); errClose != nil {
		return nil, "", fmt.Errorf("close multipart writer failed: %w", errClose)
	}
	return body.Bytes(), writer.FormDataContentType(), nil
}

func (e *OpenAICompatExecutor) resolveCredentials(auth *cliproxyauth.Auth) (baseURL, apiKey string) {
	apiKey, baseURL = helps.ResolveAPIKeyAndBaseURL(auth)
	return baseURL, apiKey
}

func (e *OpenAICompatExecutor) resolveCompatConfig(auth *cliproxyauth.Auth) *config.OpenAICompatibility {
	if auth == nil || e.cfg == nil {
		return nil
	}
	candidates := make([]string, 0, 3)
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["compat_name"]); v != "" {
			candidates = append(candidates, v)
		}
		if v := strings.TrimSpace(auth.Attributes["provider_key"]); v != "" {
			candidates = append(candidates, v)
		}
	}
	if v := strings.TrimSpace(auth.Provider); v != "" {
		candidates = append(candidates, v)
	}
	for i := range e.cfg.OpenAICompatibility {
		compat := &e.cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		for _, candidate := range candidates {
			if candidate != "" && strings.EqualFold(strings.TrimSpace(candidate), compat.Name) {
				return compat
			}
		}
	}
	return nil
}

func (e *OpenAICompatExecutor) overrideModel(payload []byte, model string) []byte {
	if len(payload) == 0 || model == "" {
		return payload
	}
	payload, _ = sjson.SetBytes(payload, "model", model)
	return payload
}
