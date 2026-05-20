package management

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	repodto "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository/dto"
	keeperservice "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service"
	dto "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service/dto"
)

type usageExportPayload struct {
	Version    int                        `json:"version"`
	ExportedAt time.Time                  `json:"exported_at"`
	Usage      repodto.StatisticsSnapshot `json:"usage"`
}

type usageImportPayload struct {
	Version int                         `json:"version"`
	Usage   *repodto.StatisticsSnapshot `json:"usage"`
}

// GetUsageStatistics returns the in-memory request statistics snapshot.
func (h *Handler) GetUsageStatistics(c *gin.Context) {
	if c.Query("count") != "" {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	var snapshot usage.StatisticsSnapshot
	if h != nil && h.usageStats != nil {
		snapshot = h.usageStats.Snapshot()
	}
	c.JSON(http.StatusOK, gin.H{
		"usage":           snapshot,
		"failed_requests": snapshot.FailureCount,
	})
}

// ExportUsageStatistics returns a complete database-backed usage snapshot for backup/migration.
func (h *Handler) ExportUsageStatistics(c *gin.Context) {
	if h == nil || h.usageService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage service not available"})
		return
	}

	snapshot, err := h.usageService.GetUsageWithFilter(c.Request.Context(), dto.UsageFilter{})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	payload := repodto.StatisticsSnapshot{}
	if snapshot != nil {
		payload = *snapshot
	}
	c.JSON(http.StatusOK, usageExportPayload{
		Version:    2,
		ExportedAt: time.Now().UTC(),
		Usage:      payload,
	})
}

// ImportUsageStatistics imports a previously exported usage snapshot into database storage.
func (h *Handler) ImportUsageStatistics(c *gin.Context) {
	if h == nil || h.usageService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage service not available"})
		return
	}

	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}

	var payload usageImportPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}
	if payload.Version != 0 && payload.Version != 1 && payload.Version != 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported version"})
		return
	}
	if payload.Usage == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "usage snapshot is required"})
		return
	}

	result, err := h.usageService.ImportUsageSnapshot(c.Request.Context(), payload.Usage)
	if err != nil {
		if errors.Is(err, keeperservice.ErrInvalidUsageImportSnapshot) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"added":           result.Added,
		"skipped":         result.Skipped,
		"total_requests":  result.TotalRequests,
		"failed_requests": result.FailedCount,
	})
}

// GetUsageQueue returns queued usage records as JSON objects.
func (h *Handler) GetUsageQueue(c *gin.Context) {
	count, errCount := parseUsageQueueCount(c.Query("count"))
	if errCount != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errCount.Error()})
		return
	}

	records := redisqueue.PopOldest(count)
	payload := make([]json.RawMessage, 0, len(records))
	for _, record := range records {
		payload = append(payload, json.RawMessage(record))
	}
	c.JSON(http.StatusOK, payload)
}

// GetDBUsageStatistics returns usage statistics from database with optional time range filter.
func (h *Handler) GetDBUsageStatistics(c *gin.Context) {
	if h == nil || h.usageService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage service not available"})
		return
	}

	filter := buildUsageFilterFromRequest(c)
	snapshot, err := h.usageService.GetUsageWithFilter(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"usage": snapshot,
	})
}

// GetDBUsageOverview returns usage overview from database with statistics.
func (h *Handler) GetDBUsageOverview(c *gin.Context) {
	if h == nil || h.usageService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage service not available"})
		return
	}

	filter := buildUsageFilterFromRequest(c)
	overview, err := h.usageService.GetUsageOverview(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, overview)
}

// GetDBUsageEvents returns paginated usage events from database.
func (h *Handler) GetDBUsageEvents(c *gin.Context) {
	if h == nil || h.usageService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage service not available"})
		return
	}

	filter := buildUsageFilterFromRequest(c)
	page, err := h.usageService.ListUsageEvents(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, page)
}

// GetDBUsageAnalysis returns usage analysis by API and model from database.
func (h *Handler) GetDBUsageAnalysis(c *gin.Context) {
	if h == nil || h.usageService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage service not available"})
		return
	}

	filter := buildUsageFilterFromRequest(c)
	analysis, err := h.usageService.GetUsageAnalysis(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, analysis)
}

// GetDBUsageEventFilterOptions returns available filter options for usage events.
func (h *Handler) GetDBUsageEventFilterOptions(c *gin.Context) {
	if h == nil || h.usageService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage service not available"})
		return
	}

	filter := buildUsageFilterFromRequest(c)
	options, err := h.usageService.ListUsageEventFilterOptions(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, options)
}

func parseUsageQueueCount(raw string) (int, error) {
	count, err := strconv.Atoi(raw)
	if err != nil || count <= 0 {
		return 0, errors.New("count must be positive")
	}
	return count, nil
}

func buildUsageFilterFromRequest(c *gin.Context) dto.UsageFilter {
	filter := dto.UsageFilter{}

	if rangeVal := c.Query("range"); rangeVal != "" {
		filter.Range = rangeVal
	}

	if startTimeStr := c.Query("start_time"); startTimeStr != "" {
		if t, err := time.Parse(time.RFC3339, startTimeStr); err == nil {
			filter.StartTime = &t
		}
	}

	if endTimeStr := c.Query("end_time"); endTimeStr != "" {
		if t, err := time.Parse(time.RFC3339, endTimeStr); err == nil {
			filter.EndTime = &t
		}
	}

	if limitStr := c.Query("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil {
			filter.Limit = limit
		}
	}

	if pageStr := c.Query("page"); pageStr != "" {
		if page, err := strconv.Atoi(pageStr); err == nil {
			filter.Page = page
		}
	}

	if pageSizeStr := c.Query("page_size"); pageSizeStr != "" {
		if pageSize, err := strconv.Atoi(pageSizeStr); err == nil {
			filter.PageSize = pageSize
		}
	}

	if offsetStr := c.Query("offset"); offsetStr != "" {
		if offset, err := strconv.Atoi(offsetStr); err == nil {
			filter.Offset = offset
		}
	}

	if model := c.Query("model"); model != "" {
		filter.Model = model
	}

	if source := c.Query("source"); source != "" {
		filter.Source = source
	}

	if authIndex := c.Query("auth_index"); authIndex != "" {
		filter.AuthIndex = authIndex
	}

	if result := c.Query("result"); result != "" {
		filter.Result = result
	}

	return filter
}
