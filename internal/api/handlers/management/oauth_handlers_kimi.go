package management

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	// "github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kimi"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func (h *Handler) RequestKimiToken(c *gin.Context) {
	domain := kimi.KimiDefaultDomain
	if qDomain := strings.TrimSpace(c.Query("domain")); qDomain != "" {
		domain = qDomain
	} else if qChan := strings.TrimSpace(c.Query("channel")); qChan != "" {
		domain = qChan
	}
	h.requestKimiTokenWithDomain(c, domain)
}

func (h *Handler) RequestKimiAIToken(c *gin.Context) {
	h.requestKimiTokenWithDomain(c, kimi.KimiAIDomain)
}

func (h *Handler) requestKimiTokenWithDomain(c *gin.Context, domain string) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	isAI := kimi.IsKimiAIDomain(domain)
	displayName := "Kimi"
	providerName := "kimi"
	filePrefix := "kimi"
	statePrefix := "kmi"
	baseURL := kimi.KimiAPIBaseURL
	if isAI {
		displayName = "Kimi.ai"
		providerName = "kimi-ai"
		filePrefix = "kimi-ai"
		statePrefix = "kmi-ai"
		baseURL = kimi.KimiAIAPIBaseURL
	}

	fmt.Printf("Initializing %s authentication...\n", displayName)

	state := fmt.Sprintf("%s-%d", statePrefix, time.Now().UnixNano())
	// Initialize Kimi auth service
	kimiAuth := kimi.NewKimiAuthWithDomain(h.cfg, domain)

	// Generate authorization URL
	deviceFlow, errStartDeviceFlow := kimiAuth.StartDeviceFlow(ctx)
	if errStartDeviceFlow != nil {
		log.Errorf("Failed to generate authorization URL: %v", errStartDeviceFlow)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}
	authURL := deviceFlow.VerificationURIComplete
	if authURL == "" {
		authURL = deviceFlow.VerificationURI
	}

	RegisterOAuthSession(state, providerName)

	go func() {
		fmt.Printf("Waiting for %s authentication...\n", displayName)
		authBundle, errWaitForAuthorization := kimiAuth.WaitForAuthorization(ctx, deviceFlow)
		if errWaitForAuthorization != nil {
			SetOAuthSessionError(state, fmt.Sprintf("%s authentication failed", displayName))
			fmt.Printf("%s authentication failed: %v\n", displayName, errWaitForAuthorization)
			return
		}

		// Create token storage
		tokenStorage := kimiAuth.CreateTokenStorage(authBundle)
		if isAI {
			tokenStorage.Type = providerName
		}

		metadata := map[string]any{
			"type":          providerName,
			"access_token":  authBundle.TokenData.AccessToken,
			"refresh_token": authBundle.TokenData.RefreshToken,
			"token_type":    authBundle.TokenData.TokenType,
			"scope":         authBundle.TokenData.Scope,
			"timestamp":     time.Now().UnixMilli(),
			"domain":        domain,
			"base_url":      baseURL,
		}
		if authBundle.TokenData.ExpiresAt > 0 {
			expired := time.Unix(authBundle.TokenData.ExpiresAt, 0).UTC().Format(time.RFC3339)
			metadata["expired"] = expired
		}
		if strings.TrimSpace(authBundle.DeviceID) != "" {
			metadata["device_id"] = strings.TrimSpace(authBundle.DeviceID)
		}

		fileName := fmt.Sprintf("%s-%d.json", filePrefix, time.Now().UnixMilli())
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: providerName,
			FileName: fileName,
			Label:    fmt.Sprintf("%s User", displayName),
			Storage:  tokenStorage,
			Metadata: metadata,
			Attributes: map[string]string{
				"base_url": baseURL,
				"domain":   domain,
			},
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}

		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		fmt.Printf("You can now use %s services through this CLI\n", displayName)
		completeOAuthSuccess(state, providerName)
	}()

	c.JSON(200, gin.H{"status": "ok", "url": authURL, "state": state})
}
