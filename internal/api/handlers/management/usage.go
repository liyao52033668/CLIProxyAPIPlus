package management

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	repodto "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository/dto"
	keeperservice "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service"
	dto "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service/dto"
)

// Keep in sync with internal/usage/keeper/api/usage_filter.go.
var allowedManagementUsagePageSizes = map[int]struct{}{
	20:   {},
	50:   {},
	100:  {},
	500:  {},
	1000: {},
}

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

	filter, errFilter := buildUsageFilterFromRequest(c)
	if errFilter != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errFilter.Error()})
		return
	}
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

	filter, errFilter := buildUsageFilterFromRequest(c)
	if errFilter != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errFilter.Error()})
		return
	}
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

	filter, errFilter := buildUsageFilterFromRequest(c)
	if errFilter != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errFilter.Error()})
		return
	}
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

	filter, errFilter := buildUsageFilterFromRequest(c)
	if errFilter != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errFilter.Error()})
		return
	}
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

	filter, errFilter := buildUsageFilterFromRequest(c)
	if errFilter != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errFilter.Error()})
		return
	}
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

// buildUsageFilterFromRequest parses management usage query parameters with
// strict validation so invalid filters fail closed instead of being ignored.
func buildUsageFilterFromRequest(c *gin.Context) (dto.UsageFilter, error) {
	if c == nil {
		return dto.UsageFilter{}, nil
	}

	filter := dto.UsageFilter{
		Page:     1,
		PageSize: dto.DefaultUsageEventsLimit,
		Limit:    dto.DefaultUsageEventsLimit,
	}

	if rangeVal := strings.TrimSpace(c.Query("range")); rangeVal != "" {
		filter.Range = rangeVal
	}

	if startTimeStr := strings.TrimSpace(c.Query("start_time")); startTimeStr != "" {
		t, err := time.Parse(time.RFC3339, startTimeStr)
		if err != nil {
			return dto.UsageFilter{}, fmt.Errorf("invalid start_time %q", startTimeStr)
		}
		filter.StartTime = &t
	}

	if endTimeStr := strings.TrimSpace(c.Query("end_time")); endTimeStr != "" {
		t, err := time.Parse(time.RFC3339, endTimeStr)
		if err != nil {
			return dto.UsageFilter{}, fmt.Errorf("invalid end_time %q", endTimeStr)
		}
		filter.EndTime = &t
	}

	if filter.StartTime != nil && filter.EndTime != nil && filter.StartTime.After(*filter.EndTime) {
		return dto.UsageFilter{}, fmt.Errorf("start_time must be before end_time")
	}

	if pageStr := strings.TrimSpace(c.Query("page")); pageStr != "" {
		page, err := strconv.Atoi(pageStr)
		if err != nil || page < 1 {
			return dto.UsageFilter{}, fmt.Errorf("invalid page %q", pageStr)
		}
		filter.Page = page
	}

	pageSizeValue := strings.TrimSpace(c.Query("page_size"))
	if pageSizeValue == "" {
		pageSizeValue = strings.TrimSpace(c.Query("limit"))
	}
	if pageSizeValue != "" {
		pageSize, err := strconv.Atoi(pageSizeValue)
		if err != nil {
			return dto.UsageFilter{}, fmt.Errorf("invalid page_size %q", pageSizeValue)
		}
		if _, ok := allowedManagementUsagePageSizes[pageSize]; !ok {
			return dto.UsageFilter{}, fmt.Errorf("invalid page_size %q", pageSizeValue)
		}
		filter.PageSize = pageSize
		filter.Limit = pageSize
	}

	if offsetStr := strings.TrimSpace(c.Query("offset")); offsetStr != "" {
		offset, err := strconv.Atoi(offsetStr)
		if err != nil || offset < 0 {
			return dto.UsageFilter{}, fmt.Errorf("invalid offset %q", offsetStr)
		}
		filter.Offset = offset
	} else {
		filter.Offset = (filter.Page - 1) * filter.PageSize
	}

	filter.Model = strings.TrimSpace(c.Query("model"))
	filter.Source = strings.TrimSpace(c.Query("source"))
	filter.AuthIndex = strings.TrimSpace(c.Query("auth_index"))
	filter.Result = strings.TrimSpace(c.Query("result"))
	if filter.Result != "" && filter.Result != "success" && filter.Result != "failed" {
		return dto.UsageFilter{}, fmt.Errorf("invalid result %q", filter.Result)
	}

	return filter, nil
}
