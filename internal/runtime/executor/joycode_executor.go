package executor

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/joycode"
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
)

const (
	joycodeChatURL = "https://joycode-api.jd.com/api/saas/openai/v1/chat/completions"
)

type JoyCodeExecutor struct {
	cfg *config.Config
}

func NewJoyCodeExecutor(cfg *config.Config) *JoyCodeExecutor {
	return &JoyCodeExecutor{cfg: cfg}
}

func (e *JoyCodeExecutor) Identifier() string { return "joycode" }

func (e *JoyCodeExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if auth == nil || auth.Metadata == nil {
		return fmt.Errorf("joycode: missing auth metadata")
	}

	ptKey, _ := auth.Metadata["ptKey"].(string)
	if ptKey == "" {
		return fmt.Errorf("joycode: missing ptKey credential")
	}

	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("ptKey", ptKey)
	req.Header.Set("loginType", "")
	req.Header.Set("User-Agent", joycode.JoyCodeUA)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-ms-client-request-id", generateJoyCodeRequestID())

	return nil
}

func (e *JoyCodeExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	client := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 5*time.Minute)

	if err := e.PrepareRequest(req, auth); err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("joycode: request failed: %w", err)
	}
	return resp, nil
}

func (e *JoyCodeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	parsed := thinking.ParseSuffix(req.Model)
	baseModel := parsed.ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	// Translate the source format to OpenAI chat completions first (the JoyCode
	// upstream only understands OpenAI format). For non-OpenAI source formats
	// (e.g. claude from /v1/messages, openai-response from /v1/responses), we
	// need a two-hop translation: SourceFormat → openai → joycode.
	payload := req.Payload
	from := opts.SourceFormat
	if from != sdktranslator.FormatOpenAI {
		payload = sdktranslator.TranslateRequest(from, sdktranslator.FormatOpenAI, baseModel, req.Payload, opts.Stream)
		from = sdktranslator.FormatOpenAI
	}
	payload = buildJoyCodePayload(payload, baseModel, auth)

	headers := make(http.Header)
	tmpReq := &http.Request{Header: headers}
	if errPrep := e.PrepareRequest(tmpReq, auth); errPrep != nil {
		return resp, errPrep
	}
	headers = tmpReq.Header

	helps.RecordUpstreamRequest(ctx, e.cfg, auth, "joycode", http.MethodPost, joycodeChatURL, headers.Clone(), payload)
	_, body, _, errDo := helps.DoJSON(ctx, e.cfg, helps.UpstreamRequest{
		Provider:       e.Identifier(),
		Auth:           auth,
		Method:         http.MethodPost,
		URL:            joycodeChatURL,
		Headers:        headers,
		Body:           payload,
		Client:         helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 5*time.Minute),
		SkipRequestLog: true,
	})
	if errDo != nil {
		if ue, ok := errDo.(helps.UpstreamStatusError); ok {
			return resp, statusErr{code: ue.Code, msg: fmt.Sprintf("joycode: API returned %d: %s", ue.Code, ue.Msg)}
		}
		return resp, errDo
	}

	// The upstream body is an OpenAI chat completion, so translate back to the
	// client's own schema (from) with openai as the wire format (to). Forcing from
	// to openai here would look up the unregistered claude→joycode pair and hand
	// OpenAI JSON to Claude clients, which cannot parse it.
	from = opts.SourceFormat
	to := sdktranslator.FormatOpenAI

	var param any
	translated := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, req.Payload, body, &param)

	promptTokens := gjson.GetBytes(body, "usage.prompt_tokens").Int()
	completionTokens := gjson.GetBytes(body, "usage.completion_tokens").Int()

	reporter.Publish(ctx, usage.Detail{
		InputTokens:  promptTokens,
		OutputTokens: completionTokens,
	})
	reporter.EnsurePublished(ctx)

	return cliproxyexecutor.Response{Payload: translated}, nil
}

func (e *JoyCodeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	parsed := thinking.ParseSuffix(req.Model)
	baseModel := parsed.ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	// Translate the source format to OpenAI chat completions first (the JoyCode
	// upstream only understands OpenAI format). For non-OpenAI source formats
	// (e.g. claude from /v1/messages, openai-response from /v1/responses), we
	// need a two-hop translation: SourceFormat → openai → joycode.
	payload := req.Payload
	from := opts.SourceFormat
	if from != sdktranslator.FormatOpenAI {
		payload = sdktranslator.TranslateRequest(from, sdktranslator.FormatOpenAI, baseModel, req.Payload, opts.Stream)
		from = sdktranslator.FormatOpenAI
	}
	payload = buildJoyCodePayload(payload, baseModel, auth)

	headers := make(http.Header)
	tmpReq := &http.Request{Header: headers}
	if errPrep := e.PrepareRequest(tmpReq, auth); errPrep != nil {
		return nil, errPrep
	}
	headers = tmpReq.Header

	helps.RecordUpstreamRequest(ctx, e.cfg, auth, "joycode", http.MethodPost, joycodeChatURL, headers.Clone(), payload)
	httpResp, errDo := helps.DoStream(ctx, e.cfg, helps.UpstreamRequest{
		Provider:       e.Identifier(),
		Auth:           auth,
		Method:         http.MethodPost,
		URL:            joycodeChatURL,
		Headers:        headers,
		Body:           payload,
		Client:         helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 5*time.Minute),
		SkipRequestLog: true,
	})
	if errDo != nil {
		if ue, ok := errDo.(helps.UpstreamStatusError); ok {
			return nil, statusErr{code: ue.Code, msg: fmt.Sprintf("joycode: API returned %d: %s", ue.Code, ue.Msg)}
		}
		return nil, errDo
	}

	chunks := make(chan cliproxyexecutor.StreamChunk, 64)

	go func() {
		defer close(chunks)
		defer httpResp.Body.Close()

		// Chunks below are OpenAI chat completion chunks, so translate back to the
		// client's own schema (from) with openai as the wire format (to). Forcing
		// from to openai here would look up the unregistered claude→joycode pair
		// and hand OpenAI SSE to Claude clients, which cannot parse it.
		from := opts.SourceFormat
		to := sdktranslator.FormatOpenAI
		var streamParam any
		var totalPromptTokens, totalCompletionTokens int64
		var sawData bool
		var streamFailed bool

		// The registered response translators consume SSE data lines, so keep each
		// chunk framed as one. Claude's translator drops unframed payloads
		// outright, which would empty the whole stream.
		emitTranslated := func(payload string) {
			framed := []byte("data: " + payload)
			for _, tc := range sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, req.Payload, framed, &streamParam) {
				if len(tc) > 0 {
					chunks <- cliproxyexecutor.StreamChunk{Payload: tc}
				}
			}
		}

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}

			var data string
			if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			} else if strings.HasPrefix(line, "data:") {
				data = strings.TrimPrefix(line, "data:")
			} else {
				continue
			}

			if data == "[DONE]" {
				break
			}
			sawData = true

			if pt := gjson.Get(data, "usage.prompt_tokens").Int(); pt > 0 {
				totalPromptTokens = pt
			}
			if ct := gjson.Get(data, "usage.completion_tokens").Int(); ct > 0 {
				totalCompletionTokens = ct
			}

			emitTranslated(data)
		}

		if err := scanner.Err(); err != nil {
			log.Warnf("joycode: stream scanner error: %v", err)
			streamFailed = true
			chunks <- cliproxyexecutor.StreamChunk{Err: err}
		}

		// Feed the terminal marker to the translator so schemas that need explicit
		// closing events (Claude's message_stop) emit them. The handler layer owns
		// the wire terminator itself, so nothing is forwarded for openai clients.
		if !streamFailed && sawData {
			emitTranslated("[DONE]")
		}

		reporter.Publish(ctx, usage.Detail{
			InputTokens:  totalPromptTokens,
			OutputTokens: totalCompletionTokens,
		})
		reporter.EnsurePublished(ctx)

		helps.RecordUpstreamRequest(ctx, e.cfg, auth, "joycode", http.MethodPost, joycodeChatURL, nil, nil)
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: httpResp.Header,
		Chunks:  chunks,
	}, nil
}

func (e *JoyCodeExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("joycode: token counting not supported")
}

func (e *JoyCodeExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

func buildJoyCodePayload(openaiPayload []byte, modelName string, auth *cliproxyauth.Auth) []byte {
	var payload map[string]interface{}
	if err := json.Unmarshal(openaiPayload, &payload); err != nil {
		log.Warnf("joycode: failed to parse payload, passing through: %v", err)
		return openaiPayload
	}

	payload["model"] = modelName
	payload["stream_options"] = map[string]interface{}{"include_usage": true}

	if _, ok := payload["thinking"]; !ok {
		payload["thinking"] = map[string]interface{}{"type": "disabled"}
	}

	tenant := ""
	userId := ""
	if auth != nil && auth.Metadata != nil {
		if t, ok := auth.Metadata["tenant"].(string); ok {
			tenant = t
		}
		if u, ok := auth.Metadata["userId"].(string); ok {
			userId = u
		}
	}
	payload["tenant"] = tenant
	payload["userId"] = userId
	payload["client"] = "JoyCode"
	payload["clientVersion"] = "2.4.8"
	payload["language"] = "text"
	payload["scene"] = "chat"
	payload["source"] = "joyCoderFe"

	result, err := json.Marshal(payload)
	if err != nil {
		log.Errorf("joycode: failed to marshal payload: %v", err)
		return openaiPayload
	}
	result = util.CleanupOrphanedRequiredInTools(result)
	return result
}

func generateJoyCodeRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%032d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
