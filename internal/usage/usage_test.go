package usage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	usageconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestRequestStatisticsPluginRecordsUsageInMemory(t *testing.T) {
	previousEnabled := IsStatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() { SetStatisticsEnabled(previousEnabled) })

	stats := NewRequestStatistics()
	requestedAt := time.Date(2026, 7, 15, 11, 30, 0, 0, time.UTC)
	plugin := &requestStatisticsPlugin{stats: stats}
	plugin.HandleUsage(context.Background(), coreusage.Record{
		APIKey:      "group-a",
		Model:       "model-a",
		RequestedAt: requestedAt,
		Detail: coreusage.Detail{
			InputTokens:     10,
			OutputTokens:    5,
			ReasoningTokens: 2,
			CachedTokens:    1,
		},
	})

	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 1 || snapshot.SuccessCount != 1 || snapshot.TotalTokens != 18 {
		t.Fatalf("unexpected snapshot totals: %+v", snapshot)
	}
	model := snapshot.APIs["group-a"].Models["model-a"]
	if model.InputTokens != 10 || model.OutputTokens != 5 || model.TotalTokens != 18 {
		t.Fatalf("unexpected model totals: %+v", model)
	}
	dayKey := requestedAt.In(time.Local).Format("2006-01-02")
	hourKey := requestedAt.UTC().Format("2006-01-02T15:00:00Z")
	if snapshot.RequestsByDay[dayKey] != 1 || snapshot.RequestsByHour[hourKey] != 1 {
		t.Fatalf("unexpected request buckets: days=%v hours=%v", snapshot.RequestsByDay, snapshot.RequestsByHour)
	}
	if snapshot.TokensByDay[dayKey] != 18 || snapshot.TokensByHour[hourKey] != 18 {
		t.Fatalf("unexpected token buckets: days=%v hours=%v", snapshot.TokensByDay, snapshot.TokensByHour)
	}
}

func TestRestoreRequestStatisticsLoadsDatabaseBaseline(t *testing.T) {
	db, err := repository.OpenDatabase(usageconfig.Config{SQLitePath: filepath.Join(t.TempDir(), "usage.db")})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql database: %v", err)
	}
	t.Cleanup(func() {
		if errClose := sqlDB.Close(); errClose != nil {
			t.Fatalf("close database: %v", errClose)
		}
	})

	requestedAt := time.Date(2026, 7, 15, 9, 20, 0, 0, time.UTC)
	if _, _, err = repository.InsertUsageEvents(db, []entities.UsageEvent{
		{
			EventKey:     "event-success",
			APIGroupKey:  "group-a",
			Model:        "model-a",
			Timestamp:    requestedAt,
			InputTokens:  12,
			OutputTokens: 4,
			TotalTokens:  16,
		},
		{
			EventKey:     "event-failure",
			APIGroupKey:  "group-a",
			Model:        "model-a",
			Timestamp:    requestedAt.Add(time.Minute),
			Failed:       true,
			InputTokens:  3,
			OutputTokens: 1,
			TotalTokens:  4,
		},
	}); err != nil {
		t.Fatalf("insert usage events: %v", err)
	}

	stats := NewRequestStatistics()
	if err = RestoreRequestStatistics(context.Background(), db, stats); err != nil {
		t.Fatalf("restore request statistics: %v", err)
	}

	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 2 || snapshot.SuccessCount != 1 || snapshot.FailureCount != 1 || snapshot.TotalTokens != 20 {
		t.Fatalf("unexpected restored totals: %+v", snapshot)
	}
	model := snapshot.APIs["group-a"].Models["model-a"]
	if model.InputTokens != 15 || model.OutputTokens != 5 || model.TotalTokens != 20 {
		t.Fatalf("unexpected restored model totals: %+v", model)
	}
	dayKey := requestedAt.In(time.Local).Format("2006-01-02")
	if snapshot.RequestsByDay[dayKey] != 2 || snapshot.TokensByDay[dayKey] != 20 {
		t.Fatalf("unexpected restored daily buckets: requests=%v tokens=%v", snapshot.RequestsByDay, snapshot.TokensByDay)
	}
}

func TestRestoreRequestStatisticsSkipsPopulatedMemory(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record("group-memory", "model-memory", time.Now(), false, 1, 2, 3)

	if err := RestoreRequestStatistics(context.Background(), nil, stats); err != nil {
		t.Fatalf("expected populated memory to skip database restore: %v", err)
	}
	if snapshot := stats.Snapshot(); snapshot.TotalRequests != 1 || snapshot.TotalTokens != 3 {
		t.Fatalf("unexpected snapshot after skipped restore: %+v", snapshot)
	}
}
