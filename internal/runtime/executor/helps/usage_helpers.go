package helps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type UsageReporter struct {
	provider                   string
	model                      string
	alias                      string
	authID                     string
	authIndex                  string
	authMu                     sync.RWMutex
	accessTokenHash            string
	authType                   string
	apiKey                     string
	source                     string
	reasoning                  string
	serviceTier                string
	pendingResponseServiceTier string
	requestedAt                time.Time
	once                       sync.Once
}

type usageExecutor interface {
	Identifier() string
}

// NewExecutorUsageReporter builds a usage reporter from an executor identifier.
func NewExecutorUsageReporter(ctx context.Context, executor usageExecutor, model string, auth *cliproxyauth.Auth) *UsageReporter {
	provider := ""
	if executor != nil {
		provider = executor.Identifier()
	}
	return NewUsageReporter(ctx, provider, model, auth)
}

func NewUsageReporter(ctx context.Context, provider, model string, auth *cliproxyauth.Auth) *UsageReporter {
	apiKey := APIKeyFromContext(ctx)
	alias := usage.RequestedModelAliasFromContext(ctx)
	if alias == "" {
		alias = model
	}
	reporter := &UsageReporter{
		provider:    provider,
		model:       model,
		alias:       strings.TrimSpace(alias),
		requestedAt: time.Now(),
		apiKey:      apiKey,
		source:      resolveUsageSource(auth, apiKey),
		authType:    resolveUsageAuthType(auth),
		reasoning:   usage.ReasoningEffortFromContext(ctx),
		serviceTier: usage.ServiceTierFromContext(ctx),
	}
	if auth != nil {
		reporter.authID = auth.ID
		reporter.authIndex = auth.EnsureIndex()
	}
	return reporter
}

// SetTranslatedReasoningEffort records the translated thinking level for usage logs.
func (r *UsageReporter) SetTranslatedReasoningEffort(payload []byte, format string) {
	if r == nil {
		return
	}
	r.reasoning = thinking.ExtractTranslatedReasoningEffort(payload, format)
}

// UpdateAccessTokenFingerprint records the access token hash for the auth.
func (r *UsageReporter) UpdateAccessTokenFingerprint(auth *cliproxyauth.Auth) {
	if r == nil {
		return
	}
	r.authMu.Lock()
	r.accessTokenHash = authAccessTokenSHA256(auth)
	r.authMu.Unlock()
}

// accessTokenFingerprint returns the stored access token hash.
func (r *UsageReporter) accessTokenFingerprint() string {
	if r == nil {
		return ""
	}
	r.authMu.RLock()
	defer r.authMu.RUnlock()
	return r.accessTokenHash
}

// TrackHTTPClient returns the client unchanged.
// Local UsageReporter does not yet instrument per-byte TTFT transports.
func (r *UsageReporter) TrackHTTPClient(client *http.Client) *http.Client {
	return client
}

func (r *UsageReporter) Publish(ctx context.Context, detail usage.Detail) {
	r.publishWithOutcome(ctx, detail, false, usage.Failure{})
}

func (r *UsageReporter) PublishAdditionalModel(ctx context.Context, model string, detail usage.Detail) {
	record, ok := r.buildAdditionalModelRecord(model, detail)
	if !ok {
		return
	}
	r.publishRecord(ctx, record)
}

func (r *UsageReporter) buildAdditionalModelRecord(model string, detail usage.Detail) (usage.Record, bool) {
	if r == nil {
		return usage.Record{}, false
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return usage.Record{}, false
	}
	detail = normalizeUsageDetailTotal(detail)
	if !hasNonZeroTokenUsage(detail) {
		return usage.Record{}, false
	}
	return r.buildRecordForModel(model, detail, false, usage.Failure{}), true
}

func (r *UsageReporter) PublishFailure(ctx context.Context, errs ...error) {
	r.publishWithOutcome(ctx, usage.Detail{}, true, failFromErrors(errs...))
}

// PublishFailureWithDetail emits a failure record carrying the last observed usage detail.
func (r *UsageReporter) PublishFailureWithDetail(ctx context.Context, detail usage.Detail, errs ...error) {
	r.publishWithOutcome(ctx, detail, true, failFromErrors(errs...))
}

func (r *UsageReporter) TrackFailure(ctx context.Context, errPtr *error) {
	if r == nil || errPtr == nil {
		return
	}
	if *errPtr != nil {
		r.PublishFailure(ctx, *errPtr)
	}
}

func (r *UsageReporter) publishWithOutcome(ctx context.Context, detail usage.Detail, failed bool, fail usage.Failure) {
	if r == nil {
		return
	}
	detail = normalizeUsageDetailTotal(detail)
	// Tier-only stream chunks should not finalize the once.Do publish, otherwise
	// a later usage chunk with tokens would be dropped.
	if !failed && !hasNonZeroTokenUsage(detail) {
		if tier := strings.TrimSpace(detail.ResponseServiceTier); tier != "" {
			r.pendingResponseServiceTier = tier
		}
		return
	}
	if strings.TrimSpace(detail.ResponseServiceTier) == "" {
		detail.ResponseServiceTier = r.pendingResponseServiceTier
	}
	r.once.Do(func() {
		r.publishRecord(ctx, r.buildRecord(detail, failed, fail))
	})
}

func normalizeUsageDetailTotal(detail usage.Detail) usage.Detail {
	if detail.TotalTokens == 0 {
		total := detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
		if total > 0 {
			detail.TotalTokens = total
		}
	}
	return detail
}

func hasNonZeroTokenUsage(detail usage.Detail) bool {
	return detail.InputTokens != 0 ||
		detail.OutputTokens != 0 ||
		detail.ReasoningTokens != 0 ||
		detail.CachedTokens != 0 ||
		detail.CacheReadTokens != 0 ||
		detail.CacheCreationTokens != 0 ||
		detail.TotalTokens != 0
}

func safeUsageTokenSum(values ...int64) (int64, bool) {
	var total int64
	for _, value := range values {
		if value < 0 || total > int64(^uint64(0)>>1)-value {
			return 0, false
		}
		total += value
	}
	return total, true
}

func parseInteractionsUsageDetail(node gjson.Result) usage.Detail {
	cacheRead := firstExistingUsageNode(node, "cache_read_tokens", "cacheReadTokens")
	toolUseTokens := firstExistingUsageNode(node, "tool_use_tokens", "total_tool_use_tokens", "toolUseTokens", "totalToolUseTokens").Int()
	inputTokens, okInput := safeUsageTokenSum(
		firstExistingUsageNode(node, "input_tokens", "prompt_tokens", "total_input_tokens").Int(),
		toolUseTokens,
	)
	detail := usage.Detail{
		InputTokens:         inputTokens,
		OutputTokens:        firstExistingUsageNode(node, "output_tokens", "completion_tokens", "total_output_tokens").Int(),
		ReasoningTokens:     firstExistingUsageNode(node, "reasoning_tokens", "thoughtsTokenCount", "total_thought_tokens").Int(),
		TotalTokens:         firstExistingUsageNode(node, "total_tokens", "totalTokenCount").Int(),
		CachedTokens:        firstExistingUsageNode(node, "cached_tokens", "cachedContentTokenCount", "total_cached_tokens").Int(),
		CacheReadTokens:     cacheRead.Int(),
		CacheCreationTokens: firstExistingUsageNode(node, "cache_creation_tokens", "cacheCreationTokens", "cache_write_tokens", "cacheWriteTokens").Int(),
	}
	if !okInput {
		return detail
	}
	if !cacheRead.Exists() && detail.CachedTokens > 0 {
		detail.CacheReadTokens = detail.CachedTokens
	}
	if detail.TotalTokens == 0 {
		var okTotal bool
		detail.TotalTokens, okTotal = safeUsageTokenSum(detail.InputTokens, detail.OutputTokens, detail.ReasoningTokens)
		if !okTotal {
			detail.TotalTokens = 0
			return detail
		}
	}
	return detail
}

func hasUsageDetail(detail usage.Detail) bool {
	return hasNonZeroTokenUsage(detail)
}

// ParseInteractionsUsage parses usage from an interactions-format payload.
func ParseInteractionsUsage(data []byte) usage.Detail {
	root := gjson.ParseBytes(data)
	node := firstExistingUsageNode(root, "usage", "total_usage", "metadata.total_usage", "metadata.usage", "usageMetadata", "usage_metadata", "interaction.usage", "interaction.total_usage", "interaction.metadata.total_usage")
	if !node.Exists() {
		return usage.Detail{}
	}
	if node.Get("promptTokenCount").Exists() || node.Get("candidatesTokenCount").Exists() {
		detail := parseGeminiFamilyUsageDetail(node)
		detail.ResponseServiceTier = extractResponseServiceTier(data)
		return detail
	}
	detail := parseInteractionsUsageDetail(node)
	detail.ResponseServiceTier = extractResponseServiceTier(data)
	return detail
}

// ParseInteractionsStreamUsage parses usage from a streaming interactions-format line.
func ParseInteractionsStreamUsage(line []byte) (usage.Detail, bool) {
	payload := jsonPayload(line)
	if len(payload) == 0 {
		payload = line
	}
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	detail := ParseInteractionsUsage(payload)
	if !hasUsageDetail(detail) {
		return usage.Detail{}, false
	}
	return detail, true
}

// ensurePublished guarantees that a usage record is emitted exactly once.
// It is safe to call multiple times; only the first call wins due to once.Do.
// This is used to ensure request counting even when upstream responses do not
// include any usage fields (tokens), especially for streaming paths.
func (r *UsageReporter) EnsurePublished(ctx context.Context) {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.publishRecord(ctx, r.buildRecord(usage.Detail{}, false, usage.Failure{}))
	})
}

func (r *UsageReporter) publishRecord(ctx context.Context, record usage.Record) {
	record.ResponseHeaders = internallogging.GetResponseHeaders(ctx)
	if ttft := r.firstTokenLatency(ctx); ttft > 0 {
		record.TTFT = ttft
	}
	usage.PublishRecord(ctx, record)
}

// firstTokenLatency derives TTFT from the response writer's first-byte timestamp
// when available; callers fall back to total latency otherwise.
func (r *UsageReporter) firstTokenLatency(ctx context.Context) time.Duration {
	if r == nil || r.requestedAt.IsZero() || ctx == nil {
		return 0
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return 0
	}
	timer, ok := ginCtx.Writer.(interface{ FirstChunkTime() time.Time })
	if !ok {
		return 0
	}
	first := timer.FirstChunkTime()
	if first.IsZero() || first.Before(r.requestedAt) {
		return 0
	}
	return first.Sub(r.requestedAt)
}

func (r *UsageReporter) buildRecord(detail usage.Detail, failed bool, failures ...usage.Failure) usage.Record {
	var fail usage.Failure
	if len(failures) > 0 {
		fail = failures[0]
	}
	if r == nil {
		return usage.Record{Detail: detail, Failed: failed, Fail: fail}
	}
	return r.buildRecordForModel(r.model, detail, failed, fail)
}

func (r *UsageReporter) buildRecordForModel(model string, detail usage.Detail, failed bool, fail usage.Failure) usage.Record {
	if r == nil {
		return usage.Record{Model: model, Detail: detail, Failed: failed, Fail: fail}
	}
	return usage.Record{
		Provider:            r.provider,
		Model:               model,
		Alias:               r.alias,
		Source:              r.source,
		APIKey:              r.apiKey,
		AuthID:              r.authID,
		AuthIndex:           r.authIndex,
		AuthType:            r.authType,
		ReasoningEffort:     r.reasoning,
		ServiceTier:         r.serviceTier,
		ResponseServiceTier: strings.TrimSpace(detail.ResponseServiceTier),
		RequestedAt:         r.requestedAt,
		Latency:             r.latency(),
		TTFT:                r.latency(),
		Failed:              failed,
		Fail:                fail,
		Detail:              detail,
	}
}

func failFromErrors(errs ...error) usage.Failure {
	for _, err := range errs {
		if err == nil {
			continue
		}
		fail := usage.Failure{
			Body: strings.TrimSpace(err.Error()),
		}
		var se interface{ StatusCode() int }
		if errors.As(err, &se) && se != nil {
			fail.StatusCode = se.StatusCode()
		}
		return fail
	}
	return usage.Failure{}
}

func (r *UsageReporter) latency() time.Duration {
	if r == nil || r.requestedAt.IsZero() {
		return 0
	}
	latency := time.Since(r.requestedAt)
	if latency < 0 {
		return 0
	}
	return latency
}

func APIKeyFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return ""
	}
	if v, exists := ginCtx.Get("userApiKey"); exists {
		switch value := v.(type) {
		case string:
			return value
		case fmt.Stringer:
			return value.String()
		default:
			return fmt.Sprintf("%v", value)
		}
	}
	return ""
}

func resolveUsageSource(auth *cliproxyauth.Auth, ctxAPIKey string) string {
	if auth != nil {
		provider := strings.TrimSpace(auth.Provider)
		if strings.EqualFold(provider, "gemini-cli") {
			if id := strings.TrimSpace(auth.ID); id != "" {
				return id
			}
		}
		if strings.EqualFold(provider, "vertex") {
			if auth.Metadata != nil {
				if projectID, ok := auth.Metadata["project_id"].(string); ok {
					if trimmed := strings.TrimSpace(projectID); trimmed != "" {
						return trimmed
					}
				}
				if project, ok := auth.Metadata["project"].(string); ok {
					if trimmed := strings.TrimSpace(project); trimmed != "" {
						return trimmed
					}
				}
			}
		}
		if _, value := auth.AccountInfo(); value != "" {
			return strings.TrimSpace(value)
		}
		if auth.Metadata != nil {
			if email, ok := auth.Metadata["email"].(string); ok {
				if trimmed := strings.TrimSpace(email); trimmed != "" {
					return trimmed
				}
			}
		}
		if auth.Attributes != nil {
			if key := strings.TrimSpace(auth.Attributes["api_key"]); key != "" {
				return key
			}
		}
	}
	if trimmed := strings.TrimSpace(ctxAPIKey); trimmed != "" {
		return trimmed
	}
	return ""
}

func resolveUsageAuthType(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	kind, _ := auth.AccountInfo()
	kind = strings.TrimSpace(kind)
	if kind == "api_key" {
		return "apikey"
	}
	return kind
}

func ParseCodexUsage(data []byte) (usage.Detail, bool) {
	responseServiceTier := extractResponseServiceTier(data)
	usageNode := gjson.ParseBytes(data).Get("response.usage")
	if !hasOpenAIStyleUsageTokenFields(usageNode) {
		if responseServiceTier == "" {
			return usage.Detail{}, false
		}
		return usage.Detail{ResponseServiceTier: responseServiceTier}, true
	}
	detail := parseOpenAIStyleUsageNode(usageNode)
	detail.ResponseServiceTier = responseServiceTier
	return detail, true
}

func ParseCodexImageToolUsage(data []byte) (usage.Detail, bool) {
	usageNode := gjson.ParseBytes(data).Get("response.tool_usage.image_gen")
	if !hasOpenAIStyleUsageTokenFields(usageNode) {
		return usage.Detail{}, false
	}
	return parseOpenAIStyleUsageNode(usageNode), true
}

func ParseOpenAIUsage(data []byte) usage.Detail {
	responseServiceTier := extractResponseServiceTier(data)
	usageNode := gjson.ParseBytes(data).Get("usage")
	if !hasOpenAIStyleUsageTokenFields(usageNode) {
		return usage.Detail{ResponseServiceTier: responseServiceTier}
	}
	detail := parseOpenAIStyleUsageNode(usageNode)
	detail.ResponseServiceTier = responseServiceTier
	return detail
}

func hasOpenAIStyleUsageTokenFields(usageNode gjson.Result) bool {
	if !usageNode.Exists() || !usageNode.IsObject() {
		return false
	}
	return usageNode.Get("prompt_tokens").Exists() ||
		usageNode.Get("input_tokens").Exists() ||
		usageNode.Get("completion_tokens").Exists() ||
		usageNode.Get("output_tokens").Exists() ||
		usageNode.Get("total_tokens").Exists() ||
		usageNode.Get("prompt_tokens_details.cached_tokens").Exists() ||
		usageNode.Get("input_tokens_details.cached_tokens").Exists() ||
		usageNode.Get("prompt_tokens_details.cache_write_tokens").Exists() ||
		usageNode.Get("prompt_tokens_details.cache_creation_tokens").Exists() ||
		usageNode.Get("input_tokens_details.cache_write_tokens").Exists() ||
		usageNode.Get("input_tokens_details.cache_creation_tokens").Exists() ||
		usageNode.Get("completion_tokens_details.reasoning_tokens").Exists() ||
		usageNode.Get("output_tokens_details.reasoning_tokens").Exists()
}

func parseOpenAIStyleUsageNode(usageNode gjson.Result) usage.Detail {
	inputNode := usageNode.Get("prompt_tokens")
	if !inputNode.Exists() {
		inputNode = usageNode.Get("input_tokens")
	}
	outputNode := usageNode.Get("completion_tokens")
	if !outputNode.Exists() {
		outputNode = usageNode.Get("output_tokens")
	}
	detail := usage.Detail{
		InputTokens:  inputNode.Int(),
		OutputTokens: outputNode.Int(),
		TotalTokens:  usageNode.Get("total_tokens").Int(),
	}
	cached := usageNode.Get("prompt_tokens_details.cached_tokens")
	if !cached.Exists() {
		cached = usageNode.Get("input_tokens_details.cached_tokens")
	}
	if cached.Exists() {
		detail.CachedTokens = cached.Int()
		detail.CacheReadTokens = cached.Int()
	}
	cacheCreation := firstExistingUsageNode(
		usageNode,
		"input_tokens_details.cache_creation_tokens",
		"input_tokens_details.cache_write_tokens",
		"prompt_tokens_details.cache_creation_tokens",
		"prompt_tokens_details.cache_write_tokens",
	)
	if cacheCreation.Exists() {
		detail.CacheCreationTokens = cacheCreation.Int()
	}
	reasoning := usageNode.Get("completion_tokens_details.reasoning_tokens")
	if !reasoning.Exists() {
		reasoning = usageNode.Get("output_tokens_details.reasoning_tokens")
	}
	if reasoning.Exists() {
		detail.ReasoningTokens = reasoning.Int()
	}
	return detail
}

var (
	openAIStreamUsageMarker       = []byte(`"usage"`)
	openAIStreamServiceTierMarker = []byte(`"service_tier"`)
)

// ParseOpenAIStreamUsage extracts usage from an OpenAI-style SSE line.
// It skips JSON parsing for chunks that cannot contain usage fields.
func ParseOpenAIStreamUsage(line []byte) (usage.Detail, bool) {
	payload := jsonPayload(line)
	if len(payload) == 0 {
		return usage.Detail{}, false
	}
	// Fast path: most stream chunks are deltas without usage/tier metadata.
	hasUsageCandidate := bytes.Contains(payload, openAIStreamUsageMarker)
	hasTierCandidate := bytes.Contains(payload, openAIStreamServiceTierMarker)
	if !hasUsageCandidate && !hasTierCandidate {
		return usage.Detail{}, false
	}
	if !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}

	responseServiceTier := ""
	if hasTierCandidate {
		responseServiceTier = extractResponseServiceTierFromValidJSON(payload)
	}

	if hasUsageCandidate {
		usageNode := gjson.GetBytes(payload, "usage")
		if hasOpenAIStyleUsageTokenFields(usageNode) {
			detail := parseOpenAIStyleUsageNode(usageNode)
			detail.ResponseServiceTier = responseServiceTier
			return detail, true
		}
	}
	// Tier-only chunks: return them so callers that buffer can retain the label.
	// Callers that publish on first ok chunk should prefer token usage events.
	if responseServiceTier != "" {
		return usage.Detail{ResponseServiceTier: responseServiceTier}, true
	}
	return usage.Detail{}, false
}

func extractResponseServiceTier(payload []byte) string {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return ""
	}
	return extractResponseServiceTierFromValidJSON(payload)
}

func extractResponseServiceTierFromValidJSON(payload []byte) string {
	for _, path := range []string{"response.service_tier", "service_tier", "interaction.service_tier"} {
		if tier := strings.TrimSpace(gjson.GetBytes(payload, path).String()); tier != "" {
			return tier
		}
	}
	return ""
}

func ParseClaudeUsage(data []byte) usage.Detail {
	usageNode := gjson.ParseBytes(data).Get("usage")
	if !usageNode.Exists() {
		return usage.Detail{}
	}
	return parseClaudeUsageNode(usageNode)
}

func ParseClaudeStreamUsage(line []byte) (usage.Detail, bool) {
	payload := jsonPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	usageNode := gjson.GetBytes(payload, "usage")
	if !usageNode.Exists() {
		usageNode = gjson.GetBytes(payload, "message.usage")
	}
	if !usageNode.Exists() {
		return usage.Detail{}, false
	}
	return parseClaudeUsageNode(usageNode), true
}

// StreamUsageBuffer keeps the latest usage detail observed in a stream.
type StreamUsageBuffer struct {
	detail usage.Detail
	ok     bool
}

// Observe records detail when ok is true, allowing the final stream usage to win.
func (b *StreamUsageBuffer) Observe(detail usage.Detail, ok bool) {
	if b == nil || !ok {
		return
	}
	responseServiceTier := strings.TrimSpace(detail.ResponseServiceTier)
	if responseServiceTier == "" || hasNonZeroTokenUsage(detail) {
		preservedTier := b.detail.ResponseServiceTier
		b.detail = detail
		if b.detail.ResponseServiceTier == "" {
			b.detail.ResponseServiceTier = preservedTier
		}
	} else {
		b.detail.ResponseServiceTier = responseServiceTier
	}
	b.ok = true
}

// ObserveClaudeStream records and merges usage from a Claude SSE line.
func (b *StreamUsageBuffer) ObserveClaudeStream(line []byte) {
	if b == nil {
		return
	}
	if detail, ok := ParseClaudeStreamUsage(line); ok {
		ObserveMergedStreamUsage(b, detail)
	}
}

// Publish emits the latest observed usage detail, if any.
func (b *StreamUsageBuffer) Publish(ctx context.Context, reporter *UsageReporter) bool {
	if b == nil || !b.ok || reporter == nil {
		return false
	}
	reporter.Publish(ctx, b.detail)
	return true
}

// PublishFailure emits the latest observed usage detail together with failure details.
func (b *StreamUsageBuffer) PublishFailure(ctx context.Context, reporter *UsageReporter, errs ...error) bool {
	if b == nil || reporter == nil {
		return false
	}
	reporter.PublishFailureWithDetail(ctx, b.detail, errs...)
	return true
}

// Detail returns the latest observed usage detail.
func (b *StreamUsageBuffer) Detail() (usage.Detail, bool) {
	if b == nil || !b.ok {
		return usage.Detail{}, false
	}
	return b.detail, true
}

// ObserveMergedStreamUsage updates buffer with merged usage details.
func ObserveMergedStreamUsage(buffer *StreamUsageBuffer, update usage.Detail) {
	if buffer == nil {
		return
	}
	if existing, ok := buffer.Detail(); ok {
		merged := MergeStreamUsageDetail(existing, update)
		buffer.Observe(merged, true)
		return
	}
	buffer.Observe(update, true)
}

// MergeStreamUsageDetail merges existing stream usage with a newer update.
func MergeStreamUsageDetail(existing, update usage.Detail) usage.Detail {
	merged := update
	if merged.InputTokens == 0 && existing.InputTokens > 0 {
		merged.InputTokens = existing.InputTokens
	}
	if merged.CachedTokens == 0 && existing.CachedTokens > 0 {
		merged.CachedTokens = existing.CachedTokens
	}
	if merged.CacheReadTokens == 0 && existing.CacheReadTokens > 0 {
		merged.CacheReadTokens = existing.CacheReadTokens
	}
	if merged.CacheCreationTokens == 0 && existing.CacheCreationTokens > 0 {
		merged.CacheCreationTokens = existing.CacheCreationTokens
	}
	if merged.OutputTokens == 0 && existing.OutputTokens > 0 {
		merged.OutputTokens = existing.OutputTokens
	}
	if merged.ReasoningTokens == 0 && existing.ReasoningTokens > 0 {
		merged.ReasoningTokens = existing.ReasoningTokens
	}
	if merged.ResponseServiceTier == "" {
		merged.ResponseServiceTier = existing.ResponseServiceTier
	}
	cached := merged.CacheReadTokens + merged.CacheCreationTokens
	if cached == 0 {
		cached = merged.CachedTokens
	}
	calculatedTotal := merged.InputTokens + merged.OutputTokens + cached
	if merged.TotalTokens == 0 || merged.TotalTokens < calculatedTotal {
		merged.TotalTokens = calculatedTotal
	}
	return merged
}

func parseClaudeUsageNode(usageNode gjson.Result) usage.Detail {
	cacheReadTokens := usageNode.Get("cache_read_input_tokens").Int()
	cacheCreationTokens := usageNode.Get("cache_creation_input_tokens").Int()
	rawOutputTokens := usageNode.Get("output_tokens").Int()
	// Anthropic reports thinking as a subset of output_tokens. Prefer the official
	// nested field, then fall back to legacy aliases used by some gateways.
	reasoningNode := firstExistingUsageNode(
		usageNode,
		"output_tokens_details.thinking_tokens",
		"output_tokens_details.reasoning_tokens",
		"thinking_tokens",
	)
	reasoningTokens := reasoningNode.Int()
	if reasoningTokens < 0 {
		reasoningTokens = 0
	}
	detail := usage.Detail{
		InputTokens:         usageNode.Get("input_tokens").Int(),
		OutputTokens:        rawOutputTokens,
		ReasoningTokens:     reasoningTokens,
		CachedTokens:        cacheReadTokens,
		CacheReadTokens:     cacheReadTokens,
		CacheCreationTokens: cacheCreationTokens,
	}
	if detail.CachedTokens == 0 {
		detail.CachedTokens = detail.CacheCreationTokens
	}
	// raw output_tokens already includes thinking; cache fields are independent
	// from input_tokens in the Messages API.
	detail.TotalTokens = detail.InputTokens + rawOutputTokens + detail.CacheReadTokens + detail.CacheCreationTokens
	return detail
}

func parseGeminiFamilyUsageDetail(node gjson.Result) usage.Detail {
	cachedTokens := node.Get("cachedContentTokenCount").Int()
	detail := usage.Detail{
		InputTokens:     node.Get("promptTokenCount").Int(),
		OutputTokens:    node.Get("candidatesTokenCount").Int(),
		ReasoningTokens: node.Get("thoughtsTokenCount").Int(),
		TotalTokens:     node.Get("totalTokenCount").Int(),
		CachedTokens:    cachedTokens,
		CacheReadTokens: cachedTokens,
	}
	if detail.TotalTokens == 0 {
		detail.TotalTokens = detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
	}
	return detail
}

func hasGeminiFamilyUsageTokenFields(node gjson.Result) bool {
	return node.Get("promptTokenCount").Exists() ||
		node.Get("candidatesTokenCount").Exists() ||
		node.Get("thoughtsTokenCount").Exists() ||
		node.Get("totalTokenCount").Exists() ||
		node.Get("cachedContentTokenCount").Exists()
}

func ParseGeminiCLIUsage(data []byte) usage.Detail {
	usageNode := gjson.ParseBytes(data)
	node := firstExistingUsageNode(usageNode,
		"response.usageMetadata",
		"response.usage_metadata",
		"usageMetadata",
		"usage_metadata",
	)
	if !node.Exists() {
		return usage.Detail{}
	}
	return parseGeminiFamilyUsageDetail(node)
}

func ParseGeminiUsage(data []byte) usage.Detail {
	usageNode := gjson.ParseBytes(data)
	node := usageNode.Get("usageMetadata")
	if !node.Exists() {
		node = usageNode.Get("usage_metadata")
	}
	if !node.Exists() {
		return usage.Detail{}
	}
	return parseGeminiFamilyUsageDetail(node)
}

func ParseGeminiStreamUsage(line []byte) (usage.Detail, bool) {
	payload := jsonPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	node := gjson.GetBytes(payload, "usageMetadata")
	if !node.Exists() {
		node = gjson.GetBytes(payload, "usage_metadata")
	}
	if !node.Exists() {
		return usage.Detail{}, false
	}
	detail := parseGeminiFamilyUsageDetail(node)
	if !hasNonZeroTokenUsage(detail) {
		return usage.Detail{}, false
	}
	return detail, true
}

func ParseGeminiCLIStreamUsage(line []byte) (usage.Detail, bool) {
	payload := jsonPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	root := gjson.ParseBytes(payload)
	node := firstExistingUsageNode(root,
		"response.usageMetadata",
		"response.usage_metadata",
		"usageMetadata",
		"usage_metadata",
	)
	if !node.Exists() {
		return usage.Detail{}, false
	}
	if !hasGeminiFamilyUsageTokenFields(node) {
		return usage.Detail{}, false
	}
	return parseGeminiFamilyUsageDetail(node), true
}

func firstExistingUsageNode(root gjson.Result, paths ...string) gjson.Result {
	for _, path := range paths {
		node := root.Get(path)
		if node.Exists() {
			return node
		}
	}
	return gjson.Result{}
}

func ParseAntigravityUsage(data []byte) usage.Detail {
	usageNode := gjson.ParseBytes(data)
	node := usageNode.Get("response.usageMetadata")
	if !node.Exists() {
		node = usageNode.Get("usageMetadata")
	}
	if !node.Exists() {
		node = usageNode.Get("usage_metadata")
	}
	if !node.Exists() {
		return usage.Detail{}
	}
	return parseGeminiFamilyUsageDetail(node)
}

func ParseAntigravityStreamUsage(line []byte) (usage.Detail, bool) {
	payload := jsonPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	node := gjson.GetBytes(payload, "response.usageMetadata")
	if !node.Exists() {
		node = gjson.GetBytes(payload, "usageMetadata")
	}
	if !node.Exists() {
		node = gjson.GetBytes(payload, "usage_metadata")
	}
	if !node.Exists() {
		return usage.Detail{}, false
	}
	return parseGeminiFamilyUsageDetail(node), true
}

var stopChunkWithoutUsage sync.Map

func rememberStopWithoutUsage(traceID string) {
	stopChunkWithoutUsage.Store(traceID, struct{}{})
	time.AfterFunc(10*time.Minute, func() { stopChunkWithoutUsage.Delete(traceID) })
}

// FilterSSEUsageMetadata removes usageMetadata from SSE events that are not
// terminal (finishReason != "stop"). Stop chunks are left untouched. This
// function is shared between aistudio and antigravity executors.
func FilterSSEUsageMetadata(payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}

	lines := bytes.Split(payload, []byte("\n"))
	modified := false
	foundData := false
	for idx, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		foundData = true
		before, after, ok := bytes.Cut(line, []byte("data:"))
		if !ok {
			continue
		}
		rawJSON := bytes.TrimSpace(after)
		traceID := gjson.GetBytes(rawJSON, "traceId").String()
		if isStopChunkWithoutUsage(rawJSON) && traceID != "" {
			rememberStopWithoutUsage(traceID)
			continue
		}
		if traceID != "" {
			if _, ok := stopChunkWithoutUsage.Load(traceID); ok && hasUsageMetadata(rawJSON) {
				stopChunkWithoutUsage.Delete(traceID)
				continue
			}
		}

		cleaned, changed := StripUsageMetadataFromJSON(rawJSON)
		if !changed {
			continue
		}
		var rebuilt []byte
		rebuilt = append(rebuilt, before...)
		rebuilt = append(rebuilt, []byte("data:")...)
		if len(cleaned) > 0 {
			rebuilt = append(rebuilt, ' ')
			rebuilt = append(rebuilt, cleaned...)
		}
		lines[idx] = rebuilt
		modified = true
	}
	if !modified {
		if !foundData {
			// Handle payloads that are raw JSON without SSE data: prefix.
			trimmed := bytes.TrimSpace(payload)
			cleaned, changed := StripUsageMetadataFromJSON(trimmed)
			if !changed {
				return payload
			}
			return cleaned
		}
		return payload
	}
	return bytes.Join(lines, []byte("\n"))
}

// StripUsageMetadataFromJSON drops usageMetadata unless finishReason is present (terminal).
// It handles both formats:
// - Aistudio: candidates.0.finishReason
// - Antigravity: response.candidates.0.finishReason
func StripUsageMetadataFromJSON(rawJSON []byte) ([]byte, bool) {
	jsonBytes := bytes.TrimSpace(rawJSON)
	if len(jsonBytes) == 0 || !gjson.ValidBytes(jsonBytes) {
		return rawJSON, false
	}

	// Check for finishReason in both aistudio and antigravity formats
	finishReason := gjson.GetBytes(jsonBytes, "candidates.0.finishReason")
	if !finishReason.Exists() {
		finishReason = gjson.GetBytes(jsonBytes, "response.candidates.0.finishReason")
	}
	terminalReason := finishReason.Exists() && strings.TrimSpace(finishReason.String()) != ""

	usageMetadata := gjson.GetBytes(jsonBytes, "usageMetadata")
	if !usageMetadata.Exists() {
		usageMetadata = gjson.GetBytes(jsonBytes, "response.usageMetadata")
	}

	// Terminal chunk: keep as-is.
	if terminalReason {
		return rawJSON, false
	}

	// Nothing to strip
	if !usageMetadata.Exists() {
		return rawJSON, false
	}

	// Remove usageMetadata from both possible locations
	cleaned := jsonBytes
	var changed bool

	if usageMetadata = gjson.GetBytes(cleaned, "usageMetadata"); usageMetadata.Exists() {
		// Rename usageMetadata to cpaUsageMetadata in the message_start event of Claude
		cleaned, _ = sjson.SetRawBytes(cleaned, "cpaUsageMetadata", []byte(usageMetadata.Raw))
		cleaned, _ = sjson.DeleteBytes(cleaned, "usageMetadata")
		changed = true
	}

	if usageMetadata = gjson.GetBytes(cleaned, "response.usageMetadata"); usageMetadata.Exists() {
		// Rename usageMetadata to cpaUsageMetadata in the message_start event of Claude
		cleaned, _ = sjson.SetRawBytes(cleaned, "response.cpaUsageMetadata", []byte(usageMetadata.Raw))
		cleaned, _ = sjson.DeleteBytes(cleaned, "response.usageMetadata")
		changed = true
	}

	return cleaned, changed
}

func hasUsageMetadata(jsonBytes []byte) bool {
	if len(jsonBytes) == 0 || !gjson.ValidBytes(jsonBytes) {
		return false
	}
	if gjson.GetBytes(jsonBytes, "usageMetadata").Exists() {
		return true
	}
	if gjson.GetBytes(jsonBytes, "response.usageMetadata").Exists() {
		return true
	}
	return false
}

func isStopChunkWithoutUsage(jsonBytes []byte) bool {
	if len(jsonBytes) == 0 || !gjson.ValidBytes(jsonBytes) {
		return false
	}
	finishReason := gjson.GetBytes(jsonBytes, "candidates.0.finishReason")
	if !finishReason.Exists() {
		finishReason = gjson.GetBytes(jsonBytes, "response.candidates.0.finishReason")
	}
	trimmed := strings.TrimSpace(finishReason.String())
	if !finishReason.Exists() || trimmed == "" {
		return false
	}
	return !hasUsageMetadata(jsonBytes)
}

func JSONPayload(line []byte) []byte {
	return jsonPayload(line)
}

func jsonPayload(line []byte) []byte {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("event:")) {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		trimmed = bytes.TrimSpace(trimmed[len("data:"):])
	}
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	return trimmed
}
