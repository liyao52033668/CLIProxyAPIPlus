package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/lobsterai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// lobsterAIChatPath is the OpenAI-compatible chat endpoint. The upstream
	// mirrors the official client's local proxy path (/v1/chat/completions
	// rewritten to /api/proxy/v1/chat/completions).
	lobsterAIChatPath = "/api/proxy/v1/chat/completions"
	// lobsterAIModelsPath lists the account's available models.
	lobsterAIModelsPath = "/api/models/available"
	// lobsterAIRefreshTimeout bounds credential renewal, which only runs before
	// an upstream connection is established.
	lobsterAIRefreshTimeout = 30 * time.Second
)

// LobsterAIExecutor handles LobsterAI (NetEase Youdao) chat completions.
//
// The upstream is SSE-only: a non-streaming request would return an error, so
// the executor always asks for a stream and aggregates it back into a single
// chat.completion response when the caller did not request streaming.
type LobsterAIExecutor struct {
	provider string
	cfg      *config.Config
}

// NewLobsterAIExecutor creates a new LobsterAI executor instance.
func NewLobsterAIExecutor(cfg *config.Config) *LobsterAIExecutor {
	return &LobsterAIExecutor{provider: lobsterai.ProviderID, cfg: cfg}
}

// Identifier returns the provider key handled by this executor.
func (e *LobsterAIExecutor) Identifier() string { return e.provider }

// PrepareRequest injects LobsterAI credentials and client identity headers.
func (e *LobsterAIExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	creds := lobsterai.CredentialsFromAuth(auth)
	if creds.AccessToken == "" {
		return fmt.Errorf("lobsterai: missing access token")
	}
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	for name, values := range lobsterai.ClientHeaders() {
		for _, value := range values {
			req.Header.Set(name, value)
		}
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects LobsterAI credentials into the request and executes it.
func (e *LobsterAIExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("lobsterai executor: request is nil")
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

// Execute performs a non-streaming request by aggregating the upstream SSE stream.
func (e *LobsterAIExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	translated, from, to, errPrepare := e.preparePayload(req, opts, true)
	if errPrepare != nil {
		return resp, errPrepare
	}

	httpResp, errDo := e.doChat(ctx, auth, translated, true)
	if errDo != nil {
		return resp, errDo
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("lobsterai: failed to close response body: %v", errClose)
		}
	}()

	aggregated, usageDetail, errAggregate := aggregateLobsterAIStream(httpResp.Body, req.Model)
	if errAggregate != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errAggregate)
		return resp, errAggregate
	}
	reporter.Publish(ctx, usageDetail)
	reporter.EnsurePublished(ctx)

	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, aggregated, &param)
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}

// ExecuteStream performs a streaming request and forwards upstream SSE frames.
func (e *LobsterAIExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	translated, from, to, errPrepare := e.preparePayload(req, opts, true)
	if errPrepare != nil {
		return nil, errPrepare
	}

	httpResp, errDo := e.doChat(ctx, auth, translated, false)
	if errDo != nil {
		return nil, errDo
	}

	out := make(chan cliproxyexecutor.StreamChunk, 16)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("lobsterai: failed to close response body: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), streamScannerBuffer)
		var param any
		emitted := false
		var streamErr error
		for scanner.Scan() {
			line := bytes.Clone(scanner.Bytes())
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) == 0 {
				continue
			}
			// LobsterAI emits prefixed SSE frames. Surface structured upstream
			// error frames instead of letting the stream end silently.
			if errFrame := lobsterAIStreamError(trimmed, req.Model); errFrame != nil {
				log.Warnf("lobsterai: upstream stream error: %v", errFrame)
				streamErr = errFrame
				break
			}
			if detail, ok := helps.ParseOpenAIStreamUsage(trimmed); ok {
				reporter.Publish(ctx, detail)
			}
			if !bytes.HasPrefix(trimmed, []byte("data:")) {
				continue
			}
			if payload := bytes.TrimSpace(trimmed[5:]); bytes.Equal(payload, []byte("[DONE]")) {
				continue
			}
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, trimmed, &param)
			for i := range chunks {
				payload := bytes.Clone(chunks[i])
				if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
					// The handler layer owns the SSE terminator.
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
			return
		}
		if streamErr != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
			reporter.PublishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: streamErr}
			return
		}
		if !emitted {
			log.Warnf("lobsterai: upstream stream closed without payload (status=%d)", httpResp.StatusCode)
		}
		reporter.EnsurePublished(ctx)
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: httpResp.Header.Clone(),
		Chunks:  out,
	}, nil
}

// Refresh rotates the access token and re-reads the account quota signals.
func (e *LobsterAIExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, errors.New("lobsterai executor: auth is nil")
	}
	creds := lobsterai.CredentialsFromAuth(auth)
	if creds.AccessToken == "" && creds.RefreshToken == "" {
		return auth, nil
	}

	refreshCtx := ctx
	if refreshCtx == nil {
		refreshCtx = context.Background()
	}
	refreshCtx, cancel := context.WithTimeout(refreshCtx, lobsterAIRefreshTimeout)
	defer cancel()

	service, errService := e.newService(creds.BaseURL)
	if errService != nil {
		return auth, errService
	}

	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}

	accessToken := creds.AccessToken
	if creds.RefreshToken != "" {
		payload, errRefresh := service.RefreshAccessToken(refreshCtx, creds.RefreshToken, creds.InstallationUUID, creds.UserID)
		if errRefresh != nil {
			log.Warnf("lobsterai: token refresh failed for %s: %v", auth.ID, errRefresh)
			return auth, errRefresh
		}
		accessToken = payload.AccessToken
		updated.Metadata["access_token"] = payload.AccessToken
		if payload.RefreshToken != "" {
			updated.Metadata["refresh_token"] = payload.RefreshToken
		}
		if expiresAt := lobsterai.FormatExpiresAt(payload.ExpiresAt); expiresAt != "" {
			updated.Metadata["expires_at"] = expiresAt
		}
		if payload.Nickname != "" {
			updated.Metadata["nickname"] = payload.Nickname
			if updated.Attributes == nil {
				updated.Attributes = make(map[string]string)
			}
			updated.Attributes["nickname"] = payload.Nickname
		}
	}

	if usage := lobsterai.FetchUsage(refreshCtx, service, accessToken); usage != nil {
		updated.Quota = cliproxyauth.QuotaState{
			ObservedAt: time.Now(),
			Signals:    lobsterai.Signals(usage),
		}
	}
	return updated, nil
}

// CountTokens is not offered by the LobsterAI API.
func (e *LobsterAIExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("lobsterai: count tokens not supported")
}

// newService builds the LobsterAI auth service bound to the configured proxy.
// An operator-supplied base_url is validated before use so an auth file cannot
// redirect credential traffic at a loopback or private address.
func (e *LobsterAIExecutor) newService(baseURL string) (*lobsterai.Service, error) {
	service := lobsterai.NewService(util.SetProxy(&e.cfg.SDKConfig, &http.Client{Timeout: lobsterAIRefreshTimeout}))
	if strings.TrimSpace(baseURL) == "" {
		return service, nil
	}
	validated, errValidate := lobsterAIValidateBaseURL(baseURL)
	if errValidate != nil {
		return nil, errValidate
	}
	service.SetValidatedServerBaseURL(validated)
	return service, nil
}

// lobsterAIValidateBaseURL guards the credential's upstream override against
// SSRF (http/https only; no loopback, private, or reserved hosts). It is a
// package-level seam so tests can target a loopback httptest server.
var lobsterAIValidateBaseURL = lobsterai.ValidateServerBaseURL

// resolveBaseURL validates the credential's base_url override.
func resolveBaseURL(baseURL string) (string, error) {
	if strings.TrimSpace(baseURL) == "" {
		return lobsterai.DefaultServerBaseURL, nil
	}
	return lobsterAIValidateBaseURL(baseURL)
}

// preparePayload translates the inbound request into the LobsterAI chat format
// and applies reasoning configuration.
func (e *LobsterAIExecutor) preparePayload(req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) ([]byte, sdktranslator.Format, sdktranslator.Format, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayloadSource, stream)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, stream)
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	translated = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", translated, originalTranslated, requestedModel, "")

	// The payload is OpenAI-shaped, but reasoning is applied under the
	// lobsterai key so the level set and the "disable means omit" rule match
	// what the upstream accepts (it has no "none" level).
	translated, errThinking := thinking.ApplyThinking(translated, req.Model, from.String(), lobsterai.ProviderID, e.Identifier())
	if errThinking != nil {
		return nil, from, to, errThinking
	}
	translated = applyLobsterAIUpstreamModel(translated, baseModel)
	translated = forceLobsterAIStream(translated)
	return translated, from, to, nil
}

// applyLobsterAIUpstreamModel strips a provider namespace and any thinking
// suffix from the wire model id.
func applyLobsterAIUpstreamModel(payload []byte, model string) []byte {
	baseModel := thinking.ParseSuffix(model).ModelName
	baseModel = strings.TrimPrefix(baseModel, "lobsterai/")
	if strings.TrimSpace(baseModel) == "" {
		return payload
	}
	updated, errSet := sjson.SetBytes(payload, "model", baseModel)
	if errSet != nil {
		return payload
	}
	return updated
}

// forceLobsterAIStream always requests a stream: the upstream rejects
// non-streaming chat requests.
func forceLobsterAIStream(payload []byte) []byte {
	updated, errSet := sjson.SetBytes(payload, "stream", true)
	if errSet != nil {
		return payload
	}
	return updated
}

// doChat issues the chat request. Non-2xx responses are classified so the auth
// conductor can react to expired sessions.
//
// Header construction goes through PrepareRequest so the bearer token and client
// identity headers have a single source of truth. helps.DoStream copies the
// supplied headers verbatim and never injects credentials itself, so building
// them here without the Authorization header would send an anonymous request
// that the upstream rejects with its "session expired" error.
func (e *LobsterAIExecutor) doChat(ctx context.Context, auth *cliproxyauth.Auth, payload []byte, expectAggregate bool) (*http.Response, error) {
	creds := lobsterai.CredentialsFromAuth(auth)
	baseURL, errBase := resolveBaseURL(creds.BaseURL)
	if errBase != nil {
		return nil, errBase
	}

	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if expectAggregate {
		headers.Set("Accept", "text/event-stream, application/json")
	} else {
		headers.Set("Accept", "text/event-stream")
	}
	headerRequest := &http.Request{Header: headers}
	if errPrepare := e.PrepareRequest(headerRequest, auth); errPrepare != nil {
		return nil, errPrepare
	}
	headers = headerRequest.Header

	reqCtx := ctx
	if reqCtx == nil {
		reqCtx = context.Background()
	}

	httpResp, errDo := helps.DoStream(reqCtx, e.cfg, helps.UpstreamRequest{
		Provider: e.Identifier(),
		Auth:     auth,
		Method:   http.MethodPost,
		URL:      baseURL + lobsterAIChatPath,
		Headers:  headers,
		Body:     payload,
	})
	if errDo != nil {
		return nil, toStatusErr(errDo)
	}
	return httpResp, nil
}

// lobsterAIStreamError reports a structured upstream error frame.
// lobsterAIStreamError reports a structured upstream error frame. Content frames
// (those carrying choices) are never treated as errors.
func lobsterAIStreamError(line []byte, model string) error {
	payload := line
	if bytes.HasPrefix(line, []byte("data:")) {
		payload = bytes.TrimSpace(line[5:])
	}
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || !json.Valid(payload) {
		return nil
	}
	if gjson.GetBytes(payload, "choices").Exists() {
		return nil
	}
	code := gjson.GetBytes(payload, "code")
	message := strings.TrimSpace(gjson.GetBytes(payload, "message").String())
	if message == "" {
		message = strings.TrimSpace(gjson.GetBytes(payload, "error.message").String())
	}
	if !code.Exists() || code.Int() == 0 {
		if message == "" || !gjson.GetBytes(payload, "error").Exists() {
			return nil
		}
	}
	if message == "" {
		message = strings.TrimSpace(string(payload))
	}
	return &lobsterAIStreamErrorValue{status: httpStatusForCode(code), message: message, model: model}
}

type lobsterAIStreamErrorValue struct {
	status  int
	message string
	model   string
}

func (e *lobsterAIStreamErrorValue) Error() string {
	return fmt.Sprintf("lobsterai: upstream stream error (status %d): %s", e.status, e.message)
}

// StatusCode implements the status error contract so the conductor can cool the
// credential down when the upstream session expired mid-stream.
func (e *lobsterAIStreamErrorValue) StatusCode() int { return e.status }

// httpStatusForCode maps LobsterAI business codes onto HTTP statuses.
func httpStatusForCode(code gjson.Result) int {
	if !code.Exists() {
		return http.StatusBadGateway
	}
	switch code.Int() {
	case 40100, 40101:
		return http.StatusUnauthorized
	case -1:
		// The user endpoints answer -1 ("未登录") when the credential is not
		// accepted, which the conductor treats as a session failure.
		return http.StatusUnauthorized
	case 40200, 40201, 40202, 41606, 41607, 41608:
		return http.StatusPaymentRequired
	default:
		return http.StatusBadGateway
	}
}

// aggregateLobsterAIStream folds the upstream SSE stream into one chat.completion.
func aggregateLobsterAIStream(reader interface{ Read([]byte) (int, error) }, model string) ([]byte, usage.Detail, error) {
	var (
		responseID   string
		streamModel  string
		created      int64
		finishReason string
		role         = "assistant"
		content      strings.Builder
		reasoning    strings.Builder
		toolOrder    []int
		toolCalls    = map[int]*lobsterAIToolCall{}
		usageDetail  usage.Detail
		sawFrame     bool
	)

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), streamScannerBuffer)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			// A JSON error envelope can arrive with HTTP 200. Surface it instead
			// of reporting a generic empty response.
			if errEnvelope := lobsterAIStreamError(line, model); errEnvelope != nil {
				return nil, usageDetail, errEnvelope
			}
			continue
		}
		payload := bytes.TrimSpace(line[5:])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || !json.Valid(payload) {
			continue
		}
		sawFrame = true
		root := gjson.ParseBytes(payload)
		if responseID == "" {
			responseID = root.Get("id").String()
		}
		if streamModel == "" {
			streamModel = root.Get("model").String()
		}
		if created == 0 {
			created = root.Get("created").Int()
		}
		if detail, ok := helps.ParseOpenAIStreamUsage(line); ok {
			if detail.InputTokens > 0 || detail.OutputTokens > 0 || detail.TotalTokens > 0 {
				usageDetail = detail
			}
		}
		for _, choice := range root.Get("choices").Array() {
			if reason := choice.Get("finish_reason").String(); reason != "" {
				finishReason = reason
			}
			delta := choice.Get("delta")
			if roleValue := delta.Get("role").String(); roleValue != "" {
				role = roleValue
			}
			content.WriteString(delta.Get("content").String())
			reasoning.WriteString(delta.Get("reasoning_content").String())
			for _, call := range delta.Get("tool_calls").Array() {
				index := int(call.Get("index").Int())
				merged, seen := toolCalls[index]
				if !seen {
					merged = &lobsterAIToolCall{}
					toolCalls[index] = merged
					toolOrder = append(toolOrder, index)
				}
				merged.merge(call)
			}
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		return nil, usageDetail, errScan
	}
	if !sawFrame {
		return nil, usageDetail, errors.New("lobsterai: empty upstream response")
	}
	if responseID == "" {
		responseID = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	if created == 0 {
		created = time.Now().Unix()
	}
	if streamModel == "" {
		streamModel = strings.TrimPrefix(thinking.ParseSuffix(model).ModelName, "lobsterai/")
	}
	if finishReason == "" && len(toolOrder) > 0 {
		finishReason = "tool_calls"
	}

	message := map[string]any{"role": role, "content": content.String()}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	if len(toolOrder) > 0 {
		sortIntsAscending(toolOrder)
		calls := make([]map[string]any, 0, len(toolOrder))
		for _, index := range toolOrder {
			calls = append(calls, toolCalls[index].toMap())
		}
		message["tool_calls"] = calls
	}
	if finishReason == "" {
		finishReason = "stop"
	}

	response := map[string]any{
		"id":      responseID,
		"object":  "chat.completion",
		"created": created,
		"model":   streamModel,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
	}
	if usageDetail.InputTokens > 0 || usageDetail.OutputTokens > 0 || usageDetail.TotalTokens > 0 {
		response["usage"] = map[string]any{
			"prompt_tokens":     usageDetail.InputTokens,
			"completion_tokens": usageDetail.OutputTokens,
			"total_tokens":      usageDetail.TotalTokens,
		}
	}
	encoded, errMarshal := json.Marshal(response)
	if errMarshal != nil {
		return nil, usageDetail, fmt.Errorf("lobsterai: encode aggregated response: %w", errMarshal)
	}
	return encoded, usageDetail, nil
}

// lobsterAIToolCall accumulates streamed tool-call fragments by index.
type lobsterAIToolCall struct {
	id        string
	callType  string
	name      string
	arguments strings.Builder
}

func (c *lobsterAIToolCall) merge(delta gjson.Result) {
	if value := delta.Get("id").String(); value != "" {
		c.id = value
	}
	if value := delta.Get("type").String(); value != "" {
		c.callType = value
	}
	if value := delta.Get("function.name").String(); value != "" {
		c.name = value
	}
	c.arguments.WriteString(delta.Get("function.arguments").String())
}

func (c *lobsterAIToolCall) toMap() map[string]any {
	callType := c.callType
	if callType == "" {
		callType = "function"
	}
	return map[string]any{
		"id":   c.id,
		"type": callType,
		"function": map[string]any{
			"name":      c.name,
			"arguments": c.arguments.String(),
		},
	}
}

// sortIntsAscending sorts tool-call indexes so the aggregated call order is stable.
func sortIntsAscending(values []int) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
