package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	usageconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository"
	repodto "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository/dto"
	keeperservice "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service"
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
	h := &Handler{cfg: &config.Config{}}
	h.SetUsageService(keeperservice.NewUsageService(db))
	return h
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
	if payload.Version != 2 {
		t.Fatalf("version = %d, want 2", payload.Version)
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

func TestImportUsageStatisticsImportsIntoDatabase(t *testing.T) {
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
							TotalRequests: 1,
							SuccessCount:  1,
							TotalTokens:   18,
							Details: []repodto.RequestDetail{{
								Timestamp: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
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
