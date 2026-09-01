package management

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

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

	// Stash the authorization URL so the web UI can re-read it while polling.
	SetOAuthSessionError(state, "auth_url|"+authURL)

	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"url":    authURL,
		"state":  state,
	})
}
