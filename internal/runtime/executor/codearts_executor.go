package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codearts"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	codeartsChatURL   = "https://snap-access.cn-north-4.myhuaweicloud.com/v1/chat/chat"
	codeArtsUserAgent = "DevKit-VSCode:huaweicloud.codearts-snap|CodeArts Agent:D1"
)

// CodeArtsExecutor executes chat completions against the HuaweiCloud CodeArts API.
type CodeArtsExecutor struct {
	cfg *config.Config
}

// NewCodeArtsExecutor constructs a new executor instance.
func NewCodeArtsExecutor(cfg *config.Config) *CodeArtsExecutor {
	return &CodeArtsExecutor{cfg: cfg}
}

// Identifier returns the executor's provider key.
func (e *CodeArtsExecutor) Identifier() string { return "codearts" }

// PrepareRequest sets CodeArts-specific headers and signs the request.
func (e *CodeArtsExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if auth == nil || auth.Metadata == nil {
		return fmt.Errorf("codearts: missing auth metadata")
	}

	ak, _ := auth.Metadata["ak"].(string)
	sk, _ := auth.Metadata["sk"].(string)
	securityToken, _ := auth.Metadata["security_token"].(string)

	if ak == "" || sk == "" {
		return fmt.Errorf("codearts: missing AK/SK credentials")
	}

	var bodyBytes []byte
	if req.Body != nil {
		bodyBytes, _ = io.ReadAll(req.Body)
		req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		req.ContentLength = int64(len(bodyBytes))
	}

	traceID := generateTraceID()

	req.Header.Set("User-Agent", codeArtsUserAgent)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Agent-Type", "ChatAgent")
	req.Header.Set("Client-Version", "Vscode_26.3.5")
	req.Header.Set("Heartbeat-Enable", "true")
	req.Header.Set("Ide-Name", "CodeArts Agent")
	req.Header.Set("Ide-Version", "1.96.4")
	req.Header.Set("Is-Confidential", "false")
	req.Header.Set("Plugin-Name", "snap_vscode")
	req.Header.Set("Plugin-Version", "26.3.5")
	req.Header.Set("X-Language", "zh-cn")
	req.Header.Set("X-Snap-Traceid", traceID)

	codearts.SignRequest(req, bodyBytes, ak, sk, securityToken)
	return nil
}

// HttpRequest executes a signed HTTP request to CodeArts.
func (e *CodeArtsExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	client := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 5*time.Minute)

	if err := e.PrepareRequest(req, auth); err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codearts: request failed: %w", err)
	}
	return resp, nil
}

// Execute handles non-streaming chat completions.
func (e *CodeArtsExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	parsed := thinking.ParseSuffix(req.Model)
	baseModel := parsed.ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	agentID := codearts.DefaultAgentID
	if auth.Attributes != nil {
		if aid := strings.TrimSpace(auth.Attributes["agent_id"]); aid != "" {
			agentID = aid
		}
	}

	userID := extractUserID(auth)

	payload := buildCodeArtsPayload(req.Payload, baseModel, agentID, userID, opts)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", codeartsChatURL, bytes.NewReader(payload))
	if err != nil {
		return resp, err
	}

	httpResp, err := e.HttpRequest(ctx, auth, httpReq)
	if err != nil {
		return resp, err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != 200 {
		body, _ := io.ReadAll(httpResp.Body)
		return resp, statusErr{
			code: httpResp.StatusCode,
			msg:  fmt.Sprintf("codearts: API returned %d: %s", httpResp.StatusCode, string(body)),
		}
	}

	var contentBuilder strings.Builder
	var reasoningBuilder strings.Builder
	var promptTokens, completionTokens int64

	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, ":heartbeat") || line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		delta := gjson.Get(data, "delta")
		if delta.Exists() {
			if c := delta.Get("content").String(); c != "" {
				contentBuilder.WriteString(c)
			}
			if r := delta.Get("reasoning_content").String(); r != "" {
				reasoningBuilder.WriteString(r)
			}
		}
		if pt := gjson.Get(data, "prompt_tokens").Int(); pt > 0 {
			promptTokens = pt
		}
		if ct := gjson.Get(data, "completion_tokens").Int(); ct > 0 {
			completionTokens = ct
		}
	}

	from := sdktranslator.FromString("openai")
	to := sdktranslator.FromString("codearts")

	openAIResp := buildOpenAINonStreamResponse(contentBuilder.String(), reasoningBuilder.String(), req.Model, promptTokens, completionTokens)
	var param any
	translated := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, req.Payload, openAIResp, &param)

	reporter.Publish(ctx, usage.Detail{
		InputTokens:  promptTokens,
		OutputTokens: completionTokens,
	})

	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:      codeartsChatURL,
		Method:   "POST",
		Provider: "codearts",
		AuthID:   auth.ID,
	})

	return cliproxyexecutor.Response{Payload: translated}, nil
}

// ExecuteStream handles streaming chat completions.
func (e *CodeArtsExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	parsed := thinking.ParseSuffix(req.Model)
	baseModel := parsed.ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	agentID := codearts.DefaultAgentID
	if auth.Attributes != nil {
		if aid := strings.TrimSpace(auth.Attributes["agent_id"]); aid != "" {
			agentID = aid
		}
	}

	userID := extractUserID(auth)

	payload := buildCodeArtsPayload(req.Payload, baseModel, agentID, userID, opts)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", codeartsChatURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}

	httpResp, err := e.HttpRequest(ctx, auth, httpReq)
	if err != nil {
		return nil, err
	}

	if httpResp.StatusCode != 200 {
		body, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		return nil, statusErr{
			code: httpResp.StatusCode,
			msg:  fmt.Sprintf("codearts: API returned %d: %s", httpResp.StatusCode, string(body)),
		}
	}

	chunks := make(chan cliproxyexecutor.StreamChunk, 64)

	go func() {
		defer close(chunks)
		defer httpResp.Body.Close()

		from := sdktranslator.FromString("openai")
		to := sdktranslator.FromString("codearts")
		var streamParam any
		var totalPromptTokens, totalCompletionTokens int64

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, ":heartbeat") || line == "" {
				continue
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}

			openAIChunk := convertCodeArtsSSEToOpenAI(data, req.Model)
			if openAIChunk == nil {
				continue
			}

			if pt := gjson.Get(data, "prompt_tokens").Int(); pt > 0 {
				totalPromptTokens = pt
			}
			if ct := gjson.Get(data, "completion_tokens").Int(); ct > 0 {
				totalCompletionTokens = ct
			}

			translatedChunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, req.Payload, openAIChunk, &streamParam)
			for _, tc := range translatedChunks {
				if len(tc) > 0 {
					chunks <- cliproxyexecutor.StreamChunk{Payload: tc}
				}
			}
		}

		if err := scanner.Err(); err != nil {
			log.Warnf("codearts: stream scanner error: %v", err)
			chunks <- cliproxyexecutor.StreamChunk{Err: err}
		}

		reporter.Publish(ctx, usage.Detail{
			InputTokens:  totalPromptTokens,
			OutputTokens: totalCompletionTokens,
		})

		helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
			URL:      codeartsChatURL,
			Method:   "POST",
			Provider: "codearts",
			AuthID:   auth.ID,
		})
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: httpResp.Header,
		Chunks:  chunks,
	}, nil
}

// CountTokens is not supported by CodeArts.
func (e *CodeArtsExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("codearts: token counting not supported")
}

// Refresh refreshes the CodeArts security token.
func (e *CodeArtsExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil || auth.Metadata == nil {
		return nil, fmt.Errorf("codearts: no metadata to refresh")
	}

	currentToken := extractCodeArtsToken(auth)
	if currentToken == nil {
		return nil, fmt.Errorf("codearts: no valid token data found for refresh")
	}

	if !codearts.NeedsRefresh(currentToken) {
		return auth, nil
	}

	caAuth := codearts.NewCodeArtsAuth(nil)
	newToken, err := caAuth.RefreshToken(ctx, currentToken)
	if err != nil {
		return nil, fmt.Errorf("codearts: refresh failed: %w", err)
	}

	updated := auth.Clone()
	updated.Metadata["ak"] = newToken.AK
	updated.Metadata["sk"] = newToken.SK
	updated.Metadata["security_token"] = newToken.SecurityToken
	updated.Metadata["expires_at"] = newToken.ExpiresAt.Format(time.RFC3339)
	if newToken.XAuthToken != "" {
		updated.Metadata["x_auth_token"] = newToken.XAuthToken
	}

	log.Infof("codearts: successfully refreshed token, expires at %s", newToken.ExpiresAt.Format(time.RFC3339))
	return updated, nil
}

// extractCodeArtsToken extracts token data from auth metadata.
func extractCodeArtsToken(auth *cliproxyauth.Auth) *codearts.CodeArtsTokenData {
	if auth == nil || auth.Metadata == nil {
		return nil
	}

	ak, _ := auth.Metadata["ak"].(string)
	sk, _ := auth.Metadata["sk"].(string)
	if ak == "" || sk == "" {
		return nil
	}

	token := &codearts.CodeArtsTokenData{
		AK:            ak,
		SK:            sk,
		SecurityToken: metadataStr(auth.Metadata, "security_token"),
		XAuthToken:    metadataStr(auth.Metadata, "x_auth_token"),
		Email:         metadataStr(auth.Metadata, "email"),
	}

	if expiresStr := metadataStr(auth.Metadata, "expires_at"); expiresStr != "" {
		if t, err := time.Parse(time.RFC3339, expiresStr); err == nil {
			token.ExpiresAt = t
		}
	}

	return token
}

func metadataStr(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func extractUserID(auth *cliproxyauth.Auth) string {
	if auth.Metadata != nil {
		if uid, ok := auth.Metadata["user_id"].(string); ok {
			return uid
		}
		if did, ok := auth.Metadata["domain_id"].(string); ok {
			return did
		}
	}
	return ""
}

func generateTraceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%032d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func generateChatID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%032d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// buildCodeArtsPayload converts the OpenAI-format payload to CodeArts format.
func buildCodeArtsPayload(openaiPayload []byte, modelName, agentID, userID string, opts cliproxyexecutor.Options) []byte {
	messages := gjson.GetBytes(openaiPayload, "messages")
	if !messages.Exists() {
		log.Warn("codearts: no messages found in payload")
		return openaiPayload
	}

	var codeArtsMessages []map[string]string
	for _, msg := range messages.Array() {
		role := msg.Get("role").String()
		content := msg.Get("content").String()

		var formattedContent string
		switch role {
		case "system":
			formattedContent = "[System]\n" + content
		case "assistant":
			toolCalls := msg.Get("tool_calls")
			if toolCalls.Exists() && len(toolCalls.Array()) > 0 {
				var parts []string
				parts = append(parts, "[Assistant]\n"+content)
				for _, tc := range toolCalls.Array() {
					name := tc.Get("function.name").String()
					id := tc.Get("id").String()
					args := tc.Get("function.arguments").String()
					parts = append(parts, fmt.Sprintf("[Tool Call: %s] (id: %s)\n%s", name, id, args))
				}
				formattedContent = strings.Join(parts, "\n")
			} else {
				formattedContent = "[Assistant]\n" + content
			}
		case "tool":
			toolName := msg.Get("name").String()
			toolID := msg.Get("tool_call_id").String()
			if toolName == "" {
				toolName = "unknown"
			}
			formattedContent = fmt.Sprintf("[Tool Result: %s] (id: %s)\n%s", toolName, toolID, content)
		case "user":
			formattedContent = content
		default:
			formattedContent = content
		}

		codeArtsMessages = append(codeArtsMessages, map[string]string{
			"type":    "text",
			"content": formattedContent,
		})
	}

	taskParameters := map[string]interface{}{
		"is_intent_recognition":   false,
		"W3_Search":               false,
		"codebase_search":         false,
		"related_question":        true,
		"preferred_language":      "zh-cn",
		"enable_code_interpreter": false,
		"projectLevelPrompt":      "",
		"contexts":                []interface{}{},
		"expert_rules":            []interface{}{},
		"ide":                     "CodeArts Agent",
		"routerVersion":           "v2",
		"isNewClient":             true,
		"features":                map[string]interface{}{"support_end_tag": true},
	}

	if tools := gjson.GetBytes(openaiPayload, "tools"); tools.Exists() {
		taskParameters["tools"] = tools.Value()
	}
	if temp := gjson.GetBytes(openaiPayload, "temperature"); temp.Exists() {
		taskParameters["temperature"] = temp.Value()
	}

	chatID := generateChatID()

	request := map[string]interface{}{
		"chat_id":               chatID,
		"messages":              codeArtsMessages,
		"client":                "IDE",
		"task":                  "chat",
		"task_parameters":       taskParameters,
		"batch_task_parameters": []interface{}{},
		"attempt":               1,
		"user_id":               userID,
		"parent_message_id":     "",
		"is_delta_response":     true,
		"model_id":              modelName,
	}

	result, err := json.Marshal(request)
	if err != nil {
		log.Errorf("codearts: failed to marshal payload: %v", err)
		return openaiPayload
	}
	return result
}

// convertCodeArtsSSEToOpenAI converts a CodeArts SSE data line to OpenAI SSE format.
func convertCodeArtsSSEToOpenAI(data string, model string) []byte {
	delta := gjson.Get(data, "delta")
	if !delta.Exists() {
		return nil
	}

	content := delta.Get("content").String()
	reasoningContent := delta.Get("reasoning_content").String()

	if content == "" && reasoningContent == "" {
		return nil
	}

	deltaMap := make(map[string]interface{})
	if content != "" {
		deltaMap["content"] = content
	}
	if reasoningContent != "" {
		deltaMap["reasoning_content"] = reasoningContent
	}

	chunk := map[string]interface{}{
		"id":      "chatcmpl-codearts",
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"delta": deltaMap,
			},
		},
	}

	result, err := json.Marshal(chunk)
	if err != nil {
		return nil
	}

	return append([]byte("data: "), result...)
}

// buildOpenAINonStreamResponse builds a complete OpenAI non-stream response.
func buildOpenAINonStreamResponse(content, reasoning, model string, promptTokens, completionTokens int64) []byte {
	message := map[string]interface{}{
		"role":    "assistant",
		"content": content,
	}
	if reasoning != "" {
		message["reasoning_content"] = reasoning
	}

	resp := map[string]interface{}{
		"id":      "chatcmpl-codearts",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"finish_reason": "stop",
				"message":       message,
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
	}

	result, _ := json.Marshal(resp)
	return result
}
