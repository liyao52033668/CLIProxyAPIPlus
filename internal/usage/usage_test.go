package usage

import (
	"context"
	"fmt"
	"math"
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

func TestRequestStatisticsBuildsRangeOverviewFromMemoryBuckets(t *testing.T) {
	stats := NewRequestStatistics()
	plugin := &requestStatisticsPlugin{stats: stats}
	anchor := time.Now().UTC().Truncate(time.Hour).Add(30 * time.Minute)
	plugin.HandleUsage(context.Background(), coreusage.Record{
		APIKey:      "group-a",
		Model:       "model-a",
		RequestedAt: anchor.Add(-26 * time.Hour),
		Detail:      coreusage.Detail{InputTokens: 70, OutputTokens: 30},
	})
	plugin.HandleUsage(context.Background(), coreusage.Record{
		APIKey:      "group-a",
		Model:       "model-a",
		RequestedAt: anchor.Add(-2 * time.Hour),
		Detail:      coreusage.Detail{InputTokens: 6, OutputTokens: 2, ReasoningTokens: 1, CachedTokens: 1},
	})
	plugin.HandleUsage(context.Background(), coreusage.Record{
		APIKey:      "group-a",
		Model:       "model-b",
		RequestedAt: anchor.Add(-time.Hour),
		Failed:      true,
		Detail:      coreusage.Detail{InputTokens: 10, OutputTokens: 10},
	})

	start := anchor.Add(-4 * time.Hour)
	overview := stats.UsageOverview(servicedto.UsageFilter{Range: "4h", StartTime: &start, EndTime: &anchor})
	if overview.Usage.TotalRequests != 2 || overview.Usage.SuccessCount != 1 || overview.Usage.FailureCount != 1 || overview.Usage.TotalTokens != 30 {
		t.Fatalf("unexpected 4h totals: %+v", overview.Usage)
	}
	if overview.Summary.RequestCount != 0 || overview.Summary.TokenCount != 0 || overview.Summary.WindowMinutes != 30 || overview.Summary.CachedTokens != 1 || overview.Summary.ReasoningTokens != 1 {
		t.Fatalf("unexpected 4h summary: %+v", overview.Summary)
	}
	if len(overview.HourlySeries.Requests) != 5 || len(overview.DailySeries.Requests) == 0 {
		t.Fatalf("unexpected range series: hourly=%v daily=%v", overview.HourlySeries.Requests, overview.DailySeries.Requests)
	}
	if overview.HourlySeries.Requests[anchor.Add(-2*time.Hour).Truncate(time.Hour).Format(time.RFC3339)] != 1 {
		t.Fatalf("missing recent hourly point: %v", overview.HourlySeries.Requests)
	}
	model := overview.Usage.APIs["group-a"].Models["model-a"]
	if model.TotalRequests != 1 || model.InputTokens != 6 || model.OutputTokens != 2 || model.TotalTokens != 10 {
		t.Fatalf("unexpected filtered model totals: %+v", model)
	}

	all := stats.UsageOverview(servicedto.UsageFilter{Range: "all"})
	if all.Usage.TotalRequests != 3 || all.Usage.TotalTokens != 130 {
		t.Fatalf("unexpected all-time totals: %+v", all.Usage)
	}
	if len(all.DailySeries.Requests) < 2 || len(all.Series.Requests) < 2 {
		t.Fatalf("unexpected all-time series: series=%v daily=%v", all.Series.Requests, all.DailySeries.Requests)
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
			Source:       "source-a",
			AuthIndex:    "auth-a",
			LatencyMS:    120,
			InputTokens:  12,
			OutputTokens: 4,
			TotalTokens:  16,
		},
		{
			EventKey:     "event-failure",
			APIGroupKey:  "group-a",
			Model:        "model-a",
			Timestamp:    requestedAt.Add(time.Minute),
			Source:       "source-a",
			AuthIndex:    "auth-b",
			Failed:       true,
			LatencyMS:    180,
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
	if model.InputTokens != 15 || model.OutputTokens != 5 || model.TotalTokens != 20 || model.TotalLatencyMS != 300 || model.LatencySampleCount != 2 || len(model.Hourly) == 0 {
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
	keyStats, health, _ := stats.UsageEventOverview(servicedto.UsageFilter{})
	if keyStats.BySource["source-a"].Success != 1 || keyStats.BySource["source-a"].Failure != 1 || len(keyStats.Credentials) != 2 {
		t.Fatalf("unexpected restored credential stats: %+v", keyStats)
	}
	for _, credential := range keyStats.Credentials {
		if credential.Model != "model-a" || credential.InputTokens <= 0 || credential.OutputTokens <= 0 {
			t.Fatalf("missing restored credential token breakdown: %+v", credential)
		}
	}
	if health.TotalSuccess != 1 || health.TotalFailure != 1 {
		t.Fatalf("unexpected restored health: %+v", health)
	}
}

func TestRestoreRequestStatisticsBuildsQuarterHourHealthFromMinuteBuckets(t *testing.T) {
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

	bucketStart := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	successCounts := []int{62, 62, 62, 63}
	failureCounts := []int{47, 48, 47, 48}
	events := make([]entities.UsageEvent, 0, 439)
	for quarter := range successCounts {
		timestamp := bucketStart.Add(time.Duration(quarter)*15*time.Minute + time.Minute)
		for i := 0; i < successCounts[quarter]; i++ {
			events = append(events, entities.UsageEvent{
				EventKey:  fmt.Sprintf("quarter-%d-success-%d", quarter, i),
				Timestamp: timestamp,
			})
		}
		for i := 0; i < failureCounts[quarter]; i++ {
			events = append(events, entities.UsageEvent{
				EventKey:  fmt.Sprintf("quarter-%d-failure-%d", quarter, i),
				Timestamp: timestamp,
				Failed:    true,
			})
		}
	}
	if inserted, _, errInsert := repository.InsertUsageEvents(db, events); errInsert != nil || inserted != len(events) {
		t.Fatalf("insert usage events: inserted=%d err=%v", inserted, errInsert)
	}

	stats := NewRequestStatistics()
	if err = RestoreRequestStatistics(context.Background(), db, stats); err != nil {
		t.Fatalf("restore request statistics: %v", err)
	}
	_, health, _ := stats.UsageEventOverview(servicedto.UsageFilter{})
	if health.TotalSuccess != 249 || health.TotalFailure != 190 {
		t.Fatalf("unexpected restored health totals: %+v", health)
	}
	if health.WindowStart.Minute()%15 != 0 || health.WindowStart.Second() != 0 || health.WindowStart.Nanosecond() != 0 {
		t.Fatalf("health window is not quarter-hour aligned: %s", health.WindowStart)
	}
	populated := 0
	for _, block := range health.BlockDetails {
		total := block.Success + block.Failure
		if total == 0 {
			continue
		}
		populated++
		if total == int64(len(events)) {
			t.Fatalf("hourly counts collapsed into one health block: %+v", block)
		}
	}
	if populated != 4 {
		t.Fatalf("expected four populated quarter-hour blocks, got %d", populated)
	}
}

func TestRestoreRequestStatisticsBuildsRatesBeyondEventCacheLimit(t *testing.T) {
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

	const eventCount = maxInMemoryUsageEvents + 105
	requestedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	events := make([]entities.UsageEvent, eventCount)
	for i := range events {
		events[i] = entities.UsageEvent{
			EventKey:     fmt.Sprintf("rate-event-%d", i),
			APIGroupKey:  "group-a",
			Model:        "model-a",
			Timestamp:    requestedAt,
			InputTokens:  2,
			OutputTokens: 1,
			TotalTokens:  3,
		}
	}
	if inserted, _, errInsert := repository.InsertUsageEvents(db, events); errInsert != nil || inserted != eventCount {
		t.Fatalf("insert usage events: inserted=%d err=%v", inserted, errInsert)
	}

	stats := NewRequestStatistics()
	if err = RestoreRequestStatistics(context.Background(), db, stats); err != nil {
		t.Fatalf("restore request statistics: %v", err)
	}
	overview := stats.UsageOverview(servicedto.UsageFilter{Range: "24h"})
	if overview.Summary.RequestCount != eventCount || overview.Summary.TokenCount != eventCount*3 {
		t.Fatalf("unexpected restored rate summary: %+v", overview.Summary)
	}
	page := stats.ListUsageEvents(servicedto.UsageFilter{Page: 1, PageSize: 20})
	if page.TotalCount > maxInMemoryUsageEvents || page.Cache == nil || !page.Cache.HasOlderEvents {
		t.Fatalf("unexpected bounded event cache: %+v", page)
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
	stats.record("api-a", "model-a", "expired-source", "expired-auth", now.Add(-8*24*time.Hour), false, 7, 0, 0, 0, 7, 10)
	stats.record("api-a", "model-a", "old-source", "old-auth", now.Add(-25*time.Hour), false, 5, 0, 0, 0, 5, 10)
	stats.record("api-a", "model-a", "source-a", "auth-a", now.Add(-2*time.Hour), false, 10, 0, 0, 0, 10, 20)
	stats.record("api-a", "model-a", "source-a", "auth-b", now.Add(-time.Hour), true, 20, 0, 0, 0, 20, 30)
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
	if credential := keyStats.Credentials[0]; credential.Model != "model-a" || credential.InputTokens != 10 || credential.Tokens != 10 {
		t.Fatalf("unexpected compact credential tokens: %+v", credential)
	}
	if health.TotalSuccess != 2 || health.TotalFailure != 1 || math.Abs(health.SuccessRate-200.0/3.0) > 1e-9 {
		t.Fatalf("unexpected service health totals: %+v", health)
	}
	if len(health.BlockDetails) != usageEventHealthRows*usageEventHealthColumns || health.BucketSeconds != 15*60 {
		t.Fatalf("unexpected service health grid: %+v", health)
	}
	if health.WindowEnd.Sub(health.WindowStart) != 7*24*time.Hour {
		t.Fatalf("unexpected service health window: %+v", health)
	}
	populated := int64(0)
	for _, block := range health.BlockDetails {
		populated += block.Success + block.Failure
	}
	if populated != 3 || cacheInfo.RetainedCount != 3 {
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
