package management

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	lobsterauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/lobsterai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// lobsterAIQuotaTimeout bounds the upstream usage lookup, which only runs while
// the management request is open.
const lobsterAIQuotaTimeout = 30 * time.Second

// lobsterAIQuotaBaseURLValidator guards a credential's upstream override against
// SSRF. It is a package-level seam so tests can target an httptest loopback server.
var lobsterAIQuotaBaseURLValidator = lobsterauth.ValidateServerBaseURL

// GetLobsterAIQuota reports the remaining credits for one LobsterAI credential.
//
// The upstream splits the answer across two endpoints (profile-summary for the
// account total and per-bucket breakdown, quota for the plan and counters), so
// they are merged here and returned in one normalized payload. The access token
// is refreshed server-side when it expired, matching the other quota routes.
func (h *Handler) GetLobsterAIQuota(c *gin.Context) {
	authIndex := strings.TrimSpace(c.Query("auth_index"))
	if authIndex == "" {
		authIndex = strings.TrimSpace(c.Query("authIndex"))
	}
	if authIndex == "" {
		authIndex = strings.TrimSpace(c.Query("AuthIndex"))
	}

	auth := h.findLobsterAIAuth(authIndex)
	if auth == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no lobsterai credential found"})
		return
	}

	creds := lobsterauth.CredentialsFromAuth(auth)
	accessToken := creds.AccessToken
	if accessToken == "" {
		// Fall back to the shared resolver so a metadata layout this provider
		// does not read directly still works.
		resolved, errToken := h.resolveTokenForAuth(c.Request.Context(), auth)
		if errToken != nil || resolved == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "lobsterai access token not available"})
			return
		}
		accessToken = resolved
	}

	service := lobsterauth.NewService(util.SetProxy(&h.cfg.SDKConfig, &http.Client{Timeout: lobsterAIQuotaTimeout}))
	if strings.TrimSpace(creds.BaseURL) != "" {
		validated, errBase := lobsterAIQuotaBaseURLValidator(creds.BaseURL)
		if errBase != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "lobsterai credential base url is not allowed"})
			return
		}
		service.SetValidatedServerBaseURL(validated)
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), lobsterAIQuotaTimeout)
	defer cancel()

	usage, errUsage := service.FetchUsage(ctx, accessToken)
	if errUsage != nil {
		log.WithError(errUsage).Debug("lobsterai quota request failed")
		status := http.StatusBadGateway
		var upstreamErr *lobsterauth.Error
		if errors.As(errUsage, &upstreamErr) && upstreamErr.SessionExpired() {
			// Surface an expired credential as such so the panel can prompt a re-login
			// instead of reporting a generic upstream failure.
			status = http.StatusUnauthorized
		}
		c.JSON(status, gin.H{"error": "lobsterai quota request failed: " + errUsage.Error()})
		return
	}

	c.JSON(http.StatusOK, buildLobsterAIQuotaPayload(usage, auth))
}

// buildLobsterAIQuotaPayload renders the normalized quota response.
func buildLobsterAIQuotaPayload(usage *lobsterauth.Usage, auth *coreauth.Auth) gin.H {
	payload := gin.H{}
	if usage != nil {
		// credits_remaining is the authoritative spendable balance. The cycle
		// counters travel under distinct keys because they use a narrower
		// accounting base (no campaign grants); presenting them as a limit for
		// credits_remaining would show two conflicting balances.
		payload["credits_remaining"] = usage.CreditsRemaining
		payload["cycle_credits_limit"] = usage.CycleCreditsLimit
		payload["cycle_credits_used"] = usage.CycleCreditsUsed
		payload["plan_name"] = usage.PlanName
		payload["subscription_status"] = usage.Subscription
		items := make([]gin.H, 0, len(usage.Items))
		for _, item := range usage.Items {
			entry := gin.H{
				"type":              item.Type,
				"label":             item.Label,
				"label_en":          item.LabelEn,
				"credits_remaining": item.CreditsRemaining,
			}
			if item.ExpiresAt != "" {
				entry["expires_at"] = item.ExpiresAt
			}
			items = append(items, entry)
		}
		payload["items"] = items
	}
	if auth != nil {
		auth.EnsureIndex()
		payload["auth_index"] = auth.Index
		payload["auth_name"] = auth.FileName
	}
	return payload
}

// findLobsterAIAuth locates a LobsterAI credential by auth index. An empty index
// selects the first enabled credential; an index that matches nothing returns nil
// so the caller reports a missing credential instead of silently using another one.
func (h *Handler) findLobsterAIAuth(authIndex string) *coreauth.Auth {
	if h == nil || h.authManager == nil {
		return nil
	}
	authIndex = strings.TrimSpace(authIndex)

	var fallback *coreauth.Auth
	for _, auth := range h.authManager.List() {
		if auth == nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), lobsterAIProvider) {
			continue
		}
		if authIndex != "" {
			auth.EnsureIndex()
			if auth.Index == authIndex {
				return auth
			}
			continue
		}
		if !auth.Disabled && fallback == nil {
			fallback = auth
		}
	}
	if authIndex == "" {
		return fallback
	}
	return nil
}
