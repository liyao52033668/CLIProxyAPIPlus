package management

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	metaauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/meta"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// RequestMetaToken implements GET /v0/management/meta-auth-url.
// Meta uses the RFC 8628 device authorization grant (no local callback): POST
// https://auth.meta.com/oidc/device/authorization/ then poll
// https://auth.meta.com/oidc/device/token/ until the user approves the code, and
// finally mint an LLM API key from the DCA token.
func (h *Handler) RequestMetaToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing Meta authentication...")

	state := fmt.Sprintf("meta-%d", time.Now().UnixNano())
	authSvc := metaauth.NewMetaAuth(h.cfg)

	deviceFlow, errStart := authSvc.StartDeviceFlow(ctx)
	if errStart != nil {
		log.Errorf("Failed to start Meta device flow: %v", errStart)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start device authorization flow"})
		return
	}
	authURL := strings.TrimSpace(deviceFlow.VerificationURIComplete)
	if authURL == "" {
		authURL = strings.TrimSpace(deviceFlow.VerificationURI)
	}
	if authURL == "" {
		log.Error("Meta device flow returned empty verification URL")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start device authorization flow"})
		return
	}

	RegisterOAuthSession(state, "meta")

	go func() {
		pollCtx, cancelPoll := context.WithCancel(ctx)
		defer cancelPoll()
		go watchOAuthSessionCancel(pollCtx, cancelPoll, state, "meta")

		fmt.Println("Waiting for Meta authentication...")
		bundle, errWait := authSvc.WaitForAuthorization(pollCtx, deviceFlow)
		if errWait != nil {
			if !IsOAuthSessionPending(state, "meta") {
				return
			}
			log.Errorf("Meta authentication failed: %v", errWait)
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Authentication failed", errWait))
			return
		}
		if !IsOAuthSessionPending(state, "meta") {
			return
		}

		tokenStorage := authSvc.CreateTokenStorage(bundle)
		if tokenStorage == nil || strings.TrimSpace(tokenStorage.AccessToken) == "" {
			log.Error("Meta token exchange returned empty access token")
			SetOAuthSessionError(state, "Failed to exchange token")
			return
		}

		fileName := metaauth.CredentialFileName(tokenStorage.Email, tokenStorage.DCAToken)
		label := strings.TrimSpace(tokenStorage.Email)
		if label == "" {
			label = "Meta"
		}

		metadata := map[string]any{
			"type":         "meta",
			"access_token": tokenStorage.AccessToken,
			"token_type":   tokenStorage.TokenType,
			"expires_in":   tokenStorage.ExpiresIn,
			"expired":      tokenStorage.Expired,
			"last_refresh": tokenStorage.LastRefresh,
			"base_url":     tokenStorage.BaseURL,
			"auth_kind":    "oauth",
		}
		if tokenStorage.DCAExpired != "" {
			metadata["dca_expired"] = tokenStorage.DCAExpired
		}
		if tokenStorage.DCAExpiresAt > 0 {
			metadata["dca_expires_at"] = tokenStorage.DCAExpiresAt
		}
		if tokenStorage.APIKey != "" {
			metadata["api_key"] = tokenStorage.APIKey
		}
		if tokenStorage.DCAToken != "" {
			metadata["dca_token"] = tokenStorage.DCAToken
		}
		if tokenStorage.Email != "" {
			metadata["email"] = tokenStorage.Email
		}
		if tokenStorage.Name != "" {
			metadata["name"] = tokenStorage.Name
		}

		attrs := map[string]string{
			"auth_kind": "oauth",
			"base_url":  tokenStorage.BaseURL,
		}
		if tokenStorage.APIKey != "" {
			attrs["api_key"] = tokenStorage.APIKey
		}
		if tokenStorage.DCAToken != "" {
			attrs["dca_token"] = tokenStorage.DCAToken
		}
		if tokenStorage.Email != "" {
			attrs["email"] = tokenStorage.Email
		}

		record := &coreauth.Auth{
			ID:         fileName,
			Provider:   "meta",
			FileName:   fileName,
			Label:      label,
			Storage:    tokenStorage,
			Metadata:   metadata,
			Attributes: attrs,
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "meta"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save Meta token to file: %v", errSave)
			SetOAuthSessionError(state, "Failed to save token to file")
			return
		}

		completeOAuthSuccess(state, "meta")
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		fmt.Println("You can now use Meta services through this CLI")
	}()

	response := gin.H{"status": "ok", "url": authURL, "state": state, "flow": "device"}
	if userCode := strings.TrimSpace(deviceFlow.UserCode); userCode != "" {
		response["user_code"] = userCode
	}
	if deviceFlow.ExpiresIn > 0 {
		response["expires_in"] = deviceFlow.ExpiresIn
	} else {
		response["expires_in"] = int(metaauth.MaxPollDuration / time.Second)
	}
	c.JSON(http.StatusOK, response)
}
