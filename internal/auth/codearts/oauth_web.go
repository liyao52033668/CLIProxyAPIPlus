package codearts

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

// codeArtsTicketPollInterval is the snap-manager ticket poll cadence.
const codeArtsTicketPollInterval = 2 * time.Second

// LoginWindow bounds how long a session waits for the user to authorize. It must
// stay below the management OAuth session TTL (10 minutes) so the timeout is
// reported while that session still exists; otherwise the web UI only ever sees
// "unknown or expired state".
const LoginWindow = 8 * time.Minute

// CallbackGracePeriod bounds ticket polling after the callback secret arrives.
// The portal redirect has been followed by then, so the ticket should be
// claimable almost immediately.
const CallbackGracePeriod = 2 * time.Minute

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
	verifier  string
	challenge string
	status    sessionStatus
	startedAt time.Time
	error     string
	token     *CodeArtsTokenData
	tokenResp *TokenResponse
	cancel    context.CancelFunc
	// pollSecret holds the secret used for ticket polling. It starts as the
	// locally generated value and is replaced by the portal-issued secret once
	// the callback delivers it.
	pollSecret atomic.Value
	// pollTicketID holds the ticket id used for polling. The portal may mint its
	// own ticket and hand it back through the callback redirect, in which case
	// that one wins over the locally generated ticket.
	pollTicketID atomic.Value
	// deadline bounds the poll loop and is extended when the callback arrives.
	deadline atomic.Value
}

func (s *webSession) currentSecret() string {
	secret, _ := s.pollSecret.Load().(string)
	return secret
}

func (s *webSession) currentTicketID() string {
	ticketID, _ := s.pollTicketID.Load().(string)
	if ticketID == "" {
		return s.ticketID
	}
	return ticketID
}

func (s *webSession) deadlineAt() time.Time {
	deadline, _ := s.deadline.Load().(time.Time)
	return deadline
}

func (s *webSession) extendDeadline(d time.Duration) {
	s.deadline.Store(time.Now().Add(d))
}

// AuthSuccessCallback is called when authentication is successful.
type AuthSuccessCallback func(stateID string)

// AuthFailureCallback is called when authentication fails or times out, so the
// management OAuth session can stop reporting the flow as pending.
type AuthFailureCallback func(stateID string, err error)

// OAuthWebHandler handles CodeArts OAuth web login flow.
type OAuthWebHandler struct {
	cfg      *config.Config
	sessions map[string]*webSession
	// Map ticket_id -> stateID for callback lookup
	ticketToState       map[string]string
	mu                  sync.RWMutex
	auth                *CodeArtsAuth
	authSuccessCallback AuthSuccessCallback
	authFailureCallback AuthFailureCallback
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

// SetAuthFailureCallback sets the callback to be called when authentication fails.
func (h *OAuthWebHandler) SetAuthFailureCallback(callback AuthFailureCallback) {
	h.authFailureCallback = callback
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
// Ticket polling starts immediately, before the user opens the URL, so the login can
// complete without the browser ever reaching a local callback listener.
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

	port := h.callbackPort()

	sess := &webSession{
		stateID:   stateID,
		ticketID:  ticketID,
		verifier:  verifier,
		challenge: challenge,
		status:    sWaitingCB,
		startedAt: time.Now(),
	}
	sess.pollSecret.Store(secret)
	sess.pollTicketID.Store(ticketID)
	sess.extendDeadline(LoginWindow)

	ctx, cancel := context.WithCancel(context.Background())
	sess.cancel = cancel

	h.mu.Lock()
	h.sessions[stateID] = sess
	h.ticketToState[ticketID] = stateID
	h.mu.Unlock()

	loginURL := BuildAuthorizeURL(ticketID, challenge, port)

	go h.runTicketPoll(ctx, cancel, sess)

	log.Infof("CodeArts OAuth: session %s started (ticket polling)", stateID)

	return loginURL, nil
}

// callbackPort returns the port advertised to the portal as the local callback port.
func (h *OAuthWebHandler) callbackPort() int {
	if h.cfg != nil && h.cfg.Port != 0 {
		return h.cfg.Port
	}
	return 8318
}

// runTicketPoll polls the snap-manager login ticket endpoint until credentials are
// available, the deadline passes, or the context is cancelled. It owns the session
// lifecycle: on success it persists the token, and either way it notifies listeners
// so the polling web UI stops waiting.
func (h *OAuthWebHandler) runTicketPoll(ctx context.Context, cancel context.CancelFunc, sess *webSession) {
	defer cancel()

	tokenResp, err := h.pollTicketForSession(ctx, sess)
	if err != nil {
		h.failSession(sess, err)
		return
	}

	h.finishSession(sess, tokenResp)
}

// failSession marks the session failed and notifies listeners.
func (h *OAuthWebHandler) failSession(sess *webSession, err error) {
	h.mu.Lock()
	alreadyDone := sess.status == sSuccess || sess.status == sFailed
	if !alreadyDone {
		sess.status = sFailed
		sess.error = err.Error()
	}
	h.mu.Unlock()

	if alreadyDone {
		return
	}

	log.Errorf("CodeArts OAuth: login failed for state %s: %v", sess.stateID, err)
	if h.authFailureCallback != nil {
		h.authFailureCallback(sess.stateID, err)
	}
}

// finishSession stores the credentials, persists the auth file and notifies listeners.
func (h *OAuthWebHandler) finishSession(sess *webSession, tokenResp *TokenResponse) {
	tokenData := h.tokenDataFromResponse(tokenResp, sess.verifier)

	h.mu.Lock()
	alreadyDone := sess.status == sSuccess
	sess.status = sSuccess
	sess.token = tokenData
	sess.tokenResp = tokenResp
	h.mu.Unlock()

	if alreadyDone {
		return
	}

	h.saveTokenToFile(tokenData)
	log.Infof("CodeArts OAuth: authentication successful for user %s (state %s)", tokenData.UserName, sess.stateID)

	if h.authSuccessCallback != nil {
		h.authSuccessCallback(sess.stateID)
	}
}

// handleCallback receives the callback from HuaweiCloud after user login.
// Two channels are supported:
//   - ticket-polling channel: query carries secret + redirect (nested ticket_id).
//     The secret is handed to the session's poller and the browser is bounced back
//     to the portal so the login can finalize; this handler never blocks on polling.
//   - PKCE channel: query carries code → ExchangeCode with the session verifier.
func (h *OAuthWebHandler) handleCallback(c *gin.Context) {
	code := c.Query("code")
	secret := c.Query("secret")
	redirectURL := c.Query("redirect")
	errMsg := c.Query("error")

	if errMsg != "" {
		log.Errorf("CodeArts OAuth callback error: %s", errMsg)
		h.renderCallbackFailure(c, "Error: "+errMsg)
		return
	}

	ticketID := ticketIDFromRedirect(redirectURL)
	sess := h.lookupSession(ticketID)
	if sess == nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "no pending session found"})
		return
	}

	if secret != "" {
		// Ticket-polling channel: the portal issues its own secret; hand it to the
		// already-running poller and send the browser back to finalize the login.
		h.acceptCallbackSecret(sess, ticketID, secret)
		if redirectURL != "" {
			validated, err := ValidatePortalRedirect(redirectURL)
			if err != nil {
				log.Warnf("CodeArts OAuth: refusing to follow callback redirect: %v", err)
				h.renderCallbackFailure(c, "The callback redirect target is not a HuaweiCloud address.")
				return
			}
			h.renderCallbackRedirect(c, validated)
			return
		}
		h.renderCallbackSuccess(c)
		return
	}

	if code == "" {
		h.renderCallbackFailure(c, "Callback carried neither an authorization code nor a ticket secret.")
		return
	}

	// PKCE authorization-code channel.
	h.markPolling(sess)
	tokenResp, err := h.auth.ExchangeCode(c.Request.Context(), code, sess.verifier, h.callbackPort())
	if err != nil {
		h.failSession(sess, err)
		h.renderCallbackFailure(c, "Authentication failed. Please try again.")
		return
	}

	h.finishSession(sess, tokenResp)
	h.renderCallbackSuccess(c)
}

// lookupSession resolves a session by the ticket id carried in the callback,
// falling back to the sole pending session when the ticket is absent.
func (h *OAuthWebHandler) lookupSession(ticketID string) *webSession {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if ticketID != "" {
		if stateID, ok := h.ticketToState[ticketID]; ok {
			if sess, okSess := h.sessions[stateID]; okSess {
				return sess
			}
		}
		return nil
	}

	// Without a ticket id the callback can only be attributed unambiguously when
	// exactly one session is waiting.
	var candidate *webSession
	for _, sess := range h.sessions {
		if sess.status != sWaitingCB && sess.status != sPolling {
			continue
		}
		if candidate != nil {
			return nil
		}
		candidate = sess
	}
	return candidate
}

// acceptCallbackSecret records the portal-issued secret and ticket id, moves the
// session into the polling state and extends the poll deadline.
func (h *OAuthWebHandler) acceptCallbackSecret(sess *webSession, ticketID, secret string) {
	sess.pollSecret.Store(secret)
	if ticketID != "" {
		sess.pollTicketID.Store(ticketID)
	}
	sess.extendDeadline(CallbackGracePeriod)
	h.markPolling(sess)
}

// markPolling moves a waiting session into the polling state.
func (h *OAuthWebHandler) markPolling(sess *webSession) {
	h.mu.Lock()
	if sess.status == sWaitingCB {
		sess.status = sPolling
	}
	h.mu.Unlock()
}

// ticketIDFromRedirect extracts the ticket_id nested in the callback redirect param.
func ticketIDFromRedirect(redirectURL string) string {
	if redirectURL == "" {
		return ""
	}
	parsed, err := url.Parse(redirectURL)
	if err != nil {
		return ""
	}
	return parsed.Query().Get("ticket_id")
}

// SubmitPastedCallback records the portal-issued secret from a pasted callback URL
// and returns the portal redirect the user still has to open. In a remote
// deployment the browser never reaches the loopback callback, so nothing has
// followed that redirect yet — and only the user's browser can, because it carries
// the portal session. The caller is expected to surface the returned URL.
func (h *OAuthWebHandler) SubmitPastedCallback(stateID, ticketID, secret, redirectURL string) (string, error) {
	if secret == "" {
		return "", fmt.Errorf("codearts: callback secret is required")
	}

	h.mu.RLock()
	sess, ok := h.sessions[stateID]
	h.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("codearts: session not found for state %s", stateID)
	}
	if ticketID != "" && ticketID != sess.ticketID {
		return "", fmt.Errorf("codearts: ticket mismatch for state %s", stateID)
	}

	finalizeURL := ""
	if redirectURL != "" {
		validated, err := ValidatePortalRedirect(redirectURL)
		if err != nil {
			return "", err
		}
		finalizeURL = validated
	}

	h.acceptCallbackSecret(sess, ticketID, secret)

	if finalizeURL == "" {
		log.Warnf("CodeArts OAuth: pasted callback for state %s carries no redirect; the portal login may never finalize", stateID)
		return "", nil
	}
	log.Infof("CodeArts OAuth: pasted callback accepted for state %s, awaiting portal finalization", stateID)
	return finalizeURL, nil
}

func (h *OAuthWebHandler) renderCallbackRedirect(c *gin.Context, redirectURL string) {
	escaped := html.EscapeString(redirectURL)
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>Completing login</title>`+
		`<meta http-equiv="refresh" content="0; url=`+escaped+`"></head>`+
		`<body style="display:flex;justify-content:center;align-items:center;min-height:100vh;margin:0;font-family:sans-serif;background:#f5f5f5">`+
		`<div style="text-align:center;padding:40px;background:white;border-radius:12px;box-shadow:0 2px 10px rgba(0,0,0,0.1)">`+
		`<h1>Completing login...</h1><p>Redirecting to HuaweiCloud to finish. If nothing happens, `+
		`<a href="`+escaped+`">click here</a>.</p></div></body></html>`)
}

func (h *OAuthWebHandler) renderCallbackSuccess(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>Authentication successful</title></head><body style="display:flex;justify-content:center;align-items:center;min-height:100vh;margin:0;font-family:sans-serif;background:#f5f5f5"><div style="text-align:center;padding:40px;background:white;border-radius:12px;box-shadow:0 2px 10px rgba(0,0,0,0.1)"><h1>&#x2705; Authentication successful!</h1><p>You can close this tab.</p><p style="color:#666;font-size:14px">You may safely close this window or tab now.</p></div></body></html>`)
}

func (h *OAuthWebHandler) renderCallbackFailure(c *gin.Context, detail string) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>Authentication failed</title></head><body style="display:flex;justify-content:center;align-items:center;min-height:100vh;margin:0;font-family:sans-serif;background:#f5f5f5"><div style="text-align:center;padding:40px;background:white;border-radius:12px;box-shadow:0 2px 10px rgba(0,0,0,0.1)"><h1>&#x274c; Authentication failed</h1><p>`+html.EscapeString(detail)+`</p></div></body></html>`)
}

// pollTicketForSession polls the snap-manager login ticket endpoint, re-reading the
// session secret, ticket id and deadline on every tick so the callback can update
// them while the loop is running.
func (h *OAuthWebHandler) pollTicketForSession(ctx context.Context, sess *webSession) (*TokenResponse, error) {
	ticker := time.NewTicker(codeArtsTicketPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			if deadline := sess.deadlineAt(); !deadline.IsZero() && time.Now().After(deadline) {
				return nil, fmt.Errorf("codearts: login timed out waiting for authorization")
			}
			tr, errPoll := h.auth.PollLoginTicket(ctx, sess.currentTicketID(), sess.currentSecret())
			if errPoll != nil {
				log.Debugf("codearts: ticket poll error for state %s: %v", sess.stateID, errPoll)
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
