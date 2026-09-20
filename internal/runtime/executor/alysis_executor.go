package executor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/alysis"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
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
)

// AlysisExecutor forwards OpenAI-compatible requests to the Alysis Code Pro
// hosted gateway, which meters subscription credits server-side.
type AlysisExecutor struct {
	cfg *config.Config
}

// NewAlysisExecutor creates a new Alysis executor instance.
func NewAlysisExecutor(cfg *config.Config) *AlysisExecutor {
	return &AlysisExecutor{cfg: cfg}
}

func (e *AlysisExecutor) Identifier() string {
	return "alysis"
}

// alysisGatewayBase is overridable in tests.
var alysisGatewayBase = alysis.SupabaseURL + alysis.GatewayPathPrefix

func (e *AlysisExecutor) chatCompletionsURL() string {
	return alysisGatewayBase + "/chat/completions"
}

// PrepareRequest prepares the HTTP request before execution.
func (e *AlysisExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	if key := alysisCredentials(auth); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	} else {
		req.Header.Del("Authorization")
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest executes a raw HTTP request.
func (e *AlysisExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("alysis executor: request is nil")
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
func (e *AlysisExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayloadSource, opts.Stream)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, opts.Stream)
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	translated = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", translated, originalTranslated, requestedModel, "")

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	// The Alysis gateway rejects requests whose max_tokens exceeds the model's
	// context window (and refuses with a 400 that the conductor may retry
	// against its Retry-After header). Clamp to the registered per-model cap.
	translated = clampAlysisMaxTokens(translated, baseModel)

	url := e.chatCompletionsURL()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return resp, err
	}
	applyAlysisHeaders(httpReq, alysisCredentials(auth), false)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)

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
		Body:      translated,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("alysis executor: close response body: %v", errClose)
		}
	}()

	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	reporter.EnsurePublished(ctx)

	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, body, &param)
	resp = cliproxyexecutor.Response{Payload: []byte(out)}
	return resp, nil
}

// ExecuteStream performs a streaming request.
func (e *AlysisExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")

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

	// Clamp max_tokens to the registered per-model cap; the gateway rejects
	// oversized output limits with a 400 that may be retried against a
	// Retry-After header, wasting quota.
	translated = clampAlysisMaxTokens(translated, baseModel)

	url := e.chatCompletionsURL()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return nil, err
	}
	applyAlysisHeaders(httpReq, alysisCredentials(auth), true)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)

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
		Body:      translated,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}

	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		_ = httpResp.Body.Close()
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("alysis executor: close response body: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := helps.ParseOpenAIStreamUsage(line); ok {
				reporter.Publish(ctx, detail)
			}
			if len(line) == 0 {
				continue
			}
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, bytes.Clone(line), &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}:
				case <-ctx.Done():
					return
				}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
				return
			}
		}
		reporter.EnsurePublished(ctx)
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: httpResp.Header.Clone(),
		Chunks:  out,
	}, nil
}

// Refresh validates the stored gateway key. The key is long-lived and has no
// server-side refresh endpoint, so the auth is returned unchanged.
func (e *AlysisExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("missing auth")
	}
	return auth, nil
}

// CountTokens returns the token count for the given request.
func (e *AlysisExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("alysis: count tokens not supported")
}

// alysisCredentials extracts the gateway key from the auth record.
func alysisCredentials(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if key, ok := auth.Metadata["gatewayKey"].(string); ok && key != "" {
			return key
		}
	}
	if auth.Attributes != nil {
		if key := auth.Attributes["gatewayKey"]; key != "" {
			return key
		}
	}
	return ""
}

// FetchAlysisModels fetches the live gateway model catalog (OpenAI-shaped
// {"data":[{"id":...}]}). Failures fall back to the static catalog.
func FetchAlysisModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	httpClient := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, alysisGatewayBase+"/models", nil)
	if err != nil {
		log.Warnf("alysis: failed to create model fetch request: %v", err)
		return registry.GetAlysisModels()
	}
	if key := alysisCredentials(auth); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Warnf("alysis: fetch models canceled: %v", err)
		} else {
			log.Warnf("alysis: using static models (API fetch failed: %v)", err)
		}
		return registry.GetAlysisModels()
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("alysis: close models response body: %v", errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Warnf("alysis: failed to read models response: %v", err)
		return registry.GetAlysisModels()
	}
	if resp.StatusCode != http.StatusOK {
		log.Warnf("alysis: fetch models failed: status %d, body: %s", resp.StatusCode, string(body))
		return registry.GetAlysisModels()
	}

	result := gjson.GetBytes(body, "data")
	if !result.Exists() {
		log.Warnf("alysis: invalid models response format (expected data field)")
		return registry.GetAlysisModels()
	}

	now := time.Now().Unix()
	seen := make(map[string]struct{})
	merged := make([]*registry.ModelInfo, 0, 8)
	for _, model := range registry.GetAlysisModels() {
		if model == nil || model.ID == "" {
			continue
		}
		seen[model.ID] = struct{}{}
		merged = append(merged, model)
	}
	result.ForEach(func(_, value gjson.Result) bool {
		id := strings.TrimSpace(value.Get("id").String())
		if id == "" {
			return true
		}
		if _, exists := seen[id]; exists {
			return true
		}
		seen[id] = struct{}{}
		displayName := strings.TrimSpace(value.Get("name").String())
		if displayName == "" {
			displayName = id
		}
		merged = append(merged, &registry.ModelInfo{
			ID:          id,
			DisplayName: displayName,
			OwnedBy:     "alysis",
			Type:        "alysis",
			Object:      "model",
			Created:     now,
		})
		return true
	})

	if len(merged) == 0 {
		return registry.GetAlysisModels()
	}
	return merged
}

func applyAlysisHeaders(r *http.Request, key string, stream bool) {
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	if stream {
		r.Header.Set("Accept", "text/event-stream")
		r.Header.Set("Cache-Control", "no-cache")
	} else {
		r.Header.Set("Accept", "application/json")
	}
}

// clampAlysisMaxTokens rewrites max_tokens in the outbound payload so that
// (input_tokens + max_tokens) stays within the model's context window. The
// Alysis gateway rejects requests whose total budget exceeds the context
// window with a 400 ("Invalid or oversized hosted request ... output limits
// (8 MiB maximum)") that the conductor may otherwise retry against a
// Retry-After header, wasting quota. When the model is unknown or has no
// context length registered, the payload is returned unchanged.
func clampAlysisMaxTokens(payload []byte, model string) []byte {
	contextLen := alysisModelContextLength(model)
	if contextLen <= 0 {
		return payload
	}
	// Estimate input tokens from the translated payload. If tokenization
	// fails, fall back to a conservative byte-based estimate.
	inputTokens := estimateInputTokens(payload, model)
	// Reserve a 5% safety margin on the context window so we don't brush
	// against the exact boundary (tokenizer approximations can undercount).
	budget := int(float64(contextLen)*0.95) - inputTokens
	if budget <= 0 {
		// Input alone already exceeds the budget; clamp to a minimal
		// output allowance so the request at least reaches the gateway
		// and the user sees a meaningful error instead of a 400.
		budget = 1024
	}
	maxTokens := gjson.GetBytes(payload, "max_tokens")
	if !maxTokens.Exists() || maxTokens.Int() <= int64(budget) {
		return payload
	}
	clamped, err := sjson.SetBytes(payload, "max_tokens", budget)
	if err != nil {
		return payload
	}
	log.Debugf("alysis: clamped max_tokens %d -> %d for model %q (input ~%d tokens, context %d)", maxTokens.Int(), budget, model, inputTokens, contextLen)
	return clamped
}

// estimateInputTokens returns an approximate token count for the outbound
// payload. Falls back to a byte-based estimate (~4 chars per token) when
// tokenization fails.
func estimateInputTokens(payload []byte, model string) int {
	enc, err := helps.TokenizerForModel(model)
	if err != nil {
		return len(payload) / 4
	}
	count, err := helps.CountOpenAIChatTokens(enc, payload)
	if err != nil {
		return len(payload) / 4
	}
	return int(count)
}

// alysisModelContextLength returns the per-model context window from the
// alysis static catalog. The global registry may carry entries for the same
// model id with a different context length from another provider, so we look
// up the alysis catalog directly.
func alysisModelContextLength(model string) int {
	for _, m := range registry.GetAlysisModels() {
		if m != nil && m.ID == model {
			return m.ContextLength
		}
	}
	return 0
}
