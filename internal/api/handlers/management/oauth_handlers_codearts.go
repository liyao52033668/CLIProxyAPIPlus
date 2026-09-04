package management

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	codeartsauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codearts"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func (h *Handler) RequestCodeArtsToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)
	_ = ctx

	state, errState := misc.GenerateRandomState()
	if errState != nil {
		log.Errorf("Failed to generate state parameter: %v", errState)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state"})
		return
	}

	RegisterOAuthSession(state, "codearts")

	// Use CodeArts OAuth handler to generate the real authorization URL directly
	var authURL string
	if h.codeArtsOAuthHandler != nil {
		var err error
		authURL, err = h.codeArtsOAuthHandler.CreateSessionAndGetAuthURL(state)
		if err != nil {
			log.WithError(err).Error("failed to create CodeArts OAuth session")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create auth session"})
			return
		}
	} else {
		// Fallback to local start endpoint if handler is not available
		var err error
		authURL, err = h.managementCallbackURL("/v0/oauth/codearts/start")
		if err != nil {
			log.WithError(err).Error("failed to compute codearts login url")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
			return
		}
	}

	// Stash the authorization URL so the web UI can re-read it while polling.
	SetOAuthSessionError(state, "auth_url|"+authURL)

	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"url":    authURL,
		"state":  state,
	})
}

// RequestCodeArtsAKSKToken authorizes CodeArts with permanent HuaweiCloud IAM
// access keys. Unlike the OAuth flow, the stored credentials carry no expiry and
// are never scheduled for refresh.
func (h *Handler) RequestCodeArtsAKSKToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	var payload struct {
		AK              string `json:"ak"`
		SK              string `json:"sk"`
		AccessKeyID     string `json:"access_key_id"`
		SecretAccessKey string `json:"secret_access_key"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid body"})
		return
	}

	ak := strings.TrimSpace(payload.AK)
	if ak == "" {
		ak = strings.TrimSpace(payload.AccessKeyID)
	}
	sk := strings.TrimSpace(payload.SK)
	if sk == "" {
		sk = strings.TrimSpace(payload.SecretAccessKey)
	}
	if ak == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "ak is required"})
		return
	}
	if sk == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "sk is required"})
		return
	}

	info, err := codeartsauth.NewCodeArtsAuth(nil).VerifyAKSK(ctx, ak, sk)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": err.Error()})
		return
	}

	label := codeartsauth.AKSKLabel(info)
	fileName := codeartsauth.AKSKFileName(label)
	record := &coreauth.Auth{
		ID:       fileName,
		Provider: "codearts",
		FileName: fileName,
		Label:    label + " (AK/SK)",
		Metadata: codeartsauth.BuildAKSKMetadata(ak, sk, info),
	}

	savedPath, errSave := h.saveTokenRecord(ctx, record)
	if errSave != nil {
		log.Errorf("Failed to save CodeArts AK/SK auth record: %v", errSave)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "failed to save authentication tokens"})
		return
	}

	fmt.Printf("CodeArts AK/SK authentication successful. Credentials saved to %s\n", savedPath)
	c.JSON(http.StatusOK, gin.H{
		"status":      "ok",
		"saved_path":  savedPath,
		"user_name":   strings.TrimSpace(info.UserName),
		"user_id":     strings.TrimSpace(info.UserID),
		"token_label": label,
	})
}
