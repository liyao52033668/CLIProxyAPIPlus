package management

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	lobsterauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/lobsterai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// lobsterAIProvider is the provider key used by the OAuth session store and
	// the auth file "type" field.
	lobsterAIProvider = lobsterauth.ProviderID
	// lobsterAIWaitTimeout bounds the browser login and matches the OAuth
	// session TTL so the state cannot outlive the wait.
	lobsterAIWaitTimeout = 10 * time.Minute
)

// lobsterAIOAuthService is the upstream contract the handler needs. It is an
// interface so tests can stub the exchanges offline.
type lobsterAIOAuthService interface {
	BuildLoginURL(redirectURI, state string) string
	ExchangeCode(ctx context.Context, code, installationUUID string) (*lobsterauth.TokenPayload, error)
	FetchUsage(ctx context.Context, accessToken string) (*lobsterauth.Usage, error)
}

var newLobsterAIOAuthService = func(cfg *config.Config) lobsterAIOAuthService {
	client := util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second})
	return lobsterauth.NewService(client)
}

// lobsterAICallbackURL builds the redirect target the LobsterAI portal accepts.
// The portal strictly validates redirect_uri as an http://127.0.0.1:<port>
// /auth/callback URL, so the local server port is used and the browser must run
// where the server is reachable at that loopback address.
func (h *Handler) lobsterAICallbackURL() (string, error) {
	if h == nil || h.cfg == nil || h.cfg.Port <= 0 {
		return "", errors.New("server port is not configured")
	}
	return fmt.Sprintf("http://127.0.0.1:%d%s", h.cfg.Port, lobsterauth.CallbackPath()), nil
}

// RequestLobsterAIToken starts the LobsterAI browser login. The login URL is
// returned immediately; the background wait completes once the portal redirects
// back to /auth/callback (local browser) or the WebUI posts the callback URL to
// POST /oauth-callback (remote server).
func (h *Handler) RequestLobsterAIToken(c *gin.Context) {
	redirectURI, errRedirect := h.lobsterAICallbackURL()
	if errRedirect != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
		return
	}
	state, errState := misc.GenerateRandomState()
	if errState != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}
	installationUUID, errUUID := lobsterauth.NewInstallationUUID()
	if errUUID != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate installation uuid"})
		return
	}

	authSvc := newLobsterAIOAuthService(h.cfg)
	authURL := authSvc.BuildLoginURL(redirectURI, state)

	CompleteOAuthSessionsByProvider(lobsterAIProvider)
	RegisterOAuthSession(state, lobsterAIProvider)
	// The WebUI polls get-auth-status and needs the login URL for remote setups.
	SetOAuthSessionError(state, "auth_url|"+authURL)

	// Do not inherit the HTTP request cancellation: login continues after returning the URL.
	ctx := PopulateAuthContext(context.Background(), c)
	authDir := h.cfg.AuthDir
	go h.completeLobsterAIOAuth(ctx, authDir, state, installationUUID, authSvc)

	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": authURL, "state": state})
}

// completeLobsterAIOAuth waits for the authorization code, exchanges it, and
// persists the credential as a LobsterAI OAuth auth file.
func (h *Handler) completeLobsterAIOAuth(ctx context.Context, authDir, state, installationUUID string, authSvc lobsterAIOAuthService) {
	waitCtx, cancel := context.WithTimeout(ctx, lobsterAIWaitTimeout)
	defer cancel()
	go watchOAuthSessionCancel(waitCtx, cancel, state, lobsterAIProvider)

	payloadCallback, errWait := waitForOAuthCallbackFile(authDir, lobsterAIProvider, state, lobsterAIWaitTimeout)
	if errWait != nil {
		if errors.Is(errWait, errOAuthSessionNotPending) {
			return
		}
		if IsOAuthSessionPending(state, lobsterAIProvider) {
			SetOAuthSessionError(state, "Timeout waiting for LobsterAI login")
		}
		return
	}
	if errValidate := validateOAuthCallbackPayload(lobsterAIProvider, state, payloadCallback, true); errValidate != nil {
		return
	}

	payload, errExchange := authSvc.ExchangeCode(waitCtx, payloadCallback.Code, installationUUID)
	if errExchange != nil || payload == nil {
		log.Warnf("lobsterai login: code exchange failed: %v", errExchange)
		if IsOAuthSessionPending(state, lobsterAIProvider) {
			SetOAuthSessionError(state, "Failed to exchange the LobsterAI authorization code")
		}
		return
	}
	if !IsOAuthSessionPending(state, lobsterAIProvider) {
		return
	}

	// The quota snapshot is best effort: a failed lookup must not fail the login.
	usage, errUsage := authSvc.FetchUsage(waitCtx, payload.AccessToken)
	if errUsage != nil {
		log.Debugf("lobsterai login: usage lookup failed: %v", errUsage)
		usage = nil
	}
	record := lobsterauth.BuildAuthRecord(payload, installationUUID, "", usage)
	if record == nil {
		SetOAuthSessionError(state, "Failed to build the LobsterAI credential")
		return
	}
	if errGuard := guardOAuthSessionPendingForSave(state, lobsterAIProvider); errGuard != nil {
		return
	}
	if _, errSave := h.saveTokenRecord(waitCtx, record); errSave != nil {
		log.Errorf("lobsterai login: failed to save credential: %v", errSave)
		SetOAuthSessionError(state, "Failed to save authentication tokens")
		return
	}
	completeOAuthSuccess(state, lobsterAIProvider)
	log.Info("LobsterAI authentication successful")
}
