package management

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// authFileRefreshTimeout bounds a manual credential refresh. Refreshing is
// credential acquisition, so an explicit timeout is allowed here.
const authFileRefreshTimeout = 60 * time.Second

type authFileRefreshRequest struct {
	Name      string `json:"name"`
	ID        string `json:"id"`
	AuthIndex string `json:"auth_index"`
}

type authFileRefreshResponse struct {
	OK          bool   `json:"ok"`
	Status      string `json:"status"`
	Provider    string `json:"provider,omitempty"`
	AuthID      string `json:"auth_id,omitempty"`
	FileName    string `json:"file_name,omitempty"`
	LastRefresh string `json:"last_refresh,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	Error       string `json:"error,omitempty"`
}

// RefreshAuthFile manually refreshes credentials for a single auth file.
//
// Endpoint:
//
//	POST /v0/management/auth-files/refresh
//
// It resolves the auth by name, id, or auth index and synchronously refreshes
// the credential via the registered provider executor. On success the refreshed
// token is persisted back to the auth file.
func (h *Handler) RefreshAuthFile(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return
	}
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth manager unavailable"})
		return
	}

	var body authFileRefreshRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	auth := h.findAuthForRefresh(body)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}

	resp := authFileRefreshResponse{
		Provider: strings.TrimSpace(auth.Provider),
		AuthID:   strings.TrimSpace(auth.ID),
		FileName: strings.TrimSpace(auth.FileName),
	}

	if auth.Disabled || auth.Status == coreauth.StatusDisabled {
		resp.Status = "disabled"
		resp.Error = "auth is disabled"
		c.JSON(http.StatusOK, resp)
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), authFileRefreshTimeout)
	defer cancel()

	updated, err := h.authManager.RefreshAuth(ctx, auth.ID)
	if err != nil {
		resp.Status = "error"
		resp.Error = err.Error()
		c.JSON(http.StatusOK, resp)
		return
	}

	resp.OK = true
	resp.Status = "ok"
	if updated != nil {
		if !updated.LastRefreshedAt.IsZero() {
			resp.LastRefresh = updated.LastRefreshedAt.Format(time.RFC3339)
		}
		if expiry, hasExpiry := updated.ExpirationTime(); hasExpiry && !expiry.IsZero() {
			resp.ExpiresAt = expiry.Format(time.RFC3339)
		}
	}
	c.JSON(http.StatusOK, resp)
}

// findAuthForRefresh resolves an auth by auth index, id, or file name.
func (h *Handler) findAuthForRefresh(body authFileRefreshRequest) *coreauth.Auth {
	if h == nil || h.authManager == nil {
		return nil
	}

	name := strings.TrimSpace(body.Name)
	id := strings.TrimSpace(body.ID)
	authIndex := strings.TrimSpace(body.AuthIndex)

	if authIndex != "" {
		if auth := h.authByIndex(authIndex); auth != nil {
			return auth
		}
	}
	if id != "" {
		if auth, ok := h.authManager.GetByID(id); ok && auth != nil {
			return auth
		}
	}

	key := name
	if key == "" {
		key = id
	}
	if key == "" {
		return nil
	}
	if auth, ok := h.authManager.GetByID(key); ok && auth != nil {
		return auth
	}
	for _, auth := range h.authManager.List() {
		if auth == nil {
			continue
		}
		if auth.FileName == key || auth.ID == key || auth.Index == key {
			return auth
		}
	}
	return nil
}
