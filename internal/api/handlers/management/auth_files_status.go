package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	// "github.com/router-for-me/CLIProxyAPI/v7/internal/browser"

	log "github.com/sirupsen/logrus"
)

func (h *Handler) GetAuthStatus(c *gin.Context) {
	state := strings.TrimSpace(c.Query("state"))
	if state == "" {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}

	if err := ValidateOAuthState(state); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid state"})
		return
	}

	provider, status, ok := GetOAuthSession(state)
	log.Infof("GetAuthStatus: state=%s, provider=%s, status=%s, ok=%v", state, provider, status, ok)
	if !ok {
		if IsOAuthSessionCompleted(state) {
			c.JSON(http.StatusOK, gin.H{"status": "ok"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "error", "error": "unknown or expired state"})
		return
	}
	if status != "" {
		log.Infof("GetAuthStatus: status is not empty, checking prefixes: %s", status)
		if strings.HasPrefix(status, "device_code|") {
			log.Infof("GetAuthStatus: status has device_code| prefix")
			parts := strings.SplitN(status, "|", 3)
			if len(parts) == 3 {
				c.JSON(http.StatusOK, gin.H{
					"status":           "device_code",
					"verification_url": parts[1],
					"user_code":        parts[2],
				})
				return
			}
		}
		if strings.HasPrefix(status, "auth_url|") {
			log.Infof("GetAuthStatus: status has auth_url| prefix, returning auth_url")
			authURL := strings.TrimPrefix(status, "auth_url|")
			c.JSON(http.StatusOK, gin.H{
				"status": "auth_url",
				"url":    authURL,
			})
			return
		}
		log.Infof("GetAuthStatus: status is an error: %s", status)
		c.JSON(http.StatusOK, gin.H{"status": "error", "error": status})
		return
	}
	log.Infof("GetAuthStatus: status is empty, returning wait")
	c.JSON(http.StatusOK, gin.H{"status": "wait"})
}

// PopulateAuthContext extracts request info and adds it to the context
