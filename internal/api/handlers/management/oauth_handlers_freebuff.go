package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	freebuffauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/freebuff"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	log "github.com/sirupsen/logrus"
)

// StartFreebuffLogin begins the Freebuff CLI device-flow login: it requests a
// browser login URL for a fresh fingerprint and returns it together with the
// handle fields the poll endpoint needs.
func (h *Handler) StartFreebuffLogin(c *gin.Context) {
	var body struct {
		BaseURL string `json:"base_url"`
	}
	_ = c.ShouldBindJSON(&body)

	baseURL, errBase := freebuffauth.ValidateLoginBaseURL(body.BaseURL)
	if errBase != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errBase.Error()})
		return
	}
	fingerprintID, errFingerprint := freebuffauth.NewFingerprintID()
	if errFingerprint != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate login fingerprint"})
		return
	}

	client := helps.NewProxyAwareHTTPClient(c.Request.Context(), h.cfg, nil, 0)
	code, errCode := freebuffauth.RequestLoginCode(c.Request.Context(), client, baseURL, fingerprintID)
	if errCode != nil {
		log.Errorf("freebuff login: failed to request login code: %v", errCode)
		c.JSON(http.StatusBadGateway, gin.H{"error": errCode.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":           "ok",
		"login_url":        code.LoginURL,
		"fingerprint_id":   code.FingerprintID,
		"fingerprint_hash": code.FingerprintHash,
		"expires_at":       code.ExpiresAt,
		"base_url":         baseURL,
	})
}

// PollFreebuffLogin performs one upstream status check for a pending login.
// Clients call it repeatedly (about every two seconds) until it reports
// "authorized" or an error.
func (h *Handler) PollFreebuffLogin(c *gin.Context) {
	var body struct {
		BaseURL         string `json:"base_url"`
		FingerprintID   string `json:"fingerprint_id"`
		FingerprintHash string `json:"fingerprint_hash"`
		ExpiresAt       int64  `json:"expires_at"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if strings.TrimSpace(body.FingerprintID) == "" || strings.TrimSpace(body.FingerprintHash) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "fingerprint_id and fingerprint_hash are required"})
		return
	}
	baseURL, errBase := freebuffauth.ValidateLoginBaseURL(body.BaseURL)
	if errBase != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errBase.Error()})
		return
	}

	client := helps.NewProxyAwareHTTPClient(c.Request.Context(), h.cfg, nil, 0)
	code := &freebuffauth.LoginCode{
		FingerprintID:   body.FingerprintID,
		FingerprintHash: body.FingerprintHash,
		ExpiresAt:       body.ExpiresAt,
	}
	user, pending, errPoll := freebuffauth.PollLoginStatus(c.Request.Context(), client, baseURL, code)
	if errPoll != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": errPoll.Error()})
		return
	}
	if pending || user == nil {
		c.JSON(http.StatusOK, gin.H{"status": "pending"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":  "authorized",
		"api_key": user.AuthToken,
		"user": gin.H{
			"email": user.Email,
			"name":  user.Name,
		},
	})
}

// CompleteFreebuffLogin verifies a finished login token and appends it to the
// freebuff-api-key configuration so the watcher picks it up immediately.
func (h *Handler) CompleteFreebuffLogin(c *gin.Context) {
	var body struct {
		APIKey  string `json:"api_key"`
		BaseURL string `json:"base_url"`
		Comment string `json:"comment"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	apiKey := strings.TrimSpace(body.APIKey)
	if apiKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "api_key is required"})
		return
	}
	baseURL, errBase := freebuffauth.ValidateLoginBaseURL(body.BaseURL)
	if errBase != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errBase.Error()})
		return
	}

	client := helps.NewProxyAwareHTTPClient(c.Request.Context(), h.cfg, nil, 0)
	if errVerify := freebuffauth.VerifyToken(c.Request.Context(), client, baseURL, apiKey); errVerify != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": errVerify.Error()})
		return
	}

	comment := strings.TrimSpace(body.Comment)
	if comment == "" {
		comment = "device-flow login " + time.Now().UTC().Format("2006-01-02")
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.cfg.FreebuffKey {
		if h.cfg.FreebuffKey[i].ContainsAPIKey(apiKey) {
			c.JSON(http.StatusConflict, gin.H{"error": "this token is already configured"})
			return
		}
	}
	h.cfg.FreebuffKey = append(h.cfg.FreebuffKey, config.FreebuffKey{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Comment: comment,
	})
	h.cfg.SanitizeFreebuffKeys()
	if !h.persistLocked(c) {
		return
	}
	log.Infof("freebuff login: appended new credential entry (total %d)", len(h.cfg.FreebuffKey))
}
