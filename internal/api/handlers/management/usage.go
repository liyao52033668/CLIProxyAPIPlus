package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	repodto "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository/dto"
	keeperservice "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service"
	dto "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service/dto"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/sirupsen/logrus"
)

// Keep in sync with internal/usage/keeper/api/usage_filter.go.
var allowedManagementUsagePageSizes = map[int]struct{}{
	20:   {},
	50:   {},
	100:  {},
	500:  {},
	1000: {},
}

var managementUsageRangeDurations = map[string]time.Duration{
	"4h":  4 * time.Hour,
	"8h":  8 * time.Hour,
	"12h": 12 * time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
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
	filter, errFilter := buildUsageFilterFromRequest(c)
	if errFilter != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errFilter.Error()})
		return
	}
	var overview usage.UsageOverviewSnapshot
	var eventCache dto.UsageEventCacheInfo
	var keyStats dto.UsageKeyStats
	var serviceHealth dto.UsageOverviewHealth
	if h != nil && h.usageStats != nil {
		overview = h.usageStats.UsageOverview(filter)
		keyStats, serviceHealth, eventCache = h.usageStats.UsageEventOverview(filter)
	}
	c.JSON(http.StatusOK, gin.H{
		"usage":           overview.Usage,
		"failed_requests": overview.Usage.FailureCount,
		"summary":         overview.Summary,
		"series":          overview.Series,
		"hourly_series":   overview.HourlySeries,
		"daily_series":    overview.DailySeries,
		"range_start":     overview.StartTime,
		"range_end":       overview.EndTime,
		"bucket_by_day":   overview.BucketByDay,
		"event_cache":     eventCache,
		"key_stats":       keyStats,
		"service_health":  serviceHealth,
	})
}

// ExportUsageStatistics returns a complete database-backed usage snapshot for backup/migration.
func (h *Handler) ExportUsageStatistics(c *gin.Context) {
	if h == nil || h.usageService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage service not available"})
		return
	}

	filter, errFilter := buildUsageFilterFromRequest(c)
	if errFilter != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errFilter.Error()})
		return
	}

	exportFile, err := os.CreateTemp("", "cli-proxy-usage-export-*.json")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	exportPath := exportFile.Name()
	defer func() {
		if errClose := exportFile.Close(); errClose != nil {
			logrus.WithError(errClose).Error("failed to close usage export file")
		}
		if errRemove := os.Remove(exportPath); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			logrus.WithError(errRemove).Error("failed to remove usage export file")
		}
	}()
	if err = h.usageService.ExportUsageSnapshot(c.Request.Context(), exportFile, time.Now().UTC(), filter); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if _, err = exportFile.Seek(0, io.SeekStart); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Header("Content-Type", "application/json; charset=utf-8")
	if _, err = io.Copy(c.Writer, exportFile); err != nil {
		logrus.WithError(err).Error("failed to write usage export response")
	}
}

// ImportUsageStatistics imports a previously exported usage snapshot into database storage.
func (h *Handler) ImportUsageStatistics(c *gin.Context) {
	if h == nil || h.usageService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage service not available"})
		return
	}

	requestContext := c.Request.Context()
	operationContext := context.WithoutCancel(requestContext)
	var result *dto.UsageImportResult
	importCommitted := false
	err := coreusage.DefaultManager().WithDispatchPaused(requestContext, func() error {
		var errImport error
		result, errImport = h.usageService.ImportUsageSnapshotStream(operationContext, c.Request.Body)
		if errImport != nil {
			return errImport
		}
		importCommitted = true
		if errReload := usage.ReloadRequestStatistics(operationContext, h.usageService, h.usageStats); errReload != nil {
			return fmt.Errorf("refresh in-memory usage after committed import: %w", errReload)
		}
		return nil
	})
	if err != nil {
		if importCommitted {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		switch {
		case errors.Is(err, keeperservice.ErrInvalidUsageImportJSON):
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		case errors.Is(err, keeperservice.ErrUnsupportedUsageImportVersion):
			c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported version"})
		case errors.Is(err, keeperservice.ErrInvalidUsageImportSnapshot):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
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

// GetDBUsageStatistics returns aggregate usage statistics without request details.
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
	snapshot, err := h.usageService.GetUsageAggregateWithFilter(c.Request.Context(), filter)
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

// GetDBUsageEvents returns paginated usage events from memory.
func (h *Handler) GetDBUsageEvents(c *gin.Context) {
	if h == nil || h.usageStats == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage statistics not available"})
		return
	}

	filter, errFilter := buildUsageFilterFromRequest(c)
	if errFilter != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errFilter.Error()})
		return
	}
	c.JSON(http.StatusOK, h.usageStats.ListUsageEvents(filter))
}

// GetDBUsageEventHistory returns paginated usage events from database storage.
func (h *Handler) GetDBUsageEventHistory(c *gin.Context) {
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

// GetDBUsageEventFilterOptions returns available in-memory filter options for usage events.
func (h *Handler) GetDBUsageEventFilterOptions(c *gin.Context) {
	if h == nil || h.usageStats == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage statistics not available"})
		return
	}

	filter, errFilter := buildUsageFilterFromRequest(c)
	if errFilter != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errFilter.Error()})
		return
	}
	c.JSON(http.StatusOK, h.usageStats.ListUsageEventFilterOptions(filter))
}

func parseUsageQueueCount(raw string) (int, error) {
	count, err := strconv.Atoi(raw)
	if err != nil || count <= 0 {
		return 0, errors.New("count must be positive")
	}
	return count, nil
}

func applyManagementUsageRange(filter *dto.UsageFilter, anchor time.Time) error {
	if filter == nil {
		return nil
	}

	switch filter.Range {
	case "all":
		return nil
	case "today":
		localAnchor := anchor.In(time.Local)
		localStart := time.Date(localAnchor.Year(), localAnchor.Month(), localAnchor.Day(), 0, 0, 0, 0, time.Local)
		startTime := localStart.UTC()
		endTime := localStart.AddDate(0, 0, 1).Add(-time.Nanosecond).UTC()
		filter.StartTime = &startTime
		filter.EndTime = &endTime
		return nil
	case "custom":
		return fmt.Errorf("custom range requires start_time and end_time")
	default:
		duration, ok := managementUsageRangeDurations[filter.Range]
		if !ok {
			return fmt.Errorf("unsupported usage range %q", filter.Range)
		}
		endTime := anchor.UTC()
		startTime := endTime.Add(-duration)
		filter.StartTime = &startTime
		filter.EndTime = &endTime
		return nil
	}
}

// buildUsageFilterFromRequest parses management usage query parameters with
// strict validation so invalid filters fail closed instead of being ignored.
func buildUsageFilterFromRequest(c *gin.Context) (dto.UsageFilter, error) {
	if c == nil {
		return dto.UsageFilter{}, nil
	}

	filter := dto.UsageFilter{
		Range:    "all",
		Page:     1,
		PageSize: dto.DefaultUsageEventsLimit,
		Limit:    dto.DefaultUsageEventsLimit,
	}

	if rangeVal := strings.TrimSpace(c.Query("range")); rangeVal != "" {
		filter.Range = rangeVal
	}
	if filter.Range != "all" && filter.Range != "today" && filter.Range != "custom" {
		if _, ok := managementUsageRangeDurations[filter.Range]; !ok {
			return dto.UsageFilter{}, fmt.Errorf("unsupported usage range %q", filter.Range)
		}
	}

	if startTimeStr := strings.TrimSpace(c.Query("start_time")); startTimeStr != "" {
		t, err := time.Parse(time.RFC3339, startTimeStr)
		if err != nil {
			return dto.UsageFilter{}, fmt.Errorf("invalid start_time %q", startTimeStr)
		}
		t = t.UTC()
		filter.StartTime = &t
	}

	if endTimeStr := strings.TrimSpace(c.Query("end_time")); endTimeStr != "" {
		t, err := time.Parse(time.RFC3339, endTimeStr)
		if err != nil {
			return dto.UsageFilter{}, fmt.Errorf("invalid end_time %q", endTimeStr)
		}
		t = t.UTC()
		filter.EndTime = &t
	}

	if filter.StartTime != nil && filter.EndTime != nil && filter.StartTime.After(*filter.EndTime) {
		return dto.UsageFilter{}, fmt.Errorf("start_time must be before end_time")
	}
	if filter.StartTime == nil && filter.EndTime == nil {
		if err := applyManagementUsageRange(&filter, time.Now()); err != nil {
			return dto.UsageFilter{}, err
		}
	} else if filter.Range == "custom" && (filter.StartTime == nil || filter.EndTime == nil) {
		return dto.UsageFilter{}, fmt.Errorf("custom range requires start_time and end_time")
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
