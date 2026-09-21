package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// ProbeAuth verifies xAI OAuth chat access without relying on token refresh.
func (e *XAIExecutor) ProbeAuth(ctx context.Context, auth *cliproxyauth.Auth) error {
	if auth == nil {
		return fmt.Errorf("xai executor: auth is nil")
	}
	if xaiUsingAPI(auth) {
		return fmt.Errorf("xai executor: API key auth inspection unsupported")
	}

	primaryBody := []byte(fmt.Sprintf(`{"model":%q,"input":"ping","stream":false}`, xaiInspectionProbeModel))
	primary, err := e.probeAuthRequest(ctx, auth, xaiResponsesPath, primaryBody)
	if err != nil {
		return err
	}
	if primary.statusCode >= http.StatusOK && primary.statusCode < http.StatusMultipleChoices {
		return nil
	}

	if primary.statusCode == http.StatusTooManyRequests && !xaiFreeUsageExhausted(primary.body) {
		if errWait := waitXAIInspectionRetry(ctx); errWait != nil {
			return errWait
		}
		if retry, errRetry := e.probeAuthRequest(ctx, auth, xaiResponsesPath, primaryBody); errRetry == nil {
			primary = retry
		}
		if primary.statusCode >= http.StatusOK && primary.statusCode < http.StatusMultipleChoices {
			return nil
		}
	}

	if xaiInspectionProbeDefinitive(primary.statusCode, primary.body) {
		return xaiStatusErr(primary.statusCode, primary.body)
	}

	fallbackBody := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"ping"}],"stream":false}`, xaiInspectionProbeModel))
	fallback, errFallback := e.probeAuthRequest(ctx, auth, xaiChatCompletionsPath, fallbackBody)
	if errFallback != nil {
		return xaiStatusErr(primary.statusCode, primary.body)
	}
	if fallback.statusCode >= http.StatusOK && fallback.statusCode < http.StatusMultipleChoices {
		return nil
	}
	return xaiStatusErr(fallback.statusCode, fallback.body)
}

type xaiAuthProbeResponse struct {
	statusCode int
	body       []byte
}

func (e *XAIExecutor) probeAuthRequest(ctx context.Context, auth *cliproxyauth.Auth, path string, body []byte) (xaiAuthProbeResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, helps.JoinBaseURL(xaiChatBaseURL(auth), path), bytes.NewReader(body))
	if err != nil {
		return xaiAuthProbeResponse{}, fmt.Errorf("xai executor: build auth probe request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.HttpRequest(ctx, auth, req)
	if err != nil {
		return xaiAuthProbeResponse{}, fmt.Errorf("xai executor: auth probe request: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("xai executor: close auth probe response body error: %v", errClose)
		}
	}()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, xaiInspectionProbeBodyLimit))
	if err != nil {
		return xaiAuthProbeResponse{}, fmt.Errorf("xai executor: read auth probe response: %w", err)
	}
	return xaiAuthProbeResponse{statusCode: resp.StatusCode, body: responseBody}, nil
}

func waitXAIInspectionRetry(ctx context.Context) error {
	timer := time.NewTimer(xaiInspectionRetryBackoff)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func xaiInspectionProbeDefinitive(statusCode int, body []byte) bool {
	if statusCode == http.StatusUnauthorized || xaiFreeUsageExhausted(body) {
		return true
	}
	message := strings.ToLower(string(body))
	for _, marker := range []string{
		"personal-team-blocked:spending-limit",
		"spending-limit",
		"access to the chat endpoint is denied",
		"chat endpoint is denied",
		"no active grok subscription",
		"subscription required",
		"permission-denied",
		"not entitled",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// Refresh refreshes xAI OAuth credentials using the stored refresh token.
func (e *XAIExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("xai executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, statusErr{code: http.StatusInternalServerError, msg: "xai executor: auth is nil"}
	}
	refreshToken := xaiMetadataString(auth.Metadata, "refresh_token")
	if refreshToken == "" {
		return auth, nil
	}
	tokenEndpoint := xaiMetadataString(auth.Metadata, "token_endpoint")
	svc := xaiauth.NewXAIAuthWithProxyURL(e.cfg, auth.ProxyURL)
	td, err := svc.RefreshTokens(ctx, refreshToken, tokenEndpoint)
	if err != nil {
		return nil, err
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["type"] = "xai"
	auth.Metadata["auth_kind"] = "oauth"
	auth.Metadata["access_token"] = td.AccessToken
	if td.RefreshToken != "" {
		auth.Metadata["refresh_token"] = td.RefreshToken
	}
	if td.IDToken != "" {
		auth.Metadata["id_token"] = td.IDToken
	}
	if td.TokenType != "" {
		auth.Metadata["token_type"] = td.TokenType
	}
	if td.ExpiresIn > 0 {
		auth.Metadata["expires_in"] = td.ExpiresIn
	}
	if td.Expire != "" {
		auth.Metadata["expired"] = td.Expire
	}
	if td.Email != "" {
		auth.Metadata["email"] = td.Email
	}
	if td.Subject != "" {
		auth.Metadata["sub"] = td.Subject
	}
	if tokenEndpoint != "" {
		auth.Metadata["token_endpoint"] = tokenEndpoint
	}
	if xaiMetadataString(auth.Metadata, "base_url") == "" {
		auth.Metadata["base_url"] = xaiauth.CLIChatProxyBaseURL
	}
	auth.Metadata["last_refresh"] = time.Now().UTC().Format(time.RFC3339)
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes["auth_kind"] = "oauth"
	if strings.TrimSpace(auth.Attributes["base_url"]) == "" {
		auth.Attributes["base_url"] = xaiauth.CLIChatProxyBaseURL
	}
	return auth, nil
}
