package management

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"

	// "github.com/router-for-me/CLIProxyAPI/v7/internal/browser"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	log "github.com/sirupsen/logrus"
)

func (h *Handler) RequestCodeArtsToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)
	_ = ctx

	state, errState := misc.GenerateRandomState()
	if errState != nil {
		log.Errorf("Failed to generate state parameter: %v", errState)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state"})
		return
	}

	RegisterOAuthSession(state, "codearts")

	callbackURL, errCallback := h.managementCallbackURL("/v0/oauth/codearts/status")
	if errCallback != nil {
		log.WithError(errCallback).Error("failed to compute codearts status url")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
		return
	}

	// Use CodeArts OAuth handler to generate the real authorization URL directly
	var authURL string
	if h.codeArtsOAuthHandler != nil {
		var err error
		authURL, err = h.codeArtsOAuthHandler.CreateSessionAndGetAuthURL(state)
		if err != nil {
			log.WithError(err).Error("failed to create CodeArts OAuth session")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create auth session"})
			return
		}
	} else {
		// Fallback to local start endpoint if handler is not available
		var err error
		authURL, err = h.managementCallbackURL("/v0/oauth/codearts/start")
		if err != nil {
			log.WithError(err).Error("failed to compute codearts login url")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
			return
		}
	}

	SetOAuthSessionError(state, "auth_url|"+callbackURL+"?state="+url.QueryEscape(state))

	// Start background task to wait for callback file (for remote deployment scenario)
	go h.waitForCodeArtsCallback(state)

	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"url":    authURL,
		"state":  state,
	})
}

// waitForCodeArtsCallback waits for the callback file written by PostOAuthCallback
// when users manually paste the redirect URL in remote deployment scenarios.
func (h *Handler) waitForCodeArtsCallback(state string) {
	if h.codeArtsOAuthHandler == nil {
		log.Warn("CodeArts OAuth handler not available, skipping callback wait")
		return
	}

	callbackPayload, errWait := waitForOAuthCallbackFile(h.cfg.AuthDir, "codearts", state, defaultOAuthCallbackWait)
	if errWait != nil {
		log.WithError(errWait).Warnf("CodeArts OAuth: wait callback file failed for state %s", state)
		return
	}

	if errValidate := validateOAuthCallbackPayload("codearts", state, callbackPayload, true); errValidate != nil {
		log.WithError(errValidate).Warnf("CodeArts OAuth: validate callback payload failed for state %s", state)
		return
	}

	// Exchange code for token using the web handler's session verifier
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	tokenResp, err := h.codeArtsOAuthHandler.ExchangeCodeForSession(ctx, state, callbackPayload.Code)
	if err != nil {
		SetOAuthSessionError(state, "Failed to exchange authorization code: "+err.Error())
		log.WithError(err).Errorf("CodeArts OAuth: exchange code failed for state %s", state)
		return
	}

	// Save auth file
	h.codeArtsOAuthHandler.SaveTokenFromResponse(state, tokenResp)

	// Complete the OAuth session
	completeOAuthSuccess(state, "codearts")
	log.Infof("CodeArts OAuth: callback processed successfully for state %s", state)
}
