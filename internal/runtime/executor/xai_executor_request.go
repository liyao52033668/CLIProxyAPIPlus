package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
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

type xaiPreparedRequest struct {
	baseModel             string
	from                  sdktranslator.Format
	responseFormat        sdktranslator.Format
	to                    sdktranslator.Format
	originalPayload       []byte
	body                  []byte
	namespaceTools        map[string]xaiNamespaceToolRef
	clientDeclaredTools   map[xaiClientToolKey]struct{}
	sessionID             string
	replayScope           xaiReasoningReplayScope
	filterInternalXSearch bool
	webSearchAlias        string
}

type xaiNamespaceToolRef struct {
	namespace string
	name      string
}

// xaiClientToolKey identifies a client-declared callable tool using the
// post-restore Responses shape (short name + optional namespace) and the
// effective upstream tool type after normalizeXAITool (client custom tools are
// sent as function). Response call types are matched against this effective
// kind so internal custom_tool_call traces are not exempted merely because a
// client declared an ordinary function/custom tool with the same short name,
// while legitimate function_call responses for normalized custom tools are kept.
type xaiClientToolKey struct {
	namespace string
	name      string
	toolType  string
}

func (e *XAIExecutor) prepareResponsesRequest(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (*xaiPreparedRequest, error) {
	return e.prepareResponsesRequestTo(ctx, req, opts, stream, sdktranslator.FormatCodex)
}

func (e *XAIExecutor) prepareResponsesRequestTo(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool, to sdktranslator.Format) (*xaiPreparedRequest, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	from := opts.SourceFormat
	responseFormat := opts.SourceFormat
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := bytes.Clone(originalPayloadSource)
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, stream)
	body := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), stream)

	var err error
	body, err = thinking.ApplyThinking(body, req.Model, from.String(), e.Identifier(), e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequestForExecutor(e.cfg, e.Identifier(), baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body, _ = sjson.SetBytes(body, "model", baseModel)
	body, _ = sjson.SetBytes(body, "stream", stream)
	hasPreviousResponseID := strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String()) != ""
	body, _ = sjson.DeleteBytes(body, "previous_response_id")
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "safety_identifier")
	body, _ = sjson.DeleteBytes(body, "stream_options")
	namespaceTools := collectXAINamespaceToolRefs(body)
	// Collect before normalizeXAITools flattens namespace wrappers so keys match
	// the post-restore (namespace, short-name) shape used by the response filter.
	clientDeclaredTools := collectXAIClientDeclaredToolKeys(body)
	body = normalizeXAITools(body)
	// Drop choices that point at tools removed by normalizeXAITools before any
	// configured x_search injection, so no surviving choice references a deleted tool.
	body = normalizeXAINamespaceToolChoice(body)
	body = normalizeXAIForcedWebSearchToolChoice(body)
	body = pruneXAIOrphanedToolChoice(body)
	body = normalizeXAIForcedImageGenerationToolChoice(body)
	body = normalizeXAIToolChoiceForTools(body)
	var webSearchAlias string
	if xaiHasClientWebSearchFunction(body, namespaceTools) {
		webSearchAlias = xaiResolveClientWebSearchAlias(body)
		body = aliasXAIClientWebSearchFunction(body, webSearchAlias, namespaceTools)
	}
	if e.cfg != nil && e.cfg.XAI.InjectXSearch && !xaiToolChoiceRequiresImageGenerationOnly(body) {
		body = ensureXAINativeXSearchTool(body)
	}
	var replayScope xaiReasoningReplayScope
	body, replayScope, err = applyXAIReasoningReplayCacheRequired(ctx, from, req, opts, body)
	if err != nil {
		return nil, err
	}
	body = normalizeXAIInputCustomToolCalls(body)
	body = normalizeXAIInputNamespaceToolCalls(body)
	body = normalizeXAIInputReasoningItems(body)
	body = sanitizeXAIInputEncryptedContent(body)
	body = normalizeCodexInstructions(body)
	// stop is supported by Chat Completions but not by xAI's Responses API.
	// Thinking was handled before payload overrides and must not be revalidated here.
	body, _ = sjson.DeleteBytes(body, "stop")
	body = normalizeXAIImageRefs(body)
	if errValidate := validateXAIToolOutputsHavePriorCalls(body, hasPreviousResponseID); errValidate != nil {
		return nil, errValidate
	}

	sessionID, errSession := xaiResolveComposerSessionID(ctx, req, opts, baseModel)
	if errSession != nil {
		return nil, errSession
	}
	if sessionID != "" {
		body, _ = sjson.SetBytes(body, "prompt_cache_key", sessionID)
	}

	return &xaiPreparedRequest{
		baseModel:             baseModel,
		from:                  from,
		responseFormat:        responseFormat,
		to:                    to,
		originalPayload:       originalPayload,
		body:                  body,
		namespaceTools:        namespaceTools,
		clientDeclaredTools:   clientDeclaredTools,
		sessionID:             sessionID,
		replayScope:           replayScope,
		filterInternalXSearch: xaiRequestHasNativeXSearch(body),
		webSearchAlias:        webSearchAlias,
	}, nil
}

func (e *XAIExecutor) recordXAIRequest(ctx context.Context, auth *cliproxyauth.Auth, url string, headers http.Header, body []byte) {
	helps.RecordUpstreamRequest(ctx, e.cfg, auth, e.Identifier(), http.MethodPost, url, headers, body)
}

func xaiCreds(auth *cliproxyauth.Auth) (token, baseURL string) {
	return helps.ResolveAPIKeyAndBaseURL(auth)
}

// xaiUsingAPI reports whether this xAI auth should use the official API path
// for non-media HTTP chat. OAuth defaults to false to use Grok Build.
func xaiUsingAPI(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return true
	}
	if len(auth.Attributes) > 0 {
		if raw := strings.TrimSpace(auth.Attributes[xaiUsingAPIAttr]); raw != "" {
			parsed, errParse := strconv.ParseBool(raw)
			if errParse == nil {
				return parsed
			}
		}
	}
	if len(auth.Metadata) > 0 {
		raw, ok := auth.Metadata[xaiUsingAPIAttr]
		if ok && raw != nil {
			switch v := raw.(type) {
			case bool:
				return v
			case string:
				parsed, errParse := strconv.ParseBool(strings.TrimSpace(v))
				if errParse == nil {
					return parsed
				}
			default:
			}
		}
	}
	if raw := strings.TrimSpace(auth.Attributes["auth_kind"]); raw != "" {
		return !strings.EqualFold(raw, "oauth")
	}
	return !strings.EqualFold(xaiMetadataString(auth.Metadata, "auth_kind"), "oauth")
}

// xaiChatBaseURL returns the base URL for non-image/video xAI HTTP chat requests.
// When auth using_api is true, the official API base URL logic is used. When it
// is false (including its OAuth default), empty or official default base_url is
// rewritten to the CLI chat-proxy endpoint; an explicit non-default base_url is
// still honored.
// Websocket and compact transports intentionally do not use this helper:
// cli-chat-proxy only accepts HTTP POST chat and does not implement
// /responses/compact (404) or websocket upgrades (405).
func xaiChatBaseURL(auth *cliproxyauth.Auth) string {
	_, baseURL := xaiCreds(auth)
	if xaiUsingAPI(auth) {
		if baseURL == "" {
			return xaiauth.DefaultAPIBaseURL
		}
		return baseURL
	}
	if baseURL != "" && !xaiIsDefaultAPIBaseURL(baseURL) {
		return baseURL
	}
	return xaiauth.CLIChatProxyBaseURL
}

func xaiNormalizeBaseURL(baseURL string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/")
}

func xaiIsDefaultAPIBaseURL(baseURL string) bool {
	return xaiNormalizeBaseURL(baseURL) == xaiNormalizeBaseURL(xaiauth.DefaultAPIBaseURL)
}

func xaiIsCLIChatProxyBaseURL(baseURL string) bool {
	return xaiNormalizeBaseURL(baseURL) == xaiNormalizeBaseURL(xaiauth.CLIChatProxyBaseURL)
}

// xaiOfficialOrExplicitBaseURL returns a base URL suitable for websocket/media.
// CLI chat-proxy only accepts HTTP chat POST, so empty/default/chat-proxy values
// fall back to the official API while still honoring custom explicit base URLs.
func xaiOfficialOrExplicitBaseURL(auth *cliproxyauth.Auth) string {
	_, baseURL := xaiCreds(auth)
	if baseURL == "" || xaiIsDefaultAPIBaseURL(baseURL) || xaiIsCLIChatProxyBaseURL(baseURL) {
		return xaiauth.DefaultAPIBaseURL
	}
	return baseURL
}

// xaiCompactBaseURL returns the base URL for xAI /responses/compact requests.
// Compact must stay on the official API (or an explicit non-CLI-proxy base_url).
// Reusing xaiChatBaseURL would pin OAuth traffic to cli-chat-proxy, which returns
// 404 for /responses/compact and then cools down the auth pool as not_found.
// Local Plus also rewrites the official default base via xaiOfficialOrExplicitBaseURL.
func xaiCompactBaseURL(auth *cliproxyauth.Auth) string {
	return xaiOfficialOrExplicitBaseURL(auth)
}

// xaiBaseURLSource classifies a resolved xAI base URL for logging.
func xaiBaseURLSource(baseURL string) string {
	switch {
	case xaiIsDefaultAPIBaseURL(baseURL):
		return "DefaultAPIBaseURL"
	case xaiIsCLIChatProxyBaseURL(baseURL):
		return "CLIChatProxyBaseURL"
	default:
		return "custom"
	}
}

// logXAIResolvedBaseURL emits a console log for the resolved upstream base URL.
func logXAIResolvedBaseURL(ctx context.Context, baseURL string) {
	helps.LogWithRequestID(ctx).Infof("xai: using base_url=%s source=%s", baseURL, xaiBaseURLSource(baseURL))
}

func applyXAIHeaders(r *http.Request, auth *cliproxyauth.Auth, token string, stream bool, sessionID string) {
	applyXAIDefaultHeaders(r, token, stream, sessionID)
	applyXAICustomHeaders(r, auth)
}

func applyXAIDefaultHeaders(r *http.Request, token string, stream bool, sessionID string) {
	r.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(token) != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if stream {
		r.Header.Set("Accept", "text/event-stream")
	} else {
		r.Header.Set("Accept", "application/json")
	}
	r.Header.Set("Connection", "Keep-Alive")
	if sessionID != "" {
		r.Header.Set("x-grok-conv-id", sessionID)
	}
}

func applyXAICustomHeaders(r *http.Request, auth *cliproxyauth.Auth) {
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(r, attrs)
}

// applyXAIChatProxyIdentityHeaders attaches the fixed Grok CLI fingerprint
// expected by the official CLI chat-proxy endpoint.
func applyXAIChatProxyIdentityHeaders(r *http.Request) {
	if r == nil {
		return
	}
	r.Header.Set(xaiUserAgentHeader, xaiUserAgentValue)
	r.Header.Set(xaiAuthResponseHeader, xaiAuthResponseValue)
	r.Header.Set(xaiClientIdentifierHeader, xaiClientIdentifierValue)
	r.Header.Set(xaiTokenAuthHeader, xaiTokenAuthValue)
	r.Header.Set(xaiClientVersionHeader, xaiClientVersionValue)
}

// applyXAIChatHeaders applies standard xAI headers for non-image/video chat
// requests. When using_api is true, this matches the standard
// applyXAIHeaders behavior. CLI chat-proxy identity headers are only attached
// when using_api is false and the resolved chat base URL is the official CLI
// chat-proxy endpoint.
func applyXAIChatHeaders(r *http.Request, auth *cliproxyauth.Auth, token string, stream bool, sessionID string) {
	if xaiUsingAPI(auth) {
		applyXAIHeaders(r, auth, token, stream, sessionID)
		return
	}
	applyXAIDefaultHeaders(r, token, stream, sessionID)
	if xaiIsCLIChatProxyBaseURL(xaiChatBaseURL(auth)) {
		applyXAIChatProxyIdentityHeaders(r)
	}
	applyXAICustomHeaders(r, auth)
}

func xaiResolveComposerSessionID(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, baseModel string) (string, error) {
	if sessionID := xaiExecutionSessionID(req, opts); sessionID != "" {
		return sessionID, nil
	}
	// Prefer stable Claude Code session identifiers for prompt/conversation cache keys.
	if opts.SourceFormat == sdktranslator.FromString("claude") || opts.SourceFormat.String() == "claude" {
		if key := codexReasoningReplaySessionKey(ctx, opts.SourceFormat, req, opts, req.Payload); key != "" {
			if strings.HasPrefix(key, "claude:") {
				return strings.TrimPrefix(key, "claude:"), nil
			}
			return key, nil
		}
		userID := strings.TrimSpace(gjson.GetBytes(req.Payload, "metadata.user_id").String())
		if strings.HasPrefix(userID, "{") {
			if sessionID := strings.TrimSpace(gjson.Get(userID, "session_id").String()); sessionID != "" {
				return sessionID, nil
			}
		}
	}
	if !xaiRequiresIsolatedConversation(baseModel) {
		return "", nil
	}
	return uuid.NewString(), nil
}

func xaiExecutionSessionID(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	if value := xaiMetadataString(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); value != "" {
		return value
	}
	if value := xaiMetadataString(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); value != "" {
		return value
	}
	if promptCacheKey := gjson.GetBytes(req.Payload, "prompt_cache_key"); promptCacheKey.Exists() {
		return strings.TrimSpace(promptCacheKey.String())
	}
	return ""
}

func xaiRequiresIsolatedConversation(model string) bool {
	name := strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).ModelName))
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	if xaiComposerModelPrefix != "" && strings.HasPrefix(name, xaiComposerModelPrefix) {
		return true
	}
	return strings.Contains(name, "composer")
}

func xaiImageEndpointPath(opts cliproxyexecutor.Options) string {
	if opts.SourceFormat.String() != xaiImageHandlerType {
		return ""
	}

	path := xaiMetadataString(opts.Metadata, cliproxyexecutor.RequestPathMetadataKey)
	if strings.HasSuffix(path, "/images/edits") {
		return xaiImagesEditsPath
	}
	if strings.HasSuffix(path, "/images/generations") {
		return xaiImagesGenerationsPath
	}
	return xaiDefaultImageEndpointPath
}

// normalizeXAIImageRefs rewrites OpenAI-style image object fields to the xAI
// image API shape before the payload is sent upstream:
//
//	{"image":{"image_url":"https://..."}} → {"image":{"url":"https://..."}}
//
// Applies to image / images / reference_images anywhere in the JSON tree,
// including nested objects and array items. Does not rewrite chat content
// parts shaped as {"type":"image_url","image_url":{...}}.
// Uses encoding/json (not gjson path strings) so keys containing dots, backslashes,
// or empty strings are handled correctly.
func normalizeXAIImageRefs(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload any
	if errDecode := decoder.Decode(&payload); errDecode != nil {
		return body
	}

	if !normalizeXAIImageRefsValue(payload) {
		return body
	}
	normalized, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return body
	}
	return normalized
}

func normalizeXAIImageRefsValue(value any) bool {
	changed := false
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			switch key {
			case "image":
				changed = normalizeXAIImageRef(child) || changed
			case "images", "reference_images":
				if refs, ok := child.([]any); ok {
					for _, ref := range refs {
						changed = normalizeXAIImageRef(ref) || changed
					}
				}
			}
			changed = normalizeXAIImageRefsValue(child) || changed
		}
	case []any:
		for _, child := range node {
			changed = normalizeXAIImageRefsValue(child) || changed
		}
	}
	return changed
}

func normalizeXAIImageRef(value any) bool {
	ref, ok := value.(map[string]any)
	if !ok {
		return false
	}

	originalURL, _ := ref["url"].(string)
	url := strings.TrimSpace(originalURL)
	imageURL, hasImageURL := ref["image_url"]
	if url == "" {
		switch imageURL := imageURL.(type) {
		case string:
			url = strings.TrimSpace(imageURL)
		case map[string]any:
			url, _ = imageURL["url"].(string)
			url = strings.TrimSpace(url)
		}
	}
	if url == "" {
		return false
	}
	if url == originalURL && !hasImageURL {
		return false
	}

	// Always emit the xAI field name and drop the OpenAI alias.
	ref["url"] = url
	delete(ref, "image_url")
	return true
}

func xaiIsVideoRequest(opts cliproxyexecutor.Options) bool {
	return opts.SourceFormat.String() == xaiVideoHandlerType
}

func xaiVideoEndpointPath(opts cliproxyexecutor.Options) string {
	if !xaiIsVideoRequest(opts) {
		return ""
	}
	path := xaiMetadataString(opts.Metadata, cliproxyexecutor.RequestPathMetadataKey)
	if strings.HasSuffix(path, "/videos/edits") {
		return xaiVideosEditsPath
	}
	if strings.HasSuffix(path, "/videos/extensions") {
		return xaiVideosExtensionsPath
	}
	if strings.HasSuffix(path, "/videos/generations") {
		return xaiVideosGenerationsPath
	}
	return ""
}

func xaiMetadataString(meta map[string]any, key string) string {
	return helps.MetadataString(meta, key)
}

// ensureXAINativeXSearchTool appends {"type":"x_search"} when the final tools
// list does not already include native X Search. When tool_choice restricts the
// model to allowed_tools, x_search is also added there (without duplicates) so
// Grok can select the injected tool. When injection is enabled, HTTP and websocket
// executors both prepare payloads through prepareResponsesRequestTo, so this runs
// once before the body is submitted upstream.
func ensureXAINativeXSearchTool(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	if !xaiRequestHasNativeXSearch(body) {
		tools := gjson.GetBytes(body, "tools")
		if !tools.Exists() || !tools.IsArray() {
			body, _ = sjson.SetRawBytes(body, "tools", []byte(`[{"type":"x_search"}]`))
		} else {
			body, _ = sjson.SetRawBytes(body, "tools.-1", xaiXSearchToolJSON)
		}
	}
	return ensureXAINativeXSearchAllowedTools(body)
}

// ensureXAINativeXSearchAllowedTools appends x_search to tool_choice.tools when
// the choice mode is allowed_tools and x_search is not already listed.
func ensureXAINativeXSearchAllowedTools(body []byte) []byte {
	choice := gjson.GetBytes(body, "tool_choice")
	if !choice.IsObject() || choice.Get("type").String() != "allowed_tools" {
		return body
	}
	allowed := choice.Get("tools")
	if !allowed.Exists() || !allowed.IsArray() {
		body, _ = sjson.SetRawBytes(body, "tool_choice.tools", []byte(`[{"type":"x_search"}]`))
		return body
	}
	for _, tool := range allowed.Array() {
		if strings.TrimSpace(tool.Get("type").String()) == xaiXSearchToolType {
			return body
		}
	}
	body, _ = sjson.SetRawBytes(body, "tool_choice.tools.-1", xaiXSearchToolJSON)
	return body
}

// normalizeXAIForcedWebSearchToolChoice rewrites Codex's hosted-tool choice
// into the allowed_tools form accepted by xAI's ModelToolChoice schema.
func normalizeXAIForcedWebSearchToolChoice(body []byte) []byte {
	choice := gjson.GetBytes(body, "tool_choice")
	if !choice.IsObject() || strings.TrimSpace(choice.Get("type").String()) != xaiWebSearchToolType {
		return body
	}

	allowedChoice := []byte(`{"type":"allowed_tools","mode":"required","tools":[]}`)
	allowedChoice, errSetAllowed := sjson.SetRawBytes(allowedChoice, "tools.-1", []byte(choice.Raw))
	if errSetAllowed != nil {
		return body
	}
	updated, errSetChoice := sjson.SetRawBytes(body, "tool_choice", allowedChoice)
	if errSetChoice != nil {
		return body
	}
	return updated
}

// normalizeXAIForcedImageGenerationToolChoice rewrites image_generation choices
// into a ModelToolChoice variant accepted by xAI chat-proxy. `{type: image_generation}`
// becomes the string "required" and the tools list is reduced to image_generation
// so later x_search injection cannot broaden the restriction. An allowed_tools
// list that only names that hosted tool becomes the original mode ("auto" or
// "required") and is likewise reduced to image_generation. Mixed lists drop the
// image_generation entry so the remaining hosted/function choices can still
// deserialize.
func normalizeXAIForcedImageGenerationToolChoice(body []byte) []byte {
	choice := gjson.GetBytes(body, "tool_choice")
	if !choice.IsObject() {
		return body
	}
	choiceType := strings.TrimSpace(choice.Get("type").String())
	if choiceType == xaiImageGenerationToolType {
		body = xaiKeepOnlyImageGenerationTools(body)
		return xaiSetToolChoiceString(body, "required")
	}
	if choiceType != "allowed_tools" {
		return body
	}
	allowed := choice.Get("tools")
	if !allowed.IsArray() {
		return body
	}
	filtered := make([][]byte, 0, len(allowed.Array()))
	stripped := false
	for _, tool := range allowed.Array() {
		if strings.TrimSpace(tool.Get("type").String()) == xaiImageGenerationToolType {
			stripped = true
			continue
		}
		filtered = append(filtered, []byte(tool.Raw))
	}
	if !stripped {
		return body
	}
	if len(filtered) == 0 {
		mode := strings.TrimSpace(choice.Get("mode").String())
		if mode != "auto" {
			mode = "required"
		}
		body = xaiKeepOnlyImageGenerationTools(body)
		return xaiSetToolChoiceString(body, mode)
	}
	updated, errSet := sjson.SetRawBytes(body, "tool_choice.tools", helps.JoinRawJSONArray(filtered))
	if errSet != nil {
		return body
	}
	return updated
}

func xaiSetToolChoiceString(body []byte, value string) []byte {
	updated, errSet := sjson.SetBytes(body, "tool_choice", value)
	if errSet != nil {
		return body
	}
	return updated
}

func xaiKeepOnlyImageGenerationTools(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body
	}
	kept := make([][]byte, 0, 1)
	for _, tool := range tools.Array() {
		if strings.TrimSpace(tool.Get("type").String()) == xaiImageGenerationToolType {
			kept = append(kept, []byte(tool.Raw))
		}
	}
	if len(kept) == 0 || len(kept) == len(tools.Array()) {
		return body
	}
	updated, errSet := sjson.SetRawBytes(body, "tools", helps.JoinRawJSONArray(kept))
	if errSet != nil {
		return body
	}
	return updated
}

func xaiToolChoiceRequiresImageGenerationOnly(body []byte) bool {
	choice := gjson.GetBytes(body, "tool_choice")
	if choice.Type != gjson.String {
		return false
	}
	switch choice.String() {
	case "required", "auto":
	default:
		return false
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() || len(tools.Array()) == 0 {
		return false
	}
	for _, tool := range tools.Array() {
		if strings.TrimSpace(tool.Get("type").String()) != xaiImageGenerationToolType {
			return false
		}
	}
	return true
}

// pruneXAIOrphanedToolChoice removes tool_choice entries that no longer match
// any remaining tool after normalizeXAITools filtering. Forced choices that
// reference a deleted tool are dropped entirely; allowed_tools lists keep only
// choices that still resolve against the post-normalization tools set.
func pruneXAIOrphanedToolChoice(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	choice := gjson.GetBytes(body, "tool_choice")
	if !choice.Exists() {
		return body
	}
	available := collectXAIAvailableToolChoiceKeys(body)
	if choice.Type == gjson.String {
		// auto / none / required are not tool references.
		return body
	}
	if !choice.IsObject() {
		return body
	}
	choiceType := strings.TrimSpace(choice.Get("type").String())
	switch choiceType {
	case "allowed_tools":
		return pruneXAIAllowedToolsChoice(body, available)
	default:
		if choiceType == "" {
			return body
		}
		if xaiToolChoiceMatchesAvailable(choice, available) {
			return body
		}
		body, _ = sjson.DeleteBytes(body, "tool_choice")
		return body
	}
}

func pruneXAIAllowedToolsChoice(body []byte, available map[xaiToolChoiceKey]struct{}) []byte {
	allowed := gjson.GetBytes(body, "tool_choice.tools")
	if !allowed.Exists() || !allowed.IsArray() {
		body, _ = sjson.DeleteBytes(body, "tool_choice")
		return body
	}
	filtered := []byte(`[]`)
	changed := false
	for _, tool := range allowed.Array() {
		if !xaiToolChoiceMatchesAvailable(tool, available) {
			changed = true
			continue
		}
		updated, errSet := sjson.SetRawBytes(filtered, "-1", []byte(tool.Raw))
		if errSet != nil {
			return body
		}
		filtered = updated
	}
	if !changed {
		return body
	}
	if len(gjson.ParseBytes(filtered).Array()) == 0 {
		body, _ = sjson.DeleteBytes(body, "tool_choice")
		return body
	}
	body, _ = sjson.SetRawBytes(body, "tool_choice.tools", filtered)
	return body
}

// xaiToolChoiceKey identifies a selectable tool the way xAI tool_choice entries
// reference it after namespace qualification: type alone for host tools, or
// type+name for function tools.
type xaiToolChoiceKey struct {
	toolType string
	name     string
}

func collectXAIAvailableToolChoiceKeys(body []byte) map[xaiToolChoiceKey]struct{} {
	keys := make(map[xaiToolChoiceKey]struct{})
	collect := func(tools gjson.Result) {
		if !tools.IsArray() {
			return
		}
		for _, tool := range tools.Array() {
			toolType := strings.TrimSpace(tool.Get("type").String())
			if toolType == "" {
				continue
			}
			key := xaiToolChoiceKey{toolType: toolType}
			if toolType == xaiFunctionToolType || toolType == xaiCustomToolType {
				key.name = strings.TrimSpace(tool.Get("name").String())
				if key.name == "" {
					continue
				}
			}
			keys[key] = struct{}{}
		}
	}
	collect(gjson.GetBytes(body, "tools"))
	input := gjson.GetBytes(body, "input")
	if input.IsArray() {
		for _, item := range input.Array() {
			if item.Get("type").String() == "additional_tools" {
				collect(item.Get("tools"))
			}
		}
	}
	return keys
}

func xaiToolChoiceMatchesAvailable(choice gjson.Result, available map[xaiToolChoiceKey]struct{}) bool {
	toolType := strings.TrimSpace(choice.Get("type").String())
	if toolType == "" {
		return false
	}
	key := xaiToolChoiceKey{toolType: toolType}
	if toolType == xaiFunctionToolType || toolType == xaiCustomToolType {
		key.name = strings.TrimSpace(choice.Get("name").String())
		if key.name == "" {
			return false
		}
	}
	_, ok := available[key]
	return ok
}

// xaiGrokImageGenerationMinVersion is the first Grok line that accepts xAI's
// native Responses image_generation tool. Older conversation models still
// reject that hosted type, so the executor keeps stripping it there.
var xaiGrokImageGenerationMinVersion = xaiGrokVersion{major: 4, minor: 6}

type xaiGrokVersion struct {
	major int
	minor int
}

// xaiSupportsNativeImageGeneration reports whether the Grok model accepts
// xAI's native Responses image_generation tool. grok-4.20-* is an older
// product line whose dotted minor is not comparable to grok-4.6.
func xaiSupportsNativeImageGeneration(model string) bool {
	name := strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).ModelName))
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	if name == "" || !strings.HasPrefix(name, "grok-") {
		return false
	}
	rest := strings.TrimPrefix(name, "grok-")
	if rest == "4.20" || strings.HasPrefix(rest, "4.20-") {
		return false
	}
	ver, ok := xaiParseGrokVersionPrefix(rest)
	if !ok {
		return false
	}
	return xaiCompareGrokVersion(ver, xaiGrokImageGenerationMinVersion) >= 0
}

func xaiParseGrokVersionPrefix(rest string) (xaiGrokVersion, bool) {
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	if i == 0 {
		return xaiGrokVersion{}, false
	}
	major, err := strconv.Atoi(rest[:i])
	if err != nil {
		return xaiGrokVersion{}, false
	}
	if i == len(rest) || rest[i] != '.' {
		return xaiGrokVersion{major: major, minor: -1}, true
	}
	j := i + 1
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	if j == i+1 {
		return xaiGrokVersion{major: major, minor: -1}, true
	}
	minor, err := strconv.Atoi(rest[i+1 : j])
	if err != nil {
		return xaiGrokVersion{}, false
	}
	return xaiGrokVersion{major: major, minor: minor}, true
}

func xaiCompareGrokVersion(a, b xaiGrokVersion) int {
	if a.major != b.major {
		if a.major < b.major {
			return -1
		}
		return 1
	}
	aMinor := a.minor
	if aMinor < 0 {
		aMinor = 0
	}
	bMinor := b.minor
	if bMinor < 0 {
		bMinor = 0
	}
	if aMinor < bMinor {
		return -1
	}
	if aMinor > bMinor {
		return 1
	}
	return 0
}

func normalizeXAITools(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	keepImageGeneration := xaiSupportsNativeImageGeneration(gjson.GetBytes(body, "model").String())
	original := body
	normalizeAtPath := func(path string) bool {
		tools := gjson.GetBytes(body, path)
		if !tools.Exists() || !tools.IsArray() {
			return true
		}
		filtered, changed, ok := normalizeXAIToolArray(tools, keepImageGeneration)
		if !ok {
			return false
		}
		if !changed {
			return true
		}
		updated, errSet := sjson.SetRawBytes(body, path, filtered)
		if errSet != nil {
			return false
		}
		body = updated
		return true
	}

	if !normalizeAtPath("tools") {
		return original
	}
	input := gjson.GetBytes(body, "input")
	if input.Exists() && input.IsArray() {
		for index, item := range input.Array() {
			if item.Get("type").String() != "additional_tools" {
				continue
			}
			if !normalizeAtPath(fmt.Sprintf("input.%d.tools", index)) {
				return original
			}
		}
	}
	return body
}

func normalizeXAIToolArray(tools gjson.Result, keepImageGeneration bool) ([]byte, bool, bool) {
	changed := false
	filtered := []byte(`[]`)
	for _, tool := range tools.Array() {
		toolType := tool.Get("type").String()
		if toolType == xaiNamespaceToolType {
			changed = true
			namespaceName := tool.Get("name").String()
			if namespaceTools := tool.Get("tools"); namespaceTools.IsArray() {
				for _, nestedTool := range namespaceTools.Array() {
					nestedRaw, nestedChanged, ok := normalizeXAITool(nestedTool, namespaceName, keepImageGeneration)
					if !ok {
						return nil, false, false
					}
					changed = changed || nestedChanged
					if len(nestedRaw) == 0 {
						continue
					}
					updated, errSet := sjson.SetRawBytes(filtered, "-1", nestedRaw)
					if errSet != nil {
						return nil, false, false
					}
					filtered = updated
				}
			}
			continue
		}
		raw, toolChanged, ok := normalizeXAITool(tool, "", keepImageGeneration)
		if !ok {
			return nil, false, false
		}
		changed = changed || toolChanged
		if len(raw) == 0 {
			continue
		}
		updated, errSet := sjson.SetRawBytes(filtered, "-1", raw)
		if errSet != nil {
			return nil, false, false
		}
		filtered = updated
	}
	return filtered, changed, true
}

// normalizeXAIToolChoiceForTools drops tool_choice and parallel_tool_calls
// when tools are absent or empty (including after normalizeXAITools filtering).
// xAI rejects payloads that include tool_choice without any tools defined.
// Existence checks avoid unnecessary sjson parse/copy passes.
func normalizeXAIToolChoiceForTools(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	hasTools := tools.Exists() && tools.IsArray() && len(tools.Array()) > 0
	if !hasTools {
		input := gjson.GetBytes(body, "input")
		if input.Exists() && input.IsArray() {
			for _, item := range input.Array() {
				additionalTools := item.Get("tools")
				if item.Get("type").String() == "additional_tools" && additionalTools.IsArray() && len(additionalTools.Array()) > 0 {
					hasTools = true
					break
				}
			}
		}
	}
	if hasTools {
		return body
	}
	if tools.Exists() {
		body, _ = sjson.DeleteBytes(body, "tools")
	}
	if gjson.GetBytes(body, "tool_choice").Exists() {
		body, _ = sjson.DeleteBytes(body, "tool_choice")
	}
	if gjson.GetBytes(body, "parallel_tool_calls").Exists() {
		body, _ = sjson.DeleteBytes(body, "parallel_tool_calls")
	}
	return body
}

// normalizeXAINamespaceToolChoice qualifies namespaced function choices using
// the same names sent in the flattened tools list. xAI does not accept the
// Responses namespace field on tool choices.
func normalizeXAINamespaceToolChoice(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	original := body
	normalizeAtPath := func(path string) bool {
		toolChoice := gjson.GetBytes(body, path)
		if !toolChoice.IsObject() || toolChoice.Get("type").String() != xaiFunctionToolType {
			return true
		}
		namespaceName := strings.TrimSpace(toolChoice.Get("namespace").String())
		toolName := strings.TrimSpace(toolChoice.Get("name").String())
		qualifiedName := qualifyXAINamespaceToolName(namespaceName, toolName)
		if namespaceName == "" || qualifiedName == "" {
			return true
		}
		updated, errSet := sjson.SetBytes(body, path+".name", qualifiedName)
		if errSet != nil {
			return false
		}
		updated, errDelete := sjson.DeleteBytes(updated, path+".namespace")
		if errDelete != nil {
			return false
		}
		body = updated
		return true
	}

	if !normalizeAtPath("tool_choice") {
		return original
	}
	tools := gjson.GetBytes(body, "tool_choice.tools")
	if tools.IsArray() {
		for index := range tools.Array() {
			if !normalizeAtPath(fmt.Sprintf("tool_choice.tools.%d", index)) {
				return original
			}
		}
	}
	return body
}

func normalizeXAITool(tool gjson.Result, namespaceName string, keepImageGeneration bool) ([]byte, bool, bool) {
	toolType := tool.Get("type").String()
	changed := false
	if toolType == xaiToolSearchType || (toolType == xaiImageGenerationToolType && !keepImageGeneration) {
		return nil, true, true
	}
	if toolType == xaiCustomToolType && tool.Get("name").String() == "apply_patch" {
		return nil, true, true
	}

	raw := []byte(tool.Raw)
	schemaTool := tool
	if toolType == xaiFunctionToolType || toolType == xaiCustomToolType {
		if rawParams := schemaTool.Get("parameters"); rawParams.Exists() {
			inlinedParams := util.InlineLocalRefs(rawParams.Raw)
			// Only rewrite when every local $ref resolved. A leftover $ref points
			// outside the parameters subtree (e.g. #/components/...), and stripping
			// $defs/definitions then leaves a dangling reference behind.
			if inlinedParams != rawParams.Raw && !strings.Contains(inlinedParams, `"$ref"`) {
				if updated, errSet := sjson.SetRawBytes(raw, "parameters", []byte(inlinedParams)); errSet == nil {
					if inlinedDefs := gjson.GetBytes(updated, "parameters.$defs"); inlinedDefs.Exists() {
						updated, _ = sjson.DeleteBytes(updated, "parameters.$defs")
					}
					if inlinedDefinitions := gjson.GetBytes(updated, "parameters.definitions"); inlinedDefinitions.Exists() {
						updated, _ = sjson.DeleteBytes(updated, "parameters.definitions")
					}
					raw = updated
					schemaTool = gjson.ParseBytes(raw)
					changed = true
				}
			}
		}
		updatedTool, schemaChanged, ok := normalizeXAIObjectRootUnionBranchTypes(raw)
		if !ok {
			return nil, false, false
		}
		raw = updatedTool
		if schemaChanged {
			schemaTool = gjson.ParseBytes(raw)
			changed = true
			log.Debugf("xai: added object types to root union branches for tool %s.%s", namespaceName, tool.Get("name").String())
		}
	}
	if toolType == xaiCustomToolType {
		updatedTool, errSet := sjson.SetBytes(raw, "type", xaiFunctionToolType)
		if errSet != nil {
			return nil, false, false
		}
		raw = updatedTool
		toolType = xaiFunctionToolType
		changed = true
	}
	if toolType == xaiWebSearchToolType && tool.Get("external_web_access").Exists() {
		updatedTool, errDel := sjson.DeleteBytes(raw, "external_web_access")
		if errDel != nil {
			return nil, false, false
		}
		raw = updatedTool
		changed = true
	}
	if toolType == xaiFunctionToolType && !schemaTool.Get("parameters").Exists() {
		updatedTool, errSet := sjson.SetRawBytes(raw, "parameters", []byte(`{"type":"object","properties":{}}`))
		if errSet != nil {
			return nil, false, false
		}
		raw = updatedTool
		changed = true
	}
	if toolType == xaiFunctionToolType && xaiFunctionParametersNeedSimplification(schemaTool, namespaceName) {
		updatedTool, errSet := sjson.SetRawBytes(raw, "parameters", []byte(xaiSafeFunctionParameters))
		if errSet != nil {
			return nil, false, false
		}
		raw = updatedTool
		if strict := schemaTool.Get("strict"); strict.Exists() && strict.Bool() {
			updatedTool, errSet = sjson.SetBytes(raw, "strict", false)
			if errSet != nil {
				return nil, false, false
			}
			raw = updatedTool
		}
		changed = true
		log.Debugf("xai: simplified parameters for tool %s.%s to avoid upstream hang", namespaceName, tool.Get("name").String())
	}
	if toolType == xaiFunctionToolType && strings.TrimSpace(namespaceName) != "" {
		qualifiedName := qualifyXAINamespaceToolName(namespaceName, tool.Get("name").String())
		if qualifiedName == "" {
			return nil, false, false
		}
		updatedTool, errSet := sjson.SetBytes(raw, "name", qualifiedName)
		if errSet != nil {
			return nil, false, false
		}
		raw = updatedTool
		changed = true
	}
	return raw, changed, true
}

func qualifyXAINamespaceToolName(namespaceName, toolName string) string {
	namespaceName = strings.TrimSpace(namespaceName)
	toolName = strings.TrimSpace(toolName)
	if namespaceName == "" || toolName == "" || strings.HasPrefix(toolName, "mcp__") {
		return toolName
	}
	prefix := namespaceName
	if !strings.HasSuffix(prefix, "__") {
		prefix += "__"
	}
	if strings.HasPrefix(toolName, prefix) {
		return toolName
	}
	return prefix + toolName
}

func collectXAINamespaceToolRefs(body []byte) map[string]xaiNamespaceToolRef {
	refs := make(map[string]xaiNamespaceToolRef)
	collect := func(tools gjson.Result) {
		if !tools.Exists() || !tools.IsArray() {
			return
		}
		for _, tool := range tools.Array() {
			if tool.Get("type").String() != xaiNamespaceToolType {
				continue
			}
			namespaceName := strings.TrimSpace(tool.Get("name").String())
			if namespaceName == "" {
				continue
			}
			for _, nestedTool := range tool.Get("tools").Array() {
				toolName := strings.TrimSpace(nestedTool.Get("name").String())
				qualifiedName := qualifyXAINamespaceToolName(namespaceName, toolName)
				if qualifiedName == "" {
					continue
				}
				refs[qualifiedName] = xaiNamespaceToolRef{namespace: namespaceName, name: toolName}
			}
		}
	}
	collect(gjson.GetBytes(body, "tools"))
	input := gjson.GetBytes(body, "input")
	if input.Exists() && input.IsArray() {
		for _, item := range input.Array() {
			if item.Get("type").String() == "additional_tools" {
				collect(item.Get("tools"))
			}
		}
	}
	return refs
}

func normalizeXAIInputCustomToolCalls(body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.Exists() || !input.IsArray() {
		return body
	}

	changed := false
	inputArray := input.Array()
	items := make([]json.RawMessage, 0, len(inputArray))
	for _, item := range inputArray {
		var normalized []byte
		switch item.Get("type").String() {
		case "custom_tool_call":
			callID := strings.TrimSpace(item.Get("call_id").String())
			name := strings.TrimSpace(item.Get("name").String())
			if callID == "" || name == "" {
				changed = true
				continue
			}
			normalized = []byte(`{"type":"function_call"}`)
			normalized, _ = sjson.SetBytes(normalized, "call_id", callID)
			normalized, _ = sjson.SetBytes(normalized, "name", name)
			normalized, _ = sjson.SetBytes(normalized, "arguments", xaiCustomToolCallArguments(item.Get("input")))
		case "custom_tool_call_output":
			callID := strings.TrimSpace(item.Get("call_id").String())
			if callID == "" {
				changed = true
				continue
			}
			normalized = []byte(`{"type":"function_call_output"}`)
			normalized, _ = sjson.SetBytes(normalized, "call_id", callID)
			normalized, _ = sjson.SetBytes(normalized, "output", xaiCustomToolCallOutput(item.Get("output")))
		default:
			items = append(items, json.RawMessage(item.Raw))
			continue
		}
		items = append(items, json.RawMessage(normalized))
		changed = true
	}
	if !changed {
		return body
	}

	rawInput, errMarshal := json.Marshal(items)
	if errMarshal != nil {
		return body
	}
	updated, errSet := sjson.SetRawBytes(body, "input", rawInput)
	if errSet != nil {
		return body
	}
	return updated
}

func xaiCustomToolCallArguments(input gjson.Result) string {
	if !input.Exists() {
		return "{}"
	}
	if input.Type == gjson.String {
		text := input.String()
		trimmed := strings.TrimSpace(text)
		if gjson.Valid(trimmed) {
			parsed := gjson.Parse(trimmed)
			if parsed.IsObject() {
				return parsed.Raw
			}
		}
		encoded, errMarshal := json.Marshal(text)
		if errMarshal != nil {
			return "{}"
		}
		return `{"input":` + string(encoded) + `}`
	}
	if input.IsObject() {
		return input.Raw
	}
	if input.Raw != "" {
		return `{"input":` + input.Raw + `}`
	}
	return "{}"
}

func xaiCustomToolCallOutput(output gjson.Result) string {
	if !output.Exists() {
		return ""
	}
	if output.Type == gjson.String {
		return output.String()
	}
	return output.Raw
}

// xAI executes these x_search subtools server-side but exposes their trace as
// client-style tool calls. Hide the trace so Responses clients do not execute it again.
// xaiHasClientWebSearchFunction reports whether the body declares an unnamespaced
// function/custom tool named "web_search" that should be aliased before sending upstream.
func xaiHasClientWebSearchFunction(body []byte, namespaceTools map[string]xaiNamespaceToolRef) bool {
	if !gjson.ValidBytes(body) {
		return false
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		toolType := strings.TrimSpace(tool.Get("type").String())
		name := strings.TrimSpace(tool.Get("name").String())
		if (toolType == xaiFunctionToolType || toolType == xaiCustomToolType) && name == xaiWebSearchToolType {
			if _, isNamespace := namespaceTools[name]; isNamespace {
				continue
			}
			return true
		}
	}
	return false
}

// xaiBodyHasToolNamed reports whether the body already has a tool or input item with the given name.
func xaiBodyHasToolNamed(body []byte, name string) bool {
	tools := gjson.GetBytes(body, "tools")
	if tools.IsArray() {
		for _, tool := range tools.Array() {
			if strings.TrimSpace(tool.Get("name").String()) == name {
				return true
			}
			if nested := tool.Get("tools"); nested.IsArray() {
				for _, child := range nested.Array() {
					if strings.TrimSpace(child.Get("name").String()) == name {
						return true
					}
				}
			}
		}
	}
	input := gjson.GetBytes(body, "input")
	if input.IsArray() {
		for _, item := range input.Array() {
			if strings.TrimSpace(item.Get("name").String()) == name {
				return true
			}
		}
	}
	return false
}

// xaiResolveClientWebSearchAlias picks a unique alias for the client's web_search tool.
// It tries the base alias first, then appends _1, _2, ... up to a safety limit
// to prevent unbounded CPU consumption on pathological requests.
func xaiResolveClientWebSearchAlias(body []byte) string {
	candidate := xaiClientWebSearchAlias
	if !xaiBodyHasToolNamed(body, candidate) {
		return candidate
	}
	const maxRetries = 1000
	for i := 1; i <= maxRetries; i++ {
		next := fmt.Sprintf("%s_%d", xaiClientWebSearchAlias, i)
		if !xaiBodyHasToolNamed(body, next) {
			return next
		}
	}
	return xaiClientWebSearchAlias
}

// aliasXAIClientWebSearchInput rewrites unnamespaced web_search calls in the input array.
func aliasXAIClientWebSearchInput(body []byte, alias string, namespaceTools map[string]xaiNamespaceToolRef) []byte {
	if !gjson.ValidBytes(body) || alias == "" {
		return body
	}
	if _, isNamespace := namespaceTools[xaiWebSearchToolType]; isNamespace {
		return body
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}
	for idx, item := range input.Array() {
		itemType := strings.TrimSpace(item.Get("type").String())
		itemName := strings.TrimSpace(item.Get("name").String())
		itemNamespace := strings.TrimSpace(item.Get("namespace").String())
		if (itemType == "function_call" || itemType == "custom_tool_call" || itemType == "function_call_output") &&
			itemName == xaiWebSearchToolType && itemNamespace == "" {
			body, _ = sjson.SetBytes(body, fmt.Sprintf("input.%d.name", idx), alias)
		}
	}
	return body
}

// aliasXAIClientWebSearchFunction rewrites the client's web_search tool declaration,
// tool_choice, and input to use the resolved alias before sending upstream.
func aliasXAIClientWebSearchFunction(body []byte, alias string, namespaceTools map[string]xaiNamespaceToolRef) []byte {
	if !gjson.ValidBytes(body) || alias == "" {
		return body
	}
	// 1. Alias in tools (excluding namespace dispatchers)
	tools := gjson.GetBytes(body, "tools")
	if tools.IsArray() {
		for idx, tool := range tools.Array() {
			toolType := strings.TrimSpace(tool.Get("type").String())
			name := strings.TrimSpace(tool.Get("name").String())
			if (toolType == xaiFunctionToolType || toolType == xaiCustomToolType) && name == xaiWebSearchToolType {
				if _, isNamespace := namespaceTools[name]; isNamespace {
					continue
				}
				body, _ = sjson.SetBytes(body, fmt.Sprintf("tools.%d.name", idx), alias)
			}
		}
	}

	// 2. Alias in tool_choice (only when unnamespaced and not a namespace dispatcher)
	choice := gjson.GetBytes(body, "tool_choice")
	if choice.IsObject() {
		if fnName := choice.Get("function.name"); fnName.Exists() && strings.TrimSpace(fnName.String()) == xaiWebSearchToolType {
			if strings.TrimSpace(choice.Get("function.namespace").String()) == "" {
				if _, isNamespace := namespaceTools[xaiWebSearchToolType]; !isNamespace {
					body, _ = sjson.SetBytes(body, "tool_choice.function.name", alias)
				}
			}
		}
		if name := choice.Get("name"); name.Exists() && strings.TrimSpace(name.String()) == xaiWebSearchToolType {
			if strings.TrimSpace(choice.Get("namespace").String()) == "" {
				choiceType := strings.TrimSpace(choice.Get("type").String())
				if choiceType == xaiFunctionToolType || choiceType == "tool" {
					if _, isNamespace := namespaceTools[xaiWebSearchToolType]; !isNamespace {
						body, _ = sjson.SetBytes(body, "tool_choice.name", alias)
					}
				}
			}
		}
		if allowed := choice.Get("tools"); allowed.IsArray() {
			for idx, allowedTool := range allowed.Array() {
				if strings.TrimSpace(allowedTool.Get("namespace").String()) != "" {
					continue
				}
				if _, isNamespace := namespaceTools[xaiWebSearchToolType]; isNamespace {
					continue
				}
				allowedType := strings.TrimSpace(allowedTool.Get("type").String())
				allowedName := strings.TrimSpace(allowedTool.Get("name").String())
				if (allowedType == xaiFunctionToolType || allowedType == "tool") && allowedName == xaiWebSearchToolType {
					body, _ = sjson.SetBytes(body, fmt.Sprintf("tool_choice.tools.%d.name", idx), alias)
				} else if allowedName == xaiWebSearchToolType && allowedType != xaiWebSearchToolType {
					body, _ = sjson.SetBytes(body, fmt.Sprintf("tool_choice.tools.%d.name", idx), alias)
				}
			}
		}
	}

	// 3. Alias in input (conversation history, only when unnamespaced)
	return aliasXAIClientWebSearchInput(body, alias, namespaceTools)
}
