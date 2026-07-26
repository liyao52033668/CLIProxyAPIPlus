package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/quota"
)

const quotaRefreshMaxAuthIndexes = 20

type quotaCheckRequest struct {
	AuthIndex string `json:"auth_index"`
}

type quotaRefreshRequest struct {
	AuthIndexes []string `json:"auth_indexes"`
	Limit       int      `json:"limit"`
}

func registerQuotaRoutes(router gin.IRoutes, provider QuotaProvider) {
	router.POST("/quota/check", func(c *gin.Context) {
		if provider == nil {
			writeInternalError(c, "quota provider is not configured", nil)
			return
		}

		// Parse and validate auth_index first so empty values never enter backend identity resolution.
		var request quotaCheckRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
			return
		}
		request.AuthIndex = strings.TrimSpace(request.AuthIndex)
		if request.AuthIndex == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
			return
		}

		// Map service-layer errors to frontend-facing HTTP statuses and messages.
		response, err := provider.Check(c.Request.Context(), quota.CheckRequest{AuthIndex: request.AuthIndex})
		if err != nil {
			switch {
			case errors.Is(err, quota.ErrValidation):
				c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
			case errors.Is(err, quota.ErrNotFound):
				c.JSON(http.StatusNotFound, gin.H{"error": "quota identity not found"})
			case errors.Is(err, quota.ErrUnsupportedType):
				c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "quota identity type is unsupported"})
			case errors.Is(err, quota.ErrProviderInput):
				c.JSON(http.StatusUnprocessableEntity, gin.H{"error": quotaProviderInputErrorMessage(err)})
			default:
				writeInternalError(c, "quota check failed", err)
			}
			return
		}

		c.JSON(http.StatusOK, response)
	})

	router.POST("/quota/cache", func(c *gin.Context) {
		if provider == nil {
			writeInternalError(c, "quota provider is not configured", nil)
			return
		}

		// Cache reads only validate the query list and do not apply the refresh-queue 20-item cap.
		var request quotaRefreshRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth_indexes are required"})
			return
		}
		if len(request.AuthIndexes) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth_indexes are required"})
			return
		}
		if request.Limit <= 0 {
			request.Limit = len(request.AuthIndexes)
		}

		response, err := provider.GetCachedQuota(c.Request.Context(), quota.CacheRequest{
			AuthIndexes: request.AuthIndexes,
			Limit:       request.Limit,
		})
		if err != nil {
			switch {
			case errors.Is(err, quota.ErrValidation):
				c.JSON(http.StatusBadRequest, gin.H{"error": "auth_indexes are required"})
			default:
				writeInternalError(c, "quota cache lookup failed", err)
			}
			return
		}

		c.JSON(http.StatusOK, response)
	})

	router.POST("/quota/refresh", func(c *gin.Context) {
		if provider == nil {
			writeInternalError(c, "quota provider is not configured", nil)
			return
		}

		// Manual refresh actually hits providers, so cap the current page to 20 items at the entrypoint.
		var request quotaRefreshRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth_indexes are required"})
			return
		}
		if len(request.AuthIndexes) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth_indexes are required"})
			return
		}
		if len(request.AuthIndexes) > quotaRefreshMaxAuthIndexes {
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth_indexes must not exceed 20"})
			return
		}
		if request.Limit <= 0 {
			request.Limit = quotaRefreshMaxAuthIndexes
		}
		if request.Limit > quotaRefreshMaxAuthIndexes {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit must not exceed 20"})
			return
		}

		response, err := provider.Refresh(c.Request.Context(), quota.RefreshRequest{
			AuthIndexes: request.AuthIndexes,
			Limit:       request.Limit,
			Source:      quota.RefreshSourceManual,
		})
		if err != nil {
			switch {
			case errors.Is(err, quota.ErrValidation):
				c.JSON(http.StatusBadRequest, gin.H{"error": "auth_indexes are required"})
			default:
				writeInternalError(c, "quota refresh failed", err)
			}
			return
		}

		c.JSON(http.StatusOK, response)
	})

	router.GET("/quota/refresh/:task_id", func(c *gin.Context) {
		if provider == nil {
			writeInternalError(c, "quota provider is not configured", nil)
			return
		}
		taskID := strings.TrimSpace(c.Param("task_id"))
		if taskID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "task_id is required"})
			return
		}

		// Frontend polling looks up task status by task_id and returns cached quota when complete.
		response, err := provider.GetRefreshTask(c.Request.Context(), taskID)
		if err != nil {
			switch {
			case errors.Is(err, quota.ErrTaskNotFound):
				c.JSON(http.StatusNotFound, gin.H{"error": "quota refresh task not found"})
			default:
				writeInternalError(c, "quota refresh task lookup failed", err)
			}
			return
		}

		c.JSON(http.StatusOK, response)
	})
}

func quotaProviderInputErrorMessage(err error) string {
	return quota.ProviderInputErrorMessage(err, "quota provider input is invalid")
}
