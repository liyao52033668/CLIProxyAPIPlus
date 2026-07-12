package management

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/bt"

	// "github.com/router-for-me/CLIProxyAPI/v7/internal/browser"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func (h *Handler) RequestBTToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	if c.Request.Method == http.MethodPost {
		var req struct {
			Phone    string `json:"phone" binding:"required"`
			Password string `json:"password" binding:"required"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "phone and password are required"})
			return
		}

		phone := strings.TrimSpace(req.Phone)
		passwordBase64 := strings.TrimSpace(req.Password)

		if phone == "" || passwordBase64 == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "phone and password cannot be empty"})
			return
		}

		fmt.Printf("Initializing BT authentication for phone: %s\n", phone)

		tokenStorage, err := bt.Login(phone, passwordBase64)
		if err != nil {
			log.Errorf("BT authentication failed: %v", err)
			c.JSON(http.StatusUnauthorized, gin.H{"error": fmt.Sprintf("Authentication failed: %v", err)})
			return
		}

		fileName := fmt.Sprintf("bt-%s.json", tokenStorage.UID)
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "bt",
			FileName: fileName,
			Label:    tokenStorage.Phone,
			Storage:  tokenStorage,
			Metadata: map[string]any{
				"type":       "bt",
				"phone":      tokenStorage.Phone,
				"uid":        tokenStorage.UID,
				"access_key": tokenStorage.AccessKey,
				"serverid":   tokenStorage.ServerID,
				"timestamp":  time.Now().UnixMilli(),
			},
		}

		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save BT token: %v", errSave)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save token"})
			return
		}

		fmt.Printf("BT authentication successful! Token saved to %s\n", savedPath)
		c.JSON(http.StatusOK, gin.H{
			"status":  "ok",
			"message": "Authentication successful",
			"file":    fileName,
			"path":    savedPath,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"message": "BT authentication requires POST request with phone and password",
		"method":  "POST",
		"body": gin.H{
			"phone":    "your_phone_number",
			"password": "base64_encoded_password",
		},
	})
}
