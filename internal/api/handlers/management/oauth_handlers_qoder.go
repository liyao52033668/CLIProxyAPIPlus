package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	// "github.com/router-for-me/CLIProxyAPI/v7/internal/browser"

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
	machineID := qoderauth.GenerateMachineID("cliproxy", "00:00:00:00:00:00", "server", "x86_64")
	metadata := map[string]any{
		"type":                  "qoder",
		"auth_method":           "pat",
		"login_mode":            "pat",
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
	stopQoderCallbackServer(qoderauth.CallbackPort)

	nonce, challenge, verifier, errPKCE := qoderauth.GeneratePKCE()
	if errPKCE != nil {
		log.Errorf("Failed to generate PKCE parameters: %v", errPKCE)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate PKCE parameters"})
		return
	}

	machineID := qoderauth.GenerateMachineID("cliproxy", "00:00:00:00:00:00", "server", "x86_64")

	state, errState := misc.GenerateRandomState()
	if errState != nil {
		log.Errorf("Failed to generate state parameter: %v", errState)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	RegisterOAuthSession(state, "qoder")

	// Start HTTP callback server (for Windows VBS handler)
	callbackPort := qoderauth.CallbackPort
	_, cbChan, errServer := startQoderCallbackServerWebUI(callbackPort, state)
	if errServer != nil {
		log.Errorf("Failed to start Qoder callback server: %v", errServer)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
		return
	}

	// Use qoder:// redirect URI (required by Qoder server)
	authURL := qoderauth.BuildAuthURLWithRedirectAndState(nonce, challenge, machineID, qoderauth.RedirectURI, state)

	log.Infof("Qoder auth URL built: %s for state: %s", authURL, state)

	SetOAuthSessionError(state, "auth_url|"+authURL)
	log.Infof("Qoder auth URL stored for state: %s", state)

	cleanupURIHandler := qoderauth.RegisterURIHandler(callbackPort)

	go func() {
		defer func() {
			stopQoderCallbackServer(callbackPort)
			cleanupURIHandler()
		}()

		tokenString, authField, errWait := waitForQoderCallback(h.cfg.AuthDir, state, cbChan, defaultOAuthCallbackWait)
		if errWait != nil {
			if errors.Is(errWait, errOAuthSessionNotPending) {
				return
			}
			log.Errorf("Qoder authentication timed out: %v", errWait)
			SetOAuthSessionError(state, "Authentication timed out")
			return
		}

		if tokenString == "" {
			log.Error("Authentication failed: token not found")
			SetOAuthSessionError(state, "Authentication failed: token not found")
			return
		}

		// Decode auth field to get user info
		uid := ""
		name := ""
		email := ""
		if authField != "" {
			// URL decode first since authField may come from URL query string
			authFieldDecoded, err := url.QueryUnescape(authField)
			if err != nil {
				log.Warnf("qoder: failed to URL decode auth field: %v", err)
				authFieldDecoded = authField
			}
			previewLen := min(50, len(authField))
			log.Infof("qoder: authField before URL decode (len=%d): %s", len(authField), authField[:previewLen])
			previewLen = min(50, len(authFieldDecoded))
			log.Infof("qoder: authField after URL decode (len=%d): %s", len(authFieldDecoded), authFieldDecoded[:previewLen])
			authInfo, errDecode := qoderauth.DecodeAuthFieldToJSON(authFieldDecoded)
			if errDecode != nil {
				previewLen := min(100, len(authField))
				log.Warnf("qoder: failed to decode auth field: %v, raw authField (first %d chars): %s", errDecode, previewLen, authField[:previewLen])
			} else {
				log.Infof("qoder: decoded auth field: %+v", authInfo)
				if v, ok := authInfo["uid"].(string); ok {
					uid = v
				}
				if v, ok := authInfo["name"].(string); ok {
					name = v
				}
				if v, ok := authInfo["email"].(string); ok {
					email = v
				}
			}
		}

		// Fallback: fetch user info via device token
		if uid == "" {
			authSvc := qoderauth.NewQoderAuth(nil)
			user, errUser := authSvc.FetchUserStatus(tokenString)
			if errUser != nil {
				log.Warnf("qoder: user status probe failed: %v", errUser)
			} else {
				log.Infof("qoder: user status via API - id=%s, name=%s, email=%s", user.ID, user.Name, user.Email)
				uid = user.ID
				name = user.Name
				email = user.Email
			}
		}

		// Fallback: derive a stable UID from the token hash so we can still save credentials
		if uid == "" {
			tokenHash := sha256.Sum256([]byte(tokenString))
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
			"type":         "qoder",
			"access_token": tokenString,
			"auth":         authField,
			"nonce":        nonce,
			"verifier":     verifier,
			"machine_id":   machineID,
			"uid":          uid,
			"timestamp":    now.UnixMilli(),
		}
		if name != "" {
			metadata["name"] = name
		}
		if email != "" {
			metadata["email"] = email
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
		"url":    authURL,
		"state":  state,
	})
}
