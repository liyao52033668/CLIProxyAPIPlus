// Package executor provides runtime execution capabilities for various AI service providers.
// This file implements the Qoder executor that proxies requests to the Qoder upstream
// using COSY authentication and custom body encoding.
package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math"
	mrand "math/rand"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	qoderMaxRetries     = 3
	qoderBaseRetryDelay = 1 * time.Second
	qoderMaxRetryDelay  = 30 * time.Second
)

var qoderRetryableHTTPStatus = map[int]bool{
	http.StatusBadGateway:         true,
	http.StatusServiceUnavailable: true,
	http.StatusGatewayTimeout:     true,
}

type qoderRetryConfig struct {
	MaxRetries      int
	BaseDelay       time.Duration
	MaxDelay        time.Duration
	RetryableErrors []string
}

func qoderDefaultRetryConfig() qoderRetryConfig {
	return qoderRetryConfig{
		MaxRetries: qoderMaxRetries,
		BaseDelay:  qoderBaseRetryDelay,
		MaxDelay:   qoderMaxRetryDelay,
		RetryableErrors: []string{
			"connection reset",
			"broken pipe",
			"temporary failure",
			"no such host",
			"network is unreachable",
			"i/o timeout",
			"unexpected eof",
			"eof",
		},
	}
}

func qoderIsRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			log.Debugf("qoder executor: isRetryableError: network timeout detected")
			return true
		}
	}
	var syscallErr syscall.Errno
	if errors.As(err, &syscallErr) {
		switch syscallErr {
		case syscall.ECONNRESET, syscall.ECONNREFUSED, syscall.EPIPE, syscall.ETIMEDOUT, syscall.ENETUNREACH, syscall.EHOSTUNREACH:
			log.Debugf("qoder executor: isRetryableError: syscall error %v detected", syscallErr)
			return true
		}
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		log.Debugf("qoder executor: isRetryableError: net.OpError detected, op=%s", opErr.Op)
		if opErr.Err != nil {
			return qoderIsRetryableError(opErr.Err)
		}
		return true
	}
	errMsg := strings.ToLower(err.Error())
	cfg := qoderDefaultRetryConfig()
	for _, pattern := range cfg.RetryableErrors {
		if strings.Contains(errMsg, pattern) {
			log.Debugf("qoder executor: isRetryableError: pattern '%s' matched in error: %s", pattern, errMsg)
			return true
		}
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		log.Debugf("qoder executor: isRetryableError: EOF/UnexpectedEOF detected")
		return true
	}
	return false
}

func qoderIsRetryableHTTPStatus(statusCode int) bool {
	return qoderRetryableHTTPStatus[statusCode]
}

func qoderCalculateRetryDelay(attempt int, cfg qoderRetryConfig) time.Duration {
	delay := float64(cfg.BaseDelay) * math.Pow(2, float64(attempt))
	if delay > float64(cfg.MaxDelay) {
		delay = float64(cfg.MaxDelay)
	}
	jitter := delay * 0.3 * (2*mrand.Float64() - 1)
	delay += jitter
	if delay < 0 {
		delay = float64(cfg.BaseDelay)
	}
	return time.Duration(delay)
}

func qoderLogRetryAttempt(attempt, maxRetries int, reason string, delay time.Duration) {
	log.Debugf("qoder executor: retry attempt %d/%d, reason: %s, next retry in %v", attempt+1, maxRetries, reason, delay)
}

var stdToCustom [256]byte

func init() {
	for i := range stdToCustom {
		stdToCustom[i] = 0xFF
	}
	for i := 0; i < 64; i++ {
		stdToCustom[qoder.StdAlphabet[i]] = qoder.CustomAlphabet[i]
	}
	stdToCustom['='] = byte(qoder.CustomPad)
}

// customBase64Encode encodes data using Qoder's custom base64 scheme.
// Process: standard base64 encode → segment rearrangement (split at n/3) → alphabet substitution.
func customBase64Encode(data []byte) string {
	std := base64.StdEncoding.EncodeToString(data)
	n := len(std)
	a := n / 3
	// Rearrange: last_third + middle_third + first_third
	rearranged := std[n-a:] + std[a:n-a] + std[:a]
	result := make([]byte, n)
	for i, c := range []byte(rearranged) {
		mapped := stdToCustom[c]
		if mapped == 0xFF {
			log.Errorf("qoder executor: char out of standard base64 alphabet: %c", c)
			return ""
		}
		result[i] = mapped
	}
	return string(result)
}

// QoderExecutor handles request execution against the Qoder upstream API.
type QoderExecutor struct {
	cfg *config.Config
}

// NewQoderExecutor creates a new Qoder executor.
func NewQoderExecutor(cfg *config.Config) *QoderExecutor {
	return &QoderExecutor{cfg: cfg}
}

// Identifier returns the executor identifier.
func (e *QoderExecutor) Identifier() string { return "qoder" }

// PrepareRequest injects Qoder COSY credentials into the outgoing HTTP request.
func (e *QoderExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	// COSY auth is built per-request in Execute/ExecuteStream, so this is minimal.
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects Qoder credentials into the request and executes it.
func (e *QoderExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("qoder executor: request is nil")
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

// Execute performs a non-streaming chat completion request to Qoder.
func (e *QoderExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := bytes.Clone(originalPayloadSource)
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, false)
	body := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), false)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), "qoder", e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	body = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", body, originalTranslated, requestedModel)

	// Build the Qoder-specific request body wrapping the OpenAI messages
	qoderBody := e.buildQoderRequestBody(body, baseModel, false)

	url := qoder.ChatBase + qoder.ChatPath + "?" + qoder.ChatQueryExtra
	qoderBodyJSON, errMarshal := json.Marshal(qoderBody)
	if errMarshal != nil {
		return resp, fmt.Errorf("qoder executor: failed to marshal request body: %w", errMarshal)
	}

	// Build COSY authenticated request (plain JSON for non-stream)
	httpReq, errReq := e.buildCosyRequest(ctx, auth, url, qoderBodyJSON, false, baseModel)
	if errReq != nil {
		return resp, errReq
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, auth.Attributes)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      qoderBodyJSON,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	retryCfg := qoderDefaultRetryConfig()
	var httpResp *http.Response
	var lastErr error

	for attempt := 0; attempt < retryCfg.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := qoderCalculateRetryDelay(attempt-1, retryCfg)
			qoderLogRetryAttempt(attempt-1, retryCfg.MaxRetries, lastErr.Error(), delay)
			time.Sleep(delay)
			httpReq, errReq = e.buildCosyRequest(ctx, auth, url, qoderBodyJSON, false, baseModel)
			if errReq != nil {
				return resp, errReq
			}
			util.ApplyCustomHeadersFromAttrs(httpReq, auth.Attributes)
		}

		httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
		httpResp, lastErr = httpClient.Do(httpReq)
		if lastErr != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, lastErr)
			if qoderIsRetryableError(lastErr) {
				continue
			}
			return resp, lastErr
		}

		helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
		if httpResp.StatusCode >= 500 && qoderIsRetryableHTTPStatus(httpResp.StatusCode) {
			b, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			helps.AppendAPIResponseChunk(ctx, e.cfg, b)
			lastErr = statusErr{code: httpResp.StatusCode, msg: string(b)}
			continue
		}
		break
	}

	if lastErr != nil {
		return resp, lastErr
	}
	if httpResp == nil {
		return resp, fmt.Errorf("qoder executor: unexpected nil response after retries")
	}

	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("qoder executor: close response body error: %v", errClose)
		}
	}()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)

	// Parse SSE response to extract the final completion
	openAIResp := e.parseQoderSSEToCompletion(data, req.Model)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(openAIResp))

	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, body, openAIResp, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

// ExecuteStream performs a streaming chat completion request to Qoder.
func (e *QoderExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := bytes.Clone(originalPayloadSource)
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, true)
	body := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), true)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), "qoder", e.Identifier())
	if err != nil {
		return nil, err
	}

	body, err = sjson.SetBytes(body, "stream_options.include_usage", true)
	if err != nil {
		return nil, fmt.Errorf("qoder executor: failed to set stream_options in payload: %w", err)
	}
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	body = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", body, originalTranslated, requestedModel)

	// Build the Qoder-specific request body
	qoderBody := e.buildQoderRequestBody(body, baseModel, true)

	url := qoder.ChatBase + qoder.ChatPath + "?" + qoder.ChatQueryExtra
	qoderBodyJSON, errMarshal := json.Marshal(qoderBody)
	if errMarshal != nil {
		return nil, fmt.Errorf("qoder executor: failed to marshal request body: %w", errMarshal)
	}

	// Build COSY authenticated request (plain JSON for stream)
	httpReq, errReq := e.buildCosyRequest(ctx, auth, url, qoderBodyJSON, true, baseModel)
	if errReq != nil {
		return nil, errReq
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, auth.Attributes)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      qoderBodyJSON,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	retryCfg := qoderDefaultRetryConfig()
	var httpResp *http.Response
	var lastErr error

	for attempt := 0; attempt < retryCfg.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := qoderCalculateRetryDelay(attempt-1, retryCfg)
			qoderLogRetryAttempt(attempt-1, retryCfg.MaxRetries, lastErr.Error(), delay)
			time.Sleep(delay)
			httpReq, errReq = e.buildCosyRequest(ctx, auth, url, qoderBodyJSON, true, baseModel)
			if errReq != nil {
				return nil, errReq
			}
			util.ApplyCustomHeadersFromAttrs(httpReq, auth.Attributes)
		}

		httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
		httpResp, lastErr = httpClient.Do(httpReq)
		if lastErr != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, lastErr)
			if qoderIsRetryableError(lastErr) {
				continue
			}
			return nil, lastErr
		}

		helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
		if httpResp.StatusCode >= 500 && qoderIsRetryableHTTPStatus(httpResp.StatusCode) {
			b, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			helps.AppendAPIResponseChunk(ctx, e.cfg, b)
			lastErr = statusErr{code: httpResp.StatusCode, msg: string(b)}
			continue
		}
		break
	}

	if lastErr != nil {
		return nil, lastErr
	}
	if httpResp == nil {
		return nil, fmt.Errorf("qoder executor: unexpected nil response after retries")
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("qoder executor: close response body error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("qoder executor: close response body error: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 1_048_576) // 1MB
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)

			// Parse Qoder SSE format: data:{...} where body contains inner OpenAI chunk
			openAIChunk := e.extractOpenAIChunkFromSSE(line, req.Model)
			if openAIChunk == nil {
				continue
			}

			if detail, ok := helps.ParseOpenAIStreamUsage(openAIChunk); ok {
				reporter.Publish(ctx, detail)
			}

			// Wrap as SSE line for translator
			sseLine := append([]byte("data: "), openAIChunk...)
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, body, bytes.Clone(sseLine), &param)
			for i := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}
			}
		}
		doneChunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, body, []byte("[DONE]"), &param)
		for i := range doneChunks {
			out <- cliproxyexecutor.StreamChunk{Payload: doneChunks[i]}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

// Refresh is a no-op for Qoder since tokens don't expire in the standard OAuth sense.
func (e *QoderExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("qoder executor: refresh called")
	if auth == nil {
		return nil, fmt.Errorf("qoder executor: auth is nil")
	}
	// Qoder tokens (access_token from the PKCE login) are long-lived
	return auth, nil
}

// CountTokens returns an unsupported error since Qoder does not expose a token counting endpoint.
func (e *QoderExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, statusErr{code: http.StatusNotImplemented, msg: "qoder does not support token counting"}
}

// buildQoderRequestBody wraps OpenAI-format messages into the Qoder request envelope.
func (e *QoderExecutor) buildQoderRequestBody(openaiBody []byte, modelKey string, stream bool) map[string]any {
	var messages []any
	msgsRaw := gjson.GetBytes(openaiBody, "messages")
	if msgsRaw.Exists() && msgsRaw.IsArray() {
		_ = json.Unmarshal([]byte(msgsRaw.Raw), &messages)
	}

	// Extract last user message for originalContent and rebuild messages per Java's logic
	lastUser := ""
	var rebuiltMessages []any
	if messages != nil {
		for i := len(messages) - 1; i >= 0; i-- {
			if m, ok := messages[i].(map[string]any); ok {
				if role, _ := m["role"].(string); role == "user" {
					if content, ok := m["content"].(string); ok {
						lastUser = content
					}
					break
				}
			}
		}
		// Rebuild messages: filter out user messages, then append new user message with contents
		for _, msg := range messages {
			if m, ok := msg.(map[string]any); ok {
				if role, _ := m["role"].(string); role != "user" {
					rebuiltMessages = append(rebuiltMessages, m)
				}
			}
		}
		// Create new user message with contents array (matching Java's logic)
		newUserMsg := map[string]any{
			"role":    "user",
			"content": "",
			"contents": []map[string]any{{
				"type": "text",
				"text": lastUser,
			}},
			"response_meta": map[string]any{
				"id": "",
				"usage": map[string]any{
					"prompt_tokens":     0,
					"completion_tokens": 0,
					"total_tokens":      0,
					"completion_tokens_details": map[string]any{
						"reasoning_tokens": 0,
					},
					"prompt_tokens_details": map[string]any{
						"cached_tokens": 0,
					},
				},
			},
			"reasoning_content_signature": "",
		}
		rebuiltMessages = append(rebuiltMessages, newUserMsg)
	}

	body := map[string]any{
		"request_id":     uuid.NewString(),
		"request_set_id": uuid.NewString(),
		"chat_record_id": uuid.NewString(),
		"stream":         stream,
		"chat_task":      "FREE_INPUT",
		"chat_context": map[string]any{
			"chatPrompt": "",
			"extra": map[string]any{
				"context":         []any{},
				"modelConfig":     map[string]any{"key": modelKey, "is_reasoning": false},
				"originalContent": map[string]any{"type": "text", "text": lastUser},
			},
			"features":  []any{},
			"imageUrls": nil,
			"text":      map[string]any{"type": "text", "text": lastUser},
		},
		"image_urls":       nil,
		"is_reply":         true, // must be true to match Java
		"is_retry":         false,
		"session_id":       uuid.NewString(),
		"code_language":    "",
		"source":           1,
		"version":          "3",
		"chat_prompt":      "",
		"parameters":       map[string]any{"max_tokens": 32768},
		"aliyun_user_type": "personal_standard",
		"session_type":     "qodercli",
		"agent_id":         "agent_common",
		"task_id":          "common",
		"messages":         rebuiltMessages,
		"tools":            []any{},
		"model_config": map[string]any{
			"key":              modelKey,
			"display_name":     modelKey,
			"model":            "",
			"format":           "openai",
			"is_vl":            false,
			"is_reasoning":     false,
			"api_key":          "",
			"url":              "",
			"source":           "system",
			"max_input_tokens": 180000,
		},
		"business": map[string]any{
			"id":       uuid.NewString(),
			"type":     "agent_chat_generation",
			"name":     "",
			"begin_at": time.Now().UnixMilli(),
		},
	}
	return body
}

// buildCosyRequest creates an HTTP request with COSY authentication headers.
func (e *QoderExecutor) buildCosyRequest(ctx context.Context, auth *cliproxyauth.Auth, reqURL string, body []byte, stream bool, modelKey string) (*http.Request, error) {
	creds := qoderCreds(auth)
	if creds.accessToken == "" {
		return nil, fmt.Errorf("qoder executor: missing access token")
	}

	// Encode body using Qoder's custom base64 scheme (required by upstream API)
	// bodyJSON, _ := json.Marshal(body)
	// log.Debugf("qoder executor: request body JSON: %s", string(bodyJSON))
	encodedBody := customBase64Encode(body)
	if encodedBody == "" {
		return nil, fmt.Errorf("qoder executor: failed to encode body")
	}

	// Parse path for signature — match Java: pathSig = pathQuery.startsWith("/algo") ? pathQuery.substring("/algo".length()) : pathQuery
	sigPath := ""
	if _, after, ok := strings.Cut(reqURL, "://"); ok {
		afterScheme := after
		if slashIdx := strings.Index(afterScheme, "/"); slashIdx >= 0 {
			sigPath = afterScheme[slashIdx:]
		}
	}
	if idx := strings.Index(sigPath, "?"); idx >= 0 {
		sigPath = sigPath[:idx]
	}
	// Remove /algo prefix for signature calculation (matches Java implementation)
	if strings.HasPrefix(sigPath, "/algo") {
		sigPath = sigPath[len("/algo"):]
	}

	// Build COSY payload
	aesKey := uuid.NewString()[:16]
	identity, _ := json.Marshal(map[string]any{
		"uid":                  creds.uid,
		"security_oauth_token": creds.accessToken,
		"name":                 creds.name,
		"aid":                  "",
		"email":                creds.email,
	})
	info := aesEncryptB64(string(identity), aesKey)
	key := base64.StdEncoding.EncodeToString(rsaEncrypt([]byte(aesKey)))

	timestamp := fmt.Sprintf("%d", time.Now().Unix())

	// Build payload with sorted keys to match Java's TreeMap ordering
	// TreeMap sorts keys alphabetically: cosyVersion, ideVersion, info, requestId, version
	payloadJSON, _ := json.Marshal(map[string]any{
		"cosyVersion": qoder.IDEVersion,
		"ideVersion":  "",
		"info":        info,
		"requestId":   uuid.NewString(),
		"version":     "v1",
	})
	payloadB64 := base64.StdEncoding.EncodeToString(payloadJSON)

	sigInput := fmt.Sprintf("%s\n%s\n%s\n%s\n%s", payloadB64, key, timestamp, encodedBody, sigPath)
	sigMD5 := fmt.Sprintf("%x", md5.Sum([]byte(sigInput)))

	bodyHash := fmt.Sprintf("%x", md5.Sum([]byte(encodedBody)))

	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, strings.NewReader(encodedBody))
	if errReq != nil {
		return nil, fmt.Errorf("qoder executor: create request: %w", errReq)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept-Encoding", "identity")
	httpReq.Header.Set("Cosy-Version", qoder.IDEVersion) // "0.1.43" to match Java
	httpReq.Header.Set("Cosy-Machineid", creds.machineID)
	httpReq.Header.Set("Cosy-Machinetoken", creds.machineID)
	httpReq.Header.Set("Cosy-Machinetype", "d19de69691ac029caa")
	httpReq.Header.Set("Cosy-Machineos", "x86_64_windows")
	httpReq.Header.Set("Cosy-Clienttype", "5")
	httpReq.Header.Set("Cosy-Clientip", "127.0.0.1")
	httpReq.Header.Set("Login-Version", "v2")
	httpReq.Header.Set("Cosy-User", creds.uid)
	httpReq.Header.Set("Cosy-Key", key)
	httpReq.Header.Set("Cosy-Date", timestamp)
	httpReq.Header.Set("Cosy-Bodyhash", bodyHash)
	httpReq.Header.Set("Cosy-Bodylength", fmt.Sprintf("%d", len(encodedBody)))
	httpReq.Header.Set("Cosy-Sigpath", sigPath)
	httpReq.Header.Set("Cosy-Data-Policy", "AGREE")
	httpReq.Header.Set("Cosy-Organization-Id", "")
	httpReq.Header.Set("Cosy-Organization-Tags", "")
	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer COSY.%s.%s", payloadB64, sigMD5))
	httpReq.Header.Set("X-Request-Id", uuid.NewString())
	httpReq.Header.Set("x-model-key", modelKey)
	httpReq.Header.Set("x-model-source", "system")

	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("Cache-Control", "no-cache")
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}

	return httpReq, nil
}

// extractOpenAIChunkFromSSE parses a Qoder SSE line and extracts the inner OpenAI chunk.
func (e *QoderExecutor) extractOpenAIChunkFromSSE(line []byte, model string) []byte {
	s := string(line)
	if !strings.HasPrefix(s, "data:") {
		return nil
	}
	raw := strings.TrimSpace(s[5:])
	if raw == "" || raw == "[DONE]" {
		return nil
	}

	// Parse the outer SSE envelope
	outerBody := gjson.Get(raw, "body")
	if !outerBody.Exists() {
		return nil
	}
	innerRaw := outerBody.String()
	if innerRaw == "[DONE]" {
		return nil
	}

	// Parse inner OpenAI chunk
	if !gjson.Valid(innerRaw) {
		return nil
	}
	inner := gjson.Parse(innerRaw)
	if !inner.Get("choices").Exists() {
		return nil
	}

	// Override the model name
	result, err := sjson.Set(innerRaw, "model", model)
	if err != nil {
		return []byte(innerRaw)
	}
	return []byte(result)
}

// parseQoderSSEToCompletion parses the full SSE response and assembles a non-streaming completion.
func (e *QoderExecutor) parseQoderSSEToCompletion(data []byte, model string) []byte {
	var fullContent strings.Builder
	var finishReason string

	lines := strings.SplitSeq(string(data), "\n")
	for line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		raw := strings.TrimSpace(line[5:])
		if raw == "" || raw == "[DONE]" {
			continue
		}

		outerBody := gjson.Get(raw, "body")
		if !outerBody.Exists() {
			continue
		}
		innerRaw := outerBody.String()
		if innerRaw == "[DONE]" {
			continue
		}
		inner := gjson.Parse(innerRaw)
		if !inner.Get("choices").Exists() {
			continue
		}
		choices := inner.Get("choices").Array()
		if len(choices) == 0 {
			continue
		}
		choice := choices[0]
		delta := choice.Get("delta")
		if delta.Exists() {
			content := delta.Get("content").String()
			fullContent.WriteString(content)
		}
		if fr := choice.Get("finish_reason").String(); fr != "" && fr != "null" {
			finishReason = fr
		}
	}

	if finishReason == "" {
		finishReason = "stop"
	}

	result := map[string]any{
		"id":      "chatcmpl-" + uuid.NewString(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{
			map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": fullContent.String(),
				},
				"finish_reason": finishReason,
			},
		},
	}
	out, _ := json.Marshal(result)
	return out
}

// qoderCredentials holds the extracted credentials for Qoder auth.
type qoderCredentials struct {
	accessToken string
	uid         string
	name        string
	email       string
	machineID   string
}

// qoderCreds extracts credentials from the auth record.
func qoderCreds(a *cliproxyauth.Auth) qoderCredentials {
	var creds qoderCredentials
	if a == nil {
		return creds
	}
	if a.Metadata != nil {
		if v, ok := a.Metadata["access_token"].(string); ok {
			creds.accessToken = v
		}
		if v, ok := a.Metadata["uid"].(string); ok {
			creds.uid = v
		}
		if v, ok := a.Metadata["name"].(string); ok {
			creds.name = v
		}
		if v, ok := a.Metadata["email"].(string); ok {
			creds.email = v
		}
		if v, ok := a.Metadata["machine_id"].(string); ok {
			creds.machineID = v
		}
	}
	if a.Attributes != nil {
		if creds.accessToken == "" {
			if v := a.Attributes["access_token"]; v != "" {
				creds.accessToken = v
			}
		}
		if creds.uid == "" {
			if v := a.Attributes["uid"]; v != "" {
				creds.uid = v
			}
		}
	}
	return creds
}

// aesEncryptB64 encrypts plaintext with AES-CBC and returns base64-encoded ciphertext.
func aesEncryptB64(plaintext, keyStr string) string {
	block, err := aes.NewCipher([]byte(keyStr))
	if err != nil {
		log.Errorf("qoder executor: AES cipher creation failed: %v", err)
		return ""
	}
	data := pkcs7Pad([]byte(plaintext), block.BlockSize())
	iv := []byte(keyStr)[:16]
	encrypted := make([]byte, len(data))
	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(encrypted, data)
	return base64.StdEncoding.EncodeToString(encrypted)
}

// pkcs7Pad pads data to a multiple of blockSize using PKCS#7 padding.
func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	padtext := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(data, padtext...)
}

// rsaEncrypt encrypts data with the Qoder server public key.
func rsaEncrypt(data []byte) []byte {
	block, _ := pem.Decode([]byte(qoder.ServerPublicKeyPEM))
	if block == nil {
		log.Error("qoder executor: failed to parse PEM block")
		return nil
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		log.Errorf("qoder executor: failed to parse public key: %v", err)
		return nil
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		log.Error("qoder executor: public key is not RSA")
		return nil
	}
	encrypted, err := rsa.EncryptPKCS1v15(rand.Reader, rsaPub, data)
	if err != nil {
		log.Errorf("qoder executor: RSA encryption failed: %v", err)
		return nil
	}
	return encrypted
}
