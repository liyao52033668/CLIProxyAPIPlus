package management

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// GetJoyCodeAuthURL generates a JoyCode OAuth login URL.
//
// Endpoint:
//
//	GET /v0/management/joycode-auth-url
//
// Query Parameters (optional):
//   - port: Custom port for callback (default: 8318 or configured port)
//
// Response:
//
//	Returns the JoyCode login URL and state for polling status.
func (h *Handler) GetJoyCodeAuthURL(c *gin.Context) {
	// Generate state ID for tracking the OAuth session
	stateID, err := generateJCState()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state"})
		return
	}

	// Register OAuth session with management
	RegisterOAuthSession(stateID, "joycode")

	// Use OAuthWebHandler if available, otherwise fallback to simple URL generation
	if h.joyCodeOAuthHandler != nil {
		loginURL, err := h.joyCodeOAuthHandler.CreateSessionAndGetAuthURL(stateID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create session"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"url":   loginURL,
			"state": stateID,
		})
		return
	}

	// Fallback: simple URL generation without session management
	port := h.cfg.Port
	if port == 0 {
		port = 8318
	}

	// Read custom port from query if provided
	customPort := c.Query("port")
	if customPort != "" {
		var customPortNum int
		if _, err := fmt.Sscanf(customPort, "%d", &customPortNum); err == nil && customPortNum > 0 && customPortNum <= 65535 {
			port = customPortNum
		}
	}

	// Generate auth key (16 bytes hex encoded = 32 chars)
	authKey := generateJCAuthKey()

	loginURL := fmt.Sprintf("https://joycode.jd.com/login/?ideAppName=JoyCode&fromIde=ide&redirect=0&authPort=%d&authKey=%s", port, authKey)

	c.JSON(http.StatusOK, gin.H{
		"url":     loginURL,
		"authKey": authKey,
		"port":    port,
		"state":   stateID,
	})
}

// generateJCState generates a random state string for OAuth sessions
func generateJCState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// generateJCAuthKey generates a random auth key for JoyCode OAuth
func generateJCAuthKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
