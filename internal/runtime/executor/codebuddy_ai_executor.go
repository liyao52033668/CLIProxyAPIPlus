package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codebuddy_ai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	codeBuddyAIChatPath = "/v2/chat/completions"
	codeBuddyAIAuthType = "codebuddy-ai"
)

type CodeBuddyAIExecutor struct {
	cfg *config.Config
}

func NewCodeBuddyAIExecutor(cfg *config.Config) *CodeBuddyAIExecutor {
	return &CodeBuddyAIExecutor{cfg: cfg}
}

func (e *CodeBuddyAIExecutor) Identifier() string { return codeBuddyAIAuthType }

func codeBuddyAICredentials(auth *cliproxyauth.Auth) (accessToken, userID, domain string) {
	if auth == nil {
		return "", "", ""
	}
	accessToken = metaStringValue(auth.Metadata, "access_token")
	userID = metaStringValue(auth.Metadata, "user_id")
	domain = metaStringValue(auth.Metadata, "domain")
	if domain == "" {
		domain = codebuddy_ai.DefaultDomain
	}
	return
}

func (e *CodeBuddyAIExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	accessToken, userID, domain := codeBuddyAICredentials(auth)
	if accessToken == "" {
		return fmt.Errorf("codebuddy-ai: missing access token")
	}
	e.applyHeaders(req, accessToken, userID, domain)
	return nil
}

func (e *CodeBuddyAIExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("codebuddy-ai executor: request is nil")
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

func (e *CodeBuddyAIExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	accessToken, userID, domain := codeBuddyAICredentials(auth)
	if accessToken == "" {
		return resp, fmt.Errorf("codebuddy-ai: missing access token")
	}

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
	translated, _ = sjson.SetBytes(translated, "stream", true)
	translated, _ = sjson.SetBytes(translated, "stream_options.include_usage", true)

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	url := codebuddy_ai.BaseURL + codeBuddyAIChatPath
	headers := make(http.Header)
	tmpReq := &http.Request{Header: headers}
	e.applyHeaders(tmpReq, accessToken, userID, domain)
	tmpReq.Header.Set("Accept", "text/event-stream")
	tmpReq.Header.Set("Cache-Control", "no-cache")
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
	aggregatedBody, usageDetail, err := aggregateOpenAIChatCompletionStream(body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	reporter.Publish(ctx, usageDetail)
	reporter.EnsurePublished(ctx)

	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, aggregatedBody, &param)
	resp = cliproxyexecutor.Response{Payload: []byte(out), Headers: respHeaders}
	return resp, nil
}

func (e *CodeBuddyAIExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	accessToken, userID, domain := codeBuddyAICredentials(auth)
	if accessToken == "" {
		return nil, fmt.Errorf("codebuddy-ai: missing access token")
	}

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

	url := codebuddy_ai.BaseURL + codeBuddyAIChatPath
	headers := make(http.Header)
	tmpReq := &http.Request{Header: headers}
	e.applyHeaders(tmpReq, accessToken, userID, domain)
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
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("codebuddy-ai executor: close stream body error: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, maxScannerBufferSize)
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
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
		}
		reporter.EnsurePublished(ctx)
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: httpResp.Header.Clone(),
		Chunks:  out,
	}, nil
}

func (e *CodeBuddyAIExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("codebuddy-ai: missing auth")
	}

	refreshToken := metaStringValue(auth.Metadata, "refresh_token")
	if refreshToken == "" {
		log.Debugf("codebuddy-ai executor: no refresh token available, skipping refresh")
		return auth, nil
	}

	accessToken, userID, domain := codeBuddyAICredentials(auth)

	authSvc := codebuddy_ai.NewCodeBuddyAIAuth(e.cfg)
	storage, err := authSvc.RefreshToken(ctx, accessToken, refreshToken, userID, domain)
	if err != nil {
		return nil, fmt.Errorf("codebuddy-ai: token refresh failed: %w", err)
	}

	updated := auth.Clone()
	updated.Metadata["access_token"] = storage.AccessToken
	if storage.RefreshToken != "" {
		updated.Metadata["refresh_token"] = storage.RefreshToken
	}
	updated.Metadata["expires_in"] = storage.ExpiresIn
	updated.Metadata["domain"] = storage.Domain
	if storage.UserID != "" {
		updated.Metadata["user_id"] = storage.UserID
	}
	now := time.Now()
	updated.UpdatedAt = now
	updated.LastRefreshedAt = now

	return updated, nil
}

func (e *CodeBuddyAIExecutor) CountTokens(_ context.Context, _ *cliproxyauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("codebuddy-ai: count tokens not supported")
}

func (e *CodeBuddyAIExecutor) applyHeaders(req *http.Request, accessToken, userID, domain string) {
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", codebuddy_ai.UserAgent)
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("X-Domain", domain)
	req.Header.Set("X-IDE-Type", "IDE")
	req.Header.Set("X-IDE-Name", "CodeBuddy")
	req.Header.Set("X-IDE-Version", "1.100.0")
	req.Header.Set("X-Product", "cloud")
	req.Header.Set("X-Product-Version", "1.100.0")
}

var codeBuddyAIInternalModelPrefixes = []string{
	"completion-",
	"codewise-",
	"nes-",
	"chat-",
	"enhance-",
}

var codeBuddyAIAllowedInternalModels = map[string]bool{
	"o4-mini": true,
}

func isCodeBuddyAIInternalModel(id string) bool {
	for _, prefix := range codeBuddyAIInternalModelPrefixes {
		if strings.HasPrefix(id, prefix) {
			return !codeBuddyAIAllowedInternalModels[id]
		}
	}
	return false
}

func FetchCodeBuddyAIModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	accessToken, userID, domain := codeBuddyAICredentials(auth)
	if accessToken == "" {
		log.Infof("codebuddy-ai: no access token found, using static model list")
		return registry.GetCodeBuddyAIModels()
	}

	log.Debugf("codebuddy-ai: fetching dynamic models from config API")

	headers := make(http.Header)
	headers.Set("User-Agent", codebuddy_ai.UserAgent)
	headers.Set("Accept", "application/json, text/plain, */*")
	headers.Set("X-Requested-With", "XMLHttpRequest")
	headers.Set("Authorization", "Bearer "+accessToken)
	headers.Set("X-User-Id", userID)
	headers.Set("X-Domain", domain)
	headers.Set("X-IDE-Type", "CodeBuddyIDE")
	headers.Set("X-IDE-Name", "CodeBuddyIDE")
	headers.Set("X-IDE-Version", "4.9.5")
	headers.Set("X-Product-Version", "4.9.5")
	headers.Set("X-Env-ID", "production")
	headers.Set("X-Product", "SaaS")

	httpClient := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 15*time.Second)
	_, body, _, errDo := helps.DoJSON(ctx, cfg, helps.UpstreamRequest{
		Provider: "codebuddy-ai",
		Auth:     auth,
		Method:   http.MethodGet,
		URL:      codebuddy_ai.BaseURL + "/v3/config",
		Headers:  headers,
		Client:   httpClient,
	})
	if errDo != nil {
		log.Warnf("codebuddy-ai: using static models (config API fetch failed: %v)", errDo)
		return registry.GetCodeBuddyAIModels()
	}

	modelsResult := gjson.GetBytes(body, "data.models")
	if !modelsResult.Exists() || !modelsResult.IsArray() {
		log.Warn("codebuddy-ai: config API response missing data.models array")
		return registry.GetCodeBuddyAIModels()
	}

	var dynamicModels []*registry.ModelInfo
	now := time.Now().Unix()
	count := 0

	modelsResult.ForEach(func(key, value gjson.Result) bool {
		id := value.Get("id").String()
		if id == "" {
			return true
		}

		if isCodeBuddyAIInternalModel(id) {
			return true
		}

		name := value.Get("name").String()
		if name == "" {
			name = id
		}

		descEn := value.Get("descriptionEn").String()
		descZh := value.Get("descriptionZh").String()
		desc := descEn
		if desc == "" {
			desc = descZh
		}
		if desc == "" {
			desc = name + " via CodeBuddy AI"
		}

		maxInputTokens := int(value.Get("maxInputTokens").Int())
		maxOutputTokens := int(value.Get("maxOutputTokens").Int())
		maxAllowedSize := int(value.Get("maxAllowedSize").Int())

		contextLength := maxInputTokens
		if contextLength <= 0 && maxAllowedSize > 0 {
			contextLength = maxAllowedSize
		}
		if contextLength <= 0 {
			contextLength = 128000
		}
		if maxOutputTokens <= 0 {
			maxOutputTokens = 32768
		}

		supportsReasoning := value.Get("supportsReasoning").Bool()
		onlyReasoning := value.Get("onlyReasoning").Bool()

		var thinkingSupport *registry.ThinkingSupport
		if supportsReasoning || onlyReasoning {
			thinkingSupport = &registry.ThinkingSupport{ZeroAllowed: true}
			reasoningEffort := value.Get("reasoning.effort").String()
			if reasoningEffort == "medium" || reasoningEffort == "high" {
				thinkingSupport.DynamicAllowed = true
			}
		}

		dynamicModels = append(dynamicModels, &registry.ModelInfo{
			ID:                  id,
			Object:              "model",
			Created:             now,
			OwnedBy:             "codebuddy-ai",
			Type:                "codebuddy-ai",
			DisplayName:         name,
			Description:         desc,
			ContextLength:       contextLength,
			MaxCompletionTokens: maxOutputTokens,
			Thinking:            thinkingSupport,
			SupportedEndpoints:  []string{"/chat/completions"},
		})
		count++
		return true
	})

	log.Infof("codebuddy-ai: fetched %d models from config API", count)
	if count == 0 {
		log.Warn("codebuddy-ai: no models parsed from config API, using static fallback")
		return registry.GetCodeBuddyAIModels()
	}

	return dynamicModels
}
