package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	qoderauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func (h *Handler) RequestQoderPATToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	var payload struct {
		BaseURL             string `json:"base_url"`
		PersonalAccessToken string `json:"personal_access_token"`
		Token               string `json:"token"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid body"})
		return
	}

	baseURL := strings.TrimRight(strings.TrimSpace(payload.BaseURL), "/")
	if baseURL == "" {
		baseURL = qoderauth.OpenAPIBase
	}
	pat := strings.TrimSpace(payload.PersonalAccessToken)
	if pat == "" {
		pat = strings.TrimSpace(payload.Token)
	}
	if pat == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "personal_access_token is required"})
		return
	}

	authSvc := qoderauth.NewQoderAuth(nil)
	user, err := authSvc.FetchUserStatusWithBaseURL(baseURL, pat)
	warning := ""
	uid := ""
	name := ""
	email := ""
	if err != nil {
		warning = err.Error()
	} else if user != nil {
		uid = strings.TrimSpace(user.ID)
		name = strings.TrimSpace(user.Name)
		email = strings.TrimSpace(user.Email)
	}
	if uid == "" {
		tokenHash := sha256.Sum256([]byte(pat))
		uid = hex.EncodeToString(tokenHash[:16])
	}
	machineID := qoderauth.GeneratePATMachineID(pat)
	metadata := map[string]any{
		"type":                  "qoder",
		"auth_method":           "pat",
		"login_mode":            "pat",
		"login_method":          "token",
		"access_token":          pat,
		"personal_access_token": pat,
		"machine_id":            machineID,
		"uid":                   uid,
		"timestamp":             time.Now().UnixMilli(),
	}
	if name != "" {
		metadata["name"] = name
	}
	if email != "" {
		metadata["email"] = email
	}
	if baseURL != qoderauth.OpenAPIBase {
		metadata["base_url"] = baseURL
	}

	fileName := qoderauth.CredentialFileName(uid, email)
	label := name
	if strings.TrimSpace(label) == "" {
		label = uid
	}
	if strings.TrimSpace(label) == "" {
		label = "qoder"
	}
	record := &coreauth.Auth{
		ID:       fileName,
		Provider: "qoder",
		FileName: fileName,
		Label:    label + " (PAT)",
		Metadata: metadata,
	}

	savedPath, err := h.saveTokenRecord(ctx, record)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "failed to save authentication tokens"})
		return
	}

	response := gin.H{
		"status":     "ok",
		"saved_path": savedPath,
		"uid":        uid,
		"name":       name,
		"email":      email,
	}
	if warning != "" {
		response["warning"] = warning
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) RequestQoderToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	log.Info("Initializing Qoder authentication...")

	CompleteOAuthSessionsByProvider("qoder")
	// Legacy local callback server is no longer required for device flow.
	stopQoderCallbackServer(qoderauth.CallbackPort)

	flow, errFlow := qoderauth.StartDeviceFlow(qoderauth.GenerateBrowserMachineID())
	if errFlow != nil {
		log.Errorf("Failed to start Qoder device flow: %v", errFlow)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start device flow"})
		return
	}

	state, errState := misc.GenerateRandomState()
	if errState != nil {
		log.Errorf("Failed to generate state parameter: %v", errState)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	RegisterOAuthSession(state, "qoder")
	SetOAuthSessionError(state, "auth_url|"+flow.AuthURL)
	log.Infof("Qoder device auth URL stored for state: %s", state)

	go func() {
		authSvc := qoderauth.NewQoderAuth(nil)
		pollCtx, cancel := context.WithTimeout(ctx, qoderauth.PollTimeout)
		defer cancel()

		// Abort poll when the OAuth session is cancelled from the UI.
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-pollCtx.Done():
					return
				case <-ticker.C:
					if !IsOAuthSessionPending(state, "qoder") {
						cancel()
						return
					}
				}
			}
		}()

		tokenResp, errPoll := authSvc.PollDeviceToken(pollCtx, flow.Nonce, flow.Verifier)
		if errPoll != nil {
			if errors.Is(errPoll, context.Canceled) || errors.Is(errPoll, errOAuthSessionNotPending) {
				return
			}
			if errors.Is(errPoll, context.DeadlineExceeded) || strings.Contains(errPoll.Error(), "timed out") {
				log.Errorf("Qoder authentication timed out: %v", errPoll)
				SetOAuthSessionError(state, "Authentication timed out")
				return
			}
			log.Errorf("Qoder device poll failed: %v", errPoll)
			SetOAuthSessionError(state, "Authentication failed: "+errPoll.Error())
			return
		}

		deviceToken := tokenResp.AccessToken()
		if deviceToken == "" {
			log.Error("Authentication failed: token not found")
			SetOAuthSessionError(state, "Authentication failed: token not found")
			return
		}

		uid := strings.TrimSpace(tokenResp.UserID)
		name := strings.TrimSpace(tokenResp.UserName)
		email := ""
		if profile := authSvc.ResolveUserProfile(deviceToken); profile != nil {
			if uid == "" {
				uid = strings.TrimSpace(profile.ID)
			}
			if name == "" {
				name = strings.TrimSpace(profile.Name)
			}
			email = strings.TrimSpace(profile.Email)
		}
		if uid == "" {
			tokenHash := sha256.Sum256([]byte(deviceToken))
			uid = hex.EncodeToString(tokenHash[:16])
			log.Warnf("qoder: using derived UID from token hash: %s", uid)
		}
		if uid == "" {
			log.Error("qoder: cannot determine user ID")
			SetOAuthSessionError(state, "Cannot determine user ID")
			return
		}

		now := time.Now()
		metadata := map[string]any{
			"type":                 "qoder",
			"auth_method":          "oauth",
			"login_method":         "browser",
			"login_mode":           "browser",
			"refresh_strategy":     "device-token",
			"access_token":         deviceToken,
			"security_oauth_token": deviceToken,
			"refresh_token":        strings.TrimSpace(tokenResp.RefreshToken),
			"nonce":                flow.Nonce,
			"verifier":             flow.Verifier,
			"machine_id":           flow.MachineID,
			"client_id":            flow.ClientID,
			"uid":                  uid,
			"timestamp":            now.UnixMilli(),
		}
		if name != "" {
			metadata["name"] = name
		}
		if email != "" {
			metadata["email"] = email
		}
		if expireUnix := tokenResp.ExpireUnix(); expireUnix > 0 {
			metadata["expired"] = time.Unix(expireUnix, 0).UTC().Format(time.RFC3339)
			metadata["expires_at"] = expireUnix
		}
		if refreshExpireUnix := tokenResp.RefreshExpireUnix(); refreshExpireUnix > 0 {
			metadata["refresh_token_expired"] = time.Unix(refreshExpireUnix, 0).UTC().Format(time.RFC3339)
			metadata["refresh_token_expires_at"] = refreshExpireUnix
		}

		fileName := qoderauth.CredentialFileName(uid, email)
		label := name
		if strings.TrimSpace(label) == "" {
			label = uid
		}
		if strings.TrimSpace(label) == "" {
			label = "qoder"
		}

		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "qoder",
			FileName: fileName,
			Label:    label,
			Metadata: metadata,
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save token to file: %v", errSave)
			SetOAuthSessionError(state, "Failed to save token to file")
			return
		}

		completeOAuthSuccess(state, "qoder")
		log.Infof("Qoder authentication successful! Token saved to %s", savedPath)
	}()

	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"url":    flow.AuthURL,
		"state":  state,
	})
}
