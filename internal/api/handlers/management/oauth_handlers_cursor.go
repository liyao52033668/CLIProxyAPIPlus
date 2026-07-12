package management

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	cursorauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor"

	// "github.com/router-for-me/CLIProxyAPI/v7/internal/browser"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func (h *Handler) RequestCursorToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	label := strings.TrimSpace(c.Query("label"))
	log.Infof("Initializing Cursor authentication (label=%q)...", label)

	authParams, err := cursorauth.GenerateAuthParams()
	if err != nil {
		log.Errorf("Failed to generate Cursor auth params: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate auth params"})
		return
	}

	state := fmt.Sprintf("cur-%d", time.Now().UnixNano())
	RegisterOAuthSession(state, "cursor")

	go func() {
		log.Info("Waiting for Cursor authentication...")
		log.Infof("Open this URL in your browser: %s", authParams.LoginURL)

		tokens, errPoll := cursorauth.PollForAuth(ctx, authParams.UUID, authParams.Verifier)
		if errPoll != nil {
			SetOAuthSessionError(state, "Authentication failed: "+errPoll.Error())
			log.Errorf("Cursor authentication failed: %v", errPoll)
			return
		}

		// Build metadata
		metadata := map[string]any{
			"type":          "cursor",
			"access_token":  tokens.AccessToken,
			"refresh_token": tokens.RefreshToken,
			"timestamp":     time.Now().UnixMilli(),
		}

		// Extract expiry and account identity from JWT
		expiry := cursorauth.GetTokenExpiry(tokens.AccessToken)
		if !expiry.IsZero() {
			metadata["expires_at"] = expiry.Format(time.RFC3339)
		}

		// Auto-identify account from JWT sub claim for multi-account support
		sub := cursorauth.ParseJWTSub(tokens.AccessToken)
		subHash := cursorauth.SubToShortHash(sub)
		if sub != "" {
			metadata["sub"] = sub
		}

		fileName := cursorauth.CredentialFileName(label, subHash)
		displayLabel := cursorauth.DisplayLabel(label, subHash)
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "cursor",
			FileName: fileName,
			Label:    displayLabel,
			Metadata: metadata,
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save Cursor tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save tokens")
			return
		}

		log.Infof("Cursor authentication successful! Token saved to %s", savedPath)
		completeOAuthSuccess(state, "cursor")
	}()

	c.JSON(200, gin.H{
		"status": "ok",
		"url":    authParams.LoginURL,
		"state":  state,
	})
}
