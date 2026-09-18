package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	managementHandlers "github.com/router-for-me/CLIProxyAPI/v7/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
)

// lobsterAICallbackPath is the only loopback redirect target the LobsterAI login
// portal accepts. The portal refuses any other host or path and silently falls
// back to its desktop deep link, which a headless proxy cannot receive.
const lobsterAICallbackPath = "/auth/callback"

// lobsterAICallbackMiddleware claims the LobsterAI loopback redirect.
//
// The Amp integration module proxies the whole /auth/* tree for its own OAuth
// flow, so a plain route registration on /auth/callback would panic at startup
// with a wildcard conflict. This middleware runs first and only acts when the
// request carries a pending LobsterAI session; every other /auth request falls
// through untouched to the routed handler.
func lobsterAICallbackMiddleware(authDir string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request == nil || c.Request.Method != http.MethodGet {
			c.Next()
			return
		}
		if c.Request.URL.Path != lobsterAICallbackPath {
			c.Next()
			return
		}
		state := strings.TrimSpace(c.Query("state"))
		code := strings.TrimSpace(c.Query("code"))
		errStr := strings.TrimSpace(c.Query("error"))
		if errStr == "" {
			errStr = strings.TrimSpace(c.Query("error_description"))
		}
		// Without a state there is no LobsterAI session to complete, and a
		// request with neither code nor error is not a callback at all.
		if state == "" || (code == "" && errStr == "") {
			c.Next()
			return
		}
		if !managementHandlers.IsOAuthSessionPending(state, constant.LobsterAI) {
			c.Next()
			return
		}

		c.Header("Cache-Control", "no-store")
		if _, errWrite := managementHandlers.WriteOAuthCallbackFileForPendingSession(authDir, constant.LobsterAI, state, code, errStr); errWrite != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid or expired OAuth callback"})
			c.Abort()
			return
		}
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, oauthCallbackSuccessHTML)
		c.Abort()
	}
}
