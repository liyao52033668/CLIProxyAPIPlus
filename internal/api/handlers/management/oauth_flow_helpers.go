package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// defaultOAuthCallbackWait is how long management OAuth handlers wait for the
// browser callback file before marking the session timed out.
const defaultOAuthCallbackWait = 5 * time.Minute

// oauthCallbackPollInterval is the sleep between callback-file polls.
const oauthCallbackPollInterval = 500 * time.Millisecond

// errOAuthCallbackEmptyCode indicates the callback file lacked a code.
var errOAuthCallbackEmptyCode = errors.New("oauth callback code not found")

// errOAuthCallbackStateMismatch indicates the callback state did not match.
var errOAuthCallbackStateMismatch = errors.New("oauth callback state mismatch")

// oauthCallbackPayload is the result of waiting for a management OAuth callback file.
type oauthCallbackPayload struct {
	Code  string
	State string
	Error string
	Auth  string
	Raw   map[string]string
}

// startWebUICallbackForwarderIfNeeded starts a local callback forwarder when the
// request is from the management WebUI. The caller must stop the returned
// forwarder (typically via defer in the background goroutine).
func (h *Handler) startWebUICallbackForwarderIfNeeded(c *gin.Context, port int, provider, callbackPath string) (isWebUI bool, forwarder *callbackForwarder, err error) {
	if c == nil || !isWebUIRequest(c) {
		return false, nil, nil
	}
	if h == nil {
		return true, nil, fmt.Errorf("management handler is nil")
	}
	targetURL, errTarget := h.managementCallbackURL(callbackPath)
	if errTarget != nil {
		return true, nil, fmt.Errorf("compute %s callback target: %w", provider, errTarget)
	}
	fwd, errStart := startCallbackForwarder(port, provider, targetURL)
	if errStart != nil {
		return true, nil, fmt.Errorf("start %s callback forwarder: %w", provider, errStart)
	}
	return true, fwd, nil
}

// waitForOAuthCallbackFile polls AuthDir for the provider callback file written
// by the OAuth redirect handler. It respects session cancellation and timeout.
func waitForOAuthCallbackFile(authDir, provider, state string, timeout time.Duration) (*oauthCallbackPayload, error) {
	provider = strings.TrimSpace(provider)
	state = strings.TrimSpace(state)
	if provider == "" || state == "" {
		return nil, fmt.Errorf("oauth wait: provider and state are required")
	}
	if timeout <= 0 {
		timeout = defaultOAuthCallbackWait
	}
	path := filepath.Join(authDir, fmt.Sprintf(".oauth-%s-%s.oauth", provider, state))
	deadline := time.Now().Add(timeout)

	for {
		if !IsOAuthSessionPending(state, provider) {
			return nil, errOAuthSessionNotPending
		}
		if time.Now().After(deadline) {
			SetOAuthSessionError(state, "Timeout waiting for OAuth callback")
			return nil, fmt.Errorf("timeout waiting for OAuth callback")
		}
		data, errRead := os.ReadFile(path)
		if errRead != nil {
			time.Sleep(oauthCallbackPollInterval)
			continue
		}
		var raw map[string]string
		if errJSON := json.Unmarshal(data, &raw); errJSON != nil {
			_ = os.Remove(path)
			log.WithError(errJSON).Warnf("oauth callback file for %s is not valid json", provider)
			time.Sleep(oauthCallbackPollInterval)
			continue
		}
		_ = os.Remove(path)
		payload := &oauthCallbackPayload{
			Code:  strings.TrimSpace(raw["code"]),
			State: strings.TrimSpace(raw["state"]),
			Error: strings.TrimSpace(raw["error"]),
			Auth:  strings.TrimSpace(raw["auth"]),
			Raw:   raw,
		}
		return payload, nil
	}
}

// validateOAuthCallbackPayload applies the common post-wait checks used by most
// management OAuth handlers (error field, optional state match, non-empty code).
// On failure it sets the OAuth session error and returns a non-nil error.
func validateOAuthCallbackPayload(provider, expectedState string, payload *oauthCallbackPayload, requireCode bool) error {
	if payload == nil {
		SetOAuthSessionError(expectedState, "Authentication failed")
		return fmt.Errorf("%s oauth: empty callback payload", provider)
	}
	if payload.Error != "" {
		msg := "Authentication failed"
		if payload.Error != "" {
			msg = "Authentication failed: " + payload.Error
		}
		SetOAuthSessionError(expectedState, msg)
		return fmt.Errorf("%s oauth callback error: %s", provider, payload.Error)
	}
	if payload.State != "" && expectedState != "" && payload.State != expectedState {
		SetOAuthSessionError(expectedState, "Authentication failed: state mismatch")
		return errOAuthCallbackStateMismatch
	}
	if requireCode && payload.Code == "" {
		SetOAuthSessionError(expectedState, "Authentication failed: code not found")
		return errOAuthCallbackEmptyCode
	}
	return nil
}

// completeOAuthSuccess marks the session complete for both the state and provider.
func completeOAuthSuccess(state, provider string) {
	CompleteOAuthSession(state)
	CompleteOAuthSessionsByProvider(provider)
}

// waitForQoderCallback waits for a Qoder OAuth callback via either an in-process
// channel (Windows auto handler) or callback files under authDir (WebUI).
// Qoder may write either a state-specific file or a state-less fallback file.
func waitForQoderCallback(authDir, state string, cbChan <-chan qoderCallbackResult, timeout time.Duration) (tokenString, authField string, err error) {
	state = strings.TrimSpace(state)
	if state == "" {
		return "", "", fmt.Errorf("qoder oauth wait: state is required")
	}
	if timeout <= 0 {
		timeout = defaultOAuthCallbackWait
	}
	waitFiles := []string{
		filepath.Join(authDir, fmt.Sprintf(".oauth-qoder-%s.oauth", state)),
		filepath.Join(authDir, ".oauth-qoder-.oauth"),
	}
	deadline := time.Now().Add(timeout)

	for {
		if !IsOAuthSessionPending(state, "qoder") {
			return "", "", errOAuthSessionNotPending
		}
		if time.Now().After(deadline) {
			SetOAuthSessionError(state, "Timeout waiting for OAuth callback")
			return "", "", fmt.Errorf("timeout waiting for Qoder callback")
		}

		if cbChan != nil {
			select {
			case cbRes := <-cbChan:
				return strings.TrimSpace(cbRes.TokenString), strings.TrimSpace(cbRes.AuthField), nil
			default:
			}
		}

		for _, waitFile := range waitFiles {
			data, errRead := os.ReadFile(waitFile)
			if errRead != nil {
				continue
			}
			var m map[string]string
			if errJSON := json.Unmarshal(data, &m); errJSON != nil {
				_ = os.Remove(waitFile)
				log.WithError(errJSON).Warn("qoder oauth callback file is not valid json")
				continue
			}
			_ = os.Remove(waitFile)

			// New format: code carries the token directly; auth may hold user info.
			if code := strings.TrimSpace(m["code"]); code != "" {
				// Prefer direct token if it does not look like a query string.
				if !strings.Contains(code, "=") && !strings.Contains(code, "%3D") && !strings.Contains(code, "%3d") {
					return code, strings.TrimSpace(m["auth"]), nil
				}
				// Legacy format: code is a (possibly double-encoded) query string.
				qs, _ := url.QueryUnescape(code)
				parsed, errParse := url.ParseQuery(qs)
				if errParse == nil {
					token := ""
					for _, k := range []string{"tokenString", "token"} {
						if v := parsed.Get(k); v != "" {
							token = v
							break
						}
					}
					if token != "" {
						return token, strings.TrimSpace(parsed.Get("auth")), nil
					}
				}
				// Fall back to treating code as the raw token.
				return code, strings.TrimSpace(m["auth"]), nil
			}
		}

		time.Sleep(oauthCallbackPollInterval)
	}
}

func PopulateAuthContext(ctx context.Context, c *gin.Context) context.Context {
	info := &coreauth.RequestInfo{
		Query:   c.Request.URL.Query(),
		Headers: c.Request.Header,
	}
	return coreauth.WithRequestInfo(ctx, info)
}
