package codearts

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

type sessionStatus string

const (
	sPending   sessionStatus = "pending"
	sWaitingCB sessionStatus = "waiting_callback"
	sPolling   sessionStatus = "polling"
	sSuccess   sessionStatus = "success"
	sFailed    sessionStatus = "failed"
)

type webSession struct {
	stateID   string
	ticketID  string
	secret    string
	verifier  string
	challenge string
	status    sessionStatus
	startedAt time.Time
	error     string
	token     *CodeArtsTokenData
	tokenResp *TokenResponse
	cancel    context.CancelFunc
}

// AuthSuccessCallback is called when authentication is successful.
type AuthSuccessCallback func(stateID string)

// OAuthWebHandler handles CodeArts OAuth web login flow.
type OAuthWebHandler struct {
	cfg      *config.Config
	sessions map[string]*webSession
	// Map ticket_id -> stateID for callback lookup
	ticketToState       map[string]string
	mu                  sync.RWMutex
	auth                *CodeArtsAuth
	authSuccessCallback AuthSuccessCallback
}

// NewOAuthWebHandler creates a new CodeArts OAuth web handler.
func NewOAuthWebHandler(cfg *config.Config) *OAuthWebHandler {
	return &OAuthWebHandler{
		cfg:           cfg,
		sessions:      make(map[string]*webSession),
		ticketToState: make(map[string]string),
		auth:          NewCodeArtsAuth(nil),
	}
}

// SetAuthSuccessCallback sets the callback to be called when authentication is successful.
func (h *OAuthWebHandler) SetAuthSuccessCallback(callback AuthSuccessCallback) {
	h.authSuccessCallback = callback
}

// RegisterRoutes registers CodeArts OAuth web routes.
func (h *OAuthWebHandler) RegisterRoutes(router gin.IRouter, protectedMiddleware ...gin.HandlerFunc) {
	oauth := router.Group("/v0/oauth/codearts")
	oauth.GET("/callback", h.handleCallback)
	oauth.GET("/status", h.handleStatus)

	protected := oauth.Group("")
	protected.Use(protectedMiddleware...)
	protected.GET("", h.handleIndex)
	protected.GET("/start", h.handleStart)

	// HuaweiCloud redirects to http://127.0.0.1:{port}/oauth/callback (PKCE flow).
	router.GET("/oauth/callback", h.handleCallback)
}

func generateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (h *OAuthWebHandler) handleIndex(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, codeArtsLoginPage)
}

func (h *OAuthWebHandler) handleStart(c *gin.Context) {
	stateID, err := generateState()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state"})
		return
	}

	loginURL, err := h.CreateSessionAndGetAuthURL(stateID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create session"})
		return
	}

	log.Infof("CodeArts OAuth: session %s started", stateID)

	if c.GetHeader("Accept") == "application/json" {
		c.JSON(http.StatusOK, gin.H{"url": loginURL, "state": stateID})
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, fmt.Sprintf(codeArtsWaitingPage, loginURL, stateID))
}

// CreateSessionAndGetAuthURL creates a new session and returns the CodeArts authorization URL.
// This method is exposed for use by management API handlers.
func (h *OAuthWebHandler) CreateSessionAndGetAuthURL(stateID string) (string, error) {
	ticketID, err := RandomHex(16)
	if err != nil {
		return "", fmt.Errorf("codearts: generate ticket id: %w", err)
	}
	secret, err := RandomHex(16)
	if err != nil {
		return "", fmt.Errorf("codearts: generate ticket secret: %w", err)
	}
	verifier, challenge, err := PKCE()
	if err != nil {
		return "", fmt.Errorf("codearts: generate PKCE pair: %w", err)
	}

	port := h.cfg.Port
	if port == 0 {
		port = 8318
	}

	sess := &webSession{
		stateID:   stateID,
		ticketID:  ticketID,
		secret:    secret,
		verifier:  verifier,
		challenge: challenge,
		status:    sWaitingCB,
		startedAt: time.Now(),
	}

	h.mu.Lock()
	h.sessions[stateID] = sess
	h.ticketToState[ticketID] = stateID
	h.mu.Unlock()

	loginURL := BuildAuthorizeURL(ticketID, challenge, port)

	log.Infof("CodeArts OAuth: session %s started (PKCE flow)", stateID)

	return loginURL, nil
}

// handleCallback receives the callback from HuaweiCloud after user login.
// Two channels are supported:
//   - PKCE channel: query carries code (+ error) → ExchangeCode with verifier
//   - ticket-polling channel: query carries secret + redirect (nested ticket_id)
//     → PollLoginTicket until credentials are ready
func (h *OAuthWebHandler) handleCallback(c *gin.Context) {
	code := c.Query("code")
	secret := c.Query("secret")
	redirectURL := c.Query("redirect")
	errMsg := c.Query("error")

	if errMsg != "" {
		log.Errorf("CodeArts OAuth callback error: %s", errMsg)
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>Authentication failed</title></head><body style="display:flex;justify-content:center;align-items:center;min-height:100vh;margin:0;font-family:sans-serif;background:#f5f5f5"><div style="text-align:center;padding:40px;background:white;border-radius:12px;box-shadow:0 2px 10px rgba(0,0,0,0.1)"><h1>❌ Authentication failed</h1><p>Error: `+errMsg+`</p></div></body></html>`)
		return
	}

	// Find a pending session to complete.
	h.mu.RLock()
	var matchedSess *webSession
	var matchedStateID string
	for stateID, sess := range h.sessions {
		if sess.status == sWaitingCB {
			matchedSess = sess
			matchedStateID = stateID
			break
		}
	}
	h.mu.RUnlock()

	if matchedSess == nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "no pending session found"})
		return
	}

	h.mu.Lock()
	matchedSess.status = sPolling
	h.mu.Unlock()

	port := h.cfg.Port
	if port == 0 {
		port = 8318
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var tokenResp *TokenResponse
	var err error

	if code != "" {
		// PKCE authorization-code channel.
		tokenResp, err = h.auth.ExchangeCode(ctx, code, matchedSess.verifier, port)
	} else if secret != "" {
		// Ticket-polling fallback channel: use ticket_id from the nested redirect.
		ticketID := matchedSess.ticketID
		if parsed, parseErr := url.Parse(redirectURL); parseErr == nil {
			if tid := parsed.Query().Get("ticket_id"); tid != "" {
				ticketID = tid
			}
		}
		tokenResp, err = h.pollTicket(ctx, ticketID, secret)
	} else {
		err = fmt.Errorf("codearts: callback missing both code and secret")
	}

	if err != nil {
		h.mu.Lock()
		matchedSess.status = sFailed
		matchedSess.error = err.Error()
		h.mu.Unlock()
		log.Errorf("CodeArts OAuth: authentication failed: %v", err)
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>Authentication failed</title></head><body style="display:flex;justify-content:center;align-items:center;min-height:100vh;margin:0;font-family:sans-serif;background:#f5f5f5"><div style="text-align:center;padding:40px;background:white;border-radius:12px;box-shadow:0 2px 10px rgba(0,0,0,0.1)"><h1>❌ Authentication failed</h1><p>Authentication failed. Please try again.</p></div></body></html>`)
		return
	}

	tokenData := h.tokenDataFromResponse(tokenResp, matchedSess.verifier)

	h.mu.Lock()
	matchedSess.status = sSuccess
	matchedSess.token = tokenData
	matchedSess.tokenResp = tokenResp
	h.mu.Unlock()

	// Save auth file
	h.saveTokenToFile(tokenData)
	log.Infof("CodeArts OAuth: authentication successful for user %s", tokenData.UserName)

	// Call the success callback if registered
	if h.authSuccessCallback != nil {
		h.authSuccessCallback(matchedStateID)
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>Authentication successful</title></head><body style="display:flex;justify-content:center;align-items:center;min-height:100vh;margin:0;font-family:sans-serif;background:#f5f5f5"><div style="text-align:center;padding:40px;background:white;border-radius:12px;box-shadow:0 2px 10px rgba(0,0,0,0.1)"><h1>✅ Authentication successful!</h1><p>You can close this tab.</p><p style="color:#666;font-size:14px">You may safely close this window or tab now.</p></div></body></html>`)
}

// pollTicket polls the snap-manager login ticket endpoint until credentials are
// available or the context is cancelled.
func (h *OAuthWebHandler) pollTicket(ctx context.Context, ticketID, secret string) (*TokenResponse, error) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			tr, errPoll := h.auth.PollLoginTicket(ctx, ticketID, secret)
			if errPoll != nil {
				continue
			}
			if tr != nil && tr.Credentials.SecurityToken != "" {
				return tr, nil
			}
		}
	}
}

// tokenDataFromResponse converts a TokenResponse into CodeArtsTokenData,
// preserving the PKCE code_verifier for refresh_token-based renewal.
func (h *OAuthWebHandler) tokenDataFromResponse(tokenResp *TokenResponse, verifier string) *CodeArtsTokenData {
	expiresAt, _ := time.Parse(time.RFC3339, tokenResp.Credentials.Expiration)
	return &CodeArtsTokenData{
		AK:            tokenResp.Credentials.AccessKeyID,
		SK:            tokenResp.Credentials.SecretAccessKey,
		SecurityToken: tokenResp.Credentials.SecurityToken,
		ExpiresAt:     expiresAt,
		RefreshToken:  tokenResp.RefreshToken,
		CodeVerifier:  verifier,
		UserID:        tokenResp.UserID,
		UserName:      tokenResp.UserName,
		DomainID:      tokenResp.DomainID,
	}
}

func (h *OAuthWebHandler) handleStatus(c *gin.Context) {
	stateID := c.Query("state")
	if stateID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing state"})
		return
	}

	h.mu.RLock()
	sess, ok := h.sessions[stateID]
	h.mu.RUnlock()

	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}

	switch sess.status {
	case sSuccess:
		msg := "Login successful! Token saved."
		if sess.token != nil && sess.token.UserName != "" {
			msg = fmt.Sprintf("Login successful! User: %s", sess.token.UserName)
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "message": msg})
	case sFailed:
		c.JSON(http.StatusOK, gin.H{"status": "failed", "error": sess.error})
	case sPolling:
		c.JSON(http.StatusOK, gin.H{"status": "pending", "message": "Polling for login result..."})
	default:
		c.JSON(http.StatusOK, gin.H{"status": "pending", "message": "Waiting for browser callback..."})
	}
}

func (h *OAuthWebHandler) saveTokenToFile(tokenData *CodeArtsTokenData) {
	authDir := ""
	if h.cfg != nil && h.cfg.AuthDir != "" {
		var err error
		authDir, err = util.ResolveAuthDir(h.cfg.AuthDir)
		if err != nil {
			log.Errorf("CodeArts OAuth: failed to resolve auth directory: %v", err)
		}
	}
	if authDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Errorf("CodeArts OAuth: failed to get home directory: %v", err)
			return
		}
		authDir = filepath.Join(home, ".cli-proxy-api")
	}
	if err := os.MkdirAll(authDir, 0700); err != nil {
		log.Errorf("CodeArts OAuth: failed to create auth directory: %v", err)
		return
	}

	fileName := "codearts-token.json"
	if tokenData.UserName != "" {
		fileName = fmt.Sprintf("codearts-%s.json", tokenData.UserName)
	}

	// Save in the same format as the file synthesizer expects:
	// { "type": "codearts", ... }
	storage := map[string]interface{}{
		"type":           "codearts",
		"ak":             tokenData.AK,
		"sk":             tokenData.SK,
		"security_token": tokenData.SecurityToken,
		"expires_at":     tokenData.ExpiresAt.Format(time.RFC3339),
		"refresh_token":  tokenData.RefreshToken,
		"code_verifier":  tokenData.CodeVerifier,
		"user_id":        tokenData.UserID,
		"user_name":      tokenData.UserName,
		"domain_id":      tokenData.DomainID,
		"last_refresh":   time.Now().Format(time.RFC3339),
	}

	data, err := json.MarshalIndent(storage, "", "  ")
	if err != nil {
		log.Errorf("CodeArts OAuth: failed to marshal token: %v", err)
		return
	}

	authFilePath := filepath.Join(authDir, fileName)
	if err := os.WriteFile(authFilePath, data, 0600); err != nil {
		log.Errorf("CodeArts OAuth: failed to write auth file: %v", err)
		return
	}
	log.Infof("CodeArts OAuth: token saved to %s", authFilePath)
}

// ExchangeCodeForSession exchanges an authorization code for a token using the
// session's PKCE verifier. This is used by the management API when users manually
// paste the redirect URL in remote deployment scenarios.
func (h *OAuthWebHandler) ExchangeCodeForSession(ctx context.Context, stateID, code string) (*TokenResponse, error) {
	h.mu.RLock()
	sess, ok := h.sessions[stateID]
	h.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("codearts: session not found for state %s", stateID)
	}

	if sess.verifier == "" {
		return nil, fmt.Errorf("codearts: session %s has no PKCE verifier", stateID)
	}

	port := h.cfg.Port
	if port == 0 {
		port = 8318
	}

	tokenResp, err := h.auth.ExchangeCode(ctx, code, sess.verifier, port)
	if err != nil {
		return nil, err
	}

	tokenData := h.tokenDataFromResponse(tokenResp, sess.verifier)

	h.mu.Lock()
	sess.status = sSuccess
	sess.token = tokenData
	sess.tokenResp = tokenResp
	h.mu.Unlock()

	return tokenResp, nil
}

// CompleteWithTicketPoll completes the OAuth flow by polling the snap-manager
// login ticket endpoint with the secret received from the HuaweiCloud callback.
// This is used by the management API when users manually paste the redirect URL
// in remote deployment scenarios (the callback URL carries secret + redirect, not code).
func (h *OAuthWebHandler) CompleteWithTicketPoll(ctx context.Context, stateID, ticketID, secret string) (*TokenResponse, error) {
	h.mu.RLock()
	sess, ok := h.sessions[stateID]
	h.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("codearts: session not found for state %s", stateID)
	}

	// Prefer the ticket_id from the callback URL; fall back to session's ticket_id.
	if ticketID == "" {
		ticketID = sess.ticketID
	}

	tokenResp, err := h.pollTicket(ctx, ticketID, secret)
	if err != nil {
		return nil, err
	}

	tokenData := h.tokenDataFromResponse(tokenResp, sess.verifier)

	h.mu.Lock()
	sess.status = sSuccess
	sess.token = tokenData
	sess.tokenResp = tokenResp
	h.mu.Unlock()

	return tokenResp, nil
}

// SaveTokenFromResponse saves the token data from a TokenResponse to the auth file.
// This is used by the management API after ExchangeCodeForSession.
func (h *OAuthWebHandler) SaveTokenFromResponse(stateID string, tokenResp *TokenResponse) {
	h.mu.RLock()
	sess, ok := h.sessions[stateID]
	h.mu.RUnlock()

	if !ok || sess.token == nil {
		log.Warnf("CodeArts OAuth: cannot save token for state %s - session or token not found", stateID)
		return
	}

	h.saveTokenToFile(sess.token)
	log.Infof("CodeArts OAuth: token saved for user %s (state %s)", sess.token.UserName, stateID)
}

// HTML templates
const codeArtsLoginPage = `<!DOCTYPE html>
<html lang="en"><head><title>CodeArts IDE Login</title>
<style>
body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; display: flex; justify-content: center; align-items: center; min-height: 100vh; margin: 0; background: #f5f5f5; }
.card { background: white; border-radius: 12px; padding: 40px; box-shadow: 0 2px 10px rgba(0,0,0,0.1); text-align: center; max-width: 400px; }
h1 { color: #333; margin-bottom: 10px; }
p { color: #666; margin-bottom: 20px; }
a.btn { display: inline-block; background: #e53935; color: white; padding: 12px 32px; border-radius: 8px; text-decoration: none; font-size: 16px; }
a.btn:hover { background: #c62828; }
</style></head><body>
<div class="card">
<h1>&#x1f511; CodeArts IDE Login</h1>
<p>Login with your HuaweiCloud account to use CodeArts IDE models through CLIProxyAPI.</p>
<a class="btn" href="/v0/oauth/codearts/start">Start Login</a>
</div></body></html>`

const codeArtsWaitingPage = `<!DOCTYPE html>
<html lang="en"><head><title>CodeArts IDE Login - Waiting</title>
<style>
body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; display: flex; justify-content: center; align-items: center; min-height: 100vh; margin: 0; background: #f5f5f5; }
.card { background: white; border-radius: 12px; padding: 40px; box-shadow: 0 2px 10px rgba(0,0,0,0.1); text-align: center; max-width: 500px; }
h1 { color: #333; margin-bottom: 10px; }
p { color: #666; margin-bottom: 20px; }
a.btn { display: inline-block; background: #e53935; color: white; padding: 12px 32px; border-radius: 8px; text-decoration: none; font-size: 16px; margin-bottom: 20px; }
a.btn:hover { background: #c62828; }
#status { padding: 12px; border-radius: 8px; background: #fff3e0; color: #e65100; }
.success { background: #e8f5e9 !important; color: #2e7d32 !important; }
.failed { background: #ffebee !important; color: #c62828 !important; }
</style></head><body>
<div class="card">
<h1>&#x1f511; CodeArts IDE Login</h1>
<p>Click the button below to open HuaweiCloud login page. After login, you will be redirected back here.</p>
<a class="btn" href="%s" target="_blank">Open HuaweiCloud Login</a>
<div id="status" role="status" aria-live="polite" aria-atomic="true">&#x23f3; Waiting for login callback...</div>
</div>
<script>
var stateID = "%s";
function poll() {
  fetch("/v0/oauth/codearts/status?state=" + stateID)
    .then(function(r) { return r.json(); })
    .then(function(data) {
      var el = document.getElementById("status");
      if (data.status === "success") {
        el.className = "success";
        el.textContent = "\u2705 " + data.message;
      } else if (data.status === "failed") {
        el.className = "failed";
        el.textContent = "\u274c Error: " + data.error;
      } else {
        el.textContent = "\u23f3 " + (data.message || "Waiting...");
        setTimeout(poll, 3000);
      }
    })
    .catch(function() { setTimeout(poll, 5000); });
}
poll();
</script></body></html>`
