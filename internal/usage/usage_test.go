package usage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	usageconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository"
	servicedto "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service/dto"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestRequestStatisticsPluginRecordsUsageInMemory(t *testing.T) {
	previousEnabled := IsStatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() { SetStatisticsEnabled(previousEnabled) })

	stats := NewRequestStatistics()
	requestedAt := time.Now().UTC().Add(-time.Minute)
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

	page := stats.ListUsageEvents(servicedto.UsageFilter{Page: 1, PageSize: 20})
	if page.TotalCount != 1 || len(page.Events) != 1 {
		t.Fatalf("unexpected in-memory events page: %+v", page)
	}
	event := page.Events[0]
	if event.Model != "model-a" || event.TotalTokens != 18 || !event.Timestamp.Equal(requestedAt) {
		t.Fatalf("unexpected in-memory event: %+v", event)
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

	requestedAt := time.Now().UTC().Add(-2 * time.Minute)
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

	page := stats.ListUsageEvents(servicedto.UsageFilter{Page: 1, PageSize: 20})
	if page.TotalCount != 2 || len(page.Events) != 2 {
		t.Fatalf("unexpected restored events page: %+v", page)
	}
	if page.Events[0].Failed != true || page.Events[0].TotalTokens != 4 || page.Events[1].TotalTokens != 16 {
		t.Fatalf("unexpected restored event order: %+v", page.Events)
	}
}

func TestRequestStatisticsFiltersAndPaginatesUsageEvents(t *testing.T) {
	stats := NewRequestStatistics()
	baseTime := time.Now().UTC().Add(-3 * time.Minute)
	stats.ReplaceEvents([]servicedto.UsageEventRecord{
		{ID: 1, Timestamp: baseTime, Model: "model-a", Source: "source-a", AuthIndex: "auth-a", TotalTokens: 10},
		{ID: 2, Timestamp: baseTime.Add(time.Minute), Model: "model-b", Source: "source-b", AuthIndex: "auth-b", Failed: true, TotalTokens: 20},
		{ID: 3, Timestamp: baseTime.Add(2 * time.Minute), Model: "model-a", Source: "source-a", AuthIndex: "auth-c", TotalTokens: 30},
	})

	page := stats.ListUsageEvents(servicedto.UsageFilter{
		Page:     1,
		PageSize: 20,
		Model:    "model-a",
		Result:   "success",
	})
	if page.TotalCount != 2 || len(page.Events) != 2 || page.TotalPages != 1 {
		t.Fatalf("unexpected filtered events page: %+v", page)
	}
	if page.Events[0].ID != 3 || page.Events[1].ID != 1 {
		t.Fatalf("unexpected filtered event order: %+v", page.Events)
	}
	if len(page.Models) != 2 || page.Models[0] != "model-a" || page.Models[1] != "model-b" {
		t.Fatalf("unexpected page models: %v", page.Models)
	}

	secondPage := stats.ListUsageEvents(servicedto.UsageFilter{Page: 2, PageSize: 1})
	if secondPage.TotalCount != 3 || len(secondPage.Events) != 1 || secondPage.Events[0].ID != 2 || secondPage.TotalPages != 3 {
		t.Fatalf("unexpected second events page: %+v", secondPage)
	}

	startTime := baseTime.Add(time.Minute)
	endTime := baseTime.Add(2 * time.Minute)
	options := stats.ListUsageEventFilterOptions(servicedto.UsageFilter{
		StartTime: &startTime,
		EndTime:   &endTime,
		Source:    "source-a",
	})
	if len(options.Models) != 2 || options.Models[0] != "model-a" || options.Models[1] != "model-b" {
		t.Fatalf("unexpected time-window model options: %+v", options)
	}
}

func TestRequestStatisticsBoundsUsageEventsInMemory(t *testing.T) {
	stats := NewRequestStatistics()
	baseTime := time.Now().UTC().Add(-time.Duration(maxInMemoryUsageEvents+2) * time.Second)
	events := make([]servicedto.UsageEventRecord, maxInMemoryUsageEvents+1)
	for i := range events {
		events[i] = servicedto.UsageEventRecord{
			ID:        uint(i + 1),
			Timestamp: baseTime.Add(time.Duration(i) * time.Second),
			Model:     "restored",
		}
	}
	stats.ReplaceEvents(events)

	page := stats.ListUsageEvents(servicedto.UsageFilter{Page: 1, PageSize: 20})
	if page.TotalCount != maxInMemoryUsageEvents || page.Events[0].ID != uint(maxInMemoryUsageEvents+1) {
		t.Fatalf("unexpected bounded restored events: count=%d newest=%+v", page.TotalCount, page.Events[0])
	}
	if page.Cache == nil || page.Cache.RetainedCount != maxInMemoryUsageEvents || !page.Cache.HasOlderEvents || page.Cache.EstimatedBytes <= 0 {
		t.Fatalf("unexpected bounded cache info: %+v", page.Cache)
	}

	stats.RecordEvent(coreusage.Record{
		Model:       "live",
		RequestedAt: baseTime.Add(time.Duration(maxInMemoryUsageEvents+1) * time.Second),
	})
	page = stats.ListUsageEvents(servicedto.UsageFilter{Page: 1, PageSize: 20})
	wantCount := int64(maxInMemoryUsageEvents - usageEventTrimBatch)
	if page.TotalCount != wantCount || page.Events[0].Model != "live" {
		t.Fatalf("unexpected rolling event cache: count=%d newest=%+v", page.TotalCount, page.Events[0])
	}
	if page.Cache == nil || page.Cache.EvictedTotal == 0 || page.Cache.MaxEvents != maxInMemoryUsageEvents {
		t.Fatalf("unexpected rolling cache info: %+v", page.Cache)
	}
}

func TestRequestStatisticsRejectsOversizedAndExpiredUsageEvents(t *testing.T) {
	stats := NewRequestStatistics()
	stats.RecordEvent(coreusage.Record{
		Model:       strings.Repeat("x", int(maxInMemoryUsageEventSize)),
		RequestedAt: time.Now().UTC(),
	})
	stats.RecordEvent(coreusage.Record{
		Model:       "expired",
		RequestedAt: time.Now().UTC().Add(-maxInMemoryUsageEventAge - time.Hour),
	})
	stats.RecordEvent(coreusage.Record{
		Model:       "recent",
		RequestedAt: time.Now().UTC(),
	})

	page := stats.ListUsageEvents(servicedto.UsageFilter{Page: 1, PageSize: 20})
	if page.TotalCount != 1 || len(page.Events) != 1 || page.Events[0].Model != "recent" {
		t.Fatalf("unexpected retained events: %+v", page)
	}
	if page.Cache == nil || page.Cache.OversizedDroppedTotal != 1 || page.Cache.EvictedTotal != 1 || !page.Cache.HasOlderEvents {
		t.Fatalf("unexpected rejected event cache info: %+v", page.Cache)
	}
}

func TestRequestStatisticsBuildsEventOverviewFromMemory(t *testing.T) {
	stats := NewRequestStatistics()
	now := time.Now().UTC()
	stats.ReplaceEvents([]servicedto.UsageEventRecord{
		{ID: 1, Timestamp: now.Add(-25 * time.Hour), Source: "old-source", AuthIndex: "old-auth", TotalTokens: 5},
		{ID: 2, Timestamp: now.Add(-2 * time.Hour), Source: "source-a", AuthIndex: "auth-a", TotalTokens: 10},
		{ID: 3, Timestamp: now.Add(-time.Hour), Source: "source-a", AuthIndex: "auth-b", Failed: true, TotalTokens: 20},
	})
	startTime := now.Add(-24 * time.Hour)
	endTime := now
	keyStats, health, cacheInfo := stats.UsageEventOverview(servicedto.UsageFilter{
		Range:     "24h",
		StartTime: &startTime,
		EndTime:   &endTime,
	})

	source := keyStats.BySource["source-a"]
	if source.Success != 1 || source.Failure != 1 || source.Tokens != 30 {
		t.Fatalf("unexpected source key stats: %+v", source)
	}
	if len(keyStats.ByAuthIndex) != 2 || len(keyStats.Credentials) != 2 {
		t.Fatalf("unexpected credential key stats: %+v", keyStats)
	}
	if health.TotalSuccess != 1 || health.TotalFailure != 1 || health.SuccessRate != 50 {
		t.Fatalf("unexpected service health totals: %+v", health)
	}
	if len(health.BlockDetails) != usageEventHealthRows*usageEventHealthColumns {
		t.Fatalf("unexpected service health blocks: %d", len(health.BlockDetails))
	}
	populated := int64(0)
	for _, block := range health.BlockDetails {
		populated += block.Success + block.Failure
	}
	if populated != 2 || cacheInfo.RetainedCount != 3 {
		t.Fatalf("unexpected service health/cache coverage: populated=%d cache=%+v", populated, cacheInfo)
	}
}

func TestRequestStatisticsConcurrentEventReadsAndWrites(t *testing.T) {
	stats := NewRequestStatistics()
	baseTime := time.Now().UTC()
	var workers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		worker := worker
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < 300; i++ {
				stats.RecordEvent(coreusage.Record{
					Model:       fmt.Sprintf("model-%d", worker),
					RequestedAt: baseTime.Add(time.Duration(worker*300+i) * time.Nanosecond),
				})
			}
		}()
	}
	for reader := 0; reader < 4; reader++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < 200; i++ {
				stats.ListUsageEvents(servicedto.UsageFilter{Page: 1, PageSize: 20})
				stats.ListUsageEventFilterOptions(servicedto.UsageFilter{})
				stats.UsageEventCacheInfo()
			}
		}()
	}
	workers.Wait()

	page := stats.ListUsageEvents(servicedto.UsageFilter{Page: 1, PageSize: 20})
	if page.Cache == nil || page.Cache.RetainedCount > maxInMemoryUsageEvents || page.TotalCount != int64(page.Cache.RetainedCount) {
		t.Fatalf("unexpected concurrent event cache: %+v", page)
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
