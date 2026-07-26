package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/redact"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service"
)

type usageIdentitiesResponse struct {
	Identities []usageIdentityResponse `json:"identities"`
}

type usageIdentitiesPageResponse struct {
	Identities []usageIdentityResponse `json:"identities"`
	TotalCount int64                   `json:"total_count"`
	Page       int                     `json:"page"`
	PageSize   int                     `json:"page_size"`
	TotalPages int                     `json:"total_pages"`
}

type usageIdentityResponse struct {
	ID                         uint                           `json:"id"`
	Name                       string                         `json:"name"`
	DisplayName                string                         `json:"displayName"`
	AuthType                   entities.UsageIdentityAuthType `json:"auth_type"`
	AuthTypeName               string                         `json:"auth_type_name"`
	Identity                   string                         `json:"identity"`
	Type                       string                         `json:"type"`
	Provider                   string                         `json:"provider"`
	PlanType                   *string                        `json:"plan_type,omitempty"`
	ActiveStart                *time.Time                     `json:"active_start,omitempty"`
	ActiveUntil                *time.Time                     `json:"active_until,omitempty"`
	TotalRequests              int64                          `json:"total_requests"`
	SuccessCount               int64                          `json:"success_count"`
	FailureCount               int64                          `json:"failure_count"`
	InputTokens                int64                          `json:"input_tokens"`
	OutputTokens               int64                          `json:"output_tokens"`
	ReasoningTokens            int64                          `json:"reasoning_tokens"`
	CachedTokens               int64                          `json:"cached_tokens"`
	TotalTokens                int64                          `json:"total_tokens"`
	LastAggregatedUsageEventID uint                           `json:"last_aggregated_usage_event_id"`
	FirstUsedAt                *time.Time                     `json:"first_used_at,omitempty"`
	LastUsedAt                 *time.Time                     `json:"last_used_at,omitempty"`
	StatsUpdatedAt             *time.Time                     `json:"stats_updated_at,omitempty"`
	IsDeleted                  bool                           `json:"is_deleted"`
	CreatedAt                  time.Time                      `json:"created_at"`
	UpdatedAt                  time.Time                      `json:"updated_at"`
	DeletedAt                  *time.Time                     `json:"deleted_at,omitempty"`
}

func registerUsageIdentityRoutes(router gin.IRoutes, usageIdentityProvider service.UsageIdentityProvider) {
	router.GET("/usage/identities/page", func(c *gin.Context) {
		if usageIdentityProvider == nil {
			c.JSON(http.StatusOK, usageIdentitiesPageResponse{Identities: []usageIdentityResponse{}, Page: 1, PageSize: 10})
			return
		}

		// The paged API is for the Credentials section: filter by auth_type server-side, then paginate.
		request, ok := parseUsageIdentitiesPageRequest(c)
		if !ok {
			return
		}
		result, err := usageIdentityProvider.ListActiveUsageIdentitiesPage(c.Request.Context(), request)
		if err != nil {
			writeInternalError(c, "list active usage identities page failed", err)
			return
		}

		// Reuse the shared response mapper so paged and legacy list APIs keep the same fields and redaction rules.
		response := make([]usageIdentityResponse, 0, len(result.Items))
		for _, item := range result.Items {
			response = append(response, mapUsageIdentityResponse(item))
		}
		c.JSON(http.StatusOK, usageIdentitiesPageResponse{
			Identities: response,
			TotalCount: result.Total,
			Page:       request.Page,
			PageSize:   request.PageSize,
			TotalPages: totalPages(result.Total, request.PageSize),
		})
	})

	router.GET("/usage/identities", func(c *gin.Context) {
		if usageIdentityProvider == nil {
			c.JSON(http.StatusOK, usageIdentitiesResponse{Identities: []usageIdentityResponse{}})
			return
		}

		items, err := usageIdentityProvider.ListActiveUsageIdentities(c.Request.Context())
		if err != nil {
			writeInternalError(c, "list active usage identities failed", err)
			return
		}

		response := make([]usageIdentityResponse, 0, len(items))
		for _, item := range items {
			response = append(response, mapUsageIdentityResponse(item))
		}
		c.JSON(http.StatusOK, usageIdentitiesResponse{Identities: response})
	})
}

func parseUsageIdentitiesPageRequest(c *gin.Context) (service.ListUsageIdentitiesRequest, bool) {
	// Apply lenient defaults for page/page_size and strict validation for auth_type so frontend sections do not get mixed data.
	page := positiveQueryInt(c, "page", 1)
	pageSize := positiveQueryInt(c, "page_size", 10)
	request := service.ListUsageIdentitiesRequest{Page: page, PageSize: pageSize}
	if rawAuthType := c.Query("auth_type"); rawAuthType != "" {
		value, err := strconv.Atoi(rawAuthType)
		if err != nil || (value != int(entities.UsageIdentityAuthTypeAuthFile) && value != int(entities.UsageIdentityAuthTypeAIProvider)) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth_type must be 1 or 2"})
			return service.ListUsageIdentitiesRequest{}, false
		}
		authType := entities.UsageIdentityAuthType(value)
		request.AuthType = &authType
	}
	return request, true
}

func positiveQueryInt(c *gin.Context, key string, fallback int) int {
	value, err := strconv.Atoi(c.Query(key))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func totalPages(total int64, pageSize int) int {
	if total <= 0 || pageSize <= 0 {
		return 0
	}
	return int((total + int64(pageSize) - 1) / int64(pageSize))
}

func mapUsageIdentityResponse(item entities.UsageIdentity) usageIdentityResponse {
	// AI provider identities are API keys; redact only in API responses and leave DB values unchanged.
	identity := item.Identity
	if item.AuthType == entities.UsageIdentityAuthTypeAIProvider {
		identity = redact.APIKeyDisplayName(item.Identity)
	}

	return usageIdentityResponse{
		ID:                         item.ID,
		Name:                       item.Name,
		DisplayName:                usageIdentityDisplayName(item),
		AuthType:                   item.AuthType,
		AuthTypeName:               item.AuthTypeName,
		Identity:                   identity,
		Type:                       item.Type,
		Provider:                   item.Provider,
		PlanType:                   item.PlanType,
		ActiveStart:                item.ActiveStart,
		ActiveUntil:                item.ActiveUntil,
		TotalRequests:              item.TotalRequests,
		SuccessCount:               item.SuccessCount,
		FailureCount:               item.FailureCount,
		InputTokens:                item.InputTokens,
		OutputTokens:               item.OutputTokens,
		ReasoningTokens:            item.ReasoningTokens,
		CachedTokens:               item.CachedTokens,
		TotalTokens:                item.TotalTokens,
		LastAggregatedUsageEventID: item.LastAggregatedUsageEventID,
		FirstUsedAt:                item.FirstUsedAt,
		LastUsedAt:                 item.LastUsedAt,
		StatsUpdatedAt:             item.StatsUpdatedAt,
		IsDeleted:                  item.IsDeleted,
		CreatedAt:                  item.CreatedAt,
		UpdatedAt:                  item.UpdatedAt,
		DeletedAt:                  item.DeletedAt,
	}
}
