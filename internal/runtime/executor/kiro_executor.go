package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	kiroauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	kiroclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/kiro/claude"
	kirocommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/kiro/common"
	kiroopenai "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/kiro/openai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const (
	// Kiro API common constants
	kiroContentType  = "application/json"
	kiroAcceptStream = "*/*"

	// Event Stream frame size constants for boundary protection
	// AWS Event Stream binary format: prelude (12 bytes) + headers + payload + message_crc (4 bytes)
	// Prelude consists of: total_length (4) + headers_length (4) + prelude_crc (4)
	minEventStreamFrameSize = 16       // Minimum: 4(total_len) + 4(headers_len) + 4(prelude_crc) + 4(message_crc)
	maxEventStreamMsgSize   = 10 << 20 // Maximum message length: 10MB

	// Event Stream error type constants
	ErrStreamFatal     = "fatal"     // Connection/authentication errors, not recoverable
	ErrStreamMalformed = "malformed" // Format errors, data cannot be parsed

	// kiroIDEAgentMode is the agent mode header value for Kiro IDE requests
	kiroIDEAgentMode = "vibe"

	// Socket retry configuration constants
	// Maximum number of retry attempts for socket/network errors
	kiroSocketMaxRetries = 3
	// Base delay between retry attempts (uses exponential backoff: delay * 2^attempt)
	kiroSocketBaseRetryDelay = 1 * time.Second
	// Maximum delay between retry attempts (cap for exponential backoff)
	kiroSocketMaxRetryDelay = 30 * time.Second
	// First token timeout for streaming responses (how long to wait for first response)
	kiroFirstTokenTimeout = 15 * time.Second
	// Streaming read timeout (how long to wait between chunks)
	kiroStreamingReadTimeout = 300 * time.Second
)

// retryableHTTPStatusCodes defines HTTP status codes that are considered retryable.
// Based on kiro2Api reference: 502 (Bad Gateway), 503 (Service Unavailable), 504 (Gateway Timeout)
var retryableHTTPStatusCodes = map[int]bool{
	502: true, // Bad Gateway - upstream server error
	503: true, // Service Unavailable - server temporarily overloaded
	504: true, // Gateway Timeout - upstream server timeout
}

// Real-time usage estimation configuration
// These control how often usage updates are sent during streaming
var (
	usageUpdateCharThreshold = 5000             // Send usage update every 5000 characters
	usageUpdateTimeInterval  = 15 * time.Second // Or every 15 seconds, whichever comes first
)

// endpointAliases maps user preference values to canonical endpoint names.
var endpointAliases = map[string]string{
	"codewhisperer": "codewhisperer",
	"ide":           "codewhisperer",
	"amazonq":       "amazonq",
	"q":             "amazonq",
	"cli":           "amazonq",
}

func enqueueTranslatedSSE(out chan<- cliproxyexecutor.StreamChunk, chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
}

type KiroExecutor struct {
	cfg          *config.Config
	refreshMu    sync.Mutex // Serializes token refresh operations to prevent race conditions
	profileArnMu sync.Mutex // Serializes profileArn fetches to prevent concurrent map writes
}

// buildKiroPayloadForFormat builds the Kiro API payload based on the source format.
// This is critical because OpenAI and Claude formats have different tool structures:
// - OpenAI: tools[].function.name, tools[].function.description
// - Claude: tools[].name, tools[].description
// headers parameter allows checking Anthropic-Beta header for thinking mode detection.
// Returns the serialized JSON payload and a boolean indicating whether thinking mode was injected.
func buildKiroPayloadForFormat(body []byte, modelID, profileArn, origin string, isAgentic, isChatOnly bool, sourceFormat sdktranslator.Format, headers http.Header) ([]byte, bool) {
	switch sourceFormat.String() {
	case "openai":
		log.Debugf("kiro: using OpenAI payload builder for source format: %s", sourceFormat.String())
		return kiroopenai.BuildKiroPayloadFromOpenAI(body, modelID, profileArn, origin, isAgentic, isChatOnly, headers, nil)
	case "kiro":
		// Body is already in Kiro format — pass through directly
		log.Debugf("kiro: body already in Kiro format, passing through directly")
		return body, false
	default:
		// Default to Claude format
		log.Debugf("kiro: using Claude payload builder for source format: %s", sourceFormat.String())
		return kiroclaude.BuildKiroPayload(body, modelID, profileArn, origin, isAgentic, isChatOnly, headers, nil)
	}
}

// NewKiroExecutor creates a new Kiro executor instance.
func NewKiroExecutor(cfg *config.Config) *KiroExecutor {
	return &KiroExecutor{cfg: cfg}
}

// Identifier returns the unique identifier for this executor.
func (e *KiroExecutor) Identifier() string { return "kiro" }

// applyDynamicFingerprint applies account-specific fingerprint headers to the request.
func applyDynamicFingerprint(req *http.Request, auth *cliproxyauth.Auth) {
	accountKey := getAccountKey(auth)
	fp := kiroauth.GlobalFingerprintManager().GetFingerprint(accountKey)

	req.Header.Set("User-Agent", fp.BuildUserAgent())
	req.Header.Set("X-Amz-User-Agent", fp.BuildAmzUserAgent())
	req.Header.Set("x-amzn-kiro-agent-mode", kiroIDEAgentMode)
	req.Header.Set("x-amzn-codewhisperer-optout", "true")

	keyPrefix := accountKey
	if len(keyPrefix) > 8 {
		keyPrefix = keyPrefix[:8]
	}
	log.Debugf("kiro: using dynamic fingerprint for account %s (SDK:%s, OS:%s/%s, Kiro:%s)",
		keyPrefix+"...", fp.StreamingSDKVersion, fp.OSType, fp.OSVersion, fp.KiroVersion)
}

// PrepareRequest prepares the HTTP request before execution.
func (e *KiroExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	accessToken, _ := kiroCredentials(auth)
	if strings.TrimSpace(accessToken) == "" {
		return statusErr{code: http.StatusUnauthorized, msg: "missing access token"}
	}

	// Apply dynamic fingerprint-based headers
	applyDynamicFingerprint(req, auth)

	req.Header.Set("Amz-Sdk-Request", "attempt=1; max=3")
	req.Header.Set("Amz-Sdk-Invocation-Id", uuid.New().String())
	req.Header.Set("Authorization", "Bearer "+accessToken)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects Kiro credentials into the request and executes it.
func (e *KiroExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("kiro executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if errPrepare := e.PrepareRequest(httpReq, auth); errPrepare != nil {
		return nil, errPrepare
	}
	httpClient := newKiroHTTPClientWithPooling(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// getAccountKey returns a stable account key for fingerprint lookup and rate limiting.
// Fallback order:
// 1) client_id / refresh_token (best account identity)
// 2) auth.ID (stable local auth record)
// 3) profile_arn (stable AWS profile identity)
// 4) access_token (least preferred but deterministic)
// 5) fixed anonymous seed
func getAccountKey(auth *cliproxyauth.Auth) string {
	var clientID, refreshToken, profileArn string
	if auth != nil && auth.Metadata != nil {
		clientID, _ = auth.Metadata["client_id"].(string)
		refreshToken, _ = auth.Metadata["refresh_token"].(string)
		profileArn, _ = auth.Metadata["profile_arn"].(string)
	}
	if clientID != "" || refreshToken != "" {
		return kiroauth.GetAccountKey(clientID, refreshToken)
	}
	if auth != nil && auth.ID != "" {
		return kiroauth.GenerateAccountKey(auth.ID)
	}
	if profileArn != "" {
		return kiroauth.GenerateAccountKey(profileArn)
	}
	if accessToken, _ := kiroCredentials(auth); accessToken != "" {
		return kiroauth.GenerateAccountKey(accessToken)
	}
	return kiroauth.GenerateAccountKey("kiro-anonymous")
}

// getAuthValue looks up a value by key in auth Metadata, then Attributes.
func getAuthValue(auth *cliproxyauth.Auth, key string) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata[key].(string); ok && v != "" {
			return strings.ToLower(strings.TrimSpace(v))
		}
	}
	if auth.Attributes != nil {
		if v := auth.Attributes[key]; v != "" {
			return strings.ToLower(strings.TrimSpace(v))
		}
	}
	return ""
}

// Execute sends the request to Kiro API and returns the response.
// Supports automatic token refresh on 401/403 errors.
func (e *KiroExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	accessToken, profileArn := kiroCredentials(auth)
	if accessToken == "" {
		return resp, fmt.Errorf("kiro: access token not found in auth")
	}

	// Rate limiting: get token key for tracking
	tokenKey := getAccountKey(auth)
	rateLimiter := kiroauth.GetGlobalRateLimiter()
	cooldownMgr := kiroauth.GetGlobalCooldownManager()

	// Check if token is in cooldown period
	if cooldownMgr.IsInCooldown(tokenKey) {
		remaining := cooldownMgr.GetRemainingCooldown(tokenKey)
		reason := cooldownMgr.GetCooldownReason(tokenKey)
		log.Warnf("kiro: token %s is in cooldown (reason: %s), remaining: %v", tokenKey, reason, remaining)
		return resp, fmt.Errorf("kiro: token is in cooldown for %v (reason: %s)", remaining, reason)
	}

	// Wait for rate limiter before proceeding
	log.Debugf("kiro: waiting for rate limiter for token %s", tokenKey)
	rateLimiter.WaitForToken(tokenKey)
	log.Debugf("kiro: rate limiter cleared for token %s", tokenKey)

	// Check if token is expired before making request (covers both normal and web_search paths)
	if e.isTokenExpired(accessToken) {
		log.Infof("kiro: access token expired, attempting recovery")

		// 方案 B: 先尝试从文件重新加载 token（后台刷新器可能已更新文件）
		reloadedAuth, reloadErr := e.reloadAuthFromFile(auth)
		if reloadErr == nil && reloadedAuth != nil {
			// 文件中有更新的 token，使用它
			auth = reloadedAuth
			accessToken, profileArn = kiroCredentials(auth)
			log.Infof("kiro: recovered token from file (background refresh), expires_at: %v", auth.Metadata["expires_at"])
		} else {
			// 文件中的 token 也过期了，执行主动刷新
			log.Debugf("kiro: file reload failed (%v), attempting active refresh", reloadErr)
			refreshedAuth, refreshErr := e.Refresh(ctx, auth)
			if refreshErr != nil {
				log.Warnf("kiro: pre-request token refresh failed: %v", refreshErr)
			} else if refreshedAuth != nil {
				auth = refreshedAuth
				// Persist the refreshed auth to file so subsequent requests use it
				if persistErr := e.persistRefreshedAuth(auth); persistErr != nil {
					log.Warnf("kiro: failed to persist refreshed auth: %v", persistErr)
				}
				accessToken, profileArn = kiroCredentials(auth)
				log.Infof("kiro: token refreshed successfully before request")
			}
		}
	}

	// Check for pure web_search request
	// Route to MCP endpoint instead of normal Kiro API
	if kiroclaude.HasWebSearchTool(req.Payload) {
		log.Infof("kiro: detected pure web_search request (non-stream), routing to MCP endpoint")
		return e.handleWebSearch(ctx, auth, req, opts, accessToken, profileArn)
	}

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("kiro")
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)

	kiroModelID := e.mapModelToKiro(req.Model)

	// Fetch profileArn if missing (for imported accounts from Kiro IDE)
	if profileArn == "" {
		if fetched := e.fetchAndSaveProfileArn(ctx, auth, accessToken); fetched != "" {
			profileArn = fetched
		}
	}

	// Determine agentic mode and effective profile ARN using helper functions
	isAgentic, isChatOnly := determineAgenticMode(req.Model)
	effectiveProfileArn := getEffectiveProfileArnWithWarning(auth, profileArn)

	// Execute with retry on 401/403 and 429 (quota exhausted)
	// Note: currentOrigin and kiroPayload are built inside executeWithRetry for each endpoint
	resp, err = e.executeWithRetry(ctx, auth, req, opts, accessToken, effectiveProfileArn, nil, body, from, to, reporter, "", kiroModelID, isAgentic, isChatOnly, tokenKey)
	return resp, err
}

// executeWithRetry performs the actual HTTP request with automatic retry on auth errors.
// Supports automatic fallback between endpoints with different quotas:
// - Amazon Q endpoint (CLI origin) uses Amazon Q Developer quota
// - CodeWhisperer endpoint (AI_EDITOR origin) uses Kiro IDE quota
// Also supports multi-endpoint fallback similar to Antigravity implementation.
// tokenKey is used for rate limiting and cooldown tracking.
func (e *KiroExecutor) executeWithRetry(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, accessToken, profileArn string, kiroPayload, body []byte, from, to sdktranslator.Format, reporter *helps.UsageReporter, currentOrigin, kiroModelID string, isAgentic, isChatOnly bool, tokenKey string) (cliproxyexecutor.Response, error) {
	var resp cliproxyexecutor.Response
	maxRetries := 2 // Allow retries for token refresh + endpoint fallback
	rateLimiter := kiroauth.GetGlobalRateLimiter()
	cooldownMgr := kiroauth.GetGlobalCooldownManager()
	endpointConfigs := getKiroEndpointConfigs(auth)
	var last429Err error

	for endpointIdx := range endpointConfigs {
		endpointConfig := endpointConfigs[endpointIdx]
		url := endpointConfig.URL
		// Use this endpoint's compatible Origin (critical for avoiding 403 errors)
		currentOrigin = endpointConfig.Origin

		// Rebuild payload with the correct origin for this endpoint
		// Each endpoint requires its matching Origin value in the request body
		kiroPayload, _ = buildKiroPayloadForFormat(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly, from, opts.Headers)

		log.Debugf("kiro: trying endpoint %d/%d: %s (Name: %s, Origin: %s)",
			endpointIdx+1, len(endpointConfigs), url, endpointConfig.Name, currentOrigin)

		for attempt := 0; attempt <= maxRetries; attempt++ {
			// Apply human-like delay before first request (not on retries)
			// This mimics natural user behavior patterns
			if attempt == 0 && endpointIdx == 0 {
				kiroauth.ApplyHumanLikeDelay()
			}

			httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(kiroPayload))
			if err != nil {
				return resp, err
			}

			httpReq.Header.Set("Content-Type", kiroContentType)
			httpReq.Header.Set("Accept", kiroAcceptStream)
			// Only set X-Amz-Target if specified (Q endpoint doesn't require it)
			if endpointConfig.AmzTarget != "" {
				httpReq.Header.Set("X-Amz-Target", endpointConfig.AmzTarget)
			}
			// Kiro-specific headers
			httpReq.Header.Set("x-amzn-kiro-agent-mode", kiroIDEAgentMode)
			httpReq.Header.Set("x-amzn-codewhisperer-optout", "true")

			// Apply dynamic fingerprint-based headers
			applyDynamicFingerprint(httpReq, auth)

			httpReq.Header.Set("Amz-Sdk-Request", "attempt=1; max=3")
			httpReq.Header.Set("Amz-Sdk-Invocation-Id", uuid.New().String())

			// Bearer token authentication for all auth types (Builder ID, IDC, social, etc.)
			httpReq.Header.Set("Authorization", "Bearer "+accessToken)

			var attrs map[string]string
			if auth != nil {
				attrs = auth.Attributes
			}
			util.ApplyCustomHeadersFromAttrs(httpReq, attrs)

			helps.RecordUpstreamRequest(ctx, e.cfg, auth, e.Identifier(), http.MethodPost, url, httpReq.Header.Clone(), kiroPayload)

			// Timeout 0: after the upstream connection is established, request
			// lifetime is governed by context cancellation only (AGENTS.md).
			httpClient := newKiroHTTPClientWithPooling(ctx, e.cfg, auth, 0)
			httpResp, err := helps.DoStream(ctx, e.cfg, helps.UpstreamRequest{
				Provider:       e.Identifier(),
				Auth:           auth,
				Method:         http.MethodPost,
				URL:            url,
				Headers:        httpReq.Header,
				Body:           kiroPayload,
				Client:         httpClient,
				SkipRequestLog: true,
			})
			if err != nil {
				if ue, ok := err.(helps.UpstreamStatusError); ok {
					httpResp = &http.Response{
						StatusCode: ue.Code,
						Header:     make(http.Header),
						Body:       io.NopCloser(bytes.NewReader([]byte(ue.Msg))),
					}
				} else {
					// Check for context cancellation first - client disconnected, not a server error
					// Use 499 (Client Closed Request - nginx convention) instead of 500
					if errors.Is(err, context.Canceled) {
						log.Debugf("kiro: request canceled by client (context.Canceled)")
						return resp, statusErr{code: 499, msg: "client canceled request"}
					}

					// Check for context deadline exceeded - request timed out
					// Return 504 Gateway Timeout instead of 500
					if errors.Is(err, context.DeadlineExceeded) {
						log.Debugf("kiro: request timed out (context.DeadlineExceeded)")
						return resp, statusErr{code: http.StatusGatewayTimeout, msg: "upstream request timed out"}
					}

					// Enhanced socket retry: Check if error is retryable (network timeout, connection reset, etc.)
					retryCfg := defaultRetryConfig()
					if isRetryableError(err) && attempt < retryCfg.MaxRetries {
						delay := calculateRetryDelay(attempt, retryCfg)
						logRetryAttempt(attempt, retryCfg.MaxRetries, fmt.Sprintf("socket error: %v", err), delay, endpointConfig.Name)
						time.Sleep(delay)
						continue
					}

					return resp, err
				}
			}

			// Handle 429 errors (quota exhausted) - try next endpoint
			// Each endpoint has its own quota pool, so we can try different endpoints
			if httpResp.StatusCode == 429 {
				respBody, _ := io.ReadAll(httpResp.Body)
				_ = httpResp.Body.Close()
				helps.AppendAPIResponseChunk(ctx, e.cfg, respBody)

				// Record failure and set cooldown for 429
				rateLimiter.MarkTokenFailed(tokenKey)
				cooldownDuration := kiroauth.CalculateCooldownFor429(attempt)
				cooldownMgr.SetCooldown(tokenKey, cooldownDuration, kiroauth.CooldownReason429)
				log.Warnf("kiro: rate limit hit (429), token %s set to cooldown for %v", tokenKey, cooldownDuration)

				// Preserve last 429 so callers can correctly backoff when all endpoints are exhausted
				last429Err = statusErr{code: httpResp.StatusCode, msg: string(respBody)}

				log.Warnf("kiro: %s endpoint quota exhausted (429), will try next endpoint, body: %s",
					endpointConfig.Name, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody))

				// Break inner retry loop to try next endpoint (which has different quota)
				break
			}

			// Handle 5xx server errors with exponential backoff retry
			// Enhanced: Use retryConfig for consistent retry behavior
			if httpResp.StatusCode >= 500 && httpResp.StatusCode < 600 {
				respBody, _ := io.ReadAll(httpResp.Body)
				_ = httpResp.Body.Close()
				helps.AppendAPIResponseChunk(ctx, e.cfg, respBody)

				retryCfg := defaultRetryConfig()
				// Check if this specific 5xx code is retryable (502, 503, 504)
				if isRetryableHTTPStatus(httpResp.StatusCode) && attempt < retryCfg.MaxRetries {
					delay := calculateRetryDelay(attempt, retryCfg)
					logRetryAttempt(attempt, retryCfg.MaxRetries, fmt.Sprintf("HTTP %d", httpResp.StatusCode), delay, endpointConfig.Name)
					time.Sleep(delay)
					continue
				} else if attempt < maxRetries {
					// Fallback for other 5xx errors (500, 501, etc.)
					backoff := min(time.Duration(1<<attempt)*time.Second, 30*time.Second)
					log.Warnf("kiro: server error %d, retrying in %v (attempt %d/%d)", httpResp.StatusCode, backoff, attempt+1, maxRetries)
					time.Sleep(backoff)
					continue
				}
				log.Errorf("kiro: server error %d after %d retries", httpResp.StatusCode, maxRetries)
				return resp, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
			}

			// Handle 401 errors with token refresh and retry
			// 401 = Unauthorized (token expired/invalid) - refresh token
			if httpResp.StatusCode == 401 {
				respBody, _ := io.ReadAll(httpResp.Body)
				_ = httpResp.Body.Close()
				helps.AppendAPIResponseChunk(ctx, e.cfg, respBody)

				log.Warnf("kiro: received 401 error, attempting token refresh")
				refreshedAuth, refreshErr := e.Refresh(ctx, auth)
				if refreshErr != nil {
					log.Errorf("kiro: token refresh failed: %v", refreshErr)
					return resp, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
				}

				if refreshedAuth != nil {
					auth = refreshedAuth
					// Persist the refreshed auth to file so subsequent requests use it
					if persistErr := e.persistRefreshedAuth(auth); persistErr != nil {
						log.Warnf("kiro: failed to persist refreshed auth: %v", persistErr)
						// Continue anyway - the token is valid for this request
					}
					accessToken, profileArn = kiroCredentials(auth)
					// Rebuild payload with new profile ARN if changed
					kiroPayload, _ = buildKiroPayloadForFormat(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly, from, opts.Headers)
					if attempt < maxRetries {
						log.Infof("kiro: token refreshed successfully, retrying request (attempt %d/%d)", attempt+1, maxRetries+1)
						continue
					}
					log.Infof("kiro: token refreshed successfully, no retries remaining")
				}

				log.Warnf("kiro request error, status: 401, body: %s", helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody))
				return resp, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
			}

			// Handle 402 errors - Monthly Limit Reached
			if httpResp.StatusCode == 402 {
				respBody, _ := io.ReadAll(httpResp.Body)
				_ = httpResp.Body.Close()
				helps.AppendAPIResponseChunk(ctx, e.cfg, respBody)

				log.Warnf("kiro: received 402 (monthly limit). Upstream body: %s", string(respBody))

				// Return upstream error body directly
				return resp, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
			}

			// Handle 403 errors - Access Denied / Token Expired
			// Do NOT switch endpoints for 403 errors
			if httpResp.StatusCode == 403 {
				respBody, _ := io.ReadAll(httpResp.Body)
				_ = httpResp.Body.Close()
				helps.AppendAPIResponseChunk(ctx, e.cfg, respBody)

				// Log the 403 error details for debugging
				log.Warnf("kiro: received 403 error (attempt %d/%d), body: %s", attempt+1, maxRetries+1, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody))

				respBodyStr := string(respBody)

				// Check for SUSPENDED status - return immediately without retry
				if strings.Contains(respBodyStr, "SUSPENDED") || strings.Contains(respBodyStr, "TEMPORARILY_SUSPENDED") {
					// Set long cooldown for suspended accounts
					rateLimiter.CheckAndMarkSuspended(tokenKey, respBodyStr)
					cooldownMgr.SetCooldown(tokenKey, kiroauth.LongCooldown, kiroauth.CooldownReasonSuspended)
					log.Errorf("kiro: account is suspended, token %s set to cooldown for %v", tokenKey, kiroauth.LongCooldown)
					return resp, statusErr{code: httpResp.StatusCode, msg: "account suspended: " + string(respBody)}
				}

				// Check if this looks like a token-related 403 (some APIs return 403 for expired tokens)
				isTokenRelated := strings.Contains(respBodyStr, "token") ||
					strings.Contains(respBodyStr, "expired") ||
					strings.Contains(respBodyStr, "invalid") ||
					strings.Contains(respBodyStr, "unauthorized")

				if isTokenRelated && attempt < maxRetries {
					log.Warnf("kiro: 403 appears token-related, attempting token refresh")
					refreshedAuth, refreshErr := e.Refresh(ctx, auth)
					if refreshErr != nil {
						log.Errorf("kiro: token refresh failed: %v", refreshErr)
						// Token refresh failed - return error immediately
						return resp, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
					}
					if refreshedAuth != nil {
						auth = refreshedAuth
						// Persist the refreshed auth to file so subsequent requests use it
						if persistErr := e.persistRefreshedAuth(auth); persistErr != nil {
							log.Warnf("kiro: failed to persist refreshed auth: %v", persistErr)
							// Continue anyway - the token is valid for this request
						}
						accessToken, profileArn = kiroCredentials(auth)
						kiroPayload, _ = buildKiroPayloadForFormat(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly, from, opts.Headers)
						log.Infof("kiro: token refreshed for 403, retrying request")
						continue
					}
				}

				// For non-token 403 or after max retries, return error immediately
				// Do NOT switch endpoints for 403 errors
				log.Warnf("kiro: 403 error, returning immediately (no endpoint switch)")
				return resp, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
			}

			if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
				b, _ := io.ReadAll(httpResp.Body)
				helps.AppendAPIResponseChunk(ctx, e.cfg, b)
				log.Debugf("kiro request error, status: %d, body: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
				err = statusErr{code: httpResp.StatusCode, msg: string(b)}
				if errClose := httpResp.Body.Close(); errClose != nil {
					log.Errorf("response body close error: %v", errClose)
				}
				return resp, err
			}

			defer func() {
				if errClose := httpResp.Body.Close(); errClose != nil {
					log.Errorf("response body close error: %v", errClose)
				}
			}()

			content, toolUses, usageInfo, stopReason, err := e.parseEventStream(httpResp.Body)
			if err != nil {
				helps.RecordAPIResponseError(ctx, e.cfg, err)
				return resp, err
			}

			// Fallback for usage if missing from upstream

			// 1. Estimate InputTokens if missing
			if usageInfo.InputTokens == 0 {
				if enc, encErr := helps.TokenizerForModel(req.Model); encErr == nil {
					if inp, countErr := helps.CountOpenAIChatTokens(enc, opts.OriginalRequest); countErr == nil {
						usageInfo.InputTokens = inp
					}
				}
			}

			// 2. Estimate OutputTokens if missing and content is available
			if usageInfo.OutputTokens == 0 && len(content) > 0 {
				// Use tiktoken for more accurate output token calculation
				if enc, encErr := helps.TokenizerForModel(req.Model); encErr == nil {
					if tokenCount, countErr := enc.Count(content); countErr == nil {
						usageInfo.OutputTokens = int64(tokenCount)
					}
				}
				// Fallback to character count estimation if tiktoken fails
				if usageInfo.OutputTokens == 0 {
					usageInfo.OutputTokens = int64(len(content) / 4)
					if usageInfo.OutputTokens == 0 {
						usageInfo.OutputTokens = 1
					}
				}
			}

			// 3. Update TotalTokens
			usageInfo.TotalTokens = usageInfo.InputTokens + usageInfo.OutputTokens

			helps.AppendAPIResponseChunk(ctx, e.cfg, []byte(content))
			reporter.Publish(ctx, usageInfo)
			reporter.EnsurePublished(ctx)

			// Record success for rate limiting
			rateLimiter.MarkTokenSuccess(tokenKey)
			log.Debugf("kiro: request successful, token %s marked as success", tokenKey)

			// Build response in Claude format for Kiro translator
			// stopReason is extracted from upstream response by parseEventStream
			requestedModel := helps.PayloadRequestedModel(opts, req.Model)
			kiroResponse := kiroclaude.BuildClaudeResponse(content, toolUses, requestedModel, usageInfo, stopReason)
			out := sdktranslator.TranslateNonStream(ctx, to, from, requestedModel, bytes.Clone(opts.OriginalRequest), body, kiroResponse, nil)
			resp = cliproxyexecutor.Response{Payload: []byte(out)}
			return resp, nil
		}
		// Inner retry loop exhausted for this endpoint, try next endpoint
		// Note: This code is unreachable because all paths in the inner loop
		// either return or continue. Kept as comment for documentation.
	}

	// All endpoints exhausted
	if last429Err != nil {
		return resp, last429Err
	}
	return resp, fmt.Errorf("kiro: all endpoints exhausted")
}

// ExecuteStream handles streaming requests to Kiro API.
// Supports automatic token refresh on 401/403 errors and quota fallback on 429.
func (e *KiroExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	accessToken, profileArn := kiroCredentials(auth)
	if accessToken == "" {
		return nil, fmt.Errorf("kiro: access token not found in auth")
	}

	// Rate limiting: get token key for tracking
	tokenKey := getAccountKey(auth)
	rateLimiter := kiroauth.GetGlobalRateLimiter()
	cooldownMgr := kiroauth.GetGlobalCooldownManager()

	// Check if token is in cooldown period
	if cooldownMgr.IsInCooldown(tokenKey) {
		remaining := cooldownMgr.GetRemainingCooldown(tokenKey)
		reason := cooldownMgr.GetCooldownReason(tokenKey)
		log.Warnf("kiro: token %s is in cooldown (reason: %s), remaining: %v", tokenKey, reason, remaining)
		return nil, fmt.Errorf("kiro: token is in cooldown for %v (reason: %s)", remaining, reason)
	}

	// Wait for rate limiter before proceeding
	log.Debugf("kiro: stream waiting for rate limiter for token %s", tokenKey)
	rateLimiter.WaitForToken(tokenKey)
	log.Debugf("kiro: stream rate limiter cleared for token %s", tokenKey)

	// Check if token is expired before making request (covers both normal and web_search paths)
	if e.isTokenExpired(accessToken) {
		log.Infof("kiro: access token expired, attempting recovery before stream request")

		// 方案 B: 先尝试从文件重新加载 token（后台刷新器可能已更新文件）
		reloadedAuth, reloadErr := e.reloadAuthFromFile(auth)
		if reloadErr == nil && reloadedAuth != nil {
			// 文件中有更新的 token，使用它
			auth = reloadedAuth
			accessToken, profileArn = kiroCredentials(auth)
			log.Infof("kiro: recovered token from file (background refresh) for stream, expires_at: %v", auth.Metadata["expires_at"])
		} else {
			// 文件中的 token 也过期了，执行主动刷新
			log.Debugf("kiro: file reload failed (%v), attempting active refresh for stream", reloadErr)
			refreshedAuth, refreshErr := e.Refresh(ctx, auth)
			if refreshErr != nil {
				log.Warnf("kiro: pre-request token refresh failed: %v", refreshErr)
			} else if refreshedAuth != nil {
				auth = refreshedAuth
				// Persist the refreshed auth to file so subsequent requests use it
				if persistErr := e.persistRefreshedAuth(auth); persistErr != nil {
					log.Warnf("kiro: failed to persist refreshed auth: %v", persistErr)
				}
				accessToken, profileArn = kiroCredentials(auth)
				log.Infof("kiro: token refreshed successfully before stream request")
			}
		}
	}

	// Check for pure web_search request
	// Route to MCP endpoint instead of normal Kiro API
	if kiroclaude.HasWebSearchTool(req.Payload) {
		log.Infof("kiro: detected pure web_search request, routing to MCP endpoint")
		streamWebSearch, errWebSearch := e.handleWebSearchStream(ctx, auth, req, opts, accessToken, profileArn)
		if errWebSearch != nil {
			return nil, errWebSearch
		}
		return &cliproxyexecutor.StreamResult{Chunks: streamWebSearch}, nil
	}

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("kiro")
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)

	kiroModelID := e.mapModelToKiro(req.Model)

	// Fetch profileArn if missing (for imported accounts from Kiro IDE)
	if profileArn == "" {
		if fetched := e.fetchAndSaveProfileArn(ctx, auth, accessToken); fetched != "" {
			profileArn = fetched
		}
	}

	// Determine agentic mode and effective profile ARN using helper functions
	isAgentic, isChatOnly := determineAgenticMode(req.Model)
	effectiveProfileArn := getEffectiveProfileArnWithWarning(auth, profileArn)

	// Execute stream with retry on 401/403 and 429 (quota exhausted)
	// Note: currentOrigin and kiroPayload are built inside executeStreamWithRetry for each endpoint
	streamKiro, errStreamKiro := e.executeStreamWithRetry(ctx, auth, req, opts, accessToken, effectiveProfileArn, nil, body, from, reporter, "", kiroModelID, isAgentic, isChatOnly, tokenKey)
	if errStreamKiro != nil {
		return nil, errStreamKiro
	}
	return &cliproxyexecutor.StreamResult{Chunks: streamKiro}, nil
}

// executeStreamWithRetry performs the streaming HTTP request with automatic retry on auth errors.
// Supports automatic fallback between endpoints with different quotas:
// - Amazon Q endpoint (CLI origin) uses Amazon Q Developer quota
// - CodeWhisperer endpoint (AI_EDITOR origin) uses Kiro IDE quota
// Also supports multi-endpoint fallback similar to Antigravity implementation.
// tokenKey is used for rate limiting and cooldown tracking.
func (e *KiroExecutor) executeStreamWithRetry(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, accessToken, profileArn string, kiroPayload, body []byte, from sdktranslator.Format, reporter *helps.UsageReporter, currentOrigin, kiroModelID string, isAgentic, isChatOnly bool, tokenKey string) (<-chan cliproxyexecutor.StreamChunk, error) {
	maxRetries := 2 // Allow retries for token refresh + endpoint fallback
	rateLimiter := kiroauth.GetGlobalRateLimiter()
	cooldownMgr := kiroauth.GetGlobalCooldownManager()
	endpointConfigs := getKiroEndpointConfigs(auth)
	var last429Err error

	for endpointIdx := range endpointConfigs {
		endpointConfig := endpointConfigs[endpointIdx]
		url := endpointConfig.URL
		// Use this endpoint's compatible Origin (critical for avoiding 403 errors)
		currentOrigin = endpointConfig.Origin

		// Rebuild payload with the correct origin for this endpoint
		// Each endpoint requires its matching Origin value in the request body
		kiroPayload, thinkingEnabled := buildKiroPayloadForFormat(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly, from, opts.Headers)

		log.Debugf("kiro: stream trying endpoint %d/%d: %s (Name: %s, Origin: %s)",
			endpointIdx+1, len(endpointConfigs), url, endpointConfig.Name, currentOrigin)

		for attempt := 0; attempt <= maxRetries; attempt++ {
			// Apply human-like delay before first streaming request (not on retries)
			// This mimics natural user behavior patterns
			// Note: Delay is NOT applied during streaming response - only before initial request
			if attempt == 0 && endpointIdx == 0 {
				kiroauth.ApplyHumanLikeDelay()
			}

			httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(kiroPayload))
			if err != nil {
				return nil, err
			}

			httpReq.Header.Set("Content-Type", kiroContentType)
			httpReq.Header.Set("Accept", kiroAcceptStream)
			// Only set X-Amz-Target if specified (Q endpoint doesn't require it)
			if endpointConfig.AmzTarget != "" {
				httpReq.Header.Set("X-Amz-Target", endpointConfig.AmzTarget)
			}
			// Kiro-specific headers
			httpReq.Header.Set("x-amzn-kiro-agent-mode", kiroIDEAgentMode)
			httpReq.Header.Set("x-amzn-codewhisperer-optout", "true")

			// Apply dynamic fingerprint-based headers
			applyDynamicFingerprint(httpReq, auth)

			httpReq.Header.Set("Amz-Sdk-Request", "attempt=1; max=3")
			httpReq.Header.Set("Amz-Sdk-Invocation-Id", uuid.New().String())

			// Bearer token authentication for all auth types (Builder ID, IDC, social, etc.)
			httpReq.Header.Set("Authorization", "Bearer "+accessToken)

			var attrs map[string]string
			if auth != nil {
				attrs = auth.Attributes
			}
			util.ApplyCustomHeadersFromAttrs(httpReq, attrs)

			helps.RecordUpstreamRequest(ctx, e.cfg, auth, e.Identifier(), http.MethodPost, url, httpReq.Header.Clone(), kiroPayload)

			httpClient := newKiroHTTPClientWithPooling(ctx, e.cfg, auth, 0)
			httpResp, err := helps.DoStream(ctx, e.cfg, helps.UpstreamRequest{
				Provider:       e.Identifier(),
				Auth:           auth,
				Method:         http.MethodPost,
				URL:            url,
				Headers:        httpReq.Header,
				Body:           kiroPayload,
				Client:         httpClient,
				SkipRequestLog: true,
			})
			if err != nil {
				if ue, ok := err.(helps.UpstreamStatusError); ok {
					httpResp = &http.Response{
						StatusCode: ue.Code,
						Header:     make(http.Header),
						Body:       io.NopCloser(bytes.NewReader([]byte(ue.Msg))),
					}
				} else {
					// Enhanced socket retry for streaming: Check if error is retryable (network timeout, connection reset, etc.)
					retryCfg := defaultRetryConfig()
					if isRetryableError(err) && attempt < retryCfg.MaxRetries {
						delay := calculateRetryDelay(attempt, retryCfg)
						logRetryAttempt(attempt, retryCfg.MaxRetries, fmt.Sprintf("stream socket error: %v", err), delay, endpointConfig.Name)
						time.Sleep(delay)
						continue
					}

					return nil, err
				}
			}

			// Handle 429 errors (quota exhausted) - try next endpoint
			// Each endpoint has its own quota pool, so we can try different endpoints
			if httpResp.StatusCode == 429 {
				respBody, _ := io.ReadAll(httpResp.Body)
				_ = httpResp.Body.Close()
				helps.AppendAPIResponseChunk(ctx, e.cfg, respBody)

				// Record failure and set cooldown for 429
				rateLimiter.MarkTokenFailed(tokenKey)
				cooldownDuration := kiroauth.CalculateCooldownFor429(attempt)
				cooldownMgr.SetCooldown(tokenKey, cooldownDuration, kiroauth.CooldownReason429)
				log.Warnf("kiro: stream rate limit hit (429), token %s set to cooldown for %v", tokenKey, cooldownDuration)

				// Preserve last 429 so callers can correctly backoff when all endpoints are exhausted
				last429Err = statusErr{code: httpResp.StatusCode, msg: string(respBody)}

				log.Warnf("kiro: stream %s endpoint quota exhausted (429), will try next endpoint, body: %s",
					endpointConfig.Name, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody))

				// Break inner retry loop to try next endpoint (which has different quota)
				break
			}

			// Handle 5xx server errors with exponential backoff retry
			// Enhanced: Use retryConfig for consistent retry behavior
			if httpResp.StatusCode >= 500 && httpResp.StatusCode < 600 {
				respBody, _ := io.ReadAll(httpResp.Body)
				_ = httpResp.Body.Close()
				helps.AppendAPIResponseChunk(ctx, e.cfg, respBody)

				retryCfg := defaultRetryConfig()
				// Check if this specific 5xx code is retryable (502, 503, 504)
				if isRetryableHTTPStatus(httpResp.StatusCode) && attempt < retryCfg.MaxRetries {
					delay := calculateRetryDelay(attempt, retryCfg)
					logRetryAttempt(attempt, retryCfg.MaxRetries, fmt.Sprintf("stream HTTP %d", httpResp.StatusCode), delay, endpointConfig.Name)
					time.Sleep(delay)
					continue
				} else if attempt < maxRetries {
					// Fallback for other 5xx errors (500, 501, etc.)
					backoff := min(time.Duration(1<<attempt)*time.Second, 30*time.Second)
					log.Warnf("kiro: stream server error %d, retrying in %v (attempt %d/%d)", httpResp.StatusCode, backoff, attempt+1, maxRetries)
					time.Sleep(backoff)
					continue
				}
				log.Errorf("kiro: stream server error %d after %d retries", httpResp.StatusCode, maxRetries)
				return nil, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
			}

			// Handle 400 errors - Credential/Validation issues
			// Do NOT switch endpoints - return error immediately
			if httpResp.StatusCode == 400 {
				respBody, _ := io.ReadAll(httpResp.Body)
				_ = httpResp.Body.Close()
				helps.AppendAPIResponseChunk(ctx, e.cfg, respBody)

				log.Warnf("kiro: received 400 error (attempt %d/%d), body: %s", attempt+1, maxRetries+1, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody))

				// 400 errors indicate request validation issues - return immediately without retry
				return nil, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
			}

			// Handle 401 errors with token refresh and retry
			// 401 = Unauthorized (token expired/invalid) - refresh token
			if httpResp.StatusCode == 401 {
				respBody, _ := io.ReadAll(httpResp.Body)
				_ = httpResp.Body.Close()
				helps.AppendAPIResponseChunk(ctx, e.cfg, respBody)

				log.Warnf("kiro: stream received 401 error, attempting token refresh")
				refreshedAuth, refreshErr := e.Refresh(ctx, auth)
				if refreshErr != nil {
					log.Errorf("kiro: token refresh failed: %v", refreshErr)
					return nil, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
				}

				if refreshedAuth != nil {
					auth = refreshedAuth
					// Persist the refreshed auth to file so subsequent requests use it
					if persistErr := e.persistRefreshedAuth(auth); persistErr != nil {
						log.Warnf("kiro: failed to persist refreshed auth: %v", persistErr)
						// Continue anyway - the token is valid for this request
					}
					accessToken, profileArn = kiroCredentials(auth)
					// Rebuild payload with new profile ARN if changed
					kiroPayload, _ = buildKiroPayloadForFormat(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly, from, opts.Headers)
					if attempt < maxRetries {
						log.Infof("kiro: token refreshed successfully, retrying stream request (attempt %d/%d)", attempt+1, maxRetries+1)
						continue
					}
					log.Infof("kiro: token refreshed successfully, no retries remaining")
				}

				log.Warnf("kiro stream error, status: 401, body: %s", string(respBody))
				return nil, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
			}

			// Handle 402 errors - Monthly Limit Reached
			if httpResp.StatusCode == 402 {
				respBody, _ := io.ReadAll(httpResp.Body)
				_ = httpResp.Body.Close()
				helps.AppendAPIResponseChunk(ctx, e.cfg, respBody)

				log.Warnf("kiro: stream received 402 (monthly limit). Upstream body: %s", string(respBody))

				// Return upstream error body directly
				return nil, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
			}

			// Handle 403 errors - Access Denied / Token Expired
			// Do NOT switch endpoints for 403 errors
			if httpResp.StatusCode == 403 {
				respBody, _ := io.ReadAll(httpResp.Body)
				_ = httpResp.Body.Close()
				helps.AppendAPIResponseChunk(ctx, e.cfg, respBody)

				// Log the 403 error details for debugging
				log.Warnf("kiro: stream received 403 error (attempt %d/%d), body: %s", attempt+1, maxRetries+1, string(respBody))

				respBodyStr := string(respBody)

				// Check for SUSPENDED status - return immediately without retry
				if strings.Contains(respBodyStr, "SUSPENDED") || strings.Contains(respBodyStr, "TEMPORARILY_SUSPENDED") {
					// Set long cooldown for suspended accounts
					rateLimiter.CheckAndMarkSuspended(tokenKey, respBodyStr)
					cooldownMgr.SetCooldown(tokenKey, kiroauth.LongCooldown, kiroauth.CooldownReasonSuspended)
					log.Errorf("kiro: stream account is suspended, token %s set to cooldown for %v", tokenKey, kiroauth.LongCooldown)
					return nil, statusErr{code: httpResp.StatusCode, msg: "account suspended: " + string(respBody)}
				}

				// Check if this looks like a token-related 403 (some APIs return 403 for expired tokens)
				isTokenRelated := strings.Contains(respBodyStr, "token") ||
					strings.Contains(respBodyStr, "expired") ||
					strings.Contains(respBodyStr, "invalid") ||
					strings.Contains(respBodyStr, "unauthorized")

				if isTokenRelated && attempt < maxRetries {
					log.Warnf("kiro: 403 appears token-related, attempting token refresh")
					refreshedAuth, refreshErr := e.Refresh(ctx, auth)
					if refreshErr != nil {
						log.Errorf("kiro: token refresh failed: %v", refreshErr)
						// Token refresh failed - return error immediately
						return nil, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
					}
					if refreshedAuth != nil {
						auth = refreshedAuth
						// Persist the refreshed auth to file so subsequent requests use it
						if persistErr := e.persistRefreshedAuth(auth); persistErr != nil {
							log.Warnf("kiro: failed to persist refreshed auth: %v", persistErr)
							// Continue anyway - the token is valid for this request
						}
						accessToken, profileArn = kiroCredentials(auth)
						kiroPayload, _ = buildKiroPayloadForFormat(body, kiroModelID, profileArn, currentOrigin, isAgentic, isChatOnly, from, opts.Headers)
						log.Infof("kiro: token refreshed for 403, retrying stream request")
						continue
					}
				}

				// For non-token 403 or after max retries, return error immediately
				// Do NOT switch endpoints for 403 errors
				log.Warnf("kiro: 403 error, returning immediately (no endpoint switch)")
				return nil, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
			}

			if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
				b, _ := io.ReadAll(httpResp.Body)
				helps.AppendAPIResponseChunk(ctx, e.cfg, b)
				log.Debugf("kiro stream error, status: %d, body: %s", httpResp.StatusCode, string(b))
				if errClose := httpResp.Body.Close(); errClose != nil {
					log.Errorf("response body close error: %v", errClose)
				}
				return nil, statusErr{code: httpResp.StatusCode, msg: string(b)}
			}

			out := make(chan cliproxyexecutor.StreamChunk)

			// Record success immediately since connection was established successfully
			// Streaming errors will be handled separately
			rateLimiter.MarkTokenSuccess(tokenKey)
			log.Debugf("kiro: stream request successful, token %s marked as success", tokenKey)

			go func(resp *http.Response, thinkingEnabled bool) {
				defer close(out)
				defer func() {
					if r := recover(); r != nil {
						log.Errorf("kiro: panic in stream handler: %v", r)
						out <- cliproxyexecutor.StreamChunk{Err: fmt.Errorf("internal error: %v", r)}
					}
				}()
				defer func() {
					if errClose := resp.Body.Close(); errClose != nil {
						log.Errorf("response body close error: %v", errClose)
					}
				}()

				// Kiro API always returns <thinking> tags regardless of request parameters
				// So we always enable thinking parsing for Kiro responses
				log.Debugf("kiro: stream thinkingEnabled = %v (always true for Kiro)", thinkingEnabled)

				e.streamToChannel(ctx, resp.Body, out, from, helps.PayloadRequestedModel(opts, req.Model), opts.OriginalRequest, body, reporter, thinkingEnabled)
			}(httpResp, thinkingEnabled)

			return out, nil
		}
		// Inner retry loop exhausted for this endpoint, try next endpoint
		// Note: This code is unreachable because all paths in the inner loop
		// either return or continue. Kept as comment for documentation.
	}

	// All endpoints exhausted
	if last429Err != nil {
		return nil, last429Err
	}
	return nil, fmt.Errorf("kiro: stream all endpoints exhausted")
}

// kiroCredentials extracts access token and profile ARN from auth.
func kiroCredentials(auth *cliproxyauth.Auth) (accessToken, profileArn string) {
	if auth == nil {
		return "", ""
	}

	// Try Metadata first (wrapper format)
	if auth.Metadata != nil {
		if token, ok := auth.Metadata["access_token"].(string); ok {
			accessToken = token
		}
		if arn, ok := auth.Metadata["profile_arn"].(string); ok {
			profileArn = arn
		}
	}

	// Try Attributes
	if accessToken == "" && auth.Attributes != nil {
		accessToken = auth.Attributes["access_token"]
		profileArn = auth.Attributes["profile_arn"]
	}

	// Try direct fields from flat JSON format (new AWS Builder ID format)
	if accessToken == "" && auth.Metadata != nil {
		if token, ok := auth.Metadata["accessToken"].(string); ok {
			accessToken = token
		}
		if arn, ok := auth.Metadata["profileArn"].(string); ok {
			profileArn = arn
		}
	}

	return accessToken, profileArn
}

// findRealThinkingEndTag finds the real </thinking> end tag, skipping false positives.
// Returns -1 if no real end tag is found.
//
// Real </thinking> tags from Kiro API have specific characteristics:
// - Usually preceded by newline (.\n</thinking>)
// - Usually followed by newline (\n\n)
// - Not inside code blocks or inline code
//
// False positives (discussion text) have characteristics:
// - In the middle of a sentence
// - Preceded by discussion words like "标签", "tag", "returns"
// - Inside code blocks or inline code
//
// Parameters:
// - content: the content to search in
// - alreadyInCodeBlock: whether we're already inside a code block from previous chunks
// - alreadyInInlineCode: whether we're already inside inline code from previous chunks
func findRealThinkingEndTag(content string, alreadyInCodeBlock, alreadyInInlineCode bool) int {
	searchStart := 0
	for {
		endIdx := strings.Index(content[searchStart:], kirocommon.ThinkingEndTag)
		if endIdx < 0 {
			return -1
		}
		endIdx += searchStart // Adjust to absolute position

		textBeforeEnd := content[:endIdx]
		textAfterEnd := content[endIdx+len(kirocommon.ThinkingEndTag):]

		// Check 1: Is it inside inline code?
		// Count backticks in current content and add state from previous chunks
		backtickCount := strings.Count(textBeforeEnd, "`")
		effectiveInInlineCode := alreadyInInlineCode
		if backtickCount%2 == 1 {
			effectiveInInlineCode = !effectiveInInlineCode
		}
		if effectiveInInlineCode {
			log.Debugf("kiro: found </thinking> inside inline code at pos %d, skipping", endIdx)
			searchStart = endIdx + len(kirocommon.ThinkingEndTag)
			continue
		}

		// Check 2: Is it inside a code block?
		// Count fences in current content and add state from previous chunks
		fenceCount := strings.Count(textBeforeEnd, "```")
		altFenceCount := strings.Count(textBeforeEnd, "~~~")
		effectiveInCodeBlock := alreadyInCodeBlock
		if fenceCount%2 == 1 || altFenceCount%2 == 1 {
			effectiveInCodeBlock = !effectiveInCodeBlock
		}
		if effectiveInCodeBlock {
			log.Debugf("kiro: found </thinking> inside code block at pos %d, skipping", endIdx)
			searchStart = endIdx + len(kirocommon.ThinkingEndTag)
			continue
		}

		// Check 3: Real </thinking> tags are usually preceded by newline or at start
		// and followed by newline or at end. Check the format.
		charBeforeTag := byte(0)
		if endIdx > 0 {
			charBeforeTag = content[endIdx-1]
		}
		charAfterTag := byte(0)
		if len(textAfterEnd) > 0 {
			charAfterTag = textAfterEnd[0]
		}

		// Real end tag format: preceded by newline OR end of sentence (. ! ?)
		// and followed by newline OR end of content
		isPrecededByNewlineOrSentenceEnd := charBeforeTag == '\n' || charBeforeTag == '.' ||
			charBeforeTag == '!' || charBeforeTag == '?' || charBeforeTag == 0
		isFollowedByNewlineOrEnd := charAfterTag == '\n' || charAfterTag == 0

		// If the tag has proper formatting (newline before/after), it's likely real
		if isPrecededByNewlineOrSentenceEnd && isFollowedByNewlineOrEnd {
			log.Debugf("kiro: found properly formatted </thinking> at pos %d", endIdx)
			return endIdx
		}

		// Check 4: Is the tag preceded by discussion keywords on the same line?
		lastNewlineIdx := strings.LastIndex(textBeforeEnd, "\n")
		lineBeforeTag := textBeforeEnd
		if lastNewlineIdx >= 0 {
			lineBeforeTag = textBeforeEnd[lastNewlineIdx+1:]
		}
		lineBeforeTagLower := strings.ToLower(lineBeforeTag)

		// Discussion patterns - if found, this is likely discussion text
		discussionPatterns := []string{
			"标签", "返回", "输出", "包含", "使用", "解析", "转换", "生成", // Chinese
			"tag", "return", "output", "contain", "use", "parse", "emit", "convert", "generate", // English
			"<thinking>",    // discussing both tags together
			"`</thinking>`", // explicitly in inline code
		}
		isDiscussion := false
		for _, pattern := range discussionPatterns {
			if strings.Contains(lineBeforeTagLower, pattern) {
				isDiscussion = true
				break
			}
		}
		if isDiscussion {
			log.Debugf("kiro: found </thinking> after discussion text at pos %d, skipping", endIdx)
			searchStart = endIdx + len(kirocommon.ThinkingEndTag)
			continue
		}

		// Check 5: Is there text immediately after on the same line?
		// Real end tags don't have text immediately after on the same line
		if len(textAfterEnd) > 0 && charAfterTag != '\n' && charAfterTag != 0 {
			// Find the next newline
			before, _, ok := strings.Cut(textAfterEnd, "\n")
			var textOnSameLine string
			if ok {
				textOnSameLine = before
			} else {
				textOnSameLine = textAfterEnd
			}
			// If there's non-whitespace text on the same line after the tag, it's discussion
			if strings.TrimSpace(textOnSameLine) != "" {
				log.Debugf("kiro: found </thinking> with text after on same line at pos %d, skipping", endIdx)
				searchStart = endIdx + len(kirocommon.ThinkingEndTag)
				continue
			}
		}

		// Check 6: Is there another <thinking> tag after this </thinking>?
		if strings.Contains(textAfterEnd, kirocommon.ThinkingStartTag) {
			before, _, _ := strings.Cut(textAfterEnd, kirocommon.ThinkingStartTag)
			textBeforeNextStart := before
			nextBacktickCount := strings.Count(textBeforeNextStart, "`")
			nextFenceCount := strings.Count(textBeforeNextStart, "```")
			nextAltFenceCount := strings.Count(textBeforeNextStart, "~~~")

			// If the next <thinking> is NOT in code, then this </thinking> is discussion text
			if nextBacktickCount%2 == 0 && nextFenceCount%2 == 0 && nextAltFenceCount%2 == 0 {
				log.Debugf("kiro: found </thinking> followed by <thinking> at pos %d, likely discussion text, skipping", endIdx)
				searchStart = endIdx + len(kirocommon.ThinkingEndTag)
				continue
			}
		}

		// This looks like a real end tag
		return endIdx
	}
}

// determineAgenticMode determines if the model is an agentic or chat-only variant.
// Returns (isAgentic, isChatOnly) based on model name suffixes.
func determineAgenticMode(model string) (isAgentic, isChatOnly bool) {
	isAgentic = strings.HasSuffix(model, "-agentic")
	isChatOnly = strings.HasSuffix(model, "-chat")
	return isAgentic, isChatOnly
}

// getEffectiveProfileArnWithWarning suppresses profileArn for builder-id and AWS SSO OIDC auth.
// Builder-id users (auth_method == "builder-id") and AWS SSO OIDC users (auth_type == "aws_sso_oidc")
// don't need profileArn — sending it causes 403 errors.
// For all other auth methods (e.g. social auth), profileArn is returned as-is,
// with a warning logged if it is empty.
func getEffectiveProfileArnWithWarning(auth *cliproxyauth.Auth, profileArn string) string {
	if auth != nil && auth.Metadata != nil {
		// Check 1: auth_method field, skip for builder-id only
		if authMethod, ok := auth.Metadata["auth_method"].(string); ok && authMethod == "builder-id" {
			return ""
		}
		// Check 2: auth_type field (from kiro-cli tokens)
		if authType, ok := auth.Metadata["auth_type"].(string); ok && authType == "aws_sso_oidc" {
			return "" // AWS SSO OIDC - don't include profileArn
		}
	}
	// For social auth and IDC, profileArn is required
	if profileArn == "" {
		log.Warnf("kiro: profile ARN not found in auth, API calls may fail")
	}
	return profileArn
}

// mapModelToKiro maps external model names to Kiro model IDs.
// Supports both Kiro and Amazon Q prefixes since they use the same API.
// Agentic variants (-agentic suffix) map to the same backend model IDs.
func (e *KiroExecutor) mapModelToKiro(model string) string {
	modelMap := map[string]string{
		// Amazon Q format (amazonq- prefix) - same API as Kiro
		"amazonq-auto":                       "auto",
		"amazonq-claude-opus-4-6":            "claude-opus-4.6",
		"amazonq-claude-sonnet-4-6":          "claude-sonnet-4.6",
		"amazonq-claude-opus-4-5":            "claude-opus-4.5",
		"amazonq-claude-sonnet-4-5":          "claude-sonnet-4.5",
		"amazonq-claude-sonnet-4-5-20250929": "claude-sonnet-4.5",
		"amazonq-claude-sonnet-4":            "claude-sonnet-4",
		"amazonq-claude-sonnet-4-20250514":   "claude-sonnet-4",
		"amazonq-claude-haiku-4-5":           "claude-haiku-4.5",
		// Kiro format (kiro- prefix) - valid model names that should be preserved
		"kiro-claude-opus-4-6":            "claude-opus-4.6",
		"kiro-claude-sonnet-4-6":          "claude-sonnet-4.6",
		"kiro-claude-opus-4-5":            "claude-opus-4.5",
		"kiro-claude-sonnet-4-5":          "claude-sonnet-4.5",
		"kiro-claude-sonnet-4-5-20250929": "claude-sonnet-4.5",
		"kiro-claude-sonnet-4":            "claude-sonnet-4",
		"kiro-claude-sonnet-4-20250514":   "claude-sonnet-4",
		"kiro-claude-haiku-4-5":           "claude-haiku-4.5",
		"kiro-auto":                       "auto",
		// Native format (no prefix) - used by Kiro IDE directly
		"claude-opus-4-6":            "claude-opus-4.6",
		"claude-opus-4.6":            "claude-opus-4.6",
		"claude-sonnet-4-6":          "claude-sonnet-4.6",
		"claude-sonnet-4.6":          "claude-sonnet-4.6",
		"claude-opus-4-5":            "claude-opus-4.5",
		"claude-opus-4.5":            "claude-opus-4.5",
		"claude-haiku-4-5":           "claude-haiku-4.5",
		"claude-haiku-4.5":           "claude-haiku-4.5",
		"claude-sonnet-4-5":          "claude-sonnet-4.5",
		"claude-sonnet-4-5-20250929": "claude-sonnet-4.5",
		"claude-sonnet-4.5":          "claude-sonnet-4.5",
		"claude-sonnet-4":            "claude-sonnet-4",
		"claude-sonnet-4-20250514":   "claude-sonnet-4",
		"auto":                       "auto",
		// Agentic variants (same backend model IDs, but with special system prompt)
		"claude-opus-4.6-agentic":        "claude-opus-4.6",
		"claude-sonnet-4.6-agentic":      "claude-sonnet-4.6",
		"claude-opus-4.5-agentic":        "claude-opus-4.5",
		"claude-sonnet-4.5-agentic":      "claude-sonnet-4.5",
		"claude-sonnet-4-agentic":        "claude-sonnet-4",
		"claude-haiku-4.5-agentic":       "claude-haiku-4.5",
		"kiro-claude-opus-4-6-agentic":   "claude-opus-4.6",
		"kiro-claude-sonnet-4-6-agentic": "claude-sonnet-4.6",
		"kiro-claude-opus-4-5-agentic":   "claude-opus-4.5",
		"kiro-claude-sonnet-4-5-agentic": "claude-sonnet-4.5",
		"kiro-claude-sonnet-4-agentic":   "claude-sonnet-4",
		"kiro-claude-haiku-4-5-agentic":  "claude-haiku-4.5",
	}
	if kiroID, ok := modelMap[model]; ok {
		return kiroID
	}

	// Smart fallback: try to infer model type from name patterns
	modelLower := strings.ToLower(model)

	// Check for Haiku variants
	if strings.Contains(modelLower, "haiku") {
		log.Debugf("kiro: unknown Haiku model '%s', mapping to claude-haiku-4.5", model)
		return "claude-haiku-4.5"
	}

	// Check for Sonnet variants
	if strings.Contains(modelLower, "sonnet") {
		// Check for specific version patterns
		if strings.Contains(modelLower, "3-7") || strings.Contains(modelLower, "3.7") {
			log.Debugf("kiro: unknown Sonnet 3.7 model '%s', mapping to claude-3-7-sonnet-20250219", model)
			return "claude-3-7-sonnet-20250219"
		}
		if strings.Contains(modelLower, "4-6") || strings.Contains(modelLower, "4.6") {
			log.Debugf("kiro: unknown Sonnet 4.6 model '%s', mapping to claude-sonnet-4.6", model)
			return "claude-sonnet-4.6"
		}
		if strings.Contains(modelLower, "4-5") || strings.Contains(modelLower, "4.5") {
			log.Debugf("kiro: unknown Sonnet 4.5 model '%s', mapping to claude-sonnet-4.5", model)
			return "claude-sonnet-4.5"
		}
		// Default to Sonnet 4
		log.Debugf("kiro: unknown Sonnet model '%s', mapping to claude-sonnet-4", model)
		return "claude-sonnet-4"
	}

	// Check for Opus variants
	if strings.Contains(modelLower, "opus") {
		if strings.Contains(modelLower, "4-6") || strings.Contains(modelLower, "4.6") {
			log.Debugf("kiro: unknown Opus 4.6 model '%s', mapping to claude-opus-4.6", model)
			return "claude-opus-4.6"
		}
		log.Debugf("kiro: unknown Opus model '%s', mapping to claude-opus-4.5", model)
		return "claude-opus-4.5"
	}

	// Final fallback to Sonnet 4.5 (most commonly used model)
	log.Warnf("kiro: unknown model '%s', falling back to claude-sonnet-4.5", model)
	return "claude-sonnet-4.5"
}

func (e *KiroExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	// Use tiktoken for local token counting
	enc, err := helps.TokenizerForModel(req.Model)
	if err != nil {
		log.Warnf("kiro: CountTokens failed to get tokenizer: %v, falling back to estimate", err)
		// Fallback: estimate from payload size (roughly 4 chars per token)
		estimatedTokens := len(req.Payload) / 4
		if estimatedTokens == 0 && len(req.Payload) > 0 {
			estimatedTokens = 1
		}
		return cliproxyexecutor.Response{
			Payload: fmt.Appendf(nil, `{"count":%d}`, estimatedTokens),
		}, nil
	}

	// Try to count tokens from the request payload
	var totalTokens int64

	// Try OpenAI chat format first
	if tokens, countErr := helps.CountOpenAIChatTokens(enc, req.Payload); countErr == nil && tokens > 0 {
		totalTokens = tokens
		log.Debugf("kiro: CountTokens counted %d tokens using OpenAI chat format", totalTokens)
	} else {
		// Fallback: count raw payload tokens
		if tokenCount, countErr := enc.Count(string(req.Payload)); countErr == nil {
			totalTokens = int64(tokenCount)
			log.Debugf("kiro: CountTokens counted %d tokens from raw payload", totalTokens)
		} else {
			// Final fallback: estimate from payload size
			totalTokens = int64(len(req.Payload) / 4)
			if totalTokens == 0 && len(req.Payload) > 0 {
				totalTokens = 1
			}
			log.Debugf("kiro: CountTokens estimated %d tokens from payload size", totalTokens)
		}
	}

	return cliproxyexecutor.Response{
		Payload: fmt.Appendf(nil, `{"count":%d}`, totalTokens),
	}, nil
}

// Refresh refreshes the Kiro OAuth token.
// Supports both AWS Builder ID (SSO OIDC) and Google OAuth (social login).
// Uses mutex to prevent race conditions when multiple concurrent requests try to refresh.
func (e *KiroExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	// Serialize token refresh operations to prevent race conditions
	e.refreshMu.Lock()
	defer e.refreshMu.Unlock()

	var authID string
	if auth != nil {
		authID = auth.ID
	} else {
		authID = "<nil>"
	}
	log.Debugf("kiro executor: refresh called for auth %s", authID)
	if auth == nil {
		return nil, fmt.Errorf("kiro executor: auth is nil")
	}

	// Double-check: After acquiring lock, verify token still needs refresh
	// Another goroutine may have already refreshed while we were waiting
	// NOTE: This check has a design limitation - it reads from the auth object passed in,
	// not from persistent storage. If another goroutine returns a new Auth object (via Clone),
	// this check won't see those updates. The mutex still prevents truly concurrent refreshes,
	// but queued goroutines may still attempt redundant refreshes. This is acceptable as
	// the refresh operation is idempotent and the extra API calls are infrequent.
	if auth.Metadata != nil {
		if lastRefresh, ok := auth.Metadata["last_refresh"].(string); ok {
			if refreshTime, err := time.Parse(time.RFC3339, lastRefresh); err == nil {
				// If token was refreshed within the last 30 seconds, skip refresh
				if time.Since(refreshTime) < 30*time.Second {
					log.Debugf("kiro executor: token was recently refreshed by another goroutine, skipping")
					return auth, nil
				}
			}
		}
		// Also check if expires_at is now in the future with sufficient buffer
		if expiresAt, ok := auth.Metadata["expires_at"].(string); ok {
			if expTime, err := time.Parse(time.RFC3339, expiresAt); err == nil {
				// If token expires more than 20 minutes from now, it's still valid
				if time.Until(expTime) > 20*time.Minute {
					log.Debugf("kiro executor: token is still valid (expires in %v), skipping refresh", time.Until(expTime))
					// CRITICAL FIX: Set NextRefreshAfter to prevent frequent refresh checks
					// Without this, shouldRefresh() will return true again in 30 seconds
					updated := auth.Clone()
					// Set next refresh to 20 minutes before expiry, or at least 30 seconds from now
					nextRefresh := expTime.Add(-20 * time.Minute)
					minNextRefresh := time.Now().Add(30 * time.Second)
					if nextRefresh.Before(minNextRefresh) {
						nextRefresh = minNextRefresh
					}
					updated.NextRefreshAfter = nextRefresh
					log.Debugf("kiro executor: setting NextRefreshAfter to %v (in %v)", nextRefresh.Format(time.RFC3339), time.Until(nextRefresh))
					return updated, nil
				}
			}
		}
	}

	var refreshToken string
	var clientID, clientSecret string
	var authMethod string
	var region, startURL string

	if auth.Metadata != nil {
		if rt, ok := auth.Metadata["refresh_token"].(string); ok {
			refreshToken = rt
		}
		if cid, ok := auth.Metadata["client_id"].(string); ok {
			clientID = cid
		}
		if cs, ok := auth.Metadata["client_secret"].(string); ok {
			clientSecret = cs
		}
		if am, ok := auth.Metadata["auth_method"].(string); ok {
			authMethod = am
		}
		if r, ok := auth.Metadata["region"].(string); ok {
			region = r
		}
		if su, ok := auth.Metadata["start_url"].(string); ok {
			startURL = su
		}
	}

	if refreshToken == "" {
		return nil, fmt.Errorf("kiro executor: refresh token not found")
	}

	var tokenData *kiroauth.KiroTokenData
	var err error

	ssoClient := kiroauth.NewSSOOIDCClient(e.cfg)

	// Use SSO OIDC refresh for AWS Builder ID or IDC, otherwise use Kiro's OAuth refresh endpoint
	switch {
	case clientID != "" && clientSecret != "" && authMethod == "idc" && region != "":
		// IDC refresh with region-specific endpoint
		log.Debugf("kiro executor: using SSO OIDC refresh for IDC (region=%s)", region)
		tokenData, err = ssoClient.RefreshTokenWithRegion(ctx, clientID, clientSecret, refreshToken, region, startURL)
	case clientID != "" && clientSecret != "" && authMethod == "builder-id":
		// Builder ID refresh with default endpoint
		log.Debugf("kiro executor: using SSO OIDC refresh for AWS Builder ID")
		tokenData, err = ssoClient.RefreshToken(ctx, clientID, clientSecret, refreshToken)
	default:
		// Fallback to Kiro's OAuth refresh endpoint (for social auth: Google/GitHub)
		log.Debugf("kiro executor: using Kiro OAuth refresh endpoint")
		oauth := kiroauth.NewKiroOAuth(e.cfg)
		tokenData, err = oauth.RefreshToken(ctx, refreshToken)
	}

	if err != nil {
		return nil, fmt.Errorf("kiro executor: token refresh failed: %w", err)
	}

	updated := auth.Clone()
	now := time.Now()
	updated.UpdatedAt = now
	updated.LastRefreshedAt = now

	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["access_token"] = tokenData.AccessToken
	updated.Metadata["refresh_token"] = tokenData.RefreshToken
	updated.Metadata["expires_at"] = tokenData.ExpiresAt
	updated.Metadata["last_refresh"] = now.Format(time.RFC3339)
	if tokenData.ProfileArn != "" {
		updated.Metadata["profile_arn"] = tokenData.ProfileArn
	}
	if tokenData.AuthMethod != "" {
		updated.Metadata["auth_method"] = tokenData.AuthMethod
	}
	if tokenData.Provider != "" {
		updated.Metadata["provider"] = tokenData.Provider
	}
	// Preserve client credentials for future refreshes (AWS Builder ID)
	if tokenData.ClientID != "" {
		updated.Metadata["client_id"] = tokenData.ClientID
	}
	if tokenData.ClientSecret != "" {
		updated.Metadata["client_secret"] = tokenData.ClientSecret
	}
	// Preserve region and start_url for IDC token refresh
	if tokenData.Region != "" {
		updated.Metadata["region"] = tokenData.Region
	}
	if tokenData.StartURL != "" {
		updated.Metadata["start_url"] = tokenData.StartURL
	}

	if updated.Attributes == nil {
		updated.Attributes = make(map[string]string)
	}
	updated.Attributes["access_token"] = tokenData.AccessToken
	if tokenData.ProfileArn != "" {
		updated.Attributes["profile_arn"] = tokenData.ProfileArn
	}

	// NextRefreshAfter is aligned with RefreshLead (20min)
	if expiresAt, parseErr := time.Parse(time.RFC3339, tokenData.ExpiresAt); parseErr == nil {
		updated.NextRefreshAfter = expiresAt.Add(-20 * time.Minute)
	}

	log.Infof("kiro executor: token refreshed successfully, expires at %s", tokenData.ExpiresAt)
	return updated, nil
}

// persistRefreshedAuth persists a refreshed auth record to disk.
// This ensures token refreshes from inline retry are saved to the auth file.
func (e *KiroExecutor) persistRefreshedAuth(auth *cliproxyauth.Auth) error {
	if auth == nil || auth.Metadata == nil {
		return fmt.Errorf("kiro executor: cannot persist nil auth or metadata")
	}

	// Determine the file path from auth attributes or filename
	var authPath string
	if auth.Attributes != nil {
		if p := strings.TrimSpace(auth.Attributes["path"]); p != "" {
			authPath = p
		}
	}
	if authPath == "" {
		fileName := strings.TrimSpace(auth.FileName)
		if fileName == "" {
			return fmt.Errorf("kiro executor: auth has no file path or filename")
		}
		if filepath.IsAbs(fileName) {
			authPath = fileName
		} else if e.cfg != nil && e.cfg.AuthDir != "" {
			authPath = filepath.Join(e.cfg.AuthDir, fileName)
		} else {
			return fmt.Errorf("kiro executor: cannot determine auth file path")
		}
	}

	// Marshal metadata to JSON
	raw, err := json.Marshal(auth.Metadata)
	if err != nil {
		return fmt.Errorf("kiro executor: marshal metadata failed: %w", err)
	}

	// Write to temp file first, then rename (atomic write)
	tmp := authPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("kiro executor: write temp auth file failed: %w", err)
	}
	if err := os.Rename(tmp, authPath); err != nil {
		return fmt.Errorf("kiro executor: rename auth file failed: %w", err)
	}

	log.Debugf("kiro executor: persisted refreshed auth to %s", authPath)
	return nil
}

// fetchAndSaveProfileArn fetches profileArn from API if missing, updates auth and persists to file.
func (e *KiroExecutor) fetchAndSaveProfileArn(ctx context.Context, auth *cliproxyauth.Auth, accessToken string) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}

	// Skip for Builder ID - they don't have profiles
	if authMethod, ok := auth.Metadata["auth_method"].(string); ok && authMethod == "builder-id" {
		log.Debugf("kiro executor: skipping profileArn fetch for builder-id auth")
		return ""
	}

	e.profileArnMu.Lock()
	defer e.profileArnMu.Unlock()

	// Double-check: another goroutine may have already fetched and saved the profileArn
	if arn, ok := auth.Metadata["profile_arn"].(string); ok && arn != "" {
		return arn
	}

	clientID, _ := auth.Metadata["client_id"].(string)
	refreshToken, _ := auth.Metadata["refresh_token"].(string)

	ssoClient := kiroauth.NewSSOOIDCClient(e.cfg)
	profileArn := ssoClient.FetchProfileArn(ctx, accessToken, clientID, refreshToken)
	if profileArn == "" {
		log.Debugf("kiro executor: FetchProfileArn returned no profiles")
		return ""
	}

	auth.Metadata["profile_arn"] = profileArn
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes["profile_arn"] = profileArn

	if err := e.persistRefreshedAuth(auth); err != nil {
		log.Warnf("kiro executor: failed to persist profileArn: %v", err)
	} else {
		log.Infof("kiro executor: fetched and saved profileArn: %s", profileArn)
	}

	return profileArn
}

// reloadAuthFromFile 从文件重新加载 auth 数据（方案 B: Fallback 机制）
// 当内存中的 token 已过期时，尝试从文件读取最新的 token
// 这解决了后台刷新器已更新文件但内存中 Auth 对象尚未同步的时间差问题
func (e *KiroExecutor) reloadAuthFromFile(auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("kiro executor: cannot reload nil auth")
	}

	// 确定文件路径
	var authPath string
	if auth.Attributes != nil {
		if p := strings.TrimSpace(auth.Attributes["path"]); p != "" {
			authPath = p
		}
	}
	if authPath == "" {
		fileName := strings.TrimSpace(auth.FileName)
		if fileName == "" {
			return nil, fmt.Errorf("kiro executor: auth has no file path or filename for reload")
		}
		if filepath.IsAbs(fileName) {
			authPath = fileName
		} else if e.cfg != nil && e.cfg.AuthDir != "" {
			authPath = filepath.Join(e.cfg.AuthDir, fileName)
		} else {
			return nil, fmt.Errorf("kiro executor: cannot determine auth file path for reload")
		}
	}

	// 读取文件
	raw, err := os.ReadFile(authPath)
	if err != nil {
		return nil, fmt.Errorf("kiro executor: failed to read auth file %s: %w", authPath, err)
	}

	// 解析 JSON
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil, fmt.Errorf("kiro executor: failed to parse auth file %s: %w", authPath, err)
	}

	// 检查文件中的 token 是否比内存中的更新
	fileExpiresAt, _ := metadata["expires_at"].(string)
	fileAccessToken, _ := metadata["access_token"].(string)
	memExpiresAt, _ := auth.Metadata["expires_at"].(string)
	memAccessToken, _ := auth.Metadata["access_token"].(string)

	// 文件中必须有有效的 access_token
	if fileAccessToken == "" {
		return nil, fmt.Errorf("kiro executor: auth file has no access_token field")
	}

	// 如果有 expires_at，检查是否过期
	if fileExpiresAt != "" {
		fileExpTime, parseErr := time.Parse(time.RFC3339, fileExpiresAt)
		if parseErr == nil {
			// 如果文件中的 token 也已过期，不使用它
			if time.Now().After(fileExpTime) {
				log.Debugf("kiro executor: file token also expired at %s, not using", fileExpiresAt)
				return nil, fmt.Errorf("kiro executor: file token also expired")
			}
		}
	}

	// 判断文件中的 token 是否比内存中的更新
	// 条件1: access_token 不同（说明已刷新）
	// 条件2: expires_at 更新（说明已刷新）
	isNewer := false

	// 优先检查 access_token 是否变化
	if fileAccessToken != memAccessToken {
		isNewer = true
		log.Debugf("kiro executor: file access_token differs from memory, using file token")
	}

	// 如果 access_token 相同，检查 expires_at
	if !isNewer && fileExpiresAt != "" && memExpiresAt != "" {
		fileExpTime, fileParseErr := time.Parse(time.RFC3339, fileExpiresAt)
		memExpTime, memParseErr := time.Parse(time.RFC3339, memExpiresAt)
		if fileParseErr == nil && memParseErr == nil && fileExpTime.After(memExpTime) {
			isNewer = true
			log.Debugf("kiro executor: file expires_at (%s) is newer than memory (%s)", fileExpiresAt, memExpiresAt)
		}
	}

	// 如果文件中没有 expires_at 但 access_token 相同，无法判断是否更新
	if !isNewer && fileExpiresAt == "" && fileAccessToken == memAccessToken {
		return nil, fmt.Errorf("kiro executor: cannot determine if file token is newer (no expires_at, same access_token)")
	}

	if !isNewer {
		log.Debugf("kiro executor: file token not newer than memory token")
		return nil, fmt.Errorf("kiro executor: file token not newer")
	}

	// 创建更新后的 auth 对象
	updated := auth.Clone()
	updated.Metadata = metadata
	updated.UpdatedAt = time.Now()

	// 同步更新 Attributes
	if updated.Attributes == nil {
		updated.Attributes = make(map[string]string)
	}
	if accessToken, ok := metadata["access_token"].(string); ok {
		updated.Attributes["access_token"] = accessToken
	}
	if profileArn, ok := metadata["profile_arn"].(string); ok {
		updated.Attributes["profile_arn"] = profileArn
	}

	log.Infof("kiro executor: reloaded auth from file %s, new expires_at: %s", authPath, fileExpiresAt)
	return updated, nil
}

// isTokenExpired checks if a JWT access token has expired.
// Returns true if the token is expired or cannot be parsed.
func (e *KiroExecutor) isTokenExpired(accessToken string) bool {
	if accessToken == "" {
		return true
	}

	// JWT tokens have 3 parts separated by dots
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		// Not a JWT token, assume not expired
		return false
	}

	// Decode the payload (second part)
	// JWT uses base64url encoding without padding (RawURLEncoding)
	payload := parts[1]
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		// Try with padding added as fallback
		switch len(payload) % 4 {
		case 2:
			payload += "=="
		case 3:
			payload += "="
		}
		decoded, err = base64.URLEncoding.DecodeString(payload)
		if err != nil {
			log.Debugf("kiro: failed to decode JWT payload: %v", err)
			return false
		}
	}

	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		log.Debugf("kiro: failed to parse JWT claims: %v", err)
		return false
	}

	if claims.Exp == 0 {
		// No expiration claim, assume not expired
		return false
	}

	expTime := time.Unix(claims.Exp, 0)
	now := time.Now()

	// Consider token expired if it expires within 1 minute (buffer for clock skew)
	isExpired := now.After(expTime) || expTime.Sub(now) < time.Minute
	if isExpired {
		log.Debugf("kiro: token expired at %s (now: %s)", expTime.Format(time.RFC3339), now.Format(time.RFC3339))
	}

	return isExpired
}

// ══════════════════════════════════════════════════════════════════════════════
// Web Search Handler (MCP API)
// ══════════════════════════════════════════════════════════════════════════════

// fetchToolDescription caching:
// Uses a mutex + fetched flag to ensure only one goroutine fetches at a time,
// with automatic retry on failure:
// - On failure, fetched stays false so subsequent calls will retry
// - On success, fetched is set to true — subsequent calls skip immediately (mutex-free fast path)
// The cached description is stored in the translator package via kiroclaude.SetWebSearchDescription(),
// enabling the translator's convertClaudeToolsToKiro to read it when building Kiro requests.
var (
	toolDescMu      sync.Mutex
	toolDescFetched atomic.Bool
)

// fetchToolDescription calls MCP tools/list to get the web_search tool description
// and caches it. Safe to call concurrently — only one goroutine fetches at a time.
// If the fetch fails, subsequent calls will retry. On success, no further fetches occur.
// The httpClient parameter allows reusing a shared pooled HTTP client.
