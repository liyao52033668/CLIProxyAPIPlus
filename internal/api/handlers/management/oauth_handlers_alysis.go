package management

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	alysisauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/alysis"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// RequestAlysisToken implements GET /v0/management/alysis-auth-url.
// Alysis uses an RFC 8628-style device flow (no local callback): POST
// /functions/v1/device-code then poll /functions/v1/device-token until the
// user approves the code on https://alysiscode.com/activate.
func (h *Handler) RequestAlysisToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing Alysis authentication...")

	state := fmt.Sprintf("alysis-%d", time.Now().UnixNano())
	authSvc := alysisauth.NewAuth()

	grant, errStart := authSvc.InitiateDeviceFlow(ctx)
	if errStart != nil {
		log.Errorf("Failed to start Alysis device flow: %v", errStart)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start device authorization flow"})
		return
	}
	authURL := strings.TrimSpace(grant.VerificationURLComplete)
	if authURL == "" {
		authURL = strings.TrimSpace(grant.VerificationURL)
	}
	if authURL == "" {
		log.Error("Alysis device flow returned empty verification URL")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start device authorization flow"})
		return
	}

	RegisterOAuthSession(state, "alysis")

	go func() {
		pollCtx, cancelPoll := context.WithCancel(ctx)
		defer cancelPoll()
		go watchOAuthSessionCancel(pollCtx, cancelPoll, state, "alysis")

		fmt.Println("Waiting for Alysis authentication...")
		status, errWait := authSvc.PollForToken(pollCtx, grant.DeviceCode)
		if errWait != nil {
			if !IsOAuthSessionPending(state, "alysis") {
				return
			}
			log.Errorf("Alysis authentication failed: %v", errWait)
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Authentication failed", errWait))
			return
		}
		if !IsOAuthSessionPending(state, "alysis") {
			return
		}
		if status == nil || strings.TrimSpace(status.Key) == "" {
			log.Error("Alysis token poll returned empty gateway key")
			SetOAuthSessionError(state, "Failed to exchange token")
			return
		}

		ts := &alysisauth.TokenStorage{
			Key:  status.Key,
			Type: "alysis",
		}
		fileName := alysisauth.CredentialFileName("", status.Key)
		label := "Alysis Code"

		metadata := map[string]any{
			"type":       "alysis",
			"gatewayKey": ts.Key,
			"email":      "",
			"auth_kind":  "oauth",
		}

		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "alysis",
			FileName: fileName,
			Label:    label,
			Storage:  ts,
			Metadata: metadata,
			Attributes: map[string]string{
				"auth_kind": "oauth",
			},
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "alysis"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save Alysis token to file: %v", errSave)
			SetOAuthSessionError(state, "Failed to save token to file")
			return
		}

		completeOAuthSuccess(state, "alysis")
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		fmt.Println("You can now use Alysis Code Pro services through this CLI")
	}()

	response := gin.H{"status": "ok", "url": authURL, "state": state, "flow": "device"}
	if userCode := strings.TrimSpace(grant.UserCode); userCode != "" {
		response["user_code"] = userCode
	}
	if grant.ExpiresIn > 0 {
		response["expires_in"] = grant.ExpiresIn
	} else {
		response["expires_in"] = 900
	}
	c.JSON(http.StatusOK, response)
}
