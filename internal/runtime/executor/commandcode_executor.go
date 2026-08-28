package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/commandcode"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	ccwire "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/commandcode"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// CommandCodeExecutor handles requests to the Command Code API.
type CommandCodeExecutor struct {
	cfg *config.Config
}

// NewCommandCodeExecutor creates a new Command Code executor instance.
func NewCommandCodeExecutor(cfg *config.Config) *CommandCodeExecutor {
	return &CommandCodeExecutor{cfg: cfg}
}

// Identifier returns the unique identifier for this executor.
func (e *CommandCodeExecutor) Identifier() string { return "commandcode" }

// PrepareRequest prepares the HTTP request before execution.
func (e *CommandCodeExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	apiKey := commandCodeAPIKey(auth)
	if strings.TrimSpace(apiKey) == "" {
		return fmt.Errorf("commandcode: missing API key")
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest executes a raw HTTP request.
func (e *CommandCodeExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("commandcode executor: request is nil")
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

// Execute performs a non-streaming request.
func (e *CommandCodeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	apiKey := commandCodeAPIKey(auth)
	if apiKey == "" {
		return resp, fmt.Errorf("commandcode: missing API key")
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("commandcode")

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayloadSource, true)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	translated = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", translated, originalTranslated, requestedModel, "")

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}
	translated = applySystemPrompt(translated, from.String())
	translated = forceStreamFlag(translated, true)

	headers := e.buildHeaders(auth, apiKey)
	headers.Set("Accept", "application/x-ndjson")

	httpResp, errDo := helps.DoStream(ctx, e.cfg, helps.UpstreamRequest{
		Provider: e.Identifier(),
		Auth:     auth,
		Method:   http.MethodPost,
		URL:      commandcode.BaseURL + ccwire.GenerateEndpoint,
		Headers:  headers,
		Body:     translated,
	})
	if errDo != nil {
		return resp, toStatusErr(errDo)
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("commandcode: failed to close response body: %v", errClose)
		}
	}()

	var allChunks bytes.Buffer
	var streamErr error
	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), streamScannerBuffer)
	for scanner.Scan() {
		line := scanner.Bytes()
		helps.AppendAPIResponseChunk(ctx, e.cfg, line)
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if allChunks.Len() > 0 {
			allChunks.WriteByte('\n')
		}
		allChunks.Write(line)
		eventType := gjson.GetBytes(line, "type").String()
		switch eventType {
		case "finish":
			if detail := parseWireUsage(line); detail.InputTokens > 0 || detail.OutputTokens > 0 {
				reporter.Publish(ctx, detail)
			}
		case ccwire.EventError, ccwire.EventAbort:
			// Terminal failure events must surface instead of translating to an
			// empty message.
			msg := commandCodeStreamErrorMessage(line)
			log.Warnf("commandcode: upstream stream %s event: %s", eventType, msg)
			streamErr = fmt.Errorf("commandcode: upstream stream %s: %s", eventType, msg)
		}
		if streamErr != nil {
			break
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errScan)
		reporter.PublishFailure(ctx)
		return resp, errScan
	}
	if streamErr != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
		return resp, streamErr
	}
	if allChunks.Len() == 0 {
		log.Warnf("commandcode: upstream returned an empty response body (status=%d)", httpResp.StatusCode)
		return resp, fmt.Errorf("commandcode: empty upstream response")
	}
	reporter.EnsurePublished(ctx)

	body := allChunks.Bytes()
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, body, &param)
	resp = cliproxyexecutor.Response{Payload: []byte(out)}
	return resp, nil
}

// ExecuteStream performs a streaming request.
func (e *CommandCodeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	apiKey := commandCodeAPIKey(auth)
	if apiKey == "" {
		return nil, fmt.Errorf("commandcode: missing API key")
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("commandcode")

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayloadSource, true)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	translated = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", translated, originalTranslated, requestedModel, "")

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}
	translated = applySystemPrompt(translated, from.String())
	translated = forceStreamFlag(translated, true)

	headers := e.buildHeaders(auth, apiKey)
	headers.Set("Accept", "application/x-ndjson")

	httpResp, errDo := helps.DoStream(ctx, e.cfg, helps.UpstreamRequest{
		Provider: e.Identifier(),
		Auth:     auth,
		Method:   http.MethodPost,
		URL:      commandcode.BaseURL + ccwire.GenerateEndpoint,
		Headers:  headers,
		Body:     translated,
	})
	if errDo != nil {
		return nil, toStatusErr(errDo)
	}

	out := make(chan cliproxyexecutor.StreamChunk, 16)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("commandcode: failed to close response body: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), streamScannerBuffer)
		var param any
		var bodyExcerpt bytes.Buffer
		emitted := false
		for scanner.Scan() {
			line := bytes.Clone(scanner.Bytes())
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			if bodyExcerpt.Len() < commandCodeBodyLogLimit {
				if bodyExcerpt.Len() > 0 {
					bodyExcerpt.WriteByte('\n')
				}
				bodyExcerpt.Write(line)
			}
			eventType := gjson.GetBytes(line, "type").String()
			if eventType == ccwire.EventError || eventType == ccwire.EventAbort {
				// Terminal failure events must surface instead of being dropped,
				// otherwise the stream closes silently and the client only sees
				// a generic empty_stream error.
				msg := commandCodeStreamErrorMessage(line)
				log.Warnf("commandcode: upstream stream %s event: %s", eventType, msg)
				reporter.PublishFailure(ctx)
				out <- cliproxyexecutor.StreamChunk{Err: fmt.Errorf("commandcode: upstream stream %s: %s", eventType, msg)}
				return
			}
			if eventType == "finish" {
				if detail := parseWireUsage(line); detail.InputTokens > 0 || detail.OutputTokens > 0 {
					reporter.Publish(ctx, detail)
				}
			}
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, line, &param)
			for i := range chunks {
				payload := bytes.Clone(chunks[i])
				if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
					continue
				}
				out <- cliproxyexecutor.StreamChunk{Payload: payload}
				emitted = true
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
		} else {
			if !emitted {
				log.Warnf("commandcode: upstream stream closed without payload (status=%d, content_type=%s, body=%q)", httpResp.StatusCode, httpResp.Header.Get("Content-Type"), bodyExcerpt.String())
			}
			if from == sdktranslator.FromString("openai") || from.String() == "openai" {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: [DONE]`)}
			}
		}
		reporter.EnsurePublished(ctx)
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: httpResp.Header.Clone(),
		Chunks:  out,
	}, nil
}

// Refresh validates the Command Code credential.
func (e *CommandCodeExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("missing auth")
	}
	return auth, nil
}

// CountTokens returns the token count for the given request.
func (e *CommandCodeExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("commandcode: count tokens not supported")
}

// buildHeaders constructs upstream request headers.
func (e *CommandCodeExecutor) buildHeaders(auth *cliproxyauth.Auth, apiKey string) http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Authorization", "Bearer "+apiKey)
	headers.Set("User-Agent", "cli")
	headers.Set(commandcode.CLIEnvHeader, commandcode.CLIEnvProd)
	headers.Set(commandcode.CLIVersionHeader, commandcode.GetCLIVersion())
	headers.Set(commandcode.ProjectSlugHeader, commandcode.DefaultProjectSlug)
	headers.Set(commandcode.TasteLearningHeader, "false")
	headers.Set(commandcode.CoFlagHeader, "false")
	headers.Set(commandcode.SessionIDHeader, uuid.New().String())
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	tmpReq := &http.Request{Header: headers}
	util.ApplyCustomHeadersFromAttrs(tmpReq, attrs)
	return tmpReq.Header
}

// commandCodeAPIKey extracts the API key from auth metadata or attributes.
func commandCodeAPIKey(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		for _, key := range []string{"commandcodeApiKey", "api_key", "apiKey"} {
			if v, ok := auth.Metadata[key].(string); ok && v != "" {
				return v
			}
		}
	}
	if auth.Attributes != nil {
		for _, key := range []string{"commandcodeApiKey", "api_key"} {
			if v := auth.Attributes[key]; v != "" {
				return v
			}
		}
	}
	if auth.Storage != nil {
		type keyHolder interface{ GetAPIKey() string }
		if kh, ok := auth.Storage.(keyHolder); ok {
			return kh.GetAPIKey()
		}
	}
	return ""
}

// applySystemPrompt moves the source system prompt into the wire envelope's
// top-level system field. The wire protocol expects the system text on the
// params.system field rather than as a message entry.
func applySystemPrompt(body []byte, fromFormat string) []byte {
	if gjson.GetBytes(body, "params.system").String() != "" {
		return body
	}
	messages := gjson.GetBytes(body, "params.messages")
	if !messages.Exists() {
		return body
	}
	var sb strings.Builder
	var kept []string
	messages.ForEach(func(index, msg gjson.Result) bool {
		if msg.Get("role").String() != "system" {
			kept = append(kept, msg.Raw)
			return true
		}
		msg.Get("content").ForEach(func(_, part gjson.Result) bool {
			if s := part.Get("text").String(); s != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n\n")
				}
				sb.WriteString(s)
			}
			return true
		})
		return true
	})
	if sb.Len() == 0 {
		return body
	}
	body, _ = sjson.SetBytes(body, "params.system", sb.String())
	if len(kept) != len(messages.Array()) {
		var rebuilt []byte
		rebuilt = append(rebuilt, '[')
		for i, raw := range kept {
			if i > 0 {
				rebuilt = append(rebuilt, ',')
			}
			rebuilt = append(rebuilt, raw...)
		}
		rebuilt = append(rebuilt, ']')
		body, _ = sjson.SetRawBytes(body, "params.messages", rebuilt)
	}
	return body
}

// forceStreamFlag ensures params.stream matches the requested mode.
func forceStreamFlag(body []byte, stream bool) []byte {
	out, _ := sjson.SetBytes(body, "params.stream", stream)
	return out
}

// parseWireUsage converts a finish event into usage details.
func parseWireUsage(body []byte) usage.Detail {
	finish := gjson.GetBytes(body, "totalUsage")
	return usage.Detail{
		InputTokens:         finish.Get("inputTokens").Int(),
		OutputTokens:        finish.Get("outputTokens").Int(),
		CacheReadTokens:     finish.Get("inputTokenDetails.cacheReadTokens").Int(),
		CacheCreationTokens: finish.Get("inputTokenDetails.cacheWriteTokens").Int(),
	}
}

// commandCodeBodyLogLimit caps how many raw upstream body bytes are kept for
// the no-payload diagnostic log.
const commandCodeBodyLogLimit = 8 * 1024

// commandCodeStreamErrorMessage extracts a human-readable message from a wire
// error/abort event line. The exact event schema is not documented, so several
// common field layouts are probed.
func commandCodeStreamErrorMessage(line []byte) string {
	for _, path := range []string{"error.message", "message", "error", "reason"} {
		if v := strings.TrimSpace(gjson.GetBytes(line, path).String()); v != "" {
			return v
		}
	}
	return "unknown upstream error"
}

// FetchCommandCodeModels fetches models dynamically from the Command Code API.
// Falls back to the static registry list on failure.
func FetchCommandCodeModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	apiKey := commandCodeAPIKey(auth)
	if apiKey == "" {
		log.Info("commandcode: no API key found, skipping dynamic model fetch")
		return registry.GetCommandCodeModels()
	}

	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+apiKey)
	headers.Set("User-Agent", "cli")
	headers.Set(commandcode.CLIEnvHeader, commandcode.CLIEnvProd)
	headers.Set(commandcode.CLIVersionHeader, commandcode.GetCLIVersion())

	httpClient := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 30*time.Second)
	_, body, _, errDo := helps.DoJSON(ctx, cfg, helps.UpstreamRequest{
		Provider: "commandcode",
		Auth:     auth,
		Method:   http.MethodGet,
		URL:      commandcode.BaseURL + "/provider/v1/models",
		Headers:  headers,
		Client:   httpClient,
	})
	if errDo != nil {
		log.Warnf("commandcode: failed to fetch models: %v", errDo)
		return registry.GetCommandCodeModels()
	}

	result := gjson.GetBytes(body, "data")
	if !result.Exists() {
		result = gjson.ParseBytes(body)
		if !result.IsArray() {
			log.Warn("commandcode: invalid models response format")
			return registry.GetCommandCodeModels()
		}
	}

	now := time.Now().Unix()
	dynamicModels := make([]*registry.ModelInfo, 0, result.Get("#").Int())
	result.ForEach(func(_, value gjson.Result) bool {
		id := value.Get("id").String()
		if id == "" {
			return true
		}
		contextLength := value.Get("context_length").Int()
		if contextLength <= 0 {
			contextLength = value.Get("context_window").Int()
		}
		displayName := value.Get("name").String()
		if displayName == "" {
			displayName = id
		}
		dynamicModels = append(dynamicModels, &registry.ModelInfo{
			ID:            id,
			Name:          id,
			DisplayName:   displayName,
			ContextLength: int(contextLength),
			OwnedBy:       "commandcode",
			Type:          "commandcode",
			Object:        "model",
			Created:       now,
			Thinking:      registry.CommandCodeThinkingSupport(),
		})
		return true
	})

	if len(dynamicModels) == 0 {
		log.Warn("commandcode: API returned no models, using static fallback")
		return registry.GetCommandCodeModels()
	}

	log.Infof("commandcode: fetched %d models from API", len(dynamicModels))
	return dynamicModels
}
