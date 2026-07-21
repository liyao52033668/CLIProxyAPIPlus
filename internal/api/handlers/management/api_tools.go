package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/gin-gonic/gin"
	cursorauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/geminicli"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// managementTransportCacheMaxEntries is the maximum number of proxy transports kept in cache.
const managementTransportCacheMaxEntries = 128

// managementTransportCache caches http.RoundTripper by proxy URL to enable connection reuse
var (
	managementTransportCache      = make(map[string]http.RoundTripper)
	managementTransportCacheMutex sync.RWMutex
)

// getOrBuildManagementTransport returns a cached RoundTripper for proxyStr, or builds and caches one.
// When the cache exceeds managementTransportCacheMaxEntries, one arbitrary entry is evicted.
func getOrBuildManagementTransport(proxyStr string) http.RoundTripper {
	managementTransportCacheMutex.RLock()
	if cached, ok := managementTransportCache[proxyStr]; ok {
		managementTransportCacheMutex.RUnlock()
		return cached
	}
	managementTransportCacheMutex.RUnlock()

	transport := buildProxyTransport(proxyStr)
	if transport == nil {
		return nil
	}
	managementTransportCacheMutex.Lock()
	if len(managementTransportCache) >= managementTransportCacheMaxEntries {
		for k := range managementTransportCache {
			delete(managementTransportCache, k)
			break
		}
	}
	managementTransportCache[proxyStr] = transport
	managementTransportCacheMutex.Unlock()
	return transport
}

const (
	maxAPICallRequestBodyBytes  int64 = 8 << 20
	maxAPICallResponseBodyBytes int64 = 32 << 20
)

var errAPICallBodyTooLarge = errors.New("api call body too large")

func readAPICallBody(reader io.Reader, limit int64) ([]byte, error) {
	data, errRead := io.ReadAll(io.LimitReader(reader, limit+1))
	if errRead != nil {
		return nil, errRead
	}
	if int64(len(data)) > limit {
		return nil, errAPICallBodyTooLarge
	}
	return data, nil
}

const (
	geminiOAuthClientID     = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"
	geminiOAuthClientSecret = "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"
)

var geminiOAuthScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
}

const (
	antigravityOAuthClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	antigravityOAuthClientSecret = "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"
)

var antigravityOAuthTokenURL = "https://oauth2.googleapis.com/token"

type apiCallRequest struct {
	AuthIndexSnake  *string           `json:"auth_index"`
	AuthIndexCamel  *string           `json:"authIndex"`
	AuthIndexPascal *string           `json:"AuthIndex"`
	Method          string            `json:"method"`
	URL             string            `json:"url"`
	Header          map[string]string `json:"header"`
	Data            string            `json:"data"`
}

type apiCallResponse struct {
	StatusCode int                 `json:"status_code"`
	Header     map[string][]string `json:"header"`
	Body       string              `json:"body"`
	Quota      *QuotaSnapshots     `json:"quota,omitempty"`
}

// APICall makes a generic HTTP request on behalf of the management API caller.
// It is protected by the management middleware.
//
// Endpoint:
//
//	POST /v0/management/api-call
//
// Authentication:
//
//	Same as other management APIs (requires a management key and remote-management rules).
//	You can provide the key via:
//	- Authorization: Bearer <key>
//	- X-Management-Key: <key>
//
// Request JSON (supports both application/json and application/cbor):
//   - auth_index / authIndex / AuthIndex (optional):
//     The credential "auth_index" from GET /v0/management/auth-files (or other endpoints returning it).
//     If omitted or not found, credential-specific proxy/token substitution is skipped.
//   - method (required): HTTP method, e.g. GET, POST, PUT, PATCH, DELETE.
//   - url (required): Absolute URL including scheme and host, e.g. "https://api.example.com/v1/ping".
//   - header (optional): Request headers map.
//     Supports magic variable "$TOKEN$" which is replaced using the selected credential:
//     1) metadata.access_token
//     2) attributes.api_key
//     3) metadata.token / metadata.id_token / metadata.cookie
//     Example: {"Authorization":"Bearer $TOKEN$"}.
//     Note: if you need to override the HTTP Host header, set header["Host"].
//   - data (optional): Raw request body as string (useful for POST/PUT/PATCH).
//
// Proxy selection (highest priority first):
//  1. Selected credential proxy_url
//  2. Global config proxy-url
//  3. Direct connect (environment proxies are not used)
//
// Response (returned with HTTP 200 when the APICall itself succeeds):
//
//	Format matches request Content-Type (application/json or application/cbor)
//	- status_code: Upstream HTTP status code.
//	- header: Upstream response headers.
//	- body: Upstream response body as string.
//	- quota (optional): For GitHub Copilot enterprise accounts, contains quota_snapshots
//	  with details for chat, completions, and premium_interactions.
//
// Example:
//
//	curl -sS -X POST "http://localhost:8317/v0/management/api-call" \
//	  -H "Authorization: Bearer <MANAGEMENT_KEY>" \
//	  -H "Content-Type: application/json" \
//	  -d '{"auth_index":"<AUTH_INDEX>","method":"GET","url":"https://api.example.com/v1/ping","header":{"Authorization":"Bearer $TOKEN$"}}'
//
//	curl -sS -X POST "http://localhost:8317/v0/management/api-call" \
//	  -H "Authorization: Bearer 831227" \
//	  -H "Content-Type: application/json" \
//	  -d '{"auth_index":"<AUTH_INDEX>","method":"POST","url":"https://api.example.com/v1/fetchAvailableModels","header":{"Authorization":"Bearer $TOKEN$","Content-Type":"application/json","User-Agent":"cliproxyapi"},"data":"{}"}'
func (h *Handler) APICall(c *gin.Context) {
	// Detect content type
	contentType := strings.ToLower(strings.TrimSpace(c.GetHeader("Content-Type")))
	isCBOR := strings.Contains(contentType, "application/cbor")

	var body apiCallRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAPICallRequestBodyBytes)

	// Parse request body based on content type
	if isCBOR {
		rawBody, errRead := readAPICallBody(c.Request.Body, maxAPICallRequestBodyBytes)
		var maxBytesErr *http.MaxBytesError
		if errors.Is(errRead, errAPICallBodyTooLarge) || errors.As(errRead, &maxBytesErr) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large"})
			return
		}
		if errRead != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
			return
		}
		if errUnmarshal := cbor.Unmarshal(rawBody, &body); errUnmarshal != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid cbor body"})
			return
		}
	} else {
		rawBody, errRead := readAPICallBody(c.Request.Body, maxAPICallRequestBodyBytes)
		var maxBytesErr *http.MaxBytesError
		if errors.Is(errRead, errAPICallBodyTooLarge) || errors.As(errRead, &maxBytesErr) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large"})
			return
		}
		if errRead != nil || json.Unmarshal(rawBody, &body) != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
	}

	method := strings.ToUpper(strings.TrimSpace(body.Method))
	if method == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing method"})
		return
	}

	urlStr := strings.TrimSpace(body.URL)
	if urlStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing url"})
		return
	}
	parsedURL, errParseURL := url.Parse(urlStr)
	if errParseURL != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid url"})
		return
	}

	authIndex := firstNonEmptyString(body.AuthIndexSnake, body.AuthIndexCamel, body.AuthIndexPascal)
	auth := h.authByIndex(authIndex)

	reqHeaders := body.Header
	if reqHeaders == nil {
		reqHeaders = map[string]string{}
	}

	var hostOverride string
	var token string
	var tokenResolved bool
	var tokenErr error
	for key, value := range reqHeaders {
		if !strings.Contains(value, "$TOKEN$") {
			continue
		}
		if !tokenResolved {
			token, tokenErr = h.resolveTokenForAuth(c.Request.Context(), auth)
			tokenResolved = true
		}
		if auth != nil && token == "" {
			if tokenErr != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "auth token refresh failed"})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth token not found"})
			return
		}
		if token == "" {
			continue
		}
		replacement := token
		// Cursor dashboard APIs expect WorkosCursorSessionToken=user_xxx%3A%3A{jwt}.
		// Keep Bearer $TOKEN$ as the raw access token for model/API calls.
		if auth != nil && strings.EqualFold(strings.TrimSpace(auth.Provider), "cursor") {
			if strings.EqualFold(key, "Cookie") || strings.Contains(value, "WorkosCursorSessionToken") {
				if session := cursorSessionTokenValue(auth, token); session != "" {
					replacement = session
				}
			}
		}
		reqHeaders[key] = strings.ReplaceAll(value, "$TOKEN$", replacement)
	}

	// When caller indicates CBOR in request headers, convert JSON string payload to CBOR bytes.
	useCBORPayload := headerContainsValue(reqHeaders, "Content-Type", "application/cbor")

	var requestBody io.Reader
	if body.Data != "" {
		if useCBORPayload {
			cborPayload, errEncode := encodeJSONStringToCBOR(body.Data)
			if errEncode != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json data for cbor content-type"})
				return
			}
			requestBody = bytes.NewReader(cborPayload)
		} else {
			requestBody = strings.NewReader(body.Data)
		}
	}

	req, errNewRequest := http.NewRequestWithContext(c.Request.Context(), method, urlStr, requestBody)
	if errNewRequest != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to build request"})
		return
	}

	for key, value := range reqHeaders {
		if strings.EqualFold(key, "host") {
			hostOverride = strings.TrimSpace(value)
			continue
		}
		req.Header.Set(key, value)
	}
	if hostOverride != "" {
		req.Host = hostOverride
	}

	httpClient := &http.Client{
		Timeout: h.apiCallTimeout(),
	}
	httpClient.Transport = h.apiCallTransport(auth)

	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		log.WithError(errDo).Debug("management APICall request failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "request failed"})
		return
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	respBody, errReadAll := readAPICallBody(resp.Body, maxAPICallResponseBodyBytes)
	if errors.Is(errReadAll, errAPICallBodyTooLarge) {
		c.JSON(http.StatusBadGateway, gin.H{"error": "response too large"})
		return
	}
	if errReadAll != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to read response"})
		return
	}

	// For CBOR upstream responses, decode into plain text or JSON string before returning.
	responseBodyText := string(respBody)
	if headerContainsValue(reqHeaders, "Accept", "application/cbor") || strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "application/cbor") {
		if decodedBody, errDecode := decodeCBORBodyToTextOrJSON(respBody); errDecode == nil {
			responseBodyText = decodedBody
		}
	}

	response := apiCallResponse{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       responseBodyText,
	}

	// If this is a GitHub Copilot token endpoint response, try to enrich with quota information
	if resp.StatusCode == http.StatusOK &&
		strings.Contains(urlStr, "copilot_internal") &&
		strings.Contains(urlStr, "/token") {
		response = h.enrichCopilotTokenResponse(c.Request.Context(), response, auth, urlStr)
	}

	// Return response in the same format as the request
	if isCBOR {
		cborData, errMarshal := cbor.Marshal(response)
		if errMarshal != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to encode cbor response"})
			return
		}
		c.Data(http.StatusOK, "application/cbor", cborData)
	} else {
		c.JSON(http.StatusOK, response)
	}
}

func firstNonEmptyString(values ...*string) string {
	for _, v := range values {
		if v == nil {
			continue
		}
		if out := strings.TrimSpace(*v); out != "" {
			return out
		}
	}
	return ""
}

func tokenValueForAuth(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if v := tokenValueFromMetadata(auth.Metadata); v != "" {
		return v
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["api_key"]); v != "" {
			return v
		}
	}
	if shared := geminicli.ResolveSharedCredential(auth.Runtime); shared != nil {
		if v := tokenValueFromMetadata(shared.MetadataSnapshot()); v != "" {
			return v
		}
	}
	return ""
}

// extractCursorUserID returns the user_xxx portion of a Cursor subject claim.
// Accepts "user_xxx" or provider-prefixed forms like "google-oauth2|user_xxx".
func extractCursorUserID(sub string) string {
	sub = strings.TrimSpace(sub)
	if sub == "" {
		return ""
	}
	if idx := strings.LastIndex(sub, "|"); idx >= 0 {
		sub = strings.TrimSpace(sub[idx+1:])
	}
	if strings.HasPrefix(sub, "user_") {
		return sub
	}
	return ""
}

func cursorUserIDFromAuth(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if id := extractCursorUserID(stringValue(auth.Metadata, "sub")); id != "" {
			return id
		}
	}
	return ""
}

// cursorSessionTokenValue builds the Cookie value used by cursor.com dashboard APIs:
// user_xxx%3A%3A{access_token}. Falls back to the raw access token when user id is unknown.
func cursorSessionTokenValue(auth *coreauth.Auth, accessToken string) string {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return ""
	}
	userID := cursorUserIDFromAuth(auth)
	if userID == "" {
		userID = extractCursorUserID(cursorauth.ParseJWTSub(accessToken))
	}
	if userID == "" {
		return accessToken
	}
	return userID + "%3A%3A" + accessToken
}

func (h *Handler) resolveTokenForAuth(ctx context.Context, auth *coreauth.Auth) (string, error) {
	if auth == nil {
		return "", nil
	}

	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if provider == "gemini-cli" {
		token, errToken := h.refreshGeminiOAuthAccessToken(ctx, auth)
		return token, errToken
	}
	if provider == "antigravity" {
		token, errToken := h.refreshAntigravityOAuthAccessToken(ctx, auth)
		return token, errToken
	}

	return tokenValueForAuth(auth), nil
}

func (h *Handler) refreshGeminiOAuthAccessToken(ctx context.Context, auth *coreauth.Auth) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if auth == nil {
		return "", nil
	}

	metadata, updater := geminiOAuthMetadata(auth)
	if len(metadata) == 0 {
		return "", fmt.Errorf("gemini oauth metadata missing")
	}

	base := make(map[string]any)
	if tokenRaw, ok := metadata["token"].(map[string]any); ok && tokenRaw != nil {
		base = cloneMap(tokenRaw)
	}

	var token oauth2.Token
	if len(base) > 0 {
		if raw, errMarshal := json.Marshal(base); errMarshal == nil {
			_ = json.Unmarshal(raw, &token)
		}
	}

	if token.AccessToken == "" {
		token.AccessToken = stringValue(metadata, "access_token")
	}
	if token.RefreshToken == "" {
		token.RefreshToken = stringValue(metadata, "refresh_token")
	}
	if token.TokenType == "" {
		token.TokenType = stringValue(metadata, "token_type")
	}
	if token.Expiry.IsZero() {
		if expiry := stringValue(metadata, "expiry"); expiry != "" {
			if ts, errParseTime := time.Parse(time.RFC3339, expiry); errParseTime == nil {
				token.Expiry = ts
			}
		}
	}

	conf := &oauth2.Config{
		ClientID:     geminiOAuthClientID,
		ClientSecret: geminiOAuthClientSecret,
		Scopes:       geminiOAuthScopes,
		Endpoint:     google.Endpoint,
	}

	ctxToken := ctx
	httpClient := &http.Client{
		Timeout:   h.apiCallTimeout(),
		Transport: h.apiCallTransport(auth),
	}
	ctxToken = context.WithValue(ctxToken, oauth2.HTTPClient, httpClient)

	src := conf.TokenSource(ctxToken, &token)
	currentToken, errToken := src.Token()
	if errToken != nil {
		return "", errToken
	}

	merged := buildOAuthTokenMap(base, currentToken)
	fields := buildOAuthTokenFields(currentToken, merged)
	if updater != nil {
		updater(fields)
	}
	return strings.TrimSpace(currentToken.AccessToken), nil
}

func (h *Handler) refreshAntigravityOAuthAccessToken(ctx context.Context, auth *coreauth.Auth) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if auth == nil {
		return "", nil
	}

	metadata := auth.Metadata
	if len(metadata) == 0 {
		return "", fmt.Errorf("antigravity oauth metadata missing")
	}

	current := strings.TrimSpace(tokenValueFromMetadata(metadata))
	if current != "" && !antigravityTokenNeedsRefresh(metadata) {
		return current, nil
	}

	refreshToken := stringValue(metadata, "refresh_token")
	if refreshToken == "" {
		return "", fmt.Errorf("antigravity refresh token missing")
	}

	tokenURL := strings.TrimSpace(antigravityOAuthTokenURL)
	if tokenURL == "" {
		tokenURL = "https://oauth2.googleapis.com/token"
	}
	form := url.Values{}
	form.Set("client_id", antigravityOAuthClientID)
	form.Set("client_secret", antigravityOAuthClientSecret)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)

	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if errReq != nil {
		return "", errReq
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	httpClient := &http.Client{
		Timeout:   h.apiCallTimeout(),
		Transport: h.apiCallTransport(auth),
	}
	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		return "", errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	bodyBytes, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return "", errRead
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("antigravity oauth token refresh failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if errUnmarshal := json.Unmarshal(bodyBytes, &tokenResp); errUnmarshal != nil {
		return "", errUnmarshal
	}

	if strings.TrimSpace(tokenResp.AccessToken) == "" {
		return "", fmt.Errorf("antigravity oauth token refresh returned empty access_token")
	}

	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	now := time.Now()
	auth.Metadata["access_token"] = strings.TrimSpace(tokenResp.AccessToken)
	if strings.TrimSpace(tokenResp.RefreshToken) != "" {
		auth.Metadata["refresh_token"] = strings.TrimSpace(tokenResp.RefreshToken)
	}
	if tokenResp.ExpiresIn > 0 {
		auth.Metadata["expires_in"] = tokenResp.ExpiresIn
		auth.Metadata["timestamp"] = now.UnixMilli()
		auth.Metadata["expired"] = now.Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339)
	}
	auth.Metadata["type"] = "antigravity"

	if h != nil && h.authManager != nil {
		auth.LastRefreshedAt = now
		auth.UpdatedAt = now
		_, _ = h.authManager.Update(ctx, auth)
	}

	return strings.TrimSpace(tokenResp.AccessToken), nil
}

func antigravityTokenNeedsRefresh(metadata map[string]any) bool {
	// Refresh a bit early to avoid requests racing token expiry.
	const skew = 30 * time.Second

	if metadata == nil {
		return true
	}
	if expStr, ok := metadata["expired"].(string); ok {
		if ts, errParse := time.Parse(time.RFC3339, strings.TrimSpace(expStr)); errParse == nil {
			return !ts.After(time.Now().Add(skew))
		}
	}
	expiresIn := int64Value(metadata["expires_in"])
	timestampMs := int64Value(metadata["timestamp"])
	if expiresIn > 0 && timestampMs > 0 {
		exp := time.UnixMilli(timestampMs).Add(time.Duration(expiresIn) * time.Second)
		return !exp.After(time.Now().Add(skew))
	}
	return true
}

func int64Value(raw any) int64 {
	switch typed := raw.(type) {
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case int64:
		return typed
	case uint:
		return int64(typed)
	case uint32:
		return int64(typed)
	case uint64:
		if typed > uint64(^uint64(0)>>1) {
			return 0
		}
		return int64(typed)
	case float32:
		return int64(typed)
	case float64:
		return int64(typed)
	case json.Number:
		if i, errParse := typed.Int64(); errParse == nil {
			return i
		}
	case string:
		if s := strings.TrimSpace(typed); s != "" {
			if i, errParse := json.Number(s).Int64(); errParse == nil {
				return i
			}
		}
	}
	return 0
}

func geminiOAuthMetadata(auth *coreauth.Auth) (map[string]any, func(map[string]any)) {
	if auth == nil {
		return nil, nil
	}
	if shared := geminicli.ResolveSharedCredential(auth.Runtime); shared != nil {
		snapshot := shared.MetadataSnapshot()
		return snapshot, func(fields map[string]any) { shared.MergeMetadata(fields) }
	}
	return auth.Metadata, func(fields map[string]any) {
		if auth.Metadata == nil {
			auth.Metadata = make(map[string]any)
		}
		maps.Copy(auth.Metadata, fields)
	}
}

func stringValue(metadata map[string]any, key string) string {
	if len(metadata) == 0 || key == "" {
		return ""
	}
	if v, ok := metadata[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	maps.Copy(out, in)
	return out
}

func buildOAuthTokenMap(base map[string]any, tok *oauth2.Token) map[string]any {
	merged := cloneMap(base)
	if merged == nil {
		merged = make(map[string]any)
	}
	if tok == nil {
		return merged
	}
	if raw, errMarshal := json.Marshal(tok); errMarshal == nil {
		var tokenMap map[string]any
		if errUnmarshal := json.Unmarshal(raw, &tokenMap); errUnmarshal == nil {
			maps.Copy(merged, tokenMap)
		}
	}
	return merged
}

func buildOAuthTokenFields(tok *oauth2.Token, merged map[string]any) map[string]any {
	fields := make(map[string]any, 5)
	if tok != nil && tok.AccessToken != "" {
		fields["access_token"] = tok.AccessToken
	}
	if tok != nil && tok.TokenType != "" {
		fields["token_type"] = tok.TokenType
	}
	if tok != nil && tok.RefreshToken != "" {
		fields["refresh_token"] = tok.RefreshToken
	}
	if tok != nil && !tok.Expiry.IsZero() {
		fields["expiry"] = tok.Expiry.Format(time.RFC3339)
	}
	if len(merged) > 0 {
		fields["token"] = cloneMap(merged)
	}
	return fields
}

func tokenValueFromMetadata(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	if v, ok := metadata["accessToken"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if v, ok := metadata["access_token"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if tokenRaw, ok := metadata["token"]; ok && tokenRaw != nil {
		switch typed := tokenRaw.(type) {
		case string:
			if v := strings.TrimSpace(typed); v != "" {
				return v
			}
		case map[string]any:
			if v, ok := typed["access_token"].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
			if v, ok := typed["accessToken"].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		case map[string]string:
			if v := strings.TrimSpace(typed["access_token"]); v != "" {
				return v
			}
			if v := strings.TrimSpace(typed["accessToken"]); v != "" {
				return v
			}
		}
	}
	if v, ok := metadata["token"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if v, ok := metadata["id_token"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if v, ok := metadata["cookie"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return ""
}

func (h *Handler) authByIndex(authIndex string) *coreauth.Auth {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" || h == nil || h.authManager == nil {
		return nil
	}
	auths := h.authManager.List()
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		auth.EnsureIndex()
		if auth.Index == authIndex {
			return auth
		}
	}
	return nil
}

// apiCallTimeout returns the configured timeout for management API calls.
// Falls back to 60 seconds if config is not available.
func (h *Handler) apiCallTimeout() time.Duration {
	if h != nil && h.cfg != nil && h.cfg.Timeouts.ManagementAPICallSeconds > 0 {
		return time.Duration(h.cfg.Timeouts.ManagementAPICallSeconds) * time.Second
	}
	return 60 * time.Second
}

func (h *Handler) apiCallTransport(auth *coreauth.Auth) http.RoundTripper {
	var proxyCandidates []string
	if auth != nil {
		if proxyStr := strings.TrimSpace(auth.ProxyURL); proxyStr != "" {
			proxyCandidates = append(proxyCandidates, proxyStr)
		}
		if h != nil && h.cfg != nil {
			if proxyStr := strings.TrimSpace(proxyURLFromAPIKeyConfig(h.cfg, auth)); proxyStr != "" {
				proxyCandidates = append(proxyCandidates, proxyStr)
			}
		}
	}
	if h != nil && h.cfg != nil {
		if proxyStr := strings.TrimSpace(h.cfg.ProxyURL); proxyStr != "" {
			proxyCandidates = append(proxyCandidates, proxyStr)
		}
	}

	for _, proxyStr := range proxyCandidates {
		if transport := getOrBuildManagementTransport(proxyStr); transport != nil {
			return transport
		}
	}

	// Return default transport without proxy
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok || transport == nil {
		return &http.Transport{Proxy: nil}
	}
	clone := transport.Clone()
	clone.Proxy = nil
	return clone
}

type apiKeyConfigEntry interface {
	GetAPIKey() string
	GetBaseURL() string
}

func resolveAPIKeyConfig[T apiKeyConfigEntry](entries []T, auth *coreauth.Auth) *T {
	if auth == nil || len(entries) == 0 {
		return nil
	}
	attrKey, attrBase := "", ""
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range entries {
		entry := &entries[i]
		cfgKey := strings.TrimSpace((*entry).GetAPIKey())
		cfgBase := strings.TrimSpace((*entry).GetBaseURL())
		if attrKey != "" && attrBase != "" {
			if strings.EqualFold(cfgKey, attrKey) && strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	if attrKey != "" {
		for i := range entries {
			entry := &entries[i]
			if strings.EqualFold(strings.TrimSpace((*entry).GetAPIKey()), attrKey) {
				return entry
			}
		}
	}
	return nil
}

func proxyURLFromAPIKeyConfig(cfg *config.Config, auth *coreauth.Auth) string {
	if cfg == nil || auth == nil {
		return ""
	}
	authKind, authAccount := auth.AccountInfo()
	if !strings.EqualFold(strings.TrimSpace(authKind), "api_key") {
		return ""
	}

	attrs := auth.Attributes
	compatName := ""
	providerKey := ""
	if len(attrs) > 0 {
		compatName = strings.TrimSpace(attrs["compat_name"])
		providerKey = strings.TrimSpace(attrs["provider_key"])
	}
	if compatName != "" || strings.EqualFold(strings.TrimSpace(auth.Provider), "openai-compatibility") {
		return resolveOpenAICompatAPIKeyProxyURL(cfg, auth, strings.TrimSpace(authAccount), providerKey, compatName)
	}

	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "gemini":
		if entry := resolveAPIKeyConfig(cfg.GeminiKey, auth); entry != nil {
			return strings.TrimSpace(entry.ProxyURL)
		}
	case "claude":
		if entry := resolveAPIKeyConfig(cfg.ClaudeKey, auth); entry != nil {
			return strings.TrimSpace(entry.ProxyURL)
		}
	case "codex":
		if entry := resolveAPIKeyConfig(cfg.CodexKey, auth); entry != nil {
			return strings.TrimSpace(entry.ProxyURL)
		}
	}
	return ""
}

func resolveOpenAICompatAPIKeyProxyURL(cfg *config.Config, auth *coreauth.Auth, apiKey, providerKey, compatName string) string {
	if cfg == nil || auth == nil {
		return ""
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return ""
	}
	candidates := make([]string, 0, 3)
	if v := strings.TrimSpace(compatName); v != "" {
		candidates = append(candidates, v)
	}
	if v := strings.TrimSpace(providerKey); v != "" {
		candidates = append(candidates, v)
	}
	if v := strings.TrimSpace(auth.Provider); v != "" {
		candidates = append(candidates, v)
	}

	for i := range cfg.OpenAICompatibility {
		compat := &cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		for _, candidate := range candidates {
			if candidate != "" && strings.EqualFold(strings.TrimSpace(candidate), compat.Name) {
				for j := range compat.APIKeyEntries {
					entry := &compat.APIKeyEntries[j]
					if strings.EqualFold(strings.TrimSpace(entry.APIKey), apiKey) {
						return strings.TrimSpace(entry.ProxyURL)
					}
				}
				return ""
			}
		}
	}
	return ""
}

func buildProxyTransport(proxyStr string) *http.Transport {
	transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyStr)
	if errBuild != nil {
		log.WithError(errBuild).Debug("build proxy transport failed")
		return nil
	}
	return transport
}

// headerContainsValue checks whether a header map contains a target value (case-insensitive key and value).
func headerContainsValue(headers map[string]string, targetKey, targetValue string) bool {
	if len(headers) == 0 {
		return false
	}
	for key, value := range headers {
		if !strings.EqualFold(strings.TrimSpace(key), strings.TrimSpace(targetKey)) {
			continue
		}
		if strings.Contains(strings.ToLower(value), strings.ToLower(strings.TrimSpace(targetValue))) {
			return true
		}
	}
	return false
}

// encodeJSONStringToCBOR converts a JSON string payload into CBOR bytes.
func encodeJSONStringToCBOR(jsonString string) ([]byte, error) {
	var payload any
	if errUnmarshal := json.Unmarshal([]byte(jsonString), &payload); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	return cbor.Marshal(payload)
}

// decodeCBORBodyToTextOrJSON decodes CBOR bytes to plain text (for string payloads) or JSON string.
func decodeCBORBodyToTextOrJSON(raw []byte) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}

	var payload any
	if errUnmarshal := cbor.Unmarshal(raw, &payload); errUnmarshal != nil {
		return "", errUnmarshal
	}

	jsonCompatible := cborValueToJSONCompatible(payload)
	switch typed := jsonCompatible.(type) {
	case string:
		return typed, nil
	case []byte:
		return string(typed), nil
	default:
		jsonBytes, errMarshal := json.Marshal(jsonCompatible)
		if errMarshal != nil {
			return "", errMarshal
		}
		return string(jsonBytes), nil
	}
}

// cborValueToJSONCompatible recursively converts CBOR-decoded values into JSON-marshalable values.
func cborValueToJSONCompatible(value any) any {
	switch typed := value.(type) {
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[fmt.Sprint(key)] = cborValueToJSONCompatible(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = cborValueToJSONCompatible(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cborValueToJSONCompatible(item)
		}
		return out
	default:
		return typed
	}
}

// QuotaDetail represents quota information for a specific resource type
type QuotaDetail struct {
	Entitlement      float64 `json:"entitlement"`
	Usage            float64 `json:"usage"`
	PercentUsed      float64 `json:"percent_used"`
	OverageCount     float64 `json:"overage_count"`
	OveragePermitted bool    `json:"overage_permitted"`
	PercentRemaining float64 `json:"percent_remaining"`
	QuotaID          string  `json:"quota_id"`
	QuotaRemaining   float64 `json:"quota_remaining"`
	Remaining        float64 `json:"remaining"`
	Unlimited        bool    `json:"unlimited"`
}

// QuotaSnapshots contains quota details for different resource types
type QuotaSnapshots struct {
	Chat                QuotaDetail `json:"chat"`
	Completions         QuotaDetail `json:"completions"`
	PremiumInteractions QuotaDetail `json:"premium_interactions"`
}

// CopilotUsageResponse represents the GitHub Copilot usage information
type CopilotUsageResponse struct {
	AccessTypeSKU         string         `json:"access_type_sku"`
	AnalyticsTrackingID   string         `json:"analytics_tracking_id"`
	AssignedDate          string         `json:"assigned_date"`
	CanSignupForLimited   bool           `json:"can_signup_for_limited"`
	ChatEnabled           bool           `json:"chat_enabled"`
	CopilotPlan           string         `json:"copilot_plan"`
	OrganizationLoginList []any          `json:"organization_login_list"`
	OrganizationList      []any          `json:"organization_list"`
	QuotaResetDate        string         `json:"quota_reset_date"`
	QuotaSnapshots        QuotaSnapshots `json:"quota_snapshots"`
}

// normalizeQuotaDetail fills derived fields so Free/Pro responses share one shape.
// GitHub may omit usage/percent fields while still returning entitlement/remaining.
// Remaining can be negative when the account has overshot the limit; treat that as 0
// when deriving usage/percent so the UI never shows negative progress.
func normalizeQuotaDetail(detail QuotaDetail, quotaID string) QuotaDetail {
	if detail.QuotaID == "" {
		detail.QuotaID = quotaID
	}
	if detail.QuotaRemaining == 0 && detail.Remaining != 0 {
		detail.QuotaRemaining = detail.Remaining
	}
	if detail.Remaining == 0 && detail.QuotaRemaining != 0 {
		detail.Remaining = detail.QuotaRemaining
	}

	effectiveRemaining := detail.Remaining
	if effectiveRemaining < 0 {
		effectiveRemaining = 0
	}

	if detail.Entitlement > 0 {
		if detail.Usage == 0 {
			detail.Usage = detail.Entitlement - effectiveRemaining
			if detail.Usage < 0 {
				detail.Usage = 0
			}
		}
		// Only derive percent when remaining is still positive but percent was omitted/zero.
		// Fully used (remaining 0, percent 0) must stay at 0; do not recompute from a negative remaining.
		if detail.PercentRemaining == 0 && effectiveRemaining > 0 {
			detail.PercentRemaining = effectiveRemaining / detail.Entitlement
		}
		if detail.PercentUsed == 0 && detail.Usage > 0 {
			detail.PercentUsed = detail.Usage / detail.Entitlement
		}
	}
	return detail
}

// anyToFloat64 coerces JSON numbers stored as float64/int/int64/json.Number/string.
func anyToFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		if n == "" {
			return 0, false
		}
		f, err := json.Number(n).Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// quotaDetailFromLimited builds a QuotaDetail from monthly_quotas + limited_user_quotas.
// monthly = entitlement, limited = remaining (Free / limited plans).
func quotaDetailFromLimited(monthly, limited map[string]any, key string) QuotaDetail {
	total, hasTotal := anyToFloat64(monthly[key])
	if !hasTotal {
		return QuotaDetail{QuotaID: key}
	}
	remaining := total
	if r, ok := anyToFloat64(limited[key]); ok {
		remaining = r
	}
	usage := total - remaining
	percentRemaining := 0.0
	percentUsed := 0.0
	if total > 0 {
		percentRemaining = remaining / total
		percentUsed = usage / total
	}
	return QuotaDetail{
		Entitlement:      total,
		Usage:            usage,
		PercentUsed:      percentUsed,
		PercentRemaining: percentRemaining,
		QuotaID:          key,
		QuotaRemaining:   remaining,
		Remaining:        remaining,
		Unlimited:        false,
	}
}

// parseCopilotUsageBody normalizes /copilot_internal/user into CopilotUsageResponse.
//
// Two official response shapes are supported:
//   - Pro / Business / Enterprise: native quota_snapshots (chat/completions/premium_interactions)
//   - Free / limited: limited_user_quotas + monthly_quotas (remaining + entitlement)
//
// GetCopilotQuota previously only handled the Free shape and zeroed Pro premium quota.
func parseCopilotUsageBody(body []byte) (CopilotUsageResponse, error) {
	var usage CopilotUsageResponse
	if err := json.Unmarshal(body, &usage); err != nil {
		return CopilotUsageResponse{}, err
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return CopilotUsageResponse{}, err
	}

	if snapshotsRaw, ok := raw["quota_snapshots"].(map[string]any); ok && len(snapshotsRaw) > 0 {
		// Paid plans already return the canonical snapshot shape.
		usage.QuotaSnapshots.Chat = normalizeQuotaDetail(usage.QuotaSnapshots.Chat, "chat")
		usage.QuotaSnapshots.Completions = normalizeQuotaDetail(usage.QuotaSnapshots.Completions, "completions")
		usage.QuotaSnapshots.PremiumInteractions = normalizeQuotaDetail(usage.QuotaSnapshots.PremiumInteractions, "premium_interactions")

		// Prefer UTC reset date when present.
		if resetUTC, ok := raw["quota_reset_date_utc"].(string); ok && strings.TrimSpace(resetUTC) != "" {
			usage.QuotaResetDate = resetUTC
		} else if usage.QuotaResetDate == "" {
			if reset, ok := raw["quota_reset_date"].(string); ok {
				usage.QuotaResetDate = reset
			}
		}
		return usage, nil
	}

	// Free / limited plans: synthesize snapshots from monthly + limited remaining.
	monthlyQuotas, _ := raw["monthly_quotas"].(map[string]any)
	limitedQuotas, _ := raw["limited_user_quotas"].(map[string]any)
	if monthlyQuotas == nil {
		monthlyQuotas = map[string]any{}
	}
	if limitedQuotas == nil {
		limitedQuotas = map[string]any{}
	}

	usage.QuotaSnapshots = QuotaSnapshots{
		Chat:                quotaDetailFromLimited(monthlyQuotas, limitedQuotas, "chat"),
		Completions:         quotaDetailFromLimited(monthlyQuotas, limitedQuotas, "completions"),
		PremiumInteractions: QuotaDetail{QuotaID: "premium_interactions", Unlimited: true},
	}

	if reset, ok := raw["limited_user_reset_date"].(string); ok && strings.TrimSpace(reset) != "" {
		usage.QuotaResetDate = reset
	} else if resetUTC, ok := raw["quota_reset_date_utc"].(string); ok && strings.TrimSpace(resetUTC) != "" {
		usage.QuotaResetDate = resetUTC
	}

	return usage, nil
}

// GetCopilotQuota fetches GitHub Copilot quota information from the /copilot_internal/user endpoint.
//
// Endpoint:
//
//	GET /v0/management/copilot-quota
//
// Query Parameters (optional):
//   - auth_index: The credential "auth_index" from GET /v0/management/auth-files.
//     If omitted, uses the first available GitHub Copilot credential.
//
// Response:
//
//	Returns the CopilotUsageResponse with quota_snapshots containing detailed quota information
//	for chat, completions, and premium_interactions.
//
// Example:
//
//	curl -sS -X GET "http://localhost:8317/v0/management/copilot-quota?auth_index=<AUTH_INDEX>" \
//	  -H "Authorization: Bearer <MANAGEMENT_KEY>"
func (h *Handler) GetCopilotQuota(c *gin.Context) {
	authIndex := strings.TrimSpace(c.Query("auth_index"))
	if authIndex == "" {
		authIndex = strings.TrimSpace(c.Query("authIndex"))
	}
	if authIndex == "" {
		authIndex = strings.TrimSpace(c.Query("AuthIndex"))
	}

	auth := h.findCopilotAuth(authIndex)
	if auth == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no github copilot credential found"})
		return
	}

	token, tokenErr := h.resolveTokenForAuth(c.Request.Context(), auth)
	if tokenErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to refresh copilot token"})
		return
	}
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "copilot token not found"})
		return
	}

	apiURL := "https://api.github.com/copilot_internal/user"
	req, errNewRequest := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, apiURL, nil)
	if errNewRequest != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build request"})
		return
	}

	// Match VS Code / official Copilot client headers so GitHub returns the same
	// plan-aware payload shape (quota_snapshots for paid, limited_user_quotas for free).
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "GithubCopilot/1.0")
	req.Header.Set("Editor-Version", "vscode/1.100.0")
	req.Header.Set("Editor-Plugin-Version", "copilot/1.300.0")
	req.Header.Set("Copilot-Integration-Id", "vscode-chat")
	req.Header.Set("X-Github-Api-Version", "2026-01-09")

	httpClient := &http.Client{
		Timeout:   h.apiCallTimeout(),
		Transport: h.apiCallTransport(auth),
	}

	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		log.WithError(errDo).Debug("copilot quota request failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "request failed"})
		return
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	respBody, errReadAll := io.ReadAll(resp.Body)
	if errReadAll != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to read response"})
		return
	}

	if resp.StatusCode != http.StatusOK {
		c.JSON(http.StatusBadGateway, gin.H{
			"error":       "github api request failed",
			"status_code": resp.StatusCode,
			"body":        string(respBody),
		})
		return
	}

	log.Debugf("GetCopilotQuota raw response body: %s", string(respBody))

	usage, errParse := parseCopilotUsageBody(respBody)
	if errParse != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to parse response"})
		return
	}

	c.JSON(http.StatusOK, usage)
}

// findCopilotAuth locates a GitHub Copilot credential by auth_index or returns the first available one.
// When no auth_index is provided, candidates are ordered by CreatedAt then ID to keep selection stable.
func (h *Handler) findCopilotAuth(authIndex string) *coreauth.Auth {
	if h == nil || h.authManager == nil {
		return nil
	}

	auths := h.authManager.List()
	candidates := make([]*coreauth.Auth, 0, len(auths))

	for _, auth := range auths {
		if auth == nil {
			continue
		}

		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		if provider != "copilot" && provider != "github" && provider != "github-copilot" {
			continue
		}

		if authIndex != "" {
			auth.EnsureIndex()
			if auth.Index == authIndex {
				return auth
			}
		}

		candidates = append(candidates, auth)
	}

	if len(candidates) == 0 {
		return nil
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		left := candidates[i]
		right := candidates[j]
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.Before(right.CreatedAt)
		}
		return left.ID < right.ID
	})

	return candidates[0]
}

// enrichCopilotTokenResponse fetches quota information and adds it to the Copilot token response body
func (h *Handler) enrichCopilotTokenResponse(ctx context.Context, response apiCallResponse, auth *coreauth.Auth, originalURL string) apiCallResponse {
	if auth == nil || response.Body == "" {
		return response
	}

	// Parse the token response to check if it's enterprise (null limited_user_quotas)
	var tokenResp map[string]any
	if err := json.Unmarshal([]byte(response.Body), &tokenResp); err != nil {
		log.WithError(err).Debug("enrichCopilotTokenResponse: failed to parse copilot token response")
		return response
	}

	// Get the GitHub token to call the copilot_internal/user endpoint
	token, tokenErr := h.resolveTokenForAuth(ctx, auth)
	if tokenErr != nil {
		log.WithError(tokenErr).Debug("enrichCopilotTokenResponse: failed to resolve token")
		return response
	}
	if token == "" {
		return response
	}

	// Fetch quota information from /copilot_internal/user
	// Derive the base URL from the original token request to support proxies and test servers
	parsedURL, errParse := url.Parse(originalURL)
	if errParse != nil {
		log.WithError(errParse).Debug("enrichCopilotTokenResponse: failed to parse URL")
		return response
	}
	quotaURL := fmt.Sprintf("%s://%s/copilot_internal/user", parsedURL.Scheme, parsedURL.Host)

	req, errNewRequest := http.NewRequestWithContext(ctx, http.MethodGet, quotaURL, nil)
	if errNewRequest != nil {
		log.WithError(errNewRequest).Debug("enrichCopilotTokenResponse: failed to build request")
		return response
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "GithubCopilot/1.0")
	req.Header.Set("Editor-Version", "vscode/1.100.0")
	req.Header.Set("Editor-Plugin-Version", "copilot/1.300.0")
	req.Header.Set("Copilot-Integration-Id", "vscode-chat")
	req.Header.Set("X-Github-Api-Version", "2026-01-09")

	httpClient := &http.Client{
		Timeout:   h.apiCallTimeout(),
		Transport: h.apiCallTransport(auth),
	}

	quotaResp, errDo := httpClient.Do(req)
	if errDo != nil {
		log.WithError(errDo).Debug("enrichCopilotTokenResponse: quota fetch HTTP request failed")
		return response
	}

	defer func() {
		if errClose := quotaResp.Body.Close(); errClose != nil {
			log.Errorf("quota response body close error: %v", errClose)
		}
	}()

	if quotaResp.StatusCode != http.StatusOK {
		return response
	}

	quotaBody, errReadAll := io.ReadAll(quotaResp.Body)
	if errReadAll != nil {
		log.WithError(errReadAll).Debug("enrichCopilotTokenResponse: failed to read response")
		return response
	}

	quotaData, errParse := parseCopilotUsageBody(quotaBody)
	if errParse != nil {
		log.WithError(errParse).Debug("enrichCopilotTokenResponse: failed to parse response")
		return response
	}

	tokenResp["quota_snapshots"] = quotaData.QuotaSnapshots
	tokenResp["access_type_sku"] = quotaData.AccessTypeSKU
	tokenResp["copilot_plan"] = quotaData.CopilotPlan
	if quotaData.QuotaResetDate != "" {
		tokenResp["quota_reset_date"] = quotaData.QuotaResetDate
	}

	// Re-serialize the enriched response
	enrichedBody, errMarshal := json.Marshal(tokenResp)
	if errMarshal != nil {
		log.WithError(errMarshal).Debug("failed to marshal enriched response")
		return response
	}

	response.Body = string(enrichedBody)

	return response
}
