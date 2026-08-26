package management

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	commandcodeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/commandcode"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// RequestCommandCodeToken accepts an API key directly via POST request (or query parameter),
// validates it against the Command Code API, and persists the credential file.
func (h *Handler) RequestCommandCodeToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	authSvc := commandcodeauth.NewCommandCodeAuth()

	var apiKey, sessionToken string
	if c.Request.Method == http.MethodPost {
		var req struct {
			APIKey       string `json:"api_key"`
			Token        string `json:"token"`
			SessionToken string `json:"session_token"`
		}
		if err := c.ShouldBindJSON(&req); err == nil {
			apiKey = strings.TrimSpace(req.APIKey)
			if apiKey == "" {
				apiKey = strings.TrimSpace(req.Token)
			}
			sessionToken = strings.TrimSpace(req.SessionToken)
		}
	}
	if apiKey == "" {
		apiKey = strings.TrimSpace(c.Query("api_key"))
		if apiKey == "" {
			apiKey = strings.TrimSpace(c.Query("token"))
		}
	}
	if sessionToken == "" {
		sessionToken = strings.TrimSpace(c.Query("session_token"))
	}

	if apiKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "api_key is required"})
		return
	}

	whoamiCtx, cancelWhoami := context.WithTimeout(ctx, 30*time.Second)
	defer cancelWhoami()
	whoami, errWhoami := authSvc.Whoami(whoamiCtx, apiKey)
	if errWhoami != nil {
		log.Errorf("Command Code credential validation failed: %v", errWhoami)
		c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "error": fmt.Sprintf("Credential validation failed: %v", errWhoami)})
		return
	}

	userName := ""
	userID := ""
	if whoami.User != nil {
		userName = whoami.User.UserName
		userID = whoami.User.ID
	}

	ts := &commandcodeauth.CommandCodeTokenStorage{
		APIKey:          apiKey,
		SessionToken:    sessionToken,
		UserID:          userID,
		UserName:        userName,
		KeyName:         "cli-proxy-api",
		AuthenticatedAt: time.Now().UTC().Format(time.RFC3339),
		Type:            "commandcode",
	}

	fileName := commandcodeauth.CredentialFileName(userName)
	record := &coreauth.Auth{
		ID:       fileName,
		Provider: "commandcode",
		FileName: fileName,
		Label:    userName,
		Storage:  ts,
		Metadata: map[string]any{
			"type":      "commandcode",
			"api_key":   apiKey,
			"user_id":   userID,
			"user_name": userName,
		},
	}
	if userName != "" {
		record.Metadata["email"] = userName
	}
	if sessionToken != "" {
		record.Metadata["session_token"] = sessionToken
		record.Metadata["sessionToken"] = sessionToken
	}

	savedPath, errSave := h.saveTokenRecord(ctx, record)
	if errSave != nil {
		log.Errorf("Failed to save authentication tokens: %v", errSave)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "Failed to save authentication tokens"})
		return
	}

	fmt.Printf("Command Code authentication successful! Token saved to %s\n", savedPath)
	c.JSON(http.StatusOK, gin.H{
		"status":   "ok",
		"message":  "Authentication successful",
		"username": userName,
		"user_id":  userID,
		"file":     fileName,
	})
}
