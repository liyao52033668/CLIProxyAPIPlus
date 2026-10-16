package management

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// KiroImportToken handles manual Kiro token imports from the management UI.
// It accepts either a bare refresh token ({"refreshToken": "..."}), which is
// validated via a token refresh round-trip, or the raw Kiro IDE token JSON
// (accessToken/refreshToken/profileArn/...), which is imported as-is.
func (h *Handler) KiroImportToken(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid request body"})
		return
	}

	tokenData, rawKiroIDEJSON, errParse := kiroauth.ParseImportTokenPayload(data)
	if errParse != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": errParse.Error()})
		return
	}

	if !rawKiroIDEJSON {
		refreshToken := strings.TrimSpace(tokenData.RefreshToken)
		if refreshToken == "" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "refresh token is required"})
			return
		}
		if !strings.HasPrefix(refreshToken, "aorAAAAAG") {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid token format. Token should start with aorAAAAAG..."})
			return
		}

		socialClient := kiroauth.NewSocialAuthClient(h.cfg)
		validated, errRefresh := socialClient.RefreshSocialToken(c.Request.Context(), refreshToken)
		if errRefresh != nil {
			log.Errorf("kiro import: token refresh validation failed: %v", errRefresh)
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": fmt.Sprintf("token validation failed: %v", errRefresh)})
			return
		}
		if validated.RefreshToken == "" {
			validated.RefreshToken = refreshToken
		}
		validated.AuthMethod = "social"
		validated.Provider = "imported"
		tokenData = validated
	}

	// Save through the standard auth-file store so the token shows up in the
	// management UI like any other Kiro credential.
	record := kiroTokenDataToAuthRecord(tokenData)
	savedPath, errSave := h.saveTokenRecord(c.Request.Context(), record)
	if errSave != nil {
		log.Errorf("kiro import: failed to save token record: %v", errSave)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "failed to save token"})
		return
	}

	log.Infof("kiro import: token saved to %s", savedPath)
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"message": "token imported successfully",
		"file":    record.FileName,
	})
}

// kiroTokenDataToAuthRecord converts Kiro token data into the auth record shape
// used by the management token store, mirroring the file naming of the OAuth web
// handler (kiro-<authMethod>-<email|id>.json).
func kiroTokenDataToAuthRecord(tokenData *kiroauth.KiroTokenData) *coreauth.Auth {
	email := strings.TrimSpace(tokenData.Email)
	if email == "" {
		email = kiroauth.ExtractEmailFromJWT(tokenData.AccessToken)
	}
	idPart := kiroauth.SanitizeEmailForFilename(email)
	if idPart == "" {
		idPart = fmt.Sprintf("%d", time.Now().UnixNano()%100000)
	}
	method := strings.ToLower(strings.TrimSpace(tokenData.AuthMethod))
	if method == "" {
		method = "imported"
	}
	fileName := fmt.Sprintf("kiro-%s-%s.json", method, idPart)

	metadata := map[string]any{
		"type":          "kiro",
		"access_token":  tokenData.AccessToken,
		"refresh_token": tokenData.RefreshToken,
		"profile_arn":   tokenData.ProfileArn,
		"expires_at":    tokenData.ExpiresAt,
		"auth_method":   tokenData.AuthMethod,
		"provider":      tokenData.Provider,
		"last_refresh":  time.Now().Format(time.RFC3339),
	}
	if email != "" {
		metadata["email"] = email
	}
	if tokenData.ClientID != "" {
		metadata["client_id"] = tokenData.ClientID
	}
	if tokenData.ClientSecret != "" {
		metadata["client_secret"] = tokenData.ClientSecret
	}
	if tokenData.ClientIDHash != "" {
		metadata["client_id_hash"] = tokenData.ClientIDHash
	}
	if tokenData.StartURL != "" {
		metadata["start_url"] = tokenData.StartURL
	}
	if tokenData.Region != "" {
		metadata["region"] = tokenData.Region
	}

	return &coreauth.Auth{
		ID:       fileName,
		Provider: "kiro",
		FileName: fileName,
		Metadata: metadata,
	}
}
