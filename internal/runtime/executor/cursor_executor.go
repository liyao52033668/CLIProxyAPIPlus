package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	cursorauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor"
	cursorproto "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/proto"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"golang.org/x/net/http2"
)

const (
	cursorAPIURL            = "https://api2.cursor.sh"
	cursorRunPath           = "/agent.v1.AgentService/Run"
	cursorModelsPath        = "/agent.v1.AgentService/GetUsableModels"
	cursorAuthType          = "cursor"
	cursorHeartbeatInterval = 5 * time.Second
	cursorSessionTTL        = 5 * time.Minute
	cursorCheckpointTTL     = 30 * time.Minute
)

// CursorExecutor handles requests to the Cursor API via Connect+Protobuf protocol.
type CursorExecutor struct {
	cfg         *config.Config
	mu          sync.Mutex
	sessions    map[string]*cursorSession
	checkpoints map[string]*savedCheckpoint // keyed by conversationId
}

// savedCheckpoint stores the server's conversation_checkpoint_update for reuse.
type savedCheckpoint struct {
	data      []byte            // raw ConversationStateStructure protobuf bytes
	blobStore map[string][]byte // blobs referenced by the checkpoint
	authID    string            // auth that produced this checkpoint (checkpoint is auth-specific)
	updatedAt time.Time
}

type cursorSession struct {
	stream       *cursorproto.H2Stream
	blobStore    map[string][]byte
	mcpTools     []cursorproto.McpToolDef
	pending      []pendingMcpExec
	cancel       context.CancelFunc // cancels the session-scoped heartbeat (NOT tied to HTTP request)
	createdAt    time.Time
	authID       string                                     // auth file ID that created this session (for multi-account isolation)
	toolResultCh chan []toolResultInfo                      // receives tool results from the next HTTP request
	resumeOutCh  chan cliproxyexecutor.StreamChunk          // output channel for resumed response
	parked       *atomic.Bool                               // set when the current HTTP response ended via an MCP tool-call park
	switchOutput func(ch chan cliproxyexecutor.StreamChunk) // callback to switch output channel
	tokenUsage   *cursorTokenUsage                          // shared with the stream goroutine; updated on each resume round
}

type pendingMcpExec struct {
	ExecMsgId  uint32
	ExecId     string
	ToolCallId string
	ToolName   string
	Args       string // JSON-encoded args
}

// NewCursorExecutor constructs a new executor instance.
func NewCursorExecutor(cfg *config.Config) *CursorExecutor {
	e := &CursorExecutor{
		cfg:         cfg,
		sessions:    make(map[string]*cursorSession),
		checkpoints: make(map[string]*savedCheckpoint),
	}
	go e.cleanupLoop()
	go e.prewarmCursorH2Pool()
	return e
}

// Identifier implements ProviderExecutor.
func (e *CursorExecutor) Identifier() string { return cursorAuthType }

// CloseExecutionSession implements ExecutionSessionCloser.
func (e *CursorExecutor) CloseExecutionSession(sessionID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if sessionID == cliproxyauth.CloseAllExecutionSessionsID {
		for k, s := range e.sessions {
			s.cancel()
			delete(e.sessions, k)
		}
		return
	}
	if s, ok := e.sessions[sessionID]; ok {
		s.cancel()
		delete(e.sessions, sessionID)
	}
}

func (e *CursorExecutor) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		e.mu.Lock()
		for k, s := range e.sessions {
			if time.Since(s.createdAt) > cursorSessionTTL {
				s.cancel()
				delete(e.sessions, k)
			}
		}
		for k, cp := range e.checkpoints {
			if time.Since(cp.updatedAt) > cursorCheckpointTTL {
				delete(e.checkpoints, k)
			}
		}
		e.mu.Unlock()
	}
}

// findSessionByConversationLocked searches for a session matching the given
// conversationId regardless of authID. Used to find and clean up stale sessions
// from a previous auth after quota failover. Caller must hold e.mu.
func (e *CursorExecutor) findSessionByConversationLocked(convId string) string {
	suffix := ":" + convId
	for k := range e.sessions {
		if strings.HasSuffix(k, suffix) {
			return k
		}
	}
	return ""
}

// cursorStatusErr implements the StatusError and RetryAfter interfaces so the
// conductor can classify Cursor errors (e.g. 429 → quota cooldown).
type cursorStatusErr struct {
	code int
	msg  string
}

func (e cursorStatusErr) Error() string              { return e.msg }
func (e cursorStatusErr) StatusCode() int            { return e.code }
func (e cursorStatusErr) RetryAfter() *time.Duration { return nil } // no retry-after info from Cursor; conductor uses exponential backoff

// classifyCursorError maps Cursor Connect/H2 errors to HTTP status codes.
// Layer 1: precise match on ConnectError.Code (gRPC standard codes).
// Layer 2: fuzzy string match for H2 frame errors and unknown formats.
// Unclassified errors pass through unchanged.
func classifyCursorError(err error) error {
	if err == nil {
		return nil
	}

	// Layer 1: structured ConnectError from ParseConnectEndStream
	var ce *cursorproto.ConnectError
	if errors.As(err, &ce) {
		log.Infof("cursor: Connect error code=%q message=%q", ce.Code, ce.Message)
		switch ce.Code {
		case "resource_exhausted":
			return cursorStatusErr{code: 429, msg: err.Error()}
		case "unauthenticated":
			return cursorStatusErr{code: 401, msg: err.Error()}
		case "permission_denied":
			return cursorStatusErr{code: 403, msg: err.Error()}
		case "unavailable":
			return cursorStatusErr{code: 503, msg: err.Error()}
		case "internal":
			return cursorStatusErr{code: 500, msg: err.Error()}
		default:
			// Unknown Connect code — log for observation, treat as 502
			return cursorStatusErr{code: 502, msg: err.Error()}
		}
	}

	// Layer 2: fuzzy match for H2 errors and unstructured messages
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "rate limit") || strings.Contains(msg, "quota") ||
		strings.Contains(msg, "too many"):
		return cursorStatusErr{code: 429, msg: err.Error()}
	case strings.Contains(msg, "rst_stream") || strings.Contains(msg, "goaway"):
		return cursorStatusErr{code: 502, msg: err.Error()}
	}

	return err
}

// PrepareRequest implements ProviderExecutor (for HttpRequest support).
func (e *CursorExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	token := cursorAccessToken(auth)
	if token == "" {
		return fmt.Errorf("cursor: access token not found")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

// HttpRequest injects credentials and executes the request.
func (e *CursorExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("cursor: request is nil")
	}
	if err := e.PrepareRequest(req, auth); err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}

// CountTokens estimates token count locally using tiktoken.
func (e *CursorExecutor) CountTokens(_ context.Context, _ *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	defer func() {
		if err != nil {
			log.Warnf("cursor CountTokens error: %v", err)
		} else {
			log.Debugf("cursor CountTokens: model=%s result=%s", req.Model, string(resp.Payload))
		}
	}()
	model := gjson.GetBytes(req.Payload, "model").String()
	if model == "" {
		model = req.Model
	}

	enc, err := helps.TokenizerForModel(model)
	if err != nil {
		// Fallback: return zero tokens rather than error (avoids 502)
		return cliproxyexecutor.Response{Payload: helps.BuildOpenAIUsageJSON(0)}, nil
	}

	// Detect format: Claude (/v1/messages) vs OpenAI (/v1/chat/completions)
	var count int64
	if gjson.GetBytes(req.Payload, "system").Exists() || opts.SourceFormat.String() == "claude" {
		count, _ = helps.CountClaudeChatTokens(enc, req.Payload)
	} else {
		count, _ = helps.CountOpenAIChatTokens(enc, req.Payload)
	}

	return cliproxyexecutor.Response{Payload: helps.BuildOpenAIUsageJSON(count)}, nil
}

// Refresh attempts to refresh the Cursor access token.
func (e *CursorExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	refreshToken := cursorRefreshToken(auth)
	if refreshToken == "" {
		return nil, fmt.Errorf("cursor: no refresh token available")
	}

	tokens, err := cursorauth.RefreshToken(ctx, refreshToken)
	if err != nil {
		return nil, err
	}

	expiresAt := cursorauth.GetTokenExpiry(tokens.AccessToken)

	newAuth := auth.Clone()
	newAuth.Metadata["access_token"] = tokens.AccessToken
	newAuth.Metadata["refresh_token"] = tokens.RefreshToken
	newAuth.Metadata["expires_at"] = expiresAt.Format(time.RFC3339)
	return newAuth, nil
}

// Execute handles non-streaming requests.
func (e *CursorExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	log.Debugf("cursor Execute: model=%s sourceFormat=%s payloadLen=%d", req.Model, opts.SourceFormat, len(req.Payload))
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("cursor Execute PANIC: %v", r)
			err = fmt.Errorf("cursor: internal panic: %v", r)
		}
		if err != nil {
			log.Warnf("cursor Execute error: %v", err)
		}
	}()
	accessToken := cursorAccessToken(auth)
	if accessToken == "" {
		return resp, fmt.Errorf("cursor: access token not found")
	}

	// Translate input to OpenAI format if needed (e.g. Claude /v1/messages format)
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	payload := req.Payload
	if from.String() != "" && from.String() != "openai" {
		payload = sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(payload), false)
	}

	// Apply the shared thinking pipeline: the cursor applier normalizes the
	// canonical config into reasoning_effort on this OpenAI-shaped payload;
	// buildRunRequestParams later maps it onto RequestedModel parameters
	// gated by the account's model catalog.
	if applied, errApply := thinking.ApplyThinking(payload, req.Model, "openai", "openai", cursorAuthType); errApply == nil {
		payload = applied
	} else {
		log.Warnf("cursor: apply thinking: %v (using unmodified payload)", errApply)
	}

	clientType, requestModel := splitCursorClientType(req.Model)
	// Variant-style ids ("...-thinking-max-fast") fold back into the base
	// model plus encoded parameters so the clean catalog stays authoritative;
	// explicit body parameters override the id-embedded ones.
	idParams := map[string]string{}
	if base, variantParams, ok := cursorDecomposeVariantId(requestModel); ok {
		requestModel = base
		idParams = variantParams
	}
	parsed := parseOpenAIRequest(payload)
	resolvedModel := helps.ResolveCursorModelID(requestModel, cursorModelCatalog())
	modelName := strings.TrimSpace(parsed.Model)
	if modelName == "" {
		modelName = req.Model
	}
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), modelName, auth)
	defer reporter.TrackFailure(ctx, &err)

	ccSessId := extractClaudeCodeSessionId(req.Payload)
	conversationId := deriveConversationId(helps.APIKeyFromContext(ctx), ccSessId, parsed.SystemPrompt)
	params := buildRunRequestParams(resolvedModel, parsed, conversationId, idParams)

	requestBytes, errEncode := cursorproto.EncodeRunRequest(params)
	if errEncode != nil {
		return resp, fmt.Errorf("cursor: encode run request: %w", errEncode)
	}
	framedRequest := cursorproto.FrameConnectMessage(requestBytes, 0)

	stream, err := openCursorH2Stream(e, auth, accessToken, clientType)
	if err != nil {
		return resp, err
	}
	defer stream.Close()

	// Send the request frame
	if err := stream.Write(framedRequest); err != nil {
		return resp, fmt.Errorf("cursor: failed to send request: %w", err)
	}

	// Start heartbeat
	sessionCtx, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel()
	go cursorH2Heartbeat(sessionCtx, stream)

	// Collect full text from streaming response
	var fullText strings.Builder
	tokenUsage := &cursorTokenUsage{}
	tokenUsage.setInputEstimate(len(payload))
	if streamErr := processH2SessionFrames(sessionCtx, stream, params.BlobStore, nil,
		func(text string, isThinking bool) {
			fullText.WriteString(text)
		},
		nil,
		nil,
		tokenUsage,
		nil, // onCheckpoint - non-streaming doesn't persist
	); streamErr != nil && fullText.Len() == 0 {
		return resp, classifyCursorError(fmt.Errorf("cursor: stream error: %w", streamErr))
	}

	inputTok, outputTok := tokenUsage.get()
	reporter.Publish(ctx, tokenUsage.detail())
	reporter.EnsurePublished(ctx)

	id := "chatcmpl-" + uuid.New().String()[:28]
	created := time.Now().Unix()
	openaiResp := fmt.Sprintf(`{"id":"%s","object":"chat.completion","created":%d,"model":%s,"choices":[{"index":0,"message":{"role":"assistant","content":%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}}`,
		id, created, jsonString(parsed.Model), jsonString(fullText.String()), inputTok, outputTok, inputTok+outputTok)

	// Translate response back to source format if needed
	result := []byte(openaiResp)
	if from.String() != "" && from.String() != "openai" {
		var param any
		result = sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), payload, result, &param)
	}
	resp.Payload = result
	return resp, nil
}

// ExecuteStream handles streaming requests.
// It supports MCP tool call sessions: when Cursor returns an MCP tool call,
// the H2 stream is kept alive. When Claude Code returns the tool result in
// the next request, the result is sent back on the same stream (session resume).
// This mirrors the activeSessions/resumeWithToolResults pattern in cursor-fetch.ts.
func (e *CursorExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	log.Debugf("cursor ExecuteStream: model=%s sourceFormat=%s payloadLen=%d", req.Model, opts.SourceFormat, len(req.Payload))
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("cursor ExecuteStream PANIC: %v", r)
			err = fmt.Errorf("cursor: internal panic: %v", r)
		}
		if err != nil {
			log.Warnf("cursor ExecuteStream error: %v", err)
		}
	}()
	accessToken := cursorAccessToken(auth)
	if accessToken == "" {
		return nil, fmt.Errorf("cursor: access token not found")
	}

	// Extract session_id from metadata BEFORE translation (translation strips metadata)
	ccSessionId := extractClaudeCodeSessionId(req.Payload)
	if ccSessionId == "" && len(opts.OriginalRequest) > 0 {
		ccSessionId = extractClaudeCodeSessionId(opts.OriginalRequest)
	}

	// Translate input to OpenAI format if needed
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	payload := req.Payload
	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}
	if from.String() != "" && from.String() != "openai" {
		log.Debugf("cursor: translating request from %s to openai", from)
		payload = sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(payload), true)
		log.Debugf("cursor: translated payload len=%d", len(payload))
	}

	// Apply the shared thinking pipeline (see Execute): the cursor applier
	// normalizes the canonical config into reasoning_effort on this
	// OpenAI-shaped payload.
	if applied, errApply := thinking.ApplyThinking(payload, req.Model, "openai", "openai", cursorAuthType); errApply == nil {
		payload = applied
	} else {
		log.Warnf("cursor: apply thinking: %v (using unmodified payload)", errApply)
	}

	// A model-name prefix (sand/, bot/, grokbot/, cli/) selects the upstream
	// client identity — i.e. the usage pool — before model resolution.
	clientType, requestModel := splitCursorClientType(req.Model)
	// Variant-style ids ("...-thinking-max-fast") fold back into the base
	// model plus encoded parameters so the clean catalog stays authoritative;
	// explicit body parameters override the id-embedded ones.
	idParams := map[string]string{}
	if base, variantParams, ok := cursorDecomposeVariantId(requestModel); ok {
		requestModel = base
		idParams = variantParams
	}
	parsed := parseOpenAIRequest(payload)
	log.Debugf("cursor: parsed request: model=%s userText=%d chars, turns=%d, tools=%d, toolResults=%d",
		parsed.Model, len(parsed.UserText), len(parsed.Turns), len(parsed.Tools), len(parsed.ToolResults))
	resolvedModel := helps.ResolveCursorModelID(requestModel, cursorModelCatalog())

	conversationId := deriveConversationId(helps.APIKeyFromContext(ctx), ccSessionId, parsed.SystemPrompt)
	authID := auth.ID // e.g. "cursor.json" or "cursor-account2.json"
	log.Debugf("cursor: conversationId=%s authID=%s clientType=%s", conversationId, authID, clientType)

	// Session key includes authID (H2 stream is auth-specific, not
	// transferable) and clientType (a parked stream keeps the usage pool it
	// was opened against, so it may only be resumed with the same identity).
	sessionKey := authID + ":" + clientType + ":" + conversationId
	// Checkpoint key: conversation + identity — a checkpoint captured under
	// one client identity is not reused under another.
	checkpointKey := conversationId + ":" + clientType
	needsTranslate := from.String() != "" && from.String() != "openai"

	// Check if we can resume an existing session with tool results
	if len(parsed.ToolResults) > 0 {
		e.mu.Lock()
		session, hasSession := e.sessions[sessionKey]
		if hasSession {
			delete(e.sessions, sessionKey)
		}
		// If no session found for current auth, check for stale sessions from
		// a different auth on the same conversation (quota failover scenario).
		// Clean them up since the H2 stream belongs to the old account.
		if !hasSession {
			if oldKey := e.findSessionByConversationLocked(conversationId); oldKey != "" {
				oldSession := e.sessions[oldKey]
				log.Infof("cursor: cleaning up stale session from auth %s for conv=%s (auth migrated to %s)", oldSession.authID, conversationId, authID)
				oldSession.cancel()
				if oldSession.stream != nil {
					oldSession.stream.Close()
				}
				delete(e.sessions, oldKey)
			}
		}
		e.mu.Unlock()

		if hasSession && session.stream != nil && session.authID == authID {
			log.Debugf("cursor: resuming session %s with %d tool results", sessionKey, len(parsed.ToolResults))
			return e.resumeWithToolResults(ctx, session, parsed, from, to, req, originalPayload, payload, needsTranslate)
		}
		if hasSession && session.authID != authID {
			log.Warnf("cursor: session %s belongs to auth %s, but request is from %s — skipping resume", sessionKey, session.authID, authID)
		}
	}

	// Clean up any stale session for this key (or from a previous auth on same conversation)
	e.mu.Lock()
	if old, ok := e.sessions[sessionKey]; ok {
		old.cancel()
		delete(e.sessions, sessionKey)
	} else if oldKey := e.findSessionByConversationLocked(conversationId); oldKey != "" {
		old := e.sessions[oldKey]
		old.cancel()
		if old.stream != nil {
			old.stream.Close()
		}
		delete(e.sessions, oldKey)
	}
	e.mu.Unlock()

	// Look up saved checkpoint for this conversation (keyed by conversationId only).
	// Checkpoint is auth-specific: if auth changed (e.g. quota exhaustion failover),
	// the old checkpoint is useless on the new account — discard and flatten.
	e.mu.Lock()
	saved, hasCheckpoint := e.checkpoints[checkpointKey]
	e.mu.Unlock()

	params := buildRunRequestParams(resolvedModel, parsed, conversationId, idParams)

	if hasCheckpoint && saved.data != nil && saved.authID == authID {
		// Same auth — use checkpoint normally
		log.Debugf("cursor: using saved checkpoint (%d bytes) for conv=%s auth=%s", len(saved.data), checkpointKey, authID)
		params.RawCheckpoint = saved.data
		// Merge saved blobStore into params
		if params.BlobStore == nil {
			params.BlobStore = make(map[string][]byte)
		}
		for k, v := range saved.blobStore {
			if _, exists := params.BlobStore[k]; !exists {
				params.BlobStore[k] = v
			}
		}
	} else if hasCheckpoint && saved.data != nil && saved.authID != authID {
		// Auth changed (quota failover) — checkpoint is not portable across accounts.
		// Discard and flatten conversation history into userText.
		log.Infof("cursor: auth migrated (%s → %s) for conv=%s, discarding checkpoint and flattening context", saved.authID, authID, checkpointKey)
		e.mu.Lock()
		delete(e.checkpoints, checkpointKey)
		e.mu.Unlock()
		if len(parsed.ToolResults) > 0 || len(parsed.Turns) > 0 {
			flattenConversationIntoUserText(parsed)
			params = buildRunRequestParams(resolvedModel, parsed, conversationId, idParams)
		}
	} else if len(parsed.ToolResults) > 0 || len(parsed.Turns) > 0 {
		// Fallback: no checkpoint available (cold resume / proxy restart).
		// Flatten the full conversation history (including tool interactions) into userText.
		// Cursor's turns encoding is not reliably read by the model, but userText always works.
		log.Debugf("cursor: no checkpoint, flattening %d turns + %d tool results into userText", len(parsed.Turns), len(parsed.ToolResults))
		flattenConversationIntoUserText(parsed)
		params = buildRunRequestParams(resolvedModel, parsed, conversationId, idParams)
	}
	requestBytes, errEncode := cursorproto.EncodeRunRequest(params)
	if errEncode != nil {
		return nil, fmt.Errorf("cursor: encode run request: %w", errEncode)
	}
	framedRequest := cursorproto.FrameConnectMessage(requestBytes, 0)

	modelName := strings.TrimSpace(parsed.Model)
	if modelName == "" {
		modelName = req.Model
	}
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), modelName, auth)
	defer reporter.TrackFailure(ctx, &err)

	stream, err := openCursorH2Stream(e, auth, accessToken, clientType)
	if err != nil {
		return nil, err
	}

	if err := stream.Write(framedRequest); err != nil {
		stream.Close()
		return nil, fmt.Errorf("cursor: failed to send request: %w", err)
	}

	// Use a session-scoped context for the heartbeat that is NOT tied to the HTTP request.
	// This ensures the heartbeat survives across request boundaries during MCP tool execution.
	// Mirrors the TS plugin's setInterval-based heartbeat that lives independently of HTTP responses.
	sessionCtx, sessionCancel := context.WithCancel(context.Background())
	go cursorH2Heartbeat(sessionCtx, stream)

	// Abort the upstream session when the HTTP client disconnects mid-stream.
	// The MCP park path sets mcpParked BEFORE closing the output channel, so a
	// normal tool-call handoff (response ends, session parked for resume) is
	// never mistaken for a disconnect. Without this, the worker goroutine blocks
	// forever on the output channel and the H2 connection stays open.
	var mcpParked atomic.Bool
	go func() {
		select {
		case <-ctx.Done():
			if !mcpParked.Load() {
				log.Infof("cursor: client disconnected mid-stream (conv=%s), aborting upstream session", conversationId)
				sessionCancel()
				stream.Close()
			}
		case <-sessionCtx.Done():
		}
	}()

	chunks := make(chan cliproxyexecutor.StreamChunk, 64)
	chatId := "chatcmpl-" + uuid.New().String()[:28]
	created := time.Now().Unix()

	var streamParam any

	// Tool result channel for inline mode. processH2SessionFrames blocks on it
	// when mcpArgs is received, while continuing to handle KV/heartbeat.
	toolResultCh := make(chan []toolResultInfo, 1)

	// Switchable output: initially writes to `chunks`. After mcpArgs, the
	// onMcpExec callback closes `chunks` (ending the first HTTP response),
	// then processH2SessionFrames blocks on toolResultCh. When results arrive,
	// it switches to `resumeOutCh` (created by resumeWithToolResults).
	var outMu sync.Mutex
	currentOut := chunks

	emitToOut := func(chunk cliproxyexecutor.StreamChunk) {
		outMu.Lock()
		out := currentOut
		outMu.Unlock()
		if out == nil {
			return
		}
		select {
		case out <- chunk:
		case <-sessionCtx.Done():
			// Session aborted (e.g. client disconnect watchdog) — drop the chunk
			// so the frame loop can reach its ctx.Done exit instead of blocking.
		}
	}

	// Wrap sendChunk/sendDone to use emitToOut
	sendChunkSwitchable := func(delta string, finishReason string) {
		fr := "null"
		if finishReason != "" {
			fr = finishReason
		}
		openaiJSON := fmt.Sprintf(`{"id":"%s","object":"chat.completion.chunk","created":%d,"model":%s,"choices":[{"index":0,"delta":%s,"finish_reason":%s}]}`,
			chatId, created, jsonString(parsed.Model), delta, fr)
		sseLine := []byte("data: " + openaiJSON + "\n")

		if needsTranslate {
			translated := sdktranslator.TranslateStream(ctx, to, from, req.Model, originalPayload, payload, sseLine, &streamParam)
			for _, t := range translated {
				emitToOut(cliproxyexecutor.StreamChunk{Payload: bytes.Clone(t)})
			}
		} else {
			emitToOut(cliproxyexecutor.StreamChunk{Payload: []byte(openaiJSON)})
		}
	}

	sendDoneSwitchable := func() {
		if needsTranslate {
			done := sdktranslator.TranslateStream(ctx, to, from, req.Model, originalPayload, payload, []byte("data: [DONE]\n"), &streamParam)
			for _, d := range done {
				emitToOut(cliproxyexecutor.StreamChunk{Payload: bytes.Clone(d)})
			}
		} else {
			emitToOut(cliproxyexecutor.StreamChunk{Payload: []byte("[DONE]")})
		}
	}

	// sendStreamErrorSwitchable reports an upstream failure on an
	// already-started SSE response: OpenAI clients receive an error data line
	// followed by [DONE]; Claude-format clients receive the terminal
	// Anthropic error event.
	sendStreamErrorSwitchable := func(streamErr error) {
		errType := "api_error"
		var statusErr cursorStatusErr
		if errors.As(classifyCursorError(streamErr), &statusErr) {
			switch statusErr.code {
			case 429:
				errType = "rate_limit_error"
			case 401:
				errType = "authentication_error"
			case 403:
				errType = "permission_error"
			}
		}
		errMsg := streamErr.Error()
		if len(errMsg) > 500 {
			errMsg = errMsg[:500]
		}
		errJSON := fmt.Sprintf(`{"error":{"type":%s,"message":%s}}`, jsonString(errType), jsonString(errMsg))
		if needsTranslate {
			emitToOut(cliproxyexecutor.StreamChunk{Payload: []byte("event: error\ndata: " + errJSON + "\n\n")})
			return
		}
		emitToOut(cliproxyexecutor.StreamChunk{Payload: []byte("data: " + errJSON + "\n")})
		sendDoneSwitchable()
	}

	// Pre-response error detection for transparent failover:
	// If the stream fails before any chunk is emitted (e.g. quota exceeded),
	// ExecuteStream returns an error so the conductor retries with a different auth.
	streamErrCh := make(chan error, 1)
	firstChunkSent := make(chan struct{}, 1) // buffered: goroutine won't block signaling

	origEmitToOut := emitToOut
	emitToOut = func(chunk cliproxyexecutor.StreamChunk) {
		select {
		case firstChunkSent <- struct{}{}:
		default:
		}
		origEmitToOut(chunk)
	}

	go func() {
		var resumeOutCh chan cliproxyexecutor.StreamChunk
		_ = resumeOutCh
		thinkingActive := false
		toolCallIndex := 0
		tokenUsage := &cursorTokenUsage{}
		tokenUsage.setInputEstimate(len(payload))

		streamErr := processH2SessionFrames(sessionCtx, stream, params.BlobStore, params.McpTools,
			func(text string, isThinking bool) {
				if isThinking {
					if !thinkingActive {
						thinkingActive = true
						sendChunkSwitchable(`{"role":"assistant","content":"<think>"}`, "")
					}
					sendChunkSwitchable(fmt.Sprintf(`{"content":%s}`, jsonString(text)), "")
				} else {
					if thinkingActive {
						thinkingActive = false
						sendChunkSwitchable(`{"content":"</think>"}`, "")
					}
					sendChunkSwitchable(fmt.Sprintf(`{"content":%s}`, jsonString(text)), "")
				}
			},
			func(exec pendingMcpExec) {
				// Mark the park BEFORE closing the output channel: the watchdog
				// fires on request ctx end and must see the park to not treat a
				// normal tool-call handoff as a client disconnect.
				mcpParked.Store(true)
				if thinkingActive {
					thinkingActive = false
					sendChunkSwitchable(`{"content":"</think>"}`, "")
				}
				// ToolCallId/ToolName come from upstream unvalidated; control
				// characters there would split the SSE data line mid-JSON.
				toolCallJSON := fmt.Sprintf(`{"tool_calls":[{"index":%d,"id":%s,"type":"function","function":{"name":%s,"arguments":%s}}]}`,
					toolCallIndex, jsonString(exec.ToolCallId), jsonString(exec.ToolName), jsonString(exec.Args))
				toolCallIndex++
				sendChunkSwitchable(toolCallJSON, "")
				sendChunkSwitchable(`{}`, `"tool_calls"`)
				sendDoneSwitchable()

				// Close current output to end the current HTTP SSE response
				outMu.Lock()
				if currentOut != nil {
					close(currentOut)
					currentOut = nil
				}
				outMu.Unlock()

				// Create new resume output channel, reuse the same toolResultCh
				resumeOut := make(chan cliproxyexecutor.StreamChunk, 64)
				log.Debugf("cursor: saving session %s for MCP tool resume (tool=%s)", sessionKey, exec.ToolName)
				e.mu.Lock()
				e.sessions[sessionKey] = &cursorSession{
					stream:       stream,
					blobStore:    params.BlobStore,
					mcpTools:     params.McpTools,
					pending:      []pendingMcpExec{exec},
					cancel:       sessionCancel,
					createdAt:    time.Now(),
					authID:       authID,
					toolResultCh: toolResultCh, // reuse same channel across rounds
					resumeOutCh:  resumeOut,
					parked:       &mcpParked,
					tokenUsage:   tokenUsage,
					switchOutput: func(ch chan cliproxyexecutor.StreamChunk) {
						outMu.Lock()
						currentOut = ch
						// Reset translator state so the new HTTP response gets
						// a fresh message_start, content_block_start, etc.
						streamParam = nil
						// New response needs its own message ID
						chatId = "chatcmpl-" + uuid.New().String()[:28]
						created = time.Now().Unix()
						// tool_call indices restart within each HTTP response
						toolCallIndex = 0
						outMu.Unlock()
					},
				}
				e.mu.Unlock()
				resumeOutCh = resumeOut

				// processH2SessionFrames will now block on toolResultCh (inline wait loop)
				// while continuing to handle KV messages
			},
			toolResultCh,
			tokenUsage,
			func(cpData []byte) {
				// Save checkpoint keyed by conversationId, tagged with authID for migration detection
				e.mu.Lock()
				e.checkpoints[checkpointKey] = &savedCheckpoint{
					data:      cpData,
					blobStore: params.BlobStore,
					authID:    authID,
					updatedAt: time.Now(),
				}
				e.mu.Unlock()
				log.Debugf("cursor: saved checkpoint (%d bytes) for conv=%s auth=%s", len(cpData), checkpointKey, authID)
			},
		)

		// processH2SessionFrames returned — stream is done.
		// Check if error happened before any chunks were emitted.
		midStreamErr := false
		if streamErr != nil {
			select {
			case <-firstChunkSent:
				// Chunks were already sent to client — can't transparently retry.
				// Next request will failover via conductor's cooldown mechanism.
				log.Warnf("cursor: stream error after data sent (auth=%s conv=%s): %v", authID, conversationId, streamErr)
				midStreamErr = true
			default:
				// No data sent yet — propagate error for transparent conductor retry.
				log.Warnf("cursor: stream error before data sent (auth=%s conv=%s): %v — signaling retry", authID, conversationId, streamErr)
				streamErrCh <- streamErr
				outMu.Lock()
				if currentOut != nil {
					close(currentOut)
					currentOut = nil
				}
				outMu.Unlock()
				sessionCancel()
				stream.Close()
				return
			}
		}

		if thinkingActive {
			sendChunkSwitchable(`{"content":"</think>"}`, "")
		}

		if midStreamErr {
			// Tell the client the turn failed: a normal stop chunk would make
			// truncated output look like a complete answer.
			sendStreamErrorSwitchable(streamErr)
		} else {
			// Include token usage in the final stop chunk and publish to usage manager.
			inputTok, outputTok, cacheRead := tokenUsage.clientUsage()
			usageJSON := fmt.Sprintf(`{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}`, inputTok, outputTok, inputTok+outputTok)
			if cacheRead > 0 {
				usageJSON = fmt.Sprintf(`{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d,"prompt_tokens_details":{"cached_tokens":%d}}`, inputTok, outputTok, inputTok+outputTok, cacheRead)
			}
			// Build the stop chunk with usage embedded in the choices array level
			openaiJSON := fmt.Sprintf(`{"id":"%s","object":"chat.completion.chunk","created":%d,"model":%s,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":%s}`,
				chatId, created, jsonString(parsed.Model), usageJSON)
			sseLine := []byte("data: " + openaiJSON + "\n")
			if needsTranslate {
				translated := sdktranslator.TranslateStream(ctx, to, from, req.Model, originalPayload, payload, sseLine, &streamParam)
				for _, t := range translated {
					emitToOut(cliproxyexecutor.StreamChunk{Payload: bytes.Clone(t)})
				}
			} else {
				emitToOut(cliproxyexecutor.StreamChunk{Payload: []byte(openaiJSON)})
			}
			sendDoneSwitchable()
		}

		// Publish usage for management stats. Stream responses previously only
		// embedded usage in the SSE payload and never reached the usage manager.
		reportedUsage := tokenUsage.detail()
		if streamErr != nil && reportedUsage.InputTokens == 0 && reportedUsage.OutputTokens == 0 {
			reporter.PublishFailure(ctx, streamErr)
		} else {
			reporter.Publish(ctx, reportedUsage)
			reporter.EnsurePublished(ctx)
		}

		// Close whatever output channel is still active
		outMu.Lock()
		if currentOut != nil {
			close(currentOut)
			currentOut = nil
		}
		outMu.Unlock()
		sessionCancel()
		stream.Close()
	}()

	// Wait for either the first chunk or a pre-response error.
	// If the stream fails before emitting any data (e.g. quota exceeded),
	// return an error so the conductor retries with a different auth.
	select {
	case streamErr := <-streamErrCh:
		reporter.PublishFailure(ctx, streamErr)
		return nil, classifyCursorError(fmt.Errorf("cursor: stream failed before response: %w", streamErr))
	case <-firstChunkSent:
		// Data started flowing — return stream to client
		return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
	}
}

// resumeWithToolResults injects tool results into the running processH2SessionFrames
// via the toolResultCh channel. The original goroutine from ExecuteStream is still alive,
// blocking on toolResultCh. Once we send the results, it sends the MCP result to Cursor
// and continues processing the response text — all in the same goroutine that has been
// handling KV messages the whole time.
func (e *CursorExecutor) resumeWithToolResults(
	ctx context.Context,
	session *cursorSession,
	parsed *parsedOpenAIRequest,
	from, to sdktranslator.Format,
	req cliproxyexecutor.Request,
	originalPayload, payload []byte,
	needsTranslate bool,
) (*cliproxyexecutor.StreamResult, error) {
	log.Debugf("cursor: resumeWithToolResults: injecting %d tool results via channel", len(parsed.ToolResults))

	if session.toolResultCh == nil {
		return nil, fmt.Errorf("cursor: session has no toolResultCh (stale session?)")
	}
	if session.resumeOutCh == nil {
		return nil, fmt.Errorf("cursor: session has no resumeOutCh")
	}

	log.Debugf("cursor: resumeWithToolResults: switching output to resumeOutCh and injecting results")

	// Switch the output channel BEFORE injecting results, so that when
	// processH2SessionFrames unblocks and starts emitting text, it writes
	// to the resumeOutCh which the new HTTP handler is reading from.
	if session.switchOutput != nil {
		session.switchOutput(session.resumeOutCh)
	}

	// Re-arm disconnect detection for this resume response: reset the park flag
	// and watch the new request's context. A second tool call re-parks the
	// session (flag set again in onMcpExec); a mid-resume client disconnect
	// tears the upstream session down instead of blocking forever.
	if session.parked != nil {
		session.parked.Store(false)
		if session.cancel != nil && session.stream != nil {
			go func() {
				<-ctx.Done()
				if !session.parked.Load() {
					log.Infof("cursor: client disconnected during resume, aborting upstream session")
					session.cancel()
					session.stream.Close()
				}
			}()
		}
	}

	// Extend the input-token estimate for this round: the tool results become
	// part of the upstream conversation context.
	if session.tokenUsage != nil {
		resultBytes := 0
		for _, tr := range parsed.ToolResults {
			resultBytes += len(tr.Content)
		}
		session.tokenUsage.addInputEstimate(resultBytes)
	}

	// Inject tool results — this unblocks the waiting processH2SessionFrames
	session.toolResultCh <- parsed.ToolResults

	// Return the resumeOutCh for the new HTTP handler to read from
	return &cliproxyexecutor.StreamResult{Chunks: session.resumeOutCh}, nil
}

// --- H2Stream helpers ---

// openCursorH2Stream acquires a Run stream: a pre-warmed pooled connection
// when available, else a fresh TLS+HTTP/2 dial (optionally through the
// configured HTTP CONNECT proxy). The announced client identity selects the
// upstream usage pool ("cli" = plan pools, "sand" = Grok Bot weekly pool).
func openCursorH2Stream(e *CursorExecutor, auth *cliproxyauth.Auth, accessToken, clientType string) (*cursorproto.H2Stream, error) {
	requestId := uuid.New().String()
	headers := map[string]string{
		":path":                    cursorRunPath,
		"content-type":             "application/connect+proto",
		"connect-protocol-version": "1",
		"connect-accept-encoding":  "gzip",
		"user-agent":               "connect-es/1.6.1",
		"te":                       "trailers",
		"authorization":            "Bearer " + accessToken,
		"x-ghost-mode":             cursorGhostModeSetting(),
		"x-cursor-client-version":  cursorClientVersionValue(),
		"x-cursor-client-type":     clientType,
		"x-request-id":             requestId,
		"x-original-request-id":    requestId,
	}
	if clientType == "sand" {
		headers["x-sand-box-namespace"] = "prod"
	}
	host := cursorAgentHostSetting()
	proxy := resolveCursorProxy(e.cfg, auth)
	// A stale pooled connection (server GOAWAY during idle) fails at
	// OpenStream and falls through to a fresh dial.
	if cursorH2PoolSizeSetting() > 0 {
		if conn, err := acquireCursorH2Conn(host, proxy); err == nil {
			if stream, openErr := conn.OpenStream(headers); openErr == nil {
				return stream, nil
			}
			conn.Close()
		}
	}
	return cursorproto.DialH2Stream(host, proxy, headers)
}

func cursorH2Heartbeat(ctx context.Context, stream *cursorproto.H2Stream) {
	ticker := time.NewTicker(cursorHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hb, err := cursorproto.EncodeHeartbeat()
			if err != nil {
				log.Warnf("cursor: encode heartbeat: %v", err)
				return
			}
			frame := cursorproto.FrameConnectMessage(hb, 0)
			if err := stream.Write(frame); err != nil {
				return
			}
		}
	}
}

// --- Response processing ---

// cursorTokenUsage tracks token usage for one upstream turn. Output tokens
// stream in via TokenDeltaUpdate messages and input is estimated from payload
// size until the authoritative turn_ended counters arrive (once, at the end of
// the whole upstream turn — spanning every parked tool-call round).
type cursorTokenUsage struct {
	mu             sync.Mutex
	outputTokens   int64
	inputTokensEst int64 // estimated from request payload sizes

	hasTurnTotals        bool
	turnInputTokens      int64
	turnOutputTokens     int64
	turnCacheReadTokens  int64
	turnCacheWriteTokens int64
	turnReasoningTokens  int64
}

func (u *cursorTokenUsage) addOutput(delta int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.outputTokens += delta
}

func (u *cursorTokenUsage) setInputEstimate(payloadBytes int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	// Rough estimate: ~4 bytes per token for mixed content
	u.inputTokensEst = max(int64(payloadBytes/4), 1)
}

// addInputEstimate extends the estimate for a resumed round (tool results
// injected into the same upstream turn).
func (u *cursorTokenUsage) addInputEstimate(payloadBytes int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.inputTokensEst += max(int64(payloadBytes/4), 1)
}

// setTurnTotals records the authoritative turn_ended counters. Callers treat
// zero counters as "not reported" and keep using the estimates.
func (u *cursorTokenUsage) setTurnTotals(input, output, cacheRead, cacheWrite, reasoning int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.hasTurnTotals = true
	u.turnInputTokens = input
	u.turnOutputTokens = output
	u.turnCacheReadTokens = cacheRead
	u.turnCacheWriteTokens = cacheWrite
	u.turnReasoningTokens = reasoning
}

// get returns the accounting totals: real turn_ended counters when the
// upstream reported them, estimates otherwise.
func (u *cursorTokenUsage) get() (input, output int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	input, output = u.inputTokensEst, u.outputTokens
	if u.hasTurnTotals {
		if u.turnInputTokens > 0 {
			input = u.turnInputTokens
		}
		if u.turnOutputTokens > 0 {
			output = u.turnOutputTokens
		}
	}
	return input, output
}

// clientUsage returns per-request usage for the final SSE chunk. Cursor's
// turn_ended counters span the whole upstream turn (every parked tool-call
// round), so reporting them verbatim makes agentic clients think the context
// window is nearly exhausted; the input side is therefore clamped to this
// turn chain's estimated prompt size. Cache reads are reported separately as
// an OpenAI-style breakdown (prompt_tokens stays the full clamped input).
func (u *cursorTokenUsage) clientUsage() (input, output, cacheRead int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	input, output = u.inputTokensEst, u.outputTokens
	if u.hasTurnTotals {
		if u.turnOutputTokens > 0 {
			output = u.turnOutputTokens
		}
		if u.turnInputTokens > 0 && u.turnInputTokens < input {
			input = u.turnInputTokens
		}
	}
	cacheRead = u.turnCacheReadTokens
	if cacheRead > input {
		cacheRead = input
	}
	return input, output, cacheRead
}

func (u *cursorTokenUsage) detail() usage.Detail {
	if u == nil {
		return usage.Detail{}
	}
	input, output := u.get()
	return usage.Detail{
		InputTokens:  input,
		OutputTokens: output,
		TotalTokens:  input + output,
	}
}

// pendingExecFromMsg converts a decoded ExecMcpArgs server message into a
// pending MCP tool call for client delivery.
func pendingExecFromMsg(msg *cursorproto.DecodedServerMessage) pendingMcpExec {
	toolCallId := msg.McpToolCallId
	if toolCallId == "" {
		toolCallId = uuid.New().String()
	}
	return pendingMcpExec{
		ExecMsgId:  msg.ExecMsgId,
		ExecId:     msg.ExecId,
		ToolCallId: toolCallId,
		ToolName:   msg.McpToolName,
		Args:       decodeMcpArgsToJSON(msg.McpArgs),
	}
}

func processH2SessionFrames(
	ctx context.Context,
	stream *cursorproto.H2Stream,
	blobStore map[string][]byte,
	mcpTools []cursorproto.McpToolDef,
	onText func(text string, isThinking bool),
	onMcpExec func(exec pendingMcpExec),
	toolResultCh <-chan []toolResultInfo, // nil for no tool result injection; non-nil to wait for results
	tokenUsage *cursorTokenUsage, // tracks accumulated token usage (may be nil)
	onCheckpoint func(data []byte), // called when server sends conversation_checkpoint_update
) error {
	var buf bytes.Buffer
	rejectReason := "Tool not available in this environment. Use the MCP tools provided instead."
	// writeEncoded accepts Encode* multi-return values and propagates encode/write errors.
	writeEncoded := func(payload []byte, encErr error) error {
		if encErr != nil {
			return fmt.Errorf("cursor: encode response: %w", encErr)
		}
		if err := stream.Write(cursorproto.FrameConnectMessage(payload, 0)); err != nil {
			return fmt.Errorf("cursor: write response: %w", err)
		}
		return nil
	}
	// answerInteractionQuery grants server-initiated approval queries
	// (currently web search) so the upstream stream proceeds instead of
	// waiting forever on an unanswered prompt.
	answerInteractionQuery := func(q *cursorproto.DecodedServerMessage) error {
		if !q.InteractionQueryWebSearch {
			return nil
		}
		log.Debugf("cursor: approving interaction query id=%d (web search)", q.InteractionQueryId)
		return writeEncoded(cursorproto.EncodeInteractionQueryResponse(q.InteractionQueryId))
	}
	log.Debugf("cursor: processH2SessionFrames started for streamID=%s, waiting for data...", stream.ID())
	// Upstream liveness guards (see cursorLiveness): checked before every
	// wait. Only the main loop checks — parked tool-result waits are exempt,
	// since the client may legitimately take minutes to execute its tools.
	liveness := newCursorLiveness()
	livenessTimer := time.NewTimer(time.Hour)
	defer livenessTimer.Stop()
	for {
		if remaining, err := liveness.check(); err != nil {
			return err
		} else if remaining >= 0 {
			if !livenessTimer.Stop() {
				select {
				case <-livenessTimer.C:
				default:
				}
			}
			livenessTimer.Reset(remaining)
		}
		select {
		case <-livenessTimer.C:
			// The loop top re-checks and reports the guard that fired.
			continue
		case <-ctx.Done():
			log.Debugf("cursor: processH2SessionFrames exiting: context done")
			return ctx.Err()
		case data, ok := <-stream.Data():
			if !ok {
				log.Debugf("cursor: processH2SessionFrames[%s]: exiting: stream data channel closed", stream.ID())
				return stream.Err() // may be RST_STREAM, GOAWAY, or nil for clean close
			}
			liveness.markFrame()
			// Log first 20 bytes of raw data for debugging
			previewLen := min(20, len(data))
			log.Debugf("cursor: processH2SessionFrames[%s]: received %d bytes from dataCh, first bytes: %x (%q)", stream.ID(), len(data), data[:previewLen], string(data[:previewLen]))
			buf.Write(data)
			log.Debugf("cursor: processH2SessionFrames[%s]: buf total=%d", stream.ID(), buf.Len())

			// Process all complete frames
			for {
				currentBuf := buf.Bytes()
				if len(currentBuf) == 0 {
					break
				}
				flags, payload, consumed, ok := cursorproto.ParseConnectFrame(currentBuf)
				if !ok {
					// Log detailed info about why parsing failed
					previewLen := min(20, len(currentBuf))
					log.Debugf("cursor: incomplete frame in buffer, waiting for more data (buf=%d bytes, first bytes: %x = %q)", len(currentBuf), currentBuf[:previewLen], string(currentBuf[:previewLen]))
					break
				}
				buf.Next(consumed)
				log.Debugf("cursor: parsed Connect frame flags=0x%02x payload=%d bytes consumed=%d", flags, len(payload), consumed)

				if flags&cursorproto.ConnectEndStreamFlag != 0 {
					if err := cursorproto.ParseConnectEndStream(payload); err != nil {
						log.Warnf("cursor: connect end stream error: %v", err)
						return err // propagate server-side errors (quota, rate limit, etc.)
					}
					continue
				}

				// The stream advertises connect-accept-encoding: gzip; a
				// compressed message carries the compression flag.
				decompressed, gzErr := cursorproto.DecompressConnectPayload(flags, payload)
				if gzErr != nil {
					log.Warnf("cursor: decompress connect payload: %v", gzErr)
					continue
				}
				payload = decompressed

				msg, err := cursorproto.DecodeAgentServerMessage(payload)
				if err != nil {
					log.Debugf("cursor: failed to decode server message: %v", err)
					continue
				}

				log.Debugf("cursor: decoded server message type=%d", msg.Type)
				switch msg.Type {
				case cursorproto.ServerMsgTextDelta:
					if msg.Text != "" && onText != nil {
						liveness.markOutput()
						onText(msg.Text, false)
					}
				case cursorproto.ServerMsgThinkingDelta:
					if msg.Text != "" && onText != nil {
						liveness.markOutput()
						onText(msg.Text, true)
					}
				case cursorproto.ServerMsgThinkingCompleted:
					// Handled by caller

				case cursorproto.ServerMsgTurnEnded:
					log.Debugf("cursor: TurnEnded received, stream will finish")
					if tokenUsage != nil && msg.HasTurnTokens {
						tokenUsage.setTurnTotals(msg.TurnInputTokens, msg.TurnOutputTokens,
							msg.TurnCacheReadTokens, msg.TurnCacheWriteTokens, msg.TurnReasoningTokens)
					}
					return nil // clean completion

				case cursorproto.ServerMsgHeartbeat:
					// Server heartbeat, ignore silently
					continue

				case cursorproto.ServerMsgInteractionQuery:
					if err := answerInteractionQuery(msg); err != nil {
						return err
					}
					continue

				case cursorproto.ServerMsgCheckpoint:
					if onCheckpoint != nil && len(msg.CheckpointData) > 0 {
						onCheckpoint(msg.CheckpointData)
					}
					continue

				case cursorproto.ServerMsgTokenDelta:
					if tokenUsage != nil && msg.TokenDelta > 0 {
						tokenUsage.addOutput(msg.TokenDelta)
					}
					continue

				case cursorproto.ServerMsgKvGetBlob:
					blobKey := cursorproto.BlobIdHex(msg.BlobId)
					data := blobStore[blobKey]
					if err := writeEncoded(cursorproto.EncodeKvGetBlobResult(msg.KvId, data)); err != nil {
						return err
					}

				case cursorproto.ServerMsgKvSetBlob:
					blobKey := cursorproto.BlobIdHex(msg.BlobId)
					blobStore[blobKey] = append([]byte(nil), msg.BlobData...)
					if err := writeEncoded(cursorproto.EncodeKvSetBlobResult(msg.KvId)); err != nil {
						return err
					}

				case cursorproto.ServerMsgExecRequestCtx:
					if err := writeEncoded(cursorproto.EncodeExecRequestContextResult(msg.ExecMsgId, msg.ExecId, mcpTools)); err != nil {
						return err
					}

				case cursorproto.ServerMsgExecMcpArgs:
					if onMcpExec != nil {
						// A tool call is user-visible output: it satisfies the
						// first-output clock.
						liveness.markOutput()
						// Parallel tool calls arrive as several ExecMcpArgs
						// frames, often inside the same read. Every call must
						// reach the client and be answered — frames decoded
						// while parked used to be dropped, leaving the upstream
						// waiting on a result that never came.
						var lateQueue []pendingMcpExec

						// waitForToolResults blocks until tool results are
						// injected, servicing KV/context/checkpoint frames in
						// the meantime. ExecMcpArgs frames that arrive while
						// parked are queued and delivered after this call.
						waitForToolResults := func() ([]toolResultInfo, error) {
							for {
								select {
								case <-ctx.Done():
									return nil, ctx.Err()
								case results, ok := <-toolResultCh:
									if !ok {
										return nil, nil
									}
									return results, nil
								case waitData, ok := <-stream.Data():
									if !ok {
										return nil, stream.Err()
									}
									liveness.markFrame()
									buf.Write(waitData)
									for {
										cb := buf.Bytes()
										if len(cb) == 0 {
											break
										}
										wf, wp, wc, wok := cursorproto.ParseConnectFrame(cb)
										if !wok {
											break
										}
										buf.Next(wc)
										if wf&cursorproto.ConnectEndStreamFlag != 0 {
											continue
										}
										if wp, gzErr = cursorproto.DecompressConnectPayload(wf, wp); gzErr != nil {
											log.Warnf("cursor: decompress connect payload: %v", gzErr)
											continue
										}
										wmsg, werr := cursorproto.DecodeAgentServerMessage(wp)
										if werr != nil {
											continue
										}
										switch wmsg.Type {
										case cursorproto.ServerMsgKvGetBlob:
											blobKey := cursorproto.BlobIdHex(wmsg.BlobId)
											d := blobStore[blobKey]
											if err := writeEncoded(cursorproto.EncodeKvGetBlobResult(wmsg.KvId, d)); err != nil {
												return nil, err
											}
										case cursorproto.ServerMsgKvSetBlob:
											blobKey := cursorproto.BlobIdHex(wmsg.BlobId)
											blobStore[blobKey] = append([]byte(nil), wmsg.BlobData...)
											if err := writeEncoded(cursorproto.EncodeKvSetBlobResult(wmsg.KvId)); err != nil {
												return nil, err
											}
										case cursorproto.ServerMsgExecRequestCtx:
											if err := writeEncoded(cursorproto.EncodeExecRequestContextResult(wmsg.ExecMsgId, wmsg.ExecId, mcpTools)); err != nil {
												return nil, err
											}
										case cursorproto.ServerMsgCheckpoint:
											if onCheckpoint != nil && len(wmsg.CheckpointData) > 0 {
												onCheckpoint(wmsg.CheckpointData)
											}
										case cursorproto.ServerMsgInteractionQuery:
											if err := answerInteractionQuery(wmsg); err != nil {
												return nil, err
											}
										case cursorproto.ServerMsgExecMcpArgs:
											lateQueue = append(lateQueue, pendingExecFromMsg(wmsg))
											log.Debugf("cursor: queued parallel mcpArgs while parked: tool=%q callId=%q (queue=%d)", wmsg.McpToolName, wmsg.McpToolCallId, len(lateQueue))
										}
									}
								case <-stream.Done():
									return nil, stream.Err()
								}
							}
						}

						exec := pendingExecFromMsg(msg)
						for {
							onMcpExec(exec)
							if toolResultCh == nil {
								return nil
							}
							log.Debugf("cursor: waiting for tool result on channel (inline mode)...")
							toolResults, errWait := waitForToolResults()
							if errWait != nil {
								return errWait
							}
							if toolResults == nil {
								return nil // result channel closed: session teardown
							}
							// Send MCP result. Matching must never silently
							// fail: a client that normalizes tool-call ids
							// (upstream ids have been observed to contain
							// newlines) would otherwise leave the upstream
							// waiting for a result forever. An unmatched call
							// is answered with an error result so the turn
							// cannot stall either.
							if tr, ok := matchToolResult(toolResults, exec.ToolCallId); ok {
								log.Debugf("cursor: sending inline MCP result for tool=%s (call id %q)", exec.ToolName, exec.ToolCallId)
								if err := writeEncoded(cursorproto.EncodeExecMcpResult(exec.ExecMsgId, exec.ExecId, tr.Content, false)); err != nil {
									return err
								}
							} else {
								log.Warnf("cursor: answering unmatched tool call %q with an error result to avoid stalling the upstream", exec.ToolCallId)
								if err := writeEncoded(cursorproto.EncodeExecMcpError(exec.ExecMsgId, exec.ExecId, "no tool result was provided for this call")); err != nil {
									return err
								}
							}
							if len(lateQueue) == 0 {
								break
							}
							// Deliver the next parked parallel call through the
							// resumed output; its results arrive on a follow-up
							// request.
							exec = lateQueue[0]
							lateQueue = lateQueue[1:]
						}
						continue
					}

				case cursorproto.ServerMsgExecReadArgs:
					if err := writeEncoded(cursorproto.EncodeExecReadRejected(msg.ExecMsgId, msg.ExecId, msg.Path, rejectReason)); err != nil {
						return err
					}
				case cursorproto.ServerMsgExecWriteArgs:
					if err := writeEncoded(cursorproto.EncodeExecWriteRejected(msg.ExecMsgId, msg.ExecId, msg.Path, rejectReason)); err != nil {
						return err
					}
				case cursorproto.ServerMsgExecDeleteArgs:
					if err := writeEncoded(cursorproto.EncodeExecDeleteRejected(msg.ExecMsgId, msg.ExecId, msg.Path, rejectReason)); err != nil {
						return err
					}
				case cursorproto.ServerMsgExecLsArgs:
					if err := writeEncoded(cursorproto.EncodeExecLsRejected(msg.ExecMsgId, msg.ExecId, msg.Path, rejectReason)); err != nil {
						return err
					}
				case cursorproto.ServerMsgExecGrepArgs:
					if err := writeEncoded(cursorproto.EncodeExecGrepError(msg.ExecMsgId, msg.ExecId, rejectReason)); err != nil {
						return err
					}
				case cursorproto.ServerMsgExecShellArgs, cursorproto.ServerMsgExecShellStream:
					if err := writeEncoded(cursorproto.EncodeExecShellRejected(msg.ExecMsgId, msg.ExecId, msg.Command, msg.WorkingDirectory, rejectReason)); err != nil {
						return err
					}
				case cursorproto.ServerMsgExecBgShellSpawn:
					if err := writeEncoded(cursorproto.EncodeExecBackgroundShellSpawnRejected(msg.ExecMsgId, msg.ExecId, msg.Command, msg.WorkingDirectory, rejectReason)); err != nil {
						return err
					}
				case cursorproto.ServerMsgExecFetchArgs:
					if err := writeEncoded(cursorproto.EncodeExecFetchError(msg.ExecMsgId, msg.ExecId, msg.Url, rejectReason)); err != nil {
						return err
					}
				case cursorproto.ServerMsgExecDiagnostics:
					if err := writeEncoded(cursorproto.EncodeExecDiagnosticsResult(msg.ExecMsgId, msg.ExecId)); err != nil {
						return err
					}
				case cursorproto.ServerMsgExecWriteShellStdin:
					if err := writeEncoded(cursorproto.EncodeExecWriteShellStdinError(msg.ExecMsgId, msg.ExecId, rejectReason)); err != nil {
						return err
					}
				}
			}

		case <-stream.Done():
			log.Debugf("cursor: processH2SessionFrames exiting: stream done")
			return stream.Err()
		}
	}
}

// --- OpenAI request parsing ---

type parsedOpenAIRequest struct {
	Model        string
	Messages     []gjson.Result
	Tools        []gjson.Result
	Stream       bool
	SystemPrompt string
	UserText     string
	Images       []cursorproto.ImageData
	Turns        []cursorproto.TurnData
	ToolResults  []toolResultInfo
	// ThinkingEffort is the normalized effort request ("", "none", or a
	// level). The thinking applier writes it as reasoning_effort on the
	// OpenAI-shaped payload; RequestedModel parameters are derived from it.
	ThinkingEffort string
}

type toolResultInfo struct {
	ToolCallId string
	Content    string
}

func parseOpenAIRequest(payload []byte) *parsedOpenAIRequest {
	p := &parsedOpenAIRequest{
		Model:          gjson.GetBytes(payload, "model").String(),
		Stream:         gjson.GetBytes(payload, "stream").Bool(),
		ThinkingEffort: cursorThinkingEffortFromPayload(payload),
	}

	messages := gjson.GetBytes(payload, "messages").Array()
	p.Messages = messages

	// Extract system prompt
	var systemParts []string
	for _, msg := range messages {
		if msg.Get("role").String() == "system" {
			systemParts = append(systemParts, extractTextContent(msg.Get("content")))
		}
	}
	if len(systemParts) > 0 {
		p.SystemPrompt = strings.Join(systemParts, "\n")
	} else {
		p.SystemPrompt = "You are a helpful assistant."
	}

	// Extract turns, tool results, and last user message
	var pendingUser string
	for _, msg := range messages {
		role := msg.Get("role").String()
		switch role {
		case "system":
			continue
		case "tool":
			p.ToolResults = append(p.ToolResults, toolResultInfo{
				ToolCallId: msg.Get("tool_call_id").String(),
				Content:    extractTextContent(msg.Get("content")),
			})
		case "user":
			if pendingUser != "" {
				p.Turns = append(p.Turns, cursorproto.TurnData{UserText: pendingUser})
			}
			pendingUser = extractTextContent(msg.Get("content"))
			p.Images = extractImages(msg.Get("content"))
		case "assistant":
			assistantText := extractTextContent(msg.Get("content"))
			if tc := describeAssistantToolCalls(msg); tc != "" {
				if assistantText != "" {
					assistantText += "\n" + tc
				} else {
					assistantText = tc
				}
			}
			if pendingUser != "" {
				p.Turns = append(p.Turns, cursorproto.TurnData{
					UserText:      pendingUser,
					AssistantText: assistantText,
				})
				pendingUser = ""
			} else if len(p.Turns) > 0 && assistantText != "" {
				// Assistant message after tool results (no pending user) —
				// append to the last turn's assistant text to preserve context.
				last := &p.Turns[len(p.Turns)-1]
				if last.AssistantText != "" {
					last.AssistantText += "\n" + assistantText
				} else {
					last.AssistantText = assistantText
				}
			}
		}
	}

	if pendingUser != "" {
		p.UserText = pendingUser
	} else if len(p.Turns) > 0 && len(p.ToolResults) == 0 {
		last := p.Turns[len(p.Turns)-1]
		p.Turns = p.Turns[:len(p.Turns)-1]
		p.UserText = last.UserText
	}

	// Extract tools
	p.Tools = gjson.GetBytes(payload, "tools").Array()

	return p
}

// matchToolResult finds the tool result for a pending MCP call. Exact id match
// first, then a whitespace-trimmed match (clients may normalize tool-call ids,
// and upstream ids have been observed to contain newlines), and finally the
// first available result so the upstream is never left waiting forever. The
// boolean reports whether a result was found; degraded matches are logged.
func matchToolResult(results []toolResultInfo, callID string) (toolResultInfo, bool) {
	for _, tr := range results {
		if tr.ToolCallId == callID {
			return tr, true
		}
	}
	trimmed := strings.TrimSpace(callID)
	if trimmed != "" {
		for _, tr := range results {
			if strings.TrimSpace(tr.ToolCallId) == trimmed {
				log.Warnf("cursor: tool result matched only after trimming call id %q", callID)
				return tr, true
			}
		}
	}
	if len(results) > 0 {
		log.Warnf("cursor: no tool result matched call id %q (%d result(s) available); falling back to the first result", callID, len(results))
		return results[0], true
	}
	log.Warnf("cursor: no tool result available for pending call id %q", callID)
	return toolResultInfo{}, false
}

// describeAssistantToolCalls renders OpenAI assistant tool_calls as plain text
// so flattened conversation history preserves which tools were invoked with
// which arguments — the upstream only sees text turns.
func describeAssistantToolCalls(msg gjson.Result) string {
	calls := msg.Get("tool_calls")
	if !calls.IsArray() {
		return ""
	}
	var sb strings.Builder
	calls.ForEach(func(_, call gjson.Result) bool {
		fn := call.Get("function")
		name := fn.Get("name").String()
		if name == "" {
			return true
		}
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		args := strings.TrimSpace(fn.Get("arguments").String())
		if args == "" {
			args = "{}"
		}
		if len(args) > 2000 {
			args = args[:2000] + "... [truncated]"
		}
		sb.WriteString("[Called tool " + name)
		if id := call.Get("id").String(); id != "" {
			sb.WriteString(" (call_id: " + id + ")")
		}
		sb.WriteString(" with arguments: " + args + "]")
		return true
	})
	return sb.String()
}

// bakeToolResultsIntoTurns merges tool results into the last turn's assistant text
// when there's no active H2 session to resume. This ensures the model sees the
// full tool interaction context in a new conversation.
// flattenConversationIntoUserText flattens the full conversation history
// (turns + tool results) into the UserText field as plain text.
// This is the fallback for cold resume when no checkpoint is available.
// Cursor reliably reads UserText but ignores structured turns.
func flattenConversationIntoUserText(parsed *parsedOpenAIRequest) {
	var buf strings.Builder

	// Flatten turns into readable context
	for _, turn := range parsed.Turns {
		if turn.UserText != "" {
			buf.WriteString("USER: ")
			buf.WriteString(turn.UserText)
			buf.WriteString("\n\n")
		}
		if turn.AssistantText != "" {
			buf.WriteString("ASSISTANT: ")
			buf.WriteString(turn.AssistantText)
			buf.WriteString("\n\n")
		}
	}

	// Flatten tool results
	for _, tr := range parsed.ToolResults {
		buf.WriteString("TOOL_RESULT (call_id: ")
		buf.WriteString(tr.ToolCallId)
		buf.WriteString("): ")
		// Truncate very large tool results to avoid overwhelming the context
		content := tr.Content
		if len(content) > 8000 {
			content = content[:8000] + "\n... [truncated]"
		}
		buf.WriteString(content)
		buf.WriteString("\n\n")
	}

	if buf.Len() > 0 {
		buf.WriteString("The above is the previous conversation context including tool call results.\n")
		buf.WriteString("Continue your response based on this context.\n\n")
	}

	// Prepend flattened history to the current UserText
	if parsed.UserText != "" {
		parsed.UserText = buf.String() + "Current request: " + parsed.UserText
	} else {
		parsed.UserText = buf.String() + "Continue from the conversation above."
	}

	// Clear turns and tool results since they're now in UserText
	parsed.Turns = nil
	parsed.ToolResults = nil
}

func extractTextContent(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsArray() {
		var parts []string
		for _, part := range content.Array() {
			if part.Get("type").String() == "text" {
				parts = append(parts, part.Get("text").String())
			}
		}
		return strings.Join(parts, "")
	}
	return content.String()
}

func extractImages(content gjson.Result) []cursorproto.ImageData {
	if !content.IsArray() {
		return nil
	}
	var images []cursorproto.ImageData
	for _, part := range content.Array() {
		if part.Get("type").String() == "image_url" {
			url := part.Get("image_url.url").String()
			if strings.HasPrefix(url, "data:") {
				img := parseDataURL(url)
				if img != nil {
					images = append(images, *img)
				}
			}
		}
	}
	return images
}

// imageSize decodes pixel dimensions from PNG/GIF/JPEG/WEBP headers. Some
// upstream models silently drop attachments without dimensions, so callers
// should include them whenever they can be recovered. Returns (0, 0) when the
// format is unknown or the header is malformed.
func imageSize(data []byte) (width, height int) {
	switch {
	case len(data) > 24 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		// IHDR: width/height are big-endian uint32 at offset 16.
		return int(uint32(data[16])<<24 | uint32(data[17])<<16 | uint32(data[18])<<8 | uint32(data[19])),
			int(uint32(data[20])<<24 | uint32(data[21])<<16 | uint32(data[22])<<8 | uint32(data[23]))
	case len(data) > 10 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return int(uint16(data[6]) | uint16(data[7])<<8), int(uint16(data[8]) | uint16(data[9])<<8)
	case len(data) > 29 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" && string(data[12:16]) == "VP8X":
		// VP8X stores canvas size minus one in 24-bit little-endian fields.
		return int(uint32(data[24])|uint32(data[25])<<8|uint32(data[26])<<16) + 1,
			int(uint32(data[27])|uint32(data[28])<<8|uint32(data[29])<<16) + 1
	case len(data) > 9 && data[0] == 0xFF && data[1] == 0xD8:
		// JPEG: scan markers for the first SOF0-SOF15 frame (skipping DHT/JPG/DAC).
		for i := 2; i+9 < len(data); {
			if data[i] != 0xFF {
				i++
				continue
			}
			marker := data[i+1]
			if marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
				i += 2
				continue
			}
			segLen := int(uint16(data[i+2])<<8 | uint16(data[i+3]))
			if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
				if i+9 < len(data) {
					return int(uint16(data[i+7])<<8 | uint16(data[i+8])), int(uint16(data[i+5])<<8 | uint16(data[i+6]))
				}
				return 0, 0
			}
			if segLen < 2 {
				return 0, 0
			}
			i += 2 + segLen
		}
	}
	return 0, 0
}

func parseDataURL(url string) *cursorproto.ImageData {
	// data:image/png;base64,...
	if !strings.HasPrefix(url, "data:") {
		return nil
	}
	parts := strings.SplitN(url[5:], ";", 2)
	if len(parts) != 2 {
		return nil
	}
	mimeType := parts[0]
	if !strings.HasPrefix(parts[1], "base64,") {
		return nil
	}
	encoded := parts[1][7:]
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		// Try RawStdEncoding for unpadded base64
		data, err = base64.RawStdEncoding.DecodeString(encoded)
		if err != nil {
			return nil
		}
	}
	w, h := imageSize(data)
	return &cursorproto.ImageData{
		MimeType: mimeType,
		Data:     data,
		Width:    w,
		Height:   h,
	}
}

// buildRunRequestParams assembles the Run request. idParams carries the
// parameters decoded from a variant-style model id; explicit request
// parameters (parsed.ThinkingEffort) override them.
func buildRunRequestParams(requestModel string, parsed *parsedOpenAIRequest, conversationId string, idParams map[string]string) *cursorproto.RunRequestParams {
	modelID := strings.TrimSpace(requestModel)
	if modelID == "" {
		modelID = parsed.Model
	}
	params := &cursorproto.RunRequestParams{
		ModelId:        modelID,
		SystemPrompt:   parsed.SystemPrompt,
		UserText:       parsed.UserText,
		MessageId:      uuid.New().String(),
		ConversationId: conversationId,
		Images:         parsed.Images,
		Turns:          parsed.Turns,
		BlobStore:      make(map[string][]byte),
		ModelParams:    cursorMergeModelParams(modelID, idParams, parsed.ThinkingEffort),
	}

	// Convert OpenAI tools to McpToolDefs
	for _, tool := range parsed.Tools {
		fn := tool.Get("function")
		params.McpTools = append(params.McpTools, cursorproto.McpToolDef{
			Name:        fn.Get("name").String(),
			Description: fn.Get("description").String(),
			InputSchema: json.RawMessage(fn.Get("parameters").Raw),
		})
	}

	return params
}

// cursorThinkingEffortFromPayload extracts the requested reasoning level from
// any of the three client styles on the OpenAI-shaped payload:
//   - Chat Completions: reasoning_effort ("high");
//   - Responses API:    reasoning.effort ("high");
//   - Anthropic:        thinking.budget_tokens (converted via the shared
//     budget→level ladder; thinking.type "disabled" maps to none).
//
// Normally the format translators have already normalized the first two into
// reasoning_effort; the extra lookups are belt-and-braces for clients that
// mix styles or translator paths that do not carry the fields. Empty when the
// client expressed no preference (upstream defaults apply).
func cursorThinkingEffortFromPayload(payload []byte) string {
	if v := gjson.GetBytes(payload, "reasoning_effort"); v.Exists() {
		return strings.TrimSpace(v.String())
	}
	if v := gjson.GetBytes(payload, "reasoning.effort"); v.Exists() {
		return strings.TrimSpace(v.String())
	}
	if v := gjson.GetBytes(payload, "thinking"); v.Exists() && v.IsObject() {
		switch strings.ToLower(strings.TrimSpace(v.Get("type").String())) {
		case "disabled":
			return string(thinking.LevelNone)
		case "enabled":
			if b := v.Get("budget_tokens"); b.Exists() {
				if level, ok := thinking.ConvertBudgetToLevel(int(b.Int())); ok {
					return level
				}
			}
		}
	}
	return ""
}

// cursorModelParamOptions caches per-model parameter definitions from the
// AvailableModels catalog: model id -> {parameter id -> allowed values}.
// Cursor rejects parameter ids or values a model does not declare, so request
// parameters are gated against this table.
var cursorModelParamOptions sync.Map

func cursorSetParamOptions(modelId string, options map[string][]string) {
	if modelId != "" && len(options) > 0 {
		cursorModelParamOptions.Store(modelId, options)
	}
}

// cursorEffortLadder orders reasoning levels weakest to strongest.
var cursorEffortLadder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// cursorNearestEffort clamps a requested level onto the values the model
// actually publishes; empty when nothing maps.
func cursorNearestEffort(level string, allowed []string) string {
	switch level {
	case "minimal":
		level = "low"
	case "default":
		level = "medium"
	case "highest":
		level = "max"
	}
	want := -1
	for i, l := range cursorEffortLadder {
		if l == level {
			want = i
			break
		}
	}
	if want < 0 {
		return ""
	}
	best, bestDist := "", len(cursorEffortLadder)+1
	for _, a := range allowed {
		idx := -1
		for i, l := range cursorEffortLadder {
			if l == a {
				idx = i
				break
			}
		}
		if idx < 0 {
			continue
		}
		dist := idx - want
		if dist < 0 {
			dist = -dist
		}
		if dist < bestDist {
			bestDist, best = dist, a
		}
	}
	return best
}

// cursorModelParamsFor translates a requested effort level into
// RequestedModel parameters ("thinking"/"effort"/"reasoning"), gated on the
// model's declared options from the account catalog.
func cursorModelParamsFor(modelId, effort string) map[string]string {
	if modelId == "" || effort == "" {
		return nil
	}
	raw, ok := cursorModelParamOptions.Load(modelId)
	if !ok {
		return nil
	}
	options, _ := raw.(map[string][]string)
	params := make(map[string]string)
	if values, ok := options["thinking"]; ok {
		if effort == "none" {
			if slices.Contains(values, "false") {
				params["thinking"] = "false"
			}
		} else if slices.Contains(values, "true") {
			params["thinking"] = "true"
		}
	}
	if effort != "none" {
		for _, pid := range []string{"effort", "reasoning"} {
			if values, ok := options[pid]; ok {
				if clamped := cursorNearestEffort(effort, values); clamped != "" {
					params[pid] = clamped
				}
				break
			}
		}
	}
	if len(params) == 0 {
		return nil
	}
	return params
}

// cursorMergeModelParams combines the parameters decoded from a variant-style
// model id with the explicitly requested reasoning level. The explicit
// request wins: its effort/thinking choices replace the id-embedded ones,
// while unrelated id parameters (e.g. fast) survive.
func cursorMergeModelParams(modelId string, idParams map[string]string, bodyEffort string) map[string]string {
	merged := make(map[string]string, len(idParams)+2)
	for k, v := range idParams {
		merged[k] = v
	}
	if bodyEffort != "" {
		delete(merged, "effort")
		delete(merged, "reasoning")
		delete(merged, "thinking")
		for k, v := range cursorModelParamsFor(modelId, bodyEffort) {
			merged[k] = v
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

// cursorDecomposeVariantId folds a variant-expanded model id (e.g.
// "claude-opus-5-thinking-max-fast") back into its base id and encoded
// RequestedModel parameters. The candidate base must be a known picker
// catalog id and every remaining token must map to a parameter value the
// base declares; otherwise the id is not a recognized variant and is
// returned unchanged. Longest base wins so "gpt-5.4-mini-none" decomposes
// against "gpt-5.4-mini" rather than "gpt-5.4".
func cursorDecomposeVariantId(id string) (string, map[string]string, bool) {
	idLower := strings.ToLower(strings.TrimSpace(id))
	if idLower == "" {
		return "", nil, false
	}
	bases := make([]string, 0, 16)
	cursorModelParamOptions.Range(func(k, _ any) bool {
		if s, ok := k.(string); ok && len(s) > 0 && len(s) < len(idLower) {
			bases = append(bases, s)
		}
		return true
	})
	sort.Slice(bases, func(i, j int) bool { return len(bases[i]) > len(bases[j]) })
	for _, base := range bases {
		if !strings.HasPrefix(idLower, base+"-") {
			continue
		}
		raw, _ := cursorModelParamOptions.Load(base)
		options, _ := raw.(map[string][]string)
		if params, ok := cursorTokensToParams(options, strings.Split(idLower[len(base)+1:], "-")); ok {
			return base, params, true
		}
	}
	return "", nil, false
}

// cursorTokensToParams maps the dash-separated suffix tokens of a variant id
// onto RequestedModel parameters, validated against the base model's declared
// options. Any unrecognized token fails the whole decomposition.
func cursorTokensToParams(options map[string][]string, tokens []string) (map[string]string, bool) {
	if len(options) == 0 || len(tokens) == 0 {
		return nil, false
	}
	params := make(map[string]string, len(tokens))
	for _, tok := range tokens {
		switch {
		case tok == "thinking" && slices.Contains(options["thinking"], "true"):
			params["thinking"] = "true"
		case tok == "fast":
			// The fast/max-mode switch is not published as a parameter
			// definition; it is accepted unconditionally (matches the ids
			// the agent usable list itself hands out).
			params["fast"] = "true"
		default:
			matched := false
			for _, pid := range []string{"effort", "reasoning"} {
				if values, ok := options[pid]; ok && slices.Contains(values, tok) {
					params[pid] = tok
					matched = true
					break
				}
			}
			if !matched {
				return nil, false
			}
		}
	}
	if len(params) == 0 {
		return nil, false
	}
	return params, true
}

// cursorCleanCatalogFilter hides variant-expanded ids from the exposed model
// list, keeping clean base ids and non-variant special ids (e.g. "default").
// Hidden variant ids remain routable: request-time decomposition folds them
// back into base + parameters.
func cursorCleanCatalogFilter(models []*registry.ModelInfo) []*registry.ModelInfo {
	out := make([]*registry.ModelInfo, 0, len(models))
	for _, info := range models {
		if info == nil || info.ID == "" {
			continue
		}
		if _, _, ok := cursorDecomposeVariantId(info.ID); ok {
			continue
		}
		out = append(out, info)
	}
	return out
}

// --- Helpers ---

// cursorModelCatalog lists the currently registered Cursor model IDs for
// request-time model resolution (see helps.ResolveCursorModelID). Empty when
// nothing is registered yet — resolution then passes names through unchanged.
func cursorModelCatalog() []string {
	infos := registry.GetGlobalRegistry().GetAvailableModelsByProvider(cursorAuthType)
	ids := make([]string, 0, len(infos))
	for _, info := range infos {
		if info != nil && info.ID != "" {
			ids = append(ids, info.ID)
		}
	}
	return ids
}

func cursorAccessToken(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if v, ok := auth.Metadata["access_token"].(string); ok {
		return v
	}
	return ""
}

func cursorRefreshToken(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if v, ok := auth.Metadata["refresh_token"].(string); ok {
		return v
	}
	return ""
}

func applyCursorHeaders(req *http.Request, accessToken string) {
	req.Header.Set("Content-Type", "application/connect+proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Te", "trailers")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-Ghost-Mode", cursorGhostModeSetting())
	req.Header.Set("X-Cursor-Client-Version", cursorClientVersionValue())
	req.Header.Set("X-Cursor-Client-Type", "cli")
	req.Header.Set("X-Request-Id", uuid.New().String())
}

func newH2Client() *http.Client {
	return &http.Client{
		Transport: &http2.Transport{
			TLSClientConfig: &tls.Config{},
		},
	}
}

// doCursorUnary performs a unary Connect/HTTP2 request and returns the response body.
// Cursor's protocol is not JSON/SSE, so this stays separate from helps.DoJSON.
func doCursorUnary(ctx context.Context, client *http.Client, requestURL, accessToken string, body []byte, contentType string) (int, []byte, error) {
	if client == nil {
		client = newH2Client()
	}
	if contentType == "" {
		contentType = "application/proto"
	}
	h2Req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	h2Req.Header.Set("Content-Type", contentType)
	h2Req.Header.Set("Te", "trailers")
	h2Req.Header.Set("Authorization", "Bearer "+accessToken)
	h2Req.Header.Set("X-Ghost-Mode", cursorGhostModeSetting())
	h2Req.Header.Set("X-Cursor-Client-Version", cursorClientVersionValue())
	h2Req.Header.Set("X-Cursor-Client-Type", "cli")

	resp, err := client.Do(h2Req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, data, nil
}

// extractCCH extracts the cch value from the system prompt's billing header.
func extractCCH(systemPrompt string) string {
	_, after, ok := strings.Cut(systemPrompt, "cch=")
	if !ok {
		return ""
	}
	rest := after
	end := strings.IndexAny(rest, "; \n")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// extractClaudeCodeSessionId extracts session_id from Claude Code's metadata.user_id JSON.
// Format: {"metadata":{"user_id":"{\"session_id\":\"xxx\",\"device_id\":\"yyy\"}"}}
func extractClaudeCodeSessionId(payload []byte) string {
	userIdStr := gjson.GetBytes(payload, "metadata.user_id").String()
	if userIdStr == "" {
		return ""
	}
	// user_id is a JSON string that needs to be parsed again
	sid := gjson.Get(userIdStr, "session_id").String()
	return sid
}

// deriveConversationId generates a deterministic conversation_id.
// Priority: session_id (stable across resume) > system prompt hash (fallback).
func deriveConversationId(apiKey, sessionId, systemPrompt string) string {
	var input string
	if sessionId != "" {
		// Best: use Claude Code's session_id — stable even across resume
		input = "cursor-conv:" + apiKey + ":" + sessionId
	} else {
		// Fallback: use system prompt content minus volatile cch
		stable := systemPrompt
		if idx := strings.Index(stable, "cch="); idx >= 0 {
			end := strings.IndexAny(stable[idx:], "; \n")
			if end > 0 {
				stable = stable[:idx] + stable[idx+end:]
			}
		}
		if len(stable) > 500 {
			stable = stable[:500]
		}
		input = "cursor-conv:" + apiKey + ":" + stable
	}
	h := sha256.Sum256([]byte(input))
	s := hex.EncodeToString(h[:16])
	return fmt.Sprintf("%s-%s-%s-%s-%s", s[:8], s[8:12], s[12:16], s[16:20], s[20:32])
}

func deriveSessionKey(clientKey string, model string, messages []gjson.Result) string {
	var firstUserContent string
	var systemContent string
	for _, msg := range messages {
		role := msg.Get("role").String()
		if role == "user" && firstUserContent == "" {
			firstUserContent = extractTextContent(msg.Get("content"))
		} else if role == "system" && systemContent == "" {
			// System prompt differs per Claude Code session (contains cwd, session_id, etc.)
			content := extractTextContent(msg.Get("content"))
			if len(content) > 200 {
				systemContent = content[:200]
			} else {
				systemContent = content
			}
		}
	}
	// Include client API key + system prompt hash to prevent session collisions:
	// - Different users have different API keys
	// - Different Claude Code sessions have different system prompts (cwd, tools, etc.)
	input := clientKey + ":" + model + ":" + systemContent + ":" + firstUserContent
	if len(input) > 500 {
		input = input[:500]
	}
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])[:16]
}

func sseChunk(id string, created int64, model string, delta string, finishReason string) cliproxyexecutor.StreamChunk {
	fr := "null"
	if finishReason != "" {
		fr = finishReason
	}
	// Note: the framework's WriteChunk adds "data: " prefix and "\n\n" suffix,
	// so we only output the raw JSON here.
	data := fmt.Sprintf(`{"id":"%s","object":"chat.completion.chunk","created":%d,"model":%s,"choices":[{"index":0,"delta":%s,"finish_reason":%s}]}`,
		id, created, jsonString(model), delta, fr)
	return cliproxyexecutor.StreamChunk{
		Payload: []byte(data),
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func decodeMcpArgsToJSON(args map[string][]byte) string {
	if len(args) == 0 {
		return "{}"
	}
	result := make(map[string]any)
	for k, v := range args {
		// Try protobuf Value decoding first (matches TS: toJson(ValueSchema, fromBinary(ValueSchema, value)))
		if decoded, err := cursorproto.ProtobufValueBytesToJSON(v); err == nil {
			result[k] = decoded
		} else {
			// Fallback: try raw JSON
			var jsonVal any
			if err := json.Unmarshal(v, &jsonVal); err == nil {
				result[k] = jsonVal
			} else {
				result[k] = string(v)
			}
		}
	}
	b, _ := json.Marshal(result)
	return string(b)
}

// --- Model Discovery ---

// FetchCursorModels retrieves available models from Cursor's API.
// The account's usable ids (GetUsableModels, including special ids like
// "default") stay the base catalog; the richer AvailableModels catalog
// contributes real context limits, thinking/effort parameter definitions and
// any additional picker ids, so nothing that used to be listed disappears.
func FetchCursorModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	accessToken := cursorAccessToken(auth)
	if accessToken == "" {
		return GetCursorFallbackModels()
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	client := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0)

	base := fetchUsableModelsCatalog(ctx, client, accessToken)
	rich := fetchAvailableModelsCatalog(ctx, client, accessToken)

	var merged []*registry.ModelInfo
	switch {
	case len(base) == 0 && len(rich) == 0:
		return GetCursorFallbackModels()
	case len(base) == 0:
		merged = rich
	case len(rich) == 0:
		merged = base
	default:
		logCursorCatalogDiff(base, rich)
		merged = mergeCursorModels(base, rich)
	}
	// Expose only clean ids: variant expansions stay routable via request-time
	// decomposition but are hidden from the model list.
	merged = cursorCleanCatalogFilter(merged)
	if len(merged) == 0 {
		return GetCursorFallbackModels()
	}
	return merged
}

// logCursorCatalogDiff dumps the id-level difference between the agent usable
// list and the picker catalog, so a deploy can settle which side actually
// carries the extra ids for this account.
func logCursorCatalogDiff(base, rich []*registry.ModelInfo) {
	if !log.IsLevelEnabled(log.DebugLevel) {
		return
	}
	idOf := func(models []*registry.ModelInfo) map[string]bool {
		ids := make(map[string]bool, len(models))
		for _, m := range models {
			if m != nil && m.ID != "" {
				ids[m.ID] = true
			}
		}
		return ids
	}
	baseIds, richIds := idOf(base), idOf(rich)
	var onlyUsable, onlyPicker []string
	for id := range baseIds {
		if !richIds[id] {
			onlyUsable = append(onlyUsable, id)
		}
	}
	for id := range richIds {
		if !baseIds[id] {
			onlyPicker = append(onlyPicker, id)
		}
	}
	sort.Strings(onlyUsable)
	sort.Strings(onlyPicker)
	log.Debugf("cursor: catalog diff: usable=%d picker=%d only-in-usable=%v only-in-picker=%v",
		len(baseIds), len(richIds), onlyUsable, onlyPicker)
}

// fetchUsableModelsCatalog queries agent.v1.AgentService/GetUsableModels.
func fetchUsableModelsCatalog(ctx context.Context, client *http.Client, accessToken string) []*registry.ModelInfo {
	status, body, err := doCursorUnary(ctx, client, cursorAPIURL+cursorModelsPath, accessToken, nil, "application/proto")
	if err != nil {
		log.Debugf("cursor: models request failed: %v", err)
		return nil
	}
	if status < 200 || status >= 300 {
		log.Debugf("cursor: models request returned status %d", status)
		return nil
	}
	return parseModelsResponse(body)
}

// fetchAvailableModelsCatalog queries aiserver.v1.AiService/AvailableModels.
func fetchAvailableModelsCatalog(ctx context.Context, client *http.Client, accessToken string) []*registry.ModelInfo {
	status, body, err := doCursorUnary(ctx, client, cursorAPIURL+cursorAvailableModelsPath, accessToken, availableModelsRequestBody(), "application/proto")
	if err != nil {
		log.Debugf("cursor: AvailableModels request failed: %v", err)
		return nil
	}
	if status < 200 || status >= 300 {
		log.Debugf("cursor: AvailableModels request returned status %d", status)
		return nil
	}
	return parseAvailableModelsResponse(body)
}

// mergeCursorModels keeps every base entry and enriches it with the picker
// catalog's metadata; ids only present in the picker catalog are appended.
func mergeCursorModels(base, rich []*registry.ModelInfo) []*registry.ModelInfo {
	merged := make([]*registry.ModelInfo, 0, len(base)+len(rich))
	byId := make(map[string]*registry.ModelInfo, len(base)+len(rich))
	for _, info := range base {
		if info == nil || info.ID == "" {
			continue
		}
		if _, exists := byId[info.ID]; exists {
			continue
		}
		byId[info.ID] = info
		merged = append(merged, info)
	}
	for _, info := range rich {
		if info == nil || info.ID == "" {
			continue
		}
		if existing, exists := byId[info.ID]; exists {
			// Enrich the usable-list entry with picker metadata.
			if info.ContextLength > 0 {
				existing.ContextLength = info.ContextLength
			}
			if info.Thinking != nil {
				existing.Thinking = info.Thinking
			}
			continue
		}
		byId[info.ID] = info
		merged = append(merged, info)
	}
	return merged
}

const cursorAvailableModelsPath = "/aiserver.v1.AiService/AvailableModels"

// availableModelsRequestBody builds AvailableModelsRequest:
// 2 include_long_context_models=true, 5 use_model_parameters=true,
// 7 do_not_use_markdown=true.
func availableModelsRequestBody() []byte {
	return []byte{0x10, 0x01, 0x28, 0x01, 0x38, 0x01}
}

// parseAvailableModelsResponse walks AvailableModelsResponse{2: repeated AvailableModel}.
func parseAvailableModelsResponse(data []byte) []*registry.ModelInfo {
	// Strip Connect framing when the unary response arrives framed.
	if _, payload, _, ok := cursorproto.ParseConnectFrame(data); ok {
		data = payload
	}
	var models []*registry.ModelInfo
	for len(data) > 0 {
		num, typ, n := consumeTag(data)
		if n < 0 {
			break
		}
		data = data[n:]
		if typ != 2 { // BytesType
			n = consumeFieldValue(num, typ, data)
			if n < 0 {
				break
			}
			data = data[n:]
			continue
		}
		val, n := consumeBytes(data)
		if n < 0 {
			break
		}
		data = data[n:]
		if num != 2 {
			continue
		}
		if info := parseAvailableModelEntry(val); info != nil {
			models = append(models, info)
		}
	}
	return models
}

// parseAvailableModelEntry decodes one AvailableModel:
// 1 name, 9 supports_thinking, 15 context_token_limit, 17 client_display_name,
// 29 parameter_definitions, 35 is_hidden. ParameterDefinition: 1 id,
// 4 values (bool_options / enum_options groups of option{1: value}).
func parseAvailableModelEntry(data []byte) *registry.ModelInfo {
	var name, displayName string
	var context int64
	var supportsThinking bool
	var options map[string][]string

	for len(data) > 0 {
		num, typ, n := consumeTag(data)
		if n < 0 {
			return nil
		}
		data = data[n:]
		switch typ {
		case 0: // VarintType
			val, n := consumeVarint(data)
			if n < 0 {
				return nil
			}
			data = data[n:]
			switch num {
			case 9:
				supportsThinking = val != 0
			case 15:
				context = int64(val)
			}
		case 2: // BytesType
			val, n := consumeBytes(data)
			if n < 0 {
				return nil
			}
			data = data[n:]
			switch num {
			case 1:
				name = string(val)
			case 17:
				displayName = string(val)
			case 29:
				if options == nil {
					options = make(map[string][]string)
				}
				mergeParamDefinition(options, val)
			}
		default:
			n := consumeFieldValue(num, typ, data)
			if n < 0 {
				return nil
			}
			data = data[n:]
		}
	}

	// Entries without a routable id carry no identity of their own. Note:
	// "default" is a valid account-level id and must survive here; the merge
	// dedupes it against the usable-list entry.
	if name == "" {
		return nil
	}
	if displayName == "" {
		displayName = name
	}
	if context <= 0 {
		context = 200000
	}

	info := &registry.ModelInfo{
		ID:                  name,
		Object:              "model",
		Created:             time.Now().Unix(),
		OwnedBy:             "cursor",
		Type:                cursorAuthType,
		DisplayName:         displayName,
		ContextLength:       int(context),
		MaxCompletionTokens: 64000,
	}
	// Expose only the thinking capability the model declares, so the shared
	// thinking pipeline validates requests against real parameters.
	thinkingValues := options["thinking"]
	effortValues := options["effort"]
	if len(effortValues) == 0 {
		effortValues = options["reasoning"]
	}
	if supportsThinking || len(thinkingValues) > 0 || len(effortValues) > 0 {
		info.Thinking = &registry.ThinkingSupport{Max: 50000, DynamicAllowed: true}
		if slices.Contains(thinkingValues, "false") {
			info.Thinking.ZeroAllowed = true
		}
		info.Thinking.Levels = effortValues
	}
	if len(options) > 0 {
		cursorSetParamOptions(name, options)
	}
	return info
}

// mergeParamDefinition decodes one ParameterDefinition into the options table:
// 1 id, 4 values — ParameterValues with bool_options(1)/enum_options(2)
// groups, each holding repeated option{1: value}.
func mergeParamDefinition(options map[string][]string, data []byte) {
	var id string
	var values []string
	for len(data) > 0 {
		num, typ, n := consumeTag(data)
		if n < 0 {
			return
		}
		data = data[n:]
		if typ != 2 {
			n = consumeFieldValue(num, typ, data)
			if n < 0 {
				return
			}
			data = data[n:]
			continue
		}
		val, n := consumeBytes(data)
		if n < 0 {
			return
		}
		data = data[n:]
		switch num {
		case 1:
			id = string(val)
		case 4:
			values = appendParamValues(values, val)
		}
	}
	if id != "" && len(values) > 0 {
		options[id] = append(options[id], values...)
	}
}

func appendParamValues(dst []string, blob []byte) []string {
	for len(blob) > 0 {
		num, typ, n := consumeTag(blob)
		if n < 0 {
			return dst
		}
		blob = blob[n:]
		if typ != 2 {
			n = consumeFieldValue(num, typ, blob)
			if n < 0 {
				return dst
			}
			blob = blob[n:]
			continue
		}
		group, n := consumeBytes(blob)
		if n < 0 {
			return dst
		}
		blob = blob[n:]
		if num != 1 && num != 2 {
			continue
		}
		// Each group holds repeated option submessages whose field 1 is the value.
		for len(group) > 0 {
			gnum, gtyp, gn := consumeTag(group)
			if gn < 0 {
				return dst
			}
			group = group[gn:]
			if gtyp != 2 {
				gn = consumeFieldValue(gnum, gtyp, group)
				if gn < 0 {
					return dst
				}
				group = group[gn:]
				continue
			}
			opt, gn := consumeBytes(group)
			if gn < 0 {
				return dst
			}
			group = group[gn:]
			if v := firstStringField(opt, 1); v != "" {
				dst = append(dst, v)
			}
		}
	}
	return dst
}

// firstStringField returns the first string-valued occurrence of the target
// field number in a protobuf submessage.
func firstStringField(data []byte, target int) string {
	for len(data) > 0 {
		num, typ, n := consumeTag(data)
		if n < 0 {
			return ""
		}
		data = data[n:]
		if typ != 2 {
			n = consumeFieldValue(num, typ, data)
			if n < 0 {
				return ""
			}
			data = data[n:]
			continue
		}
		val, n := consumeBytes(data)
		if n < 0 {
			return ""
		}
		data = data[n:]
		if num == target {
			return string(val)
		}
	}
	return ""
}

func parseModelsResponse(data []byte) []*registry.ModelInfo {
	// Try stripping Connect framing first
	if len(data) >= cursorproto.ConnectFrameHeaderSize {
		_, payload, _, ok := cursorproto.ParseConnectFrame(data)
		if ok {
			data = payload
		}
	}

	// The response is a GetUsableModelsResponse protobuf.
	// We need to decode it manually - it contains a repeated "models" field.
	// Based on the TS code, the response has a `models` field (repeated) containing
	// model objects with modelId, displayName, thinkingDetails, etc.

	// For now, we'll try a simple decode approach
	var models []*registry.ModelInfo
	// Field 1 is likely "models" (repeated submessage)
	for len(data) > 0 {
		num, typ, n := consumeTag(data)
		if n < 0 {
			break
		}
		data = data[n:]

		if typ == 2 { // BytesType (submessage)
			val, n := consumeBytes(data)
			if n < 0 {
				break
			}
			data = data[n:]

			if num == 1 { // models field
				if m := parseModelEntry(val); m != nil {
					models = append(models, m)
				}
			}
		} else {
			n := consumeFieldValue(num, typ, data)
			if n < 0 {
				break
			}
			data = data[n:]
		}
	}

	return models
}

func parseModelEntry(data []byte) *registry.ModelInfo {
	var modelId, displayName string
	var hasThinking bool

	for len(data) > 0 {
		num, typ, n := consumeTag(data)
		if n < 0 {
			break
		}
		data = data[n:]

		switch typ {
		case 2: // BytesType
			val, n := consumeBytes(data)
			if n < 0 {
				return nil
			}
			data = data[n:]
			switch num {
			case 1: // modelId
				modelId = string(val)
			case 2: // thinkingDetails
				hasThinking = true
			case 3: // displayModelId (use as fallback)
				if displayName == "" {
					displayName = string(val)
				}
			case 4: // displayName
				displayName = string(val)
			case 5: // displayNameShort
				if displayName == "" {
					displayName = string(val)
				}
			}
		case 0: // VarintType
			_, n := consumeVarint(data)
			if n < 0 {
				return nil
			}
			data = data[n:]
		default:
			n := consumeFieldValue(num, typ, data)
			if n < 0 {
				return nil
			}
			data = data[n:]
		}
	}

	if modelId == "" {
		return nil
	}
	if displayName == "" {
		displayName = modelId
	}

	info := &registry.ModelInfo{
		ID:                  modelId,
		Object:              "model",
		Created:             time.Now().Unix(),
		OwnedBy:             "cursor",
		Type:                cursorAuthType,
		DisplayName:         displayName,
		ContextLength:       200000,
		MaxCompletionTokens: 64000,
	}
	if hasThinking {
		info.Thinking = &registry.ThinkingSupport{
			Max:            50000,
			DynamicAllowed: true,
		}
	}
	return info
}

// GetCursorFallbackModels returns hardcoded fallback models.
func GetCursorFallbackModels() []*registry.ModelInfo {
	return []*registry.ModelInfo{
		{ID: "composer-2", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Composer 2", ContextLength: 200000, MaxCompletionTokens: 64000, Thinking: &registry.ThinkingSupport{Max: 50000, DynamicAllowed: true}},
		{ID: "claude-4-sonnet", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Claude 4 Sonnet", ContextLength: 200000, MaxCompletionTokens: 64000, Thinking: &registry.ThinkingSupport{Max: 50000, DynamicAllowed: true}},
		{ID: "claude-3.5-sonnet", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Claude 3.5 Sonnet", ContextLength: 200000, MaxCompletionTokens: 8192},
		{ID: "gpt-4o", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "GPT-4o", ContextLength: 128000, MaxCompletionTokens: 16384},
		{ID: "cursor-small", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Cursor Small", ContextLength: 200000, MaxCompletionTokens: 64000},
		{ID: "gemini-2.5-pro", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Gemini 2.5 Pro", ContextLength: 1000000, MaxCompletionTokens: 65536, Thinking: &registry.ThinkingSupport{Max: 50000, DynamicAllowed: true}},
	}
}

// Low-level protowire helpers (avoid importing protowire in executor)
func consumeTag(b []byte) (num int, typ int, n int) {
	v, n := consumeVarint(b)
	if n < 0 {
		return 0, 0, -1
	}
	return int(v >> 3), int(v & 7), n
}

func consumeVarint(b []byte) (uint64, int) {
	var val uint64
	for i := 0; i < len(b) && i < 10; i++ {
		val |= uint64(b[i]&0x7f) << (7 * i)
		if b[i]&0x80 == 0 {
			return val, i + 1
		}
	}
	return 0, -1
}

func consumeBytes(b []byte) ([]byte, int) {
	length, n := consumeVarint(b)
	if n < 0 || int(length) > len(b)-n {
		return nil, -1
	}
	return b[n : n+int(length)], n + int(length)
}

func consumeFieldValue(num, typ int, b []byte) int {
	switch typ {
	case 0: // Varint
		_, n := consumeVarint(b)
		return n
	case 1: // 64-bit
		if len(b) < 8 {
			return -1
		}
		return 8
	case 2: // Length-delimited
		_, n := consumeBytes(b)
		return n
	case 5: // 32-bit
		if len(b) < 4 {
			return -1
		}
		return 4
	default:
		return -1
	}
}
