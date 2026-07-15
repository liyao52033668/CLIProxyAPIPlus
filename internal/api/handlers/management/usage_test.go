package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	memoryusage "github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	usageconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository"
	repodto "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository/dto"
	keeperservice "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service"
	servicedto "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service/dto"
	"gorm.io/gorm"
)

func openManagementUsageTestDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := repository.OpenDatabase(usageconfig.Config{SQLitePath: filepath.Join(t.TempDir(), "management-usage.db")})
	if err != nil {
		t.Fatalf("OpenDatabase returned error: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql database: %v", err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Fatalf("close database: %v", err)
		}
	})
	return db
}

func newManagementUsageHandler(t *testing.T, db *gorm.DB) *Handler {
	t.Helper()
	stats := memoryusage.NewRequestStatistics()
	if err := memoryusage.RestoreRequestStatistics(context.Background(), db, stats); err != nil {
		t.Fatalf("restore request statistics: %v", err)
	}
	h := &Handler{cfg: &config.Config{}}
	h.SetUsageService(keeperservice.NewUsageService(db))
	h.SetUsageStatistics(stats)
	return h
}

func TestGetUsageStatisticsIncludesMemoryEventOverview(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stats := memoryusage.NewRequestStatistics()
	stats.ReplaceEvents([]servicedto.UsageEventRecord{
		{
			Timestamp:   time.Now().UTC().Add(-time.Hour),
			Source:      "source-a",
			AuthIndex:   "auth-a",
			TotalTokens: 25,
		},
	})
	h := &Handler{cfg: &config.Config{}}
	h.SetUsageStatistics(stats)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage?range=24h", nil)
	h.GetUsageStatistics(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("usage status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response struct {
		EventCache    servicedto.UsageEventCacheInfo `json:"event_cache"`
		KeyStats      servicedto.UsageKeyStats       `json:"key_stats"`
		ServiceHealth servicedto.UsageOverviewHealth `json:"service_health"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal usage response: %v", err)
	}
	if response.EventCache.RetainedCount != 1 || response.KeyStats.ByAuthIndex["auth-a"].Tokens != 25 {
		t.Fatalf("unexpected memory event overview: %+v", response)
	}
	if response.ServiceHealth.TotalSuccess != 1 || len(response.ServiceHealth.BlockDetails) != 7*96 {
		t.Fatalf("unexpected memory service health: %+v", response.ServiceHealth)
	}
}

type failingExportUsageProvider struct {
	keeperservice.UsageProvider
}

func (p *failingExportUsageProvider) ExportUsageSnapshot(_ context.Context, output io.Writer, _ time.Time, _ servicedto.UsageFilter) error {
	_, _ = io.WriteString(output, `{"partial":true}`)
	return errors.New("export failed after partial generation")
}

func TestExportUsageStatisticsReturnsDatabaseSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openManagementUsageTestDatabase(t)
	if _, _, err := repository.InsertUsageEvents(db, []entities.UsageEvent{{
		EventKey:        "event-1",
		APIGroupKey:     "provider-a",
		Model:           "claude-sonnet",
		Timestamp:       time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		Source:          "source-a",
		AuthIndex:       "2",
		LatencyMS:       321,
		InputTokens:     10,
		OutputTokens:    5,
		ReasoningTokens: 2,
		CachedTokens:    1,
		TotalTokens:     18,
	}}); err != nil {
		t.Fatalf("InsertUsageEvents returned error: %v", err)
	}

	h := newManagementUsageHandler(t, db)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/export", nil)

	h.ExportUsageStatistics(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload usageExportPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}
	if payload.Version != 3 {
		t.Fatalf("version = %d, want 3", payload.Version)
	}
	if payload.Usage.TotalRequests != 1 {
		t.Fatalf("total_requests = %d, want 1", payload.Usage.TotalRequests)
	}
	apiSnapshot, ok := payload.Usage.APIs["provider-a"]
	if !ok {
		t.Fatalf("expected exported snapshot to contain provider-a")
	}
	modelSnapshot, ok := apiSnapshot.Models["claude-sonnet"]
	if !ok {
		t.Fatalf("expected exported snapshot to contain claude-sonnet")
	}
	if len(modelSnapshot.Details) != 1 {
		t.Fatalf("details len = %d, want 1", len(modelSnapshot.Details))
	}
}

func TestExportUsageStatisticsReturnsErrorBeforeWritingPartialSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openManagementUsageTestDatabase(t)
	h := &Handler{cfg: &config.Config{}}
	h.SetUsageService(&failingExportUsageProvider{UsageProvider: keeperservice.NewUsageService(db)})
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/export", nil)

	h.ExportUsageStatistics(ctx)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusInternalServerError, recorder.Body.String())
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte(`"partial"`)) {
		t.Fatalf("partial export leaked into error response: %s", recorder.Body.String())
	}
}

func TestExportUsageStatisticsFiltersSelectedTimeRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openManagementUsageTestDatabase(t)
	insideTimestamp := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	events := []entities.UsageEvent{
		{
			EventKey:    "event-inside",
			APIGroupKey: "provider-a",
			Model:       "model-a",
			Timestamp:   insideTimestamp,
			TotalTokens: 10,
		},
		{
			EventKey:    "event-outside",
			APIGroupKey: "provider-a",
			Model:       "model-a",
			Timestamp:   insideTimestamp.Add(-24 * time.Hour),
			TotalTokens: 20,
		},
	}
	if _, _, err := repository.InsertUsageEvents(db, events); err != nil {
		t.Fatalf("InsertUsageEvents returned error: %v", err)
	}

	h := newManagementUsageHandler(t, db)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(
		http.MethodGet,
		"/v0/management/usage/export?range=custom&start_time=2026-05-01T00:00:00Z&end_time=2026-05-01T23:59:59Z",
		nil,
	)

	h.ExportUsageStatistics(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var payload usageExportPayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}
	if payload.Usage.TotalRequests != 1 || payload.Usage.TotalTokens != 10 {
		t.Fatalf("unexpected filtered totals: requests=%d tokens=%d", payload.Usage.TotalRequests, payload.Usage.TotalTokens)
	}
	details := payload.Usage.APIs["provider-a"].Models["model-a"].Details
	if len(details) != 1 || !details[0].Timestamp.Equal(insideTimestamp) {
		t.Fatalf("filtered details = %+v, want only %s", details, insideTimestamp)
	}
}

func TestGetDBUsageStatisticsOmitsRequestDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openManagementUsageTestDatabase(t)
	if _, _, err := repository.InsertUsageEvents(db, []entities.UsageEvent{{
		EventKey:    "event-aggregate",
		APIGroupKey: "provider-a",
		Model:       "model-a",
		Timestamp:   time.Now().UTC(),
		Source:      "source-a",
		AuthIndex:   "auth-a",
		TotalTokens: 10,
	}}); err != nil {
		t.Fatalf("InsertUsageEvents returned error: %v", err)
	}

	h := newManagementUsageHandler(t, db)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/db?range=all", nil)
	h.GetDBUsageStatistics(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var payload struct {
		Usage repodto.StatisticsSnapshot `json:"usage"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal usage response: %v", err)
	}
	if payload.Usage.TotalRequests != 1 || payload.Usage.TotalTokens != 10 {
		t.Fatalf("unexpected aggregate usage: %+v", payload.Usage)
	}
	if details := payload.Usage.APIs["provider-a"].Models["model-a"].Details; len(details) != 0 {
		t.Fatalf("expected aggregate endpoint to omit request details, got %+v", details)
	}
}

func TestDBUsageRangeKeepsOverviewEventsAndKeyStatsInSync(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openManagementUsageTestDatabase(t)
	now := time.Now().UTC()
	events := []entities.UsageEvent{
		{
			EventKey:    "recent-success",
			APIGroupKey: "provider-a",
			Model:       "model-a",
			Timestamp:   now.Add(-time.Hour),
			Source:      "source-a",
			AuthIndex:   "auth-a",
			TotalTokens: 10,
		},
		{
			EventKey:    "recent-failure",
			APIGroupKey: "provider-a",
			Model:       "model-a",
			Timestamp:   now.Add(-2 * time.Hour),
			Source:      "source-a",
			AuthIndex:   "auth-a",
			Failed:      true,
			TotalTokens: 5,
		},
		{
			EventKey:    "old-success",
			APIGroupKey: "provider-b",
			Model:       "model-b",
			Timestamp:   now.Add(-6 * time.Hour),
			Source:      "source-b",
			AuthIndex:   "auth-b",
			TotalTokens: 20,
		},
	}
	if _, _, err := repository.InsertUsageEvents(db, events); err != nil {
		t.Fatalf("InsertUsageEvents returned error: %v", err)
	}

	h := newManagementUsageHandler(t, db)
	overviewRecorder := httptest.NewRecorder()
	overviewContext, _ := gin.CreateTestContext(overviewRecorder)
	overviewContext.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/db/overview?range=4h", nil)
	h.GetDBUsageOverview(overviewContext)
	if overviewRecorder.Code != http.StatusOK {
		t.Fatalf("overview status = %d, want %d; body=%s", overviewRecorder.Code, http.StatusOK, overviewRecorder.Body.String())
	}

	var overview servicedto.UsageOverviewSnapshot
	if err := json.Unmarshal(overviewRecorder.Body.Bytes(), &overview); err != nil {
		t.Fatalf("unmarshal overview: %v", err)
	}
	if overview.Usage == nil {
		t.Fatal("overview usage is nil")
	}
	if overview.Usage.TotalRequests != 2 || overview.Usage.SuccessCount != 1 || overview.Usage.FailureCount != 1 {
		t.Fatalf("overview counts = total:%d success:%d failure:%d, want 2/1/1", overview.Usage.TotalRequests, overview.Usage.SuccessCount, overview.Usage.FailureCount)
	}
	keyCount := overview.KeyStats.ByAuthIndex["auth-a"]
	if keyCount.Success != 1 || keyCount.Failure != 1 {
		t.Fatalf("auth-a key stats = success:%d failure:%d, want 1/1", keyCount.Success, keyCount.Failure)
	}
	if _, ok := overview.KeyStats.ByAuthIndex["auth-b"]; ok {
		t.Fatal("old auth-b event must not be included in 4h key stats")
	}

	if err := db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&entities.UsageEvent{}).Error; err != nil {
		t.Fatalf("delete database events after memory restore: %v", err)
	}

	eventsRecorder := httptest.NewRecorder()
	eventsContext, _ := gin.CreateTestContext(eventsRecorder)
	eventsContext.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/db/events?range=4h&page=1&page_size=20", nil)
	h.GetDBUsageEvents(eventsContext)
	if eventsRecorder.Code != http.StatusOK {
		t.Fatalf("events status = %d, want %d; body=%s", eventsRecorder.Code, http.StatusOK, eventsRecorder.Body.String())
	}

	var eventsPage servicedto.UsageEventsPage
	if err := json.Unmarshal(eventsRecorder.Body.Bytes(), &eventsPage); err != nil {
		t.Fatalf("unmarshal events: %v", err)
	}
	if eventsPage.TotalCount != 2 || len(eventsPage.Events) != 2 || eventsPage.PageSize != 20 {
		t.Fatalf("events page = count:%d rows:%d page_size:%d, want 2/2/20", eventsPage.TotalCount, len(eventsPage.Events), eventsPage.PageSize)
	}

	optionsRecorder := httptest.NewRecorder()
	optionsContext, _ := gin.CreateTestContext(optionsRecorder)
	optionsContext.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/db/filter-options?range=4h", nil)
	h.GetDBUsageEventFilterOptions(optionsContext)
	if optionsRecorder.Code != http.StatusOK {
		t.Fatalf("filter options status = %d, want %d; body=%s", optionsRecorder.Code, http.StatusOK, optionsRecorder.Body.String())
	}
	var options servicedto.UsageEventFilterOptions
	if err := json.Unmarshal(optionsRecorder.Body.Bytes(), &options); err != nil {
		t.Fatalf("unmarshal filter options: %v", err)
	}
	if len(options.Models) != 1 || options.Models[0] != "model-a" {
		t.Fatalf("filter options models = %v, want [model-a]", options.Models)
	}
}

func TestGetDBUsageEventHistoryReadsDatabase(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openManagementUsageTestDatabase(t)
	if _, _, err := repository.InsertUsageEvents(db, []entities.UsageEvent{{
		EventKey:    "history-event",
		APIGroupKey: "provider-a",
		Model:       "model-history",
		Timestamp:   time.Now().UTC().Add(-time.Hour),
		TotalTokens: 10,
	}}); err != nil {
		t.Fatalf("insert history event: %v", err)
	}

	h := newManagementUsageHandler(t, db)
	h.usageStats.ReplaceEvents(nil)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/db/events/history?range=24h&page=1&page_size=20", nil)
	h.GetDBUsageEventHistory(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("history status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var page servicedto.UsageEventsPage
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatalf("unmarshal history events: %v", err)
	}
	if page.TotalCount != 1 || len(page.Events) != 1 || page.Events[0].Model != "model-history" || page.Cache != nil {
		t.Fatalf("unexpected database history page: %+v", page)
	}
}

func TestImportUsageStatisticsImportsIntoDatabase(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openManagementUsageTestDatabase(t)
	h := newManagementUsageHandler(t, db)
	requestedAt := time.Now().UTC().Add(-time.Minute)

	payload := usageImportPayload{
		Version: 2,
		Usage: &repodto.StatisticsSnapshot{
			APIs: map[string]repodto.APISnapshot{
				"provider-a": {
					Models: map[string]repodto.ModelSnapshot{
						"claude-sonnet": {
							TotalRequests: 1,
							SuccessCount:  1,
							TotalTokens:   18,
							Details: []repodto.RequestDetail{{
								Timestamp: requestedAt,
								LatencyMS: 321,
								Source:    "source-a",
								AuthIndex: "2",
								Failed:    false,
								Tokens: repodto.TokenStats{
									InputTokens:     10,
									OutputTokens:    5,
									ReasoningTokens: 2,
									CachedTokens:    1,
									TotalTokens:     18,
								},
							}},
						},
					},
				},
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", bytes.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.ImportUsageStatistics(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var result struct {
		Added         int   `json:"added"`
		Skipped       int   `json:"skipped"`
		TotalRequests int64 `json:"total_requests"`
		FailedCount   int64 `json:"failed_requests"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}
	if result.Added != 1 || result.Skipped != 0 || result.TotalRequests != 1 || result.FailedCount != 0 {
		t.Fatalf("unexpected import result: %+v", result)
	}

	snapshot, err := repository.BuildUsageSnapshot(db)
	if err != nil {
		t.Fatalf("BuildUsageSnapshot returned error: %v", err)
	}
	if snapshot.TotalRequests != 1 {
		t.Fatalf("database total_requests = %d, want 1", snapshot.TotalRequests)
	}
	memorySnapshot := h.usageStats.Snapshot()
	if memorySnapshot.TotalRequests != 1 || memorySnapshot.TotalTokens != 18 {
		t.Fatalf("memory snapshot was not refreshed after import: %+v", memorySnapshot)
	}
	memoryEvents := h.usageStats.ListUsageEvents(servicedto.UsageFilter{Page: 1, PageSize: 20})
	if memoryEvents.TotalCount != 1 || len(memoryEvents.Events) != 1 || !memoryEvents.Events[0].Timestamp.Equal(requestedAt) {
		t.Fatalf("memory events were not refreshed after import: %+v", memoryEvents)
	}
}

func TestImportUsageStatisticsRejectsAggregateOnlySnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openManagementUsageTestDatabase(t)
	h := newManagementUsageHandler(t, db)

	payload := usageImportPayload{
		Version: 2,
		Usage: &repodto.StatisticsSnapshot{
			APIs: map[string]repodto.APISnapshot{
				"provider-a": {
					Models: map[string]repodto.ModelSnapshot{
						"claude-sonnet": {
							TotalRequests: 2,
							SuccessCount:  2,
							TotalTokens:   30,
						},
					},
				},
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", bytes.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.ImportUsageStatistics(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestGetUsageQueuePopsRequestedRecords(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withManagementUsageQueue(t, func() {
		redisqueue.Enqueue([]byte(`{"id":1}`))
		redisqueue.Enqueue([]byte(`{"id":2}`))
		redisqueue.Enqueue([]byte(`{"id":3}`))

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=2", nil)

		h := &Handler{}
		h.GetUsageQueue(ginCtx)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var payload []json.RawMessage
		if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
			t.Fatalf("unmarshal response: %v", errUnmarshal)
		}
		if len(payload) != 2 {
			t.Fatalf("response records = %d, want 2", len(payload))
		}
		requireRecordID(t, payload[0], 1)
		requireRecordID(t, payload[1], 2)

		remaining := redisqueue.PopOldest(10)
		if len(remaining) != 1 || string(remaining[0]) != `{"id":3}` {
			t.Fatalf("remaining queue = %q, want third item only", remaining)
		}
	})
}

func TestGetUsageQueueInvalidCountDoesNotPop(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withManagementUsageQueue(t, func() {
		redisqueue.Enqueue([]byte(`{"id":1}`))

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=0", nil)

		h := &Handler{}
		h.GetUsageQueue(ginCtx)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
		}

		remaining := redisqueue.PopOldest(10)
		if len(remaining) != 1 || string(remaining[0]) != `{"id":1}` {
			t.Fatalf("remaining queue = %q, want original item", remaining)
		}
	})
}

func withManagementUsageQueue(t *testing.T, fn func()) {
	t.Helper()

	prevQueueEnabled := redisqueue.Enabled()
	redisqueue.SetEnabled(false)
	redisqueue.SetEnabled(true)

	defer func() {
		redisqueue.SetEnabled(false)
		redisqueue.SetEnabled(prevQueueEnabled)
	}()

	fn()
}

func requireRecordID(t *testing.T, raw json.RawMessage, want int) {
	t.Helper()

	var payload struct {
		ID int `json:"id"`
	}
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal record: %v", errUnmarshal)
	}
	if payload.ID != want {
		t.Fatalf("record id = %d, want %d", payload.ID, want)
	}
}

func TestBuildUsageFilterFromRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("defaults and valid pagination", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/events?page=2&page_size=50&result=success", nil)

		filter, err := buildUsageFilterFromRequest(ctx)
		if err != nil {
			t.Fatalf("buildUsageFilterFromRequest returned error: %v", err)
		}
		if filter.Page != 2 {
			t.Fatalf("page = %d, want 2", filter.Page)
		}
		if filter.PageSize != 50 || filter.Limit != 50 {
			t.Fatalf("page_size/limit = %d/%d, want 50/50", filter.PageSize, filter.Limit)
		}
		if filter.Offset != 50 {
			t.Fatalf("offset = %d, want 50", filter.Offset)
		}
		if filter.Result != "success" {
			t.Fatalf("result = %q, want success", filter.Result)
		}
	})

	t.Run("preset range resolves UTC boundaries", func(t *testing.T) {
		filter := servicedto.UsageFilter{Range: "4h"}
		anchor := time.Date(2026, time.July, 14, 12, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))

		if err := applyManagementUsageRange(&filter, anchor); err != nil {
			t.Fatalf("applyManagementUsageRange returned error: %v", err)
		}
		wantEnd := anchor.UTC()
		wantStart := wantEnd.Add(-4 * time.Hour)
		if filter.StartTime == nil || !filter.StartTime.Equal(wantStart) {
			t.Fatalf("start_time = %v, want %v", filter.StartTime, wantStart)
		}
		if filter.EndTime == nil || !filter.EndTime.Equal(wantEnd) {
			t.Fatalf("end_time = %v, want %v", filter.EndTime, wantEnd)
		}
	})

	t.Run("today resolves local day boundaries", func(t *testing.T) {
		filter := servicedto.UsageFilter{Range: "today"}
		anchor := time.Date(2026, time.July, 14, 12, 30, 0, 0, time.Local)

		if err := applyManagementUsageRange(&filter, anchor); err != nil {
			t.Fatalf("applyManagementUsageRange returned error: %v", err)
		}
		localStart := time.Date(2026, time.July, 14, 0, 0, 0, 0, time.Local)
		wantStart := localStart.UTC()
		wantEnd := localStart.AddDate(0, 0, 1).Add(-time.Nanosecond).UTC()
		if filter.StartTime == nil || !filter.StartTime.Equal(wantStart) {
			t.Fatalf("start_time = %v, want %v", filter.StartTime, wantStart)
		}
		if filter.EndTime == nil || !filter.EndTime.Equal(wantEnd) {
			t.Fatalf("end_time = %v, want %v", filter.EndTime, wantEnd)
		}
	})

	t.Run("unsupported range is rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/events?range=invalid", nil)

		_, err := buildUsageFilterFromRequest(ctx)
		if err == nil {
			t.Fatal("expected unsupported range error")
		}
	})

	t.Run("invalid page_size is rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/events?page_size=9999", nil)

		_, err := buildUsageFilterFromRequest(ctx)
		if err == nil {
			t.Fatal("expected invalid page_size error")
		}
	})

	t.Run("invalid start_time is rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/events?start_time=not-a-time", nil)

		_, err := buildUsageFilterFromRequest(ctx)
		if err == nil {
			t.Fatal("expected invalid start_time error")
		}
	})

	t.Run("start after end is rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/events?start_time=2026-05-02T00:00:00Z&end_time=2026-05-01T00:00:00Z", nil)

		_, err := buildUsageFilterFromRequest(ctx)
		if err == nil {
			t.Fatal("expected start_time/end_time order error")
		}
	})

	t.Run("invalid result is rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/events?result=maybe", nil)

		_, err := buildUsageFilterFromRequest(ctx)
		if err == nil {
			t.Fatal("expected invalid result error")
		}
	})
}

func TestGetDBUsageEventsRejectsInvalidFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openManagementUsageTestDatabase(t)
	h := newManagementUsageHandler(t, db)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/events?page_size=9999", nil)

	h.GetDBUsageEvents(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}
