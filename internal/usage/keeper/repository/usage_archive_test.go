package repository

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository/dto"
)

func TestCleanupUsageEventsPreservesAggregatesAndRemovesExpiredDetails(t *testing.T) {
	db := openTestDatabase(t)
	now := time.Date(2026, 7, 14, 12, 34, 0, 0, time.UTC)
	events := []entities.UsageEvent{
		{
			EventKey: "old-success", APIGroupKey: "api-a", Provider: "claude", AuthType: "oauth",
			Model: "model-a", Timestamp: now.Add(-40 * 24 * time.Hour), Source: "source-a", AuthIndex: "auth-a",
			LatencyMS: 120, InputTokens: 100, OutputTokens: 20, ReasoningTokens: 5, CachedTokens: 10, TotalTokens: 125,
		},
		{
			EventKey: "old-failure", APIGroupKey: "api-a", Provider: "claude", AuthType: "oauth",
			Model: "model-a", Timestamp: now.Add(-40*24*time.Hour + 10*time.Minute), Source: "source-a", AuthIndex: "auth-a",
			Failed: true, LatencyMS: 80, InputTokens: 50, OutputTokens: 5, TotalTokens: 55,
		},
		{
			EventKey: "recent-success", APIGroupKey: "api-b", Provider: "openai", AuthType: "apikey",
			Model: "model-b", Timestamp: now.Add(-time.Hour), Source: "source-b", AuthIndex: "auth-b",
			LatencyMS: 60, InputTokens: 30, OutputTokens: 10, CachedTokens: 3, TotalTokens: 40,
		},
	}
	if inserted, deduped, err := InsertUsageEvents(db, events); err != nil || inserted != 3 || deduped != 0 {
		t.Fatalf("InsertUsageEvents = (%d, %d, %v), want (3, 0, nil)", inserted, deduped, err)
	}

	start := now.Add(-60 * 24 * time.Hour)
	filter := dto.UsageQueryFilter{Range: "all", StartTime: &start, EndTime: &now}
	beforeSnapshot, err := BuildUsageAggregateSnapshotWithFilter(db, filter)
	if err != nil {
		t.Fatalf("BuildUsageAggregateSnapshotWithFilter before cleanup: %v", err)
	}
	beforeOverview, err := BuildUsageOverviewWithFilter(db, filter)
	if err != nil {
		t.Fatalf("BuildUsageOverviewWithFilter before cleanup: %v", err)
	}
	beforeAPIs, beforeModels, err := ListUsageAnalysisWithFilter(db, filter)
	if err != nil {
		t.Fatalf("ListUsageAnalysisWithFilter before cleanup: %v", err)
	}

	result, err := CleanupUsageEvents(db, now)
	if err != nil {
		t.Fatalf("CleanupUsageEvents returned error: %v", err)
	}
	if result.ArchivedEvents != 2 || result.UpdatedAggregates != 1 {
		t.Fatalf("unexpected cleanup result: %+v", result)
	}

	var eventCount int64
	if err := db.Model(&entities.UsageEvent{}).Count(&eventCount).Error; err != nil {
		t.Fatalf("count remaining usage events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("expected one recent usage event, got %d", eventCount)
	}
	var aggregateCount int64
	if err := db.Model(&entities.UsageHourlyAggregate{}).Count(&aggregateCount).Error; err != nil {
		t.Fatalf("count usage aggregates: %v", err)
	}
	if aggregateCount != 1 {
		t.Fatalf("expected one hourly aggregate, got %d", aggregateCount)
	}

	afterSnapshot, err := BuildUsageAggregateSnapshotWithFilter(db, filter)
	if err != nil {
		t.Fatalf("BuildUsageAggregateSnapshotWithFilter after cleanup: %v", err)
	}
	if !reflect.DeepEqual(beforeSnapshot, afterSnapshot) {
		t.Fatalf("aggregate snapshot changed after cleanup\nbefore: %#v\nafter:  %#v", beforeSnapshot, afterSnapshot)
	}
	afterOverview, err := BuildUsageOverviewWithFilter(db, filter)
	if err != nil {
		t.Fatalf("BuildUsageOverviewWithFilter after cleanup: %v", err)
	}
	if !reflect.DeepEqual(beforeOverview.Usage, afterOverview.Usage) ||
		!reflect.DeepEqual(beforeOverview.Summary, afterOverview.Summary) ||
		!reflect.DeepEqual(beforeOverview.Series, afterOverview.Series) ||
		!reflect.DeepEqual(beforeOverview.DailySeries, afterOverview.DailySeries) ||
		!reflect.DeepEqual(beforeOverview.KeyStats, afterOverview.KeyStats) {
		t.Fatalf("usage overview changed after cleanup\nbefore: %#v\nafter:  %#v", beforeOverview, afterOverview)
	}
	afterAPIs, afterModels, err := ListUsageAnalysisWithFilter(db, filter)
	if err != nil {
		t.Fatalf("ListUsageAnalysisWithFilter after cleanup: %v", err)
	}
	if !reflect.DeepEqual(beforeAPIs, afterAPIs) || !reflect.DeepEqual(beforeModels, afterModels) {
		t.Fatalf("usage analysis changed after cleanup\nbefore: %#v %#v\nafter:  %#v %#v", beforeAPIs, beforeModels, afterAPIs, afterModels)
	}

	page, err := ListUsageEventsWithFilter(db, dto.UsageQueryFilter{Page: 1, PageSize: 100})
	if err != nil {
		t.Fatalf("ListUsageEventsWithFilter returned error: %v", err)
	}
	if page.TotalCount != 1 || len(page.Events) != 1 || page.Events[0].Model != "model-b" {
		t.Fatalf("expected only recent event details, got %+v", page)
	}

	inserted, deduped, err := InsertUsageEvents(db, events[:1])
	if err != nil {
		t.Fatalf("reinsert archived event: %v", err)
	}
	if inserted != 0 || deduped != 1 {
		t.Fatalf("expected archived event key to remain deduplicated, got inserted=%d deduped=%d", inserted, deduped)
	}
	second, err := CleanupUsageEvents(db, now)
	if err != nil {
		t.Fatalf("second CleanupUsageEvents returned error: %v", err)
	}
	if second.ArchivedEvents != 0 || second.UpdatedAggregates != 0 {
		t.Fatalf("expected idempotent cleanup, got %+v", second)
	}
}

func TestUsageAggregateQueryWindowIncludesOnlyCompleteBuckets(t *testing.T) {
	db := openTestDatabase(t)
	aggregates := []entities.UsageHourlyAggregate{
		{BucketStart: time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC), APIGroupKey: "api-a", Model: "model-a", RequestCount: 10, SuccessCount: 10},
		{BucketStart: time.Date(2026, 7, 14, 11, 0, 0, 0, time.UTC), APIGroupKey: "api-a", Model: "model-a", RequestCount: 20, SuccessCount: 20},
	}
	if _, _, err := InsertUsageHourlyAggregates(db, aggregates); err != nil {
		t.Fatalf("InsertUsageHourlyAggregates returned error: %v", err)
	}

	start := time.Date(2026, 7, 14, 10, 30, 0, 0, time.UTC)
	end := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	snapshot, err := BuildUsageAggregateSnapshotWithFilter(db, dto.UsageQueryFilter{StartTime: &start, EndTime: &end})
	if err != nil {
		t.Fatalf("BuildUsageAggregateSnapshotWithFilter returned error: %v", err)
	}
	if snapshot.TotalRequests != 20 {
		t.Fatalf("expected only complete 11:00 bucket, got %+v", snapshot)
	}

	start = time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	end = time.Date(2026, 7, 14, 11, 30, 0, 0, time.UTC)
	snapshot, err = BuildUsageAggregateSnapshotWithFilter(db, dto.UsageQueryFilter{StartTime: &start, EndTime: &end})
	if err != nil {
		t.Fatalf("BuildUsageAggregateSnapshotWithFilter returned error: %v", err)
	}
	if snapshot.TotalRequests != 10 {
		t.Fatalf("expected only complete 10:00 bucket, got %+v", snapshot)
	}
}

func TestInsertUsageHourlyAggregatesRejectsDuplicateKeys(t *testing.T) {
	db := openTestDatabase(t)
	aggregate := entities.UsageHourlyAggregate{
		BucketStart:  time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC),
		APIGroupKey:  "api-a",
		Model:        "model-a",
		RequestCount: 1,
		SuccessCount: 1,
	}
	_, _, err := InsertUsageHourlyAggregates(db, []entities.UsageHourlyAggregate{aggregate, aggregate})
	if !errors.Is(err, ErrDuplicateUsageAggregate) {
		t.Fatalf("expected ErrDuplicateUsageAggregate, got %v", err)
	}
}

func TestInsertUsageHourlyAggregatesRejectsLiveEventOverlap(t *testing.T) {
	db := openTestDatabase(t)
	bucketStart := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	if _, _, err := InsertUsageEvents(db, []entities.UsageEvent{{
		EventKey: "event-a", APIGroupKey: "api-a", Provider: "claude", AuthType: "oauth",
		Model: "model-a", Source: "source-a", AuthIndex: "auth-a", Timestamp: bucketStart.Add(15 * time.Minute),
	}}); err != nil {
		t.Fatalf("InsertUsageEvents returned error: %v", err)
	}
	aggregate := entities.UsageHourlyAggregate{
		BucketStart: bucketStart, APIGroupKey: "api-a", Provider: "claude", AuthType: "oauth",
		Model: "model-a", Source: "source-a", AuthIndex: "auth-a", RequestCount: 1, SuccessCount: 1,
	}
	_, _, err := InsertUsageHourlyAggregates(db, []entities.UsageHourlyAggregate{aggregate})
	if !errors.Is(err, ErrUsageAggregateOverlapsEvent) {
		t.Fatalf("expected ErrUsageAggregateOverlapsEvent, got %v", err)
	}
}

func TestCleanupUsageEventsExpiresOldDeduplicationKeys(t *testing.T) {
	db := openTestDatabase(t)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	if _, _, err := InsertUsageEvents(db, []entities.UsageEvent{{
		EventKey: "expired-key", Timestamp: now.Add(-100 * 24 * time.Hour),
	}}); err != nil {
		t.Fatalf("InsertUsageEvents returned error: %v", err)
	}
	result, err := CleanupUsageEvents(db, now)
	if err != nil {
		t.Fatalf("CleanupUsageEvents returned error: %v", err)
	}
	if result.ArchivedEvents != 1 || result.DeletedEventKeys != 1 {
		t.Fatalf("unexpected cleanup result: %+v", result)
	}
	var keyCount int64
	if err := db.Model(&entities.UsageEventKey{}).Count(&keyCount).Error; err != nil {
		t.Fatalf("count usage event keys: %v", err)
	}
	if keyCount != 0 {
		t.Fatalf("expected expired event key to be removed, got %d", keyCount)
	}
}
